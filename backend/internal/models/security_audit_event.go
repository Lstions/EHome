package models

import "time"

// SecurityAuditEvent is an append-only security event.
//
// 它是唯一的审计事件载体: 旧 models.OperationLog 已于 2026-09 退役
// (0 写入者 / 0 读取者 / 0 行, 缺 request_id/source_ip/result/metadata,
// 无法承载任何现代审计问题; 裁决见
// docs/分析/运行期无界增长表-保留策略设计-2026-09-13.md §2.7), 表本身由
// database.RetireLegacyOperationLogs 幂等 DROP。
type SecurityAuditEvent struct {
	ID            uint64    `gorm:"primaryKey" json:"id"`
	ActorType     string    `gorm:"size:32;not null;index" json:"actor_type"`
	ActorUserID   *uint     `gorm:"index" json:"actor_user_id,omitempty"`
	ActorSnapshot string    `gorm:"size:128" json:"actor_snapshot,omitempty"`
	EventName     string    `gorm:"size:96;not null;index" json:"event_name"`
	EventVersion  int       `gorm:"not null;default:1" json:"event_version"`
	Result        string    `gorm:"size:24;not null;index" json:"result"`
	RequestID     string    `gorm:"size:64;index" json:"request_id,omitempty"`
	SourceIP      string    `gorm:"size:64" json:"source_ip,omitempty"`
	TargetType    string    `gorm:"size:64" json:"target_type,omitempty"`
	TargetID      string    `gorm:"size:128" json:"target_id,omitempty"`
	Metadata      string    `gorm:"type:text" json:"metadata,omitempty"`
	CreatedAt     time.Time `gorm:"index;not null" json:"created_at"`
}
