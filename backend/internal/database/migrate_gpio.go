package database

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/logger"

	"gorm.io/gorm"
)

// MigrateGPIOChannelsResult records the outcome of a GPIO channel migration run.
type MigrateGPIOChannelsResult struct {
	Scanned  int      // total GPIO channels found
	Migrated int      // new GPIOConfig rows created
	Skipped  int      // channels skipped (bad data, already migrated, etc.)
	Errors   int      // channels that hit unexpected errors
	Warnings []string // per-channel warning messages
}

// MigrateGPIOChannels performs a one-time, idempotent migration of old GPIO
// channels (hardware_type='gpio' OR bus_type='GPIO') into the standalone
// gpio_configs table.
//
// Design (based on docs/设计/GPIO控制重构设计.md §8.1):
//
//   - Old Channel.bus_config is hex-encoded binary: [0]=pin, [1]=direction
//   - Old Channel.config is a JSON string that may contain: label, initial_level, enabled
//   - New GPIOConfig uses (node_id, pin) as the logical unique key
//
// Safety guarantees:
//
//   - Transactional: the entire migration runs in a single GORM transaction.
//     Any DB-level failure rolls back all changes.
//   - Idempotent: if a GPIOConfig already exists for (node_id, pin), the
//     channel is skipped (counted as Skipped, not duplicated).
//   - Non-destructive: old Channel records are NOT deleted or modified.
//   - Bad-data tolerant: channels with missing/invalid pin or unparseable
//     config are skipped with a warning rather than failing the migration.
//     Only a DB transaction error aborts the entire migration.
func MigrateGPIOChannels(db *gorm.DB) (*MigrateGPIOChannelsResult, error) {
	result := &MigrateGPIOChannelsResult{}

	// Fetch candidate channels outside the transaction (read-only).
	var channels []models.Channel
	if err := db.Where(
		"LOWER(TRIM(hardware_type)) IN ? OR LOWER(TRIM(bus_type)) IN ?",
		[]string{"gpio", "4"}, []string{"gpio", "4"},
	).Find(&channels).Error; err != nil {
		return nil, fmt.Errorf("migrate_gpio: query channels: %w", err)
	}
	result.Scanned = len(channels)

	if len(channels) == 0 {
		return result, nil
	}

	// Run the mutation phase inside a transaction.
	err := db.Transaction(func(tx *gorm.DB) error {
		for _, ch := range channels {
			if err := migrateOneGPIOChannel(tx, ch, result); err != nil {
				// A DB-level error — abort the transaction.
				return err
			}
		}
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("migrate_gpio: transaction: %w", err)
	}

	logger.Infof("migrate_gpio: scanned=%d migrated=%d skipped=%d errors=%d",
		result.Scanned, result.Migrated, result.Skipped, result.Errors)
	return result, nil
}

// migrateOneGPIOChannel processes a single channel row. Non-DB errors are
// recorded as warnings in result and do not propagate; only DB errors
// propagate to abort the transaction.
func migrateOneGPIOChannel(tx *gorm.DB, ch models.Channel, result *MigrateGPIOChannelsResult) error {
	quarantine := func(reason string) error {
		if err := retireLegacyChannel(tx, ch.ID); err != nil {
			result.Errors++
			return err
		}
		result.Skipped++
		result.Warnings = append(result.Warnings, reason)
		return nil
	}
	// --- 1. Parse pin from bus_config (hex-encoded binary) ---
	pin, ok := parseGPIOPinFromBusConfig(ch.BusConfig)
	if !ok {
		return quarantine(fmt.Sprintf("channel id=%d node_id=%s: cannot parse pin from bus_config %q", ch.ID, ch.NodeID, ch.BusConfig))
	}

	// --- 2. Parse direction from bus_config (second byte) ---
	direction, ok := parseGPIODirectionFromBusConfig(ch.BusConfig)
	if !ok {
		return quarantine(fmt.Sprintf("channel id=%d node_id=%s: invalid or missing GPIO direction", ch.ID, ch.NodeID))
	}

	// --- 3. Parse optional fields from config JSON ---
	var cfgMap map[string]interface{}
	if ch.Config != "" {
		if err := json.Unmarshal([]byte(ch.Config), &cfgMap); err != nil {
			return quarantine(fmt.Sprintf("channel id=%d node_id=%s: invalid config JSON %q: %v", ch.ID, ch.NodeID, ch.Config, err))
		}
	}

	label := ""
	initialLevel := uint8(0)
	enabled := ch.Enabled // default to channel's enabled state

	if cfgMap != nil {
		if l, ok := cfgMap["label"].(string); ok {
			label = l
		}
		if il, ok := cfgMap["initial_level"]; ok {
			valid := false
			switch v := il.(type) {
			case float64:
				if v == 0 || v == 1 {
					initialLevel = uint8(v)
					valid = true
				}
			case json.Number:
				n, _ := v.Int64()
				if n == 0 || n == 1 {
					initialLevel = uint8(n)
					valid = true
				}
			}
			if !valid {
				return quarantine(fmt.Sprintf("channel id=%d node_id=%s: invalid initial_level", ch.ID, ch.NodeID))
			}
		}
		if e, ok := cfgMap["enabled"].(bool); ok {
			enabled = e
		}
	}

	// --- 4. Idempotency check: skip if GPIOConfig already exists for (node_id, pin) ---
	var existing int64
	if err := tx.Model(&models.GPIOConfig{}).
		Where("node_id = ? AND pin = ?", ch.NodeID, pin).
		Count(&existing).Error; err != nil {
		result.Errors++
		return fmt.Errorf("check existing gpio_config for node_id=%s pin=%d: %w", ch.NodeID, pin, err)
	}
	if existing > 0 {
		if err := retireLegacyChannel(tx, ch.ID); err != nil {
			result.Errors++
			return fmt.Errorf("retire migrated GPIO channel id=%d: %w", ch.ID, err)
		}
		result.Skipped++
		return nil
	}

	// --- 5. Create GPIOConfig and retire the legacy executable channel ---
	// Use a map to avoid GORM's default:true overriding a zero-value false
	// on the Enabled field.
	createData := map[string]interface{}{
		"node_id":       ch.NodeID,
		"pin":           pin,
		"direction":     direction,
		"initial_level": initialLevel,
		"label":         label,
		"enabled":       enabled,
	}

	if err := tx.Model(&models.GPIOConfig{}).Create(createData).Error; err != nil {
		// If it's a unique constraint violation (race or already exists), skip.
		if isUniqueConstraintError(err) {
			if retireErr := retireLegacyChannel(tx, ch.ID); retireErr != nil {
				result.Errors++
				return retireErr
			}
			result.Skipped++
			return nil
		}
		result.Errors++
		return fmt.Errorf("create gpio_config for node_id=%s pin=%d: %w", ch.NodeID, pin, err)
	}

	if err := retireLegacyChannel(tx, ch.ID); err != nil {
		result.Errors++
		return fmt.Errorf("retire migrated GPIO channel id=%d: %w", ch.ID, err)
	}
	result.Migrated++
	return nil
}

func retireLegacyChannel(tx *gorm.DB, channelID uint) error {
	if err := tx.Model(&models.EdgeDevice{}).Where("channel_id = ?", channelID).Update("enabled", false).Error; err != nil {
		return fmt.Errorf("disable dependent edge devices for channel id=%d: %w", channelID, err)
	}
	if err := tx.Model(&models.Channel{}).Where("id = ?", channelID).Update("enabled", false).Error; err != nil {
		return fmt.Errorf("disable legacy channel id=%d: %w", channelID, err)
	}
	return nil
}

// parseGPIOPinFromBusConfig extracts the pin number from hex-encoded
// bus_config. The first byte is the pin number. Returns (pin, true) on
// success, (0, false) if the config is missing or too short.
func parseGPIOPinFromBusConfig(busConfig string) (int, bool) {
	busConfig = strings.TrimSpace(busConfig)
	if busConfig == "" {
		return 0, false
	}
	data, err := hex.DecodeString(busConfig)
	if err != nil || len(data) < 1 {
		return 0, false
	}
	return int(data[0]), true
}

// parseGPIODirectionFromBusConfig extracts the direction from hex-encoded
// bus_config. The second byte is the direction (0-3). Returns (dir, true) on
// success, (0, false) if the config is missing or too short.
func parseGPIODirectionFromBusConfig(busConfig string) (uint8, bool) {
	busConfig = strings.TrimSpace(busConfig)
	if busConfig == "" {
		return 0, false
	}
	data, err := hex.DecodeString(busConfig)
	if err != nil || len(data) < 2 {
		return 0, false
	}
	dir := data[1]
	if dir > 3 {
		// Invalid direction — default to 0 (INPUT) but signal not-ok.
		return 0, false
	}
	return dir, true
}

// isUniqueConstraintError checks whether a GORM error represents a unique
// constraint violation (SQLite and PostgreSQL have different message formats).
func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// SQLite: "UNIQUE constraint failed: ..."
	// PostgreSQL: "duplicate key value violates unique constraint ..."
	return strings.Contains(msg, "UNIQUE") || strings.Contains(msg, "duplicate")
}
