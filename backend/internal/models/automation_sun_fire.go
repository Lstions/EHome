package models

import "time"

// AutomationSunFire 是日出/日落事件的**认领行**：每场事件全局只允许一个进程触发。
//
// 为什么需要它（2026-11-18 P2）：求值器的 sunStates 在进程内存，单副本时足够；
// 但滚动发布会短暂并存两个容器，两边各自认为"本场未触发" ⇒ 同一场事件各下发
// 一次设备动作。本表用唯一键 (rule_id, event_date, kind) 做跨进程抢占：
// INSERT ... ON CONFLICT DO NOTHING，抢到（RowsAffected=1）的进程才执行。
//
// 语义是 at-most-once：抢到后若进程在提交动作前崩溃，本场事件丢失（不再补发）。
// 这是刻意选择 —— 重复下发设备动作（例如重复断开 BMS MOS）比漏一次更危险；
// 且 missed_solar_event 审计可让用户看见"这次没执行"。
type AutomationSunFire struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// RuleID + EventDate + Kind 组成全局唯一键。
	RuleID uint `gorm:"not null;uniqueIndex:uniq_sun_fire_claim,priority:1" json:"rule_id"`
	// EventDate 是**事件所属的规则本地日** "2006-01-02"（不是 fireAt 的日期 ——
	// 偏移跨午夜时两者不同；用 fireAt 日期会让同一场事件被认领两次）。
	EventDate string `gorm:"size:10;not null;uniqueIndex:uniq_sun_fire_claim,priority:2" json:"event_date"`
	Kind      string `gorm:"size:8;not null;uniqueIndex:uniq_sun_fire_claim,priority:3" json:"kind"`
	// FireAt 是 eventAt + offset，保留给排障（认领键里不含它，因为同一场事件
	// 的理论 fireAt 是确定的，偏移变化会走 configChanged 而不是换一场事件）。
	FireAt    time.Time `gorm:"not null" json:"fire_at"`
	CreatedAt time.Time `json:"created_at"`
}

// TableName 固定表名（GORM 默认复数化会得到 automation_sun_fires，显式写死防漂移）。
func (AutomationSunFire) TableName() string { return "automation_sun_fires" }
