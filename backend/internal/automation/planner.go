package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

	// latestValueFn 数据层时序化 (方案 v3.4 §3.2.4): 最新值查询回调,
	// 默认走 api.LatestValue; 测试注入内存实现。nil 时跳过 F4 条件复核。
	latestValueFn func(deviceID uint) (models.UnifiedData, bool)

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
		latestValueFn: nil, // 由 SetLatestValueFn 注入 (main.go 接线), 避免 automation→api 编译期依赖
	}
}

// SetLatestValueFn 注入最新值查询回调 (数据层时序化 v3.4 §3.2.4)。
// 与 databus 的 latestSink 同点挂接, 避免 automation→api 编译期依赖。
func (p *Planner) SetLatestValueFn(fn func(deviceID uint) (models.UnifiedData, bool)) {
	p.latestValueFn = fn
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
			// F6: 口径改为只 count(executed) — pending_confirm 不占日限额 (方案 §3.2 语义)
			Where("rule_id = ? AND triggered_at >= ? AND result = ?",
				rule.ID, dayStart, models.AutomationResultExecuted).
			Count(&cnt).Error; err == nil && int(cnt) >= rule.MaxDailyExec {
			eventID := p.recordRet(rule, at, ev.Value, models.AutomationResultSuppressedDailyLimit,
				"", fmt.Sprintf("daily limit %d reached", rule.MaxDailyExec))
			// F5: 补发 warning 级通知, 同日同规则只发一次 (幂等: 查当日是否已发过)
			p.notifyDailyLimitOnce(rule, at, eventID)
			return
		}
	}

	// ── 确认分流: require_confirmed=true 只生成建议执行通知 ──
	if rule.RequireConfirmed {
		eventID := p.recordRet(rule, at, ev.Value, models.AutomationResultPendingConfirm, "",
			"awaiting manual confirmation")
		p.notifyConfirmation(rule, at, ev.Value, eventID)
		return
	}

	// ── 动作分发 ──
	switch rule.ActionType {
	case models.AutomationActionNotification:
		p.notifyAction(rule, at, ev.Value)
		p.record(rule, at, ev.Value, models.AutomationResultNotification, "", "")
	case models.AutomationActionDeviceAction:
		// F4: 执行前复核条件 — 触发到执行间条件可能已失效 (DurationSec 长规则安全相关)。
		// 任一条件不满足则落 condition_changed 事件并返回, 不执行动作。
		if reason, ok := p.checkConditionsStillSatisfied(rule); !ok {
			p.record(rule, at, ev.Value, models.AutomationResultConditionChanged, "", reason)
			return
		}
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
		ActorKind:      commandexec.ActorKindSystem,
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

// checkConditionsStillSatisfied F4 条件复核: 用最新值缓存重查 rule 的所有 conditions
// 与 trigger 条件。任一不满足则返回 (原因, false), 由调用方落 condition_changed。
//
// 语义边界: 与 evaluator 的 compare 同源同语义, 不复用避免 automation→evaluator
// 编译期依赖。latestValueFn 为 nil 时 (未接线) 返回 true 跳过复核, 保持既有行为。
func (p *Planner) checkConditionsStillSatisfied(rule models.AutomationRule) (string, bool) {
	if p.latestValueFn == nil {
		return "", true // 未注入最新值查询, 跳过复核 (兼容旧接线)
	}
	conds, err := rule.ParseConditions()
	if err != nil {
		return fmt.Sprintf("invalid conditions_json: %v", err), false
	}
	// 触发器设备 ID 为 0 时无法定位最新值, 跳过复核 (不推荐配置, 见模型注释)
	if rule.TriggerEdgeDeviceID == 0 {
		return "", true
	}
	rec, ok := p.latestValueFn(rule.TriggerEdgeDeviceID)
	if !ok {
		return fmt.Sprintf("latest value unavailable for edge_device_id=%d", rule.TriggerEdgeDeviceID), false
	}
	// Trigger 条件复核 (仅 sensor_threshold; time_window 由 evaluator 保证)
	if rule.TriggerType == models.AutomationTriggerSensorThreshold {
		if !compare(rule.TriggerComparator, rec.Value, rule.TriggerThreshold) {
			return fmt.Sprintf("trigger condition no longer satisfied: %s %.2f vs threshold %.2f",
				rule.TriggerComparator, rec.Value, rule.TriggerThreshold), false
		}
	}
	// 附加条件复核 (全部 AND; SensorName 匹配 UnifiedData.SensorName)
	for _, c := range conds {
		if c.SensorName != rec.SensorName {
			continue // 最新值缓存单条记录只覆盖一个传感器, 其余条件无法复核则跳过
		}
		if !compare(c.Comparator, rec.Value, c.Threshold) {
			return fmt.Sprintf("condition %s %s %.2f no longer satisfied: latest=%.2f",
				c.SensorName, c.Comparator, c.Threshold, rec.Value), false
		}
	}
	return "", true
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
	p.recordRet(rule, at, value, result, commandID, detail)
}

// recordRet 同 record, 但返回落库事件 ID (0=写失败)。确认分流需 eventID 透传通知。
func (p *Planner) recordRet(rule models.AutomationRule, at time.Time, value float64,
	result, commandID, detail string) uint {
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
		return 0
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
	return ev.ID
}

// notifyDailyLimitOnce F5 日熔断 warning 通知, 同日同规则只发一次 (幂等)。
// 复用 notifyConfirmation 的 Notification 构造模式, level=warning。
func (p *Planner) notifyDailyLimitOnce(rule models.AutomationRule, at time.Time, eventID uint) {
	dayStart := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, at.Location())
	var cnt int64
	// 幂等闸: 当日已发过该规则的 daily_limit warning 通知则跳过
	if err := p.db.Model(&models.Notification{}).
		Where("source = ? AND source_id = ? AND title = ? AND created_at >= ?",
			"automation_rule", fmt.Sprintf("%d", rule.ID),
			"策略日熔断: "+rule.Name, dayStart).
		Count(&cnt).Error; err == nil && cnt > 0 {
		return
	}
	desc := fmt.Sprintf("策略「%s」已达日执行上限 (MaxDailyExec=%d), 今日后续触发将被抑制 (event_id=%d)",
		rule.Name, rule.MaxDailyExec, eventID)
	n := models.Notification{
		Type:        models.NotificationType(models.AlertLevelWarning),
		Title:       "策略日熔断: " + rule.Name,
		Message:     desc,
		Description: desc,
		Source:      "automation_rule",
		SourceID:    fmt.Sprintf("%d", rule.ID),
		Read:        false,
		CreatedAt:   at,
	}
	if err := p.db.Create(&n).Error; err != nil {
		metrics.DataConsumerDBWriteFailures.WithLabelValues("automation_planner", "notifications").Inc()
		logger.Warn("automation: failed to create daily limit notification", "rule_id", rule.ID, "error", err)
	}
	if p.broadcast != nil {
		p.broadcast("automation_daily_limit", gin.H{
			"rule_id":   rule.ID,
			"rule_name": rule.Name,
			"event_id":  eventID,
			"limit":     rule.MaxDailyExec,
		})
	}
}

// notifyConfirmation require_confirmed=true 的"建议执行"通知 (铁律 4)。
// eventID 透传给前端, 人工据此调 POST /automation-events/:id/confirm 定位该事件。
func (p *Planner) notifyConfirmation(rule models.AutomationRule, at time.Time, value float64, eventID uint) {
	desc := fmt.Sprintf("策略「%s」已触发 (值 %.2f), 动作 %s 待人工确认执行 (event_id=%d)", rule.Name, value, rule.ActionID, eventID)
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
	if p.broadcast != nil {
		p.broadcast("automation_pending_confirm", gin.H{
			"rule_id":   rule.ID,
			"rule_name": rule.Name,
			"event_id":  eventID,
			"action_id": rule.ActionID,
			"value":     value,
		})
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

// ── 确认制闭环 (裁决 4, 设计/自动化确认制闭环实现方案.md) ──

// pendingConfirmTTL pending_confirm 事件人工确认窗口, 超时由 StartCleanup 置 expired。
const pendingConfirmTTL = 24 * time.Hour

// cleanupInterval 超时清扫周期 (独立于 evaluator ticker, 因 evaluator 只缓存
// sensor_threshold 规则, time_window 事件会漏扫且职责错位)。
const cleanupInterval = 5 * time.Minute

// ── 手动触发 (POST /api/v1/automation-rules/:id/trigger) ──

// TriggerRule 手动触发错误哨兵 (handler 据此映射 HTTP 状态码)。
var (
	ErrTriggerRuleNotFound = errors.New("automation rule not found")
	ErrTriggerRuleDisabled = errors.New("automation rule is disabled")
)

// TriggerRule 手动触发一条自动化规则 (手动触发端点)。
//
// 与自动触发 (HandleTrigger) 的差异:
//   - 跳过条件评估: 用户点击即确认, 不查 F4 conditions
//   - 跳过确认制:   require_confirmed=true 的规则也直接执行 (点击按钮=人工确认)
//   - 保留安全门禁: cooldown / max_daily_exec / 日熔断 仍然生效
//   - 审计标记:     TriggerSource = manual, Reason 带触发者 ID
//
// 返回落库的 AutomationEvent (含 result), 调用方据此返回 HTTP 200/409。
func (p *Planner) TriggerRule(ctx context.Context, ruleID, actorID uint, sourceIP string) (models.AutomationEvent, error) {
	var rule models.AutomationRule
	if err := p.db.WithContext(ctx).First(&rule, ruleID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.AutomationEvent{}, ErrTriggerRuleNotFound
		}
		return models.AutomationEvent{}, err
	}
	if !rule.Enabled {
		return models.AutomationEvent{}, ErrTriggerRuleDisabled
	}

	at := p.nowFn()

	// ── 日熔断: MaxDailyExec > 0 时统计当日 executed 行数 (与自动触发同口径) ──
	if rule.MaxDailyExec > 0 {
		dayStart := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, at.Location())
		var cnt int64
		if err := p.db.WithContext(ctx).Model(&models.AutomationEvent{}).
			Where("rule_id = ? AND triggered_at >= ? AND result = ?",
				rule.ID, dayStart, models.AutomationResultExecuted).
			Count(&cnt).Error; err == nil && int(cnt) >= rule.MaxDailyExec {
			ev := models.AutomationEvent{
				RuleID:        rule.ID,
				TriggeredAt:   at,
				TriggerSource: models.AutomationTriggerSourceManual,
				Result:        models.AutomationResultSuppressedDailyLimit,
				Detail:        fmt.Sprintf("daily limit %d reached", rule.MaxDailyExec),
				CreatedAt:     at,
			}
			_ = p.db.WithContext(ctx).Create(&ev).Error
			p.notifyDailyLimitOnce(rule, at, ev.ID)
			return ev, nil
		}
	}

	// ── 冷却抑制: 查最近一次 executed/pending_confirm 的 triggered_at,
	//    与自动触发 evaluator 的内存 cooldown 等价 (planner 侧 DB 兜底) ──
	if rule.CooldownSec > 0 {
		var lastEv models.AutomationEvent
		err := p.db.WithContext(ctx).
			Where("rule_id = ? AND result IN ?", rule.ID,
				[]string{models.AutomationResultExecuted, models.AutomationResultPendingConfirm}).
			Order("triggered_at DESC").First(&lastEv).Error
		if err == nil {
			cooldown := time.Duration(rule.CooldownSec) * time.Second
			if at.Sub(lastEv.TriggeredAt) < cooldown {
				remaining := cooldown - at.Sub(lastEv.TriggeredAt)
				ev := models.AutomationEvent{
					RuleID:        rule.ID,
					TriggeredAt:   at,
					TriggerSource: models.AutomationTriggerSourceManual,
					Result:        models.AutomationResultSuppressedCooldown,
					Detail:        fmt.Sprintf("cooldown active (%.0fs remaining)", remaining.Seconds()),
					CreatedAt:     at,
				}
				_ = p.db.WithContext(ctx).Create(&ev).Error
				return ev, nil
			}
		}
	}

	// ── 动作分发 ──
	switch rule.ActionType {
	case models.AutomationActionNotification:
		p.notifyAction(rule, at, 0)
		ev := models.AutomationEvent{
			RuleID:        rule.ID,
			TriggeredAt:   at,
			TriggerSource: models.AutomationTriggerSourceManual,
			Result:        models.AutomationResultNotification,
			CreatedAt:     at,
		}
		_ = p.db.WithContext(ctx).Create(&ev).Error
		return ev, nil
	case models.AutomationActionDeviceAction:
		return p.executeManualDeviceAction(ctx, rule, at, actorID, sourceIP)
	default:
		ev := models.AutomationEvent{
			RuleID:        rule.ID,
			TriggeredAt:   at,
			TriggerSource: models.AutomationTriggerSourceManual,
			Result:        models.AutomationResultFailedDispatch,
			Detail:        "unknown action_type: " + rule.ActionType,
			CreatedAt:     at,
		}
		_ = p.db.WithContext(ctx).Create(&ev).Error
		return ev, nil
	}
}

// executeManualDeviceAction 手动触发的 device_action 执行。
// 跳过 F4 条件复核 (用户已确认), 但走全 commandexec.Service.Create 的
// availability gate + 幂等 + 审计链路。
func (p *Planner) executeManualDeviceAction(ctx context.Context, rule models.AutomationRule,
	at time.Time, actorID uint, sourceIP string) (models.AutomationEvent, error) {
	params, err := rule.ParseActionParams()
	if err != nil {
		ev := models.AutomationEvent{
			RuleID:        rule.ID,
			TriggeredAt:   at,
			TriggerSource: models.AutomationTriggerSourceManual,
			Result:        models.AutomationResultFailedDispatch,
			Detail:        "invalid action_params: " + err.Error(),
			CreatedAt:     at,
		}
		_ = p.db.WithContext(ctx).Create(&ev).Error
		return ev, nil
	}

	// 幂等键: 手动触发独立命名空间, 当日序号
	dayStart := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, at.Location())
	var seq int64
	_ = p.db.WithContext(ctx).Model(&models.AutomationEvent{}).
		Where("rule_id = ? AND triggered_at >= ? AND result = ? AND trigger_source = ?",
			rule.ID, dayStart, models.AutomationResultExecuted, models.AutomationTriggerSourceManual).
		Count(&seq).Error
	idempotencyKey := fmt.Sprintf("automation:manual:%d:%s:%d", rule.ID, at.Format("20060102"), seq+1)

	exec, _, err := p.cmdSvc.Create(ctx, commandexec.CreateInput{
		EdgeDeviceID:   rule.ActionDeviceID,
		ActorUserID:    actorID,
		ActionID:       rule.ActionID,
		Params:         params,
		IdempotencyKey: idempotencyKey,
		SourceIP:       sourceIP,
		Reason:         fmt.Sprintf("automation:manual:%d:%s:by_user:%d", rule.ID, rule.Name, actorID),
	})
	if err != nil {
		result := models.AutomationResultFailedDispatch
		if isGateError(err) {
			result = models.AutomationResultFailedGate
		}
		ev := models.AutomationEvent{
			RuleID:        rule.ID,
			TriggeredAt:   at,
			TriggerSource: models.AutomationTriggerSourceManual,
			Result:        result,
			Detail:        err.Error(),
			CreatedAt:     at,
		}
		_ = p.db.WithContext(ctx).Create(&ev).Error
		return ev, nil
	}
	ev := models.AutomationEvent{
		RuleID:        rule.ID,
		TriggeredAt:   at,
		TriggerSource: models.AutomationTriggerSourceManual,
		Result:        models.AutomationResultExecuted,
		CommandID:     exec.CommandID,
		CreatedAt:     at,
	}
	_ = p.db.WithContext(ctx).Create(&ev).Error
	if p.broadcast != nil {
		p.broadcast("automation_event", gin.H{
			"rule_id":        rule.ID,
			"rule_name":      rule.Name,
			"event_id":       ev.ID,
			"result":         ev.Result,
			"command_id":     exec.CommandID,
			"trigger_source": models.AutomationTriggerSourceManual,
		})
	}
	return ev, nil
}

// ConfirmEvent 错误哨兵 (handler 据此映射 HTTP 状态码)。
var (
	ErrConfirmEventNotFound   = errors.New("automation event not found")
	ErrConfirmNotPending      = errors.New("automation event is not awaiting confirmation")
	ErrConfirmRuleMissing     = errors.New("automation rule no longer exists")
	ErrConfirmActionChanged   = errors.New("automation rule action changed, cannot confirm")
	ErrConfirmInvalidParams   = errors.New("automation rule action params invalid")
	ErrConfirmEventExpired    = errors.New("automation event confirmation window expired")
)

// ConfirmEvent 人工确认执行 pending_confirm 事件 (路径 B: 操作者 confirm 当下即铸即销 token)。
//
// 幂等防重: 确定性幂等键 automation:confirm:<ruleID>:<eventID> 与事件 1:1,
// 重复 confirm 同 eventID 命中 command_executions 唯一索引走 replay 只读;
// 条件 UPDATE (result='pending_confirm') 作应用层第一道防重闸。
// TOCTOU: 现读 rule 最新动作, rule 被删/改 action_type 时 fail-closed 报错,
// 不存 params 快照 (detail size:512 保持纯失败语义)。
func (p *Planner) ConfirmEvent(ctx context.Context, eventID, actorID uint, sourceIP string) (models.AutomationEvent, error) {
	var ev models.AutomationEvent
	if err := p.db.WithContext(ctx).First(&ev, eventID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ev, ErrConfirmEventNotFound
		}
		return ev, err
	}
	if ev.Result == models.AutomationResultExpired {
		return ev, ErrConfirmEventExpired
	}
	if ev.Result != models.AutomationResultPendingConfirm {
		return ev, ErrConfirmNotPending
	}
	// 双保险: 落库时刻 triggered_at 已超窗 (清扫可能尚未跑到) 也按超时拒绝。
	if p.nowFn().Sub(ev.TriggeredAt) > pendingConfirmTTL {
		return ev, ErrConfirmEventExpired
	}

	var rule models.AutomationRule
	if err := p.db.WithContext(ctx).First(&rule, ev.RuleID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ev, ErrConfirmRuleMissing
		}
		return ev, err
	}
	if rule.ActionType != models.AutomationActionDeviceAction {
		return ev, ErrConfirmActionChanged
	}
	params, err := rule.ParseActionParams()
	if err != nil {
		return ev, fmt.Errorf("%w: %v", ErrConfirmInvalidParams, err)
	}

	reason := fmt.Sprintf("automation:confirm:%d", rule.ID)
	// 即铸: 操作者 confirm 当下铸造 (近认证门由 IssueConfirmation 内核对操作者 LastLoginAt 强制)。
	grant, err := p.cmdSvc.IssueConfirmation(ctx, commandexec.ConfirmationInput{
		EdgeDeviceID: rule.ActionDeviceID,
		ActorUserID:  actorID,
		ActionID:     rule.ActionID,
		Params:       params,
		Reason:       reason,
		SourceIP:     sourceIP,
	})
	if err != nil {
		return ev, err
	}
	// 即销: Create 携 token 消费, 走全 availability gate + 幂等 + 审计 + readback。
	exec, _, err := p.cmdSvc.Create(ctx, commandexec.CreateInput{
		EdgeDeviceID:      rule.ActionDeviceID,
		ActorUserID:       actorID,
		ActionID:          rule.ActionID,
		Params:            params,
		IdempotencyKey:    fmt.Sprintf("automation:confirm:%d:%d", rule.ID, ev.ID),
		SourceIP:          sourceIP,
		ConfirmationToken: grant.Token,
		Reason:            reason,
	})

	// 条件 UPDATE 原事件行: 仅当仍是 pending_confirm 才翻转 (并发 confirm/expired 时
	// RowsAffected=0, 幂等返回现值不覆写)。
	if err == nil {
		p.db.WithContext(ctx).Model(&models.AutomationEvent{}).
			Where("id = ? AND result = ?", ev.ID, models.AutomationResultPendingConfirm).
			Updates(map[string]interface{}{
				"result":     models.AutomationResultExecuted,
				"command_id": exec.CommandID,
			})
		p.db.WithContext(ctx).First(&ev, ev.ID)
		return ev, nil
	}
	result := models.AutomationResultFailedDispatch
	if isGateError(err) {
		result = models.AutomationResultFailedGate
	}
	p.db.WithContext(ctx).Model(&models.AutomationEvent{}).
		Where("id = ? AND result = ?", ev.ID, models.AutomationResultPendingConfirm).
		Updates(map[string]interface{}{"result": result, "detail": err.Error()})
	return ev, err
}

// StartCleanup 启动 pending_confirm 超时清扫 goroutine (24h 未确认 → expired)。
// 幂等: 重复调用安全 (每轮全量 UPDATE 超窗行)。ctx 取消即退出。
func (p *Planner) StartCleanup(ctx context.Context) {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()
	p.sweepExpiredPending()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.sweepExpiredPending()
		}
	}
}

// sweepExpiredPending 单轮清扫: result='pending_confirm' AND triggered_at < now-24h。
// 走 (result) 与 (triggered_at) 索引, 全表量小无性能压力。
func (p *Planner) sweepExpiredPending() {
	cutoff := p.nowFn().Add(-pendingConfirmTTL)
	res := p.db.Model(&models.AutomationEvent{}).
		Where("result = ? AND triggered_at < ?", models.AutomationResultPendingConfirm, cutoff).
		Updates(map[string]interface{}{
			"result": models.AutomationResultExpired,
			"detail": "pending_confirm 超时未确认 (24h)",
		})
	if res.Error != nil {
		logger.Warn("automation: pending_confirm sweep failed", "error", res.Error)
		return
	}
	if res.RowsAffected > 0 {
		logger.Info("automation: expired pending_confirm events", "count", res.RowsAffected)
	}
}

// TrimSpace 显式引用 (confirm 链路对 reason 做 TrimSpace 校验对称, 防未使用告警)。
var _ = strings.TrimSpace
