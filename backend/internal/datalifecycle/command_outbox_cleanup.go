package datalifecycle

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"ehome/backend/pkg/metrics"
)

// CommandOutboxCleanupReport summarizes one command_outboxes cleanup pass.
type CommandOutboxCleanupReport struct {
	Deleted int64 `json:"deleted"`
}

// CommandOutboxCleaner 按保留期分批硬删 command_outboxes 的终态行。
//
// 这是三张表里【唯一可以短留】的一张 (30 天), 依据是设计 §2.5 实跑过的等价性验证
// (INV-9): outbox 的两条"唯一事实"在别处都有第二份记录 ——
//   - payload_json 逐字节等于 execution.params_json (断言 A, 实测 3776/3776 相等);
//   - processed_at ≈ attempt.published_at (断言 B, 【容差】关系: 实测仅 11/3773
//     精确相等, 最大差 93µs, 且无一条 outbox 早于对应 attempt)。
//
// ⚠️ 断言 B 不得写成等号: 同一事务内两次独立的 time.Now()
// (dispatcher.go:211 的 processed_at 与传输层的 PublishedAt) 本就不保证相等。
// 写等号会永久变红, 从而把一个【本质上冗余】的表误判为"不冗余", 反向阻止一个正确
// 的清理 —— 与"不变式过松"同样是缺陷 (设计 §INV-9 的自我修正)。
//
// ⚠️ 失效条件 (必须如实记录): 一旦 dispatcher.go:190 的 AttemptNo: 1 不再硬编码
// (一次命令可产生多条 outbox/attempt), 一对多映射会让 processed_at 不再与唯一
// attempt 对应, 本判定必须立即重新评估。
type CommandOutboxCleaner struct {
	db         *gorm.DB
	batchSize  int // 0 → dialect default (PG 1万 / SQLite 1千)
	batchSleep time.Duration
	// windowDays > 0 显式覆盖保留期 (测试用)。
	windowDays int
	// now is injectable for tests.
	now func() time.Time
}

// NewCommandOutboxCleaner creates the cleaner with the batching defaults
// shared with purge/retention.
func NewCommandOutboxCleaner(db *gorm.DB) *CommandOutboxCleaner {
	return &CommandOutboxCleaner{
		db:         db,
		batchSleep: purgeBatchSleep,
		now:        time.Now,
	}
}

// SetRetention overrides the retention window in days (tests only).
func (c *CommandOutboxCleaner) SetRetention(days int) { c.windowDays = days }

// SetBatchSize overrides the per-transaction delete batch size (tests only).
func (c *CommandOutboxCleaner) SetBatchSize(n int) {
	if n > 0 {
		c.batchSize = n
	}
}

// SetBatchSleep overrides the inter-batch sleep (tests only; 0 disables).
func (c *CommandOutboxCleaner) SetBatchSleep(d time.Duration) { c.batchSleep = d }

// retentionWindowDays resolves the effective retention window (days).
func (c *CommandOutboxCleaner) retentionWindowDays() int {
	if c.windowDays > 0 {
		return c.windowDays
	}
	return DefaultCommandOutboxRetentionDays
}

// RunOnce deletes terminal outboxes older than the retention window.
//
// 两个条件缺一不可 (设计 §5 档位 5):
//  1. outbox 自身已终态 (state IN ('PROCESSED','CANCELLED'), 白名单);
//  2. 【对应的 execution 不处于非终态】—— 一条 outbox 已 PROCESSED 但 execution
//     还在 VERIFYING 时, 它仍是排查"这条在途命令当时怎么发出去的"的唯一线索;
//     而 execution 行已不存在时 (被 730 天清理或运维删除) 该 outbox 是孤儿, 可删
//     —— 否则它永远删不掉, 表重新无界增长。
//
// 【绝不级联】: 只 DELETE command_outboxes 一张表。删 outbox 不得连带删除
// execution / attempt (INV-3), 三表之间实测零 DB 外键 —— 若有人加了
// ON DELETE CASCADE, TestCommandDomain_DeletingOutboxDoesNotCascade 会红。
//
// Idempotent; 失败只由调用方 slog.Warn + 指标。
func (c *CommandOutboxCleaner) RunOnce(ctx context.Context) (CommandOutboxCleanupReport, error) {
	var report CommandOutboxCleanupReport
	if c.db == nil {
		return report, errCommandCleanupNoDB("command outbox")
	}
	cutoff := c.now().Add(-time.Duration(c.retentionWindowDays()) * 24 * time.Hour)
	batchSize := commandDomainBatchSize(c.db, c.batchSize)
	states := outboxTerminalStates()
	if len(states) == 0 {
		// 白名单为空 = 不删任何行 (fail-closed, 绝不退化成无 WHERE 的 DELETE)。
		return report, nil
	}

	// SQL 是【全静态】的 (白名单由 placeholders 展开成 ?, 不拼接用户输入):
	//   - 时间字段用 COALESCE(processed_at, created_at): processed_at 可空
	//     (PENDING 从没被处理过), 只用它会漏删, 只用 created_at 会把"入队很早但
	//     最近才发布"的行提前删掉。终态行的 processed_at 一定非空, 两者对终态行等价,
	//     但 COALESCE 让语义在"脏终态行"上也保持保守 (取更早的时刻)。
	//   - 第二段写成 NOT EXISTS(非终态 execution), 而不是 EXISTS(终态 execution):
	//     两者对"execution 还活着且终态"的行等价, 但对"execution 行已被删掉"的行
	//     截然不同 —— EXISTS 形式会让这类 outbox 成为【永远删不掉的孤儿】
	//     (execution 清理器 730 天先删, 或运维整体清理后, outbox 的删除条件再也
	//     无法成立 ⇒ 表重新无界增长, 正是本任务要消除的东西)。行不存在 ⇒ 不可能
	//     有在途命令 ⇒ 终态 outbox 可以删。
	//   - 判定用【非终态黑名单取反】(status NOT IN 终态白名单): 将来若新增终态,
	//     它会被当成"非终态"从而【保留】outbox —— fail-closed, 与 execution 清理器
	//     对未知状态的保守取向一致。
	execStatuses := terminalExecutionStatuses()
	query := fmt.Sprintf(
		"DELETE FROM command_outboxes WHERE id IN ("+
			"SELECT o.id FROM command_outboxes o WHERE o.state IN (%s) "+
			"AND COALESCE(o.processed_at, o.created_at) < ? "+
			"AND NOT EXISTS (SELECT 1 FROM command_executions e WHERE e.command_id = o.command_id AND e.status NOT IN (%s)) "+
			"ORDER BY o.id LIMIT ?)",
		placeholders(len(states)), placeholders(len(execStatuses)),
	)
	args := make([]interface{}, 0, len(states)+len(execStatuses)+2)
	for _, s := range states {
		args = append(args, s)
	}
	args = append(args, cutoff)
	for _, s := range execStatuses {
		args = append(args, s)
	}
	args = append(args, batchSize)

	for batch := 0; batch < maxPurgeBatches; batch++ {
		var affected int64
		err := c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			res := tx.Exec(query, args...)
			affected = res.RowsAffected
			return res.Error
		})
		if err != nil {
			metrics.LifecycleTaskFailures.WithLabelValues("command_outbox_cleanup").Inc()
			return report, fmt.Errorf("datalifecycle: command outbox cleanup batch: %w", err)
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
		metrics.LifecyclePurgedRows.WithLabelValues("command_outbox_cleanup").Add(float64(report.Deleted))
	}
	return report, nil
}

// runOnceLogged is the best-effort wrapper used by the daily retention task.
func (c *CommandOutboxCleaner) runOnceLogged(ctx context.Context) {
	report, err := c.RunOnce(ctx)
	if err != nil {
		slog.Warn("datalifecycle: command outbox cleanup failed",
			"error", err, "deleted", report.Deleted)
		return
	}
	if report.Deleted > 0 {
		slog.Info("datalifecycle: command outboxes cleaned", "deleted", report.Deleted)
	}
}
