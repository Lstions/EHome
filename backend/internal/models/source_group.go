package models

import "time"

// ── 来源组（设计/规则数据来源与复合来源-方案-2026-10-22.md §4.1/§4.2）──
//
// 为什么独立于 data_sources（方案 D-1 / P-1 裁决）：
//   - 组要**跨逻辑设备**（两台光照传感器通常属不同逻辑设备），而 data_sources
//     组键是 (LogicalDevice.ID, category)，跨设备会被拆成多个组；
//   - 主备 / 复合共用「成员集合」模型，用户的三种组合退化为成员数 / combine 不同；
//   - data_sources 行语义（某逻辑设备某类别由某边缘设备提供）与 v3.3「一逻辑
//     设备一存活实例」约束都不动。

// 来源组 kind 取值。
const (
	SourceGroupKindPrimaryBackup = "primary_backup"
	SourceGroupKindComposite     = "composite"
)

// 复合组合并算子白名单（方案 §4.1 / V-1）。
//
// 只保留 avg / sum（2026-10-22 收敛）：
//   - avg：多个同类传感器的共识值（原始需求「两个光照传感器合成一个光照数据源」）
//   - sum：累加类物理量（多路雨量计合计降雨量）
//
// 砍掉 max/min/first_available 的理由不是「用不上」，而是**收益成本比不成立**：
// 每多一个算子要同步 5 处（常量白名单 / CombineGroup case / 真值表测试 /
// 前端下拉项 / 门禁 SG-03），而后端实际只多 6 行 case。
// first_available 另有独立理由：它与 on_degrade=use_available 语义重叠（N-8），
// 已由缺员处置完整表达，不需要第二个入口。
//
// ⚠ 删除安全：生产 source_groups 表 0 行，无历史取值需要兼容。
const (
	CombineAvg = "avg"
	CombineSum = "sum"
)

// CombineWhitelist 是 composite 组允许的 combine 取值集合。
var CombineWhitelist = []string{CombineAvg, CombineSum}

// 缺员处置（方案 §4.1 / D-5）。空串归一为 fail_closed。
const (
	OnDegradeFailClosed   = "fail_closed"
	OnDegradeUseAvailable = "use_available"
)

// 成员在组内的角色状态。与 DataSource.Status **同词表但语义域不同**：
// 这里的 active = 组内权威成员（主备组）；composite 组不维护 active（I5）。
const (
	SourceMemberStatusActive   = "active"
	SourceMemberStatusStandby  = "standby"
	SourceMemberStatusError    = "error"
	SourceMemberStatusDisabled = "disabled"
)

// SourceGroup 是「一个数据来源」的产品对象：主备组或复合组。
//
// 纪律：本文件**不得出现非零 default: tag**（否则 models/defaults_gate_test.go
// 会要求登记，而登记表属「显式零值有语义」白名单，这里语义由 service 归一）。
type SourceGroup struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"size:128;not null" json:"name"`

	// Kind primary_backup | composite（必填，无默认；保存期校验枚举）。
	Kind string `gorm:"size:20;not null;index;check:chk_sg_kind,kind IN ('primary_backup','composite')" json:"kind"`
	// Category 组读取的物理名（= unified_data.sensor_name；一期只字面量，
	// 与 data_sources.category 同口径同宽 :64）。
	Category string `gorm:"size:64;not null;index" json:"category"`

	// Combine 仅 composite 必填：avg|sum（v0.4 收敛，见 ADR）。
	//   primary_backup 必须为空串（V-2）。
	//   CHECK 与 models.CombineWhitelist 必须一致（门禁 SG-03 断言两者相等）。
	Combine string `gorm:"size:20;not null;default:'';check:chk_sg_combine,combine IN ('avg','sum','')" json:"combine,omitempty"`
	// MinMembers composite 最少可用成员数；0 = 归一为 1。
	//   语义见 §12.9.2：只统计 required=false 成员的缺席。
	MinMembers int `gorm:"not null;default:0" json:"min_members"`
	// StalenessSec 成员值年龄上限（秒）。0 = 由采集间隔推导（2×Interval）。
	StalenessSec int `gorm:"not null;default:0" json:"staleness_sec"`
	// OnDegrade fail_closed | use_available（仅 composite；空串=fail_closed）。
	OnDegrade string `gorm:"size:16;not null;default:''" json:"on_degrade,omitempty"`

	// Enabled 人工启停；显式 false 不依赖 DB default（defaults_gate 陷阱）。
	Enabled bool `gorm:"not null;index" json:"enabled"`
	// Revision 拓扑/成员/active 变更时单调 +1（方案 F-1 bump 白名单）。
	//   **运行时健康写（MarkSuccess/MarkFailure 计数）不得 bump** —— 否则窗口
	//   identity 每帧变化、DurationSec 永不满足（复评 B-4 的教训）。
	Revision int64 `gorm:"not null;default:0" json:"revision"`

	Description string    `gorm:"size:255" json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (SourceGroup) TableName() string { return "source_groups" }

// SourceGroupMember 组内成员（一台边缘设备）及其组内运行态。
//
// 运行态放 member 行而不是复用 data_sources 行（P-1）：组键不同，且 data_sources
// 的唯一索引 (device_id,category,edge_device_id) 会限制跨逻辑设备。
type SourceGroupMember struct {
	ID uint `gorm:"primaryKey" json:"id"`

	// GroupID 双挂索引：成员唯一 (group_id,edge_device_id) + 每组至多一条 active。
	GroupID uint `gorm:"not null;index;uniqueIndex:ux_sgm_member,priority:1;uniqueIndex:ux_sgm_one_active,priority:1,where:status = 'active'" json:"group_id"`
	// EdgeDeviceID = EdgeDevice.ID（= unified_data.device_id；禁 unified_data.edge_device_id，DS-14）。
	EdgeDeviceID uint `gorm:"not null;uniqueIndex:ux_sgm_member,priority:2;index" json:"edge_device_id"`

	// Priority 越大越优先（R3 候选排序 priority DESC, id ASC）。
	Priority int `gorm:"not null;default:0" json:"priority"`
	// Required 仅 composite + use_available 有意义（§12.9.2）：
	//   缺 required=true 成员 ⇒ unbound；仅缺 required=false ⇒ 合并+Degraded。
	Required bool `gorm:"not null" json:"required"`

	// ── 组内运行态（R1–R11 唯一写入口；composite 只维护 last_success 供观测）──
	Status       string     `gorm:"size:20;not null;index" json:"status"`
	FailCount    int        `gorm:"not null;default:0" json:"fail_count"`
	MaxFailCount int        `gorm:"not null;default:0" json:"max_fail_count"` // 0 = 归一为 3
	LastSuccess  *time.Time `json:"last_success"`
	LastFailure  *time.Time `json:"last_failure"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (SourceGroupMember) TableName() string { return "source_group_members" }

// SourceGroupFailoverLog 组级切换日志（方案 N-11：**独立表**，不复用 failover_logs）。
//
// 为什么独立：failover_logs.DeviceID/Category 均 NOT NULL，组事件没有「逻辑设备 ID +
// 类别」这两个必填值，填 0 = 伪外键（契约评审 B6）。
type SourceGroupFailoverLog struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	GroupID      uint      `gorm:"not null;index:idx_sgfl_group_time,priority:1" json:"group_id"`
	FromMemberID uint      `json:"from_member_id"`
	ToMemberID   uint      `json:"to_member_id"`
	Reason       string    `gorm:"size:32;not null" json:"reason"`
	Trigger      string    `gorm:"size:32" json:"trigger"`
	CreatedAt    time.Time `gorm:"index:idx_sgfl_group_time,priority:2" json:"created_at"`
}

func (SourceGroupFailoverLog) TableName() string { return "source_group_failover_logs" }

// SourceGroupHealth 组级健康事件（方案 N-11：独立表）。
//
// Event 取值：transition | failure | success | degraded | selfheal | silent |
//
//	frame_discarded。Message 用于承载 reason / members 摘要。
type SourceGroupHealth struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	GroupID   uint      `gorm:"not null;index:idx_sgh_group_time,priority:1" json:"group_id"`
	MemberID  uint      `gorm:"index" json:"member_id"`
	Event     string    `gorm:"size:24;not null;index" json:"event"`
	Message   string    `gorm:"size:255" json:"message"`
	CreatedAt time.Time `gorm:"index:idx_sgh_group_time,priority:2" json:"created_at"`
}

func (SourceGroupHealth) TableName() string { return "source_group_health" }
