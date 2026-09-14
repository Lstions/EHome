package datalifecycle

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"ehome/backend/pkg/metrics"
)

// D-2: notifications 表此前没有任何删除路径, 随开发/运行无限增长
// (审计库 ehome_uiux 实测 19 天 3764 行且从未被清理)。本文件给出与
// retention_task.go 同构的定期清理: 已读通知保留短窗口, 未读通知保留
// 长窗口后同样清除, 避免"用户从不点开铃铛 ⇒ 表无限膨胀"的原始缺陷。
//
// 保留期来源沿用系统级保留期快照 (retention.go 的 SystemRetentionDays,
// main.go 由 config.DataRetentionDays() 写入, 默认 90 —— 与
// config.DefaultDataRetentionDays 一致)。没有新增独立配置项: notifications
// 是**系统级全局表**, 不属于任何逻辑设备, 其保留语义就是系统级保留期,
// 复用既有配置源而不是再发明一个旋钮。
const (
	// DefaultNotificationRetentionDays 是已读通知的保留天数兜底值。
	// 与 config.DefaultDataRetentionDays / retention.go init() 的 90 保持一致。
	DefaultNotificationRetentionDays = 90

	// notificationUnreadRetentionMultiplier 让未读通知保留更久。
	//
	// 裁决: **未读通知也删**, 但保留期是已读的 2 倍 (默认 180 天)。
	// 理由:
	//  1. 只删已读 = 缺陷未修复。未读数只增不减时 (用户从不点开铃铛, 或
	//     分组事件持续产生未读), 表仍然无限增长 —— 这正是 D-2 的原始症状。
	//  2. 未读 = 用户尚可能想看, 因此需要比已读更长的时间窗, 而不是等长。
	//  3. 180 天对家用/小型部署足够覆盖"半年没看通知中心"的场景。
	//  4. 告警通知 (source='alert_rule') 的已读态落在 Notification 行上
	//     (见 api/handler_alert.go 的 readAlertEvents), 删除 180 天前的
	//     未读告警通知只会让回链列表重新显示为未读 —— 告警本体仍在
	//     alert_events 表中, 不构成告警历史丢失。
	notificationUnreadRetentionMultiplier = 2
)

// NotificationCleanupReport summarizes one notification cleanup pass.
type NotificationCleanupReport struct {
	ReadDeleted   int64 `json:"read_deleted"`
	UnreadDeleted int64 `json:"unread_deleted"`
}

// NotificationCleaner 按保留期分批硬删 notifications。
//
// 它被设计成**无自己 goroutine 的纯执行体**: 调度交给既有每日 retention
// 任务 (RetentionTask.RunOnce 调用), 这样:
//   - 不新增后台协程 / 不改 cmd/server/main.go 的启动与停机编排;
//   - 清理与既有数据保留期任务同频 (每日)、同错峰, 失败只 slog.Warn,
//     绝不影响主业务与 retention 主流程。
//
// 分批 (§4.3 锁交互说明, 与 purge/retention 完全一致): 每批独立事务,
// PG 1 万行 / SQLite 1 千行, 批间 sleep 由调用方配置默认 purgeBatchSleep,
// 避免一次删除几十万行造成长事务与 WAL 膨胀。
type NotificationCleaner struct {
	db         *gorm.DB
	batchSize  int // 0 → dialect default (PG 1万 / SQLite 1千)
	batchSleep time.Duration
	// readDays / unreadDays <= 0 → 分别回落到系统级保留期与其 2 倍。
	readDays   int
	unreadDays int
	// now is injectable for tests.
	now func() time.Time
}

// NewNotificationCleaner creates the cleaner with the batching defaults
// shared with purge/retention.
func NewNotificationCleaner(db *gorm.DB) *NotificationCleaner {
	return &NotificationCleaner{
		db:         db,
		batchSleep: purgeBatchSleep,
		now:        time.Now,
	}
}

// SetRetention overrides the read/unread retention windows (days).
// Non-positive values fall back to SystemRetentionDays() and its 2x.
func (c *NotificationCleaner) SetRetention(readDays, unreadDays int) {
	c.readDays = readDays
	c.unreadDays = unreadDays
}

// SetBatchSize overrides the per-transaction delete batch size (tests only).
func (c *NotificationCleaner) SetBatchSize(n int) {
	if n > 0 {
		c.batchSize = n
	}
}

// SetBatchSleep overrides the inter-batch sleep (tests only; 0 disables).
func (c *NotificationCleaner) SetBatchSleep(d time.Duration) { c.batchSleep = d }

// retentionWindows resolves the effective (read, unread) retention in days.
// 未显式配置时读系统级保留期快照 (默认 90), 未读 = 已读 × 2。
// 脏值防御: 非正值一律回落到默认值, 避免负保留期算出未来 cutoff 而删除
// 本不该删的通知 (与 globalPartitionRetentionDays 的防御同思路)。
func (c *NotificationCleaner) retentionWindows() (readDays, unreadDays int) {
	readDays = c.readDays
	if readDays <= 0 {
		readDays = SystemRetentionDays()
	}
	if readDays <= 0 {
		readDays = DefaultNotificationRetentionDays
	}
	unreadDays = c.unreadDays
	if unreadDays <= 0 {
		unreadDays = readDays * notificationUnreadRetentionMultiplier
	}
	if unreadDays < readDays {
		unreadDays = readDays // 未读不得比已读先被清理
	}
	return readDays, unreadDays
}

// RunOnce deletes read notifications older than the read retention window and
// unread notifications older than the (longer) unread window.
//
// Idempotent: a second pass finds nothing past the cutoff and deletes 0 rows.
// Returns the error of the failing pass; the caller logs it and moves on —
// cleanup failure must never break the main business path.
func (c *NotificationCleaner) RunOnce(ctx context.Context) (NotificationCleanupReport, error) {
	var report NotificationCleanupReport
	if c.db == nil {
		return report, fmt.Errorf("datalifecycle: notification cleanup requires a db")
	}
	readDays, unreadDays := c.retentionWindows()
	now := c.now()

	// 已读: 短窗口。已读 = 用户已消费, 无回看价值的最低风险下限。
	deleted, err := c.deleteBefore(ctx, true, now.Add(-time.Duration(readDays)*24*time.Hour))
	if err != nil {
		metrics.LifecycleTaskFailures.WithLabelValues("notification_cleanup").Inc()
		return report, err
	}
	report.ReadDeleted = deleted

	// 未读: 长窗口 (默认 180 天)。见 notificationUnreadRetentionMultiplier 裁决说明。
	deleted, err = c.deleteBefore(ctx, false, now.Add(-time.Duration(unreadDays)*24*time.Hour))
	if err != nil {
		metrics.LifecycleTaskFailures.WithLabelValues("notification_cleanup").Inc()
		return report, err
	}
	report.UnreadDeleted = deleted

	if total := report.ReadDeleted + report.UnreadDeleted; total > 0 {
		metrics.LifecyclePurgedRows.WithLabelValues("notification_cleanup").Add(float64(total))
	}
	return report, nil
}

// deleteBefore batch-deletes notifications with read = isRead and
// created_at < cutoff. Each batch is an independent transaction; batches
// sleep in between to bound WAL pressure and lock hold time.
func (c *NotificationCleaner) deleteBefore(ctx context.Context, isRead bool, cutoff time.Time) (int64, error) {
	batchSize := c.batchSize
	if batchSize <= 0 {
		batchSize = purgeBatchSizePostgres
		if c.db.Dialector != nil && c.db.Dialector.Name() != "postgres" {
			batchSize = purgeBatchSizeSQLite
		}
	}
	var total int64
	for batch := 0; batch < maxPurgeBatches; batch++ {
		var affected int64
		err := c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			// 与 purge/retention 同一分批范式: DELETE ... WHERE id IN
			// (SELECT id ... LIMIT ?), PG 与 SQLite 行为一致。
			res := tx.Exec(
				"DELETE FROM notifications WHERE id IN (SELECT id FROM notifications WHERE read = ? AND created_at < ? LIMIT ?)",
				isRead, cutoff, batchSize,
			)
			affected = res.RowsAffected
			return res.Error
		})
		if err != nil {
			return total, fmt.Errorf("datalifecycle: notification cleanup batch (read=%t): %w", isRead, err)
		}
		total += affected
		if affected < int64(batchSize) {
			break // 到期行删尽
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
func (c *NotificationCleaner) runOnceLogged(ctx context.Context) {
	report, err := c.RunOnce(ctx)
	if err != nil {
		slog.Warn("datalifecycle: notification cleanup failed",
			"error", err, "read_deleted", report.ReadDeleted)
		return
	}
	if report.ReadDeleted > 0 || report.UnreadDeleted > 0 {
		slog.Info("datalifecycle: notifications cleaned",
			"read_deleted", report.ReadDeleted, "unread_deleted", report.UnreadDeleted)
	}
}
