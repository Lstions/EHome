package models

import "time"

// =====================================================================
// 阈值告警引擎 (方案 v0.4 §5 任务C)
// =====================================================================

// 告警规则目标类型 (TargetType 取值)
const (
	AlertTargetEdgeDevice    = "edge_device"
	AlertTargetLogicalDevice = "logical_device"
)

// 告警比较符 (Comparator 取值)
const (
	AlertComparatorGT  = "gt"
	AlertComparatorGTE = "gte"
	AlertComparatorLT  = "lt"
	AlertComparatorLTE = "lte"
	AlertComparatorEQ  = "eq"
	AlertComparatorNEQ = "neq"
)

// 告警级别 (Level 取值)
const (
	AlertLevelInfo     = "info"
	AlertLevelWarning  = "warning"
	AlertLevelCritical = "critical"
)

// AlertRule 阈值规则 (方案 v0.4 §5.1.1)。
// 挂接点: SensorParserConsumer 解析后回调 (alertSink), 非独立 DataConsumer。
type AlertRule struct {
	ID         uint    `gorm:"primaryKey" json:"id"`
	TargetType string  `gorm:"size:20;not null;index;default:'edge_device'" json:"target_type"` // edge_device | logical_device
	TargetID   uint    `gorm:"not null;index" json:"target_id"`
	SensorName string  `gorm:"size:64;not null;index" json:"sensor_name"` // 与 UnifiedData.SensorName 同域
	Comparator string  `gorm:"size:8;not null;default:'gt'" json:"comparator"` // gt|gte|lt|lte|eq|neq
	Threshold  float64 `json:"threshold"`
	DurationSec int    `gorm:"default:0" json:"duration_sec"` // 连续满足时长, 0=立即
	SilenceSec  int    `gorm:"default:300" json:"silence_sec"` // 恢复后静默窗口, 默认 300
	Level      string  `gorm:"size:10;not null;default:'warning'" json:"level"` // info|warning|critical
	Enabled    bool    `gorm:"default:true" json:"enabled"`
	Name       string  `gorm:"size:64;not null" json:"name"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (AlertRule) TableName() string { return "alert_rules" }

// AlertEvent 告警事件 (含恢复, 方案 v0.4 §5.1.1)。
// State: firing | resolved。firing/resolved 各生成一行事件;
// SilenceSec 内的重复满足只更新最近一条 firing 事件的 Value/NotifiedAt, 不再发新通知。
type AlertEvent struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	RuleID     uint       `gorm:"not null;index" json:"rule_id"`
	State      string     `gorm:"size:10;not null;index" json:"state"` // firing | resolved
	Value      float64    `json:"value"`                                // 触发/恢复时值
	FiredAt    *time.Time `json:"fired_at"`
	ResolvedAt *time.Time `json:"resolved_at"`
	NotifiedAt *time.Time `json:"notified_at"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

func (AlertEvent) TableName() string { return "alert_events" }

// NotificationType 告警级别 → 通知中心 type 映射 (§5.1.3):
// critical→"error", warning→"warning", info→"info", 其余 fallback "info"。
func NotificationType(level string) string {
	switch level {
	case AlertLevelCritical:
		return "error"
	case AlertLevelWarning:
		return "warning"
	default:
		return "info"
	}
}
