package commandexec

// automation_backfill.go —— 自动化审计行的**终态回填**（2026-11-20 生产缺陷修复）。
//
// ── 缺陷 ──────────────────────────────────────────────────────────────
// planner.recordExecuted 只在"命令刚提交到 commandexec"时写一行
// automation_events.result = 'executed'（models.AutomationResultExecuted）。此后命令
// 走到终态（SUCCEEDED / FAILED / UNKNOWN / CANCELLED）**没有任何代码回填**，于是
// 「自动化策略」页把"已提交"渲染成"已执行"。生产现场因此出现：规则「关闭BMS充电」
// 的命令实际 FAILED（ChannelCmdV2 final verification failed），触发历史却显示
// "已执行"，用户认为策略生效了。
//
// ── 修法 ──────────────────────────────────────────────────────────────
// 每条命令的终态**只有这里一处收口**：command_executions 的终态迁移在 commandexec 内
// 只有三个写入点 —— recordInbox（设备侧 accepted/verifying/final_*）、
// RecoverExpired（Deadline 收尾）、Cancel（操作者取消）。三处都在**同一事务**内调用
// backfillAutomationEventTx，所以"命令终态"与"自动化审计终态"不可能一半成功：
//   · 命令终态提交了、审计没回填 ⇒ 用户继续看到假的"已执行"（本缺陷）；
//   · 审计回填了、命令终态没提交 ⇒ 审计说失败而设备其实成功了。
// 事务里的错误照常抛给调用方（回滚 + inbox 事件可重传），不吞错。
//
// ── 为什么不在 automation 包里做 ──────────────────────────────────────
// commandexec 不能 import automation（automation 已 import commandexec，会成环）。
// 因此本文件只做两件事：
//   ① 用 models 包（同一 models 包，不引入新依赖方向）按 command_id 回填事件行；
//   ② 把回填结果交给**可选的观察者**（Service.SetAutomationEventObserver，由
//      cmd/server/main.go 接线到 automation.Planner.OnCommandTerminal）做 WS 广播与
//      通知。观察者为 nil（单测/仿真默认）时行为不变：回填照常写库。

import (
	"fmt"
	"strings"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/logger"

	"gorm.io/gorm"
)

// automationEventDetailColumnRunes 是 automation_events.detail 的存储预算
// （models.AutomationEvent: `gorm:"size:512"`）。
//
// 口径与 FinalReasonColumnRunes 一致：按**字符**（rune）计，并在 rune 边界截断 ——
// 超长值在 MySQL 会被静默截断、在 PostgreSQL 会直接报错，两种方言表现不同，
// 所以预算必须在写库前由这里统一强制。
const automationEventDetailColumnRunes = 512

// automationDetailSucceeded 是命令成功终态的 detail 文案。
//
// 为什么成功也要写 detail：改造前这一列对 executed 行恒为空，用户无法从审计行
// 区分"已提交、还在跑"与"设备已确认成功"。写明后，触发历史里同一 result 的两行
// 也有一句人话可说。（设备回传的完整校验结果在
// command_executions.verified_result_json，按 command_id 可查，不在本列重复。）
const automationDetailSucceeded = "设备已确认执行成功（命令终态 SUCCEEDED）"

// AutomationTerminalEvent 是一次"命令到达终态"的最小事实，供审计回填使用。
type AutomationTerminalEvent struct {
	CommandID string
	// Status 是终态字面量（StatusSucceeded / StatusFailed / StatusUnknown / StatusCancelled）。
	Status string
	// FinalReason 是 command_executions.final_reason 的**同一个值**（设备事件类型
	// 或 deadline/cancel 的原因串）。原文进 detail，便于运维直接与执行记录对齐。
	FinalReason string
	// Cause 是服务端把设备成功回传改判为失败时的判定原因（目前由 ChannelCmdV2
	// verifier 提供）。它比 final_reason 细一层：final_reason=final_failed 既可能
	// 是设备真的失败，也可能是设备成功但回读对账没通过，处置完全不同。
	Cause string
}

// AutomationEventUpdate 是回填后的自动化事件投影（WS 广播 / 通知的输入）。
//
// 刻意只带"命令维度"的字段：规则名等自动化域信息由观察者自己按 RuleID 取，
// 避免 commandexec 反向拼装另一个领域的载荷形状。
type AutomationEventUpdate struct {
	EventID   uint
	RuleID    uint
	Result    string
	Detail    string
	CommandID string
	Status    string // 命令终态，供日志/前端区分 FAILED 与 UNKNOWN
}

// Applied 报告本次是否真的回填了事件行（false = 该命令不是自动化下发，或已回填过）。
func (u AutomationEventUpdate) Applied() bool { return u.EventID != 0 }

// SetAutomationEventObserver 注入自动化事件终态回填后的观察者。
//
// 生产实现是 automation.Planner.OnCommandTerminal（cmd/server/main.go 接线）；
// nil 是显式支持的合法状态：回填照常写库，只是不广播 WS、不发通知。
//
// 观察者在**事务提交之后**、由收件箱/清扫/取消这三条路径的调用方同步调用。
func (s *Service) SetAutomationEventObserver(observer func(AutomationEventUpdate)) {
	s.automationObserver = observer
}

// publishAutomationTerminal 在事务提交后把回填结果交给观察者（fail-open）。
//
// 为什么必须 fail-open：命令终态与审计回填都已经提交，观察者只负责"让用户立刻
// 看见"（WS + 通知）。这里 panic 若向上冒，会掀翻设备收件箱的读循环 —— 一个提示
// 失败不该让设备的命令通道停摆。
func (s *Service) publishAutomationTerminal(update AutomationEventUpdate) {
	if s.automationObserver == nil || !update.Applied() {
		return
	}
	defer func() {
		if rec := recover(); rec != nil {
			logger.Errorf("commandexec: recovered panic in automation event observer: %v", rec)
		}
	}()
	s.automationObserver(update)
}

// backfillAutomationEventTx 在调用方事务内把"命令终态"回填到 automation_events。
//
// 只改 result = 'executed' 的行，两个作用：
//
//	· 幂等 —— 同一终态重复投递（设备重传 / 清扫重跑）不会再改写一次；
//	· 不越界 —— 绝不覆盖其它结果码（pending_confirm 的待办、failed_gate 的审计、
//	  人工确认后的 partial_failure 等）与已回填过的行。
//
// 匹配不到任何行是**正常**的（人工下发、告警下发、或该系统里没有任何规则共用这条
// 命令），返回零值 + nil error。
func backfillAutomationEventTx(tx *gorm.DB, terminal AutomationTerminalEvent) (AutomationEventUpdate, error) {
	var none AutomationEventUpdate
	commandID := strings.TrimSpace(terminal.CommandID)
	if tx == nil || commandID == "" || terminal.Status == "" {
		return none, nil
	}
	result, detail := automationTerminalOutcome(terminal.Status, terminal.FinalReason, terminal.Cause)
	var events []models.AutomationEvent
	if err := tx.Select("id", "rule_id").
		Where("command_id = ? AND result = ?", commandID, models.AutomationResultExecuted).
		Order("id").Find(&events).Error; err != nil {
		return none, err
	}
	if len(events) == 0 {
		return none, nil
	}
	ids := make([]uint, 0, len(events))
	for i := range events {
		ids = append(ids, events[i].ID)
	}
	if err := tx.Model(&models.AutomationEvent{}).Where("id IN ?", ids).
		Updates(map[string]interface{}{"result": result, "detail": detail}).Error; err != nil {
		return none, err
	}
	// 正常情况下一条命令只关联一行事件（幂等键含 action_seq，多动作各有一条命令，
	// 但只有最后一个动作的 command_id 被写进事件行）。多于一行时按 id 升序取第一行
	// 做投影即可 —— 回填本身对全部匹配行生效。
	return AutomationEventUpdate{
		EventID:   events[0].ID,
		RuleID:    events[0].RuleID,
		Result:    result,
		Detail:    detail,
		CommandID: commandID,
		Status:    terminal.Status,
	}, nil
}

// automationTerminalOutcome 把命令终态映射成 (result, detail)。
//
// 纯函数（无 DB / 无时钟），供回填与单测共用 —— 结果码与文案的对应关系只在这里，
// 不允许在调用点各自拼字符串。
func automationTerminalOutcome(status, finalReason, cause string) (result, detail string) {
	reason := strings.TrimSpace(finalReason)
	if status == StatusSucceeded {
		return models.AutomationResultExecuted, automationDetailSucceeded
	}
	label := "设备未确认执行成功"
	switch status {
	case StatusFailed:
		label = "设备未确认执行成功"
	case StatusUnknown:
		label = "执行结果未知（设备可能已执行，需人工核查）"
	case StatusCancelled:
		label = "命令已被取消（未下发到设备）"
	}
	reasonText := automationTerminalCauseText(reason, cause)
	if reason == "" {
		reason = "(空)"
	}
	// final_reason 原文必须出现：运维拿着执行记录里的原因串与本行对齐时，
	// 不能要求他去猜"detail 里的中文对应哪条原因码"。
	detail = fmt.Sprintf("%s（命令终态 %s）: %s; final_reason=%s", label, status, reasonText, reason)
	return models.AutomationResultFailedExecution, clampRunes(detail, automationEventDetailColumnRunes)
}

// automationTerminalCauseText 把 final_reason 的原因码翻成一句人能读的话。
//
// 未识别的原因码**不丢弃**：调用方会把原文附在 detail 末尾（final_reason=...），
// 这里只负责说明"这条码属于哪一类"，不做有损映射。
func automationTerminalCauseText(finalReason, cause string) string {
	// 服务端 verifier 的原始原因优先：它是唯一能区分"设备没执行"与"设备执行了
	// 但回读对账没通过"的信息，也是用户排查时最需要看到的一句。
	if trimmed := strings.TrimSpace(cause); trimmed != "" {
		return "服务端回读校验未通过: " + clampRunes(trimmed, 240)
	}
	switch {
	case finalReason == "":
		return "执行记录没有 final_reason"
	case finalReason == "final_failed":
		return "设备上报最终失败，或最终回传未通过服务端校验（未记录判定原因）"
	case finalReason == "final_succeeded":
		return "设备上报最终成功（但命令记录未落到 SUCCEEDED）"
	case finalReason == "unknown":
		return "设备上报结果未知"
	case finalReason == "cancelled by actor":
		return "操作者取消了该命令"
	case strings.Contains(finalReason, "deadline expired without final evidence"):
		return "超过截止时间仍未收到设备的最终结果"
	case strings.Contains(finalReason, "deadline expired before dispatch"):
		return "超过截止时间仍未能下发到设备"
	case strings.Contains(finalReason, "deadline expired"):
		return "超过截止时间"
	case strings.HasPrefix(finalReason, "dispatch rejected:"):
		return "下发被拒绝: " + strings.TrimSpace(strings.TrimPrefix(finalReason, "dispatch rejected:"))
	default:
		return "未识别的失败原因（原文见 final_reason）"
	}
}

// 编译期守卫：回填只处理"已提交"的自动化事件行，结果码常量必须与 models 对齐。
// 若将来有人把 executed 改名，这里会先红，而不是让回填静默匹配不到任何行。
var _ = models.AutomationResultExecuted
var _ = models.AutomationResultFailedExecution
