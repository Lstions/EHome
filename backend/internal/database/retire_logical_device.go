package database

// retire_logical_device.go — 逻辑设备（LogicalDevice）概念层的数据层拆除。
//
// 背景与裁决：产品负责人确认「根本不需要什么逻辑设备概念」，真正需求只有一条
// —— 同一物理设备从节点 A 换到节点 B 后历史数据不丢。评估见
// docs/分析/逻辑设备-业务逻辑与需求偏差评估-2026-10-23.md，
// 实施契约见 .contract/logical-device-removal.md。
//
// 拆除范围（与代码层删除一一对应）：
//   表   logical_devices / merge_jobs / backfill_jobs            -> DROP TABLE
//   列   edge_devices.logical_device_id                          -> DROP COLUMN
//   列   unified_data.logical_device_id（父表 + 全部子分区）      -> DROP COLUMN
//   列   device_data.logical_device_id（父表 + 全部子分区）        -> DROP COLUMN
//   列   data_sources.device_id（= 逻辑设备 id）                  -> DROP COLUMN
//
// 为什么必须先 DROP 索引再 DROP COLUMN：
//   PostgreSQL 会拒绝删除仍被索引引用的列（cannot drop column ... other objects
//   depend on it）。逻辑设备层在 unified_data/device_data 上留了 6 个索引
//   （logical_ts / logical_sensor_ts / devdata_logical_created / legacy_null 等），
//   且分区表上这些索引存在于**每个子分区**里（索引名带分区后缀），必须先枚举
//   pg_inherits 递归删干净。
//
// 为什么不用 CASCADE：本仓既有纪律（见 retire_operation_logs.go）—— CASCADE 会
// 连带删除任何「别的对象对该列的依赖」，误伤面不可控，且 SQLite 不支持该语法。
// 这里改为**显式枚举 + 逐个 DROP**，每一步写日志。
//
// 幂等性：所有步骤都用 HasTable / HasColumn / IF EXISTS 判定，重跑零副作用。
// 返回值为被删除的行数（仅用于日志），表不存在时为 0。

import (
	"fmt"

	"ehome/backend/pkg/logger"

	"gorm.io/gorm"
)

// retiredLogicalTables 是逻辑设备概念层独占的表（顺序：先子引用后主表）。
var retiredLogicalTables = []string{"merge_jobs", "backfill_jobs", "logical_devices"}

// logicalColumnSpec 描述一张需要摘除逻辑身份列的表。
type logicalColumnSpec struct {
	Table  string
	Column string
}

var logicalColumnSpecs = []logicalColumnSpec{
	{Table: "edge_devices", Column: "logical_device_id"},
	{Table: "unified_data", Column: "logical_device_id"},
	{Table: "device_data", Column: "logical_device_id"},
	// data_sources.device_id 是逻辑设备 id（不是 edge_device_id）。
	{Table: "data_sources", Column: "device_id"},
}

// RetireLogicalDeviceLayer 幂等拆除逻辑设备概念层的数据结构。
func RetireLogicalDeviceLayer(db *gorm.DB) (int64, error) {
	if db == nil {
		return 0, fmt.Errorf("retire logical device layer: database is nil")
	}
	var total int64

	// 1) 列：先删索引，再删列。
	for _, spec := range logicalColumnSpecs {
		if !db.Migrator().HasTable(spec.Table) {
			continue
		}
		if !db.Migrator().HasColumn(spec.Table, spec.Column) {
			continue
		}
		if err := dropIndexesOfColumn(db, spec.Table, spec.Column); err != nil {
			return total, err
		}
		var rows int64
		// 计数仅用于留痕，失败不阻断（该列可能全 NULL / 全空）。
		_ = db.Raw(fmt.Sprintf("SELECT count(*) FROM %s WHERE %s IS NOT NULL", spec.Table, spec.Column)).Scan(&rows).Error
		if err := db.Exec(fmt.Sprintf("ALTER TABLE %s DROP COLUMN IF EXISTS %s", spec.Table, spec.Column)).Error; err != nil {
			return total, fmt.Errorf("retire logical layer: drop column %s.%s: %w", spec.Table, spec.Column, err)
		}
		total += rows
		logger.Warnf("retire logical layer: dropped column %s.%s (non-null rows=%d)", spec.Table, spec.Column, rows)
	}

	// 2) 表：先数行数留痕，再 DROP。
	for _, table := range retiredLogicalTables {
		if !db.Migrator().HasTable(table) {
			continue
		}
		var rows int64
		_ = db.Raw("SELECT count(*) FROM " + table).Scan(&rows).Error
		if err := db.Exec("DROP TABLE IF EXISTS " + table).Error; err != nil {
			return total, fmt.Errorf("retire logical layer: drop table %s: %w", table, err)
		}
		total += rows
		logger.Warnf("retire logical layer: dropped table %s (rows=%d)", table, rows)
	}
	return total, nil
}

// dropIndexesOfColumn 删除某列上的索引，**包括分区表各子分区上的衍生索引**。
//
// PostgreSQL 分区索引有两条关键规则（2026-10-23 实测踩到）：
//  1. 父表上建的索引在**每个子分区**都有一个同名衍生索引，由 pg_inherits 关联；
//  2. 子分区索引**不能单独 DROP**（会报 "cannot drop index ... because index
//     <parent> requires it"），必须先 DROP 父索引，子索引随之自动消失。
//
// 因此这里的顺序是：先删「不是任何索引的子节点」的那些（即父索引 / 普通表索引），
// 再重查一遍把剩余的（理论上不该有的）删掉。
//
// 为什么不依赖索引名约定：改名就会漏删，而漏删的后果是 DROP COLUMN 直接报
// "cannot drop column ... other objects depend on it"，迁移中断。
func dropIndexesOfColumn(db *gorm.DB, table, column string) error {
	if db.Dialector != nil && db.Dialector.Name() == "postgres" {
		// 两轮：第一轮删父索引（DROP 父索引会连带删所有子分区的衍生索引），
		// 第二轮兜底删掉任何仍然存在的残余（例如非分区表上的普通索引）。
		for round := 0; round < 2; round++ {
			type idxRow struct {
				Schema  string
				Name    string
				IsChild bool `gorm:"column:is_child"`
			}
			var indexes []idxRow
			const q = `WITH RECURSIVE rels(oid) AS (
				SELECT $1::regclass::oid
				UNION ALL
				SELECT i.inhrelid FROM pg_inherits i JOIN rels r ON i.inhparent = r.oid
			)
			SELECT DISTINCT n.nspname AS schema, c.relname AS name,
				EXISTS (SELECT 1 FROM pg_inherits pi WHERE pi.inhrelid = c.oid) AS is_child
			FROM pg_index x
			JOIN rels r ON r.oid = x.indrelid
			JOIN pg_class c ON c.oid = x.indexrelid
			JOIN pg_namespace n ON n.oid = c.relnamespace
			JOIN pg_attribute a ON a.attrelid = x.indrelid AND a.attnum = ANY (x.indkey)
			WHERE a.attname = $2
			ORDER BY is_child ASC`
			if err := db.Raw(q, table, column).Scan(&indexes).Error; err != nil {
				return fmt.Errorf("retire logical layer: list indexes of %s.%s: %w", table, column, err)
			}
			dropped := false
			for _, idx := range indexes {
				if round == 0 && idx.IsChild {
					continue // 子分区索引: 等父索引被 DROP 后自动消失
				}
				stmt := fmt.Sprintf("DROP INDEX IF EXISTS %s.%s", quoteIdent(idx.Schema), quoteIdent(idx.Name))
				if err := db.Exec(stmt).Error; err != nil {
					return fmt.Errorf("retire logical layer: drop index %s.%s: %w", idx.Schema, idx.Name, err)
				}
				dropped = true
				logger.Infof("retire logical layer: dropped index %s.%s (on %s.%s)", idx.Schema, idx.Name, table, column)
			}
			if !dropped {
				return nil
			}
		}
		return nil
	}

	type nameRow struct {
		Name string
	}
	var rows []nameRow
	if err := db.Raw("SELECT name FROM sqlite_master WHERE type = ? AND tbl_name = ? AND name LIKE ?",
		"index", table, "%"+column+"%").Scan(&rows).Error; err != nil {
		logger.Warnf("retire logical layer: skip index enumeration on %s (dialect=%s)", table, db.Dialector.Name())
		return nil
	}
	for _, r := range rows {
		if err := db.Exec("DROP INDEX IF EXISTS " + quoteIdent(r.Name)).Error; err != nil {
			return fmt.Errorf("retire logical layer: drop index %s: %w", r.Name, err)
		}
	}
	return nil
}

// quoteIdent 给标识符加双引号（名称来自系统目录，非用户输入）。
func quoteIdent(s string) string {
	out := make([]rune, 0, len(s)+2)
	out = append(out, rune(34))
	for _, r := range s {
		if r == rune(34) {
			out = append(out, rune(34))
		}
		out = append(out, r)
	}
	out = append(out, rune(34))
	return string(out)
}
