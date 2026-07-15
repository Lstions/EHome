package models

import "time"

// AuthOutbox persists authentication state changes for cross-instance delivery.
type AuthOutbox struct {
	ID             uint64     `gorm:"primaryKey" json:"-"`
	EventType      string     `gorm:"size:64;not null;index" json:"event_type"`
	SubjectID      uint       `gorm:"not null;index" json:"subject_id"`
	SessionVersion int64      `gorm:"not null" json:"session_version"`
	Reason         string     `gorm:"size:64" json:"reason"`
	CreatedAt      time.Time  `gorm:"not null;index" json:"created_at"`
	ProcessedAt    *time.Time `gorm:"index" json:"processed_at,omitempty"`
}
