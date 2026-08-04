package models

import "time"

// BackfillJob 大表回填进度水位行 (方案 v3.3 §4.3 断点续跑 / §七 M 迁移步骤)。
//
// 每个 (job_type, table_name) 一行。watermark_id 记录已处理到的数据行 id
// 上界, 中断重启后按 `id > watermark_id` 续跑; 水位推进用条件 UPDATE
// (WHERE watermark_id = 旧值) 作乐观锁, 多副本并发不会互相覆盖。
//
// 选独立 backfill_jobs 表而非给 merge_jobs 加 job_type 字段: merge_jobs
// 属于 P3 合并业务 (本卡不存在该表), 一次性迁移水位的生命周期 (running→
// done 后只读留存) 也与持续运营业务表不同, 预先定义 merge_jobs 结构会
// 与 P3 卡的 schema 决策互相打架。
type BackfillJob struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	JobType     string     `gorm:"size:32;not null;uniqueIndex:idx_backfill_job_type_table" json:"job_type"`
	Table       string     `gorm:"column:table_name;size:64;not null;uniqueIndex:idx_backfill_job_type_table" json:"table_name"`
	Status      string     `gorm:"size:16;not null;default:running" json:"status"` // running / done
	WatermarkID int64      `gorm:"not null;default:0" json:"watermark_id"`         // 已处理数据行 id 上界
	RowsUpdated int64      `gorm:"not null;default:0" json:"rows_updated"`
	BatchCount  int64      `gorm:"not null;default:0" json:"batch_count"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	FinishedAt  *time.Time `json:"finished_at"`
}

// TableName GORM 表名
func (BackfillJob) TableName() string { return "backfill_jobs" }

// BackfillJob 状态取值
const (
	BackfillStatusRunning = "running"
	BackfillStatusDone    = "done"
)

// BackfillJobTypeLogicalID — §七 M 步骤: unified_data/device_data 回填
// logical_device_id。后续其它回填类型以此模式扩展。
const BackfillJobTypeLogicalID = "logical_device_id"
