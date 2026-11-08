package datalifecycle

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"gorm.io/gorm"

	"ehome/backend/pkg/metrics"
)

// RetentionResult reports one retention run's outcome.
//
// 2026-10-23: 逻辑设备层移除后，保留期回到**系统级单一窗口**
// (EHOME_DATA_RETENTION_DAYS，默认 90)，不再有「按身份配置」与 30/7 天两档通知
// —— 那两档的前提是「每个身份有自己的到期日」，系统级窗口下到期日对全库一致。
type RetentionResult struct {
	// Days 是本轮采用的系统级保留天数。0 表示永久保留。
	Days int `json:"days"`
	// Permanent 表示本轮**跳过了数据删除**（保留期为 0 = 永久）。
	//
	// 单独给一个布尔而不是让调用方去判 Days==0：日志/接口消费方不该知道
	// 「0 是永久还是未配置」这个内部约定，否则迟早有人把 0 当默认值渲染成
	// 「保留 0 天 = 删除所有数据」。
	Permanent bool `json:"permanent,omitempty"`
	// RowsDeleted 是本轮按窗口精确删除的行数（分区 DROP 另计）。
	RowsDeleted int64  `json:"rows_deleted"`
	Err         string `json:"error,omitempty"`
}

// RetentionTask is the §4.3 任务 1 daily worker: 保留期临期通知
// (30/7 天) + 到期分批硬删。清理范围与查询同一 scope 解析
// (v3.1-Q2), 与 purge 同样守卫 pending 合并参与者 (搬迁窗口内数据
// 归属未定, 不应用目标/源任一方的保留期)。
type RetentionTask struct {
	db           *gorm.DB
	interval     time.Duration
	initialDelay time.Duration
	batchSleep   time.Duration
	batchSize    int // 0 → dialect default (PG 1万 / SQLite 1千)
	// notifier 清理 notifications 表 (D-2): 与本任务同频每日执行, 无独立
	// goroutine —— 调度编排不变, 失败只 slog.Warn, 不影响 retention 主流程。
	notifier *NotificationCleaner
	// 注 (2026-10-23): 本任务**不再是通知生产者**。
	//
	// 历史上它是「保留期临期通知（30/7 天两档）」的写入点，因此需要 main.go 注入
	// notify.Dispatcher。逻辑设备层移除后保留期变成系统级单一窗口，到期日对全库
	// 一致，不再有「这个身份快到期了」的个体语义 ⇒ 临期通知整体消失，本任务只
	// 删除过期数据。
	//
	// 因此注入字段与 setter 方法**一并删除**，而不是留一个空壳 setter ——
	// 空壳 setter 正是 notify_wiring_gate 要防的「引擎孤儿」形状（调用点在、
	// 实际无投递，静默失效）。该性质由 notify_wiring_test.go 的源码扫描用例
	// 钉住（本包内不得再出现通知注入点的标识符）。
	// deliveries 清理 notification_deliveries 投递审计表 (D-1 裁决 3 的欠账):
	// 同样挂在本任务上, 与 notifications 清理**同批处理** (裁决原文要求),
	// 不新增 goroutine、不改 main.go。
	deliveries *NotificationDeliveryCleaner
	// events 清理 automation_events (运行期无界增长表设计的 §2.1 分层保留裁决):
	// 与 notifications / deliveries 清理**同一批**、同一 RunOnce 调用点,
	// 不新增 goroutine、不改 main.go。旁路: 失败只 slog.Warn + 指标。
	//
	// 该表同时是 7 处业务判定的读对象 (日熔断/幂等序号/手动冷却/超时清扫), 因此
	// 它的清理带【分层窗口 + pending_confirm 显式排除 + 白名单 fail-closed】三重约束,
	// 详见 automation_event_cleanup.go 文件头。
	events *AutomationEventCleaner
	// 命令域三表清理 (运行期无界增长表设计 §2.4/§2.5/§2.6): 三个清理器【各自独立】,
	// 因为三张表的保留策略各不相同 (execution 730d 终态 / attempt 730d 全部 /
	// outbox 30d 终态)。"三张表一套参数"正是设计 §3 专项要防的错误。
	// 同样挂在本任务上 (同一 RunOnce 调用点), 不新增 goroutine、不改 main.go;
	// 失败只 slog.Warn + 指标, 不影响逐设备 retention 主流程。
	commandExecutions *CommandExecutionCleaner
	commandAttempts   *CommandAttemptCleaner
	commandOutboxes   *CommandOutboxCleaner
	// audit 清理 security_audit_events (运行期无界增长表设计 §2.3 的裁决: 统一保留
	// 730 天, 按 created_at 删)。它是本批八张表里最后一个此前没有清理器的表 ——
	// 在此之前表只增不减 (实测 3797 行 / 1.9 MB)。
	// 与上面六个清理器同一 RunOnce 调用点 (第 7 个), 不新增 goroutine、不改 main.go;
	// 失败只 slog.Warn + 指标, 不影响逐设备 retention 主流程。
	audit *SecurityAuditCleaner
	// nodeEvents 清理 node_events (运行期无界增长表设计 §2.2 的裁决: 统一保留
	// 400 天, 按 created_at 删, 【不设分层档位】)。
	//
	// ⚠️ 口径纠正: audit 上面的注释曾把 security_audit_events 称作"本批八张表里
	// 最后一个此前没有清理器的表"—— 那句对 §2.3 成立, 对本表【不成立】。实测本表
	// (node_events) 才是真正被漏掉的那一张: §2.2 的裁决 2026-09-13 就已落下, 但全仓
	// 零 Delete NodeEvent / 零 DELETE FROM node_events, 表只增不减 (实测 6098 行 /
	// 2026-08-11 → 09-12)。详见 node_event_cleanup.go 文件头。
	//
	// 与上面七个清理器同一 RunOnce 调用点 (第 8 个), 不新增 goroutine、不改 main.go;
	// 失败只 slog.Warn + 指标, 不影响逐设备 retention 主流程。
	nodeEvents *NodeEventCleaner
	// now is injectable for tests.
	now func() time.Time

	startOnce sync.Once
	stopCh    chan struct{}
	stopOnce  sync.Once
	wg        sync.WaitGroup
}

// NewRetentionTask creates the daily retention worker.
func NewRetentionTask(db *gorm.DB) *RetentionTask {
	return &RetentionTask{
		db:           db,
		interval:     24 * time.Hour,
		initialDelay: 40 * time.Second, // purge 30s 首发之后, 错峰启动
		batchSleep:   purgeBatchSleep,
		notifier:     NewNotificationCleaner(db),
		deliveries:   NewNotificationDeliveryCleaner(db),
		events:       NewAutomationEventCleaner(db),

		commandExecutions: NewCommandExecutionCleaner(db),
		commandAttempts:   NewCommandAttemptCleaner(db),
		commandOutboxes:   NewCommandOutboxCleaner(db),
		audit:             NewSecurityAuditCleaner(db),
		nodeEvents:        NewNodeEventCleaner(db),

		now:    time.Now,
		stopCh: make(chan struct{}),
	}
}

// SetSchedule overrides timing (tests only).
func (r *RetentionTask) SetSchedule(interval, initialDelay, batchSleep time.Duration) {
	if interval > 0 {
		r.interval = interval
	}
	if initialDelay > 0 {
		r.initialDelay = initialDelay
	}
	r.batchSleep = batchSleep
}

// SetBatchSize overrides the per-transaction delete batch size (tests only).
func (r *RetentionTask) SetBatchSize(n int) {
	if n > 0 {
		r.batchSize = n
	}
}

// SetBatchSleep overrides the inter-batch sleep (tests only; 0 disables).
func (r *RetentionTask) SetBatchSleep(d time.Duration) { r.batchSleep = d }

// Start launches the retention goroutine once.
func (r *RetentionTask) Start() {
	r.startOnce.Do(func() {
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			r.run()
		}()
	})
}

// Stop signals the goroutine to exit and waits for it.
func (r *RetentionTask) Stop() {
	r.stopOnce.Do(func() { close(r.stopCh) })
	r.wg.Wait()
}

func (r *RetentionTask) run() {
	timer := time.NewTimer(r.initialDelay)
	defer timer.Stop()
	select {
	case <-timer.C:
		r.runOnceLogged()
	case <-r.stopCh:
		return
	}
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			r.runOnceLogged()
		case <-r.stopCh:
			return
		}
	}
}

func (r *RetentionTask) runOnceLogged() {
	results, err := r.RunOnce(context.Background())
	if err != nil {
		slog.Warn("datalifecycle: retention run failed", "error", err)
		return
	}
	for _, res := range results {
		if res.Err != "" {
			slog.Warn("datalifecycle: retention processing failed",
				"days", res.Days, "error", res.Err)
			continue
		}
		if res.RowsDeleted > 0 {
			slog.Info("datalifecycle: retention expired rows deleted",
				"days", res.Days, "rows_deleted", res.RowsDeleted)
		}
	}
}

// RunOnce applies the system-level retention window to the whole database.
//
// 语义（2026-10-23 逻辑设备层移除后的最终形态；2026-11-07 增加「永久保留」）：
//   - 保留期只有一个系统级窗口 EHOME_DATA_RETENTION_DAYS（默认 90 天）；
//   - 先按月 DROP 分区（整月分区的每一天都已过期才删，见 dropExpiredPartitionsOnce），
//     再对剩余边界分区按 timestamp 做精确分批 DELETE；
//   - **保留期为 0 表示永久保留**：上面两步整段跳过，但下面 8 个旁路清理器照跑
//     （它们的窗口来自各自的常量，与数据保留期无关）；
//   - 不再有「按身份跳过」「pending 合并顺延」这类守卫 —— 它们存在的唯一理由是
//     「不同身份有不同的到期日」，而系统级窗口下这个前提不存在。
//
// 旁路清理器（notifications / deliveries / automation_events / 命令域三表 /
// 审计 / node_events）与本任务同频、同一调用点，顺序与理由见下方逐条注释；
// 任何一项失败都只告警，绝不影响数据保留期主流程。
//
// 为什么「永久保留」不跳过旁路清理器（这是本轮最容易做错的一处）：
// 用户说的「所有数据要长期保留」指的是**遥测数据**（unified_data / device_data /
// telemetry_frame）。旁路清理器删的是通知、投递审计、自动化事件、命令记录、安全审计、
// 节点事件 —— 这些是**元数据**，且它们**没有独立 goroutine**，唯一调用点就是本函数。
// 整体 return 会让这 8 张表无限增长，把「保留数据」变成「全库膨胀」的事故。
func (r *RetentionTask) RunOnce(ctx context.Context) ([]RetentionResult, error) {
	days := SystemRetentionDays()
	// ==================== 0 = 永久保留（2026-11-07）====================
	//
	// 用户裁决「所有数据要长期保留」，因此这里**不再**把 0 兜底成 1 天
	// （那是个反向 bug：设 0 想永久保留，反而删得更狠）。改成:
	//
	//	days <= 0 ⇒ 跳过【数据】删除（分区 DROP + 行级 DELETE），
	//	           但**旁路清理器全部照跑** —— 通知/投递/自动化事件/命令域三表/
	//	           审计/node_events 各有自己的保留期常量，与数据保留期无关。
	//
	// 为什么不能「整体 return」：那会让这 8 张表**无限增长**（它们的清理器没有
	// 独立 goroutine，唯一调用点就是本函数），把一个「保留数据」的需求变成
	// 「所有表都永久膨胀」的事故。而它们清理的是**运维/审计元数据**，不是用户
	// 的遥测数据 —— 用户说的「所有数据」不包含这些。
	//
	// 负值同样走这条路（SetSystemRetentionDays 会忽略负值，所以实际到不了这里的
	// 只有真正的 0；保留 <= 0 判断是为了防「有人绕过 setter 直接改 atomic」）。
	permanent := days <= 0
	if permanent {
		slog.Info("datalifecycle: data retention is PERMANENT (retention_days<=0): " +
			"skipping partition DROP and row DELETE; bypass cleaners still run")
	} else {
		// 全局分区清扫 (v3.4 §3.2.1): 每轮一次。系统级窗口下 cutoff 唯一，不存在
		// 「短保留期设备连带删掉长保留期设备数据」的问题（那是 per-identity retention
		// 才有的问题）。
		//
		// 清扫失败不得中断下面的精确 DELETE: 只记指标 + 日志, 继续。
		if err := r.dropExpiredPartitionsOnce(ctx, r.now(), days); err != nil {
			metrics.LifecycleTaskFailures.WithLabelValues("retention").Inc()
			slog.Error("datalifecycle: retention global partition sweep failed; continuing exact deletes",
				"days", days, "error", err)
		}
	}

	// D-2: notifications 清理 (与 retention 同频每日一次)。旁路操作:
	// 失败仅告警, 绝不影响下面的逐设备保留期删除。
	if ctx.Err() == nil {
		r.notifier.runOnceLogged(ctx)
	}

	// D-1 裁决 3 欠账: notification_deliveries 投递审计清理, 与
	// notifications 清理**同批处理** (同一每日任务、同一 RunOnce 调用点,
	// 不是两个各自调度的任务)。同样是旁路操作: 失败仅告警, 不影响
	// 下面的逐设备保留期删除。
	if ctx.Err() == nil {
		r.deliveries.runOnceLogged(ctx)
	}

	// 运行期无界增长表 §2.1: automation_events 分层保留清理 (非执行档 30 天 /
	// 执行档 400 天), 与上两个清理器同一批。旁路操作: 失败仅告警, 不影响下面的
	// 逐设备保留期删除。
	if ctx.Err() == nil {
		r.events.runOnceLogged(ctx)
	}

	// 命令域三表清理 (设计 §2.4/§2.5/§2.6)。顺序不是任意的:
	//  1. executions 先删 —— outbox 的删除条件之一是"对应 execution 不处于非终态",
	//     先删掉到期终态 execution 会让更多到期 outbox 在同一轮里满足条件;
	//  2. outboxes 再删 (30 天窗, 唯一可短留的表);
	//  3. attempts 最后删 (730 天窗, 与 executions 同寿命 —— 防伪锚点不得短留)。
	// 三者都是独立的单表 DELETE (零级联), 换个顺序也不会错, 但这样同一轮能一次清完。
	//
	// 三个清理器各自独立失败: 旁路操作, 失败仅告警, 绝不影响下面的逐设备保留期删除。
	if ctx.Err() == nil {
		r.commandExecutions.runOnceLogged(ctx)
	}
	if ctx.Err() == nil {
		r.commandOutboxes.runOnceLogged(ctx)
	}
	if ctx.Err() == nil {
		r.commandAttempts.runOnceLogged(ctx)
	}

	// 运行期无界增长表 §2.3 (本批最后一个缺口): security_audit_events 单一时间窗
	// 清理 (730 天, 按 created_at)。它是 append-only 审计证据, 无状态档位, 因此与
	// 上面六个清理器不同 —— 没有白名单, 整表按时间删 (与 notification_cleanup 同型)。
	// 放在命令域之后不是任意的: 本表 request_id 指向 command_executions.command_id
	// (INV-6), 同一轮里先清命令域再清审计, 审计行【最后】消失。
	if ctx.Err() == nil {
		r.audit.runOnceLogged(ctx)
	}

	// 运行期无界增长表 §2.2: node_events 单一时间窗清理 (400 天, 按 created_at)。
	// 本表【不分层】—— 99.6% 是 offline, 而 offline 恰恰是运维时间线的主内容, 按
	// event_type 分档等于删掉时间线本身 (§2.2 理由 1); 它是本批八张表里唯一保存
	// "已不存在的节点"历史的表, 因此单表 DELETE、零级联, 绝不碰 nodes (INV-3)。
	// 放在最后 (第 8 个) 不是任意的: 本表与命令域/审计没有跨表引用, 排在其后是为了
	// 让"无引用关系的最慢增长表"不插在命令域与审计之间 (那两者有 INV-6 的先后约定)。
	if ctx.Err() == nil {
		r.nodeEvents.runOnceLogged(ctx)
	}

	// 分区 DROP 之后，边界分区里仍可能有「整月未到期但单行已过期」的数据:
	// 用系统级 cutoff 做一次精确分批 DELETE（全表，不再按设备 scope）。
	//
	// 永久保留（days<=0）时**整段跳过**：既没有 cutoff 可算，也不该删任何行。
	// 注意 RowsDeleted 保持 0 而不是省略 —— 结果形状是 # 个元素、每个 Days 字段
	// 都照常填（Days=0 就是「永久」这个事实本身），让调用方/日志能看出本轮为什么没删。
	res := RetentionResult{Days: days, Permanent: permanent}
	if !permanent && ctx.Err() == nil {
		deleted, derr := r.deleteExpiredRows(ctx, days, r.now())
		res.RowsDeleted = deleted
		if derr != nil {
			res.Err = derr.Error()
			metrics.LifecycleTaskFailures.WithLabelValues("retention").Inc()
		}
		if deleted > 0 {
			metrics.LifecyclePurgedRows.WithLabelValues("retention").Add(float64(deleted))
		}
	}
	return []RetentionResult{res}, nil
}

// dropExpiredPartitionsOnce performs the per-run GLOBAL monthly partition reap
// for every partitioned time-series table (unified_data, device_data,
// telemetry_frame) (v3.4 §3.2.1). It is intentionally the ONLY place retention
// drops partitions.
// No-op unless at least one of those tables is a partitioned PostgreSQL table.
//
// cutoff 语义: 分区 DROP 是不可分割的全局整月操作 —— 只有当一个分区的最后一天
// 都已早于 cutoff 时才可删除（DropPartitionsBeforeFor 内部按分区上界判断）。
// 系统级单一窗口下 cutoff 对全库唯一，因此不再需要「取所有所有者中最长保留期」
// 那层推导（那是 per-identity retention 的产物）。
func (r *RetentionTask) dropExpiredPartitionsOnce(ctx context.Context, now time.Time, days int) error {
	// 永久保留（days<=0）不得 DROP 任何分区：调用方已经跳过，这里再挡一次，
	// 因为「0 天窗口」会算出 cutoff = now，把当下所有分区全判成过期 ——
	// 这是本函数最危险的参数错误，必须在函数自己身上也 fail-closed。
	if days <= 0 {
		return nil
	}
	// 各表先判断是否已分区: 未分区 (非 PG / 迁移未完成) 的表跳过, 一张表未分区
	// 不影响另一张。SQLite 下 IsTablePartitioned 恒 false → 整体 no-op。
	//
	// 表清单来自 partitionLifecycleTables()（telemetry_frame_lifecycle.go）——
	// L2 帧表成为主路径后必须与 unified_data/device_data 同列，否则"帧数据永不
	// 清理"（评审 B-3）。清单只给表名：分区 DROP 与列名无关（按分区名 YYYYMM 判定）。
	var partitioned []string
	for _, table := range partitionLifecycleTables() {
		if IsTablePartitioned(r.db, table) {
			partitioned = append(partitioned, table)
		}
	}
	if len(partitioned) == 0 {
		return nil // 非 PG / 均未分区: 分区机制 no-op (SQLite 主路径不受影响)
	}
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
	pm := NewPartitionManager(r.db)
	for _, table := range partitioned {
		if _, err := pm.DropPartitionsBeforeFor(table, cutoff); err != nil {
			return fmt.Errorf("retention drop %s partitions: %w", table, err)
		}
	}
	return nil
}

// deleteExpiredRows batch-deletes rows older than the system-level retention
// cutoff across **all** time-series tables (unified_data / device_data /
// telemetry_frame).
//
// 与分区 DROP 的区别: 分区是整月粒度，边界分区里可能有「整月未到期但单行已过期」
// 的数据；本函数用「时间列 < cutoff」精确删除它们（帧表的时间列是 ts）。
// 每批独立事务 + 批间 sleep，避免长事务持锁与 WAL 膨胀。
//
// 注意本函数不再带任何 scope 条件 —— 逻辑设备层移除后不存在「按身份取子集」的
// 需求；全库同一窗口。
func (r *RetentionTask) deleteExpiredRows(ctx context.Context, days int, now time.Time) (int64, error) {
	// 永久保留（days<=0）不得删任何行：同 dropExpiredPartitionsOnce 的二次防御，
	// days=0 算出的 cutoff 就是 now，会把整表删空。
	if days <= 0 {
		return 0, nil
	}
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
	batchSize := r.batchSize
	if batchSize <= 0 {
		batchSize = purgeBatchSizePostgres
		if r.db.Dialector != nil && r.db.Dialector.Name() != "postgres" {
			batchSize = purgeBatchSizeSQLite
		}
	}
	var total int64
	// 按表分支而不是把表名塞进同一条 SQL：帧表没有 id 列、时间列叫 ts、
	// 主键是 (device_id, ts, seq) —— 用 unified_data 的 "WHERE id IN (...) AND
	// timestamp < ?" 会直接报列不存在（评审 B-3 / 契约 §4 的 ⚠）。
	for _, table := range existingTimeseriesTables(r.db) {
		for batch := 0; batch < maxPurgeBatches; batch++ {
			var affected int64
			err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				n, derr := deleteExpiredRowsFromTable(tx, table, cutoff, batchSize)
				affected = n
				return derr
			})
			if err != nil {
				return total, fmt.Errorf("retention delete %s: %w", table.Name, err)
			}
			total += affected
			if affected < int64(batchSize) {
				break // 本表到期行删尽
			}
			if r.batchSleep > 0 {
				select {
				case <-ctx.Done():
					return total, ctx.Err()
				case <-time.After(r.batchSleep):
				}
			}
		}
	}
	return total, nil
}
