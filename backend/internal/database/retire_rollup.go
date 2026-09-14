package database

import (
	"fmt"

	"ehome/backend/pkg/logger"

	"gorm.io/gorm"
)

// rollupTable 是已退役的分钟级聚合表的表名。
//
// 退役裁决 (2026-09-15): docs/分析/rollup-退役裁决-2026-09-15.md。
// 依据 (EXPLAIN 实测, 本轮复核): 30 天窗口的 unified_data 查询
//   - 带 LIMIT 的 /devices/:id/history      : Index Scan Backward, 0.79 ms / 104 buffers;
//   - 无 LIMIT 的 /unified-data/historical-batch (全窗口载入后再降采样):
//     3.64 ms / 1191 buffers —— 即"rollup 理论上最该救的那条路径"也只要几毫秒。
//
// 各分区上的 (logical_device_id, "timestamp" DESC) 与 ("timestamp") 索引本就存在
// (unified_data_<月份>_logical_device_id_timestamp_idx / _timestamp_idx),
// 长跨度查询本来就索引 + 分区裁剪; rollup 的收益是"省几毫秒", 代价却是:
//  1. 无界增长的第二张表 (需另配保留期 + 清理器 + 测试);
//  2. 必须改数据模型才能接线 (rollup 表无 logical_device_id 列, 而本仓查询恒带
//     logical scope ⇒ 上一轮已论证接线在生产不可达, 除非加列 + 改冲突键 + 重聚合回填);
//  3. 冻结件本身就有成本 (每次启动的建表 DDL + consumer + 3 个测试的维护面)。
//
// ⇒ **退役**。与 operation_logs (死 schema) 同案处置, 但理由不同:
// operation_logs 是"没人写"; 本表是"写了没人读, 且读了也不划算"。
//
// 表名以常量持有: models.UnifiedDataRollup1m 已随退役删除, 本文件是该表名在
// 生产代码中的**唯一**合法出现点 (门禁测试
// datalifecycle/rollup_retirement_gate_test.go 的排除项), 且它只被 DROP 使用 ——
// 绝不参与建表/SELECT/INSERT。
const rollupTable = "unified_data_rollup_1m"

// RetireLegacyRollup1m 幂等退役 unified_data_rollup_1m 表 (DDL: DROP TABLE IF EXISTS)。
//
// 范式对齐 (本仓既有做法, 未新创):
//   - 调用点: gorm.go 的 AutoMigrate() 尾部, 与 MigrateGPIOChannels /
//     RetireLegacyPWMChannels / RetireLegacyOperationLogs 并列 —— 生产 PG 路径的
//     表管理集中在 database/gorm.go (cmd/server/main.go 与 cmd/ehomectl/main.go 都只调
//     database.AutoMigrate())。
//   - DDL: "DROP TABLE IF EXISTS <表>" 幂等, 同上; 先数行数并写日志再 DROP
//     (留痕用, 不阻断)。**不带 CASCADE**: 本步骤在所有方言上跑, SQLite
//     (单测/仿真) 直接拒绝 "DROP TABLE ... CASCADE" 语法; 且该表无外键、无依赖者,
//     CASCADE 在此是空转 (少一个 CASCADE 反而少一条"误删外部对象"的路径)。
//   - 存在性判断: db.Migrator().HasTable —— GORM 的 PG 实现按
//     table_schema = CURRENT_SCHEMA() 限定, 集成测试的隔离 schema
//     (test_<pid>_<nano>) 中判断正确, 不会跨 schema 误判; SQLite 同样走 HasTable。
//
// 安全边界: 仅 DROP 这一张写死的表, 无动态表名, 无连带删除。不触碰 ehome / ehome_test。
//
// 幂等性: 重复调用时 HasTable=false ⇒ 直接返回 (0, nil), 不产生任何 SQL 副作用。
// 返回值为被删除表在删除前的行数 (仅用于日志/测试断言; 表不存在时为 0)。
func RetireLegacyRollup1m(db *gorm.DB) (int64, error) {
	if db == nil {
		return 0, fmt.Errorf("retire unified_data_rollup_1m: database is nil")
	}
	if !db.Migrator().HasTable(rollupTable) {
		return 0, nil
	}
	// 行数仅用于留痕, 查询失败不阻断 DROP。
	var rows int64
	db.Raw("SELECT count(*) FROM " + rollupTable).Scan(&rows)
	if err := db.Exec("DROP TABLE IF EXISTS " + rollupTable).Error; err != nil {
		return 0, fmt.Errorf("retire unified_data_rollup_1m: drop table: %w", err)
	}
	logger.Warnf("retire unified_data_rollup_1m: dropped retired rollup table (rows=%d); "+
		"rollup 已退役 (EXPLAIN 实测 30 天窗口查询仅需毫秒级, 且该表无 logical_device_id 列 ⇒ "+
		"接线不可达); 裁决见 docs/分析/rollup-退役裁决-2026-09-15.md", rows)
	return rows, nil
}
