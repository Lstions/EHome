package commandexec

import (
	"time"

	"ehome/backend/internal/models"

	"gorm.io/gorm"
)

// ─ 保留策略删除范围 (清理前置条件 B, 供下一任务的清理器复用) ──
//
// 见 docs/分析/清理前置条件-冷却锚点与监控基线-2026-09-14.md §2.2.1 与 B-INV-1b。
//
// 本文件把"哪些执行行可以被时间窗删除"这条裁决【落到生产代码里】, 而不是留在
// 清理器实现者的记忆里。理由: 未处置的 UNKNOWN 一旦被删, 后果不是"少一行统计",
// 而是【这条悬案永远无法结案】—— 人工处置入口必须先加载该执行行:
//
//	// commandexec/service.go:594-600 ResolveUnknown
//	tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&execution, "command_id = ?", in.CommandID)
//	    // 行不存在 → 直接返回 error, 处置失败
//
// 因此"不删未处置的 UNKNOWN"是【能力保证】而非【统计口径】。把它写成函数并让测试
// 直接调用, 可以让"有人把 UNKNOWN 加进删除白名单"这件事立刻变红 (B-INV-1b②),
// 而不是等到上线后由用户发现悬案结不了案。

// PrunableStatuses 可参与时间窗删除的终态白名单。
//
// 【刻意不含 StatusUnknown】: UNKNOWN 只有在被 command_manual_resolutions 处置过之后
// 才可删 (见 PrunableExecutionsQuery 的第二分支); 未处置的 UNKNOWN 是"待人工结案的悬案",
// 其价值恰恰在于"还没人管" —— 按时间删掉它等于【用清理器代替人做了结案】。
//
// QUEUED/DISPATCHED/DEVICE_ACCEPTED/VERIFYING 是非终态在途行, 永不删除 (上游 INV-4)。
func PrunableStatuses() []string {
	return []string{StatusSucceeded, StatusFailed, StatusCancelled}
}

// PrunableExecutionsQuery 返回"可被保留策略按时间窗删除"的执行行查询。
//
// 三个条件缺一不可:
//  1. 已过保留期 (COALESCE(completed_at, created_at) < cutoff) —— 非终态行 completed_at 为空,
//     会退化成 created_at; 但因为第 2 条的状态白名单已排除非终态, 在途行不会被误删;
//  2. 终态白名单 (PrunableStatuses, 不含 UNKNOWN);
//  3. 【或】已经被人工处置过的 UNKNOWN (EXISTS command_manual_resolutions) ——
//     这类行已经结案, 处置能力不再依赖它存在。
//
// 调用方必须与 AccumulateMetricsBaseline 在【同一事务】内使用本查询:
// 先按它统计将被删的行与状态分布, 再删除, 再累加基线 —— 否则会留下
// "行删了但基线没加"的窗口, 面板数字凭空变小 (正是前置条件 B 要防的损坏)。
func PrunableExecutionsQuery(db *gorm.DB, cutoff time.Time) *gorm.DB {
	return db.Model(&models.CommandExecution{}).
		Where("COALESCE(completed_at, created_at) < ?", cutoff).
		Where("(status IN ?) OR (status = ? AND EXISTS (SELECT 1 FROM command_manual_resolutions cmr WHERE cmr.command_id = command_executions.command_id))",
			PrunableStatuses(), StatusUnknown)
}
