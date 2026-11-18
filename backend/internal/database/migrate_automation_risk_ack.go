package database

import (
	"fmt"

	"ehome/backend/pkg/logger"

	"gorm.io/gorm"
)

// legacyRiskAckColumn 是 2026-11-18 随"规则级风险授权"整层删除后残留的死列。
//
// 背景: 该列曾是"这条自动化规则被用户显式授权执行风险动作"的载体
// (automation_rules.risk_acknowledged_at)。2026-11-18 用户裁决删除该设计:
// 风险只有两级 (normal/elevated), 风险动作由界面弹窗确认一次; 自动执行是否
// 等人工由 require_confirmed 决定。代码已零引用, 但 **AutoMigrate 不会删列**
// (GORM 只增不减), 老库会永久残留。
//
// 为什么同版本删而不再多留一版: 用户明确要求"不写兼容代码 / 不留债"; 代价是
// 回滚窗口关闭 —— 旧镜像 (ee903d0f 之前) SELECT 该列会报错, 本次发布后回滚
// 只能前进修复。这一条已写进 deploy/prod/DEPLOY-LOG.md。
const legacyRiskAckColumn = "risk_acknowledged_at"

// MigrateAutomationRuleDropRiskAcknowledged 幂等删除 automation_rules.risk_acknowledged_at。
//
// 安全边界:
//   - 只对写死的表 automation_rules 与写死的列 risk_acknowledged_at 操作, 无动态标识符;
//   - 先判 HasTable + HasColumn, 不存在则完全不产生 DDL (新库 / 重复启动均空转);
//   - 删前只把"还有多少非空取值"记进日志, 不阻断 —— 该列语义已废除, 不是业务数据。
//
// 幂等性: 重复调用第二次 HasColumn=false ⇒ 返回 (false, nil)。
// 返回 dropped=true 表示本次真的执行了 DROP。
func MigrateAutomationRuleDropRiskAcknowledged(db *gorm.DB) (bool, error) {
	if db == nil {
		return false, fmt.Errorf("drop automation_rules.%s: database is nil", legacyRiskAckColumn)
	}
	m := db.Migrator()
	if !m.HasTable("automation_rules") {
		return false, nil
	}
	if !m.HasColumn("automation_rules", legacyRiskAckColumn) {
		return false, nil
	}
	var rows int64
	if err := db.Table("automation_rules").Where(legacyRiskAckColumn + " IS NOT NULL").Count(&rows).Error; err != nil {
		logger.Warnf("[migrate] automation_rules.%s row count failed (continuing): %v", legacyRiskAckColumn, err)
	}
	// 用原生 DDL 而不是 gorm 的 Migrator().DropColumn:
	//   · PG 与 SQLite(>=3.35) 都支持 ALTER TABLE ... DROP COLUMN, 语义一致;
	//   · gorm 的 sqlite 实现走"重建表"路径, 且传入表名字符串时 stmt.Schema 为 nil
	//     会 panic (同 migrate_ota_collector_id.go 的教训)。
	// 表名与列名都是本文件内的常量, 无动态拼接。
	stmt := fmt.Sprintf("ALTER TABLE automation_rules DROP COLUMN %s", legacyRiskAckColumn)
	if err := db.Exec(stmt).Error; err != nil {
		return false, fmt.Errorf("drop automation_rules.%s: %w", legacyRiskAckColumn, err)
	}
	logger.Infof("[migrate] dropped dead column automation_rules.%s (had %d non-null rows; rule-level risk authorization removed 2026-11-18)",
		legacyRiskAckColumn, rows)
	return true, nil
}
