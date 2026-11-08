package models

import "time"

// L2 帧级增量编码的两张表（设计/遥测存储优化方案-2026-11-06.md §4.1/§4.4）。
//
// ============ 为什么是"一帧一行"而不是"一字段一行" ============
//
// 现状 unified_data 每帧解析出 29 个字段，落 29 行 × 178.7 B（含 5 条索引）
// ≈ 5.2 KB；而一帧的**真实信息量**只有 12–17 B（§1.3）。放大 300–400 倍
// 全部来自"把一帧拆成 29 行 + 每行重复索引键"。
//
// telemetry_frame 把同一帧压成**一行**：payload 里只有改变的字段（量化位打包），
// 变化抑制由 changed_mask 直接表达，不需要 L1 的"值不变不入库"规则配合
// （两者叠加，不是二选一 —— §2 的总览图）。
//
// ============ 与 UnifiedData 同一建模范式 ============
//
// 与 UnifiedData / DeviceData / TelemetryRollup1m 一样：
//
//   - **生产**：PG 分区母表由 datalifecycle.partition_mgr 用裸 DDL 建
//     （AutoMigrate 无法建分区表，且会把已存在的分区母表降级改写成普通表 ⇒
//     分区 DROP 保留期机制静默失效）。DDL 登记在 partitionParentDDL；
//   - **测试**：SQLite 由 internal/telemetry 自己的测试建表（AutoMigrate 本模型）。
//
// ⚠ 因此**不要**把这两个模型加进 internal/database.AutoMigrate 清单。
// 这条纪律与 UnifiedData 完全相同（见 gorm.go:89-96 的注记）。

// TelemetryFrame 是一帧的压缩存储行（§4.1）。
//
// 主键 (device_id, ts, seq)：
//   - device_id：= EdgeDevice.ID（与 unified_data.device_id 同口径；
//     禁 unified_data.edge_device_id —— DS-14 未修复前该列全 NULL）；
//   - ts：帧时间戳（微秒精度，与 unified_data.timestamp 同源）；
//   - seq：**同一设备同一时间戳**的序号。为什么必须有：device15 实测 1.85 帧/秒，
//     而写路径的时间戳来自 time.Now()（纳秒精度），理论上同一微秒撞多帧是可能的；
//     没有 seq，主键冲突会让后一帧**覆盖**前一帧 ⇒ 静默丢数据。
//     实测生产数据同 ts 内 0 冲突，但那是运气不是保证（§8 风险表"秒内多帧"）。
type TelemetryFrame struct {
	// DeviceID 边缘设备 ID（= UnifiedData.DeviceID）。
	DeviceID uint `gorm:"column:device_id;primaryKey;not null" json:"device_id"`
	// TS 帧时间戳（分区键）。
	TS time.Time `gorm:"column:ts;primaryKey;not null" json:"ts"`
	// Seq 同 (device_id, ts) 内的序号，0 起。
	Seq int16 `gorm:"column:seq;primaryKey;not null;default:0" json:"seq"`
	// FrameKind 0 = 增量帧，1 = 关键帧（该 slot 全部字段带值，可独立重建状态）。
	FrameKind int16 `gorm:"column:frame_kind;not null" json:"frame_kind"`
	// SlotID 帧类型编号（指向 TelemetrySlot，定义字段集合与量化规格快照）。
	SlotID int16 `gorm:"column:slot_id;not null" json:"slot_id"`
	// Payload 编码后的位流（1B flags + ceil(N/8)B mask + LSB 位打包量化值）。
	Payload []byte `gorm:"column:payload;not null" json:"payload"`
	// Raw 原始总线帧（可选）。只在关键帧/错误帧上保留，供"停写旧表后仍可回放"
	// （§9.6 可回滚）。常规增量帧留 NULL：raw 平均 78 B，占满会吃掉本方案的
	// 大部分收益（§1.2「帧级行 + 原始 raw(78B) 列」= 174.3 B/行）。
	Raw []byte `gorm:"column:raw" json:"raw,omitempty"`
}

// TableName 显式声明表名（与 partitionParentDDL 的登记键一致）。
func (TelemetryFrame) TableName() string { return "telemetry_frame" }

// FrameKind 取值（与 codec.FlagKeyFrame 语义一一对应，但**不共用常量**：
// 一个是列值，一个是 payload 内部 flag 位，二者独立演进）。
const (
	// TelemetryFrameKindDelta 增量帧：payload 只含本帧变化的字段。
	TelemetryFrameKindDelta int16 = 0
	// TelemetryFrameKindKey 关键帧：payload 含该 slot 全部有效字段。
	TelemetryFrameKindKey int16 = 1
)

// TelemetrySlot 是 slot 字典的一行（§4.4）：帧类型 = 字段集合 + 量化规格快照。
//
// ============ 为什么不能硬编码槽位（方案原话）============
//
// device15 实测 7 种帧类型（3/14/2/5/5/9/4 字段），来自 7 条不同指令。
// 硬编码"42 槽 = 6 设备 × 7 型号"会把不同指令的值写进同一槽 ⇒ **写串**。
// 因此 slot 由「command_template_id + 解析结果签名」在运行时**派生**，
// 本表只是它的持久化（跨进程稳定，重启后同一帧类型仍拿到同一 slot_id）。
//
// ============ 为什么 fields_json 里存的是规格快照而不是引用 ============
//
// 量化有损：同一串码点在不同 (量程, 步长) 下解出不同物理量。若只存"规格版本号+
// 当前代码里的规格表"，那么调整一个字段的步长就会让**历史帧**按新规格解错。
// 把 (name, min, step, bits) 快照内联进 slot，历史帧永远按写入时的规格解，
// 与当前代码的默认表解耦（§4.3 尾注「读旧数据用记录在 slot 描述中的版本号」）。
type TelemetrySlot struct {
	// SlotID 帧类型编号。smallint 而非 int32：实测总槽位数是个位数到几十。
	SlotID int16 `gorm:"column:slot_id;primaryKey;autoIncrement" json:"slot_id"`
	// Signature 字典键：sha256(command_template_id | spec_version | 排好序的字段名)。
	// 唯一索引保证同一帧类型在任何进程上只有一行、一个 slot_id。
	Signature string `gorm:"column:signature;size:64;not null;uniqueIndex" json:"signature"`
	// CommandTemplateID 产出该帧的指令模板 ID（0 = 非命令路径，如 BMS 普通驱动）。
	// 它进签名：两条不同指令哪怕字段集合相同也算两个 slot —— 因为它们
	// **语义不同**（同一物理量的不同量程/不同刷新率），合并会让变化抑制失效。
	CommandTemplateID int64 `gorm:"column:command_template_id;not null;default:0" json:"command_template_id"`
	// SpecVersion 写入该 slot 时使用的量化规格版本。
	SpecVersion int16 `gorm:"column:spec_version;not null" json:"spec_version"`
	// FieldCount 字段数（= fields_json 的长度；冗余列用于校验与快速拒绝坏行）。
	FieldCount int16 `gorm:"column:field_count;not null" json:"field_count"`
	// FieldsJSON 字段名 + 量化规格快照，JSON 数组（见 telemetry.SlotField）。
	FieldsJSON string    `gorm:"column:fields_json;type:text;not null" json:"fields_json"`
	CreatedAt  time.Time `json:"created_at"`
}

// TableName 显式声明表名。
func (TelemetrySlot) TableName() string { return "telemetry_slot" }
