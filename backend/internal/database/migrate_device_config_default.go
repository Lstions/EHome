package database

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

const deviceConfigDefaultIndex = "ux_device_configs_one_default_per_type"

// ensureDeviceConfigDefaultConstraint rejects ambiguous historical defaults
// before installing the partial unique index that preserves the invariant:
// at most one live default exists for each device type.
func ensureDeviceConfigDefaultConstraint(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("device_configs default constraint: database is nil")
	}
	if !db.Migrator().HasTable("device_configs") {
		return nil
	}

	var deviceTypes []string
	if err := db.Raw(`
		SELECT device_type
		FROM device_configs
		WHERE is_default = TRUE AND deleted_at IS NULL
		GROUP BY device_type
		HAVING COUNT(*) > 1
	`).Scan(&deviceTypes).Error; err != nil {
		return fmt.Errorf("check device_configs default duplicates: %w", err)
	}
	if len(deviceTypes) != 0 {
		conflicts := make([]string, 0, len(deviceTypes))
		for _, deviceType := range deviceTypes {
			var ids []uint
			if err := db.Raw(`
				SELECT id FROM device_configs
				WHERE device_type = ? AND is_default = TRUE AND deleted_at IS NULL
				ORDER BY id
			`, deviceType).Scan(&ids).Error; err != nil {
				return fmt.Errorf("inspect duplicate default device_configs for %q: %w", deviceType, err)
			}
			conflicts = append(conflicts, fmt.Sprintf("device_type=%q ids=%v", deviceType, ids))
		}
		return fmt.Errorf("device_configs default migration_required: conflicting live defaults (%s); reconcile explicitly before retrying", strings.Join(conflicts, "; "))
	}

	if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS ux_device_configs_one_default_per_type ON device_configs (device_type) WHERE is_default = TRUE AND deleted_at IS NULL`).Error; err != nil {
		return fmt.Errorf("create device_configs default unique index: %w", err)
	}
	return nil
}
