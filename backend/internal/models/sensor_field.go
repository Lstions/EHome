package models

import "time"

// 数据字段解析层（Field Resolution Layer）的两张新表。
//
// 权威设计：docs/设计/数据字段解析层-方案-2026-10-21.md v0.2 §4.2/§4.3。
//
// 两张表都在 internal/models（叶子包）：sensorfield 领域包**不**定义 GORM 模型，
// 避免 models → sensorfield 的依赖倒灌（方案 §3.2 依赖方向）。
//
// # 命名口径（写死，防 DS-14 陷阱）
//
// 本文件所有 device_id 一律指 **EdgeDevice.ID**，即 unified_data.device_id 列的值。
// unified_data.edge_device_id 当前全表 NULL（台账 DS-14 未修复），
// **禁止**用它做投影或聚合（方案 §4.1 命名口径 / v0.2 关键修订）。

// SensorFieldObs 是"观测"投影表：某设备实际入库过的字段。
//
// 数据来源：cmd/server 在既有 SetLatestSinkFn 注入点包装 Observe（纯内存计数），
// 由 projector 每 observe_interval（默认 60s）批量 UPSERT 一次。
//
// 表名 sensor_field_obs（模型 tag 推导即得，不显式 TableName，保持与全仓一致的可推导性；
// 如有必要再加方法，本文件不引入额外命名魔法）。
type SensorFieldObs struct {
	// DeviceID = EdgeDevice.ID（= unified_data.device_id），复合主键第一列。
	DeviceID uint `gorm:"column:device_id;primaryKey;not null" json:"device_id"`
	// SensorName 物理名字符串（原始 parser/driver 产出名，不做任何归一）。
	SensorName string `gorm:"column:sensor_name;primaryKey;size:64;not null" json:"sensor_name"`
	// Unit 最近一次观测到的单位（空串 = 未知；单位只用于展示/校验，不参与命中判断）。
	Unit string `gorm:"size:16;not null;default:''" json:"unit"`
	// FirstSeen 首次观测时间（回填/投影共用，取 LEAST 语义）。
	FirstSeen time.Time `gorm:"not null" json:"first_seen"`
	// LastSeen 最近一次观测时间（有独立索引：暴毙字段排查与 never_resolved 口径都会扫它）。
	LastSeen time.Time `gorm:"not null;index:idx_sfo_last_seen" json:"last_seen"`
	// Samples 累计观测条数（增量累加；回填用聚合 count(*)）。
	Samples int64 `gorm:"not null;default:0" json:"samples"`
	// UpdatedAt 行更新时间（投影批次写；GORM 自动维护）。
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName 显式声明表名（与方案 §4.2 的 CREATE TABLE 一致）。
func (SensorFieldObs) TableName() string { return "sensor_field_obs" }

// SensorFieldMeta 是"丰富层"：字段的展示元数据（中文名/单位/语义类型/精度/状态）。
//
// 缺行不影响功能：前端降级显示物理名（方案 §4.3）。
// 唯一消费者是展示 API（GET /api/v1/sensor-fields），不参与求值。
type SensorFieldMeta struct {
	// SensorName 物理名，主键。丰富层按"物理名"而非按设备：同一物理名跨设备共享展示元数据。
	SensorName string `gorm:"column:sensor_name;primaryKey;size:64;not null" json:"sensor_name"`
	// LabelZh 中文展示名（空串 = 未提供，前端回落物理名）。
	LabelZh string `gorm:"column:label_zh;size:128;not null;default:''" json:"label_zh"`
	// Unit 单位（空串 = 未知）。
	Unit string `gorm:"size:16;not null;default:''" json:"unit"`
	// ValueType 语义类型：number/boolean/enum/bitmask/string/raw（空串 = 未标注）。
	// 只有 number 参与数值比较（方案 §5.3 定义 1）。
	ValueType string `gorm:"column:value_type;size:16;not null;default:''" json:"value_type"`
	// Precision 展示小数位；nil = 未设置（NULL 与 0 有区别：0 表示"整数展示"是合法值，
	// 不能折叠成"未设置"，故用指针）。
	Precision *int `json:"precision"`
	// Status active / deprecated。deprecated 的字段默认不出现在 GET /sensor-fields，
	// 需 include_deprecated=true 才返回（响应里 deprecated:true）。
	Status    string    `gorm:"size:16;not null;default:'';index" json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName 显式声明表名（与方案 §4.3 一致）。
func (SensorFieldMeta) TableName() string { return "sensor_field_meta" }
