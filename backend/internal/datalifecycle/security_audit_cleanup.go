package datalifecycle

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"ehome/backend/pkg/metrics"
)

// security_audit_events 单一时间窗清理器。
//
// 裁决 (docs/分析/运行期无界增长表-保留策略设计-2026-09-13.md §2.3, 原文摘录):
//
//	"裁决: 统一保留 730 天 (2 年), 按 created_at 删。"
//	"保留期必须由产品/合规拍板, 工程默认值取 730 天并作为可配置常量 (非法值
//	 fail-closed 回落到该值, 不允许 0/负数表示「永久」——那会重新打开无界增长)。"
//	"为什么不是 90 天": 本表的消费者是【未来的争议调查】, 不是当前的排障;
//	 "上季度某次密码重置是不是管理员本人做的" 这类问题恰好总是在 90 天后才被提出。
//	"为什么不是「永久」": 永久保留 = 无界增长重新抬头, 且没有任何一条业务规则
//	 需要 2 年以上。
//	"可实现性": created_at 有独立索引, DELETE ... WHERE created_at < ? 可索引扫描,
//	 无需新增索引。
//	INV-6: 本表 request_id → command_executions.command_id (无 DB 外键), 故本表窗口
//	 【不得长于】command_executions; 设计取齐为 730 天。
//
// 本文件补的是本批八张无界增长表里【最后一个缺口】: §2.3 的裁决 2026-09-13 就已落下,
// 但清理器一直未实现 (实测: 表 3797 行 / 1.9 MB, 无任何删除路径, 随运行持续增长)。
//
// ─ 与命令域清理器的两处结构性不同 (都由表本身决定, 不是新造机制) ──────
//
//  1. 【无状态白名单, 整表按时间删】。命令域三表按 status/state 分档, 因为表里混有
//     "在途 / 待人工处置"的行; 本表是 append-only 审计事件 (模型自述), 没有状态机,
//     §2.3 的裁决就是"统一保留 730 天"、不分档。这里【刻意不】引入档位 —— 那正是
//     设计 §3 专项要防的"为一个自己发明的档位付解释成本"。
//     实测 audit.Writer 的 result 取值有 queued / cancelled / issued / success
//     (commandexec/service.go:527,663, commandexec/confirmation.go:166, auth/*.go),
//     没有任何"未处置待办"取值 —— 与 automation_events 的 pending_confirm 不同型,
//     因此不存在需要显式排除的受保护取值。
//     与 notification_cleanup.go 同型: 它同样是"整表按 created_at 删, 无白名单"。
//
//  2. 【保留期是本地常量, 不跟系统级保留期】。系统级保留期 (SystemRetentionDays,
//     默认 90) 是"逻辑设备遥测数据"的语义; 审计保留期按 §2.3 只能由产品/合规决定,
//     套用 90 天会让 §2.3 第 2 条明确拒绝的那类追问永远答不出来。
//
// ─ 与既有清理器同构 (照抄范式, 不新造) ───────────────────────────────
//
//   - 无自己 goroutine 的纯执行体: 挂在既有每日 RetentionTask.RunOnce 上 (不新增
//     goroutine、不改 cmd/server/main.go 的启停编排);
//   - 分批: PG 1 万 / SQLite 1 千, 每批独立事务, 批间 sleep (默认 purgeBatchSleep),
//     避免一次删除几十万行造成长事务与 WAL 膨胀 (设计 §4.3 锁交互说明);
//   - 幂等: 再跑一轮删 0 行;
//   - 旁路: 失败只 slog.Warn + 指标, 绝不影响主业务与逐设备 retention 主流程;
//   - 单表 DELETE, 不级联 (与 command_domain_cleanup.go 的 INV-3 同约定)。
const (
	// DefaultSecurityAuditRetentionDays 是 security_audit_events 的保留期 (天)。
	//
	// 730 = 设计 §2.3 的裁决值 (2 年)。它必须与
	// DefaultCommandExecutionRetentionDays 取齐 (INV-6): 本表 request_id 指向
	// command_executions.command_id, 本表窗口更长会在超出部分产生"指向一条已不存在的
	// 命令"的孤证 (审计还在, 被审计对象没了)。由
	// TestSecurityAuditCleaner_RetentionMatchesCommandExecution 钉住。
	DefaultSecurityAuditRetentionDays = 730
)

// SecurityAuditCleanupReport summarizes one security_audit_events cleanup pass.
type SecurityAuditCleanupReport struct {
	Deleted int64 `json:"deleted"`
	// RetentionDays 是本次【实际生效】的窗口 (天)。报告里带上它, 让"非法值 fail-closed
	// 回落"在日志里可观测 —— 否则没人能区分"删了 0 行因为没到期"与"窗口被配成了 730
	// 以外的值"。
	RetentionDays int `json:"retention_days"`
}

// SecurityAuditCleaner 按保留期分批硬删 security_audit_events (整表按 created_at)。
//
// 删除条件只有一条: created_at < now() - retentionWindowDays()*24h。
// 其余任何列 (result / event_name / actor_type ...) 都不参与判定 —— §2.3 的裁决是
// "按 created_at 删", 不是"按事件类型删"。测试
// TestSecurityAuditCleaner_DeletesByCreatedAtOnly 用一个"created_at 旧但别的时间列新"
// 的对照钉住这条。
type SecurityAuditCleaner struct {
	db         *gorm.DB
	batchSize  int // 0 → dialect default (PG 1万 / SQLite 1千)
	batchSleep time.Duration
	// windowDays 是显式覆盖的保留期 (天)。> 0 时生效; 非正值【不做"永久保留"解释】,
	// 一律 fail-closed 回落到 DefaultSecurityAuditRetentionDays —— 这是设计 §2.3 的
	// 明文要求 ("不允许 0/负数表示「永久」——那会重新打开无界增长")。
	windowDays int
	// now is injectable for tests.
	now func() time.Time
}

// NewSecurityAuditCleaner creates the cleaner with the batching defaults shared
// with purge/retention.
func NewSecurityAuditCleaner(db *gorm.DB) *SecurityAuditCleaner {
	return &SecurityAuditCleaner{
		db:         db,
		batchSleep: purgeBatchSleep,
		now:        time.Now,
	}
}

// SetRetention overrides the retention window in days (测试/运维注入用)。
//
// ⚠️ 非正值【不是】"永久保留": 它被 retentionWindowDays 以 fail-closed 方式回落到
// DefaultSecurityAuditRetentionDays。把 0/负数解释成永久 = 重新打开无界增长, 正是
// §2.3 明文禁止的取值语义。
func (c *SecurityAuditCleaner) SetRetention(days int) { c.windowDays = days }

// SetBatchSize overrides the per-transaction delete batch size (tests only).
func (c *SecurityAuditCleaner) SetBatchSize(n int) {
	if n > 0 {
		c.batchSize = n
	}
}

// SetBatchSleep overrides the inter-batch sleep (tests only; 0 disables).
func (c *SecurityAuditCleaner) SetBatchSleep(d time.Duration) { c.batchSleep = d }

// retentionWindowDays resolves the effective retention window (days).
//
// 脏值防御: 非正值 (0 与负数) 一律 fail-closed 回落到
// DefaultSecurityAuditRetentionDays —— 这是设计 §2.3 的明文要求:
//
//   - 0 【不得】被解释为"永久保留" (那会重新打开无界增长, 正是 §2.3 拒绝的语义);
//   - 负数会算出【未来】的 cutoff, 把窗内的行也一起删掉 (静默数据丢失)。
//
// 正值按原样生效 (保留期是 §2.3 要求的"可配置常量")。⚠️ 调用方若把它调到大于
// DefaultCommandExecutionRetentionDays, 就会产生 §2.3/INV-6 描述的孤证审计行
// (request_id 指向一条已被清理的命令) —— 生产路径不覆盖该值, 常量级取齐由
// TestSecurityAuditCleaner_RetentionMatchesCommandExecution 钉住。
func (c *SecurityAuditCleaner) retentionWindowDays() int {
	if c.windowDays > 0 {
		return c.windowDays
	}
	return DefaultSecurityAuditRetentionDays
}

// RunOnce deletes audit rows older than the retention window.
//
// 幂等: 再跑一轮找不得到期行, 删 0 行。
// 返回失败批次的错误; 调用方 (runOnceLogged) 只记日志 —— 清理失败绝不影响主业务。
func (c *SecurityAuditCleaner) RunOnce(ctx context.Context) (SecurityAuditCleanupReport, error) {
	report := SecurityAuditCleanupReport{}
	if c.db == nil {
		return report, errCommandCleanupNoDB("security audit event")
	}
	days := c.retentionWindowDays()
	report.RetentionDays = days
	cutoff := c.now().Add(-time.Duration(days) * 24 * time.Hour)

	deleted, err := c.deleteBefore(ctx, cutoff)
	if err != nil {
		metrics.LifecycleTaskFailures.WithLabelValues("security_audit_cleanup").Inc()
		return report, err
	}
	report.Deleted = deleted

	if report.Deleted > 0 {
		metrics.LifecyclePurgedRows.WithLabelValues("security_audit_cleanup").Add(float64(report.Deleted))
	}
	return report, nil
}

// deleteBefore batch-deletes audit rows with created_at < cutoff.
//
// 每批一个独立事务; 批间 sleep 以限制 WAL 膨胀与锁持有时间。分批范式与
// notification_cleanup.go / command_attempt_cleanup.go 完全一致:
// DELETE ... WHERE id IN (SELECT id ... ORDER BY id LIMIT ?) —— PG 与 SQLite 行为相同
// (PG 侧另见 security_audit_cleanup_pg 跑批)。
//
// ORDER BY id 让批次按主键顺序推进: 没有它, 同一条 SQL 在两种方言下选出的"这批"
// 可能不同 (PostgreSQL 无 ORDER BY 的 LIMIT 不保证稳定), 幂等断言会因此偶发。
func (c *SecurityAuditCleaner) deleteBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	batchSize := commandDomainBatchSize(c.db, c.batchSize)

	var total int64
	for batch := 0; batch < maxPurgeBatches; batch++ {
		var affected int64
		err := c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			res := tx.Exec(
				"DELETE FROM security_audit_events WHERE id IN (SELECT id FROM security_audit_events WHERE created_at < ? ORDER BY id LIMIT ?)",
				cutoff, batchSize,
			)
			affected = res.RowsAffected
			return res.Error
		})
		if err != nil {
			return total, fmt.Errorf("datalifecycle: security audit cleanup batch: %w", err)
		}
		total += affected
		if affected < int64(batchSize) {
			break // 到期行删尽
		}
		if err := sleepBetweenCommandBatches(ctx, c.batchSleep); err != nil {
			return total, err
		}
	}
	return total, nil
}

// runOnceLogged is the best-effort wrapper used by the daily retention task:
// 失败只记日志与指标, 不向调用方传播 (旁路清理不得影响主业务)。
func (c *SecurityAuditCleaner) runOnceLogged(ctx context.Context) {
	report, err := c.RunOnce(ctx)
	if err != nil {
		slog.Warn("datalifecycle: security audit cleanup failed",
			"error", err, "deleted", report.Deleted, "retention_days", report.RetentionDays)
		return
	}
	if report.Deleted > 0 {
		slog.Info("datalifecycle: security audit events cleaned",
			"deleted", report.Deleted, "retention_days", report.RetentionDays)
	}
}
