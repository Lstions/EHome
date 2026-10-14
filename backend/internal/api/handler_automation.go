package api

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"ehome/backend/internal/automation"
	"ehome/backend/internal/commandexec"
	"ehome/backend/internal/database"
	"ehome/backend/internal/deviceaction"
	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 合法取值集 (设计/自动化策略引擎方案.md v0.1 §3/§5)
var (
	validAutomationTriggerTypes = map[string]bool{
		models.AutomationTriggerSensorThreshold: true,
		models.AutomationTriggerTimeWindow:      true,
		models.AutomationTriggerEvent:           true,
	}
	validAutomationActionTypes = map[string]bool{
		models.AutomationActionDeviceAction: true,
		models.AutomationActionNotification: true,
	}
	validAutomationWindowEdges = map[string]bool{
		models.AutomationWindowEnter:  true,
		models.AutomationWindowExit:   true,
		models.AutomationWindowInside: true,
	}
)

// automationEvaluator 供 CRUD 写路径失效规则缓存 (避免 api→automation 编译期依赖)。
type automationEvaluator interface {
	Invalidate()
}

// automationPlanner 供 confirm 端点人工确认执行 pending_confirm 事件 (裁决 4 确认制闭环)。
// 同 evaluator 模式避免 api→automation 编译期依赖; 单测可传 nil (confirm 端点拒 503)。
type automationPlanner interface {
	ConfirmEvent(ctx context.Context, eventID, actorID uint, sourceIP string) (models.AutomationEvent, error)
}

// automationManualTrigger 供手动触发端点 (POST /automation-rules/:id/trigger)。
// 与 automationPlanner 分离注入, 避免单测传 nil 时 confirm 端点误报 503。
// 实际实现者也是 *automation.Planner (TriggerRule 方法)。
type automationManualTrigger interface {
	TriggerRule(ctx context.Context, ruleID, actorID uint, sourceIP string) (models.AutomationEvent, error)
}

// automationCatalogQuerier 供 device_action 创建/更新时校验 action_id 存在性 +
// action_params_json CanonicalizeParams (§5.3 校验补强)。commandexec.Service 实现。
// 接口注入避免 api→commandexec 直接依赖 (单测可传 mock)。
type automationCatalogQuerier interface {
	Catalog(ctx context.Context, edgeDeviceID uint) ([]commandexec.CatalogItem, error)
}

type createAutomationRuleRequest struct {
	Name                string   `json:"name"`
	Enabled             *bool    `json:"enabled"`
	TriggerType         string   `json:"trigger_type"`
	TriggerSensorName   string   `json:"trigger_sensor_name"`
	TriggerComparator   string   `json:"trigger_comparator"`
	TriggerThreshold    *float64 `json:"trigger_threshold"`
	TriggerDurationSec  int      `json:"trigger_duration_sec"`
	TriggerWindowStart  string   `json:"trigger_window_start"`
	TriggerWindowEnd    string   `json:"trigger_window_end"`
	TriggerWindowEdge   string   `json:"trigger_window_edge"`
	TriggerEdgeDeviceID *uint    `json:"trigger_edge_device_id"`
	ConditionsJSON      string   `json:"conditions_json"`
	ActionType          string   `json:"action_type"`
	ActionDeviceID      *uint    `json:"action_device_id"`
	ActionID            string   `json:"action_id"`
	ActionParamsJSON    string   `json:"action_params_json"`
	ActionLevel         string   `json:"action_level"`
	// Actions 是**多动作**新形态 (方案 §4.1.1 / §3.4 D3)。与旧单动作字段二选一:
	// 两者都给时以 Actions 为准 (数组是显式的完整集合, 标量字段是兼容形态)。
	// 两者都缺 → 动作集合为空 → 400 (fail-closed)。
	Actions          []automationActionInput `json:"actions"`
	CooldownSec      *int                    `json:"cooldown_sec"`
	MaxDailyExec     int                     `json:"max_daily_exec"`
	RequireConfirmed *bool                   `json:"require_confirmed"`
	// RiskAcknowledged 是用户在保存时对**风险动作**的显式确认 (O-2 规则级授权,
	// 实施计划 §0.1)。true = 允许该规则的风险动作在自动路径上跳过人工确认。
	//
	// 为什么必须有这个字段: 授权载体是 automation_rules.risk_acknowledged_at,
	// 但客户端不能直接写时间戳 (那等于自行授权且无法审计"谁在何时确认的")。
	// 请求只表达"我确认过了", 时间戳由服务端在保存成功时写入。
	RiskAcknowledged *bool `json:"risk_acknowledged"`
}

type updateAutomationRuleRequest struct {
	Name                *string  `json:"name"`
	Enabled             *bool    `json:"enabled"`
	TriggerType         *string  `json:"trigger_type"`
	TriggerSensorName   *string  `json:"trigger_sensor_name"`
	TriggerComparator   *string  `json:"trigger_comparator"`
	TriggerThreshold    *float64 `json:"trigger_threshold"`
	TriggerDurationSec  *int     `json:"trigger_duration_sec"`
	TriggerWindowStart  *string  `json:"trigger_window_start"`
	TriggerWindowEnd    *string  `json:"trigger_window_end"`
	TriggerWindowEdge   *string  `json:"trigger_window_edge"`
	TriggerEdgeDeviceID *uint    `json:"trigger_edge_device_id"`
	ConditionsJSON      *string  `json:"conditions_json"`
	ActionType          *string  `json:"action_type"`
	ActionDeviceID      *uint    `json:"action_device_id"`
	ActionID            *string  `json:"action_id"`
	ActionParamsJSON    *string  `json:"action_params_json"`
	ActionLevel         *string  `json:"action_level"`
	// Actions 非 nil 时**整体替换**动作集合 (方案 §3.4 D3); nil 时按标量字段
	// 合并到第一个动作 (旧单动作语义)。两者都涉及动作时以 Actions 为准。
	Actions          []automationActionInput `json:"actions"`
	CooldownSec      *int                    `json:"cooldown_sec"`
	MaxDailyExec     *int                    `json:"max_daily_exec"`
	RequireConfirmed *bool                   `json:"require_confirmed"`
	// RiskAcknowledged 同创建接口: 显式确认/撤回对风险动作的规则级授权。
	// 动作集合发生变化时, 既有授权会被清空, 必须在本请求里重新确认才能通过 V7。
	RiskAcknowledged *bool `json:"risk_acknowledged"`
}

// registerAutomationRoutes 注册自动化策略规则/事件 API (设计/自动化策略引擎方案.md v0.1)。
// 权限对齐 alert: 单主体模式写操作登录即可, evaluator 经 options 注入 (main.go)。
// planner 为确认制闭环 (POST /automation-events/:id/confirm) 提供编排入口, 单测可传 nil。
// catalog 为 §5.3 校验补强提供 action_id 存在性 + params 规范化校验, 单测可传 nil (跳过校验)。
func registerAutomationRoutes(v1 *gin.RouterGroup, db *gorm.DB, evaluator automationEvaluator, planner automationPlanner, trigger automationManualTrigger, catalog automationCatalogQuerier) {
	rules := v1.Group("/automation-rules")
	{
		rules.GET("", listAutomationRules(db))
		rules.POST("", createAutomationRule(db, evaluator, catalog))
		rules.GET("/:id", getAutomationRule(db))
		rules.PUT("/:id", updateAutomationRule(db, evaluator, catalog))
		rules.DELETE("/:id", deleteAutomationRule(db, evaluator))
		rules.PATCH("/:id/enabled", patchAutomationRuleEnabled(db, evaluator))
		rules.POST("/:id/trigger", triggerAutomationRule(trigger))
	}

	events := v1.Group("/automation-events")
	{
		events.GET("", listAutomationEvents(db))
		events.POST("/:id/confirm", confirmAutomationEvent(planner))
	}
}

// GET /api/v1/automation-rules
func listAutomationRules(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var items []models.AutomationRule
		q := db.Model(&models.AutomationRule{})
		if tt := c.Query("trigger_type"); tt != "" {
			q = q.Where("trigger_type = ?", tt)
		}
		if tid := c.Query("trigger_edge_device_id"); tid != "" {
			if id, err := strconv.ParseUint(tid, 10, 64); err == nil {
				q = q.Where("trigger_edge_device_id = ?", id)
			}
		}
		if at := c.Query("action_type"); at != "" {
			q = q.Where("action_type = ?", at)
		}
		if err := q.Order("id DESC").Find(&items).Error; err != nil {
			Error(c, 500, "查询自动化策略失败")
			return
		}
		// 动作集合来自子表 (方案 §3.2.3 M1 / §3.4 D4): 一次查询批量填充, 避免 N+1。
		if err := database.AttachRuleActions(db, items); err != nil {
			Error(c, 500, "查询策略动作失败")
			return
		}
		Success(c, items)
	}
}

// GET /api/v1/automation-rules/:id
func getAutomationRule(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var rule models.AutomationRule
		if err := db.First(&rule, c.Param("id")).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				Error(c, 404, "策略不存在")
				return
			}
			Error(c, 500, "查询策略失败")
			return
		}
		attached := []models.AutomationRule{rule}
		if err := database.AttachRuleActions(db, attached); err != nil {
			Error(c, 500, "查询策略动作失败")
			return
		}
		Success(c, attached[0])
	}
}

// POST /api/v1/automation-rules
func createAutomationRule(db *gorm.DB, evaluator automationEvaluator, catalog automationCatalogQuerier) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req createAutomationRuleRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, 400, "参数错误: "+err.Error())
			return
		}
		createRequireConfirmed := false
		if req.RequireConfirmed != nil {
			createRequireConfirmed = *req.RequireConfirmed
		}
		// ── 动作集合解析 (多动作新形态 + 旧单动作兼容, 方案 §3.4 D3) ──
		scalarProvided := req.ActionType != "" || req.ActionID != "" || req.ActionParamsJSON != "" ||
			req.ActionLevel != "" || req.ActionDeviceID != nil
		actions := resolveRuleActions(req.Actions, nil, req.ActionType, derefUint(req.ActionDeviceID),
			req.ActionID, req.ActionParamsJSON, req.ActionLevel, scalarProvided)
		if msg := validateActionSequence(actions); msg != "" {
			ErrorWithCode(c, 400, "invalid_automation_rule", msg)
			return
		}
		for _, a := range actions {
			if msg := validateActionFields(a); msg != "" {
				ErrorWithCode(c, 400, "invalid_automation_rule", msg)
				return
			}
		}
		first := actions[0]
		if msg := validateAutomationRuleFields(req.Name, req.TriggerType, first.ActionType,
			req.TriggerEdgeDeviceID, req.TriggerSensorName, req.TriggerComparator, req.TriggerThreshold,
			req.TriggerWindowStart, req.TriggerWindowEnd, req.TriggerWindowEdge,
			&first.ActionDeviceID, first.ActionID, first.ActionLevel,
			createRequireConfirmed, false); msg != "" {
			ErrorWithCode(c, 400, "invalid_automation_rule", msg)
			return
		}
		// §5.3 校验补强: cooldown_sec / max_daily_exec 边界 + device_action 的
		// action_id Catalog 存在性 + action_params_json CanonicalizeParams (逐动作)。
		if msg := validateAutomationRuleConstraints(req.CooldownSec, req.MaxDailyExec); msg != "" {
			ErrorWithCode(c, 400, "invalid_automation_rule", msg)
			return
		}
		if msg := validateActionsCatalog(c.Request.Context(), catalog, actions); msg != "" {
			ErrorWithCode(c, 400, "invalid_automation_rule", msg)
			return
		}
		// V7: **任一**风险动作 + require_confirmed=false 且未显式授权 → 拒绝保存
		// (方案 §4.1.1 V7 / §3.2.5 第 4 步)。杜绝"配了一条永远不会执行的规则"。
		riskAcknowledged := req.RiskAcknowledged != nil && *req.RiskAcknowledged
		if msg, code := validateRiskActionAuthorization(c.Request.Context(), catalog,
			actions, createRequireConfirmed, riskAcknowledged); msg != "" {
			ErrorWithCode(c, 400, code, msg)
			return
		}
		rule := models.AutomationRule{
			Name:               req.Name,
			TriggerType:        req.TriggerType,
			TriggerSensorName:  req.TriggerSensorName,
			TriggerComparator:  req.TriggerComparator,
			TriggerDurationSec: req.TriggerDurationSec,
			TriggerWindowStart: req.TriggerWindowStart,
			TriggerWindowEnd:   req.TriggerWindowEnd,
			TriggerWindowEdge:  req.TriggerWindowEdge,
			ConditionsJSON:     req.ConditionsJSON,
			// 镜像列 = seq 最小的动作 (兼容期只读投影, 方案 §3.4 D2)。
			ActionType:       first.ActionType,
			ActionDeviceID:   first.ActionDeviceID,
			ActionID:         first.ActionID,
			ActionParamsJSON: first.ActionParamsJSON,
			ActionLevel:      first.ActionLevel,
			MaxDailyExec:     req.MaxDailyExec,
		}
		if req.TriggerEdgeDeviceID != nil {
			rule.TriggerEdgeDeviceID = *req.TriggerEdgeDeviceID
		}
		if req.TriggerThreshold != nil {
			rule.TriggerThreshold = *req.TriggerThreshold
		}
		if req.Enabled != nil {
			rule.Enabled = *req.Enabled
		} else {
			rule.Enabled = true
		}
		if req.CooldownSec != nil {
			rule.CooldownSec = *req.CooldownSec
		} else {
			rule.CooldownSec = 300
		}
		if req.RequireConfirmed != nil {
			rule.RequireConfirmed = *req.RequireConfirmed
		}
		if riskAcknowledged {
			now := time.Now().UTC()
			rule.RiskAcknowledgedAt = &now
		}
		// 规则行与动作子表必须**同事务**写入: 分两次写会留下"规则已存在、动作
		// 还没有"的窗口, 该窗口内的触发会按空动作集合处理 (规则静默不执行)。
		if err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&rule).Error; err != nil {
				return err
			}
			for i := range actions {
				actions[i].ID = 0
				actions[i].RuleID = rule.ID
				if err := tx.Create(&actions[i]).Error; err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			Error(c, 500, "创建策略失败: "+err.Error())
			return
		}
		rule.Actions = actions
		if evaluator != nil {
			evaluator.Invalidate()
		}
		Success(c, rule)
	}
}

// PUT /api/v1/automation-rules/:id
func updateAutomationRule(db *gorm.DB, evaluator automationEvaluator, catalog automationCatalogQuerier) gin.HandlerFunc {
	return func(c *gin.Context) {
		var rule models.AutomationRule
		if err := db.First(&rule, c.Param("id")).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				Error(c, 404, "策略不存在")
				return
			}
			Error(c, 500, "查询策略失败")
			return
		}
		var req updateAutomationRuleRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, 400, "参数错误: "+err.Error())
			return
		}
		// ── 动作集合合并 (多动作新形态 + 旧单动作兼容, 方案 §3.4 D3) ──
		//
		// 必须先读**库中现有动作集合**再合并: 标量字段的合并目标是"当前第一个
		// 动作", 而不是凭空新建一个 —— 否则只改 action_id 的老前端请求会丢掉
		// 其余动作 (多动作规则被静默降级成单动作)。
		current := loadActionsOrMirror(db, rule)
		scalarProvided := req.ActionType != nil || req.ActionID != nil || req.ActionParamsJSON != nil ||
			req.ActionLevel != nil || req.ActionDeviceID != nil
		// 标量候选值: 未提供的字段取当前第一个动作的值。
		first := current[0]
		scalarType := pickStr(req.ActionType, first.ActionType)
		scalarDeviceID := first.ActionDeviceID
		if req.ActionDeviceID != nil {
			scalarDeviceID = *req.ActionDeviceID
		}
		scalarActionID := pickStr(req.ActionID, first.ActionID)
		scalarParams := pickStr(req.ActionParamsJSON, first.ActionParamsJSON)
		scalarLevel := pickStr(req.ActionLevel, first.ActionLevel)
		actions := resolveRuleActions(req.Actions, current, scalarType, scalarDeviceID,
			scalarActionID, scalarParams, scalarLevel, scalarProvided)
		if msg := validateActionSequence(actions); msg != "" {
			ErrorWithCode(c, 400, "invalid_automation_rule", msg)
			return
		}
		for _, a := range actions {
			if msg := validateActionFields(a); msg != "" {
				ErrorWithCode(c, 400, "invalid_automation_rule", msg)
				return
			}
		}
		// 合并候选值后校验 (更新或现值)。trigger 校验用请求里的候选触发字段;
		// 动作校验用**合并后的第一个动作**。
		name := pickStr(req.Name, rule.Name)
		tt := pickStr(req.TriggerType, rule.TriggerType)
		teid := rule.TriggerEdgeDeviceID
		if req.TriggerEdgeDeviceID != nil {
			teid = *req.TriggerEdgeDeviceID
		}
		sn := pickStr(req.TriggerSensorName, rule.TriggerSensorName)
		cmp := pickStr(req.TriggerComparator, rule.TriggerComparator)
		thr := rule.TriggerThreshold
		if req.TriggerThreshold != nil {
			thr = *req.TriggerThreshold
		}
		ws := pickStr(req.TriggerWindowStart, rule.TriggerWindowStart)
		we := pickStr(req.TriggerWindowEnd, rule.TriggerWindowEnd)
		we2 := pickStr(req.TriggerWindowEdge, rule.TriggerWindowEdge)
		rc := rule.RequireConfirmed
		if req.RequireConfirmed != nil {
			rc = *req.RequireConfirmed
		}
		if msg := validateAutomationRuleFields(name, tt, first.ActionType, &teid, sn, cmp, &thr, ws, we, we2,
			&first.ActionDeviceID, first.ActionID, first.ActionLevel, rc, true); msg != "" {
			ErrorWithCode(c, 400, "invalid_automation_rule", msg)
			return
		}
		// §5.3 校验补强: cooldown_sec / max_daily_exec 边界 + 逐动作 Catalog 校验。
		cs := rule.CooldownSec
		if req.CooldownSec != nil {
			cs = *req.CooldownSec
		}
		mde := rule.MaxDailyExec
		if req.MaxDailyExec != nil {
			mde = *req.MaxDailyExec
		}
		if msg := validateAutomationRuleConstraints(&cs, mde); msg != "" {
			ErrorWithCode(c, 400, "invalid_automation_rule", msg)
			return
		}
		if msg := validateActionsCatalog(c.Request.Context(), catalog, actions); msg != "" {
			ErrorWithCode(c, 400, "invalid_automation_rule", msg)
			return
		}
		// ── 授权失效 (安全关键, 实施计划 §0.1): 动作集合变化即清空 risk_acknowledged_at ──
		//
		// 为什么必须清空: 用户授权的是"这条规则的这些动作"。若把动作从 A 改成 B
		// (或改目标设备/参数) 而保留旧授权, 旧授权会静默地把 B 也放行 —— 用户从未
		// 确认过 B, 而 B 可能是完全不同的物理操作。仅改名称/冷却等非动作字段不失效。
		//
		// **逐 seq 比较** (B1 遗留债务的收口): 只比第一个动作会让"把第 2 个动作换成
		// 另一个风险动作"保留旧授权, 那是安全漏洞。
		changed := automationActionSetChanged(current, actions)
		// ── P1-D: "同一次请求里既改动作集合、又声明已授权" 必须显式 400 ──
		//
		// 缺陷现象 (评审 verify-b2 P1-D): PUT 同时带 actions(已变) + risk_acknowledged=true
		// + require_confirmed=false 时返回 200, 但落库是「风险动作 + require_confirmed=false
		// + risk_acknowledged_at IS NULL」——正是 V7 声称要杜绝的规则。
		//
		// 根因: V7 看到请求里的 true 而放行; 随后的写入 switch 又因 changed 为真而**无条件**
		// 清空授权列 (清空优先于请求字段)。两处判定不在同一个分支里, 于是"校验通过"与
		// "落库结果"不一致。
		//
		// 为什么不静默忽略 true 而选择 400: 静默降级会存下一条"永远不会执行、且不 warn"
		// 的风险规则, 用户以为已授权 (C5 同族)。显式报错把冲突告知用户, 并提供可执行路径:
		// 先保存动作改动, 再单独提交一次授权确认。
		//
		// 与实施计划 §0.1 一致: 动作集合变化即清空授权, 强制用户**重新确认**——
		// "重新"意味着另一次请求, 而不是在同一次请求里顺手声明。
		if changed && req.RiskAcknowledged != nil && *req.RiskAcknowledged {
			ErrorWithCode(c, 400, "action_changed_requires_reconfirmation",
				"动作集合已变更: 授权已失效, 请先保存动作改动, 再单独提交一次 risk_acknowledged=true 完成重新确认")
			return
		}
		acknowledged := rule.RiskAcknowledgedAt != nil
		if changed {
			acknowledged = false
		}
		if req.RiskAcknowledged != nil {
			acknowledged = *req.RiskAcknowledged
		}
		// V7: 清空后若仍配 require_confirmed=false 的风险动作 → 拒绝保存, 强制重新确认。
		// 逐动作检查: 只看第一个动作会漏掉"第 2 个是风险动作"的规则。
		if msg, code := validateRiskActionAuthorization(c.Request.Context(), catalog,
			actions, rc, acknowledged); msg != "" {
			ErrorWithCode(c, 400, code, msg)
			return
		}
		updates := map[string]interface{}{}
		if req.Name != nil {
			updates["name"] = *req.Name
		}
		if req.Enabled != nil {
			updates["enabled"] = *req.Enabled
		}
		if req.TriggerType != nil {
			updates["trigger_type"] = *req.TriggerType
		}
		if req.TriggerSensorName != nil {
			updates["trigger_sensor_name"] = *req.TriggerSensorName
		}
		if req.TriggerComparator != nil {
			updates["trigger_comparator"] = *req.TriggerComparator
		}
		if req.TriggerThreshold != nil {
			updates["trigger_threshold"] = *req.TriggerThreshold
		}
		if req.TriggerDurationSec != nil {
			updates["trigger_duration_sec"] = *req.TriggerDurationSec
		}
		if req.TriggerWindowStart != nil {
			updates["trigger_window_start"] = *req.TriggerWindowStart
		}
		if req.TriggerWindowEnd != nil {
			updates["trigger_window_end"] = *req.TriggerWindowEnd
		}
		if req.TriggerWindowEdge != nil {
			updates["trigger_window_edge"] = *req.TriggerWindowEdge
		}
		if req.TriggerEdgeDeviceID != nil {
			updates["trigger_edge_device_id"] = *req.TriggerEdgeDeviceID
		}
		if req.ConditionsJSON != nil {
			updates["conditions_json"] = *req.ConditionsJSON
		}
		if req.CooldownSec != nil {
			updates["cooldown_sec"] = *req.CooldownSec
		}
		if req.MaxDailyExec != nil {
			updates["max_daily_exec"] = *req.MaxDailyExec
		}
		if req.RequireConfirmed != nil {
			updates["require_confirmed"] = *req.RequireConfirmed
		}
		// 授权列的写入必须显式表达"清空"(nil), 不能用 map 的省略语义 ——
		// 省略 = 保持原值, 那正是"改动作后旧授权仍然生效"的漏洞。
		switch {
		case changed:
			// 动作集合变化 → 强制清空 (无论请求是否带 risk_acknowledged)。
			// 本条优先于请求字段: 用户不能在"改动作"的同一次请求里顺手沿用旧授权,
			// 必须重新显式确认。
			updates["risk_acknowledged_at"] = nil
		case req.RiskAcknowledged != nil && *req.RiskAcknowledged:
			updates["risk_acknowledged_at"] = time.Now().UTC()
		case req.RiskAcknowledged != nil:
			updates["risk_acknowledged_at"] = nil
		}
		// 规则字段更新 + 子表替换 + 镜像列回写必须**同事务**: 任何一步失败都
		// 整体回滚, 不留"规则已改、动作没换"的中间态。
		if err := db.Transaction(func(tx *gorm.DB) error {
			if len(updates) > 0 {
				if err := tx.Model(&models.AutomationRule{}).Where("id = ?", rule.ID).
					Updates(updates).Error; err != nil {
					return err
				}
			}
			return database.ReplaceRuleActions(tx, rule.ID, actions)
		}); err != nil {
			Error(c, 500, "更新策略失败: "+err.Error())
			return
		}
		db.First(&rule, rule.ID)
		if evaluator != nil {
			evaluator.Invalidate()
		}
		attached := []models.AutomationRule{rule}
		if err := database.AttachRuleActions(db, attached); err == nil {
			rule = attached[0]
		}
		Success(c, rule)
	}
}

// DELETE /api/v1/automation-rules/:id
func deleteAutomationRule(db *gorm.DB, evaluator automationEvaluator) gin.HandlerFunc {
	return func(c *gin.Context) {
		var rule models.AutomationRule
		if err := db.First(&rule, c.Param("id")).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				Error(c, 404, "策略不存在")
				return
			}
			Error(c, 500, "查询策略失败")
			return
		}
		if err := db.Delete(&rule).Error; err != nil {
			Error(c, 500, "删除策略失败: "+err.Error())
			return
		}
		if evaluator != nil {
			evaluator.Invalidate()
		}
		Success(c, gin.H{"deleted": true, "id": rule.ID})
	}
}

// PATCH /api/v1/automation-rules/:id/enabled
func patchAutomationRuleEnabled(db *gorm.DB, evaluator automationEvaluator) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body struct {
			Enabled *bool `json:"enabled"`
		}
		if err := c.ShouldBindJSON(&body); err != nil || body.Enabled == nil {
			Error(c, 400, "参数错误: enabled 必填")
			return
		}
		var rule models.AutomationRule
		if err := db.First(&rule, c.Param("id")).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				Error(c, 404, "策略不存在")
				return
			}
			Error(c, 500, "查询策略失败")
			return
		}
		if err := db.Model(&rule).Update("enabled", *body.Enabled).Error; err != nil {
			Error(c, 500, "更新失败: "+err.Error())
			return
		}
		if evaluator != nil {
			evaluator.Invalidate()
		}
		Success(c, gin.H{"id": rule.ID, "enabled": *body.Enabled})
	}
}

// GET /api/v1/automation-events
//
// 分页契约 (架构与接口评估 P1.2 裁决: items + total): 查询参数 page (默认 1, <1 归 1) /
// page_size (默认 20, 超出 [1,200] 归 20), 响应 data = {items,total,page,page_size}。
// 参数语义与 /vendors、/device-configs 完全一致; 结构用 items 而非 list ——
// /device-configs 的 {list,...} 是待收敛的旧方言, 不在此扩散。rule_id / result 筛选原样保留。
//
// 历史 (为什么必须改): 本端点曾硬编码 Limit(500) 静默截断。实测交叉筛选证明真实事件
// >1006 条 (rule_id=1、rule_id=3、result=executed、result=expired 各自均触顶 500,
// rule_id=3&result=expired 仍触顶), 即用户只看到一半历史且**没有任何截断提示**。
// 改为真分页后 total 如实反映过滤后的全量条数, 前端据此渲染分页器。
func listAutomationEvents(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
		if page < 1 {
			page = 1
		}
		if pageSize < 1 || pageSize > 200 {
			pageSize = 20
		}
		q := db.Model(&models.AutomationEvent{})
		if rid := c.Query("rule_id"); rid != "" {
			if id, err := strconv.ParseUint(rid, 10, 64); err == nil {
				q = q.Where("rule_id = ?", id)
			}
		}
		if res := c.Query("result"); res != "" {
			q = q.Where("result = ?", res)
		}
		var total int64
		if err := q.Count(&total).Error; err != nil {
			Error(c, 500, "查询策略事件失败")
			return
		}
		// 非 nil 空切片: 空集序列化为 [] 而非 null (与 handler_data_source.go 同约定)。
		items := make([]models.AutomationEvent, 0)
		if err := q.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error; err != nil {
			Error(c, 500, "查询策略事件失败")
			return
		}
		Success(c, gin.H{"items": items, "total": total, "page": page, "page_size": pageSize})
	}
}

// POST /api/v1/automation-rules/:id/trigger
// 手动触发端点: 跳过条件评估与确认制 (用户点击即确认),
// 但保留 cooldown / max_daily_exec / 日熔断安全门禁。
// 返回落库的 AutomationEvent (含 result), 前端据此展示执行状态。
func triggerAutomationRule(trigger automationManualTrigger) gin.HandlerFunc {
	return func(c *gin.Context) {
		if trigger == nil {
			Error(c, 503, "自动化引擎未启用")
			return
		}
		ruleID, err := strconv.ParseUint(c.Param("id"), 10, 64)
		if err != nil || ruleID == 0 {
			Error(c, 400, "无效的规则 ID")
			return
		}
		actorID, _ := c.Get("subject_id")
		aid, _ := actorID.(uint)
		if aid == 0 {
			Error(c, 401, "未认证")
			return
		}
		ev, err := trigger.TriggerRule(c.Request.Context(), uint(ruleID), aid, c.ClientIP())
		if err != nil {
			switch {
			case errors.Is(err, automation.ErrTriggerRuleNotFound):
				Error(c, 404, "策略不存在")
			case errors.Is(err, automation.ErrTriggerRuleDisabled):
				Error(c, 409, "策略已禁用, 无法手动触发")
			default:
				Error(c, 500, "触发失败: "+err.Error())
			}
			return
		}
		Success(c, ev)
	}
}

// POST /api/v1/automation-events/:id/confirm
// 裁决 4 确认制闭环: 人工确认 pending_confirm 事件后真正下发动作。
// 前置: 操作者须先经 POST /api/v1/account/reauthenticate 重新认证 (校验密码后
// 由 authservice.AuthenticateSingleUser 刷新 LastLoginAt, 见 handler_account.go),
// 以通过 commandexec 的近认证门 —— 该窗口为 10 分钟
// (commandexec/confirmation.go: recentAuthenticationWindow)。
// 无 body; planner 即铸即销 token, token 不跨请求存储。
func confirmAutomationEvent(planner automationPlanner) gin.HandlerFunc {
	return func(c *gin.Context) {
		if planner == nil {
			Error(c, 503, "自动化引擎未启用")
			return
		}
		eventID, err := strconv.ParseUint(c.Param("id"), 10, 64)
		if err != nil || eventID == 0 {
			Error(c, 400, "无效的事件 ID")
			return
		}
		actorID, _ := c.Get("subject_id")
		aid, _ := actorID.(uint)
		if aid == 0 {
			Error(c, 401, "未认证")
			return
		}
		ev, err := planner.ConfirmEvent(c.Request.Context(), uint(eventID), aid, c.ClientIP())
		if err != nil {
			switch {
			case errors.Is(err, automation.ErrConfirmEventNotFound):
				Error(c, 404, "事件不存在")
			case errors.Is(err, automation.ErrConfirmEventExpired):
				ErrorWithCode(c, 409, "EVENT_EXPIRED", "确认窗口已超时 (24h), 事件已过期")
			case errors.Is(err, automation.ErrConfirmNotPending):
				Error(c, 409, "事件不在待确认状态 (可能已执行/过期/已确认)")
			case errors.Is(err, automation.ErrConfirmRuleMissing):
				Error(c, 410, "规则已删除, 无法确认")
			case errors.Is(err, automation.ErrConfirmActionChanged):
				ErrorWithCode(c, 409, "RULE_CHANGED", "规则动作在触发后已变更, 为保一致性拒绝确认; 请按新配置重新触发")
			case errors.Is(err, automation.ErrConfirmInvalidParams):
				ErrorWithCode(c, 409, "INVALID_PARAMS", "规则动作参数非法, 无法确认")
			case errors.Is(err, commandexec.ErrRecentAuthRequired):
				ErrorWithCode(c, 403, "RECENT_AUTH_REQUIRED", "需先完成手动确认 (刷新近认证)")
			case errors.Is(err, commandexec.ErrConfirmationInvalid):
				ErrorWithCode(c, 409, "CONFIRMATION_INVALID", "确认令牌无效或已过期")
			default:
				Error(c, 500, "确认失败: "+err.Error())
			}
			return
		}
		Success(c, ev)
	}
}

// validateAutomationRuleFields 候选值校验 (fail-closed, 与 alert 同款风格)。
// requireConfirmed 传入合并后的候选值: 裁决 4 确认制仅对 device_action 有意义
// (notification 是纯通知无需人工确认), 非 device_action 配 require_confirmed=true 拒绝。
// event 触发器: isUpdate=false (创建) 时禁配 (无 sensor 值与 trigger_value 语义错位);
// isUpdate=true (更新) 时兼容存量规则 (评审③灰度: 不锁死历史 event 规则)。
func validateAutomationRuleFields(name, triggerType, actionType string,
	triggerEdgeDeviceID *uint, sensorName, comparator string, threshold *float64,
	windowStart, windowEnd, windowEdge string,
	actionDeviceID *uint, actionID, actionLevel string,
	requireConfirmed bool, isUpdate bool) string {
	if name == "" {
		return "name 不能为空"
	}
	if !validAutomationTriggerTypes[triggerType] {
		return "trigger_type 非法: " + triggerType
	}
	if !validAutomationActionTypes[actionType] {
		return "action_type 非法: " + actionType
	}
	// event 触发器: 创建时禁配 (评审③: 其无 sensor 值语义, 与 trigger_value 错位);
	// 更新存量规则不拦 (历史 event 规则仍可改其它字段), 但创建新 event 规则拒绝。
	if triggerType == models.AutomationTriggerEvent && !isUpdate {
		return "trigger_type=event 暂不支持创建 (语义未冻结)"
	}
	switch triggerType {
	case models.AutomationTriggerSensorThreshold:
		if triggerEdgeDeviceID == nil || *triggerEdgeDeviceID == 0 {
			return "trigger_edge_device_id 不能为空 (sensor_threshold)"
		}
		if sensorName == "" {
			return "trigger_sensor_name 不能为空 (sensor_threshold)"
		}
		if !validComparators[comparator] {
			return "trigger_comparator 非法: " + comparator
		}
		if threshold == nil {
			return "trigger_threshold 不能为空 (sensor_threshold)"
		}
	case models.AutomationTriggerTimeWindow:
		if windowStart == "" || windowEnd == "" {
			return "trigger_window_start/end 不能为空 (time_window)"
		}
		if !validAutomationWindowEdges[windowEdge] {
			return "trigger_window_edge 非法: " + windowEdge
		}
	}
	switch actionType {
	case models.AutomationActionDeviceAction:
		if actionDeviceID == nil || *actionDeviceID == 0 {
			return "action_device_id 不能为空 (device_action)"
		}
		if actionID == "" {
			return "action_id 不能为空 (device_action)"
		}
	case models.AutomationActionNotification:
		if actionLevel == "" {
			return "action_level 不能为空 (notification)"
		}
	}
	// 交叉校验: require_confirmed 仅 device_action 有意义 (notification 本就纯通知)。
	if requireConfirmed && actionType != models.AutomationActionDeviceAction {
		return "require_confirmed=true 仅对 action_type=device_action 有意义 (notification 是纯通知)"
	}
	return ""
}

// validateAutomationRuleConstraints §5.3 校验补强: cooldown_sec / max_daily_exec 边界。
// cooldownSec 为 nil 时跳过 (创建时由应用层赋默认值); 非 nil 校验 [0, 86400]。
// maxDailyExec 校验 [0, 1000] (0=不限)。
func validateAutomationRuleConstraints(cooldownSec *int, maxDailyExec int) string {
	if cooldownSec != nil && (*cooldownSec < 0 || *cooldownSec > 86400) {
		return "cooldown_sec 必须在 [0, 86400] 范围内"
	}
	if maxDailyExec < 0 || maxDailyExec > 1000 {
		return "max_daily_exec 必须在 [0, 1000] 范围内"
	}
	return ""
}

// validateDeviceActionCatalog §5.3 校验补强: device_action 的 action_id Catalog 存在性 +
// action_params_json CanonicalizeParams。catalog=nil 时跳过 (单测兼容)。
// 校验逻辑: Catalog(ctx, deviceID) → 遍历 items 找 actionID → 不存在则 400;
// 找到后取 Definition.InputSchema → CanonicalizeParams(schema, rawParams) → 非法则 400。
// Catalog 需要 edge_device 存在, action_device_id 无效时 Catalog 本身报错。
func validateDeviceActionCatalog(ctx context.Context, catalog automationCatalogQuerier,
	deviceID uint, actionID string, paramsJSON string) string {
	if catalog == nil {
		return "" // 无 catalog 注入时跳过 (单测/兼容路径)
	}
	items, err := catalog.Catalog(ctx, deviceID)
	if err != nil {
		return "action_device_id 无效: " + err.Error()
	}
	var schema deviceaction.ParameterSchema
	found := false
	for _, item := range items {
		if item.Definition.ID == actionID {
			schema = item.Definition.InputSchema
			found = true
			break
		}
	}
	if !found {
		return "action_id 在设备能力目录中不存在: " + actionID
	}
	// action_params_json 为空时传 "{}" (CanonicalizeParams 内部会处理空输入)。
	raw := json.RawMessage(paramsJSON)
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	if _, err := deviceaction.CanonicalizeParams(schema, raw); err != nil {
		return "action_params_json 非法: " + err.Error()
	}
	return ""
}

// ── 风险动作的规则级授权 (O-2, 实施计划 §0.1) ──────────────────────────────

// automationActionSetChanged 判定更新请求是否**改变了规则的动作集合**。
//
// 口径 (实施计划 §0.1 的授权失效规则): 增删动作、或任一动作的 action_id /
// action_device_id / action_type / action_params_json 变化 → 视为变化。
// 仅改名称、冷却、触发条件等非动作字段不失效。
//
// 【B2 收口 · 原 B1 显式债务】本函数在 B1 期间只比较单动作四列, 并在注释里留下
// "B2 上子表后必须扩展为逐 seq 比较动作集合" 的债务。现已扩展: 比较对象是
// **整个动作集合** (长度 + 逐 seq 字段)。为什么必须扩展 —— 只比第一个动作时,
// 多动作规则"把第 2 个动作换成另一个风险动作"会保留旧授权, 而用户从未确认过
// 新动作, 旧授权却把它放行 (安全漏洞)。回归锁见
// TestUpdateSecondActionChangeClearsAcknowledgement。
//
// current 必须由调用方按"库中当前动作集合"传入 (子表为空时回落镜像列)。
func automationActionSetChanged(current, incoming []models.AutomationRuleAction) bool {
	return !actionSetsEqual(current, incoming)
}

// validateRiskActionAuthorization 实现校验 V7 (方案 §4.1.1 V7 / §8 A-10):
// **集合中含风险动作** 且 require_confirmed=false 且未授权 → 拒绝保存。
//
// 为什么是 fail-closed: 不拦的话会存下一条"永远不会执行"的规则 —— 自动路径遇
// 确认门落 confirmation_required, 用户在无人值守场景下等不到任何物理动作。
// C5 的根因之一正是"用户不知道自己的规则不可能成功", 所以在保存入口收口比事后
// 通知更强 (方案 §3.2.5 第 4 步)。
//
// 为什么**逐动作**检查 (B2): 只看第一个动作时, "第 1 个普通、第 2 个风险"的规则
// 会绕过 V7 落库, 结局同样是"永远不会执行"。
//
// 返回 (错误文案, 错误码)。错误码固定为 risk_action_requires_confirmation (A-10 契约)。
//
// catalog 为 nil 时**无法判定风险等级**: 这是单测兼容路径 (与 validateDeviceActionCatalog
// 同款取舍), 此时跳过 V7 而不是猜测 —— 猜测会把风险动作误判成普通动作 (fail-open)。
// 生产装配路径上 catalog 恒非 nil (routes.go 注入 commandexec.Service)。
func validateRiskActionAuthorization(ctx context.Context, catalog automationCatalogQuerier,
	actions []models.AutomationRuleAction, requireConfirmed, acknowledged bool) (string, string) {
	const code = "risk_action_requires_confirmation"
	if requireConfirmed || acknowledged || catalog == nil {
		return "", ""
	}
	for _, a := range actions {
		if a.ActionType != models.AutomationActionDeviceAction || a.ActionDeviceID == 0 {
			continue
		}
		items, err := catalog.Catalog(ctx, a.ActionDeviceID)
		if err != nil {
			// Catalog 查不到目标设备时, 前面的 validateActionsCatalog 已经给出
			// 更准确的报错; 这里不重复报, 也不据此判定风险 (避免误报 V7)。
			continue
		}
		for _, item := range items {
			if item.Definition.ID != a.ActionID {
				continue
			}
			if item.Definition.RiskLevel.RequiresConfirmation() {
				return "该动作是风险操作: 自动执行需要保存时显式确认 (risk_acknowledged=true), 或改回 require_confirmed=true 走人工确认",
					code
			}
			break
		}
	}
	return "", ""
}

// pickStr 合并候选值辅助 (alert 同款风格)。
func pickStr(p *string, cur string) string {
	if p != nil {
		return *p
	}
	return cur
}
