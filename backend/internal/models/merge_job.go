package models

import "time"

// MergeJob 合并搬迁任务进度行 (方案 v3.3 §4.3 任务 3 / §3.4)。
//
// 一个 merge_jobs 行对应一个源逻辑设备向目标的搬迁。API 事务只创建
// 标记行 (v1-H3: 搬迁全部移出 API 事务), 后台 Migrator 按 watermark_id
// 断点续跑; 失败按水位重试 (同目标幂等), 超限才 failed + 源复位 (§3.4
// v3.2-F3)。watermark 语义与 backfill_jobs 一致: 已处理到的数据行 id
// 上界, unified_data 处理完后进入 device_data (phase 切换, id 从头)。
type MergeJob struct {
	ID              uint       `gorm:"primaryKey" json:"id"`
	SourceLogicalID uint       `gorm:"not null;index" json:"source_logical_id"`
	TargetLogicalID uint       `gorm:"not null;index" json:"target_logical_id"`
	Status          string     `gorm:"size:16;not null;default:pending" json:"status"` // pending / running / done / failed
	MigratedRows    int64      `gorm:"not null;default:0" json:"migrated_rows"`
	TotalEstimate   int64      `gorm:"not null;default:0" json:"total_estimate"` // 创建时 row_estimate 快照 (进度分母, 可能不精确)
	WatermarkID     int64      `gorm:"not null;default:0" json:"watermark_id"`   // 当前 phase 已处理数据行 id 上界
	WatermarkPhase  string     `gorm:"size:16;not null;default:unified_data" json:"watermark_phase"`
	RetryCount      int        `gorm:"not null;default:0" json:"retry_count"` // 失败重试计数, 超限 (3) 才 failed+复位
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	FinishedAt      *time.Time `json:"finished_at"`
}

// TableName GORM 表名
func (MergeJob) TableName() string { return "merge_jobs" }

// MergeJob 状态取值
const (
	MergeJobPending = "pending"
	MergeJobRunning = "running"
	MergeJobDone    = "done"
	MergeJobFailed  = "failed"
)

// MergeJob 水位 phase 取值 (unified_data 先行, device_data 随后)
const (
	MergePhaseUnifiedData = "unified_data"
	MergePhaseDeviceData  = "device_data"
)
