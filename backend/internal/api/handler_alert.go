package api

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/sensorfield"
	"ehome/backend/pkg/logger"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 合法取值集 (方案 v0.4 §5.1.1/§5.1.3)
var (
	validComparators = map[string]bool{
		models.AlertComparatorGT: true, models.AlertComparatorGTE: true,
		models.AlertComparatorLT: true, models.AlertComparatorLTE: true,
		models.AlertComparatorEQ: true, models.AlertComparatorNEQ: true,
	}
	validLevels = map[string]bool{
		models.AlertLevelInfo: true, models.AlertLevelWarning: true, models.AlertLevelCritical: true,
	}
	validTargetTypes = map[string]bool{
		models.AlertTargetEdgeDevice: true, models.AlertTargetLogicalDevice: true,
	}
)

// alertEvaluator 供 CRUD 写路径失效规则缓存 (避免 api→alert 编译期依赖的最小接口)。
type alertEvaluator interface {
	Invalidate()
}

type createAlertRuleRequest struct {
	TargetType  string   `json:"target_type"`
	TargetID    *uint    `json:"target_id"`
	SensorName  string   `json:"sensor_name"`
	Comparator  string   `json:"comparator"`
	Threshold   *float64 `json:"threshold"`
	DurationSec int      `json:"duration_sec"`
	SilenceSec  *int     `json:"silence_sec"`
	Level       string   `json:"level"`
	Enabled     *bool    `json:"enabled"`
	Name        string   `json:"name"`
	// ── 数据来源（方案 §4.3 / N-6 / N-7；SG-01/SG-03）──
	SourceKind    *string `json:"source_kind"`
	SourceGroupID *uint   `json:"source_group_id"`
}

type updateAlertRuleRequest struct {
	TargetType  string   `json:"target_type"`
	TargetID    *uint    `json:"target_id"`
	SensorName  *string  `json:"sensor_name"`
	Comparator  *string  `json:"comparator"`
	Threshold   *float64 `json:"threshold"`
	DurationSec *int     `json:"duration_sec"`
	SilenceSec  *int     `json:"silence_sec"`
	Level       *string  `json:"level"`
	Enabled     *bool    `json:"enabled"`
	Name        *string  `json:"name"`
	// ── 数据来源（同创建；显式空串 source_kind = 解绑）──
	SourceKind    *string `json:"source_kind"`
	SourceGroupID *uint   `json:"source_group_id"`
}

// registerAlertRoutes 注册阈值告警规则/事件 API (方案 v0.4 §5.1.3 任务C)。
// 权限对齐现状 (v0.3 修正): 单主体模式无 role 强制中间件, 写操作登录即可。
func registerAlertRoutes(v1 *gin.RouterGroup, db *gorm.DB, evaluator alertEvaluator) {
	rules := v1.Group("/alert-rules")
	{
		rules.GET("", listAlertRules(db))
		rules.POST("", createAlertRule(db, evaluator))
		rules.PUT("/:id", updateAlertRule(db, evaluator))
		rules.DELETE("/:id", deleteAlertRule(db, evaluator))
		rules.PATCH("/:id/enabled", patchAlertRuleEnabled(db, evaluator))
	}

	events := v1.Group("/alert-events")
	{
		events.GET("", listAlertEvents(db))
		events.POST("/read", readAlertEvents(db))
	}
}

// GET /api/v1/alert-rules
func listAlertRules(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var items []models.AlertRule
		q := db.Model(&models.AlertRule{})
		if tt := c.Query("target_type"); tt != "" {
			q = q.Where("target_type = ?", tt)
		}
		if tid := c.Query("target_id"); tid != "" {
			if id, err := strconv.ParseUint(tid, 10, 64); err == nil {
				q = q.Where("target_id = ?", id)
			}
		}
		if lv := c.Query("level"); lv != "" {
			q = q.Where("level = ?", lv)
		}
		if err := q.Order("id DESC").Find(&items).Error; err != nil {
			Error(c, 500, "查询告警规则失败")
			return
		}
		Success(c, items)
	}
}

// POST /api/v1/alert-rules
func createAlertRule(db *gorm.DB, evaluator alertEvaluator) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req createAlertRuleRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, 400, "参数错误: "+err.Error())
			return
		}
		// ── 来源绑定（SG-01/SG-03）：source_group ⇒ 校验组 + 锚点双写到 target_type/target_id（N-7）──
		sourceBinding, bindErr := resolveRuleSourceBinding(db, req.SourceKind, req.SourceGroupID, 0, false)
		if respondSourceRefError(c, bindErr) {
			return
		}
		if sourceBinding.IsGroup {
			req.TargetType = models.AlertTargetEdgeDevice
			req.TargetID = ptrUint(sourceBinding.AnchorEdgeDeviceID)
		}
		if msg := validateAlertRuleFields(req.TargetType, req.TargetID, req.SensorName, req.Comparator, req.Level); msg != "" {
			ErrorWithCode(c, 400, "invalid_alert_rule", msg)
			return
		}
		if code, msg := validateAlertSensorSelector(req.TargetType, *req.TargetID, req.SensorName); msg != "" {
			ErrorWithCode(c, 400, code, msg)
			return
		}
		if req.Threshold == nil {
			ErrorWithCode(c, 400, "invalid_alert_rule", "threshold 不能为空")
			return
		}
		rule := models.AlertRule{
			TargetType:  req.TargetType,
			TargetID:    *req.TargetID,
			SensorName:  req.SensorName,
			Comparator:  req.Comparator,
			Threshold:   *req.Threshold,
			DurationSec: req.DurationSec,
			SilenceSec:  defaultAlertSilenceSec(req.SilenceSec),
			Level:       req.Level,
			Enabled:     req.Enabled == nil || *req.Enabled,
			Name:        req.Name,
		}
		if sourceBinding.IsGroup {
			rule.SourceKind = ptrString(string(models.SourceKindSourceGroup))
			rule.SourceGroupID = ptrUint(sourceBinding.GroupID)
			rule.TargetType = models.AlertTargetEdgeDevice
			rule.TargetID = sourceBinding.AnchorEdgeDeviceID
		} else if req.SourceKind != nil && *req.SourceKind == string(models.SourceKindEdgeDevice) {
			rule.SourceKind = req.SourceKind
		}
		if err := db.Create(&rule).Error; err != nil {
			logger.Warn("api: create alert rule failed", "error", err)
			Error(c, 500, "创建告警规则失败")
			return
		}
		if evaluator != nil {
			evaluator.Invalidate()
		}
		SuccessWithCode(c, 201, rule)
	}
}

// PUT /api/v1/alert-rules/:id
func updateAlertRule(db *gorm.DB, evaluator alertEvaluator) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := strconv.ParseUint(c.Param("id"), 10, 64)
		if err != nil {
			Error(c, 400, "无效规则 ID")
			return
		}
		var rule models.AlertRule
		if err := db.First(&rule, id).Error; err != nil {
			Error(c, 404, "告警规则不存在")
			return
		}
		var req updateAlertRuleRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, 400, "参数错误: "+err.Error())
			return
		}
		// 校验合并后的最终取值
		targetType := rule.TargetType
		if req.TargetType != "" {
			targetType = req.TargetType
		}
		targetID := rule.TargetID
		if req.TargetID != nil {
			targetID = *req.TargetID
		}
		sensorName := rule.SensorName
		if req.SensorName != nil {
			sensorName = *req.SensorName
		}
		comparator := rule.Comparator
		if req.Comparator != nil {
			comparator = *req.Comparator
		}
		level := rule.Level
		if req.Level != nil {
			level = *req.Level
		}
		// ── 来源绑定合并 + 校验（SG-01/SG-03）──
		effKind := ""
		if req.SourceKind != nil {
			effKind = *req.SourceKind
		} else if rule.SourceKind != nil {
			effKind = *rule.SourceKind
		}
		var effKindPtr *string
		if effKind != "" {
			effKindPtr = &effKind
		}
		var effGroupPtr *uint
		if effKind == string(models.SourceKindSourceGroup) {
			if req.SourceGroupID != nil {
				effGroupPtr = req.SourceGroupID
			} else if rule.SourceGroupID != nil {
				effGroupPtr = rule.SourceGroupID
			}
		}
		sourceTouched := req.SourceKind != nil || req.SourceGroupID != nil
		sourceBinding, bindErr := resolveRuleSourceBinding(db, effKindPtr, effGroupPtr, 0, false)
		if respondSourceRefError(c, bindErr) {
			return
		}
		if sourceBinding.IsGroup {
			targetType = models.AlertTargetEdgeDevice
			targetID = sourceBinding.AnchorEdgeDeviceID
		}
		if msg := validateAlertRuleFields(targetType, &targetID, sensorName, comparator, level); msg != "" {
			ErrorWithCode(c, 400, "invalid_alert_rule", msg)
			return
		}
		if code, msg := validateAlertSensorSelector(targetType, targetID, sensorName); msg != "" {
			ErrorWithCode(c, 400, code, msg)
			return
		}
		updates := map[string]interface{}{}
		if req.TargetType != "" {
			updates["target_type"] = req.TargetType
		}
		if req.TargetID != nil {
			updates["target_id"] = *req.TargetID
		}
		if req.SensorName != nil {
			updates["sensor_name"] = *req.SensorName
		}
		if req.Comparator != nil {
			updates["comparator"] = *req.Comparator
		}
		if req.Threshold != nil {
			updates["threshold"] = *req.Threshold
		}
		if req.DurationSec != nil {
			updates["duration_sec"] = *req.DurationSec
		}
		if req.SilenceSec != nil {
			updates["silence_sec"] = *req.SilenceSec
		}
		if req.Level != nil {
			updates["level"] = *req.Level
		}
		if req.Enabled != nil {
			updates["enabled"] = *req.Enabled
		}
		if req.Name != nil {
			updates["name"] = *req.Name
		}
		// ── 来源绑定写回（N-7 / SG-01）──
		switch {
		case sourceBinding.IsGroup:
			updates["source_kind"] = string(models.SourceKindSourceGroup)
			updates["source_group_id"] = sourceBinding.GroupID
			updates["target_type"] = models.AlertTargetEdgeDevice
			updates["target_id"] = sourceBinding.AnchorEdgeDeviceID
		case sourceTouched:
			updates["source_group_id"] = nil
			if req.SourceKind != nil && *req.SourceKind == string(models.SourceKindEdgeDevice) {
				updates["source_kind"] = string(models.SourceKindEdgeDevice)
			} else {
				updates["source_kind"] = nil
			}
		}
		if len(updates) > 0 {
			if err := db.Model(&models.AlertRule{}).Where("id = ?", id).Updates(updates).Error; err != nil {
				logger.Warn("api: update alert rule failed", "id", id, "error", err)
				Error(c, 500, "更新告警规则失败")
				return
			}
		}
		var out models.AlertRule
		db.First(&out, id)
		if evaluator != nil {
			evaluator.Invalidate()
		}
		Success(c, out)
	}
}

// DELETE /api/v1/alert-rules/:id
func deleteAlertRule(db *gorm.DB, evaluator alertEvaluator) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := strconv.ParseUint(c.Param("id"), 10, 64)
		if err != nil {
			Error(c, 400, "无效规则 ID")
			return
		}
		result := db.Delete(&models.AlertRule{}, id)
		if result.Error != nil {
			Error(c, 500, "删除告警规则失败")
			return
		}
		if result.RowsAffected == 0 {
			Error(c, 404, "告警规则不存在")
			return
		}
		if evaluator != nil {
			evaluator.Invalidate()
		}
		Success(c, gin.H{"deleted": id})
	}
}

// PATCH /api/v1/alert-rules/:id/enabled  body: {"enabled": bool}
func patchAlertRuleEnabled(db *gorm.DB, evaluator alertEvaluator) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := strconv.ParseUint(c.Param("id"), 10, 64)
		if err != nil {
			Error(c, 400, "无效规则 ID")
			return
		}
		var req struct {
			Enabled *bool `json:"enabled"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || req.Enabled == nil {
			Error(c, 400, "参数错误: enabled 必填")
			return
		}
		result := db.Model(&models.AlertRule{}).Where("id = ?", id).Update("enabled", *req.Enabled)
		if result.Error != nil {
			Error(c, 500, "更新告警规则失败")
			return
		}
		if result.RowsAffected == 0 {
			Error(c, 404, "告警规则不存在")
			return
		}
		if evaluator != nil {
			evaluator.Invalidate()
		}
		var out models.AlertRule
		db.First(&out, id)
		Success(c, out)
	}
}

// GET /api/v1/alert-events  过滤: rule_id/state/start_time/end_time (RFC3339)
//
// 分页契约 (架构与接口评估 P1.2 裁决: items + total): 查询参数 page (默认 1, <1 归 1) /
// page_size (默认 20, 超出 [1,200] 归 20), 响应 data = {items,total,page,page_size}。
// 参数语义与 /automation-events、/vendors、/device-configs、/nodes、/edge-devices
// 完全一致; 结构用 items 而非 list —— /device-configs 的 {list,...} 是待收敛的旧方言,
// 不在此扩散。rule_id / state / start_time / end_time 筛选原样保留。
//
// 历史 (为什么必须改): 本端点曾 `Order("id DESC").Limit(500)` 后直接
// `Success(c, items)` 返回**裸数组** —— 与 /automation-events 同源的静默截断:
// 事件超过 500 条时用户只看到最近 500 条且**没有任何截断提示**, total 也无从得知;
// 同时前端 AlertRules.vue 的「告警事件」表绑的是本地全量数组, el-pagination 数量为 0,
// 即使用户想翻页也没有入口。改为真分页后 total 如实反映过滤后的全量条数,
// 前端据此渲染分页器 (与 AutomationRules.vue 同范式)。
func listAlertEvents(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
		if page < 1 {
			page = 1
		}
		if pageSize < 1 || pageSize > 200 {
			pageSize = 20
		}
		q := db.Model(&models.AlertEvent{})
		if rid := c.Query("rule_id"); rid != "" {
			if id, err := strconv.ParseUint(rid, 10, 64); err == nil {
				q = q.Where("rule_id = ?", id)
			}
		}
		if st := c.Query("state"); st != "" {
			q = q.Where("state = ?", st)
		}
		if v := c.Query("start_time"); v != "" {
			if ts, err := time.Parse(time.RFC3339, v); err == nil {
				q = q.Where("created_at >= ?", ts)
			} else {
				Error(c, 400, "start_time 需为 RFC3339 格式")
				return
			}
		}
		if v := c.Query("end_time"); v != "" {
			if ts, err := time.Parse(time.RFC3339, v); err == nil {
				q = q.Where("created_at <= ?", ts)
			} else {
				Error(c, 400, "end_time 需为 RFC3339 格式")
				return
			}
		}
		var total int64
		if err := q.Count(&total).Error; err != nil {
			Error(c, 500, "查询告警事件失败")
			return
		}
		// 非 nil 空切片: 空集序列化为 [] 而非 null (与 handler_data_source.go 同约定)。
		items := make([]models.AlertEvent, 0)
		if err := q.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error; err != nil {
			Error(c, 500, "查询告警事件失败")
			return
		}
		Success(c, gin.H{"items": items, "total": total, "page": page, "page_size": pageSize})
	}
}

// POST /api/v1/alert-events/read  body: {"ids": [eventID]} 或 {"all": true}
// AlertEvent 无 read 列 — 已读态落在回链的 Notification 行上
// (source='alert_rule' AND source_id=ruleID), 与通知中心语义一致。
func readAlertEvents(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			IDs []uint `json:"ids"`
			All bool   `json:"all"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, 400, "参数错误: "+err.Error())
			return
		}
		q := db.Model(&models.Notification{}).Where("source = ?", "alert_rule")
		if req.All {
			// 全部告警通知标记已读
		} else if len(req.IDs) > 0 {
			var ruleIDs []string
			if err := db.Model(&models.AlertEvent{}).Where("id IN ?", req.IDs).
				Distinct().Pluck("rule_id", &ruleIDs).Error; err != nil || len(ruleIDs) == 0 {
				Success(c, gin.H{"updated": false})
				return
			}
			q = q.Where("source_id IN ?", ruleIDs)
		} else {
			Error(c, 400, "ids 或 all 必填其一")
			return
		}
		if err := q.Update("read", true).Error; err != nil {
			Error(c, 500, "标记已读失败")
			return
		}
		Success(c, gin.H{"updated": true})
	}
}

func validateAlertRuleFields(targetType string, targetID *uint, sensorName, comparator, level string) string {
	if !validTargetTypes[targetType] {
		return "target_type 需为 edge_device 或 logical_device"
	}
	if targetID == nil || *targetID == 0 {
		return "target_id 不能为空"
	}
	if sensorName == "" {
		return "sensor_name 不能为空"
	}
	if !validComparators[comparator] {
		return "comparator 需为 gt|gte|lt|lte|eq|neq"
	}
	if !validLevels[level] {
		return "level 需为 info|warning|critical"
	}
	return ""
}

func defaultAlertSilenceSec(v *int) int {
	if v == nil || *v <= 0 {
		return 300
	}
	return *v
}

// validateAlertSensorSelector 保存期校验 sensor_name 的 selector 合法性（设计 §9.2#7）。
//
// 规则（设计 §8.5）：保存端点只返回**硬错误**——语法错误/未知语义/未知派生/开关关闭
// 下的特殊 selector ⇒ 400 + error_code；语法合法但当前解析不到（unbound）**不阻塞**
// （符号可能尚未上报，属于软告警，precheck 的 warnings 才负责提示）。
//
// target_type=logical_device 时 TargetID 不是 EdgeDevice.ID：无法按设备解析
// （unbound 判定需要设备已知集合），因此只用 deviceID=0 做**语法/语义**校验。
func validateAlertSensorSelector(targetType string, targetID uint, sensorName string) (string, string) {
	resolver := currentSelectorResolver()
	if resolver == nil || strings.TrimSpace(sensorName) == "" {
		return "", "" // 未接线：保持旧行为（不校验）
	}
	deviceID := uint(0)
	if targetType == models.AlertTargetEdgeDevice {
		deviceID = targetID
	}
	if _, err := resolver.Resolve(deviceID, sensorName); err != nil {
		code := sensorfield.ErrorCode(err)
		if code == "" {
			code = "invalid_alert_rule"
		}
		return code, fmt.Sprintf("sensor_name 字段选择器非法 (%s): %v", code, err)
	}
	return "", ""
}
