package datalifecycle

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"gorm.io/gorm"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
)

// CommandIntervalsCleanupReport summarizes one cleanup pass.
type CommandIntervalsCleanupReport struct {
	DevicesScanned int
	DevicesCleaned int
	KeysRemoved    int
}

// CleanupCommandIntervals removes every key of edge_devices.command_intervals
// that is not a schedulable command template id of the device type's driver
// (I-1 enforcement on legacy data; 演进方案 C2/M4).
//
// Handles the three legacy shapes: NULL, empty map, and maps containing
// non-schedulable keys. A map that becomes empty is stored as NULL.
// A device whose type has no registered driver (or no template provider)
// has an empty schedulable set, so all its keys are removed.
//
// Idempotent: a second pass finds no non-schedulable keys and writes nothing.
func CleanupCommandIntervals(db *gorm.DB, registry *drivers.Registry) (CommandIntervalsCleanupReport, error) {
	var report CommandIntervalsCleanupReport
	if registry == nil {
		return report, fmt.Errorf("datalifecycle: cleanup command_intervals requires a driver registry")
	}
	var devices []models.EdgeDevice
	if err := db.Find(&devices).Error; err != nil {
		return report, fmt.Errorf("datalifecycle: scan edge_devices: %w", err)
	}
	for i := range devices {
		dev := &devices[i]
		report.DevicesScanned++
		intervals := parseCleanupIntervals(dev.CommandIntervals)
		if len(intervals) == 0 {
			continue // NULL or empty map: nothing to remove
		}
		schedulable := map[string]struct{}{}
		if drv, err := registry.Get(dev.Type); err == nil {
			if provider, ok := drv.(drivers.CommandTemplateProvider); ok {
				for _, tmpl := range provider.GetCommandTemplates() {
					if tmpl.Schedulable {
						schedulable[tmpl.ID] = struct{}{}
					}
				}
			}
		}
		cleaned := make(map[string]int, len(intervals))
		removed := 0
		for id, v := range intervals {
			if _, ok := schedulable[id]; ok {
				cleaned[id] = v
			} else {
				removed++
			}
		}
		if removed == 0 {
			continue
		}
		var raw any
		if len(cleaned) == 0 {
			raw = nil // empty after cleanup → NULL
		} else {
			b, err := json.Marshal(cleaned)
			if err != nil {
				return report, fmt.Errorf("datalifecycle: marshal cleaned intervals for device %d: %w", dev.ID, err)
			}
			raw = json.RawMessage(b)
		}
		if err := db.Model(&models.EdgeDevice{}).Where("id = ?", dev.ID).
			Update("command_intervals", raw).Error; err != nil {
			return report, fmt.Errorf("datalifecycle: update command_intervals for device %d: %w", dev.ID, err)
		}
		report.DevicesCleaned++
		report.KeysRemoved += removed
		slog.Info("datalifecycle: command_intervals cleaned",
			"edge_device", dev.ID, "type", dev.Type, "keys_removed", removed)
	}
	return report, nil
}

func parseCleanupIntervals(raw json.RawMessage) map[string]int {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]int
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}
