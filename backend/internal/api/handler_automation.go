package api

import (
	"strconv"

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
	CooldownSec         *int     `json:"cooldown_sec"`
	MaxDailyExec        int      `json:"max_daily_exec"`
	RequireConfirmed    *bool    `json:"require_confirmed"`
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
	CooldownSec         *int     `json:"cooldown_sec"`
	MaxDailyExec        *int     `json:"max_daily_exec"`
	RequireConfirmed    *bool    `json:"require_confirmed"`
}

// registerAutomationRoutes 注册自动化策略规则/事件 API (设计/自动化策略引擎方案.md v0.1)。
// 权限对齐 alert: 单主体模式写操作登录即可, evaluator 经 options 注入 (main.go)。
func registerAutomationRoutes(v1 *gin.RouterGroup, db *gorm.DB, evaluator automationEvaluator) {
	rules := v1.Group("/automation-rules")
	{
		rules.GET("", listAutomationRules(db))
		rules.POST("", createAutomationRule(db, evaluator))
		rules.GET("/:id", getAutomationRule(db))
		rules.PUT("/:id", updateAutomationRule(db, evaluator))
		rules.DELETE("/:id", deleteAutomationRule(db, evaluator))
		rules.PATCH("/:id/enabled", patchAutomationRuleEnabled(db, evaluator))
	}

	events := v1.Group("/automation-events")
	{
		events.GET("", listAutomationEvents(db))
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
		Success(c, rule)
	}
}

// POST /api/v1/automation-rules
func createAutomationRule(db *gorm.DB, evaluator automationEvaluator) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req createAutomationRuleRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, 400, "参数错误: "+err.Error())
			return
		}
		if msg := validateAutomationRuleFields(req.Name, req.TriggerType, req.ActionType,
			req.TriggerEdgeDeviceID, req.TriggerSensorName, req.TriggerComparator, req.TriggerThreshold,
			req.TriggerWindowStart, req.TriggerWindowEnd, req.TriggerWindowEdge,
			req.ActionDeviceID, req.ActionID, req.ActionLevel); msg != "" {
			ErrorWithCode(c, 400, "invalid_automation_rule", msg)
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
			ActionType:         req.ActionType,
			ActionID:           req.ActionID,
			ActionParamsJSON:   req.ActionParamsJSON,
			ActionLevel:        req.ActionLevel,
			MaxDailyExec:       req.MaxDailyExec,
		}
		if req.TriggerEdgeDeviceID != nil {
			rule.TriggerEdgeDeviceID = *req.TriggerEdgeDeviceID
		}
		if req.TriggerThreshold != nil {
			rule.TriggerThreshold = *req.TriggerThreshold
		}
		if req.ActionDeviceID != nil {
			rule.ActionDeviceID = *req.ActionDeviceID
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
		if err := db.Create(&rule).Error; err != nil {
			Error(c, 500, "创建策略失败: "+err.Error())
			return
		}
		if evaluator != nil {
			evaluator.Invalidate()
		}
		Success(c, rule)
	}
}

// PUT /api/v1/automation-rules/:id
func updateAutomationRule(db *gorm.DB, evaluator automationEvaluator) gin.HandlerFunc {
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
		// 合并候选值后校验 (更新或现值)
		name := pickStr(req.Name, rule.Name)
		tt := pickStr(req.TriggerType, rule.TriggerType)
		at := pickStr(req.ActionType, rule.ActionType)
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
		adevid := rule.ActionDeviceID
		if req.ActionDeviceID != nil {
			adevid = *req.ActionDeviceID
		}
		aid := pickStr(req.ActionID, rule.ActionID)
		alv := pickStr(req.ActionLevel, rule.ActionLevel)
		if msg := validateAutomationRuleFields(name, tt, at, &teid, sn, cmp, &thr, ws, we, we2, &adevid, aid, alv); msg != "" {
			ErrorWithCode(c, 400, "invalid_automation_rule", msg)
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
		if req.ActionType != nil {
			updates["action_type"] = *req.ActionType
		}
		if req.ActionDeviceID != nil {
			updates["action_device_id"] = *req.ActionDeviceID
		}
		if req.ActionID != nil {
			updates["action_id"] = *req.ActionID
		}
		if req.ActionParamsJSON != nil {
			updates["action_params_json"] = *req.ActionParamsJSON
		}
		if req.ActionLevel != nil {
			updates["action_level"] = *req.ActionLevel
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
		if err := db.Model(&rule).Updates(updates).Error; err != nil {
			Error(c, 500, "更新策略失败: "+err.Error())
			return
		}
		db.First(&rule, rule.ID)
		if evaluator != nil {
			evaluator.Invalidate()
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
func listAutomationEvents(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var items []models.AutomationEvent
		q := db.Model(&models.AutomationEvent{})
		if rid := c.Query("rule_id"); rid != "" {
			if id, err := strconv.ParseUint(rid, 10, 64); err == nil {
				q = q.Where("rule_id = ?", id)
			}
		}
		if res := c.Query("result"); res != "" {
			q = q.Where("result = ?", res)
		}
		if err := q.Order("id DESC").Limit(500).Find(&items).Error; err != nil {
			Error(c, 500, "查询策略事件失败")
			return
		}
		Success(c, items)
	}
}

// validateAutomationRuleFields 候选值校验 (fail-closed, 与 alert 同款风格)。
func validateAutomationRuleFields(name, triggerType, actionType string,
	triggerEdgeDeviceID *uint, sensorName, comparator string, threshold *float64,
	windowStart, windowEnd, windowEdge string,
	actionDeviceID *uint, actionID, actionLevel string) string {
	if name == "" {
		return "name 不能为空"
	}
	if !validAutomationTriggerTypes[triggerType] {
		return "trigger_type 非法: " + triggerType
	}
	if !validAutomationActionTypes[actionType] {
		return "action_type 非法: " + actionType
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
	// event 触发器仅占位 (明确不做), 无额外约束。
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
	return ""
}

// pickStr 合并候选值辅助 (alert 同款风格)。
func pickStr(p *string, cur string) string {
	if p != nil {
		return *p
	}
	return cur
}
