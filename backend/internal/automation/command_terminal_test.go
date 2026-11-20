package automation

// command_terminal_test.go —— 命令终态回填后的**自动化侧收尾**（2026-11-20）。
//
// 缺陷的用户可见面：规则「关闭BMS充电」的命令实际 FAILED（ChannelCmdV2 final
// verification failed），「自动化策略」页的触发历史却显示"已执行"，且**没有任何提示**。
// 用户原话："自动规则失败了应该有提示"。
//
// 本文件钉 commandexec → Planner.OnCommandTerminal 这条收尾链：
//   ① 失败必须落一条通知（走 notify.Dispatcher → notification_channels 外发）；
//   ② 失败必须广播 automation_execution_failed（前端据此即时弹提示，无需刷新）；
//   ③ 成功只广播通用刷新信号，**不得**打扰用户（否则"策略成功"会天天弹错误）；
//   ④ 同一事件只通知一次（重复投递不得刷屏）；规则已被删除时降级成 #id 不 panic。

import (
	"strings"
	"testing"

	"ehome/backend/internal/commandexec"
	"ehome/backend/internal/models"
)

type capturedPush struct {
	name    string
	payload any
}

// capturePushes 装上广播收集器（生产实现是 wsHub.BroadcastEvent）。
func capturePushes(p *Planner) *[]capturedPush {
	pushes := &[]capturedPush{}
	p.broadcast = func(name string, payload any) {
		*pushes = append(*pushes, capturedPush{name: name, payload: payload})
	}
	return pushes
}

func pushNames(pushes []capturedPush) []string {
	names := make([]string, 0, len(pushes))
	for _, push := range pushes {
		names = append(names, push.name)
	}
	return names
}

func hasPush(pushes []capturedPush, name string) bool {
	for _, push := range pushes {
		if push.name == name {
			return true
		}
	}
	return false
}

// TestOnCommandTerminalFailureNotifiesAndBroadcasts 是本次缺陷的靶点：
// 命令终态失败 ⇒ 页面必须收到提示级广播 + 通知中心必须有一条可外发的通知。
//
// 反向验证：删掉 OnCommandTerminal 里的 notifyExecutionFailed +
// automation_execution_failed 分支 ⇒ 本用例红（只剩一条静默的 automation_event，
// 正是生产上"失败了没有任何提示"的形态）。
func TestOnCommandTerminalFailureNotifiesAndBroadcasts(t *testing.T) {
	p, _ := setupConfirmPlanner(t)
	pushes := capturePushes(p)
	rule := models.AutomationRule{
		Name: "关闭BMS充电", Enabled: true,
		TriggerType: models.AutomationTriggerSensorThreshold,
		ActionType:  models.AutomationActionNotification, ActionLevel: models.AlertLevelInfo,
	}
	if err := p.db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	at := p.nowFn()
	detail := "设备未确认执行成功（命令终态 FAILED）: 设备上报最终执行失败，或最终回传未通过服务端校验; final_reason=final_failed"
	ev := models.AutomationEvent{
		RuleID: rule.ID, TriggeredAt: at, Result: models.AutomationResultFailedExecution,
		CommandID: "cmd-bms-1", Detail: detail, CreatedAt: at,
	}
	if err := p.db.Create(&ev).Error; err != nil {
		t.Fatal(err)
	}

	p.OnCommandTerminal(commandexec.AutomationEventUpdate{
		EventID: ev.ID, RuleID: rule.ID, CommandID: "cmd-bms-1", Status: commandexec.StatusFailed,
		Result: models.AutomationResultFailedExecution, Detail: detail,
	})

	// ① 通用刷新信号 + ② 失败专用提示信号
	if !hasPush(*pushes, "automation_event") {
		t.Fatalf("pushes=%v 缺少 automation_event（页面无法即时重拉事件表）", pushNames(*pushes))
	}
	if !hasPush(*pushes, "automation_execution_failed") {
		t.Fatalf("pushes=%v 缺少 automation_execution_failed（失败没有任何提示，正是用户投诉的形态）",
			pushNames(*pushes))
	}

	// ③ 通知行：必须是 error 级 + 规则名 + 命令 ID，且 source_id = 事件 ID（可回链）
	var notes []models.Notification
	if err := p.db.Where("source = ?", "automation_event").Find(&notes).Error; err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 {
		t.Fatalf("notifications=%d want 1 (执行失败必须产生一条可见通知)", len(notes))
	}
	n := notes[0]
	if n.Type != models.NotificationType(models.AlertLevelCritical) {
		t.Fatalf("notification type=%q want %q (设备没照做是需要介入的故障，不是普通信息)",
			n.Type, models.NotificationType(models.AlertLevelCritical))
	}
	if !strings.Contains(n.Title, rule.Name) {
		t.Fatalf("title=%q 必须含规则名（用户要知道是哪条策略没执行成功）", n.Title)
	}
	if !strings.Contains(n.Message, "cmd-bms-1") || !strings.Contains(n.Message, "final_failed") {
		t.Fatalf("message=%q 必须带 command_id 与失败原因（否则用户无从核查）", n.Message)
	}
	if n.SourceID != itoa(ev.ID) {
		t.Fatalf("source_id=%q want 事件 ID %d（source_id=事件 ID 才能回链到具体的触发记录）", n.SourceID, ev.ID)
	}

	// ④ 幂等：同一终态再投递一次不得再发一条通知（设备重传/清扫重跑）
	p.OnCommandTerminal(commandexec.AutomationEventUpdate{
		EventID: ev.ID, RuleID: rule.ID, CommandID: "cmd-bms-1", Status: commandexec.StatusFailed,
		Result: models.AutomationResultFailedExecution, Detail: detail,
	})
	var again int64
	if err := p.db.Model(&models.Notification{}).Where("source = ?", "automation_event").Count(&again).Error; err != nil {
		t.Fatal(err)
	}
	if again != 1 {
		t.Fatalf("notifications=%d after replay want 1 (重复投递不得刷屏)", again)
	}
}

// TestOnCommandTerminalSuccessStaysQuiet：命令成功只发通用刷新信号，
// 不得发通知/不得发失败提示（否则每次成功都弹一条"失败"，用户会关掉整个通知）。
func TestOnCommandTerminalSuccessStaysQuiet(t *testing.T) {
	p, _ := setupConfirmPlanner(t)
	pushes := capturePushes(p)
	rule := models.AutomationRule{
		Name: "开风机", Enabled: true,
		TriggerType: models.AutomationTriggerSensorThreshold,
		ActionType:  models.AutomationActionNotification, ActionLevel: models.AlertLevelInfo,
	}
	if err := p.db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	at := p.nowFn()
	ev := models.AutomationEvent{
		RuleID: rule.ID, TriggeredAt: at, Result: models.AutomationResultExecuted,
		CommandID: "cmd-ok-1", Detail: "设备已确认执行成功（命令终态 SUCCEEDED）", CreatedAt: at,
	}
	if err := p.db.Create(&ev).Error; err != nil {
		t.Fatal(err)
	}

	p.OnCommandTerminal(commandexec.AutomationEventUpdate{
		EventID: ev.ID, RuleID: rule.ID, CommandID: "cmd-ok-1", Status: commandexec.StatusSucceeded,
		Result: models.AutomationResultExecuted, Detail: ev.Detail,
	})

	if !hasPush(*pushes, "automation_event") {
		t.Fatalf("pushes=%v 缺少 automation_event（成功也要让页面把 detail 刷新成终态说明）", pushNames(*pushes))
	}
	if hasPush(*pushes, "automation_execution_failed") {
		t.Fatalf("pushes=%v 成功不得广播失败提示", pushNames(*pushes))
	}
	var count int64
	if err := p.db.Model(&models.Notification{}).Where("source = ?", "automation_event").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("notifications=%d want 0 (成功不打扰用户)", count)
	}
}

// TestOnCommandTerminalToleratesDeletedRule：规则被删除后事件行仍保留历史归因，
// 收尾不得 panic，也不得因为查不到规则就丢掉提示。
func TestOnCommandTerminalToleratesDeletedRule(t *testing.T) {
	p, _ := setupConfirmPlanner(t)
	pushes := capturePushes(p)
	at := p.nowFn()
	ev := models.AutomationEvent{
		RuleID: 4242, TriggeredAt: at, Result: models.AutomationResultFailedExecution,
		CommandID: "cmd-orphan", Detail: "final_reason=final_failed", CreatedAt: at,
	}
	if err := p.db.Create(&ev).Error; err != nil {
		t.Fatal(err)
	}
	p.OnCommandTerminal(commandexec.AutomationEventUpdate{
		EventID: ev.ID, RuleID: 4242, CommandID: "cmd-orphan", Status: commandexec.StatusFailed,
		Result: models.AutomationResultFailedExecution, Detail: ev.Detail,
	})
	if !hasPush(*pushes, "automation_execution_failed") {
		t.Fatalf("pushes=%v 规则已删除时仍必须提示（事件行还在，用户有权知道）", pushNames(*pushes))
	}
	var n models.Notification
	if err := p.db.Where("source = ?", "automation_event").First(&n).Error; err != nil {
		t.Fatalf("notification not created: %v", err)
	}
	if !strings.Contains(n.Title, "#4242") {
		t.Fatalf("title=%q 规则已删除时必须降级成 #id，不能留空白", n.Title)
	}
}

// TestOnCommandTerminalIgnoresUnappliedUpdate：Applied()=false（该命令没关联自动化事件）
// 时必须完全安静 —— 人工/告警下发的命令不得因为"命令失败"就往自动化通知里塞消息。
func TestOnCommandTerminalIgnoresUnappliedUpdate(t *testing.T) {
	p, _ := setupConfirmPlanner(t)
	pushes := capturePushes(p)
	p.OnCommandTerminal(commandexec.AutomationEventUpdate{
		CommandID: "cmd-manual", Status: commandexec.StatusFailed,
		Result: models.AutomationResultFailedExecution, Detail: "final_reason=final_failed",
	})
	if len(*pushes) != 0 {
		t.Fatalf("pushes=%v want none: 无人订阅的事件更新不得广播", pushNames(*pushes))
	}
	var count int64
	if err := p.db.Model(&models.Notification{}).Where("source = ?", "automation_event").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("notifications=%d want 0", count)
	}
}
