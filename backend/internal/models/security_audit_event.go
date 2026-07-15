package models

import "time"

// SecurityAuditEvent is an append-only security event. OperationLog remains
// available for legacy operational history.
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
