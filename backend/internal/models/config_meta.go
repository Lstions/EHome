package models

// ConfigMeta stores global configuration metadata (epoch persistence).
// Single-row table (id=1) used by EpochGenerator.
type ConfigMeta struct {
	ID    uint   `gorm:"primaryKey;autoIncrement:false" json:"id"`
	Epoch uint64 `gorm:"not null;default:0" json:"epoch"`
}
