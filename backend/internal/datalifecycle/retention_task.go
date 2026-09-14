package datalifecycle

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
	"ehome/backend/internal/notify"
	"ehome/backend/pkg/metrics"
)

// retention 通知文案 (§4.2): 到期前 30/7 天各一条, 文案含延长保留期入口。
// 标题固定用于每日任务的去重查询 (同设备同层级窗口期内不重复发)。
const (
	retentionNoticeTitle30 = "数据保留期即将到期（剩余 30 天）"
	retentionNoticeTitle7  = "数据保留期即将到期（剩余 7 天）"
)

// RetentionResult reports one logical device's retention outcome.
type RetentionResult struct {
	LogicalID    uint   `json:"logical_id"`
	NotifiedTier int    `json:"notified_tier,omitempty"` // 30 / 7 / 0
	RowsDeleted  int64  `json:"rows_deleted"`
	Err          string `json:"error,omitempty"`
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
	// notifySink 是**通知写入 + 投递**入口 (D-1 步骤 3): main.go 经 SetNotifier
	// 注入 notify.Dispatcher, 由它写 notifications 行并顺带外发投递。
	//
	// 字段名不叫 notifier 是因为它与上面的 D-2 清理器 *NotificationCleaner* 撞名
	// (两者语义相反: 一个写通知, 一个删通知), 保留不同名字避免误读。
	// **nil 是显式支持的合法状态**: 既有测试直接 NewRetentionTask(db) 不注入时
	// 回落为直接写 notifications 表, 行为与改造前逐字节一致。
	notifySink notify.Notifier
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
				"logical_id", res.LogicalID, "error", res.Err)
			continue
		}
		if res.RowsDeleted > 0 {
			slog.Info("datalifecycle: retention expired rows deleted",
				"logical_id", res.LogicalID, "rows_deleted", res.RowsDeleted)
		}
		if res.NotifiedTier > 0 {
			slog.Info("datalifecycle: retention expiry notice sent",
				"logical_id", res.LogicalID, "tier_days", res.NotifiedTier)
		}
	}
}

// RunOnce applies retention policy to every eligible logical device.
// Eligibility guards (与 purge 任务对齐):
//   - purge_requested=TRUE → purge 任务负责 (其数据将被整体删除)
//   - pending 合并参与者 (目标或源) → 顺延至合并终态 (搬迁窗口内数据
//     归属在移动, 保留期删除会误伤未搬迁分片或应用错误的 retention)
func (r *RetentionTask) RunOnce(ctx context.Context) ([]RetentionResult, error) {
	var devices []models.LogicalDevice
	if err := r.db.WithContext(ctx).
		Where("purge_requested = ?", false).
		Order("id").
		Find(&devices).Error; err != nil {
		metrics.LifecycleTaskFailures.WithLabelValues("retention").Inc()
		return nil, fmt.Errorf("datalifecycle: scan logical devices: %w", err)
	}
	// 全局分区清扫 (v3.4 §3.2.1): 每轮仅一次, 不再按设备各做一次。
	//
	// 为什么必须上移且不能按单设备 retention: DropPartitionsBeforeFor 扫描
	// unified_data_% 并整表 DROP, 是**不区分数据所有者**的全局操作; 若按某个设备
	// 的 retention 计算 cutoff, 短保留期设备会连带删除长保留期设备仍应保留的历史
	// 整月分区 (P0 静默不可恢复数据丢失)。cutoff 取所有逻辑设备中最长 retention,
	// 保证一个分区只有在它对每一个数据所有者都已到期时才被删除。
	//
	// 清扫失败不得中断逐设备的 scope 精确 DELETE: 只记指标 + 日志, 继续。
	if err := r.dropExpiredPartitionsOnce(ctx, r.now()); err != nil {
		metrics.LifecycleTaskFailures.WithLabelValues("retention").Inc()
		slog.Error("datalifecycle: retention global partition sweep failed; continuing per-device deletes",
			"error", err)
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

	results := make([]RetentionResult, 0, len(devices))
	for i := range devices {
		select {
		case <-ctx.Done():
			return results, ctx.Err()
		default:
		}
		res := r.processOne(ctx, &devices[i])
		if res.Err != "" {
			metrics.LifecycleTaskFailures.WithLabelValues("retention").Inc()
		}
		if res.RowsDeleted > 0 {
			metrics.LifecyclePurgedRows.WithLabelValues("retention").Add(float64(res.RowsDeleted))
		}
		results = append(results, res)
	}
	return results, nil
}

// dropExpiredPartitionsOnce performs the per-run GLOBAL monthly partition reap
// for every partitioned time-series table (unified_data, device_data)
// (v3.4 §3.2.1). It is intentionally the ONLY place retention drops partitions.
// No-op unless at least one of those tables is a partitioned PostgreSQL table.
//
// cutoff 语义 (防回归, 见 deleteExpired 注记): 分区 DROP 是不可分割的全局整月
// 操作, 一个分区只有对**每一个**数据所有者都已到期时才可删除, 因此 cutoff 取
// now - max(retention_days) (所有逻辑设备中**最长**的保留期)。若取较短保留期
// (或按单设备取), 会在处理该设备时连带 DROP 掉长保留期设备仍需要的历史整月
// 分区, 造成跨设备、静默、不可恢复的数据丢失。两张表共用同一 cutoff, 语义不变。
func (r *RetentionTask) dropExpiredPartitionsOnce(ctx context.Context, now time.Time) error {
	// 各表先判断是否已分区: 未分区 (非 PG / 迁移未完成) 的表跳过, 一张表未分区
	// 不影响另一张。SQLite 下 IsTablePartitioned 恒 false → 整体 no-op。
	var partitioned []string
	for _, table := range []string{"unified_data", "device_data"} {
		if IsTablePartitioned(r.db, table) {
			partitioned = append(partitioned, table)
		}
	}
	if len(partitioned) == 0 {
		return nil // 非 PG / 均未分区: 分区机制 no-op (SQLite 主路径不受影响)
	}
	days, ok, err := r.globalPartitionRetentionDays(ctx)
	if err != nil {
		return err
	}
	if !ok {
		// 无逻辑设备 = 无数据所有者: 保守起见不动任何分区 (宁可多留, 不可误删)。
		return nil
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

// globalPartitionRetentionDays returns the retention window (days) governing the
// global partition sweep: the LONGEST retention_days across ALL logical_devices
// (including purge_requested ones — 保守)。
//
// retention_days <= 0 的脏数据按 1 天处理, 避免负值算出未来 cutoff 而删除本不该
// 删的分区。无设备行 (MAX 为 NULL) 时 ok=false, 调用方跳过清扫。
func (r *RetentionTask) globalPartitionRetentionDays(ctx context.Context) (days int, ok bool, err error) {
	var longest *int
	if scanErr := r.db.WithContext(ctx).
		Raw("SELECT MAX(retention_days) FROM logical_devices").
		Scan(&longest).Error; scanErr != nil {
		return 0, false, fmt.Errorf("scan logical devices max retention_days: %w", scanErr)
	}
	if longest == nil {
		return 0, false, nil
	}
	days = *longest
	if days <= 0 {
		days = 1
	}
	return days, true, nil
}

func (r *RetentionTask) processOne(ctx context.Context, ld *models.LogicalDevice) RetentionResult {
	result := RetentionResult{LogicalID: ld.ID}

	pending, err := isPendingMergeParticipant(ctx, r.db, ld.ID)
	if err != nil {
		result.Err = err.Error()
		return result
	}
	if pending {
		return result // 顺延 (无错误): 合并终态后下轮处理
	}

	scope, err := ResolveScope(r.db, ld.ID)
	if err != nil {
		result.Err = err.Error()
		return result
	}

	oldest, err := scopeOldestTimestamp(ctx, r.db, scope)
	if err != nil {
		result.Err = err.Error()
		return result
	}
	if oldest == nil {
		return result // 无数据, 保留期不适用
	}

	now := r.now()
	expiry := oldest.Add(time.Duration(ld.RetentionDays) * 24 * time.Hour)
	remaining := expiry.Sub(now)

	switch {
	case remaining > 30*24*time.Hour:
		// 未临期: 无需通知或清理。
	case remaining > 7*24*time.Hour:
		if sent := r.notifyExpiry(ctx, ld, retentionNoticeTitle30, 30, remaining); sent {
			result.NotifiedTier = 30
		}
	case remaining > 0:
		if sent := r.notifyExpiry(ctx, ld, retentionNoticeTitle7, 7, remaining); sent {
			result.NotifiedTier = 7
		}
	default:
		// 到期: 分批硬删 (§4.3 任务 1)。通知窗口已过, 文案已明确告知
		// 不可恢复; 此处只执行删除。
		deleted, derr := r.deleteExpired(ctx, scope, ld.RetentionDays, now)
		if derr != nil {
			result.Err = derr.Error()
			return result
		}
		result.RowsDeleted = deleted
	}
	return result
}

// notifyExpiry sends the tier notification unless the same (device, tier)
// notification was already sent within the tier window (每日任务去重)。
// Returns true when a notification was created.
func (r *RetentionTask) notifyExpiry(ctx context.Context, ld *models.LogicalDevice, title string, tierDays int, remaining time.Duration) bool {
	sourceID := strconv.FormatUint(uint64(ld.ID), 10)
	windowStart := r.now().Add(-time.Duration(tierDays) * 24 * time.Hour)
	var existing int64
	if err := r.db.WithContext(ctx).Model(&models.Notification{}).
		Where("source = ? AND source_id = ? AND title = ? AND created_at > ?",
			NotificationSourceRetentionExpiring, sourceID, title, windowStart).
		Count(&existing).Error; err != nil {
		slog.Warn("datalifecycle: retention notice dedup query failed",
			"logical_id", ld.ID, "error", err)
		return false
	}
	if existing > 0 {
		return false
	}
	daysLeft := int(remaining.Hours()/24) + 1
	n := models.Notification{
		Type:        "warning",
		Title:       title,
		Message:     fmt.Sprintf("逻辑设备「%s」（%s）最早的数据将在约 %d 天后到期硬删除，删除后不可恢复。", ld.Name, ld.DeviceType, daysLeft),
		Description: fmt.Sprintf("如需保留更久，请到「逻辑设备」管理页调大该设备的保留天数（当前 %d 天）。", ld.RetentionDays),
		Source:      NotificationSourceRetentionExpiring,
		SourceID:    sourceID,
	}
	if !r.createNotification(ctx, &n, ld.ID) {
		return false
	}
	return true
}

// SetNotifier 注入"通知写入 + 投递"入口 (D-1 步骤 3, main.go 接线)。
// 传 nil 显式回落为直接写 notifications 表 (见 notifySink 字段注释)。
func (r *RetentionTask) SetNotifier(n notify.Notifier) {
	r.notifySink = n
}

// createNotification 落库一条通知 (D-1 步骤 3: 经注入的 Notifier 或直接落库)。
//
// nil 回落是**显式设计**, 既有测试直接 NewRetentionTask(db) 不注入时行为不变。
// 调用点 (notifyExpiry) 不在事务内, 且在业务写完成之后, 因此同步投递安全
// (详见 notify/dispatcher.go 的 Create 投递时机裁决)。
func (r *RetentionTask) createNotification(ctx context.Context, n *models.Notification, logicalID uint) bool {
	if r.notifySink != nil {
		r.notifySink.Create(ctx, n)
		return n.ID != 0
	}
	if err := r.db.WithContext(ctx).Create(n).Error; err != nil {
		slog.Error("datalifecycle: create retention notification failed",
			"logical_id", logicalID, "error", err)
		return false
	}
	return true
}

// deleteExpired batch-deletes rows older than the retention cutoff.
// Same batching strategy as purge (§4.3 锁交互说明).
func (r *RetentionTask) deleteExpired(ctx context.Context, scope *Scope, retentionDays int, now time.Time) (int64, error) {
	cutoff := now.Add(-time.Duration(retentionDays) * 24 * time.Hour)

	// 防回归 (P0 数据丢失, 2026-09): 本函数**严禁**再调用
	// DropPartitionsBefore / DropPartitionsBeforeFor。
	//
	// 为什么: 分区 DROP 是全局整月粒度操作 (DropPartitionsBeforeFor 扫描
	// unified_data_%, 不区分该分区里的数据属于哪个逻辑设备), 而本函数的 cutoff
	// 由**单一 scope 的 retentionDays** 算出。若在此做全局 DROP, 处理短保留期
	// 设备 A(retention_days=30) 时, 会把长保留期设备 B(365) 仍应保留的历史整月
	// 分区**整表 DROP**, 连带删除 B 的数据 —— 静默、不可恢复, 且任何一方都不会
	// 收到错误提示。
	//
	// 全局分区清扫已上移到 RunOnce (每轮一次), 并以**所有**逻辑设备中**最长**的
	// retention_days 计算 cutoff, 保证一个分区只有对每一个数据所有者都已到期时
	// 才被删除。本函数只保留按 scope 限定的精确 DELETE 批次 (安全, 不变)。
	batchSize := r.batchSize
	if batchSize <= 0 {
		batchSize = purgeBatchSizePostgres
		if r.db.Dialector != nil && r.db.Dialector.Name() != "postgres" {
			batchSize = purgeBatchSizeSQLite
		}
	}
	cond, args := scope.Cond()

	var total int64
	for _, table := range []string{"unified_data", "device_data"} {
		for batch := 0; batch < maxPurgeBatches; batch++ {
			var affected int64
			err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				res := tx.Exec(
					fmt.Sprintf("DELETE FROM %s WHERE id IN (SELECT id FROM %s WHERE %s AND timestamp < ? LIMIT ?)", table, table, cond),
					append(append([]interface{}{}, args...), cutoff, batchSize)...,
				)
				affected = res.RowsAffected
				return res.Error
			})
			if err != nil {
				return total, fmt.Errorf("retention delete %s: %w", table, err)
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
