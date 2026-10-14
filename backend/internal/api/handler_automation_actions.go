package api

import (
	"context"

	"ehome/backend/internal/database"
	"ehome/backend/internal/models"

	"gorm.io/gorm"
)

// automationActionInput 是多动作请求体里的单个动作 (方案 §4.1.1)。
//
// Seq 是 int 而不是 *int: "缺省"与"显式 0"都必须被拒绝 (seq 从 1 起), 用零值
// 表达缺省即可, 不需要区分二者。
type automationActionInput struct {
	Seq              int    `json:"seq"`
	ActionType       string `json:"action_type"`
	ActionDeviceID   *uint  `json:"action_device_id"`
	ActionID         string `json:"action_id"`
	ActionParamsJSON string `json:"action_params_json"`
	ActionLevel      string `json:"action_level"`
	Enabled          *bool  `json:"enabled"`
}

// resolveRuleActions 把请求体解析成"最终要落库的动作集合"。
//
// 兼容策略 (方案 §3.4 D3):
//
//	① 请求带 actions → 以数组为准 (完整替换);
//	② 请求不带 actions → 把旧单动作标量字段合并到"当前第一个动作"上,
//	   保持改造前的单动作编辑语义 (老前端只发 action_id 时行为不变);
//	③ 两者都没有 → 返回当前动作集合 (无动作维度变化)。
//
// current 是库中现有动作集合 (已按 seq 升序)。返回值不做 DB 写入, 只做组装 ——
// 校验与比较都在调用方, 保持本函数可单测。
func resolveRuleActions(reqActions []automationActionInput, current []models.AutomationRuleAction,
	scalarActionType string, scalarDeviceID uint, scalarActionID, scalarParamsJSON, scalarLevel string,
	scalarProvided bool) []models.AutomationRuleAction {
	if reqActions != nil {
		out := make([]models.AutomationRuleAction, 0, len(reqActions))
		for _, a := range reqActions {
			act := models.AutomationRuleAction{
				Seq:              a.Seq,
				ActionType:       a.ActionType,
				ActionID:         a.ActionID,
				ActionParamsJSON: a.ActionParamsJSON,
				ActionLevel:      a.ActionLevel,
				Enabled:          true,
			}
			if a.ActionDeviceID != nil {
				act.ActionDeviceID = *a.ActionDeviceID
			}
			if a.Enabled != nil {
				act.Enabled = *a.Enabled
			}
			out = append(out, act)
		}
		return out
	}
	if !scalarProvided {
		return current
	}
	// 标量合并: 目标 = 现有第一个动作 (无则新建 seq=1)。
	var target models.AutomationRuleAction
	if len(current) > 0 {
		target = current[0]
	} else {
		target = models.AutomationRuleAction{Seq: 1, Enabled: true}
	}
	target.ActionType = scalarActionType
	target.ActionDeviceID = scalarDeviceID
	target.ActionID = scalarActionID
	target.ActionParamsJSON = scalarParamsJSON
	target.ActionLevel = scalarLevel
	if len(current) > 0 {
		out := append([]models.AutomationRuleAction(nil), current...)
		out[0] = target
		return out
	}
	return []models.AutomationRuleAction{target}
}

// validateActionSequence 实现校验 V1 (方案 §4.1.1): seq 必须为 1..N 连续无重复。
//
// 为什么是 fail-closed: seq 是"顺序"语义的物理载体, 重复或跳号会让"第 k 个动作"
// 无法被审计精确定位, 也会让唯一索引 (rule_id, seq) 在落库时才报错 (错误码更差)。
func validateActionSequence(actions []models.AutomationRuleAction) string {
	if len(actions) == 0 {
		return "actions 不能为空"
	}
	seen := make(map[int]bool, len(actions))
	for _, a := range actions {
		if a.Seq < 1 || a.Seq > len(actions) {
			return "actions[].seq 必须在 1..N 范围内且连续"
		}
		if seen[a.Seq] {
			return "actions[].seq 重复"
		}
		seen[a.Seq] = true
	}
	return ""
}

// validateActionFields 校验单个动作的类型专属必填字段 (与单动作校验同口径)。
func validateActionFields(a models.AutomationRuleAction) string {
	if !validAutomationActionTypes[a.ActionType] {
		return "actions[].action_type 非法: " + a.ActionType
	}
	switch a.ActionType {
	case models.AutomationActionDeviceAction:
		if a.ActionDeviceID == 0 {
			return "actions[].action_device_id 不能为空 (device_action)"
		}
		if a.ActionID == "" {
			return "actions[].action_id 不能为空 (device_action)"
		}
	case models.AutomationActionNotification:
		if a.ActionLevel == "" {
			return "actions[].action_level 不能为空 (notification)"
		}
	}
	return ""
}

// validateActionsCatalog 对动作集合里的每个 device_action 校验 Catalog 存在性 +
// 参数规范化 (沿用 validateDeviceActionCatalog, 逐项)。
func validateActionsCatalog(ctx context.Context, catalog automationCatalogQuerier,
	actions []models.AutomationRuleAction) string {
	for _, a := range actions {
		if a.ActionType != models.AutomationActionDeviceAction {
			continue
		}
		if msg := validateDeviceActionCatalog(ctx, catalog, a.ActionDeviceID, a.ActionID, a.ActionParamsJSON); msg != "" {
			return msg
		}
	}
	return ""
}

// loadActionsOrMirror 读规则当前动作集合; 子表为空时回落镜像列合成 seq=1。
func loadActionsOrMirror(db *gorm.DB, rule models.AutomationRule) []models.AutomationRuleAction {
	actions, err := database.LoadRuleActions(db, rule.ID)
	if err != nil || len(actions) == 0 {
		return []models.AutomationRuleAction{models.AutomationActionFromRule(rule)}
	}
	return actions
}

// actionSetsEqual 逐 seq 比较两个动作集合 (顺序无关: 先按 seq 归一)。
//
// 位置说明: 本函数是 automationActionSetChanged 的唯一判据实现, 放在本文件
// (动作集合工具集) 而调用点在 handler_automation.go 的 update 路径。
func actionSetsEqual(a, b []models.AutomationRuleAction) bool {
	if len(a) != len(b) {
		return false
	}
	bySeq := make(map[int]models.AutomationRuleAction, len(a))
	for _, x := range a {
		bySeq[x.Seq] = x
	}
	for _, y := range b {
		x, ok := bySeq[y.Seq]
		if !ok {
			return false
		}
		if x.ActionType != y.ActionType ||
			x.ActionDeviceID != y.ActionDeviceID ||
			x.ActionID != y.ActionID ||
			x.ActionParamsJSON != y.ActionParamsJSON {
			return false
		}
	}
	return true
}

// derefUint 解引用可空 uint (nil → 0)。
func derefUint(p *uint) uint {
	if p == nil {
		return 0
	}
	return *p
}
