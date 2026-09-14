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

// automation_events 分层保留清理器 (设计裁决: docs/分析/运行期无界增长表-保留策略设计-2026-09-13.md §2.1)。
//
//	为什么这张表不能"一刀切 N 天" ───────────────────────────────────
//
// automation_events 不是只读审计, 它是【热状态的持久层】: 全仓有 7 处业务判定在
// 读它 (前置条件文档 docs/分析/清理前置条件-冷却锚点与监控基线-2026-09-14.md §1.1
// 证据 4 + 本轮复核):
//
//	1 evaluator.go:119-183 rebuildCooldowns  启动重建冷却 —— 已由 automation_rules.last_triggered_at
//	                                         锚点物理解耦 (前置条件 A, 提交 1d55cf6b),
//	                                         删事件行不再影响冷却 (A-INV-1);
//	2 planner.go:93-97    日熔断 (自动)      当日 trigger_source 不限的 executed 计数;
//	3 planner.go:175-179  幂等键序号 (自动)  同日 executed 计数 → automation:<rule>:<yyyymmdd>:<seq+1>;
//	4 planner.go:583-598  日熔断 (手动)      与 2 同口径;
//	5 planner.go:605-625  手动冷却抑制 (DB 兜底) 最近一条 executed/pending_confirm 的 triggered_at;
//	6 planner.go:703-712  幂等键序号 (手动)  当日 executed AND trigger_source='manual' 计数;
//	7 planner.go:900-917  sweepExpiredPending 24h 前的 pending_confirm → expired (UPDATE, 非删除)。
//
// 逐条判定 (本轮复核结论, 全部以 [triggered_at] 为唯一时间列, 与清理器同列):
//
//		2/3/4/6 (当日计数): 读窗口 [dayStart, ∞) 由【本地时区当日 00:00】起算, 跨度 < 1 天;
//		  本清理器任何档位的窗口都 >= automationEventMinRetentionDays (2 天, 见下), 被删行
//		  必然早于 now-2d, 因此必然早于 dayStart(max(dayStart, now-1d)) —— 两个区间不可能相交。
//		  字段一致性: 清理按 triggered_at, 计数也按 triggered_at (不是 created_at), 无错位;
//		  两者只在"triggered_at 早于 created_at"的补写场景下有偏差, 而补写方向是【更早】,
//		  即更远离当日窗口, 只会更安全。
//		  注: 每日熔断是"同一自然日累积"的软限流, 不是审计不变式; 即便未来有人缩短窗口,
//		  受影响量级也是"阈值附近的一次放行", 不是静默数据损坏。
//
//		5 (手动冷却): 已由前置条件 A 的 cooldownBase 改为锚点优先 (planner.go:656-679),
//		  事件表只在锚点为空 (锚点列上线前的历史数据/回填尚未跑到) 时兜底。
//		  ⇒ 400 天执行档删的是远超冷却窗上界 (cooldown_sec <= 86400, handler_automation.go:652)
//		  的旧行; 且**清理器绝不写/删 automation_rules.last_triggered_at** (锚点在另一张表)。
//
//		7 (sweepExpiredPending): 依赖 pending_confirm 行【存在】。TTL 是 24h, 而本表最短档位
//		  30 天 —— "看起来" sweep 总会先把它改成 expired。**但这个依赖是不可接受的**:
//		  StartCleanup 是 main.go:294 起的独立 goroutine (每 5 分钟一轮), 它 panic / 未启动 /
//		  上下文提前取消时行就永不 sweep; 未来有人调大 TTL 或改周期, 本清理器的安全性会
//		  静默改变而它自己一个字没动 (与 §3.17/§3.21 "把正确性寄托在别处" 同型)。
//		  ⇒ 因此 pending_confirm **显式排除**: 它不在任何一档的可删白名单里, 且由
//		  automationEventProtectedResults + assertResultsExcludeProtected 在删除前强制校验
//		  (若有人把它加进白名单, RunOnce 直接 fail-closed 报错并【完全不删】, 而不是静默过滤)。
//		  pending_confirm 是"待人工确认的待办" (models/automation.go:40), 消费方是
//		  ConfirmEvent (planner.go:785-792: 行不存在 → ErrConfirmEventNotFound), 删掉 = 抹掉
//		  一个待办 —— 与命令域"未处置的 UNKNOWN" (commandexec/retention_scope.go) 同型。
//		  本清理器【不依赖】StartCleanup 存活; 两者是彼此独立的保护。
//
//	 分层保留裁决 (§2.1, 不是"一刀切") ────────────────────────────
//
//		A 非执行档 (遥测/诊断): 30 天  —— 被抑制/条件失效/门禁拒绝的瞬时判断, 30 天后无人追问;
//		B 执行档 (executed):    400 天  —— 承载设备动作归因 ("这条规则上个月为什么开了一次阀门").
//
// 400 天的技术下界由 cooldown_sec 上界 (86400 = 1 天) 决定, 而不是由偏好决定:
// "重启回填是否正确"只依赖最近 1 天的 executed 行 —— 但该依赖已被前置条件 A 移除
// (锚点在规则行上), 所以 400 天是为【事后追责】留的窗口, 不是为冷却算的。
// 执行档比非执行档长 13 倍是有意的: 把两者拉平成一个数字 = 要么丢掉设备动作归因
// (取 30), 要么让 99% 的诊断噪声跟着留 400 天 (取 400) —— 两者都与裁决相反。
//
//	白名单, 不是黑名单 (fail-closed) ──────────────────────────────
//
// DELETE 语句由白名单生成 (result IN (...)), 照 notification_delivery_cleanup.go 的
// state IN (...) 范式。白名单之外的任何取值 —— 含 pending_confirm 与**将来新增的
// result 取值** —— 都不在删除范围内, 即使它们比窗口老得多。
// 审计清理对"看不懂的东西"必须保留: 黑名单 (result <> 'executed') 会在新增一个
// 终态时把它静默当垃圾删掉, 而白名单只会让它多留一段时间 (可事后追加裁决)。
//
// ─ 与既有清理器同构 ──────────────────────────────────────────────
//
// 分批 (PG 1 万 / SQLite 1 千, 每批独立事务, 批间 sleep)、幂等、旁路 (失败只
// slog.Warn + 指标)、挂在既有每日 RetentionTask.RunOnce 上: 不新增 goroutine、
// 不改 cmd/server/main.go 的启动与停机编排。
const (
	// DefaultAutomationEventTelemetryRetentionDays 是【非执行档】的保留期 (天):
	// suppressed_cooldown / suppressed_daily_limit / condition_changed / failed_gate /
	// failed_dispatch / notification / expired。承载"为什么没触发"的诊断价值, 30 天后
	// 无人追问 (§2.1 档位 A)。
	DefaultAutomationEventTelemetryRetentionDays = 30

	// DefaultAutomationEventExecutionRetentionDays 是【执行档】的保留期 (天):
	// 仅 result='executed'。承载"这条规则上一次真正动设备是什么时候"的归因历史
	// (§2.1 档位 B)。技术下界是 cooldown_sec 上界 (1 天), 400 天是追责窗口。
	DefaultAutomationEventExecutionRetentionDays = 400

	// automationEventMinRetentionDays 是所有档位窗口的【下界】(天)。
	//
	// 它不是"再留宽一点"的偏好, 而是让"当日业务判定不受清理影响"成为结构性质:
	// 日熔断/幂等序号只看当日 (dayStart..now, 跨度 < 1 天), 手动冷却只看最近一条
	// executed/pending_confirm。窗口 >= 2 天给出 1 天的时区/时钟冗余 ——
	// 任何被删的行都必然早于"本地时区今日 00:00", 与当日判定区间不可能相交。
	// 非正值/越界值一律回落到至少本值 (脏值防御, 与 globalPartitionRetentionDays 同思路:
	// 错误的短窗口会算出未来的 cutoff 而删除本不该删的行)。
	automationEventMinRetentionDays = 2
)

// automationEventRetentionClass 是一档分层保留策略: 一组 result 取值 + 一个窗口。
//
// results 是**白名单**: 只有列出的 result 会被删除, DELETE 语句由它生成。
// 白名单之外的任何取值 (含 pending_confirm 与将来新增的 result) 都不在删除范围内。
type automationEventRetentionClass struct {
	label      string   // 报告/日志用的人类可读标签 ("telemetry" / "execution")
	results    []string // 参与删除的 result 白名单
	windowDays int      // 该档的默认保留期 (天), 生产默认路径使用
}

// automationEventRetentionClasses 返回 automation_events 的分层保留档位 (单一事实源:
// 白名单与窗口都只在这里定义, 清理语句与测试断言都从它派生)。
func automationEventRetentionClasses() []automationEventRetentionClass {
	return []automationEventRetentionClass{
		{
			// 档位 A: 非执行档 (诊断/遥测) —— 30 天。
			// 逐个列出而不写 "NOT IN ('executed')": 白名单之外一律保留 (fail-closed)。
			label: "telemetry",
			results: []string{
				models.AutomationResultSuppressedCooldown,
				models.AutomationResultSuppressedDailyLimit,
				models.AutomationResultConditionChanged,
				models.AutomationResultFailedGate,
				models.AutomationResultFailedDispatch,
				models.AutomationResultNotification,
				models.AutomationResultExpired, // 24h 超时清扫的产物, 已是终态, 非"待办"
			},
			windowDays: DefaultAutomationEventTelemetryRetentionDays,
		},
		{
			// 档位 B: 执行档 —— 400 天。
			// 刻意不含 pending_confirm: 它是待人工确认的待办, 见文件头第 7 条与
			// automationEventProtectedResults。
			label:      "execution",
			results:    []string{models.AutomationResultExecuted},
			windowDays: DefaultAutomationEventExecutionRetentionDays,
		},
	}
}

// automationEventProtectedResults 是【永不可被时间窗删除】的 result 取值集合。
//
// 它与白名单【分开列出】并由 assertResultsExcludeProtected 在删除前强制校验:
// 若有人把其中任一取值加进白名单, RunOnce 会 fail-closed 地报错并完全不删,
// 而不是静默把它过滤掉 —— 静默过滤会让"白名单里到底有什么"变得不可观测,
// 从而让守卫要保护的那条测试永远绿 (本仓 §6.5 两个"首次漏网"变异正是此类缺口)。
func automationEventProtectedResults() []string {
	return []string{
		// 待人工确认的待办: ConfirmEvent 依赖该行存在 (行不存在 → ErrConfirmEventNotFound),
		// 删掉 = 用清理器代替人把悬案结掉了。同型: commandexec 的未处置 UNKNOWN。
		models.AutomationResultPendingConfirm,
	}
}

// AutomationEventPrunableResults 返回可参与时间窗删除的 result 白名单 (两档并集)。
//
// 供调用方与测试直接断言"白名单里有什么" —— 把"哪些 result 可删"这条裁决落到
// 生产代码而不是留在实现者的记忆里 (同 commandexec/retention_scope.go 的
// PrunableStatuses 范式)。**刻意不含 pending_confirm**: 见 automationEventProtectedResults。
func AutomationEventPrunableResults() []string {
	var out []string
	for _, class := range automationEventRetentionClasses() {
		out = append(out, class.results...)
	}
	return out
}

// assertResultsExcludeProtected 校验一组白名单不含任何受保护取值。
// 返回错误时调用方必须 fail-closed 地【放弃删除】(而不是过滤后继续)。
func assertResultsExcludeProtected(results []string) error {
	for _, protected := range automationEventProtectedResults() {
		for _, r := range results {
			if r == protected {
				return fmt.Errorf(
					"datalifecycle: automation event cleanup whitelist contains protected result %q "+
						"(待人工确认的待办不得被清理, 拒绝删除本档)", protected)
			}
		}
	}
	return nil
}

// AutomationEventCleanupReport summarizes one automation_events cleanup pass.
type AutomationEventCleanupReport struct {
	TelemetryDeleted int64 `json:"telemetry_deleted"`
	ExecutionDeleted int64 `json:"execution_deleted"`
}

// Total returns the number of event rows removed in this pass.
func (r AutomationEventCleanupReport) Total() int64 {
	return r.TelemetryDeleted + r.ExecutionDeleted
}

// AutomationEventCleaner 按分层保留期分批硬删 automation_events。
//
// 与 NotificationCleaner / NotificationDeliveryCleaner 同构的**无自己 goroutine 的
// 纯执行体**: 调度交给既有每日 retention 任务 (RetentionTask.RunOnce 调用),
// 不新增后台协程、不改 cmd/server/main.go 的启动与停机编排; 失败只 slog.Warn + 指标。
type AutomationEventCleaner struct {
	db         *gorm.DB
	batchSize  int // 0 → dialect default (PG 1万 / SQLite 1千)
	batchSleep time.Duration
	// telemetryDays / executionDays > 0 显式覆盖对应档位窗口 (测试用)。
	// **生产默认路径不设它**: 窗口是设计裁决的常量, 不存在运行期配置旋钮
	// (与 notification 侧"沿用系统级保留期"不同 —— automation 事件归规则所有,
	// 系统级保留期是"逻辑设备数据"的语义, 套用会让两个不同口径互相污染)。
	telemetryDays int
	executionDays int
	// now is injectable for tests.
	now func() time.Time
}

// NewAutomationEventCleaner creates the cleaner with the batching defaults
// shared with purge/retention.
func NewAutomationEventCleaner(db *gorm.DB) *AutomationEventCleaner {
	return &AutomationEventCleaner{
		db:         db,
		batchSleep: purgeBatchSleep,
		now:        time.Now,
	}
}

// SetRetention overrides the tier windows in days (tests only; 非正值回落到默认值,
// 且任何值都不得低于 automationEventMinRetentionDays)。
func (c *AutomationEventCleaner) SetRetention(telemetryDays, executionDays int) {
	c.telemetryDays = telemetryDays
	c.executionDays = executionDays
}

// SetBatchSize overrides the per-transaction delete batch size (tests only).
func (c *AutomationEventCleaner) SetBatchSize(n int) {
	if n > 0 {
		c.batchSize = n
	}
}

// SetBatchSleep overrides the inter-batch sleep (tests only; 0 disables).
func (c *AutomationEventCleaner) SetBatchSleep(d time.Duration) { c.batchSleep = d }

// retentionWindowDays resolves the effective window (days) of one class.
//
// 默认 = 设计裁决的档位常量; 显式覆盖仅测试使用。脏值防御 (两道):
//  1. 非正值 → 回落默认值 (避免负保留期算出未来的 cutoff 而删除本不该删的行);
//  2. 任何结果都不得低于 automationEventMinRetentionDays (保住"当日判定区间
//     与删除区间不相交"这条结构性质, 见该常量注释)。
func (c *AutomationEventCleaner) retentionWindowDays(class automationEventRetentionClass) int {
	days := class.windowDays
	switch class.label {
	case "telemetry":
		if c.telemetryDays > 0 {
			days = c.telemetryDays
		}
	case "execution":
		if c.executionDays > 0 {
			days = c.executionDays
		}
	}
	if days < automationEventMinRetentionDays {
		days = automationEventMinRetentionDays
	}
	return days
}

// RunOnce deletes event rows older than their tier's window.
//
// Idempotent: a second pass finds nothing past the cutoff and deletes 0 rows.
// Returns the error of the failing pass; the caller logs it and moves on —
// cleanup failure must never break the main business path.
func (c *AutomationEventCleaner) RunOnce(ctx context.Context) (AutomationEventCleanupReport, error) {
	var report AutomationEventCleanupReport
	if c.db == nil {
		return report, fmt.Errorf("datalifecycle: automation event cleanup requires a db")
	}
	// 结构性守卫: 白名单里混入受保护取值时 fail-closed 报错并【完全不删】
	// (不是静默过滤 —— 静默过滤会让白名单内容不可观测, 见 automationEventProtectedResults)。
	if err := assertResultsExcludeProtected(AutomationEventPrunableResults()); err != nil {
		metrics.LifecycleTaskFailures.WithLabelValues("automation_event_cleanup").Inc()
		return report, err
	}

	for _, class := range automationEventRetentionClasses() {
		days := c.retentionWindowDays(class)
		cutoff := c.now().Add(-time.Duration(days) * 24 * time.Hour)
		deleted, err := c.deleteBefore(ctx, class.results, cutoff)
		if err != nil {
			metrics.LifecycleTaskFailures.WithLabelValues("automation_event_cleanup").Inc()
			return report, err
		}
		switch class.label {
		case "telemetry":
			report.TelemetryDeleted = deleted
		case "execution":
			report.ExecutionDeleted = deleted
		}
	}

	if total := report.Total(); total > 0 {
		metrics.LifecyclePurgedRows.WithLabelValues("automation_event_cleanup").Add(float64(total))
	}
	return report, nil
}

// deleteBefore batch-deletes event rows whose result is in the whitelist and whose
// [triggered_at] is older than cutoff.
//
// 时间列是 triggered_at (与全部 7 处业务判定读同一列, 不是 created_at):
// 两者在正常路径同值 (记录时显式传 at), 但 created_at 在补写/维护脚本场景可能不同步;
// 与读侧同列保证"清理窗口"与"业务判定窗口"是同一把尺子。
//
// Each batch is an independent transaction; batches sleep in between to bound WAL
// pressure and lock hold time. 与 notification_cleanup.go/purge.go 同一分批范式:
// DELETE ... WHERE id IN (SELECT id ... LIMIT ?) —— PG 与 SQLite 行为一致
// (PG-only 验证见 automation_event_cleanup_test.go 的 PG 跑批记录)。
func (c *AutomationEventCleaner) deleteBefore(ctx context.Context, results []string, cutoff time.Time) (int64, error) {
	if len(results) == 0 {
		// 白名单为空 = 该档不删任何行 (fail-closed, 绝不退化成无 WHERE 的 DELETE)。
		return 0, nil
	}
	if err := assertResultsExcludeProtected(results); err != nil {
		return 0, err
	}
	batchSize := c.batchSize
	if batchSize <= 0 {
		batchSize = purgeBatchSizePostgres
		if c.db.Dialector != nil && c.db.Dialector.Name() != "postgres" {
			batchSize = purgeBatchSizeSQLite
		}
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(results)), ",")
	inner := fmt.Sprintf("SELECT id FROM automation_events WHERE result IN (%s) AND triggered_at < ? LIMIT ?", placeholders)
	outer := fmt.Sprintf("DELETE FROM automation_events WHERE id IN (%s)", inner)
	args := make([]interface{}, 0, len(results)+2)
	for _, r := range results {
		args = append(args, r)
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
			return total, fmt.Errorf("datalifecycle: automation event cleanup batch (results=%v): %w", results, err)
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
func (c *AutomationEventCleaner) runOnceLogged(ctx context.Context) {
	report, err := c.RunOnce(ctx)
	if err != nil {
		slog.Warn("datalifecycle: automation event cleanup failed",
			"error", err, "deleted", report.Total())
		return
	}
	if report.Total() > 0 {
		slog.Info("datalifecycle: automation events cleaned",
			"telemetry_deleted", report.TelemetryDeleted,
			"execution_deleted", report.ExecutionDeleted)
	}
}
