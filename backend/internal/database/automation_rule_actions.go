package database

import (
	"ehome/backend/internal/models"

	"gorm.io/gorm"
)

// LoadRuleActions 读取规则的子表动作 (按 seq 升序)。
//
// 返回**原始**子行, 不在这里回落镜像列 —— 回落的取舍属于调用方语义:
// planner 用"空集合 ⇒ 合成 seq=1"保证历史库可执行; 写路径用"空集合 ⇒
// 与镜像列比较"判断授权是否失效。两处口径不同, 不在数据层替它们决定。
func LoadRuleActions(db *gorm.DB, ruleID uint) ([]models.AutomationRuleAction, error) {
	if db == nil {
		return nil, gorm.ErrInvalidDB
	}
	var actions []models.AutomationRuleAction
	if err := db.Where("rule_id = ?", ruleID).Order("seq ASC").Find(&actions).Error; err != nil {
		return nil, err
	}
	return actions, nil
}

// ReplaceRuleActions 用 incoming 替换规则的动作集合 (子表为权威), 同时把
// seq 最小的动作写回 automation_rules 的镜像列 (双写兼容期, 方案 §3.4 D2)。
//
// 事务边界 (调用契约): **调用方必须把本函数放在自己的事务里** —— 子表替换与
// 镜像列更新必须与"规则行其余字段的更新"同事务, 否则会留下"规则字段已改、动作
// 还没换"或"子表已换、镜像还是旧动作"的窗口。本函数**不**自己开事务, 就是为了
// 让调用方能把它与其它写操作合成一个原子边界 (create/update 两个写路径都如此)。
//
// 镜像列语义: 恒等于 seq 最小的动作。老客户端/老读路径只看这四列时, 行为与
// 改造前一致 (改造前就只有一个动作)。
func ReplaceRuleActions(tx *gorm.DB, ruleID uint,
	incoming []models.AutomationRuleAction) error {
	if tx == nil {
		return gorm.ErrInvalidDB
	}
	if err := tx.Where("rule_id = ?", ruleID).Delete(&models.AutomationRuleAction{}).Error; err != nil {
		return err
	}
	for i := range incoming {
		incoming[i].ID = 0
		incoming[i].RuleID = ruleID
		if err := tx.Create(&incoming[i]).Error; err != nil {
			return err
		}
	}
	if len(incoming) == 0 {
		return nil
	}
	first := incoming[0]
	for _, a := range incoming[1:] {
		if a.Seq < first.Seq {
			first = a
		}
	}
	return tx.Model(&models.AutomationRule{}).Where("id = ?", ruleID).Updates(map[string]interface{}{
		"action_type":        first.ActionType,
		"action_device_id":   first.ActionDeviceID,
		"action_id":          first.ActionID,
		"action_params_json": first.ActionParamsJSON,
		"action_level":       first.ActionLevel,
	}).Error
}

// AttachRuleActions 给一批规则填充 Actions 响应字段 (方案 §3.4 D4)。
//
// 一次查询取回全部子行 (避免 N+1), 按 rule_id 分组后逐条挂上。子表为空的规则
// 回落镜像列合成 seq=1 —— 回填尚未跑到的历史库在 API 上仍是"有一个动作"。
func AttachRuleActions(db *gorm.DB, rules []models.AutomationRule) error {
	if db == nil || len(rules) == 0 {
		return nil
	}
	ids := make([]uint, 0, len(rules))
	for _, r := range rules {
		ids = append(ids, r.ID)
	}
	var rows []models.AutomationRuleAction
	if err := db.Where("rule_id IN ?", ids).Order("rule_id ASC, seq ASC").Find(&rows).Error; err != nil {
		return err
	}
	grouped := make(map[uint][]models.AutomationRuleAction, len(rules))
	for _, row := range rows {
		grouped[row.RuleID] = append(grouped[row.RuleID], row)
	}
	for i := range rules {
		actions := grouped[rules[i].ID]
		if len(actions) == 0 {
			actions = []models.AutomationRuleAction{models.AutomationActionFromRule(rules[i])}
		}
		rules[i].Actions = actions
	}
	return nil
}
