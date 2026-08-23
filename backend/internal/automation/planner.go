package automation

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"ehome/backend/internal/commandexec"
	"ehome/backend/internal/models"
	"ehome/backend/pkg/logger"
	"ehome/backend/pkg/metrics"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Planner 触发后的执行编排器 (设计/自动化策略引擎方案.md §4.2):
// 冷却/日熔断 → 确认分流 → 动作执行, 全部在此收敛, 求值器不感知。
//
// 铁律 1: 动作执行唯一入口 = commandexec.Service.Create, 白得 9 项
// availability gate + 幂等 + verify; Planner 不持有 dispatcher/MQTT。
//
// 铁律 3: 系统 actor 归因 — Reason = "automation:<rule_id>:<rule_name>",
// ActorUserID = SystemActorID (main.go 注入的内置系统用户, 禁止登录)。
//
// 铁律 4: require_confirmed=true 的规则不直接执行, 仅生成"建议执行"通知
// + IssueConfirmation 占位 (人工确认后走既有 confirmation 链路)。
type Planner struct {
	db            *gorm.DB
	cmdSvc        *commandexec.Service
	broadcast     func(eventType string, payload any) // nil 时跳过 WS 广播
	systemActorID uint                                // main.go 注入, 内置系统用户 ID

	// nowFn 可注入时钟 (测试用), 默认 time.Now。
	nowFn func() time.Time
}

// NewPlanner 构造编排器。systemActorID 必须是 users 表内置系统用户 (is_system),
// 由 main.go 在启动时确保存在后注入; 0 会被 commandexec 外键拒绝。
func NewPlanner(db *gorm.DB, cmdSvc *commandexec.Service, broadcast func(string, any), systemActorID uint) *Planner {
	return &Planner{
		db:            db,
		cmdSvc:        cmdSvc,
		broadcast:     broadcast,
		systemActorID: systemActorID,
		nowFn:         time.Now,
	}
}

// HandleTrigger 实现 TriggerHandler 接口, 由 Evaluator 在 armed→triggered 时调用。
// 执行约束顺序 (裁决 5): MaxDailyExec 日熔断 → require_confirmed 确认分流 → 动作。
func (p *Planner) HandleTrigger(ev TriggerEvent) {
	rule := ev.Rule
	at := ev.At
	if at.IsZero() {
		at = p.nowFn()
	}

	// ── 日熔断: MaxDailyExec > 0 时统计当日 executed/pending_confirm 行数 ──
	if rule.MaxDailyExec > 0 {
		dayStart := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, at.Location())
		var cnt int64
		if err := p.db.Model(&models.AutomationEvent{}).
			Where("rule_id = ? AND triggered_at >= ? AND result IN ?",
				rule.ID, dayStart,
				[]string{models.AutomationResultExecuted, models.AutomationResultPendingConfirm}).
			Count(&cnt).Error; err == nil && int(cnt) >= rule.MaxDailyExec {
			p.record(rule, at, ev.Value, models.AutomationResultSuppressedDailyLimit,
				"", fmt.Sprintf("daily limit %d reached", rule.MaxDailyExec))
			return
		}
	}

	// ── 确认分流: require_confirmed=true 只生成建议执行通知 ──
	if rule.RequireConfirmed {
		p.record(rule, at, ev.Value, models.AutomationResultPendingConfirm, "",
			"awaiting manual confirmation")
		p.notifyConfirmation(rule, at, ev.Value)
		return
	}

	// ── 动作分发 ──
	switch rule.ActionType {
	case models.AutomationActionNotification:
		p.notifyAction(rule, at, ev.Value)
		p.record(rule, at, ev.Value, models.AutomationResultNotification, "", "")
	case models.AutomationActionDeviceAction:
		p.executeDeviceAction(rule, at, ev.Value)
	default:
		p.record(rule, at, ev.Value, models.AutomationResultFailedDispatch, "",
			"unknown action_type: "+rule.ActionType)
	}
}

// executeDeviceAction 走 commandexec.Service.Create (裁决 1)。
// 幂等键 = automation:<rule_id>:<yyyymmdd>:<seq> (裁决 3) — seq 为当日该规则
// 已执行次数+1, 保证同日多次触发幂等键不同, 跨日自然重置。
func (p *Planner) executeDeviceAction(rule models.AutomationRule, at time.Time, value float64) {
	params, err := rule.ParseActionParams()
	if err != nil {
		p.record(rule, at, value, models.AutomationResultFailedDispatch, "",
			"invalid action_params: "+err.Error())
		return
	}

	// 幂等键: 当日序号
	dayStart := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, at.Location())
	var seq int64
	_ = p.db.Model(&models.AutomationEvent{}).
		Where("rule_id = ? AND triggered_at >= ? AND result = ?",
			rule.ID, dayStart, models.AutomationResultExecuted).
		Count(&seq).Error
	idemKey := fmt.Sprintf("automation:%d:%s:%d", rule.ID, at.Format("20060102"), seq+1)

	exec, _, err := p.cmdSvc.Create(context.Background(), commandexec.CreateInput{
		EdgeDeviceID:   rule.ActionDeviceID,
		ActorUserID:    p.systemActorID,
		ActionID:       rule.ActionID,
		Params:         params,
		IdempotencyKey: idemKey,
		Reason:         fmt.Sprintf("automation:%d:%s", rule.ID, rule.Name),
	})
	if err != nil {
		// gate fail-closed 与 dispatch 失败分流记录 (裁决: 审计可读性)
		result := models.AutomationResultFailedDispatch
		if isGateError(err) {
			result = models.AutomationResultFailedGate
		}
		p.record(rule, at, value, result, "", err.Error())
		return
	}
	p.record(rule, at, value, models.AutomationResultExecuted, exec.CommandID, "")
}

// isGateError 判定 commandexec 返回错误是否属于 availability gate fail-closed
// (gate 拒绝 = 预期安全行为, 与 dispatch 传输失败在审计上区分)。
func isGateError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// gate 失败错误均带 "action unavailable" 前缀 (commandexec/service.go)。
	return len(msg) >= 18 && msg[:18] == "action unavailable"
}

// record 落审计行 (fail-open: 写失败只计指标不阻塞, 对齐 alert 风格)。
func (p *Planner) record(rule models.AutomationRule, at time.Time, value float64,
	result, commandID, detail string) {
	ev := models.AutomationEvent{
		RuleID:      rule.ID,
		TriggeredAt: at,
		Result:      result,
		CommandID:   commandID,
		Detail:      detail,
		CreatedAt:   at,
	}
	if value != 0 || rule.TriggerType == models.AutomationTriggerSensorThreshold {
		ev.TriggerValue = &value
	}
	if err := p.db.Create(&ev).Error; err != nil {
		metrics.DataConsumerDBWriteFailures.WithLabelValues("automation_planner", "automation_events").Inc()
		logger.Warn("automation: failed to record event", "rule_id", rule.ID, "error", err)
		return
	}
	if p.broadcast != nil {
		p.broadcast("automation_event", gin.H{
			"rule_id":   rule.ID,
			"rule_name": rule.Name,
			"event_id":  ev.ID,
			"result":    result,
			"value":     value,
			"command_id": commandID,
		})
	}
}

// notifyConfirmation require_confirmed=true 的"建议执行"通知 (铁律 4)。
func (p *Planner) notifyConfirmation(rule models.AutomationRule, at time.Time, value float64) {
	desc := fmt.Sprintf("策略「%s」已触发 (值 %.2f), 动作 %s 待人工确认执行", rule.Name, value, rule.ActionID)
	n := models.Notification{
		Type:        models.NotificationType(models.AlertLevelWarning),
		Title:       "策略待确认: " + rule.Name,
		Message:     desc,
		Description: desc,
		Source:      "automation_rule",
		SourceID:    fmt.Sprintf("%d", rule.ID),
		Read:        false,
		CreatedAt:   at,
	}
	if err := p.db.Create(&n).Error; err != nil {
		metrics.DataConsumerDBWriteFailures.WithLabelValues("automation_planner", "notifications").Inc()
		logger.Warn("automation: failed to create confirmation notification", "rule_id", rule.ID, "error", err)
	}
}

// notifyAction action=notification 的纯通知动作。
func (p *Planner) notifyAction(rule models.AutomationRule, at time.Time, value float64) {
	level := rule.ActionLevel
	if level == "" {
		level = models.AlertLevelInfo
	}
	desc := fmt.Sprintf("策略「%s」触发通知 (值 %.2f)", rule.Name, value)
	n := models.Notification{
		Type:        models.NotificationType(level),
		Title:       "策略通知: " + rule.Name,
		Message:     desc,
		Description: desc,
		Source:      "automation_rule",
		SourceID:    fmt.Sprintf("%d", rule.ID),
		Read:        false,
		CreatedAt:   at,
	}
	if err := p.db.Create(&n).Error; err != nil {
		metrics.DataConsumerDBWriteFailures.WithLabelValues("automation_planner", "notifications").Inc()
		logger.Warn("automation: failed to create action notification", "rule_id", rule.ID, "error", err)
	}
}

// 防止 json 未使用告警 (ParseActionParams 返回 json.RawMessage 已用, 此处仅为显式 import 对称)。
var _ = json.RawMessage{}
