package datalifecycle

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"ehome/backend/pkg/metrics"
)

// CommandAttemptCleanupReport summarizes one command_attempts cleanup pass.
type CommandAttemptCleanupReport struct {
	Deleted int64 `json:"deleted"`
}

// CommandAttemptCleaner 按保留期分批硬删 command_attempts。
//
// ⚠️ 【这张表绝不可短留】(硬约束 3): attempt.BootID + attempt.WireDigest 是
// nodemgr/handler_channel_cmd_v2.go:42-46 逐字节校验设备上报帧的唯一凭据
// (matchesAttemptDigest → subtle.ConstantTimeCompare), 而
// commandexec/inbox.go:188-205 的 RecoverExpired 会把超时 DISPATCHED 的 execution
// 改写成 UNKNOWN, 抹掉"曾经发出过"这一事实 —— 于是 attempt 成了唯一的见证。
// 删 attempt = 抹掉"设备到底有没有收到这条命令"的唯一答案。
//
// 窗口因此与 command_executions 【同寿命】(730 天), 由
// TestCommandDomain_AttemptWindowNeverShorterThanExecution (INV-6) 钉住:
// 有人把 attemptWindowDays 调到 730 以下, 测试立刻变红。
//
// 【按 created_at 删, 不按 published_at】: 裁决原文 (设计 §2.6) 即按 created_at;
// 且 published_at 可空 (dispatcher 建行后才回填, 崩溃窗口内可能为空) —— 用它做
// cutoff 会让"从未回填 publish 时刻"的行永远不可删, 表重新无界增长。
type CommandAttemptCleaner struct {
	db         *gorm.DB
	batchSize  int // 0 → dialect default (PG 1万 / SQLite 1千)
	batchSleep time.Duration
	// windowDays > 0 显式覆盖保留期 (测试用)。
	windowDays int
	// now is injectable for tests.
	now func() time.Time
}

// NewCommandAttemptCleaner creates the cleaner with the batching defaults
// shared with purge/retention.
func NewCommandAttemptCleaner(db *gorm.DB) *CommandAttemptCleaner {
	return &CommandAttemptCleaner{
		db:         db,
		batchSleep: purgeBatchSleep,
		now:        time.Now,
	}
}

// SetRetention overrides the retention window in days (tests only).
func (c *CommandAttemptCleaner) SetRetention(days int) { c.windowDays = days }

// SetBatchSize overrides the per-transaction delete batch size (tests only).
func (c *CommandAttemptCleaner) SetBatchSize(n int) {
	if n > 0 {
		c.batchSize = n
	}
}

// SetBatchSleep overrides the inter-batch sleep (tests only; 0 disables).
func (c *CommandAttemptCleaner) SetBatchSleep(d time.Duration) { c.batchSleep = d }

// retentionWindowDays resolves the effective retention window (days).
func (c *CommandAttemptCleaner) retentionWindowDays() int {
	if c.windowDays > 0 {
		return c.windowDays
	}
	return DefaultCommandAttemptRetentionDays
}

// RunOnce deletes attempts older than the retention window.
//
// 与另外两个清理器不同, 本表【不按状态分档】: attempt 的物理意义是"传输层接受过的
// 这一次尝试", 其 status 只影响 execution 的状态机推进, 不影响它作为线路证据的价值。
// 按状态分档删除会引入"某档留得更久"的复杂度, 且没有任何证据支持某一档应当更短。
//
// Idempotent; returns the error of the failing pass. 单表 DELETE, 不级联
// (INV-3: 删 attempt 不得改变 command_executions / command_outboxes 行数)。
func (c *CommandAttemptCleaner) RunOnce(ctx context.Context) (CommandAttemptCleanupReport, error) {
	var report CommandAttemptCleanupReport
	if c.db == nil {
		return report, errCommandCleanupNoDB("command attempt")
	}
	cutoff := c.now().Add(-time.Duration(c.retentionWindowDays()) * 24 * time.Hour)
	batchSize := commandDomainBatchSize(c.db, c.batchSize)

	for batch := 0; batch < maxPurgeBatches; batch++ {
		var affected int64
		err := c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			res := tx.Exec(
				"DELETE FROM command_attempts WHERE id IN (SELECT id FROM command_attempts WHERE created_at < ? ORDER BY id LIMIT ?)",
				cutoff, batchSize,
			)
			affected = res.RowsAffected
			return res.Error
		})
		if err != nil {
			metrics.LifecycleTaskFailures.WithLabelValues("command_attempt_cleanup").Inc()
			return report, fmt.Errorf("datalifecycle: command attempt cleanup batch: %w", err)
		}
		report.Deleted += affected
		if affected < int64(batchSize) {
			break // 到期行删尽
		}
		if err := sleepBetweenCommandBatches(ctx, c.batchSleep); err != nil {
			return report, err
		}
	}

	if report.Deleted > 0 {
		metrics.LifecyclePurgedRows.WithLabelValues("command_attempt_cleanup").Add(float64(report.Deleted))
	}
	return report, nil
}

// runOnceLogged is the best-effort wrapper used by the daily retention task.
func (c *CommandAttemptCleaner) runOnceLogged(ctx context.Context) {
	report, err := c.RunOnce(ctx)
	if err != nil {
		slog.Warn("datalifecycle: command attempt cleanup failed",
			"error", err, "deleted", report.Deleted)
		return
	}
	if report.Deleted > 0 {
		slog.Info("datalifecycle: command attempts cleaned", "deleted", report.Deleted)
	}
}
