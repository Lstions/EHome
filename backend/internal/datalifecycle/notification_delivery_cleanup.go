package datalifecycle

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/metrics"
)

// D-1 裁决 3 的欠账补齐 (本文件)。
//
// 裁决原文 (docs/分析/未发布产品负债盘点-2026-09-13.md:902-907):
// "notification_deliveries 是**审计记录** ... 保留 ... **代价**: 需要一条
// 清理策略 (按时间/条数), 与 notifications 表的清理需求同批处理。"
//
// D-2 只落了 notifications 的清理 (notification_cleanup.go), 漏了
// notification_deliveries —— 而它**增长快于 notifications**: 每次投递尝试
// 一行, 一条通知 × N 个通道 × M 次重试 = 1:N:M (models/notification_channel.go:180-196)。
// 因此"保留审计"若不带清理策略, 等价于把 D-2 的原始缺陷 (表随运行无限增长)
// 从 notifications 搬到一张增长更快的表上。本文件给出与 notification_cleanup.go
// **完全同构**的清理: 分批、幂等、旁路、失败仅 slog.Warn + 指标, 挂在同一个
// 每日 RetentionTask.RunOnce 上 (不新增协程、不改 main.go)。
//
// ─ 保留期裁决: 证据必须活得比被审计对象久 ─────────────────────────
//
// 不变式 (本文件的核心, 由 TestDeliveryCleanup_EvidenceOutlivesNotificationBody
// 等测试约束):
//
//	投递证据的保留期  >=  被审计对象 (通知本体) 的最长保留期
//
// 为什么: D-1 保留投递审计, 为的是事后追溯"**某条通知当时是否送达**"。
// 这个追问的**窗口长度由通知本体决定** —— 通知本体还在 (用户还能在通知中心
// 看到它、还能追问), 证据就必须还在。证据早死一天, 就是删掉 D-1 要保的东西。
//
// 一个**已被证伪的错误论据** (留档防回归): 曾以"delivered 行是 notifications
// 行的冗余第二份拷贝"为由把成功记录压到短窗 (基准/2 = 45 天)。该论据不成立 ——
// models.Notification 的字段只有 ID/Type/Message/Title/Description/Source/
// SourceID/Read/CreatedAt, **没有任何投递状态列**, 全仓也没有第二处读取投递
// 结果。因此"这条通知当时送达没有"这个事实**只存在于 notification_deliveries**:
// delivered 与 failed 同样是**唯一证据**, 不是冗余拷贝 (反证见
// TestDeliveryCleanup_EvidenceOutlivesNotificationBody 的列检查断言)。
// "成功是预期路径所以不必留" 是把**诊断**价值当成了**审计**价值 ——
// 预期路径恰恰是审计要证明的东西 ("我说发了, 证据呢")。
//
// 由此推出的具体策略:
//   - 三档 state (delivered / failed / pending) **共用同一个窗口** =
//     通知本体的最长保留期 = max(已读窗, 未读窗) = 基准 × 2 (默认 180 天)。
//   - 该窗口不是写死的数字, 而是由 notificationBodyMaxRetentionDays() **算出**的
//     (与 NotificationCleaner 共用 notificationRetentionWindows() 同一解析函数) ——
//     通知侧窗口若将来变长, 投递证据窗口自动跟随, 不变式不会因两处常量不同步而失效。
//   - 不再区分 delivered/failed 的窗口长短: 曾用于支持"delivered 可以更短"的
//     论据已被证伪; 而把某一档留得**更久** (例如 only-failures 永久保留) 会让表
//     重新无界增长 —— 那正是 D-1 明写的代价 ("需要一条清理策略") 要避免的。
//     在满足不变式的前提下, 三档等长是**最省且仍然完整**的保守解。
//   - 未知 state **永不删除** (白名单 "state IN (...)", 而非 "state <> 'delivered'"):
//     将来若新增终态 (例如 'suppressed'), 清理器不会把它当垃圾删掉 ——
//     审计清理对"看不懂的东西"必须 fail-closed 地保留。
//   - pending 与其余档同窗: pending 只在"尝试已开始、结论未落库"的崩溃窗口内
//     合法存在 (dispatcher.go:186-204), 超过整个审计窗口还是 pending 说明写结论
//     的进程崩了或行被孤立 —— 这本身是证据, 按同窗保留、同窗清理。
//   - 按时间删而不按条数删: 条数上限会按写入速率反推保留时长 (突发告警时保留期
//     被压缩到几小时), 审计保留期必须可预期、与写入量无关。
//
// 与 D-1 裁决的一致性: 裁决要求"保留这条审计证据链", 同时明写代价是"需要一条
// 清理策略"。本策略删的只是**被审计对象已经不在**的旧记录 (通知本体已按 90/180
// 天窗口清除, 追问对象消失, 证据不再可行动); 只要追问对象还在, 证据就一定还在
// (不变式)。通道/通知被删除**不**触发提前清理 —— 只按 created_at 时间窗,
// 也因此与"删通道不级联删审计" (裁决 3 的实现要求) 完全一致。
//
// 保留期来源沿用系统级保留期快照 (retention.go 的 SystemRetentionDays,
// main.go 由 config.DataRetentionDays() 写入, 默认 90) 与 notifications 的
// 未读倍数常量。**没有新增配置项**: 与 notification_cleanup.go 同源,
// 保证两张表的清理随同一个系统级旋钮变化。
const (
	// DefaultNotificationDeliveryRetentionDays 是投递审计保留期的兜底值,
	// 等于通知本体最长保留期的默认值: 基准 (90) × notificationUnreadRetentionMultiplier (2) = 180。
	DefaultNotificationDeliveryRetentionDays = DefaultNotificationRetentionDays * notificationUnreadRetentionMultiplier
)

// deliveryRetentionClass 是一档投递审计保留策略: 一组 state 取值。
//
// states 是**白名单**: 只有列出的 state 会被删除。DELETE 语句由它生成,
// 白名单之外的任何取值 (含将来新增的终态) 都不在删除范围内。
//
// 三档共用同一窗口 (notificationBodyMaxRetentionDays), 故这里不再带窗口字段 ——
// 窗口只有一个来源, 结构上不可能出现"某档偷偷比通知本体短"。
type deliveryRetentionClass struct {
	label  string   // 报告/日志用的人类可读标签 ("delivered" / "failed" / "pending")
	states []string // 参与删除的 state 白名单
}

// deliveryRetentionClasses 返回投递审计的 state 白名单分档 (三档共用审计窗口)。
func deliveryRetentionClasses() []deliveryRetentionClass {
	return []deliveryRetentionClass{
		{label: "delivered", states: []string{models.DeliveryStateDelivered}},
		{label: "failed", states: []string{models.DeliveryStateFailed}},
		{label: "pending", states: []string{models.DeliveryStatePending}},
	}
}

// NotificationDeliveryCleanupReport summarizes one delivery-audit cleanup pass.
type NotificationDeliveryCleanupReport struct {
	DeliveredDeleted int64 `json:"delivered_deleted"`
	FailedDeleted    int64 `json:"failed_deleted"`
	PendingDeleted   int64 `json:"pending_deleted"`
}

// Total returns the number of audit rows removed in this pass.
func (r NotificationDeliveryCleanupReport) Total() int64 {
	return r.DeliveredDeleted + r.FailedDeleted + r.PendingDeleted
}

// NotificationDeliveryCleaner 按保留期分批硬删 notification_deliveries。
//
// 与 NotificationCleaner 同构的**无自己 goroutine 的纯执行体**: 调度交给既有
// 每日 retention 任务 (RetentionTask.RunOnce 调用), 不新增后台协程、不改
// cmd/server/main.go 的启动与停机编排; 失败只 slog.Warn + 指标。
//
// 分批 (§4.3 锁交互说明, 与 purge/retention/notification 清理完全一致):
// 每批独立事务, PG 1 万行 / SQLite 1 千行, 批间 sleep 默认 purgeBatchSleep,
// 避免一次删除几十万行造成长事务与 WAL 膨胀。
type NotificationDeliveryCleaner struct {
	db         *gorm.DB
	batchSize  int // 0 → dialect default (PG 1万 / SQLite 1千)
	batchSleep time.Duration
	// windowDays > 0 显式覆盖审计窗口 (测试用)。
	// **生产默认路径不设它**: 窗口由通知本体的保留期解析出来, 以满足
	// "证据活得比被审计对象久"的不变式。
	windowDays int
	// now is injectable for tests.
	now func() time.Time
}

// NewNotificationDeliveryCleaner creates the cleaner with the batching
// defaults shared with purge/retention.
func NewNotificationDeliveryCleaner(db *gorm.DB) *NotificationDeliveryCleaner {
	return &NotificationDeliveryCleaner{
		db:         db,
		batchSleep: purgeBatchSleep,
		now:        time.Now,
	}
}

// SetRetention overrides the audit window in days (tests only). 生产路径不调用:
// 默认窗口由通知本体的最长保留期推导, 见 retentionWindowDays 与文件头不变式。
func (c *NotificationDeliveryCleaner) SetRetention(days int) { c.windowDays = days }

// SetBatchSize overrides the per-transaction delete batch size (tests only).
func (c *NotificationDeliveryCleaner) SetBatchSize(n int) {
	if n > 0 {
		c.batchSize = n
	}
}

// SetBatchSleep overrides the inter-batch sleep (tests only; 0 disables).
func (c *NotificationDeliveryCleaner) SetBatchSleep(d time.Duration) { c.batchSleep = d }

// retentionWindowDays resolves the effective audit retention window (days).
//
// 默认 (未显式覆盖) = **通知本体的最长保留期** = max(已读窗, 未读窗),
// 由 notificationRetentionWindows() 与 NotificationCleaner **同一函数**算出 ——
// 这就是不变式"投递证据不得短于被审计对象的最长保留期"的结构性保证:
// 通知侧窗口一变, 本窗口跟着变, 不存在两处常量各自漂移的可能。
//
// 脏值防御: 系统级快照非正值一律回落到默认值, 避免负保留期算出未来的 cutoff
// 而删除本不该删的行 (与 notification_cleanup.go / globalPartitionRetentionDays
// 同思路)。
func (c *NotificationDeliveryCleaner) retentionWindowDays() int {
	if c.windowDays > 0 {
		return c.windowDays
	}
	return notificationBodyMaxRetentionDays()
}

// RunOnce deletes audit rows older than the audit window.
//
// Idempotent: a second pass finds nothing past the cutoff and deletes 0 rows.
// Returns the error of the failing pass; the caller logs it and moves on —
// cleanup failure must never break the main business path.
func (c *NotificationDeliveryCleaner) RunOnce(ctx context.Context) (NotificationDeliveryCleanupReport, error) {
	var report NotificationDeliveryCleanupReport
	if c.db == nil {
		return report, fmt.Errorf("datalifecycle: notification delivery cleanup requires a db")
	}
	days := c.retentionWindowDays()
	cutoff := c.now().Add(-time.Duration(days) * 24 * time.Hour)

	for _, class := range deliveryRetentionClasses() {
		deleted, err := c.deleteBefore(ctx, class.states, cutoff)
		if err != nil {
			metrics.LifecycleTaskFailures.WithLabelValues("notification_delivery_cleanup").Inc()
			return report, err
		}
		switch class.label {
		case "delivered":
			report.DeliveredDeleted = deleted
		case "failed":
			report.FailedDeleted = deleted
		case "pending":
			report.PendingDeleted = deleted
		}
	}

	if total := report.Total(); total > 0 {
		metrics.LifecyclePurgedRows.WithLabelValues("notification_delivery_cleanup").Add(float64(total))
	}
	return report, nil
}

// deleteBefore batch-deletes audit rows whose state is in the whitelist and
// whose created_at is older than cutoff. Each batch is an independent
// transaction; batches sleep in between to bound WAL pressure and lock hold
// time. 与 notification_cleanup.go/purge.go 同一分批范式:
// DELETE ... WHERE id IN (SELECT id ... LIMIT ?) —— PG 与 SQLite 行为一致
// (PG-only 验证见 notification_cleanup_pg_test.go)。
func (c *NotificationDeliveryCleaner) deleteBefore(ctx context.Context, states []string, cutoff time.Time) (int64, error) {
	if len(states) == 0 {
		// 白名单为空 = 该档不删任何行 (fail-closed, 绝不退化成无 WHERE 的 DELETE)。
		return 0, nil
	}
	batchSize := c.batchSize
	if batchSize <= 0 {
		batchSize = purgeBatchSizePostgres
		if c.db.Dialector != nil && c.db.Dialector.Name() != "postgres" {
			batchSize = purgeBatchSizeSQLite
		}
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(states)), ",")
	inner := fmt.Sprintf("SELECT id FROM notification_deliveries WHERE state IN (%s) AND created_at < ? LIMIT ?", placeholders)
	outer := fmt.Sprintf("DELETE FROM notification_deliveries WHERE id IN (%s)", inner)
	args := make([]interface{}, 0, len(states)+2)
	for _, s := range states {
		args = append(args, s)
	}
	args = append(args, cutoff, batchSize)

	var total int64
	for batch := 0; batch < maxPurgeBatches; batch++ {
		var affected int64
		err := c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			res := tx.Exec(outer, args...)
			affected = res.RowsAffected
			return res.Error
		})
		if err != nil {
			return total, fmt.Errorf("datalifecycle: notification delivery cleanup batch (states=%v): %w", states, err)
		}
		total += affected
		if affected < int64(batchSize) {
			break // 本档到期行删尽
		}
		if c.batchSleep > 0 {
			select {
			case <-ctx.Done():
				return total, ctx.Err()
			case <-time.After(c.batchSleep):
			}
		}
	}
	return total, nil
}

// runOnceLogged is the best-effort wrapper used by the daily retention task:
// 失败只记日志与指标, 不向调用方传播 (旁路清理不得影响主业务)。
func (c *NotificationDeliveryCleaner) runOnceLogged(ctx context.Context) {
	report, err := c.RunOnce(ctx)
	if err != nil {
		slog.Warn("datalifecycle: notification delivery audit cleanup failed",
			"error", err, "deleted", report.Total())
		return
	}
	if report.Total() > 0 {
		slog.Info("datalifecycle: notification delivery audit cleaned",
			"delivered_deleted", report.DeliveredDeleted,
			"failed_deleted", report.FailedDeleted,
			"pending_deleted", report.PendingDeleted)
	}
}
