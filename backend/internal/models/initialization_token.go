package models

import "time"

// InitializationToken stores only a selector and secret hash. The raw
// selector.secret credential is shown once by the local administration CLI.
type InitializationToken struct {
	ID           uint64     `gorm:"primaryKey" json:"-"`
	Selector     string     `gorm:"size:64;uniqueIndex;not null" json:"-"`
	SecretHash   string     `gorm:"size:128;not null" json:"-"`
	Source       string     `gorm:"size:64;not null" json:"-"`
	AttemptCount int        `gorm:"not null;default:0" json:"-"`
	ExpiresAt    time.Time  `gorm:"not null;index" json:"-"`
	ConsumedAt   *time.Time `json:"-"`
	CreatedAt    time.Time  `json:"-"`
}
