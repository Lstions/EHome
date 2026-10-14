package database

import (
	"fmt"
	"time"

	"ehome/backend/pkg/logger"

	"gorm.io/gorm"
)

// MigrateAutomationRuleActionsResult 记录一次回填迁移的结果。
type MigrateAutomationRuleActionsResult struct {
	Scanned    int // 扫到的 automation_rules 行数
	Backfilled int // 本次真的插入了 seq=1 子行的规则数
	Skipped    int // 已有子行 (或并发插入冲突) 而跳过的规则数
	Errors     int // 意外错误数
}

// MigrateAutomationRuleActions 把存量规则的"单动作镜像列"幂等回填为
// automation_rule_actions 的 seq=1 子行 (方案 §5.2 数据回填 / 实施计划 §2 B2)。
//
// 为什么需要回填: 子表上线前创建的规则只有 automation_rules 的四列动作,
// 读路径切到子表后这些规则会"没有动作" —— 规则静默失效。回填把历史数据补齐。
//
// 幂等判据 (方案 §5.2): INSERT ... WHERE NOT EXISTS (SELECT 1 FROM
// automation_rule_actions WHERE rule_id = ?) —— 仅当该 rule_id **无任何子行**
// 时才插入 seq=1。因此:
//
//   - 第二次运行: 每个 rule_id 都已有子行 ⇒ 一条都不插, 行数不变 (A-19);
//   - 用户已改过动作集合 (子表非空) ⇒ 绝不被回填覆盖 (只补空的)。
//
// 并发安全: 两个进程同时启动时可能都判定"无子行"。此时唯一索引
// (rule_id, seq) 让后者报唯一约束冲突, 本函数按"已有子行"跳过 (与
// migrate_gpio.go 的 isUniqueConstraintError 同款处置), 不使启动失败。
//
// 非破坏性: 原动作列**一个字节都不改** (方案 §3.4 D2 兼容期只读镜像),
// 回滚时镜像列仍持有最后一次写入的动作。
func MigrateAutomationRuleActions(db *gorm.DB) (*MigrateAutomationRuleActionsResult, error) {
	result := &MigrateAutomationRuleActionsResult{}
	if db == nil {
		return nil, fmt.Errorf("migrate automation_rule_actions: database is nil")
	}
	m := db.Migrator()
	if !m.HasTable("automation_rules") || !m.HasTable("automation_rule_actions") {
		return result, nil
	}

	var ruleIDs []uint
	if err := db.Table("automation_rules").Order("id ASC").Pluck("id", &ruleIDs).Error; err != nil {
		return nil, fmt.Errorf("migrate automation_rule_actions: list rules: %w", err)
	}
	result.Scanned = len(ruleIDs)
	if len(ruleIDs) == 0 {
		return result, nil
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		for _, ruleID := range ruleIDs {
			inserted, err := backfillOneRuleAction(tx, ruleID)
			if err != nil {
				result.Errors++
				return err
			}
			if inserted {
				result.Backfilled++
			} else {
				result.Skipped++
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("migrate automation_rule_actions: %w", err)
	}
	if result.Backfilled > 0 {
		logger.Infof("[migrate] automation_rule_actions backfilled: scanned=%d backfilled=%d skipped=%d",
			result.Scanned, result.Backfilled, result.Skipped)
	}
	return result, nil
}

// backfillOneRuleAction 为单条规则插入 seq=1 子行; 已有子行时返回 (false, nil)。
//
// SQL 是单条语句 (INSERT ... SELECT ... WHERE NOT EXISTS), 由 DB 自己保证
// "判定与插入"之间不存在应用层竞态窗口; 唯一索引是第二道物理保证。
// 表名/列名全部写死, 无动态标识符拼接; 唯一的变量是 rule_id 与时间戳参数。
func backfillOneRuleAction(tx *gorm.DB, ruleID uint) (bool, error) {
	stmt := `INSERT INTO automation_rule_actions
		(rule_id, seq, action_type, action_device_id, action_id, action_params_json, action_level, enabled, created_at, updated_at)
		SELECT r.id, 1, r.action_type, r.action_device_id, r.action_id, r.action_params_json, r.action_level, ?, ?, ?
		FROM automation_rules r
		WHERE r.id = ? AND NOT EXISTS (SELECT 1 FROM automation_rule_actions a WHERE a.rule_id = r.id)`

	now := time.Now().UTC()
	res := tx.Exec(stmt, true, now, now, ruleID)
	if res.Error != nil {
		// 并发启动: 另一进程刚插入了 seq=1 ⇒ 唯一索引拒绝本次插入。这是幂等
		// 语义下的预期结果 (子行已存在), 不是错误。
		if isUniqueConstraintError(res.Error) {
			return false, nil
		}
		return false, fmt.Errorf("backfill rule %d: %w", ruleID, res.Error)
	}
	return res.RowsAffected > 0, nil
}
