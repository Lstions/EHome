package dbschema

import (
	"fmt"

	"gorm.io/gorm"
)

// ── 来源组 schema（方案《规则数据来源与复合来源》§9.1 / N-11 / N-23）──
//
// 为什么需要独立的迁移函数（不能只靠 AutoMigrate）：
//  1. 两个**部分唯一索引**（`UNIQUE ... WHERE status='active'`）是「每组至多一条
//     权威成员」的 DB 级保证；GORM 的 AutoMigrate 只在首次建表时按 tag 建索引，
//     且 SQLite 不支持 ALTER TABLE ADD CONSTRAINT，因此必须用
//     CREATE UNIQUE INDEX IF NOT EXISTS 显式建（两库都支持 WHERE 谓词）。
//  2. 历史库可能已经存在**重复 active** 行（旧 data_sources 的「每组至多一条
//     active」此前只是应用层假设）。冲突必须 fail-closed 让启动失败，而不是
//     静默跳过索引 —— 否则「规则读到哪条 active」变成不确定行为。
//
// 调用点（两处清单必须同步，本仓既有纪律）：
//   - internal/database/gorm.go 的 AutoMigrate 收尾（生产启动，dbschema.EnsureSourceGroupSchema）
//   - internal/testutil/db.go 的 OpenTestDB（测试库）
//     ⚠ 若测试库不调本函数，部分唯一索引在 SQLite 测试库不存在 ⇒ SG-04 假绿。
func EnsureSourceGroupSchema(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("source_group schema: database is nil")
	}

	// 1) 新表：每组至多一条 active 成员（I1）。
	if db.Migrator().HasTable("source_group_members") {
		if err := ensureSingleActiveIndex(db, "source_group_members", "group_id", "CAST(group_id AS TEXT)"); err != nil {
			return err
		}
	}

	// 2) 旧表补洞（D-3）：data_sources 的「每组至多一条 active」此前只有
	//    应用层约束，一旦 active 被规则消费，双 active 会变成「规则读到哪条」
	//    的不确定性。生产 data_sources=0 行，零风险。
	if db.Migrator().HasTable("data_sources") {
		if err := ensureSingleActiveIndex(db, "data_sources", "device_id, category", "CAST(device_id AS TEXT) || ',' || category"); err != nil {
			return err
		}
	}

	// 3) R10 冷却锚点索引：按组取最近一次切换（(group_id, id DESC)）。
	if db.Migrator().HasTable("source_group_failover_logs") {
		if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_sgfl_cooldown 
			ON source_group_failover_logs (group_id, id DESC)`).Error; err != nil {
			return fmt.Errorf("source_group schema: create cooldown index: %w", err)
		}
	}

	return nil
}

// ensureSingleActiveIndex 在「已存在重复 active」时 fail-closed，否则建部分唯一索引。
//
// 为什么先查重再建索引：CREATE UNIQUE INDEX 在 PG 上遇到重复行只会报一句
// 「could not create unique index」，运维无法定位是哪一组；这里先查出来并给出
// 具体分组键。SQLite 同理（unique constraint failed 不带分组信息）。
func ensureSingleActiveIndex(db *gorm.DB, table, groupCols, keyExpr string) error {
	type dupRow struct {
		GroupKey string
		Cnt      int64
	}
	var dups []dupRow
	check := fmt.Sprintf(
		"SELECT %s AS group_key, COUNT(*) AS cnt FROM %s WHERE status = 'active' GROUP BY %s HAVING COUNT(*) > 1 LIMIT 5",
		keyExpr, table, groupCols)
	if err := db.Raw(check).Scan(&dups).Error; err != nil {
		return fmt.Errorf("source_group schema: check duplicate active in %s: %w", table, err)
	}
	if len(dups) > 0 {
		// fail-closed：重复 active 意味着「规则读到哪条」不确定，必须人工收敛后启动。
		return fmt.Errorf(
			"source_group schema: %s 有 %d 组存在重复 active 行（如 %s）—— 请先收敛为单条 active 再启动 (migration_required)",
			table, len(dups), dups[0].GroupKey)
	}

	idx := "ux_" + shortTableName(table) + "_one_active"
	stmt := fmt.Sprintf(
		"CREATE UNIQUE INDEX IF NOT EXISTS %s ON %s (%s) WHERE status = 'active'",
		idx, table, groupCols)
	if err := db.Exec(stmt).Error; err != nil {
		return fmt.Errorf("source_group schema: create %s: %w", idx, err)
	}
	return nil
}

func shortTableName(table string) string {
	switch table {
	case "source_group_members":
		return "sgm"
	case "data_sources":
		return "ds"
	default:
		return table
	}
}
