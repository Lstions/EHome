package models

import "time"

// CommandConfirmation is a one-time server-side confirmation grant for a
// high/critical action. Only a SHA-256 token hash is persisted; the raw token
// is returned once to the authenticated caller and is never audited or logged.
type CommandConfirmation struct {
	TokenHash     string     `gorm:"primaryKey;size:64" json:"-"`
	ActorUserID   uint       `gorm:"not null;index" json:"-"`
	EdgeDeviceID  uint       `gorm:"not null;index" json:"-"`
	ActionID      string     `gorm:"size:96;not null" json:"-"`
	ActionVersion int        `gorm:"not null" json:"-"`
	RequestHash   string     `gorm:"size:64;not null;index" json:"-"`
	ExpiresAt     time.Time  `gorm:"not null;index" json:"expires_at"`
	ConsumedAt    *time.Time `gorm:"index" json:"-"`
	CreatedAt     time.Time  `gorm:"not null;index" json:"created_at"`
}
