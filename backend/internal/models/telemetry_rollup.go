package models

import "time"

// L3 分钟级宽行预聚合表（设计/遥测存储优化方案-2026-11-06.md §5.2）。
//
// ============ 与已退役表的关系（先读这段，别复活死表）============
//
// 2026-09-15 退役的 unified_data_rollup_1m 是**窄行**（每字段一行）表，退役
// 理由是「写了没人读，且读了也不划算」：当时所有读路径都是 device_id 等值 +
// 时间窗，索引 + 分区裁剪已够快，rollup 只省几毫秒。裁决见
// docs/分析/rollup-退役裁决-2026-09-15.md。
//
// 本表**不是那张表的复活**，三条硬差异（缺一条都不该建）：
//  1. 宽行：每设备每分钟**一行** payload bytea（内含全部字段的 avg/min/max/last），
//     不是每字段一行。窄表 29 字段 × 1440 分钟 = 41,760 行/设备/天，宽表 1440 行；
//  2. 有明确消费者：长窗图表（> 解码阈值）直接读本表，短窗仍走解码/原表
//     （方案 §5.3 的读路径落点表）；
//  3. 保留期与清理器同批落地（分区 DROP 复用 partition_mgr 的月分区机制，
//     不新增无界表）—— 死表退役裁决里「无界第二张表」的代价被消掉。
//
// 死表不得复活的门禁（datalifecycle/rollup_retirement_gate_test.go）判的是
// **旧表名** `unified_data_rollup_1m` 与旧符号，不是本表；本表反而是该门禁的
// 反向断言对象（退役步骤只能 DROP 旧表，绝不能碰到本表）。
//
// ============ 为什么模型在 models 里，却不在生产 AutoMigrate 清单 ============
//
// 与 UnifiedData / DeviceData 同一范式（internal/database/gorm.go:89-96 的注记）：
// PG 生产库的分区母表由 datalifecycle.partition_mgr 用裸 DDL 建（AutoMigrate
// 无法建分区表，且会把已存在的分区母表降级改写）。因此：
//   - 生产：partition_mgr.createPartitionedParent 建表（partitionParentDDL 已登记）；
//   - 测试：SQLite 由 testutil.allModels / 各包测试自建（**模型必须可 AutoMigrate**，
//     否则 SQLite 用例无法覆盖读写路径）。
//
// ⚠ 两处清单同步纪律（本仓既有）：若把本模型加进生产 AutoMigrate，PG 上会把
// 分区母表改成普通表 —— 那会让分区 DROP 保留期机制静默失效。别加。
type TelemetryRollup1m struct {
	// DeviceID = EdgeDevice.ID（与 unified_data.device_id 同口径；
	// 禁 unified_data.edge_device_id，DS-14 未修复前该列全 NULL）。
	DeviceID uint `gorm:"column:device_id;primaryKey;not null" json:"device_id"`
	// Bucket 是分钟桶起点（UTC 对齐的整分钟）。与 device_id 组成主键：
	// PG 分区表要求分区键进主键，且「一设备一分钟一行」是宽行语义的落库契约。
	Bucket time.Time `gorm:"column:bucket;primaryKey;not null" json:"bucket"`
	// Payload 是该分钟内全部字段的统计量打包（avg/min/max/last），编码见
	// datalifecycle/telemetry_rollup.go 的 RollupCodec。取 bytea 而不是宽列：
	// 字段集合按设备型号不同（BMS 29 个 / 逆变器 7 种帧类型），宽列会把列数钉死。
	Payload []byte `gorm:"column:payload;not null" json:"payload"`
	// UpdatedAt 行更新时间（旁路聚合 UPSERT 覆盖写；GORM 自动维护）。
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName 显式声明表名（与 partitionParentDDL / 方案 §5.2 的 DDL 一致）。
func (TelemetryRollup1m) TableName() string { return "telemetry_rollup_1m" }
