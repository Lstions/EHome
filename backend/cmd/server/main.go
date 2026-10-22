// EHomeSystem main.go
package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"ehome/backend/internal/alert"
	"ehome/backend/internal/api"
	authservice "ehome/backend/internal/auth"
	"ehome/backend/internal/automation"
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
	"ehome/backend/internal/transport"
	"ehome/backend/internal/websocket"
	"ehome/backend/pkg/logger"
	"encoding/json"

	"github.com/gin-contrib/cors"
	"github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"

	"gorm.io/gorm"
)

func main() {
	cfg := config.Load()

	if err := logger.Init(cfg.LogLevel()); err != nil {
		panic("failed to init logger: " + err.Error())
	}
	defer logger.Sync()

	logger.Infof("EHomeSystem Server v2.0 starting...")

	// Validate JWT secret is not default in production
	api.ValidateJWTSecret()
	logger.Infof("Config: DB=%s:%d/%s, API=%s, device_listener=%v",
		cfg.DBConfig().Host, cfg.DBConfig().Port, cfg.DBConfig().DBName, cfg.APIAddr(), cfg.Device.Enabled)

	dbCfg := cfg.DBConfig()
	if err := database.Connect(database.Config{
		Host:     dbCfg.Host,
		Port:     dbCfg.Port,
		User:     dbCfg.User,
		Password: dbCfg.Password,
		DBName:   dbCfg.DBName,
		SSLMode:  dbCfg.SSLMode,
	}); err != nil {
		logger.Fatalf("Failed to connect database: %v", err)
	}
	if err := database.AutoMigrate(); err != nil {
		logger.Fatalf("Failed to migrate database: %v", err)
	}
	logger.Infof("Database connected and migrated")

	// 数据层时序化 (方案 v3.4 §3.2.1): unified_data / device_data 分区迁移 +
	// 滚动分区保障。迁移失败即 Fatal 中止启动, 不再静默降级为"继续用平表":
	// 失败路径已保证源平表与 legacy/_new 表完整 (安全门禁在行数校验前不 swap),
	// 且搬迁幂等 (主键 (id,timestamp) + ON CONFLICT), 修好后重跑即可收敛;
	// 而"带着未完成的迁移继续跑"会让后半段行为与预期不符且极难排查。
	// 非 PG 方言 (SQLite 测试) 下两个迁移函数均为 no-op 返回 nil, 不受影响。
	db := database.GetDB()
	if err := datalifecycle.MigrateUnifiedDataToPartitioned(db); err != nil {
		logger.Fatalf("unified_data partition migration failed (aborting startup, source tables intact, fix and restart to resume): %v", err)
	}
	if err := datalifecycle.MigrateTableToPartitioned(db, "device_data", "device_data_legacy"); err != nil {
		logger.Fatalf("device_data partition migration failed (aborting startup, source tables intact, fix and restart to resume): %v", err)
	}
	// 滚动分区保障: 已分区的时序表各自确保 [上月, 未来 3 月] 分区存在。
	// 未分区表 (迁移失败/非 PG) 跳过, 避免对普通表执行 PARTITION OF 报错。
	pm := datalifecycle.NewPartitionManager(db)
	ensurePartitions := func() {
		for _, table := range []string{"unified_data", "device_data"} {
			if !datalifecycle.IsTablePartitioned(db, table) {
				continue
			}
			if err := pm.EnsurePartitionsFor(table, 3); err != nil {
				logger.Errorf("ensure %s partitions failed: %v", table, err)
			}
		}
	}
	ensurePartitions()
	// 每日滚动检查: 创建下月分区（retention 到期分区由 retention_task 触发 DROP）。
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

	// 数据生命周期 P0 (方案 v3.3 §2.3.1 路径 1 + §4.1): 注册系统级保留期
	// 快照源, 并对全量 edge_devices (含软删) 幂等补建 logical_device。
	datalifecycle.SetSystemRetentionDays(cfg.DataRetentionDays())
	if backfilled, err := datalifecycle.BackfillLogicalDevices(db, cfg.DataRetentionDays()); err != nil {
		logger.Errorf("Logical device backfill failed: %v", err)
	} else if backfilled > 0 {
		logger.Infof("Logical device backfill: %d instance(s) attached", backfilled)
	} else {
		logger.Infof("Logical device backfill: all instances already attached")
	}

	// purge 后台任务 (§4.3 任务 2): 每日分批硬删 purge_requested 的逻辑设备数据。
	purger := datalifecycle.NewPurger(db)
	purger.Start()

	// 数据生命周期 M 迁移步骤 (方案 v3.3 §七 + §1.1): 复合索引
	// CONCURRENTLY 创建 + 大表 logical_device_id 分批回填, 全部在后台
	// goroutine 内执行——墙钟随存量数据量增长, 不能阻塞启动; 回填进度
	// 持久化在 backfill_jobs 水位表, 中断重启自动续跑 (§4.3); 两者均幂等,
	// 失败下次启动重试。运维可用 `ehomectl datalifecycle backfill`
	// 同步执行并以退出码判定校验结果。
	// 前置: P0 身份补建 (上方 BackfillLogicalDevices) 必须先完成,
	// 回填依赖实例已挂载 logical_device_id。
	backfiller := datalifecycle.NewBackfiller(db)
	backfiller.Start()

	// 数据生命周期 P3 (方案 v3.3 §4.3 任务 3): 合并搬迁 worker — 处理
	// merge_status='pending' 源的数据搬迁, 水位断点续跑 + 失败通知/重试。
	// 外发通知通道 (D-1 步骤 3, 设计/外发通知通道.md §2.1 取向 A): Dispatcher 是
	// **所有通知写入点的唯一落库入口** —— 它写 notifications 行 (保持现有语义),
	// 再按 notification_channels 配置外发投递 (出站 HTTP/SSRF 判定/超时/重试/
	// 投递审计/指标全部封装在 internal/notify 内)。
	//
	// 接线必须早于下面所有通知生产者 (migrator / retention / alert / datasource /
	// automation): 漏掉任何一个, 它的 notifySink 就保持 nil 而走"直接落库"回落 ——
	// 通知照旧出现在通知中心, 但**永远不会外发**, 正是本步骤要消灭的"引擎孤儿"。
	//
	// 必须在 database.AutoMigrate() 之后构造: Dispatcher 要读 notification_channels /
	// notification_deliveries 表。
	notifyDispatcher := notify.NewDispatcher(db)

	migrator := datalifecycle.NewMigrator(db)
	migrator.SetNotifier(notifyDispatcher)
	migrator.Start()

	// 数据生命周期 P3 (方案 v3.3 §4.1/§4.2/§4.3 任务 1): retention 每日
	// 任务 — 到期前 30/7 天通知 + 到期分批硬删。
	retentionTask := datalifecycle.NewRetentionTask(db)
	retentionTask.SetNotifier(notifyDispatcher)
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
	// 数据层时序化 (v3.4 §3.2.4) 的**启动回填**：必须在开始服务前完成，
	// 否则重启后每台设备的首次 /overview 都 Miss ⇒ 回落 DISTINCT ON 扫分区表。
	//
	// 为什么放在这里：`api.WarmupLatestValues` 自 2026-08-21 引入起**一直没有调用者**
	// （git 历史确认引入那次提交就没有；实施计划 §3.2.4 却明确要求「启动回填」）——
	// 属未完成的实现（验收项「latest-value 缓存命中/回落双路径一致」至今未勾选）。
	// 不 warmup 不影响**正确性**（Miss 回落 SQL 结果相同），影响的是性能与两条路径形状一致。
	//
	// 失败不 Fatal：回填只是预热，Miss 路径仍是正确兜底（与 WarmupLatestValues 内部
	// 「回填失败不阻塞启动」的约定一致）。
	//
	// SQL 与 /overview、边缘设备列表的**回落查询同构**（DISTINCT ON 取最新时间戳 +
	// JOIN 取该时刻全部行），保证「缓存命中」与「缓存 miss」两条路径形状一致。
	api.SetLatestValuesSource(func() ([]models.UnifiedData, error) {
		var rows []models.UnifiedData
		err := db.Raw("SELECT u.* FROM unified_data u " +
			"INNER JOIN (SELECT DISTINCT ON (device_id) device_id, created_at FROM unified_data " +
			"WHERE device_id IS NOT NULL ORDER BY device_id, created_at DESC) latest " +
			"ON u.device_id = latest.device_id AND u.created_at = latest.created_at").Scan(&rows).Error
		return rows, err
	})
	logger.Infof("Latest value cache warmed up: %d rows", api.WarmupLatestValues())
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
	// 数据源主备通知经 Dispatcher 落库 + 外发 (D-1 步骤 3)。Dispatcher 优先级高于
	// 下面既有的 SetNotifier 回调; 那个回调保留为未接线时的回落。
	datasourceSvc.SetDispatchNotifier(notifyDispatcher)
	// 数据源主备引擎接线 (设计/数据源主备与故障转移.md §4/§6):
	//   解析成功 → MarkSuccess; 边缘设备离线 → MarkFailure; 停滞扫描 → Start。
	// 硬约束: sink 注入必须在 nodemgr.NewManager 之后 (nodeMgr 已构建);
	// SetSourceHealthSink 会把回调推送到所有已构建 parserConsumers, 否则
	// consumer 持有的 sink 永远为 nil, 引擎"从未触发"。
	datasourceSvc.SetNotifier(func(n models.Notification) {
		if err := db.Create(&n).Error; err != nil {
			logger.Warn("datasource: failed to write notification", "source_id", n.SourceID, "error", err)
		}
	})
	nodeMgr.SetSourceHealthSink(func(edgeDeviceID uint, names []string, at time.Time) {
		datasourceSvc.MarkSuccess(edgeDeviceID, names, at)
	})
	offlineDetector.SetDeviceOfflineHook(func(edgeDeviceID uint) {
		datasourceSvc.MarkFailure(edgeDeviceID, datasource.TriggerDeviceOffline)
	})
	dataSourceCtx, dataSourceStop := context.WithCancel(context.Background())
	defer dataSourceStop()
	go datasourceSvc.Start(dataSourceCtx)

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

	// 数据面两条 sink 同时喂两个引擎：解析成功 ⇒ 两边都 MarkSuccess；
	// 设备离线 ⇒ 两边都 MarkFailure。漏接一条会让该引擎「永不切换」（静默失效）。
	nodeMgr.SetSourceHealthSink(func(edgeDeviceID uint, names []string, at time.Time) {
		datasourceSvc.MarkSuccess(edgeDeviceID, names, at)
		sourceGroupSvc.MarkSuccess(edgeDeviceID, names, at)
	})
	offlineDetector.SetDeviceOfflineHook(func(edgeDeviceID uint) {
		datasourceSvc.MarkFailure(edgeDeviceID, datasource.TriggerDeviceOffline)
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
	// 否则最早几帧会走「resolver 未接线 ⇒ fail-closed」路径（安全但会产生
	// source_resolver_not_configured 噪声审计）。
	//
	// 两引擎 + planner 共用同一份快照：帧求值（MatchFrame）与 F4 复核读的必须是
	// 同一个 revision，否则会出现「求值时是 A 供数、复核时快照已切到 B」的口径分裂。
	automationEvaluator.SetSourceResolver(sourceGroupReg)
	automationPlanner.SetSourceResolver(sourceGroupReg)
	automationEvaluator.SetLatestValueBySensorFn(api.LatestValueBySensor)
	automationPlanner.SetLatestValueBySensorFn(api.LatestValueBySensor)
	automationEvaluator.SetDeviceIntervalFn(deviceIntervalFn(db))
	alertEvaluator.SetSourceResolver(sourceGroupReg)
	alertEvaluator.SetLatestValueBySensorFn(api.LatestValueBySensor)
	alertEvaluator.SetDeviceIntervalFn(deviceIntervalFn(db))

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

	gin.SetMode(gin.ReleaseMode)
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
	api.SetupRoutes(r, db, wsHub, nodeMgr, otaMgr, driverRegistry, commandService, alertEvaluator, automationEvaluator, automationPlanner, datasourceSvc, notifyDispatcher, fieldResolver.registry, api.ControlPolicy{
		RawDiagnosticsEnabled: controlCfg.RawDiagnosticsEnabled,
	})

	staticDir := os.Getenv("EHOME_STATIC_DIR")
	// P2：静态资源缓存头（/assets immutable、SPA 入口 no-cache）封装在 static.go
	setupStaticRoutes(r, staticDir)

	r.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":  "ok",
			"version": "2.0.0",
			"drivers": driverRegistry.List(),
		})
	})

	// Start HTTP server with graceful shutdown support
	srv := &http.Server{
		Addr:    cfg.APIAddr(),
		Handler: r,
	}

	go func() {
		logger.Infof("API server listening on %s", cfg.APIAddr())
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatalf("API server failed: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
	sig := <-quit

	logger.Infof("Received signal %v, shutting down gracefully...", sig)

	purger.Stop()
	logger.Infof("Data lifecycle purger stopped")

	backfiller.Stop()
	logger.Infof("Data lifecycle backfiller stopped")

	migrator.Stop()
	logger.Infof("Data lifecycle merge migrator stopped")

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

// deviceIntervalFn 返回「按边缘设备取采集间隔」的回调，供两引擎的 S8 窗口 gap 上限
// 与来源组 staleness 推导使用（方案 §5.6 S8 / F-3）。
//
// 缓存策略：采集间隔是**低频变更的配置**（改配置会走 API 且极少发生），而回调在
// 每帧求值路径上调用。这里用 10 分钟 TTL 的内存缓存 + 主键查询兜底：
//   - 未配置 / 查询失败 ⇒ 返回 0 ⇒ 调用方**不启用** gap 判据
//     （宁可少一道保护，也不要因为拿不到 interval 就让所有规则停止触发）；
//   - 不做无上限缓存，避免长跑进程持有过期配置。
func deviceIntervalFn(db *gorm.DB) func(edgeDeviceID uint) time.Duration {
	type entry struct {
		d  time.Duration
		at time.Time
	}
	var (
		mu    sync.Mutex
		cache = map[uint]entry{}
	)
	const ttl = 10 * time.Minute
	return func(edgeDeviceID uint) time.Duration {
		if edgeDeviceID == 0 {
			return 0
		}
		mu.Lock()
		if e, ok := cache[edgeDeviceID]; ok && time.Since(e.at) < ttl {
			mu.Unlock()
			return e.d
		}
		mu.Unlock()

		var dev models.EdgeDevice
		var d time.Duration
		if err := db.Select("id", "interval_ms").First(&dev, edgeDeviceID).Error; err == nil && dev.IntervalMs > 0 {
			d = time.Duration(dev.IntervalMs) * time.Millisecond
		}
		mu.Lock()
		cache[edgeDeviceID] = entry{d: d, at: time.Now()}
		mu.Unlock()
		return d
	}
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
