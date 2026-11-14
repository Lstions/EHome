// EHomeSystem main.go
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"ehome/backend/internal/alert"
	"ehome/backend/internal/api"
	authservice "ehome/backend/internal/auth"
	"ehome/backend/internal/automation"
	"ehome/backend/internal/bootstrap"
	"ehome/backend/internal/commandexec"
	"ehome/backend/internal/config"
	"ehome/backend/internal/database"
	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/datasource"
	"ehome/backend/internal/deviceaction"
	"ehome/backend/internal/downlink"
	"ehome/backend/internal/drivers"
	"ehome/backend/internal/events"
	"ehome/backend/internal/models"
	"ehome/backend/internal/nodemgr"
	"ehome/backend/internal/notify"
	"ehome/backend/internal/offlinedetector"
	"ehome/backend/internal/ota"
	"ehome/backend/internal/seed"
	"ehome/backend/internal/sourceref"
	"ehome/backend/internal/telemetry"
	"ehome/backend/internal/transport"
	"ehome/backend/internal/websocket"
	"ehome/backend/pkg/logger"
	"ehome/backend/pkg/parser"
	"encoding/json"

	"github.com/gin-contrib/cors"
	"github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"

	"gorm.io/gorm"
)

// buildCommit 由构建期注入（Dockerfile 的 -ldflags -X main.buildCommit=<sha>）。
//
// 为什么需要它（2026-10-21 生产事故）：那次部署的镜像"不是从 main 构建"，
// 导致已修复的解析器语义被回退，生产写入了 91 分钟的错误字段。
// 根因不是代码缺陷，而是**部署产物来源不可追溯** —— 无法在部署时判断
// 这个镜像到底是哪份代码。把 commit 烙进二进制后，/health 直接回显，
// "部署的到底是哪份代码"变成可查询事实，而不是靠 strings 事后推断。
var buildCommit = "unknown"

func main() {
	cfg := config.Load()

	if err := logger.Init(cfg.LogLevel()); err != nil {
		panic("failed to init logger: " + err.Error())
	}
	defer logger.Sync()

	logger.Infof("EHomeSystem Server v2.0 starting...")

	// Validate JWT secret is not default in production
	// 构建来源转发给 api 层，供 /health 回显（2026-10-21 事故教训：镜像必须能自证来源）。
	api.SetBuildCommit(buildCommit)
	api.ValidateJWTSecret()
	logger.Infof("Config: DB=%s:%d/%s, API=%s, device_listener=%v",
		cfg.DBConfig().Host, cfg.DBConfig().Port, cfg.DBConfig().DBName, cfg.APIAddr(), cfg.Device.Enabled)

	// ════════════════════════════════════════════════════════════════════════
	// 启动进度 + 提前监听（契约 docs/设计/启动期数据库优化与进度展示-契约-2026-11-06.md §2）
	//
	// 这是本次改造**唯一**的时序变化: 监听提前到 database.Connect 之前，其余依赖
	// 顺序一律不动。为什么必须这样 —— 下面这段是数据库连接 + 一串分钟级 DDL
	// （补建父表索引实测 +4.51 GB / 69.6M 行；DROP 死索引取 ACCESS EXCLUSIVE）。
	// 改造前全程同步阻塞，端口**根本没有监听**，浏览器只看到连接失败/网关超时，
	// 用户无法区分"正在升级"与"服务挂了"。
	//
	// 三件事按序做:
	//   1) 建进度状态机（DefaultSteps 与契约 §3.1 的 10 个步骤逐字对应）；
	//   2) 把 datalifecycle 的迁移埋点接到同一台状态机上（月/批次级细粒度进度由它驱动，
	//      main 不重复实现埋点 —— 契约 §3.2 要求埋点只在真正耗时处，都由 datalifecycle 上报）；
	//   3) 用「维护引擎 + RootHandler」起 http.Server，早于数据库连接。
	// ════════════════════════════════════════════════════════════════════════
	startupTracker := bootstrap.NewTracker(bootstrap.DefaultSteps())
	startupTracker.SetBuildCommit(buildCommit)
	progressAdapter := &startupProgressAdapter{tracker: startupTracker, touched: map[string]bool{}}
	datalifecycle.SetMigrationProgress(progressAdapter)

	// 静态资源目录**只读一次**：维护引擎（此刻）与全量引擎（装配完成后）必须服务
	// 同一份产物。两处各自 os.Getenv 会让"维护页能开、正式页 404"这类问题变得可能。
	staticDir := os.Getenv("EHOME_STATIC_DIR")

	// gin 的输出模式必须在**任何引擎构造之前**定：维护引擎先于 r 创建，若沿用
	// debug 模式，生产启动会多出成百上千行 [GIN-debug] 路由注册日志（r 的路由在
	// 启动末尾注册，那些日志对排障毫无价值）。
	gin.SetMode(gin.ReleaseMode)

	// 维护引擎：只做健康探针 + 进度接口 + 静态资源 + 维护态 503（见 api/maintenance.go）。
	// 进度源用函数适配（api 包刻意不 import bootstrap，见该文件顶部的镜像类型说明）。
	maintenanceEngine := api.NewMaintenanceEngine(api.MaintenanceSourceFunc(func() api.MaintenanceSnapshot {
		return maintenanceSnapshotFromTracker(startupTracker)
	}), staticDir)

	// rootHandler: nil 全量引擎 ⇒ 维护引擎；SetEngine 一次原子写入 ⇒ 全量引擎。
	// 绝不在 Serve 期间 AddRoute（gin 的路由树没有读锁，那是数据竞争）。
	rootHandler := api.NewRootHandler(maintenanceEngine)

	srv := &http.Server{
		Addr:    cfg.APIAddr(),
		Handler: rootHandler,
	}

	// httpListenGate 把「起监听」与「何时真正 Accept」解耦（只为 §3.4 的排障开关）：
	//   · 默认（维护页开启）⇒ 立刻 close ⇒ 立即监听，请求全部由维护引擎应答；
	//   · EHOME_STARTUP_MAINTENANCE_PAGE=false ⇒ goroutine 在此阻塞，等全量路由装配
	//     完成后再 close ⇒ 与改造前"先做完再监听"完全等价（排障时用来隔离变量）。
	httpListenGate := make(chan struct{})
	// listenOnce 保证门只关一次：默认路径在下面立刻关，排障路径在装配完成后关。
	// 用 Once 而不是裸 close —— 两处 close 撞车时的 panic 会发生在启动最早期，
	// 现场只有一行 "close of closed channel"，极难定位。
	var listenOnce sync.Once
	listenNow := func() { listenOnce.Do(func() { close(httpListenGate) }) }
	go func() {
		<-httpListenGate
		logger.Infof("API server listening on %s", cfg.APIAddr())
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatalf("API server failed: %v", err)
		}
	}()
	if startupMaintenancePageEnabled() {
		listenNow()
		logger.Infof("startup maintenance page ENABLED: listening on %s BEFORE database connect (progress: GET /api/v1/system/maintenance)", cfg.APIAddr())
	} else {
		logger.Warnf("EHOME_STARTUP_MAINTENANCE_PAGE=false: 回退旧行为 —— 启动完成前不监听（仅供排障）")
	}
	// 可选的启动看门狗（默认关闭，EHOME_STARTUP_FAIL_FAST_SECONDS=0）。
	startupFailFast(startupTracker)

	dbCfg := cfg.DBConfig()
	startupTracker.Begin(bootstrap.StepDBConnect)
	if err := database.Connect(database.Config{
		Host:     dbCfg.Host,
		Port:     dbCfg.Port,
		User:     dbCfg.User,
		Password: dbCfg.Password,
		DBName:   dbCfg.DBName,
		SSLMode:  dbCfg.SSLMode,
	}); err != nil {
		startupTracker.Fail(bootstrap.StepDBConnect, err)
		abortStartup("Failed to connect database: %v", err)
	}
	startupTracker.Done(bootstrap.StepDBConnect, fmt.Sprintf("%s:%d/%s", dbCfg.Host, dbCfg.Port, dbCfg.DBName))

	startupTracker.Begin(bootstrap.StepAutoMigrate)
	if err := database.AutoMigrate(); err != nil {
		startupTracker.Fail(bootstrap.StepAutoMigrate, err)
		abortStartup("Failed to migrate database: %v", err)
	}
	startupTracker.Done(bootstrap.StepAutoMigrate, "表结构已同步")
	logger.Infof("Database connected and migrated")

	// 数据层时序化 (方案 v3.4 §3.2.1): 时序表分区迁移 + 滚动分区保障。
	// （2026-11-08 unified_data、2026-11-11 device_data 两次整表/迁移退役后，
	//  这一带只剩 telemetry_frame 与 telemetry_rollup_1m 的迁移。）
	// 迁移失败即 Fatal 中止启动, 不再静默降级为"继续用平表":
	// 失败路径已保证源平表与 legacy/_new 表完整 (安全门禁在行数校验前不 swap),
	// 且搬迁幂等 (主键 (id,timestamp) + ON CONFLICT), 修好后重跑即可收敛;
	// 而"带着未完成的迁移继续跑"会让后半段行为与预期不符且极难排查。
	// 非 PG 方言 (SQLite 测试) 下两个迁移函数均为 no-op 返回 nil, 不受影响。
	db := database.GetDB()
	// 剩余时序表的迁移各自 Begin/…/Done。注意 parent_indexes 与 retire_dead_indexes
	// **不是独立调用**：它们嵌在 MigrateTableToPartitioned 内部（幂等补齐路径
	// partition_mgr.go: finalizePartitionParentIndexes）。因此这里只能"外面包一层"，
	// 细粒度进度（第几条索引 / 第几条 DROP）由 datalifecycle 经
	// SetMigrationProgress 回调上报 —— main 不重复实现埋点（契约 §3.2）。
	//
	// ⚠ 2026-11-08 起 unified_data **不在这个清单里**（见下方说明）。
	// ============ unified_data 不再迁移（2026-11-08 硬切换）============
	//
	// 这里原本是：
	//
	//	startupTracker.Begin(bootstrap.StepPartitionUnifiedData)
	//	if err := datalifecycle.MigrateUnifiedDataToPartitioned(db); err != nil { ... abortStartup }
	//	startupTracker.Done(bootstrap.StepPartitionUnifiedData, "unified_data 分区就绪")
	//
	// 删掉它有两个理由，第二个才是决定性的：
	//
	//  ① 该表已整表退役（2026-11-13：写入/读取/回填删除，运维用
	//     ehomectl retire-unified-data 显式 DROP），不再产生新数据，分区维护没有意义；
	//
	//  ② **更危险的是它会把 DROP 掉的表重新建出来**。MigrateTableToPartitioned 的
	//     判定链是：IsTablePartitioned(relkind='p') → 否；flatExists(relkind='r') → 否；
	//     于是走「原表不存在（全新部署）→ createPartitionedParent」这条分支。
	//     代码无法区分「全新部署」与「我们刚把表 DROP 了」——
	//     ⇒ DROP 之后每次重启都会得到一张**空的** unified_data（5 个月分区、0 行）。
	//     磁盘没省，数据没了，而 `pg_class` 里它看起来一切正常。
	//
	// 一次性测试证明这条分支真实存在（走的就是 DROP 之后的路径）：
	//
	//	TestMigrateUnifiedData_FreshDeploy_PartitionedParentAtFinalName
	//	  INFO migrate_partitioned: no legacy flat table, creating fresh partitioned parent
	//	  --- PASS
	//
	// 保留 MigrateUnifiedDataToPartitioned 函数本身（partition_mgr 的泛化入口，
	// device_data / telemetry_frame 仍在用同一套机制），只是不再为 unified_data 调用。
	//
	// 同时从 bootstrap.DefaultSteps 移除 StepPartitionUnifiedData，其 22 权重按比例
	// 分给剩余步骤（合计仍必须 100，TestDefaultStepsTable 守着）。
	// ============ device_data 分区迁移已删除（2026-11-11 整表退役）============
	//
	// 这里原本是：
	//
	//	startupTracker.Begin(bootstrap.StepPartitionDeviceData)
	//	if err := datalifecycle.MigrateTableToPartitioned(db, "device_data", "device_data_legacy"); err != nil { ... abortStartup }
	//	startupTracker.Done(bootstrap.StepPartitionDeviceData, "device_data 分区就绪")
	//
	// 删除理由与 unified_data 2026-11-08 那一次**完全相同**（见下方长注释的第 ② 条）：
	// 表已经整表退役（写点/读点全删，DROP 后不存在），而 MigrateTableToPartitioned
	// 的判定链 IsTablePartitioned→否 / flatExists→否 会走「全新部署 ⇒ 建空分区母表」
	// 分支，**代码无法区分「全新部署」与「刚 DROP」** ⇒ 每次重启都会把 device_data
	// 重新建出来（0 行、5 个月空分区）。磁盘没省、DROP 白做，而 pg_class 里它看起来
	// 一切正常 —— 那正好抵消本次改造的全部收益。
	//
	// 因此这一步（bootstrap.StepPartitionDeviceData）从 DefaultSteps 也一并删除，
	// 权重按原相对比例分给剩余步骤（合计仍 100，TestDefaultStepsTable 守着）。
	// ======================================================================

	// L2 帧表 telemetry_frame + slot 字典表 telemetry_slot
	// (设计/遥测存储-L1L2无条件启用-契约-2026-11-07.md §3.4/§3.5)。
	//
	// ==================== 为什么这里是 Fatal（对比下面的 rollup）====================
	// 2026-11-07 用户裁决「L2 无条件启用、删除开关」后，帧表从**影子旁路**变成
	// **主路径之一**：每一帧解析结果都要写进去。此时"表没建但服务照跑"是一条
	// 静默丢数据的路径 —— unified_data 还在写，所有面板都正常，只有帧表空的，
	// 而它要等到下一轮读路径切换/影子校验才会被发现，代价是那一整天的帧数据。
	// 因此迁移失败必须中止启动（与已退役的 unified_data / device_data 同一口径）。
	//
	// 对比：下面 telemetry_rollup_1m 仍是 Warn —— 那是**派生数据**（丢了下一分钟
	// 重新累积），且有 fail-open 回落到原始表。两者语义不同，不要统一。
	//
	// 顺序也重要: telemetry_slot 先建（帧行的 slot_id 指向它），再建分区母表。
	startupTracker.Begin(bootstrap.StepPartitionTelemetryFrame)
	if err := datalifecycle.MigrateTelemetryFrameToPartitioned(db); err != nil {
		startupTracker.Fail(bootstrap.StepPartitionTelemetryFrame, err)
		abortStartup("telemetry_frame partition migration failed (aborting startup: L2 frames are the main write path, a missing frame table would silently drop every frame): %v", err)
	}
	startupTracker.Done(bootstrap.StepPartitionTelemetryFrame, "telemetry_frame 分区就绪")
	// L3 分钟级宽行预聚合表 (设计/遥测存储优化方案-2026-11-06.md §5.2): 建/迁移分区母表。
	//
	// 与上面两张表的关键差别 —— **失败不 Fatal**:
	//   真值来源表 (unified_data / device_data，均已退役) 迁移失败必须中止启动
	//   (半迁移状态会让行为与预期不符且极难排查); 而 telemetry_rollup_1m 是**派生数据** (由原始表
	//   旁路聚合而来, 丢了下一分钟会重新累积), 且读路径对它有 fail-open 回落
	//   (api/rollup_read.go: 表空/坏/不存在 ⇒ 回落原始路径)。为一张派生表挡住整个
	//   服务启动, 是把「预聚合不可用」升级成「服务不可用」—— 方向错了。
	// 因此这里只 Warn, 并明确记录「本进程内 rollup 不可用」的后果。
	startupTracker.Begin(bootstrap.StepPartitionRollup)
	if err := datalifecycle.MigrateTelemetryRollup1mToPartitioned(db); err != nil {
		logger.Warnf("telemetry_rollup_1m partition migration failed (non-fatal: derived data, read path falls back to raw tables): %v", err)
		// 失败仍是 Warn（语义不变），但进度里必须留下痕迹：Skip 计满权重，
		// 否则总体百分比会永远卡在这一步，维护页看起来像卡死。
		startupTracker.Skip(bootstrap.StepPartitionRollup, "非致命失败，读路径回落原始表: "+err.Error())
	} else {
		startupTracker.Done(bootstrap.StepPartitionRollup, "telemetry_rollup_1m 分区就绪")
	}
	// parent_indexes / retire_dead_indexes 的收尾判定（这两步嵌在迁移函数内部，
	// 见上方说明）。判据用 progressAdapter 的"实际被回调过"标记 —— 这是**唯一**能
	// 区分「确实建了索引/删了死索引」与「本就没缺、无事可做」的信号：
	//   · 回调过 ⇒ Done（这一步真的花了时间，维护页应看到它完成）
	//   · 未回调 ⇒ Skip（幂等补齐无事可做；计满权重，否则总进度永远 <100%）
	// 不用"是否 PG"/"是否有缺失"去猜：那等于把 datalifecycle 的探测逻辑重写一遍。
	settleNestedIndexSteps := func() {
		if progressAdapter.wasTouched(bootstrap.StepParentIndexes) {
			startupTracker.Done(bootstrap.StepParentIndexes, "父表索引已补齐")
		} else {
			startupTracker.Skip(bootstrap.StepParentIndexes, "父表索引已齐备（无需补建）")
		}
		if progressAdapter.wasTouched(bootstrap.StepRetireDeadIndexes) {
			startupTracker.Done(bootstrap.StepRetireDeadIndexes, "死索引已退役")
		} else {
			startupTracker.Skip(bootstrap.StepRetireDeadIndexes, "无死索引需要退役")
		}
	}
	settleNestedIndexSteps()

	// 滚动分区保障: 已分区的时序表各自确保 [上月, 未来 3 月] 分区存在。
	// 未分区表 (迁移失败/非 PG) 跳过, 避免对普通表执行 PARTITION OF 报错。
	pm := datalifecycle.NewPartitionManager(db)
	ensurePartitions := func() {
		// 表清单与生命周期清单同源（datalifecycle.TimeseriesTables + rollup）：
		// telemetry_frame 是 L2 主写入路径（2026-11-07 无条件启用），漏掉它会让
		// 「下个月没有分区」在月初静默变成写入失败（PG 分区表无兜底 default 分区）。
		for _, table := range append(timeseriesTableNames(), datalifecycle.TelemetryRollupTable) {
			if !datalifecycle.IsTablePartitioned(db, table) {
				continue
			}
			if err := pm.EnsurePartitionsFor(table, 3); err != nil {
				logger.Errorf("ensure %s partitions failed: %v", table, err)
			}
		}
	}
	startupTracker.Begin(bootstrap.StepEnsurePartitions)
	ensurePartitions()
	startupTracker.Done(bootstrap.StepEnsurePartitions, "滚动分区就绪")

	// startup_tasks 覆盖「后台任务启动」这一整段（purger / retention / notify /
	// 设备传输 / 驱动 / 字段解析 / websocket / ota / offline / alert / automation /
	// source_group …）。它从下面一行开始，到 rootHandler.SetEngine 之前结束：
	// 中途会嵌套 warmup（最新值缓存预热），嵌套是安全的 —— tracker 是每步独立
	// 状态机，没有全局顺序要求。
	startupTracker.Begin(bootstrap.StepStartupTasks)
	// L3 rollup 的保留期（方案 §5.2/§5.4 的硬要求之一）。
	//
	// 为什么必须在这里接: 旧 rollup 表 (unified_data_rollup_1m) 退役理由之一是
	// 「无界增长的第二张表」。新表若不配保留期，就是同一个问题的复刻。
	// 实现复用统一窗口 (datalifecycle.SystemRetentionDays = EHOME_DATA_RETENTION_DAYS)，
	// 且走「整月分区 DROP」这条 O(1) 路径 —— 与 unified_data/device_data 同款
	//（两张表已分别于 2026-11-08 / 2026-11-11 退役，机制不变，受益者只剩帧表与 rollup）。
	//
	// 为什么不挂进 RetentionTask（那里的表清单由 retention_task.go 维护）:
	// 本任务不动该文件（并行改动区），改为在此按同一
	// 保留期语义独立驱动；两者都用 datalifecycle.SystemRetentionDays() 取值，口径一致。
	// 失败只 Warn: 派生数据的清理失败不该阻断服务（分区 DROP 是幂等的，下一轮重试）。
	reapRollupPartitions := func() {
		days := datalifecycle.SystemRetentionDays()
		if days <= 0 {
			return
		}
		cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
		dropped, err := datalifecycle.DropTelemetryRollup1mPartitionsBefore(db, cutoff)
		if err != nil {
			logger.Warnf("telemetry_rollup_1m retention: drop expired partitions failed (will retry next tick): %v", err)
			return
		}
		if len(dropped) > 0 {
			logger.Infof("telemetry_rollup_1m retention: dropped expired partitions %v (retention_days=%d)", dropped, days)
		}
	}
	reapRollupPartitions()
	// 每日滚动检查: 创建下月分区 + 回收到期 rollup 分区
	//（telemetry_frame 的到期分区由 retention_task 触发 DROP）。
	partitionStop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-partitionStop:
				return
			case <-ticker.C:
				ensurePartitions()
				reapRollupPartitions()
			}
		}
	}()
	// 与其他后台任务同序收尾。
	defer close(partitionStop)

	// 注: 数据层时序化的 rollup 分钟聚合表 (EnsureRollupTable) 已于 2026-09-15 退役,
	// 其建表调用点在此删除; 表本身由 database.AutoMigrate() 尾部幂等 DROP。
	// 裁决: docs/分析/rollup-退役裁决-2026-09-15.md。

	// v3.0: One-time idempotent migration of old GPIO channels → gpio_configs
	if migrateResult, err := database.MigrateGPIOChannels(database.GetDB()); err != nil {
		logger.Warnf("GPIO channel migration failed (non-fatal): %v", err)
	} else if migrateResult.Migrated > 0 {
		logger.Infof("GPIO channel migration: %d migrated, %d skipped, %d errors",
			migrateResult.Migrated, migrateResult.Skipped, migrateResult.Errors)
		for _, w := range migrateResult.Warnings {
			logger.Warnf("GPIO migration: %s", w)
		}
	}

	// db 已在上方分区迁移处获取 (database.GetDB())。

	// 系统级数据保留期 (EHOME_DATA_RETENTION_DAYS，默认 90 天)。
	//
	// 2026-10-23 逻辑设备层移除后，保留期不再按「身份」配置与快照 —— 它是
	// 一条系统级策略：全库同一个时间窗，每日任务按月 DROP 分区 + 按 timestamp
	// 精确分批删除过期行（见 datalifecycle/retention_task.go）。
	datalifecycle.SetSystemRetentionDays(cfg.DataRetentionDays())

	// 设备数据清除任务：处理 edge_devices.data_purge_requested=true 的行。
	// 语义与旧 purge 任务一致（删除设备时勾选「同时删除数据」→ 后台分批硬删
	// 该设备的历史数据 → 清标记 → 硬删设备行），只是标记从已删除的
	// logical_devices.purge_requested 搬到了设备自身。
	purger := datalifecycle.NewDevicePurger(db)
	purger.Start()
	// 历史设备「彻底删除」入队后立即唤醒清理任务，避免等每日周期。
	api.SetDevicePurger(purger)

	// 外发通知通道 (D-1 步骤 3, 设计/外发通知通道.md §2.1 取向 A): Dispatcher 是
	// **所有通知写入点的唯一落库入口** —— 它写 notifications 行 (保持现有语义),
	// 再按 notification_channels 配置外发投递 (出站 HTTP/SSRF 判定/超时/重试/
	// 投递审计/指标全部封装在 internal/notify 内)。
	//
	// 接线必须早于下面所有通知生产者 (retention / alert / datasource /
	// automation): 漏掉任何一个, 它的 notifySink 就保持 nil 而走"直接落库"回落 ——
	// 通知照旧出现在通知中心, 但**永远不会外发**。
	//
	// 必须在 database.AutoMigrate() 之后构造: Dispatcher 要读 notification_channels /
	// notification_deliveries 表。
	notifyDispatcher := notify.NewDispatcher(db)

	// 数据保留期每日任务 — 系统级窗口到期硬删（分区 DROP + 精确 DELETE），
	// 并顺带驱动 8 个无关表的清理器（notifications / deliveries /
	// automation_events / 命令域三表 / 审计 / node_events）。
	// 注: RetentionTask 不再是通知生产者（临期通知随 per-identity retention 移除，
	// 见 datalifecycle/retention_task.go 的字段注释），因此没有 SetNotifier 接线。
	retentionTask := datalifecycle.NewRetentionTask(db)
	retentionTask.Start()

	if credential, err := authservice.CreateStartupInitializationCredential(db); err != nil {
		logger.Fatalf("Failed to create initialization credential: %v", err)
	} else if credential != "" {
		logger.Infof("Initialization credential (valid for 10 minutes): %s", credential)
	}

	if os.Getenv("SEED_TEST_DATA") == "true" {
		if err := seed.SeedTestData(db); err != nil {
			logger.Warnf("Failed to seed test data: %v", err)
		} else {
			logger.Infof("Test data seeded")
		}
	}

	// 3.0 device transport (design 7.3 P0). Disabled by default: with
	// device.enabled unset this build behaves exactly as before, which is what
	// makes deploying it safe. A partially configured listener is refused HERE
	// rather than silently not starting, because "I enabled it" and "the
	// device cannot connect" would otherwise look identical.
	if err := cfg.Device.Validate(); err != nil {
		logger.Fatalf("Invalid device transport configuration: %v", err)
	}

	// 3.0 downlink wiring. This is the ONLY device transport (MQTT was removed
	// from the backend on 2026-10-08).
	//
	// The registry is created HERE, before anything that publishes downlinks,
	// and handed to the transport below. That ordering is what lets one object
	// serve both roles: the transport populates it as devices connect, and the
	// bridge reads it to find the node's live session. With the transport owning
	// its own registry there would be a window in which a connected device is
	// reachable by the routing layer but invisible to everyone else.
	//
	// ⚠ With device.enabled false there is NO device path at all: every
	// downlink fails with "no live session", and no device can reach the server.
	// That is logged below so it is visible rather than discovered.
	deviceRegistry := transport.NewRegistry()
	publisher := downlink.New(deviceRegistry)
	if !cfg.Device.Enabled {
		logger.Warnf("device.enabled=false: the 3.0 listener is OFF and MQTT is gone -- " +
			"NO device can connect or be commanded. This is intended only for API-only deployments.")
	}

	parserConfigs := loadDeviceConfigParsers(db)
	driverRegistry := drivers.NewRegistry()
	drivers.RegisterBuiltInDriversWithParsers(driverRegistry, parserConfigs)
	// 仅 -tags=simulation 构建下生效（空实现在 drivers_prod.go）。仿真型号
	// （sim_generic / sim_scene_*_*）以"只作为型号存在"的驱动登记，使收紧后的
	// POST /device-configs 门禁与 fail-closed 地址门禁不会误伤场景仿真；
	// 生产产物里不含这些型号。
	registerSimulationDriverTypes(driverRegistry)
	logger.Infof("Registered %d device drivers with %d parser overrides", len(driverRegistry.List()), len(parserConfigs))

	// 数据字段解析层 (设计/数据字段解析层-方案-2026-10-21.md v0.3 §3.3/§4.2/§9.2#10):
	// 构造 Registry → 注入声明/别名/派生 → 冷加载投影表 → 启动 projector。
	//
	// 时序要求（写死，改动前先读方案）：
	//   - 必须在 database.AutoMigrate 之后（冷加载要读 sensor_field_obs/meta）；
	//   - 必须先于下面 nodemgr.SetLatestSinkFn 的包装（观测采集点要引用同一 registry）；
	//   - 必须先于 alert/automation 求值器构造（SetPlanProvider 注入同一实例）。
	// fail-closed：配置无效（别名冲突/死候选/派生环）时 setupFieldResolver 返回 error，
	// 这里 Fatal 拒绝启动（方案 §12.1）；投影表读失败只降级"仅声明"。
	fieldResolver, err := setupFieldResolver(db, cfg, driverRegistry)
	if err != nil {
		logger.Fatalf("Field resolver startup failed (fail-closed): %v", err)
	}
	// 保存期 selector 校验注入（precheck / alert / automation / data_sources 的校验路径共用）：
	// 经 api 包窄接口注入，不影响未接线时的旧行为（nil = 不校验）。
	api.SetFieldSelectorResolver(fieldResolver.registry)
	fieldResolverCtx, fieldResolverStop := context.WithCancel(context.Background())
	defer fieldResolverStop()
	go fieldResolver.projector.Run(fieldResolverCtx)

	// 数据生命周期 P4 收尾 (方案 v3.3 §2.4-2/§七-3): 尽力回填
	// config_templates.edge_device_id 归属。依赖 driverRegistry 已就绪
	// (WriteData 匹配需 driver CommandTemplates); 幂等, 失败仅告警不阻断
	// 启动 (归属匹配不上留 NULL, 宁留勿删)。
	if backfilled, err := datalifecycle.BackfillConfigTemplateOwnership(db, driverRegistry); err != nil {
		logger.Warnf("ConfigTemplate ownership backfill failed: %v", err)
	} else if backfilled > 0 {
		logger.Infof("ConfigTemplate ownership backfill: %d template(s) attributed", backfilled)
	}

	wsHub := websocket.NewHub()
	wsHub.SetSessionValidator(func(subjectID uint, version int64) bool {
		var user models.User
		if err := db.Where("id = ? AND subject_key = ? AND retired_at IS NULL AND enabled = ?", subjectID, models.SystemAdminSubjectKey, true).First(&user).Error; err != nil {
			return false
		}
		state, err := models.LoadAuthState(db)
		return err == nil && state.State == models.AuthStateInitialized && user.SessionVersion == version
	})
	go wsHub.Run()
	outboxContext, stopOutbox := context.WithCancel(context.Background())
	defer stopOutbox()
	outboxProcessor := authservice.NewOutboxProcessor(db, func(subjectID uint, _ int64, _ string) {
		wsHub.DisconnectSubject(subjectID)
	})
	go outboxProcessor.Run(outboxContext, time.Second)

	otaMgr := ota.NewManager(db, publisher, wsHub)
	offlineDetector := offlinedetector.NewDetector(db, wsHub)
	nodeMgr := nodemgr.NewManager(db, publisher, wsHub, offlineDetector, otaMgr, driverRegistry)
	// 数据层时序化 (v3.4 §3.2.4): 最新值缓存回调接线 (api 包函数, 避免包依赖环)。
	// Bring up the 3.0 listener now that FrameHandler exists. It uses the SAME
	// registry the bridge above holds, so a device that authenticates becomes
	// immediately addressable over TCP.
	devTransport, err := startDeviceTransport(cfg, nodeMgr.FrameHandler(), deviceRegistry)
	if err != nil {
		logger.Fatalf("Device transport: %v", err)
	}
	defer devTransport.stop()

	// 数据字段解析层观测采集点（方案 §3.3 红线 1 + 评审 B1 的处置）：
	// **包装既有注入点**，internal/databus 零改动、零 import。每条已入库记录：
	// 既有行为（更新最新值缓存）+ 新增纯内存观测计数（sync.Map + atomic，无锁无 DB）。
	// device_id 口径 = UnifiedData.DeviceID = EdgeDevice.ID（禁用 edge_device_id，DS-14）。
	nodeMgr.SetLatestSinkFn(func(record models.UnifiedData) {
		api.SetLatestValue(record)
		fieldResolver.registry.Observe(record.DeviceID, record.SensorName, record.Unit, record.Timestamp)
	})
	// ============ 启动预热：只保留帧表那一趟（2026-11-08）============
	//
	// 原实现有**两趟**：
	//
	//	趟 1（已删）：SetLatestValuesSource + WarmupLatestValues —— 从 unified_data 取，
	//	              DROP 之后那条 SQL 报 42P01（生产实测启动时 1 条 ERROR）。
	//	              ⚠ 它本来就已经是纯冗余：停写后 unified_data 尾部不会有新行。
	//	趟 2（保留）：WarmupLatestValuesFromFrames —— 帧表覆盖所有有帧的设备，是唯一有效来源。
	//
	// 为什么把「注入点」也一起删掉（只删调用会留下 SetLatestValuesSource + warmupSource）：
	// 一个**没人注入的注入点**比死代码更误导 —— 下一个人会以为「有两条预热路径」，
	// 然后去修那条永远 no-op 的。
	//
	// 趟 2 在下面 telemetryDict 就绪之后执行（见 WarmupLatestValuesFromFrames 的调用点）。

	// ── L3 分钟级宽行预聚合：**无条件启用**（2026-11-07 用户裁决，删除开关）──
	//
	// 契约：docs/设计/遥测存储-L1L2无条件启用-契约-2026-11-07.md §1/§3.5。
	// 写入侧常驻：collector 恒构造、sink 恒注入、60s ticker 恒运行。
	//
	// 读侧开关（EHOME_TELEMETRY_ROLLUP_READ）**保留** —— 用户只要求 L1/L2，读切换
	// 有独立风险（见 internal/api/rollup_read.go 的 fail-open 约定）。
	//
	// 两条旁路都挂在「解析成功路径」上（consumers_heavy.go 的 telemetryRollupSink /
	// telemetrySink 调用点），与 L1 的「是否入库」解耦 —— L1 跳过入库的帧仍然是
	// 真实采样，必须进分钟桶与帧表。
	//
	// 失败语义（契约 §3.4）：rollup 是派生数据，flush 失败只 Warn 并下轮重试；
	// 它绝不能影响 unified_data 主写入。
	rollupCollector := datalifecycle.NewRollupCollector(db)
	// 注入：collector 只做内存累加，flush 由下面的 60s ticker 驱动。
	collectSink := rollupCollector.Sink()
	nodeMgr.SetTelemetryRollupSink(func(edgeDeviceID uint, fields []parser.Field, at time.Time) {
		samples := make([]datalifecycle.RollupSample, len(fields))
		for i := range fields {
			samples[i] = datalifecycle.RollupSample{Name: fields[i].Name, Value: fields[i].Value}
		}
		collectSink(edgeDeviceID, samples, at)
	})
	logger.Infof("telemetry: L3 rollup write ACTIVE (1min wide-row buckets -> telemetry_rollup_1m)")
	// ── L2 帧级编码：**无条件启用**（2026-11-07 用户裁决，删除开关）──
	//
	// 契约：docs/设计/遥测存储-L1L2无条件启用-契约-2026-11-07.md §1/§3.4。
	// 帧表 telemetry_frame 从「影子旁路」变成主写入路径之一：每一帧解析结果都
	// 编码进去。因此这里**没有开关判断**：
	//
	//   · 构造失败（表缺失/字典不可用）⇒ Fatal。半残状态（主写成功、帧表空）
	//     会让帧数据静默缺失，比启动失败更难发现（契约 §3.4）。
	//   · 运行期编码/写库失败 ⇒ 只 Warn + 计数（在 DualWriter 内部），绝不影响
	//     unified_data 主写入 —— 表示层不能把主路径拖垮。
	//
	// slot 字典先 WarmUp：它只有几十行，启动时一次全量装载后读路径零查询
	// （解码要靠 slot 还原字段名与量化规格快照）。
	telemetryDict := telemetry.NewDBSlots(db)
	if err := telemetryDict.WarmUp(); err != nil {
		abortStartup("telemetry: slot dictionary warmup failed (L2 frames cannot be decoded without it): %v", err)
	}
	// task-18: 把**同一份** slot 字典交给历史图表读路径（帧表解码要用它还原字段名
	// 与量化规格快照）。
	//
	// 为什么必须是同一份：与写路径共用才能保证"编码时用的 slot 规格"与"解码时
	// 用的"逐位一致。两套字典会解出错位的值 —— 而且**不报错**，只是数值看起来
	// 合理但已经偏了（本仓 §8 的静默错数）。
	//
	// 不注入也能工作（api 侧有惰性装载兜底），但那样首个图表请求要多付一次全量
	// WarmUp（生产约 7,400 个 slot / 约 100 ms），而且是第二份内存副本。
	api.SetHistoryFrameDictionary(telemetryDict)

	// ── 最新值缓存：帧表补全（task-24 步骤 3）──
	//
	// 上面那趟 warmup 读的是 unified_data。**步骤 4 停写之后它会返回 0 行且不报错**：
	// rows=0 -> 缓存空 -> 服务照常启动 -> 首次请求 Miss 回落 SQL -> SQL 读的也是空表
	// => 接口静默返回空，没有任何错误信号。
	//
	// 所以字典一旦就绪（就在上一行），立刻用帧表把还空着的设备补齐。
	// 过渡期两趟都有数据 => 本趟几乎不做事；停写之后本趟就是唯一的预热来源。
	// 详见 internal/api/latest_values_frames.go 的文件头。
	// ============ StepWarmup 的记账移到这里（2026-11-08）============
	//
	// 原来那趟 unified_data 预热（已删）承担了 startupTracker.Done(StepWarmup, ...)。
	// 删掉它之后，StepWarmup 在 steps.go 的清单里**永远等不到 Done/Skip**：
	//
	//	=> tracker 永不 ready => /readyz 永远 503 => 容器健康检查失败。
	//
	// 这不是理论风险：TestStartupAllStepsMarked 会直接红（它断言每一步都有终态记账）。
	//
	// 现在由**帧表那一趟**（唯一有效的预热来源）记账。无论补了多少行都 Done ——
	// 预热是「尽力而为」，0 行也是正常终态（全新部署 / 设备都没上报）。
	startupTracker.Begin(bootstrap.StepWarmup)
	warmedFromFrames := api.WarmupLatestValuesFromFrames(db, telemetryDict)
	if warmedFromFrames > 0 {
		logger.Infof("Latest value cache topped up from frame table: %d rows", warmedFromFrames)
	}
	startupTracker.Done(bootstrap.StepWarmup, fmt.Sprintf("帧表预热完成: %d 行", warmedFromFrames))
	telemetryDual, dualErr := telemetry.NewDualWriter(db, telemetryDict, telemetry.ConfigFromEnv())
	if dualErr != nil {
		abortStartup("telemetry: L2 frame writer construction failed (frames are the main write path; refusing to start with a half-broken telemetry pipeline): %v", dualErr)
	}
	defer telemetryDual.Close()
	nodeMgr.SetTelemetrySink(telemetry.SinkOrNil(telemetryDual))
	logger.Infof("telemetry: L2 frame encoding ACTIVE (every parsed frame -> telemetry_frame, queue=%d)",
		telemetry.ConfigFromEnv().QueueSize)
	// L3 的分钟级 flush：只落「已结束」的桶（FlushCompleted 内部判定），失败只 Warn
	// （派生数据，下一轮重试）。与上面 24h 的 partitionStop ticker 各自独立 ——
	// 不复用同一个变量，避免「关掉一个把另一个也停了」。
	rollupFlusher := rollupCollector.FlushSink()
	rollupFlushStop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-rollupFlushStop:
				return
			case <-ticker.C:
				if _, err := rollupFlusher(context.Background()); err != nil {
					logger.Warnf("telemetry rollup flush failed (derived data, will retry): %v", err)
				}
			}
		}
	}()
	defer close(rollupFlushStop)

	// 阈值告警引擎 (方案 v0.4 §5 任务C): 求值器构造 + 解析后回调接线。
	alertEvaluator := alert.NewEvaluator(db, wsHub.BroadcastEvent)
	// 阈值告警通知经 Dispatcher 落库 + 外发 (D-1 步骤 3): 告警是外发通道最主要的
	// 用户价值来源 —— 无人值守时把告警推到微信/QQ。
	alertEvaluator.SetNotifier(notifyDispatcher)
	// 数据字段解析层控制面注入 (方案 §9.2#3): 告警求值同样按 selector 编译。
	//
	// ⚠ **仅当开关打开时注入**（lead 2026-10-21 性能裁决）：provider 为 nil 时
	// evaluator/alert 走 legacy 逐字节兼容路径（纯字面量直接 matchField，不付
	// 编译缓存查找/谓词求值的固定开销）。无条件注入会让 enabled=false 的纯字面量
	// 求值多付 ~58ns/次（基线 1ns，+5700%，impl-core 基准实测）。
	if fieldResolver.registry.Enabled() {
		alertEvaluator.SetPlanProvider(fieldResolver.registry)
	}
	nodeMgr.SetAlertEvaluator(alertEvaluator)
	go alertEvaluator.Start()
	defer alertEvaluator.Stop()

	// 自动化策略引擎 (设计/自动化策略引擎方案.md v0.1): 求值器+执行器构造接线。
	// 与 alert 并列挂同一批解析后物理量; 动作执行一律走 commandexec (9 gate+幂等+审计)。
	// 系统 actor = 单主体管理员 (subject_key=system_admin), 策略执行归因到该主体。
	//
	// 这里只是**启动期预检**, 不是取值来源: 命中则作为 Planner 的初值 (省掉首次
	// 惰性查询); 未命中是全新安装的正常状态 —— 该用户由 POST /api/v1/auth/initialize
	// 在进程启动**之后**创建。因此: 不中止启动, 也**不把 0 当有效值冻结**。
	// 真正的取值由 Planner 在首次自动 device_action 触发时惰性解析并缓存
	// (automation.Planner.resolveSystemActorID), 否则整条自动执行链会在进程整个
	// 生命周期内失效, 直到重启才自愈。
	var systemActorPrecheckID uint
	{
		var adminUser models.User
		if err := db.Where("subject_key = ? AND retired_at IS NULL", models.SystemAdminSubjectKey).First(&adminUser).Error; err == nil {
			systemActorPrecheckID = adminUser.ID
		} else {
			logger.Infof("[automation] 系统主体用户尚未创建 (subject_key=system_admin), 首次自动 device_action 触发时将惰性解析: %v", err)
		}
	}
	actionRegistry := deviceaction.NewBuiltInRegistry(driverRegistry)
	commandService := commandexec.NewService(db, actionRegistry)
	// 数据源主备领域服务 (B1 已实现)。显式 Options{} 即 §4 默认语义:
	// Cooldown 5m / MinResidency 2m / Staleness 5m / ScanInterval 60s。
	datasourceSvc := datasource.New(db, datasource.Options{})
	// 2026-10-24（遗留处置方案 A）：这里原有两条通知接线
	//   datasourceSvc.SetDispatchNotifier(notifyDispatcher)
	//   datasourceSvc.SetNotifier(func(n models.Notification) { db.Create(&n) })
	// **已删除**，因为 datasource.Service 上的四个手工运维方法
	// （Delete/Activate/Deactivate/Reset）已删 —— 那 4 个方法是本服务**唯一**的
	// 通知来源，删掉后 SetNotifier/SetDispatchNotifier 没有调用点，留着就是死接线。
	// 组域通知不受影响：它走 GroupService 自己的
	//   sourceGroupSvc.SetDispatchNotifier(notifyDispatcher)（见下方）。
	//
	// 数据源主备引擎的 sink 接线 (SetSourceHealthSink / SetDeviceOfflineHook)
	// 仍在下方统一注入（那两个钩子喂的是 GroupService 的自动主备状态机，不是本服务）。
	// ⚠ 此处**不再**注入 SetSourceHealthSink / SetDeviceOfflineHook。
	//
	// M3（数据流架构 v1 §六）发现：原代码在这里注入了一次只喂 datasourceSvc 的
	// sink，随后（来源组接线之后）又注入了一次同时喂两个引擎的 sink。
	// 而 Manager.SetSourceHealthSink 的语义是**覆盖**不是追加
	// （manager.go:145-150：赋值 + 推给所有 consumer），
	// 因此第一次注入**从未生效**，是死代码 —— 保留它会让读者误以为
	// 「datasource 的健康独立注入」，掩盖真实拓扑。
	//
	// 现在唯一的注入点在下方（两引擎都喂）。同时 datasourceSvc 的主备扫描循环
	// （Start/ScanStale/maybeFailover）已下线：主备语义只由 source_groups 承担
	// （用户明确要求「只有来源组支持主备」）。datasourceSvc 退化为台账投影 +
	// 只读查询，不再有运行态状态机。

	// ── 来源组引擎（设计/规则数据来源与复合来源-方案-2026-10-22.md v0.3 §4/§5.5）──
	//
	// 与 datasourceSvc 并列的第二条数据源引擎：datasourceSvc 管 (逻辑设备,类别) 的成员台账，
	// groupSvc 管「一个数据来源」对象（主备/复合，跨逻辑设备）。两者共用同一套 R1–R11 决策
	// 纯函数（ds-analyst 保证），但各自的表与状态互不干扰。
	//
	// 三个失败即拒绝启动（fail-closed，不许带病运行）：
	//   1. PublishAll 内置 SelfCheck —— composite 组存在 active 行 / 同组多 active 行；
	//   2. Registry 是求值热路径的唯一真值来源，没有它规则会 fail-closed 到「永不触发」；
	//   3. 钩子负责清窗与审计，未接线时只能靠窗口 identity 兜底（S3），可用但更迟。
	sourceGroupReg := sourceref.NewRegistry()
	sourceGroupSvc := datasource.NewGroupService(db, datasource.Options{})
	sourceGroupSvc.SetGroupRegistry(sourceGroupReg)
	sourceGroupSvc.SetDispatchNotifier(notifyDispatcher)
	// 规则保存期需要它做来源绑定校验（组存在/enabled/锚点非 0/cooldown 推导下限）。
	// 不注入时 api 层会按 db 惰性构造一个只读实例兜底 —— 功能可用，但每次保存多一次
	// 构造开销，且与这里的实例状态不同源。显式注入是唯一正确做法。
	api.SetRuleSourceGroupService(sourceGroupSvc)
	// 快照发布 + 自检：composite 组误置 active 或同组多 active 会让「用哪一台」不确定，
	// 必须在启动期拒绝，而不是运行期静默取第一条。
	if err := sourceGroupSvc.PublishAll(); err != nil {
		// fail-closed：来源组配置自相矛盾（composite 组误置 active / 同组多 active）
		// 会让「到底用哪一台供数」不确定。带病启动比不启动更危险。
		logger.Fatalf("source_group: 启动自检失败（拒绝启动）：%v", err)
	}
	// 分发器与扫描器在这里启动；切换**钩子**要等两引擎构造完才能注册（见下方 automation 段）。
	sourceGroupCtx, sourceGroupStop := context.WithCancel(context.Background())
	defer sourceGroupStop()
	go sourceGroupSvc.RunDispatcher(sourceGroupCtx)
	go sourceGroupSvc.Start(sourceGroupCtx)
	// 钩子通道溢出后的兜底重载：dispatcher 的变更通道是固定缓冲，突发变更
	// （批量导入 / 脚本操作）会溢出并丢通知。丢了不重载 ⇒ 快照陈旧 ⇒ 规则继续按
	// 旧 active 过滤，而且**没有任何告警**。这个循环是那条失效路径的唯一出口
	// （架构终审 H-5：DirtySinceLastReload 原先无消费方，是个死安全阀）。
	go sourceGroupSvc.RunDirtyReloader(sourceGroupCtx, 5*time.Second)

	// ── L2 来源层投影（数据流架构 v1 §四 M2）──
	//
	// 为什么在 PublishAll 之后：派生输入需要「DB 里真实 enabled 的组」，而 PublishAll
	// 已保证组配置自检通过（composite 误置 active 已在上面 Fatal）。
	//
	// 为什么失败不 fail-closed：data_sources 是**只读台账**（grep 证明 automation/alert
	// 零引用），它坏了最多让页面显示不全，不影响采集/规则/告警。为它拒绝启动是过度反应
	// —— 与 source_group 的启动自检（那条必须 Fatal）性质不同。
	//
	// 为什么放冷路径：生产 unified_data 每 10 分钟约 1.9 万行，任何在采集 sink 里写库的
	// 设计都会反压 ingest。这里启动一次 + 60s ticker（复用字段解析层 flush 节奏）。
	sourceProjection := setupSourceProjection(sourceGroupCtx, db, datasourceSvc, sourceGroupSvc, sourceGroupReg, driverRegistry)
	go sourceProjection.RunProjection(sourceGroupCtx, db, sourceGroupReg, 60*time.Second)

	// 数据面两条 sink 同时喂两个引擎：解析成功 ⇒ 两边都 MarkSuccess；
	// 设备离线 ⇒ 两边都 MarkFailure。漏接一条会让该引擎「永不切换」（静默失效）。
	// M3（数据流架构 v1 §六）：health sink 现在**只喂 GroupService**。
	//
	// datasourceSvc 的主备状态机已删除（见 internal/datasource/service.go 顶部
	// 「已下线的 legacy 主备状态机」注释）。主备语义只有来源组一套 ——
	// 用户明确要求「只有来源组支持主备」，两套状态机会互相覆盖 status。
	nodeMgr.SetSourceHealthSink(func(edgeDeviceID uint, names []string, at time.Time) {
		sourceGroupSvc.MarkSuccess(edgeDeviceID, names, at)
	})
	offlineDetector.SetDeviceOfflineHook(func(edgeDeviceID uint) {
		sourceGroupSvc.MarkFailure(edgeDeviceID, datasource.TriggerDeviceOffline)
	})

	// systemActorPrecheckID 可能为 0 (全新安装): 语义是"待 Planner 惰性解析", 不是有效 actor。
	automationPlanner := automation.NewPlanner(db, commandService, wsHub.BroadcastEvent, systemActorPrecheckID)
	// 自动化策略通知经 Dispatcher 落库 + 外发 (D-1 步骤 3): 日熔断 / 待人工确认 /
	// 纯通知动作 / 系统主体不可用四类通知随之可外发 —— 无人值守下"待确认"必须能推送到手机。
	automationPlanner.SetNotifier(notifyDispatcher)
	// F4 条件复核接线: 注入最新值缓存查询, 触发到执行间条件失效则落 condition_changed 不执行。
	// 用函数注入避免 automation→api 编译期反向依赖 (与 databus latestSink 同模式)。
	automationPlanner.SetLatestValueFn(api.LatestValue)
	// F4 复核按 selector 取最新值（设计 §9.2#4 / AU-12）：
	//   literal  → 按 (设备, 物理名) 精确取（新增函数，不改旧签名）；
	//   family/@semantic/virtual → 最新一帧多槽值（复核输入需要整帧）。
	automationPlanner.SetLatestValueBySensorFn(api.LatestValueBySensor)
	automationPlanner.SetLatestValuesFn(api.LatestValues)
	automationEvaluator := automation.NewEvaluator(db, automationPlanner)
	// 数据字段解析层控制面注入 (方案 §9.2#1/#2): trigger 与 conditions 都走编译产物。
	// 与 alert 同理：**仅 enabled=true 时注入**，否则保持 legacy 快速路径（见上方说明）。
	if fieldResolver.registry.Enabled() {
		automationEvaluator.SetPlanProvider(fieldResolver.registry)
		automationPlanner.SetPlanProvider(fieldResolver.registry)
	}

	// ── 来源组接线（方案 §5.2 / §6.1 / §12.2.2）──
	//
	// 注入顺序有硬约束：两引擎的 SetSourceResolver 必须在**开始接收帧之前**完成，
	// 否则最早几帧会走「resolver 未接线」路径（安全但会产生 unresolved_selector
	// 噪声审计，detail 里带 f4_fail_reason=source_resolver_not_configured）。
	//
	// 两引擎 + planner 共用同一份快照：帧求值（MatchFrame）与 F4 复核读的必须是
	// 同一个 revision，否则会出现「求值时是 A 供数、复核时快照已切到 B」的口径分裂。
	automationEvaluator.SetSourceResolver(sourceGroupReg)
	automationPlanner.SetSourceResolver(sourceGroupReg)
	automationEvaluator.SetLatestValueBySensorFn(api.LatestValueBySensor)
	automationPlanner.SetLatestValueBySensorFn(api.LatestValueBySensor)
	alertEvaluator.SetSourceResolver(sourceGroupReg)
	alertEvaluator.SetLatestValueBySensorFn(api.LatestValueBySensor)

	// 切换钩子：ActiveChange 只在**事务提交后**发射（datasource 侧保证）。三件事 ——
	//   ① 两引擎立即清窗：**加速**，不是正确性前提（窗口 identity 不匹配即清，S3）；
	//   ② 关闭告警的陈旧 firing（S4）：换人后旧「正在告警」不再代表权威来源；
	//   ③ 落审计：运维要知道换了谁、为什么换。
	// 求值期降级可见性：sourceref 的缺席/陈旧判定发生在**求值侧**，只有那里知道
	// 「这次为什么 unbound」。不接这个回调，规则会正确地 fail-closed，但
	// source_group_health（运维看板）一片空白 —— 用户以为来源组健康，实际已不可用。
	// 三处注入共用同一方法值：GroupService 内部按 (group_id, reason) 1 分钟节流，
	// 所以每帧调用是安全的。
	automationEvaluator.SetSourceHealthFn(sourceGroupSvc.RecordDegraded)
	automationPlanner.SetSourceHealthFn(sourceGroupSvc.RecordDegraded)
	alertEvaluator.SetSourceHealthFn(sourceGroupSvc.RecordDegraded)

	sourceGroupSvc.SetActiveChangeHook(func(ev datasource.ActiveChange) {
		alertEvaluator.OnSourceTransition(ev.GroupID)
		automationEvaluator.OnSourceTransition(ev.GroupID)
		logger.Infof("[source_group] active change group=%d %d->%d reason=%s trigger=%s rev=%d seq=%d",
			ev.GroupID, ev.PrevMemberID, ev.NextMemberID, ev.Reason, ev.Trigger, ev.Revision, ev.TransitionSeq)
	})

	nodeMgr.SetAutomationEvaluator(automationEvaluator)
	go automationEvaluator.Start()
	defer automationEvaluator.Stop()
	// B1 接线: time_window 触发器独立 1min ticker (不挂传感器解析回调)。
	automationWindowCtx, automationWindowStop := context.WithCancel(context.Background())
	automationEvaluator.StartWindowTicker(automationWindowCtx)
	defer automationWindowStop()
	// 裁决 4 确认制闭环: pending_confirm 事件 24h 超时清扫 goroutine (设计/自动化确认制闭环实现方案.md §3.4)。
	// 独立挂 Planner (非 evaluator ticker): evaluator 只缓存 sensor_threshold 规则会漏扫其它触发类型。
	automationCleanupContext, automationPlannerStopCleanup := context.WithCancel(context.Background())
	go automationPlanner.StartCleanup(automationCleanupContext)
	defer automationPlannerStopCleanup()
	commandService.SetDispatchEnabled(cfg.ControlConfig().DeviceControlV2Enabled)
	nodeMgr.SetCommandExecutionService(commandService)
	go nodeMgr.Start()

	wsHub.SetOnMessage(func(client *websocket.Client, evt websocket.Event) {
		if evt.Type == "send" {
			// Generic WebSocket events must never be a raw WriteCmd transport.
			// The former handler bypassed REST diagnostics gating, audit and the
			// CommandExecution control domain. Raw diagnostics have no audited
			// implementation yet, so reject rather than preserving a second
			// physical-TX path.
			logger.Warnf("[WS] Rejecting retired raw send event for subject=%d", client.SubjectID)
		}
	})

	if cfg.ControlConfig().DeviceControlV2Enabled {
		dispatcherOwner := commandexec.NewDispatcherOwner("server")
		// MultiTransport 按 action Transport 路由: channel_cmd_v2 → 通道指令,
		// periph_cmd → GPIO/PWM 外设帧 (PeriphCmd 0x1B)。
		transport := commandexec.NewMultiTransport(
			commandexec.NewChannelCmdV2Transport(db, publisher, actionRegistry),
			commandexec.NewPeriphTransport(db, publisher, actionRegistry))
		dispatcher := commandexec.NewDispatcher(db, transport, dispatcherOwner)
		go runCommandDispatcher(outboxContext, dispatcher, commandService, wsHub)
		logger.Infof("ChannelCmdV2 dispatcher enabled owner=%s", dispatcherOwner)
	} else {
		logger.Infof("ChannelCmdV2 dispatcher disabled by configuration")
	}

	// Server-startup config push.
	//
	// ⚠ This MUST keep running: it was gated on <-mqttClient.Ready() (broker
	// connected), and simply deleting the gate together with MQTT would have
	// silently removed the feature. The gate was meaningful because "the
	// transport is up" is a real precondition for a push to arrive.
	//
	// Over 3.0 the honest translation of that precondition is "the node has a
	// live session", checked per node below. Two reasons it cannot just be
	// dropped to an unconditional loop:
	//
	//  1. A node with no session returns an error from Publish, and
	//     SendConfigManifestWithDecision turns that into fail(), which writes
	//     config_status=failed. At startup the DB still says "online" for nodes
	//     that simply have not reconnected yet, so an unconditional push would
	//     mark healthy nodes failed -- reporting "the device refused its config"
	//     when the truth is "it is not connected".
	//  2. It would be useless work: the push could not arrive.
	//
	// Devices that are not connected yet are NOT left unsynced: handleHello
	// (handler_hello.go) runs the same gate on every Hello, so a node gets its
	// config the moment it connects. This loop covers nodes already connected
	// when the server starts, which is exactly what it always covered.
	//
	// No sleep/x grace period is used on purpose -- an arbitrary wait is what
	// the original comment ("never after an arbitrary sleep") rejected.
	go func() {
		decisions := nodeMgr.SyncGate().OnServerStartup()
		notified, skipped := 0, 0
		for _, d := range decisions {
			if d.Action == nodemgr.SyncActionNone {
				continue
			}
			if !deviceRegistry.HasSession(d.DeviceID) {
				// Not connected: skip rather than mark it failed. Its config is
				// delivered by the Hello path when it connects.
				skipped++
				logger.Infof("[sync_id=%s] Server-startup push skipped: device=%s reason=%s "+
					"(no live session yet; Hello will sync it on connect)",
					d.SyncID, d.DeviceID, d.Reason)
				continue
			}
			nodeMgr.SendConfigManifestWithDecision(d)
			notified++
			logger.Infof("[sync_id=%s] Server-startup push: device=%s reason=%s",
				d.SyncID, d.DeviceID, d.Reason)
		}
		if len(decisions) > 0 {
			logger.Infof("Server-startup push complete: %d notified, %d skipped (not connected)",
				notified, skipped)
		}
	}()

	otaMgr.Start()
	offlineDetector.Start()

	// 全量引擎（gin 输出模式已在启动最前面设定，这里不再重复 SetMode）。
	// API 服务器的监听**早已开始**（见 main 顶部维护引擎段）：此刻起所有新请求由
	// 维护引擎应答；装配完成后 rootHandler.SetEngine(r) 一次性原子切到本引擎。
	r := gin.New()
	r.Use(gin.Recovery())
	// Gzip compression for API responses (20MB JSON → ~3MB).
	// /metrics and /api/v1/metrics/prometheus are excluded: promhttp.Handler()
	// performs its own gzip negotiation and emits its own gzip framing, so
	// wrapping it in this middleware produced a doubly-framed body
	// ([gzip header][gzip header][single deflate stream]) that no standard
	// gzip decoder can read — breaking real Prometheus scrapes. Excluding the
	// paths lets promhttp negotiate gzip correctly and set its own
	// Content-Encoding header, so compression is preserved.
	r.Use(gzip.Gzip(gzip.DefaultCompression, gzip.WithExcludedPaths([]string{
		"/metrics",
		"/api/v1/metrics/prometheus",
	})))
	allowedOrigins := []string{}
	for _, origin := range strings.Split(os.Getenv("EHOME_ALLOWED_ORIGINS"), ",") {
		if value := strings.TrimSpace(origin); value != "" {
			allowedOrigins = append(allowedOrigins, value)
		}
	}
	// CORS is only needed when AllowCredentials=true and the frontend runs on a
	// different origin. When EHOME_ALLOWED_ORIGINS is unset (empty), the
	// production deployment serves the frontend from the same origin, so CORS
	// headers are unnecessary and an empty AllowOrigins slice would panic in
	// gin-contrib/cors v1.7.7 when AllowCredentials=true.
	// 支持通配符 "*"（放开任意来源）：gin-contrib/cors 在 AllowOrigins 含 "*"
	// 且 AllowCredentials=true 时会自动回显请求 Origin 而非字面 "*"，因此
	// 既允许任何来源的浏览器访问，也保留凭据（Cookie/Authorization）传递。
	if len(allowedOrigins) > 0 {
		r.Use(cors.New(cors.Config{
			AllowOrigins:     allowedOrigins,
			AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"},
			AllowHeaders:     []string{"Origin", "Content-Type", "Authorization", "Accept", "X-Requested-With"},
			ExposeHeaders:    []string{"Content-Length", "Content-Type"},
			AllowCredentials: true,
			MaxAge:           12 * time.Hour,
		}))
	}
	controlCfg := cfg.ControlConfig()
	// notifyDispatcher 一并交给 API 层: POST /notification-channels/:id/test 要用它真发
	// 一条测试消息 (D-1 步骤 4)。传的是**同一个实例** —— 否则"测试按钮"与真实通知
	// 走两套客户端/两套 SSRF 判定的风险, 且测试通过不代表线上通道可用。
	// ⚠ sourceGroupSvc **必须**在这里传：它的唯一作用是让 api 层把 /source-groups
	// 九条路由注册到真实实现上。api.SetupRoutes 按 option 的**具体类型**做 switch
	// （routes.go:89 case *datasource.GroupService），漏传时不会报错、不会 panic，
	// 而是安静地走 nil 分支注册 503 占位 —— 前端表现为"来源组页面永远报
	// source group service unavailable"。
	//
	// 这个缺陷曾真实存在（2026-10-23 用户在浏览器实测发现）：sourceGroupSvc 造好后
	// 只调了 api.SetRuleSourceGroupService（那是**规则保存期校验**用的另一条通道，
	// 见 source_group_guard.go），从未作为 option 传给 SetupRoutes ⇒ 来源组 CRUD
	// 全部 503，而来源组的领域逻辑本身完全正常。两条注入路径互不相通，必须都接。
	api.SetupRoutes(r, db, wsHub, nodeMgr, otaMgr, driverRegistry, commandService, alertEvaluator, automationEvaluator, automationPlanner, datasourceSvc, sourceGroupSvc, notifyDispatcher, fieldResolver.registry, api.ControlPolicy{
		RawDiagnosticsEnabled: controlCfg.RawDiagnosticsEnabled,
	})

	// P2：静态资源缓存头（/assets immutable、SPA 入口 no-cache）封装在 static.go。
	// staticDir 在 main 顶部已读取一次（维护引擎与全量引擎必须服务同一份产物）。
	setupStaticRoutes(r, staticDir)

	r.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":  "ok",
			"version": "2.0.0",
			"drivers": driverRegistry.List(),
		})
	})

	// ════════════════════════════════════════════════════════════════════════
	// 原子切换：维护引擎 → 全量引擎（契约 §2）
	//
	// 时机是硬约束（有源码级门禁 cmd/server/maintenance_gate_test.go 守着）：
	// 必须在 api.SetupRoutes(...) **与** setupStaticRoutes(...) 都完成之后。
	// 早一步切换 ⇒ 用户会看到 404（路由还没注册完）；
	// 晚一步 ⇒ 装配完毕却仍在维护页，看起来像卡死。
	// ════════════════════════════════════════════════════════════════════════
	// 顺序有意如此：先把最后一步记账成 done，**再**切换引擎 —— 这样
	// 「/readyz 变 200」与「全量引擎开始服务」是同一时刻，不会出现"路由已是全量、
	// 探针仍 503"（K8s 会把正在接客的实例摘掉）或反之的窗口。
	// ════════════════════════════════════════════════════════════════════════
	// 老数据回填（unified_data → telemetry_frame）已于 2026-11-13 **整包删除**。
	//
	// 原因：unified_data 退役后没有源表可读；全新部署从不创建该表，旧部署由
	// ehomectl retire-unified-data --yes 一次性 DROP。留着这个后台任务只会
	// 在每次启动时 probe 一张不存在的表并打印 42P01。历史迁移的生产实测
	//（3.42M 帧）与回滚窗口都已完成，不存在"还没迁完"的部署。
	// 进程内 slot 字典（telemetryDict）仍被 L2 双写与 rollup 回填使用，未受影响。
	// ============ 存量 device_data sensors 剥离任务已删除（2026-11-11 整表退役）============
	//
	// 这里原本跑 datalifecycle.NewDeviceDataSensorStripper（task-21）：把 device_data 里
	// 与 unified_data / telemetry_frame 100% 重复的 sensors 数组从 data_json 里剥掉，
	// 预计省约 1.3 GB。
	//
	// 表已整表退役（DROP），剥离任务与它的 watermark 表（device_data_strip_progress）
	// 一起删除 —— 留着它只会在启动时对一张不存在的表报 42P01，且日志会让人误以为
	// 「还有个后台任务在跑」。
	//
	// 历史收益的去向：那 3,488 MB 由 §3.3 的 DROP TABLE 一次性收回（比剥离更彻底），
	// 详见 docs/设计/原始数据退役-改造方案-2026-11-11.md。
	// ======================================================================================

	startupTracker.Done(bootstrap.StepStartupTasks, "后台任务已启动")
	// 收尾自检：任何一步既非 done 也非 skipped 都是**漏标**（新增步骤时最容易犯），
	// 宁可在这里吵一句，也不要让运维对着永远 99% 的进度条猜。
	if !startupTracker.Ready() {
		logger.Errorf("startup tracker NOT ready before engine switch (some step never finished — check Begin/Done pairs); snapshot=%+v", startupTracker.Snapshot())
	}

	// ════════════════════════════════════════════════════════════════════════
	// L3 rollup 历史回填（task-22）
	// ════════════════════════════════════════════════════════════════════════
	//
	// 为什么要它：帧表已完整覆盖历史，但读路径分派仍是
	//   ≤1h → 帧表 / 1h~6h → unified_data / >6h → rollup（开关关）→ unified_data。
	// 只要 >1h 的窗口还读 unified_data，老的 12 GB 分区就删不掉。
	// 本回填把 rollup 的历史补齐，让长窗改读 rollup。
	//
	// 三条启动纪律（与上面帧表回填同款）：
	//   1. **绝不阻塞启动** —— 只建一次便宜的元数据表，真正的回填在 goroutine 里；
	//   2. 元数据表建不出来 ⇒ 只 Warn 并跳过回填，不 Fatal（不挡主写入）；
	//   3. 幂等可续跑 —— 已 complete/missing 的 (设备,日) 单元直接跳过，容器重启
	//      只会从断点继续，不会重复劳动。
	{
		rollupBackfillCfg, rollupBackfillWarns := datalifecycle.RollupBackfillConfigFromEnv()
		for _, w := range rollupBackfillWarns {
			logger.Warnf("rollup-backfill: %s", w)
		}
		rollupBackfillEnabled := rollupBackfillCfg.Enabled
		if err := datalifecycle.EnsureRollupCoverageTable(db); err != nil {
			// 覆盖元数据表建不出来 ⇒ 读路径永远判"未覆盖"⇒ 长窗永远回落。
			// 这是**降级**不是故障（服务可用、只是慢），所以 Warn + 跳过，不 Fatal。
			logger.Warnf("rollup-backfill: 建覆盖元数据表失败，跳过回填（不阻塞启动）: %v", err)
			rollupBackfillEnabled = false
		}
		// ── 帧表聚合源注入（P0 残留缺口修复，2026-11-07）──
		//
		// 为什么是注入而不是直接 import：internal/telemetry 的**测试包** import 了
		// datalifecycle（pg_partition_test.go），所以 datalifecycle 的生产代码一旦
		// import telemetry 就是 test-only 的 import cycle（go vet ./... 会红）。
		// 用注入把依赖方向掰回来：datalifecycle 只认中性类型 FramePoint。
		//
		// 段宽护栏在 datalifecycle 侧：`framesBatchLimit`，现在是 1h。
		//
		// ============ 它是**上限（cap）**，不是步长 ============
		//
		//	外层（rollup_backfill.go）: for t := dayStart; t.Before(dayEnd); t = t.Add(batch)
		//	内层（rollup_backfill_frames.go）: for t := from; t.Before(to); t = t.Add(framesBatchLimit)
		//	                                  segTo := min(t+framesBatchLimit, to)   ← 被外层 to 卡住
		//
		// 所以 **实际段宽 = min(framesBatchLimit, BatchSeconds)**。
		// BatchSeconds 默认 3600（1h，见 defaultRollupBackfillBatchSeconds），
		// 因此默认段宽 = 1h => 1h × 1fps = 3,600 帧，上限 32,768 => **余量 9 倍**。
		//
		// framesBatchLimit 只在「有人把 BatchSeconds 设成 > 1h」时才起作用 ——
		// 那时它把窗口切小，防止一次解码太多帧。把它从 6h 收到 1h 是**纯防御性收紧**：
		// 即使有人设 BatchSeconds=24h，单段也最多 1h（余量从 1.5 倍提到 9 倍），
		// 默认行为完全不变。
		//
		// ⚠ 但段宽多大都**不能**替代下面的截断检查：10 fps 的设备（interval_ms=100 合法）
		// 在 1h 段内就是 36,000 帧 > 32,768。只有显式检查能保证「超了就知道」。
		//
		// （2026-11-08 修正：我先把它误读成「实现用 6h、注释算 1h」，据此写过一版
		//   声称「注释和实现不一致」的注释。核代码后确认**实现也是 1h** —— cap 被
		//   外层 batch 卡住。原注释是对的。这段留在这里是为了记住那次误读。）
		datalifecycle.SetFrameDecoder(func(ctx context.Context, db *gorm.DB, deviceID uint, from, to time.Time) ([]datalifecycle.FramePoint, error) {
			window := to.Sub(from)
			// ============ 帧预算必须用 MaxScanFrames（2026-11-11 生产事故修复）============
			//
			// 这里此前传的是 telemetry.MaxDecodeFrames（32768），**错了语义**：
			// MaxDecodeFrames 是「一次性解码（DecodeRange 单批）」的上限，而
			// DecodeRangeAnchoredBounded 的第 8 个参数是**扫描预算**（从锚点起最多
			// 扫多少帧）—— 两者不是一回事，后者的正确取值是 MaxScanFrames（262144）。
			//
			// 生产现场（2026-11-11 实测）：device14 的 keyframe 在 2026-11-08 15:57:58，
			// 下一个在 2026-11-10 05:21:20 —— 中间 32 小时没有关键帧。回填 11-10 的
			// 每一段（段宽 1h）都要先从 32 小时前的锚点预热 ≈ 118,922 帧：
			//	  · 预算 32768 ⇒ 只扫到前 27%，FramesTruncated>0 ⇒ 本段报错
			//	  · 整个单元回填失败 ⇒ MarkRollupCoverage(partial, buckets=0)
			//	  · 但 rollup_1m 里那一天其实有 1439 个分钟桶（数据是齐的！）
			//	  ⇒ 读路径 RollupCoverageVerified 只认 complete ⇒ 判定「覆盖未验证」
			//	  ⇒ 24h/7d/30d 图表全报「超过帧表单次解码上限 6h，且该窗口尚未被分钟聚合覆盖」
			//
			// 即：**一个常量选错，让用户可见的图表坏掉，而底层数据完好**。
			// 262144 @1Hz ≈ 72.8 小时，覆盖 32 小时锚点跨度有 2.3 倍余量。
			pts, stats, err := telemetry.DecodeRangeAnchoredBounded(
				ctx, db, telemetryDict, deviceID, from, to, nil,
				window*2, telemetry.MaxScanFrames)
			if err != nil {
				return nil, err
			}
			// ============ 截断即失败（2026-11-08）============
			//
			// 为什么必须在这里失败，而不是「照常返回部分点」：
			//
			//	部分点 => 回填照常写 rollup 行 + coverage 标 complete
			//	        => RollupCoverageVerified 认可 complete => **永不重试**
			//	        => 长窗图表读 rollup，显示错误数字且看起来完全正常
			//	        => 而 rollup 是预聚合，**没有任何独立来源能对拍它**
			//
			// 相比之下，图表少一段是「用户能看见」，这个错了是「用户看不见」。
			//
			// ⚠ 原注释写「段宽 1h × 1fps = 3,600 帧，远低于上限，这个分支在生产不会触发」
			//   —— **这个判断是错的，2026-11-11 已在生产触发**。错在只算了「段内帧数」，
			//   没算「锚点预热段帧数」：预算计的是**从锚点起扫过的全部帧**，而锚点在
			//   32 小时前时预热就要 11.5 万帧。触发条件不是「高频设备」，而是
			//   「**关键帧间隔 × 采样率 > 预算**」—— 1 Hz 设备只要 9.1 小时没有关键帧
			//   就会撞上 32768。
			//
			// 保留这个分支仍然正确（截断必须失败，否则不完整统计会被标 complete 永不重试），
			// 但预算是 MaxScanFrames 之后，正常形态（≤72.8 小时无关键帧）不会被它拦下。
			if stats.FramesTruncated > 0 {
				return nil, fmt.Errorf("rollup backfill segment truncated: device=%d [%s,%s) "+
					"dropped %d oldest frames (seg=%s, budget=%d, anchor_span 过大); "+
					"段内帧数超上限 —— 继续回填会把不完整统计标成 complete（永不重试）",
					deviceID, from.Format(time.RFC3339), to.Format(time.RFC3339),
					stats.FramesTruncated, to.Sub(from), telemetry.MaxScanFrames)
			}
			out := make([]datalifecycle.FramePoint, len(pts))
			for i := range pts {
				out[i] = datalifecycle.FramePoint{TS: pts[i].TS, Name: pts[i].Name, Value: pts[i].Value}
			}
			return out, nil
		})
		rollupBackfillRunner := datalifecycle.NewRollupBackfillRunner(db, rollupBackfillCfg)
		// 进度接口（免鉴权，与 /api/v1/system/backfill 同级）：载荷只有计数/状态，
		// 没有业务数据。⚠ 必须在 SetEngine 之前注册（gin 路由树无读锁）。
		api.RegisterRollupBackfillStatus(r, func() api.RollupBackfillSnapshot {
			p := rollupBackfillRunner.Progress()
			return api.RollupBackfillSnapshot{
				State:          p.State,
				UnitsTotal:     p.UnitsTotal,
				UnitsDone:      p.UnitsDone,
				UnitsSkipped:   p.UnitsSkipped,
				BucketsWritten: p.BucketsWritten,
				CurrentDay:     p.CurrentDay,
				CurrentDevice:  p.CurrentDevice,
				Errors:         p.Errors,
			}
		})
		if rollupBackfillEnabled {
			go func() {
				logger.Infof("rollup-backfill: L3 历史回填已启动（batch=%ds, sleep=%s）—— 后台执行，不阻塞服务",
					rollupBackfillCfg.BatchSeconds, rollupBackfillCfg.BatchSleep)
				if err := rollupBackfillRunner.RunOnce(context.Background()); err != nil {
					logger.Warnf("rollup-backfill: 回填中断（下次启动将从覆盖元数据继续）: %v", err)
					return
				}
				p := rollupBackfillRunner.Progress()
				logger.Infof("rollup-backfill: 完成 state=%s 单元=%d/%d 分钟桶=%d 跳过=%d",
					p.State, p.UnitsDone, p.UnitsTotal, p.BucketsWritten, p.UnitsSkipped)
			}()
			// ── 每日 rollover（A-改，2026-11-07）──
			//
			// 为什么需要：coverage 只在回填**跑起来**时才推进，而上一条 goroutine 只在
			// 进程启动跑一次。服务连续运行多日 => coverage 停在启动那天 =>
			// RollupWindowCovered=false => 长窗回落 => 停写后图表静默截断（HTTP 200，无告警）。
			//
			// 为什么是「再跑一次 RunOnce」而不是「让 live collector 写 coverage」：
			// RollupWindowCovered **跳过今天**（今天交给 live collector），所以写今天的
			// coverage 不会让任何窗口变成 covered；窗口要 covered 需要「昨天」有行。
			// 而 RunOnce 对已 complete 的单元是**跳过**的，重复跑几乎零成本。
			//
			// 两条路径（启动 + 每日）都调同一个 RunOnce，天然幂等：
			// 进程若在 00:05 那一刻不在运行（重启/部署窗口），启动那次会滚动补上。
			//
			// ── 每次每日推进的开销估算（Lead 2026-11-07 要求写清，免得被当成常数）──
			//
			// 单元数 = 设备数 x 回填天数。跳过判定 unitAlreadyDone 是**逐单元一次**
			// coverage 表主键查询 (device_id, day)，索引命中、亚毫秒级。
			//
			//	当前 10 设备 x 45 天 = 450 单元 => 约 450 次轻量查询，可忽略
			//	设备数 x 保留期涨到 5,000 单元 => 约 5,000 次 => 每日一次仍可接受，但**不是常数**
			//
			// 若将来单元数再上一个量级，优化路径是：把逐单元 unitAlreadyDone 换成
			// ReadRollupCoverage 一次性取回整个日期区间（1 次查询 + 内存比对）。
			// 现在不提前做 —— 当前量级下它只是把 450 次索引查询换成 1 次全区间扫描，收益不明。
			go func() {
				for {
					next := datalifecycle.NextRollupRolloverAt(time.Now())
					logger.Infof("rollup-backfill: 每日推进已排程，下次 %s", next.Format(time.RFC3339))
					time.Sleep(time.Until(next))
					began := time.Now()
					err := rollupBackfillRunner.RunOnce(context.Background())
					p := rollupBackfillRunner.Progress()
					// 必须打日志：否则「跑了但什么都没做」与「没跑」在日志上同形。
					logger.Infof("rollup-backfill: 每日推进 state=%s 单元=%d/%d 跳过=%d 分钟桶=%d 耗时=%s err=%v",
						p.State, p.UnitsDone, p.UnitsTotal, p.UnitsSkipped, p.BucketsWritten,
						time.Since(began).Round(time.Millisecond), err)
				}
			}()
		} else {
			logger.Infof("rollup-backfill: 已关闭（%s=false）或不可用，跳过", datalifecycle.EnvRollupBackfillEnabled)
		}
	}

	rootHandler.SetEngine(r)
	logger.Infof("root handler switched to FULL engine (翻转为正式服务，维护页退出): percent=%.1f", startupTracker.Snapshot().Percent)

	// 排障开关 EHOME_STARTUP_MAINTENANCE_PAGE=false 时，监听一直被上面那道门挡着，
	// 此刻才放行 —— 与改造前"启动完成后才开始 Accept"完全等价。
	// 默认（true）情况下这道门在 main 顶部就关了，listenNow 是幂等的空操作。
	listenNow()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
	sig := <-quit

	logger.Infof("Received signal %v, shutting down gracefully...", sig)

	purger.Stop()
	logger.Infof("Edge device data purger stopped")

	retentionTask.Stop()
	logger.Infof("Data lifecycle retention task stopped")

	offlineDetector.Stop()
	logger.Infof("Offline detector stopped")

	otaMgr.Close()
	logger.Infof("OTA manager stopped")

	nodeMgr.Stop()
	logger.Infof("Collector manager stopped")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Errorf("HTTP server shutdown error: %v", err)
	} else {
		logger.Infof("HTTP server stopped")
	}

	logger.Infof("EHomeSystem Server stopped")
}

// ════════════════════════════════════════════════════════════════════════════
// 启动进度接线（契约 §3.1/§3.2/§3.4）
// ════════════════════════════════════════════════════════════════════════════

// startupProgressAdapter 把 datalifecycle 的迁移埋点转投到 bootstrap.Tracker。
//
// 为什么需要这层适配：datalifecycle 刻意不 import bootstrap（依赖方向保持
// datalifecycle → 标准库/GORM，见 partition_mgr.go 顶部说明），它只认
// 自己的 MigrationProgress 窄接口。适配器是两边唯一的接缝，放在 cmd/server ——
// 启动编排的归属地。
//
// touched 记录「哪些步骤真的被回调过」：parent_indexes / retire_dead_indexes
// 嵌在迁移函数内部，main 只能在外面 Begin，无法从返回值判断"到底建没建索引"。
// 回调与否就是那个判据（Done vs Skip），见 settleNestedIndexSteps。
type startupProgressAdapter struct {
	tracker *bootstrap.Tracker

	mu      sync.Mutex
	touched map[string]bool
}

// StepProgress 实现 datalifecycle.MigrationProgress。由迁移 goroutine 调用，
// 必须并发安全（touched 有自己的锁，tracker 内部也有锁）。
func (a *startupProgressAdapter) StepProgress(stepID string, pct float64, detail string) {
	if a == nil || a.tracker == nil || stepID == "" {
		return
	}
	a.mu.Lock()
	if a.touched == nil {
		a.touched = map[string]bool{}
	}
	a.touched[stepID] = true
	a.mu.Unlock()
	a.tracker.StepProgress(stepID, pct, detail)
}

// wasTouched 报告某个 step 是否收到过至少一次进度回调。
func (a *startupProgressAdapter) wasTouched(stepID string) bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.touched[stepID]
}

// maintenanceSnapshotFromTracker 把 bootstrap.Snapshot 映射成 api 的镜像类型。
//
// 为什么手写映射而不让 api import bootstrap：api 是路由/协议层，bootstrap 是启动
// 编排层，让前者依赖后者会把「HTTP 契约」与「谁负责跑迁移」焊死。镜像类型的
// JSON tag 由 internal/api/maintenance_test.go 的 TestMaintenanceSnapshotJSONShape
// MatchesContract 钉在契约 §3.1 上，两边漂移会立刻变红。
func maintenanceSnapshotFromTracker(t *bootstrap.Tracker) api.MaintenanceSnapshot {
	if t == nil {
		return api.MaintenanceSnapshot{}
	}
	snap := t.Snapshot()
	out := api.MaintenanceSnapshot{
		Ready:        snap.Ready,
		Failed:       snap.Failed,
		Percent:      snap.Percent,
		CurrentStep:  snap.CurrentStep,
		CurrentLabel: snap.CurrentLabel,
		StartedAt:    snap.StartedAt,
		ElapsedMS:    snap.ElapsedMS,
		Version:      snap.Version,
		BuildCommit:  snap.BuildCommit,
		Steps:        make([]api.MaintenanceStep, len(snap.Steps)),
	}
	for i, step := range snap.Steps {
		out.Steps[i] = api.MaintenanceStep{
			ID:         step.ID,
			Label:      step.Label,
			Weight:     step.Weight,
			State:      string(step.State),
			Progress:   step.Progress,
			Detail:     step.Detail,
			Error:      step.Error,
			StartedAt:  step.StartedAt,
			FinishedAt: step.FinishedAt,
		}
	}
	return out
}

// timeseriesTableNames 返回参与生命周期与分区保障的时序表名（顺序固定）。
//
// 为什么从 datalifecycle 取而不是在 main 里再抄一份清单：这张清单已经在
// retention / purge / data-summary / merge-history / latestTimestamp 五处被
// 使用（评审 B-3 的修复点）。main 里再抄一份就等于第七处，早晚漂移 ——
// 「下个月没有分区」在月初会静默变成写入失败（PG 分区表无兜底 default 分区）。
func timeseriesTableNames() []string {
	specs := datalifecycle.TimeseriesTables()
	out := make([]string, 0, len(specs))
	for _, t := range specs {
		out = append(out, t.Name)
	}
	return out
}

// startupFailureGrace 是「标记失败 → 进程真正退出」之间留给前端的窗口。
//
// 为什么需要（2026-11-06 契约 §5 的硬要求）：Fatal 会立刻终止进程，浏览器只看到
// 连接断开，看不到失败原因。留一点时间让已经在轮询的维护页抓到一次 failed 快照，
// 用户才能看到"连接数据库…失败：xxx"，而不是白屏。
//
// 800ms 是折中：短于前端的 2s 轮询间隔就**可能**错过（但前端门禁首次探测就是
// 立刻发的，加上失败前的运行时间通常足够）；再长则拖慢容器编排的重启决策。
const startupFailureGrace = 800 * time.Millisecond

// abortStartup = 「记录失败 → 留窗口 → Fatal」的统一出口。
//
// 调用方必须先 tracker.Fail(stepID, err) 再调本函数（保持步骤归属清晰）；
// 本函数只负责让失败**可见**，退出语义与改造前完全一致（仍是 logger.Fatalf，
// 仍是非 0 退出）。maintenance page 关掉时同样生效（只是没人看得到）。
func abortStartup(format string, args ...interface{}) {
	if startupMaintenancePageEnabled() {
		time.Sleep(startupFailureGrace)
	}
	logger.Fatalf(format, args...)
}

// startupMaintenancePageEnabled 读 EHOME_STARTUP_MAINTENANCE_PAGE（默认 true）。
//
// 默认 true 是刻意的：这条改造的价值全在"升级期间用户能看见进度"，
// 默认开启才符合契约 §3.4。=false 时退化为旧行为（先做完再监听），只用于排障：
// 排障时要能一键判断题现的异常是不是"提前监听"引入的。
func startupMaintenancePageEnabled() bool {
	raw := strings.TrimSpace(os.Getenv("EHOME_STARTUP_MAINTENANCE_PAGE"))
	if raw == "" {
		return true
	}
	enabled, err := strconv.ParseBool(raw)
	if err != nil {
		// 拼错的布尔值不该让运维以为开关生效了：按默认值走并明确告警。
		logger.Warnf("EHOME_STARTUP_MAINTENANCE_PAGE=%q 不是合法布尔值，按默认 true 处理", raw)
		return true
	}
	return enabled
}

// startupFailFast 可选看门狗（契约 §3.4 第二行，默认关闭）。
//
// EHOME_STARTUP_FAIL_FAST_SECONDS > 0 时：启动超过该秒数仍未就绪 ⇒ 进程以非 0
// 退出。为什么需要它：提前监听后端口是通的，容器编排与负载均衡会认为"这个实例
// 起来了"，而实际上它可能卡在一条几十分钟的索引 DDL 上。设置阈值让"卡死"
// 能被编排层识别并以重启收敛，而不是永远挂着一个不通业务的实例。
func startupFailFast(tracker *bootstrap.Tracker) {
	raw := strings.TrimSpace(os.Getenv("EHOME_STARTUP_FAIL_FAST_SECONDS"))
	if raw == "" {
		return
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		logger.Warnf("EHOME_STARTUP_FAIL_FAST_SECONDS=%q 无效或 <=0，启动看门狗关闭", raw)
		return
	}
	go func() {
		timer := time.NewTimer(time.Duration(seconds) * time.Second)
		defer timer.Stop()
		<-timer.C
		if !tracker.Ready() {
			snap := tracker.Snapshot()
			logger.Fatalf("startup exceeded EHOME_STARTUP_FAIL_FAST_SECONDS=%d without becoming ready (percent=%.1f current=%s): failing fast so the orchestrator can restart",
				seconds, snap.Percent, snap.CurrentStep)
		}
	}()
	logger.Infof("startup fail-fast watchdog armed: %d s", seconds)
}

func loadDeviceConfigParsers(db *gorm.DB) map[string]json.RawMessage {
	configs := make(map[string]json.RawMessage)
	var deviceConfigs []models.DeviceConfig
	if err := db.Where("status = ?", "active").Order("is_default DESC, id DESC").Find(&deviceConfigs).Error; err != nil {
		logger.Warnf("Failed to load DeviceConfig parsers: %v", err)
		return configs
	}
	for _, cfg := range deviceConfigs {
		if cfg.DeviceType == "" || len(cfg.Parser) == 0 || string(cfg.Parser) == "{}" || string(cfg.Parser) == "null" {
			continue
		}
		if _, exists := configs[cfg.DeviceType]; !exists {
			configs[cfg.DeviceType] = cfg.Parser
		}
	}
	return configs
}

func runCommandDispatcher(ctx context.Context, dispatcher *commandexec.Dispatcher, service *commandexec.Service, wsHub *websocket.Hub) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := dispatcher.ProcessOnce(ctx); err != nil {
				logger.Errorf("ChannelCmdV2 dispatch failed: %v", err)
			}
			expired, err := service.RecoverExpired(ctx)
			if err != nil {
				logger.Errorf("ChannelCmdV2 recovery failed: %v", err)
			} else if wsHub != nil {
				for _, execution := range expired {
					wsHub.BroadcastAuthenticatedEvent(events.DeviceOperationUpdate, execution)
				}
			}
		}
	}
}
