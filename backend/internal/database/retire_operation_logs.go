package database

import (
	"fmt"

	"ehome/backend/pkg/logger"

	"gorm.io/gorm"
)

// operationLogsTable 是已退役的死 schema 的表名。
//
// 退役裁决: docs/分析/运行期无界增长表-保留策略设计-2026-09-13.md §2.7。
// 依据 (领队实测): 运行期写入者 0 / 读取者 0 / 数据 0 行; 唯一生产注册点是
// AutoMigrate; 已被 models.SecurityAuditEvent 取代。给一张没人写、没人读、
// 0 行的表配"清理策略"是空转, 故**退役**而非清理。
//
// 表名以常量持有: models.OperationLog 已随退役删除, 本文件是该表名在
// 生产代码中的**唯一**合法出现点 (门禁测试 operation_logs_gate_test.go 的
// 排除项), 且它只被 DROP 使用 —— 绝不参与建表/SELECT/INSERT。
const operationLogsTable = "operation_logs"

// RetireLegacyOperationLogs 幂等退役 operation_logs 表 (DDL: DROP TABLE IF EXISTS)。
//
// 范式对齐 (本仓既有做法, 未新创):
//   - 调用点: gorm.go 的 AutoMigrate() 尾部, 与 MigrateGPIOChannels /
//     RetireLegacyPWMChannels 并列 —— 生产 PG 路径的表管理集中在
//     database/gorm.go (cmd/server/main.go 与 cmd/ehomectl/main.go 都只调
//     database.AutoMigrate()); datalifecycle/ 负责的是分区表生命周期,
//     与本次单表退役无关。
//   - DDL: "DROP TABLE IF EXISTS <表>" 幂等, 同
//     datalifecycle/partition_mgr.go dropLegacyTable / dropStaleMonthPartitions;
//     先数行数并写日志, 再 DROP —— 与 dropLegacyTable 一致 (留痕用, 不阻断)。
//     **一处有意偏离**: 不带 CASCADE。partition_mgr 的 CASCADE 只在 PG 路径执行,
//     而本步骤在所有方言上跑 —— SQLite (单测/仿真) 直接拒绝 "DROP TABLE ... CASCADE"
//     语法 (实测: near "CASCADE": syntax error)。语义上 CASCADE 在此也是空转:
//     DROP TABLE 本就带走该表自己的索引/序列, CASCADE 只对**别的表/视图**的依赖生效,
//     而 operation_logs 无外键、无依赖者。少一个 CASCADE 反而少一条"误删外部对象"的路径。
//   - 存在性判断: db.Migrator().HasTable —— 同
//     ensureDeviceConfigDefaultConstraint (migrate_device_config_default.go)。
//     GORM 的 PG 实现按 table_schema = CURRENT_SCHEMA() 限定, 因此集成测试的
//     隔离 schema (test_<pid>_<nano>) 中判断正确, 不会跨 schema 误判。
//   - SQLite (单测/仿真) 同样走 HasTable, 无表即零动作。
//
// 安全边界: 仅 DROP 这一张写死的表, 无动态表名, 无 CASCADE 之外的连带删除
// (该表无外键、无子对象)。不触碰 ehome / ehome_test。
//
// 幂等性: 重复调用时 HasTable=false ⇒ 直接返回 (0, nil), 不产生任何 SQL 副作用。
// 返回值为被删除表在删除前的行数 (仅用于日志/测试断言; 表不存在时为 0)。
func RetireLegacyOperationLogs(db *gorm.DB) (int64, error) {
	if db == nil {
		return 0, fmt.Errorf("retire operation_logs: database is nil")
	}
	if !db.Migrator().HasTable(operationLogsTable) {
		return 0, nil
	}
	// 行数仅用于留痕 (同 dropLegacyTable), 查询失败不阻断 DROP。
	var rows int64
	db.Raw("SELECT count(*) FROM " + operationLogsTable).Scan(&rows)
	if err := db.Exec("DROP TABLE IF EXISTS " + operationLogsTable).Error; err != nil {
		return 0, fmt.Errorf("retire operation_logs: drop table: %w", err)
	}
	logger.Warnf("retire operation_logs: dropped retired dead schema (rows=%d); "+
		"审计事件唯一载体为 security_audit_events (裁决见 docs/分析/运行期无界增长表-保留策略设计-2026-09-13.md §2.7)", rows)
	return rows, nil
}
