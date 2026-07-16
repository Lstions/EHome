package database

import (
	"ehome/backend/internal/models"
	"fmt"

	"gorm.io/gorm"
)

// LegacyPWMRow identifies a pin-only PWM row that cannot be assigned a
// hardware resource without a fresh ResourceReport and operator choice.
type LegacyPWMRow struct {
	ID     uint   `json:"id"`
	NodeID string `json:"node_id"`
	Pin    int    `json:"pin"`
}

// LegacyPWMCheckResult is returned before migrating the PWM schema.
type LegacyPWMCheckResult struct {
	MigrationRequired bool           `json:"migration_required"`
	Rows              []LegacyPWMRow `json:"rows,omitempty"`
}

// CheckLegacyPWMRows detects an existing pin-only pwm_configs schema. It never
// guesses hardware_id/channel and never mutates the table. Call it before
// AutoMigrate adds the new identity columns.
func CheckLegacyPWMRows(db *gorm.DB) (*LegacyPWMCheckResult, error) {
	result := &LegacyPWMCheckResult{}
	if !db.Migrator().HasTable("pwm_configs") {
		return result, nil
	}
	if db.Migrator().HasColumn("pwm_configs", "hardware_id") && db.Migrator().HasColumn("pwm_configs", "channel") {
		return result, nil
	}
	if err := db.Table("pwm_configs").Select("id, node_id, pin").Order("id ASC").Scan(&result.Rows).Error; err != nil {
		return nil, fmt.Errorf("check legacy pwm_configs rows: %w", err)
	}
	result.MigrationRequired = len(result.Rows) > 0
	return result, nil
}

// RetireLegacyPWMChannels disables legacy PWM Channel rows and every dependent
// EdgeDevice binding. The rows remain available for audit but cannot execute.
func RetireLegacyPWMChannels(db *gorm.DB) (int64, error) {
	var channels []models.Channel
	if err := db.Where("LOWER(TRIM(bus_type)) IN ? OR LOWER(TRIM(hardware_type)) IN ?",
		[]string{"pwm", "6"}, []string{"pwm", "6"}).Find(&channels).Error; err != nil {
		return 0, fmt.Errorf("query legacy PWM channels: %w", err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		for _, ch := range channels {
			if err := retireLegacyChannel(tx, ch.ID); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return 0, err
	}
	return int64(len(channels)), nil
}
