package models

import "time"

// ConfigChangeOutbox is the durable hand-off from a committed control side
// effect to the in-process SyncGate. It is separate from CommandInbox because
// a command may be terminal while its manifest notification is still waiting
// for an event consumer or a temporarily unavailable collector.
type ConfigChangeOutbox struct {
	EventID     string     `gorm:"primaryKey;size:96" json:"event_id"`
	Type        string     `gorm:"size:32;not null" json:"type"`
	Action      string     `gorm:"size:16;not null" json:"action"`
	NodeID      string     `gorm:"size:32;not null;index" json:"node_id"`
	EntityID    string     `gorm:"size:64" json:"entity_id"`
	Actor       string     `gorm:"size:96" json:"actor"`
	State       string     `gorm:"size:16;not null;index" json:"state"`
	CreatedAt   time.Time  `gorm:"not null;index" json:"created_at"`
	ProcessedAt *time.Time `gorm:"index" json:"processed_at,omitempty"`
}
