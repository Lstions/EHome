package database

import (
	"fmt"

	"ehome/backend/pkg/logger"

	"gorm.io/gorm"
)

// legacyOTACollectorIDColumn 是 v2.3 把 OTATask.CollectorID 改名为 NodeID 之后
// 残留在老库里的死列。
//
// 背景（2026-09-17 生产实测）: 在 v2.3 之前初始化的库中，ota_tasks.collector_id
// 是 NOT NULL；改名后模型只写 node_id，代码里已无任何读写 collector_id 的地方，
// 但 **PostgreSQL 不会因为 AutoMigrate 就把这列去掉** —— 于是 INSERT 报
//
//	null value in column "collector_id" of relation "ota_tasks"
//	violates not-null constraint (SQLSTATE 23502)
//
// 表现为「OTA 升级」点下去必 500，OTA 功能整体不可用。
//
// 为什么单测没拦住: 测试默认跑 **SQLite 内存库**（testutil.OpenTestDB），
// SQLite 由 AutoMigrate 依模型现建表，压根不存在这列；只有老 PG 库才有。
// 即"模型与代码都自洽，唯一的问题是旧库 schema 漂移"。
//
// 处置: 幂等 DROP COLUMN。只在列存在时执行，SQLite（无此列）与已修好的库都是空转。
const legacyOTACollectorIDColumn = "collector_id"

// MigrateOTATaskDropLegacyCollectorID 幂等删除 ota_tasks.collector_id 死列。
//
// 安全边界:
//   - 只对写死的表 ota_tasks 与写死的列 collector_id 操作，无动态标识符；
//   - 先判 HasTable + HasColumn，不存在则完全不产生 DDL；
//   - 该列在代码中零引用（已由 v2.3 改名替代为 node_id），删除不丢业务数据。
//
// 幂等性: 重复调用第二次 HasColumn=false ⇒ 返回 (false, nil)。
// 返回 dropped=true 表示本次真的执行了 DROP。
func MigrateOTATaskDropLegacyCollectorID(db *gorm.DB) (bool, error) {
	if db == nil {
		return false, fmt.Errorf("drop ota_tasks.collector_id: database is nil")
	}
	m := db.Migrator()
	if !m.HasTable("ota_tasks") {
		return false, nil
	}
	if !m.HasColumn("ota_tasks", legacyOTACollectorIDColumn) {
		return false, nil
	}
	// 留痕: 记录该列是否还有非空取值（仅日志，不阻断）。
	var rows int64
	if err := db.Table("ota_tasks").Where(legacyOTACollectorIDColumn + " IS NOT NULL").Count(&rows).Error; err != nil {
		logger.Warnf("[migrate] ota_tasks.%s row count failed (continuing): %v", legacyOTACollectorIDColumn, err)
	}
	// 用原生 DDL 而不是 gorm 的 Migrator().DropColumn：
	//   · PG 与 SQLite(>=3.35) 都支持 ALTER TABLE ... DROP COLUMN，语义一致；
	//   · gorm 的 sqlite 实现走"重建表"路径，且传入表名字符串时
	//     stmt.Schema 为 nil 会 panic（sqlite migrator.DropColumn → LookUpField）。
	// 表名与列名都是本文件内的常量，无动态拼接。
	stmt := fmt.Sprintf("ALTER TABLE ota_tasks DROP COLUMN %s", legacyOTACollectorIDColumn)
	if err := db.Exec(stmt).Error; err != nil {
		return false, fmt.Errorf("drop ota_tasks.%s: %w", legacyOTACollectorIDColumn, err)
	}
	logger.Infof("[migrate] dropped legacy column ota_tasks.%s (had %d non-null rows; superseded by node_id in v2.3)",
		legacyOTACollectorIDColumn, rows)
	return true, nil
}
