package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"ehome/backend/internal/commandexec"
	"ehome/backend/internal/models"
	"ehome/backend/internal/notify"
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
	db        *gorm.DB
	cmdSvc    *commandexec.Service
	broadcast func(eventType string, payload any) // nil 时跳过 WS 广播

	// actorMu 保护 systemActorID 的惰性解析与缓存。HandleTrigger 会被多个
	// worker 并发调用, 首次解析必须串行化 (顺带保证只查一次库)。
	// 注意: 持锁期间做一次 DB 查询可接受 —— 只有未命中缓存的首次调用才会查。
	actorMu sync.Mutex
	// systemActorID 内置系统主体用户 ID (subject_key=system_admin)。
	// 0 = "尚未解析", 不是有效 actor: 首次自动 device_action 触发时由
	// resolveSystemActorID 惰性解析并缓存 (见 NewPlanner 注释)。
	systemActorID uint

	// latestValueFn 数据层时序化 (方案 v3.4 §3.2.4): 最新值查询回调,
	// 默认走 api.LatestValue; 测试注入内存实现。nil 时跳过 F4 条件复核。
	latestValueFn func(deviceID uint) (models.UnifiedData, bool)

	// nowFn 可注入时钟 (测试用), 默认 time.Now。
	nowFn func() time.Time

	// notifier 通知写入+投递入口 (D-1 步骤 3, 设计/外发通知通道.md §2.1 取向 A):
	// main.go 经 SetNotifier 注入 notify.Dispatcher。
	// **nil 是显式支持的合法状态**: 既有测试 NewPlanner(db, svc, nil, id) 不注入时,
	// 回落为直接写 notifications 表 —— 行为与改造前逐字节等价 (见 createNotification)。
	notifier notify.Notifier
}

// NewPlanner 构造编排器。systemActorID 是 users 表内置系统主体用户
// (subject_key=system_admin AND retired_at IS NULL) 的 ID; main.go 的启动期
// 预检若命中则作为初值传入, 省掉首次惰性查询。
//
// 允许传 0, 语义是"尚未解析"而非有效 actor: 全新安装时该用户由
// POST /api/v1/auth/initialize 在进程启动**之后**创建, 启动期解析必然拿不到。
// 真正的取值在首次自动 device_action 触发时由 resolveSystemActorID 惰性解析并缓存,
// **绝不能**把启动期的 0 冻结成长期状态 —— 那会让自动 device_action 在进程整个
// 生命周期内被 commandexec 的 ActorUserID==0 fail-closed 校验拒绝, 直到重启才自愈。
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

// SetNotifier 注入"通知写入 + 投递"入口 (D-1 步骤 3, main.go 接线)。
//
// 契约: 注入后通知经它落库并顺带外发投递; 传 nil 显式回落为直接写 notifications 表。
// 沿用本包既有的二阶段 setter 注入范式 (SetLatestValueFn) 与窄接口取向, 避免
// automation→(外发 HTTP 引擎) 的编译期依赖。
func (p *Planner) SetNotifier(n notify.Notifier) {
	p.notifier = n
}

// createNotification 落库一条通知 (D-1 步骤 3: 经注入的 Notifier 或直接落库)。
//
// nil 回落是**显式设计**: 既有测试直接 NewPlanner(db, ...) 不注入, 必须保持
// 改造前的行为 (直接 Create + 同一条指标与日志), 不得 panic。
//
// 以下三个前提使"注入后同步投递"安全 (见 notify/dispatcher.go 的投递时机裁决):
//   - 本函数**不在任何事务内**被调用 (notifydailyLimitOnce / notifyConfirmation /
//     notifyAction 全部在 recordRet 的独立 db.Create 返回后才调用);
//   - 返回 true 表示已拿到自增 ID, 调用方据此广播 WS 载荷 (契约: 广播的必须是
//     库里真实存在的通知行), 因此要求同步落库 —— 不能用 CreateAsync;
//   - Dispatcher 自身 fail-open 且带 recover, 通知路径不会掀翻 Planner 控制流。
func (p *Planner) createNotification(ctx context.Context, n *models.Notification, scope string) bool {
	if p.notifier != nil {
		p.notifier.Create(ctx, n)
		return n.ID != 0
	}
	if err := p.db.Create(n).Error; err != nil {
		metrics.DataConsumerDBWriteFailures.WithLabelValues("automation_planner", "notifications").Inc()
		logger.Warn("automation: failed to create notification", "scope", scope, "error", err)
		return false
	}
	return true
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

// ── 系统 actor 惰性解析 (P0: 全新安装后自动 device_action 永久失败) ──

// resolveSystemActorID 返回内置系统主体用户 ID, 首次调用时解析并缓存。
//
// 为什么必须惰性: main.go 的启动期预检发生在 POST /api/v1/auth/initialize 之前,
// 全新安装时 users 表还是空的; 若把启动期的 0 当终值冻结, 自动 device_action 会在
// 进程整个生命周期内被 commandexec 的 ActorUserID==0 fail-closed 校验拒绝
// (事件恒为 failed_dispatch "invalid command request"), 只有重启才自愈。
//
// 并发: 用 actorMu 串行化解析 (HandleTrigger 可能被多个 worker 并发调用),
// 已缓存非 0 时走快路径直接返回, 后续触发不再查库。
func (p *Planner) resolveSystemActorID() (uint, error) {
	p.actorMu.Lock()
	defer p.actorMu.Unlock()
	if p.systemActorID != 0 {
		return p.systemActorID, nil
	}
	var admin models.User
	if err := p.db.
		Where("subject_key = ? AND retired_at IS NULL", models.SystemAdminSubjectKey).
		First(&admin).Error; err != nil {
		return 0, fmt.Errorf("active system_admin user not found: %w", err)
	}
	p.systemActorID = admin.ID
	logger.Info("automation: resolved system actor lazily", "user_id", admin.ID)
	return admin.ID, nil
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

	// 系统 actor 惰性解析: 全新安装下该用户在进程启动后才创建, 这里才第一次拿得到。
	// 解析失败 = 自动路径被禁用 (未初始化 / 主体被停用): 照常留审计, 但 Detail 写
	// 真实原因 (不是笼统的 "invalid command request"), 并补一条面向用户的通知。
	actorID, err := p.resolveSystemActorID()
	if err != nil {
		eventID := p.recordRet(rule, at, value, models.AutomationResultFailedDispatch, "",
			"system actor unavailable: "+err.Error())
		p.notifySystemActorUnavailableOnce(at, eventID, err)
		return
	}

	exec, _, err := p.cmdSvc.Create(context.Background(), commandexec.CreateInput{
		EdgeDeviceID:   rule.ActionDeviceID,
		ActorUserID:    actorID,
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
	p.recordExecuted(rule, at, value, exec.CommandID)
}

// recordExecuted 落 result='executed' 事件, 并在【同一事务】内更新规则行的冷却锚点
// (清理前置条件 A, docs/分析/清理前置条件-冷却锚点与监控基线-2026-09-14.md)。
//
// 为什么必须同事务: 事件行与锚点共同表达"该规则在 at 时刻触发过"这一个事实。
// 分两次写会留下崩溃窗口 —— 事件落了锚点没落 → 重启后冷却提前解除 → 设备动作多发;
// 锚点落了事件没落 → 审计说没触发过但系统确实冷却了 (本测试注入失败即断言前者)。
//
// fail-open 边界: 与 recordRet 一致, 写失败只告警不阻塞控制流 —— 但【两者一起失败】,
// 不会出现"一半事实"。返回事件 ID (0=写失败), 供需要透传的调用方使用。
func (p *Planner) recordExecuted(rule models.AutomationRule, at time.Time, value float64, commandID string) uint {
	var eventID uint
	err := p.db.Transaction(func(tx *gorm.DB) error {
		ev := models.AutomationEvent{
			RuleID:      rule.ID,
			TriggeredAt: at,
			Result:      models.AutomationResultExecuted,
			CommandID:   commandID,
			CreatedAt:   at,
		}
		if value != 0 || rule.TriggerType == models.AutomationTriggerSensorThreshold {
			ev.TriggerValue = &value
		}
		if err := tx.Create(&ev).Error; err != nil {
			return err
		}
		// 锚点取 at (本次触发时刻), 不取 DB now(): 求值器的冷却起点就是触发时刻,
		// 用落库时间会让重启后的冷却窗比崩溃前更长 (保守偏差, 同样是行为改变)。
		if err := tx.Model(&models.AutomationRule{}).Where("id = ?", rule.ID).
			Update("last_triggered_at", at).Error; err != nil {
			return err
		}
		eventID = ev.ID
		return nil
	})
	if err != nil {
		metrics.DataConsumerDBWriteFailures.WithLabelValues("automation_planner", "automation_events").Inc()
		logger.Warn("automation: failed to record executed event + cooldown anchor",
			"rule_id", rule.ID, "error", err)
		return 0
	}
	if p.broadcast != nil {
		p.broadcast("automation_event", gin.H{
			"rule_id":    rule.ID,
			"rule_name":  rule.Name,
			"event_id":   eventID,
			"result":     models.AutomationResultExecuted,
			"value":      value,
			"command_id": commandID,
		})
	}
	return eventID
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
//
// 必须用 errors.Is 而不是比对错误文本: commandexec 的 gate 拒绝返回的是哨兵
// ErrActionUnavailable ("action is unavailable for this device", service.go:27),
// 旧实现的 len(msg)>=18 && msg[:18]=="action unavailable" 与它**恒不相等**
// ("action is unavail" != "action unavailable") —— failed_gate 因此全仓没有生产者,
// 门禁拒绝被错误地记成 failed_dispatch。
//
// Create 的所有失败出口都在 gorm 事务回调里直接 return 哨兵本身(service.go:449-531):
//   - 动作目录未命中 / 任一 availability gate 不通过 → ErrActionUnavailable (原样返回)
//   - 参数不可解析 → fmt.Errorf("%w: %v", ErrInvalidParams, err) (包装, 本就不是 gate)
//   - 其余 gate (确认制/近认证) → 各自的哨兵
//
// gorm 的 Transaction 用 defer 直接 return 回调的 err, 不再包一层, 因此 errors.Is
// 能可靠穿透。此处不再保留文案兜底: 文案比对正是这个缺陷的成因, 留着只会再次腐烂。
func isGateError(err error) bool {
	return errors.Is(err, commandexec.ErrActionUnavailable)
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
			"rule_id":    rule.ID,
			"rule_name":  rule.Name,
			"event_id":   ev.ID,
			"result":     result,
			"value":      value,
			"command_id": commandID,
		})
	}
	return ev.ID
}

// notificationBroadcastPayload 构造自动化通知的 WS 广播载荷 (负债 D-3 契约):
// 顶层 = 通知实体本身, 与前端 api/notification.ts 的 Notification 接口、
// GET /notifications 列表项同形 (id/type/title/description/message/source/
// source_id/read/created_at); 自动化领域详情 (rule_id/event_id/...) 收进嵌套
// detail 字段, 不与通知字段平铺混淆。
//
// 调用前提: 通知行已 Create 成功 (n.ID != 0) —— 只有拿到自增 ID 才广播,
// 否则前端会插入一条库里不存在的通知且永远无法标记已读。
func notificationBroadcastPayload(n models.Notification, detail gin.H) gin.H {
	return gin.H{
		// ── 通知实体 (与列表项同形) ──
		"id":          n.ID,
		"type":        n.Type,
		"title":       n.Title,
		"description": n.Description,
		"message":     n.Message,
		"source":      n.Source,
		"source_id":   n.SourceID,
		"read":        n.Read,
		"created_at":  n.CreatedAt,
		// ── 自动化领域详情 (嵌套, 不平铺) ──
		"detail": detail,
	}
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
	if !p.createNotification(context.Background(), &n, "daily_limit") {
		return
	}
	if p.broadcast != nil {
		// 载荷 = 通知实体本身 (负债 D-3, 与 alert 同契约): 前端收到即可插入列表;
		// 自定义事件名保留 (前端按语义给出差异化提示), 自动化领域详情进嵌套 detail。
		p.broadcast("automation_daily_limit", notificationBroadcastPayload(n, gin.H{
			"rule_id":   rule.ID,
			"rule_name": rule.Name,
			"event_id":  eventID,
			"limit":     rule.MaxDailyExec,
		}))
	}
}

// 系统 actor 不可用通知的幂等键 (查询与落库共用同一组常量)。
// 与 notifyDailyLimitOnce 同样是 (source, source_id, title) 三元组幂等, 但**不按天重置**:
// "未初始化 / 主体被停用"是持续性的系统状态, 不是每日配额; 同一原因只发一条,
// 避免每次触发都往通知中心刷 (修好前每次触发的原因完全相同)。
const (
	automationSystemActorSource   = "automation_system"
	automationSystemActorSourceID = "system_admin"
	automationSystemActorTitle    = "自动化执行已禁用: 系统主体用户不可用"
)

// notifySystemActorUnavailableOnce 系统 actor 解析失败的告警通知 (同一原因只发一次)。
//
// 为什么必须通知: 解析失败时自动 device_action 全部静默失败 (事件只落审计表),
// 用户会误以为策略生效。这里是唯一面向用户的提示 —— 级别用 critical (type=error),
// 与"整条自动化执行链被禁用"的严重度相称, 比日熔断的 warning 更醒目。
func (p *Planner) notifySystemActorUnavailableOnce(at time.Time, eventID uint, cause error) {
	var cnt int64
	// 幂等闸: 已有同 (source, source_id, title) 通知则跳过 (全部历史, 不按天)
	if err := p.db.Model(&models.Notification{}).
		Where("source = ? AND source_id = ? AND title = ?",
			automationSystemActorSource, automationSystemActorSourceID, automationSystemActorTitle).
		Count(&cnt).Error; err == nil && cnt > 0 {
		return
	}
	desc := fmt.Sprintf("自动化 device_action 执行已被禁用: 无法解析系统主体用户 (subject_key=%s)。"+
		"若系统尚未初始化, 请完成初始化 (POST /api/v1/auth/initialize); 若已初始化, "+
		"请检查该主体是否被停用 (retired_at)。原因: %v (event_id=%d)",
		models.SystemAdminSubjectKey, cause, eventID)
	n := models.Notification{
		Type:        models.NotificationType(models.AlertLevelCritical),
		Title:       automationSystemActorTitle,
		Message:     desc,
		Description: desc,
		Source:      automationSystemActorSource,
		SourceID:    automationSystemActorSourceID,
		Read:        false,
		CreatedAt:   at,
	}
	if !p.createNotification(context.Background(), &n, "system_actor_unavailable") {
		return
	}
	if p.broadcast != nil {
		p.broadcast("automation_system_actor_unavailable", notificationBroadcastPayload(n, gin.H{
			"event_id": eventID,
			"reason":   cause.Error(),
		}))
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
	if !p.createNotification(context.Background(), &n, "pending_confirm") {
		return
	}
	if p.broadcast != nil {
		p.broadcast("automation_pending_confirm", notificationBroadcastPayload(n, gin.H{
			"rule_id":   rule.ID,
			"rule_name": rule.Name,
			"event_id":  eventID,
			"action_id": rule.ActionID,
			"value":     value,
		}))
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
	if !p.createNotification(context.Background(), &n, "notification_action") {
		return
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
	//    与自动触发 evaluator 的内存 cooldown 等价 (planner 侧 DB 兜底)。
	//    判定走 cooldownFor — 与 evaluator 同一份实现 (负债 D-5: 两路径曾语义相反,
	//    evaluator 把 0 当默认 300s, 此处把 0 当不冷却); 0 表示不冷却, 直接跳过。 ──
	if cooldown := cooldownFor(rule); cooldown > 0 {
		// 冷却基线优先读规则行锚点 (清理前置条件 A): automation_events 会被保留策略
		// 清理, 拿它当唯一基线会让"删审计"变成"冷却提前解除"。锚点为空时 (锚点列
		// 上线前的历史数据 / 回填尚未跑) 才回落到事件表, 保持既有行为不失。
		lastAt, ok := p.cooldownBase(ctx, rule)
		if ok {
			if at.Sub(lastAt) < cooldown {
				remaining := cooldown - at.Sub(lastAt)
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

// cooldownBase 返回某规则冷却窗的起算时刻 (是否存在)。
//
// 读取优先级 (清理前置条件 A):
//  1. automation_rules.last_triggered_at —— 唯一持久锚点, 清理器不碰;
//  2. 回落 automation_events "最近一条 executed/pending_confirm" —— 仅用于锚点列
//     上线前的历史数据 (回填尚未跑到时), 保持既有行为不失。
//
// 语义差异说明: 锚点记录【触发】时刻 (executed 落库那一刻的 at); 旧事件表兜底口径
// 把 pending_confirm 也算作冷却起点。回落分支保留该口径以便与旧数据对齐, 主分支
// 只认锚点 —— 一旦锚点被回填/写入, 行为即收敛到 evaluator 的触发时刻语义。
func (p *Planner) cooldownBase(ctx context.Context, rule models.AutomationRule) (time.Time, bool) {
	if rule.LastTriggeredAt != nil {
		return *rule.LastTriggeredAt, true
	}
	var lastEv models.AutomationEvent
	err := p.db.WithContext(ctx).
		Where("rule_id = ? AND result IN ?", rule.ID,
			[]string{models.AutomationResultExecuted, models.AutomationResultPendingConfirm}).
		Order("triggered_at DESC").First(&lastEv).Error
	if err != nil {
		return time.Time{}, false
	}
	return lastEv.TriggeredAt, true
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
	// 手动触发同样要更新冷却锚点 (清理前置条件 A): 手动执行成功也是"该规则触发过"
	// 这一事实, 且 planner.TriggerRule 的手动冷却判定正是读这个锚点。
	if err := p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&ev).Error; err != nil {
			return err
		}
		return tx.Model(&models.AutomationRule{}).Where("id = ?", rule.ID).
			Update("last_triggered_at", at).Error
	}); err != nil {
		metrics.DataConsumerDBWriteFailures.WithLabelValues("automation_planner", "automation_events").Inc()
		logger.Warn("automation: failed to record manual executed event + cooldown anchor",
			"rule_id", rule.ID, "error", err)
	}
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
	ErrConfirmEventNotFound = errors.New("automation event not found")
	ErrConfirmNotPending    = errors.New("automation event is not awaiting confirmation")
	ErrConfirmRuleMissing   = errors.New("automation rule no longer exists")
	ErrConfirmActionChanged = errors.New("automation rule action changed, cannot confirm")
	ErrConfirmInvalidParams = errors.New("automation rule action params invalid")
	ErrConfirmEventExpired  = errors.New("automation event confirmation window expired")
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
		// 翻转 + 冷却锚点同事务 (清理前置条件 A): 确认制规则的可执行路径是
		// pending_confirm --人工确认--> executed, 若这里不写锚点, require_confirmed=true
		// 的规则锚点【永远不会被写】, 重启后冷却照样提前解除。
		// 触发时刻用原事件的 TriggeredAt (pending_confirm 落库时刻), 而不是 now():
		// 冷却窗起点是"策略判定该触发"的时刻, 与自动/手动路径同语义。
		if err := p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			res := tx.Model(&models.AutomationEvent{}).
				Where("id = ? AND result = ?", ev.ID, models.AutomationResultPendingConfirm).
				Updates(map[string]interface{}{
					"result":     models.AutomationResultExecuted,
					"command_id": exec.CommandID,
				})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				return nil // 并发 confirm/expired: 未翻转, 不动锚点
			}
			return tx.Model(&models.AutomationRule{}).Where("id = ?", rule.ID).
				Update("last_triggered_at", ev.TriggeredAt).Error
		}); err != nil {
			logger.Warn("automation: failed to flip event + cooldown anchor",
				"event_id", ev.ID, "rule_id", rule.ID, "error", err)
		}
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
