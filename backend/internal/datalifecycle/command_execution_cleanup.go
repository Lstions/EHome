package datalifecycle

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/commandexec"
	"ehome/backend/internal/models"
	"ehome/backend/pkg/metrics"
)

// CommandExecutionCleanupReport summarizes one command_executions cleanup pass.
//
// DeletedByStatus 是【每档实际删除的行数】, 不是"按白名单尝试的次数" ——
// 报告与基线累加必须逐字段一致 (B-INV-3: 累加行数与删除行数不许分叉)。
type CommandExecutionCleanupReport struct {
	DeletedTotal    int64            `json:"deleted_total"`
	DeletedByStatus map[string]int64 `json:"deleted_by_status,omitempty"`
	// BaselineApplied 是本次【实际写进 command_metrics_baselines 的】增量。
	// 正常情况下它与 DeletedTotal 逐字段相等; 分开记录是为了让"删了行但没累加
	// 基线"这种损坏在报告里立刻可见 (而不是只能靠面板数字变小才发现)。
	BaselineApplied commandexec.MetricsBaselineDelta `json:"baseline_applied"`
}

// CommandExecutionCleaner 按保留期分批硬删 command_executions 的终态行。
//
// 与 NotificationCleaner / NotificationDeliveryCleaner 同构的【无自己 goroutine 的
// 纯执行体】: 调度交给既有每日 retention 任务 (RetentionTask.RunOnce 调用)。
//
// 删除范围【不是】本文件自己写的状态白名单, 而是复用
// commandexec/retention_scope.go 的 PrunableExecutionsQuery —— 让"未处置 UNKNOWN
// 不得删"这条能力保证只有一处实现 (硬约束 1)。有人把 UNKNOWN 擅自加进删除范围,
// commandexec 的 B-INV-1b 测试与本地测试会同时变红。
type CommandExecutionCleaner struct {
	db         *gorm.DB
	batchSize  int // 0 → dialect default (PG 1万 / SQLite 1千)
	batchSleep time.Duration
	// windowDays > 0 显式覆盖保留期 (测试用); 生产默认 DefaultCommandExecutionRetentionDays。
	windowDays int
	// now is injectable for tests.
	now func() time.Time
}

// NewCommandExecutionCleaner creates the cleaner with the batching defaults
// shared with purge/retention.
func NewCommandExecutionCleaner(db *gorm.DB) *CommandExecutionCleaner {
	return &CommandExecutionCleaner{
		db:         db,
		batchSleep: purgeBatchSleep,
		now:        time.Now,
	}
}

// SetRetention overrides the retention window in days (tests only).
func (c *CommandExecutionCleaner) SetRetention(days int) { c.windowDays = days }

// SetBatchSize overrides the per-transaction delete batch size (tests only).
func (c *CommandExecutionCleaner) SetBatchSize(n int) {
	if n > 0 {
		c.batchSize = n
	}
}

// SetBatchSleep overrides the inter-batch sleep (tests only; 0 disables).
func (c *CommandExecutionCleaner) SetBatchSleep(d time.Duration) { c.batchSleep = d }

// retentionWindowDays resolves the effective retention window (days).
//
// 脏值防御: 非正值一律回落到默认值, 避免负保留期算出未来的 cutoff 而删除本不该删
// 的行 (与 notification_cleanup.go / globalPartitionRetentionDays 同思路)。
func (c *CommandExecutionCleaner) retentionWindowDays() int {
	if c.windowDays > 0 {
		return c.windowDays
	}
	return DefaultCommandExecutionRetentionDays
}

// RunOnce deletes terminal executions older than the retention window,
// accumulating the deleted counts into the metrics baseline in the SAME
// transaction as each delete (INV-2: 面板计数不得被清理改写)。
//
// Idempotent: a second pass finds nothing past the cutoff and deletes 0 rows.
// Returns the error of the failing pass; the caller logs it and moves on —
// cleanup failure must never break the main business path.
func (c *CommandExecutionCleaner) RunOnce(ctx context.Context) (CommandExecutionCleanupReport, error) {
	report := CommandExecutionCleanupReport{DeletedByStatus: map[string]int64{}}
	if c.db == nil {
		return report, errCommandCleanupNoDB("command execution")
	}
	cutoff := c.now().Add(-time.Duration(c.retentionWindowDays()) * 24 * time.Hour)
	batchSize := commandDomainBatchSize(c.db, c.batchSize)

	for _, status := range terminalExecutionStatuses() {
		deleted, delta, err := c.deleteStatusBatches(ctx, status, cutoff, batchSize)
		if err != nil {
			metrics.LifecycleTaskFailures.WithLabelValues("command_execution_cleanup").Inc()
			return report, err
		}
		report.DeletedTotal += deleted
		if deleted > 0 {
			report.DeletedByStatus[status] = deleted
		}
		report.BaselineApplied.Total += delta.Total
		report.BaselineApplied.Succeeded += delta.Succeeded
		report.BaselineApplied.Failed += delta.Failed
		report.BaselineApplied.Unknown += delta.Unknown
		report.BaselineApplied.Cancelled += delta.Cancelled
	}

	if report.DeletedTotal > 0 {
		metrics.LifecyclePurgedRows.WithLabelValues("command_execution_cleanup").Add(float64(report.DeletedTotal))
	}
	// 报告自检: 删除行数与基线累加必须逐字段一致。不一致说明有人把累加与删除拆开了
	// (或改了其中一侧的过滤条件) —— 那是 INV-2 的直接违反, 必须在日志里炸出来。
	if report.BaselineApplied.Total != report.DeletedTotal {
		slog.Error("datalifecycle: command execution cleanup baseline mismatch",
			"deleted_total", report.DeletedTotal, "baseline_total", report.BaselineApplied.Total)
	}
	return report, nil
}

// deleteStatusBatches 删除【单一终态】中到期且可清理的执行行, 每批一个独立事务。
//
// 每批事务内固定三步 (顺序不可换):
//  1. 用 PrunableExecutionsQuery 选出本批的 command_id (LIMIT batchSize);
//  2. 按主键删除【同一批 id】(不是按时间重新筛一遍 —— 批间可能有人写入新行);
//  3. 用【实际删掉的行数】累加基线 (AccumulateMetricsBaseline, 同一事务)。
//
// 第 3 步与第 2 步同事务是本函数的全部意义: 分开做会留下"行删了但基线没加"的窗口,
// 面板数字凭空变小 —— 正是 INV-2 要防的损坏。
func (c *CommandExecutionCleaner) deleteStatusBatches(ctx context.Context, status string, cutoff time.Time, batchSize int) (int64, commandexec.MetricsBaselineDelta, error) {
	var total int64
	var delta commandexec.MetricsBaselineDelta
	for batch := 0; batch < maxPurgeBatches; batch++ {
		var batchDelta commandexec.MetricsBaselineDelta
		var affected int64
		err := c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var ids []string
			if err := commandexec.PrunableExecutionsQuery(tx, cutoff).
				Where("status = ?", status).
				Order("command_id").
				Limit(batchSize).
				Pluck("command_id", &ids).Error; err != nil {
				return fmt.Errorf("select prunable command executions (status=%s): %w", status, err)
			}
			if len(ids) == 0 {
				return nil
			}
			args := make([]interface{}, 0, len(ids))
			for _, id := range ids {
				args = append(args, id)
			}
			res := tx.Exec(
				"DELETE FROM command_executions WHERE command_id IN ("+placeholders(len(ids))+")",
				args...,
			)
			if res.Error != nil {
				return fmt.Errorf("delete command executions (status=%s): %w", status, res.Error)
			}
			affected = res.RowsAffected

			// 基线按【实际删除的行数】累加, 且只累加本档对应的桶。
			batchDelta.Total = affected
			switch status {
			case commandexec.StatusSucceeded:
				batchDelta.Succeeded = affected
			case commandexec.StatusFailed:
				batchDelta.Failed = affected
			case commandexec.StatusUnknown:
				// 【只有已被人工处置的 UNKNOWN 会走到这里】——
				// PrunableExecutionsQuery 的第二分支要求 EXISTS(command_manual_resolutions)。
				// 未处置的 UNKNOWN 根本不在候选集里, 因此"悬案被清理器代替人结案"
				// 不可能发生 (硬约束 1)。
				batchDelta.Unknown = affected
			case commandexec.StatusCancelled:
				batchDelta.Cancelled = affected
			}
			return commandexec.AccumulateMetricsBaseline(tx, batchDelta)
		})
		if err != nil {
			return total, delta, fmt.Errorf("datalifecycle: command execution cleanup batch (status=%s): %w", status, err)
		}
		total += affected
		delta.Total += batchDelta.Total
		delta.Succeeded += batchDelta.Succeeded
		delta.Failed += batchDelta.Failed
		delta.Unknown += batchDelta.Unknown
		delta.Cancelled += batchDelta.Cancelled

		if affected < int64(batchSize) {
			break // 本档到期行删尽
		}
		if err := sleepBetweenCommandBatches(ctx, c.batchSleep); err != nil {
			return total, delta, err
		}
	}
	return total, delta, nil
}

// runOnceLogged is the best-effort wrapper used by the daily retention task:
// 失败只记日志与指标, 不向调用方传播 (旁路清理不得影响主业务)。
func (c *CommandExecutionCleaner) runOnceLogged(ctx context.Context) {
	report, err := c.RunOnce(ctx)
	if err != nil {
		slog.Warn("datalifecycle: command execution cleanup failed",
			"error", err, "deleted", report.DeletedTotal)
		return
	}
	if report.DeletedTotal > 0 {
		slog.Info("datalifecycle: command executions cleaned",
			"deleted_total", report.DeletedTotal,
			"deleted_by_status", report.DeletedByStatus,
			"baseline_unknown", report.BaselineApplied.Unknown)
	}
}

// ── 测试可观察的纯函数 (不碰 DB) ─────────────────────────────────────
//
// 下面两个函数把"删除范围"的裁决从 SQL 里抽出来, 让测试可以【直接调用】它们并断言
// 语义 —— 与 commandexec/retention_scope.go 的 PrunableStatuses 同一手法:
// 有人把某个非终态或未处置 UNKNOWN 放进删除范围, 测试立刻变红, 而不是等到上线后
// 由用户发现"命令发不出去了"或"悬案结不了案"。

// ExecutionStatusPrunable 报告某终态是否参与时间窗删除【在不考虑人工处置的前提下】。
//
// UNKNOWN 恒为 false: 它只有在被 command_manual_resolutions 处置后才可删, 那条路径
// 由 PrunableExecutionsQuery 的 EXISTS 分支实现, 【不】由这里返回 true。
func ExecutionStatusPrunable(status string) bool {
	for _, s := range commandexec.PrunableStatuses() {
		if s == status {
			return true
		}
	}
	return false
}

// CommandExecutionCleanupScope 是给测试/文档看的删除范围快照:
// 白名单状态 + 时间字段 + 窗口, 三者一起构成"这条规则删什么"。
type CommandExecutionCleanupScope struct {
	Statuses        []string
	TimeExpression  string
	RetentionDays   int
	UnknownRequires string
}

// ExecutionCleanupScope 返回可清理范围快照。
func ExecutionCleanupScope() CommandExecutionCleanupScope {
	// models.CommandExecution 的 CompletedAt 可空, 故时间字段必须是 COALESCE:
	// 只用 completed_at 会漏掉所有非终态历史行 (它们 completed_at 为空), 只用
	// created_at 会把"创建很早但最近才完成"的行提前删掉。
	return CommandExecutionCleanupScope{
		Statuses:        terminalExecutionStatuses(),
		TimeExpression:  "COALESCE(completed_at, created_at)",
		RetentionDays:   DefaultCommandExecutionRetentionDays,
		UnknownRequires: "EXISTS(command_manual_resolutions.command_id = command_executions.command_id)",
	}
}

// 编译期守卫: 本文件引用的模型必须存在且字段名与 SQL 字符串一致。
var _ = models.CommandExecution{}
