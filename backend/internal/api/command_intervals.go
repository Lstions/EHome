package api

import (
	"encoding/json"
	"fmt"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/nodemgr"
)

// SchedulableCommandIDs returns the set of schedulable command template IDs
// declared by the driver registered for devType. A driver that is not
// registered or provides no templates yields an error — callers treat the
// schedulable set as authoritative (I-1: command_intervals keys ⊆ this set).
func SchedulableCommandIDs(driverRegistry *drivers.Registry, devType string) (map[string]struct{}, error) {
	drv, err := driverRegistry.Get(devType)
	if err != nil {
		return nil, fmt.Errorf("cannot validate command_intervals: driver for type %q is not registered", devType)
	}
	provider, ok := drv.(drivers.CommandTemplateProvider)
	if !ok {
		return nil, fmt.Errorf("cannot validate command_intervals: driver for type %q provides no command templates", devType)
	}
	schedulable := make(map[string]struct{}, 8)
	for _, tmpl := range provider.GetCommandTemplates() {
		if tmpl.Schedulable {
			schedulable[tmpl.ID] = struct{}{}
		}
	}
	return schedulable, nil
}

// ValidateCommandIntervals rejects any command id that is unknown or belongs
// to a non-schedulable (one-shot) template of the driver registered for
// devType. This is the single authority for invariant I-1 and is shared by
// the create path (POST /edge-devices) and the update path
// (PUT /edge-devices/:id/commands).
func ValidateCommandIntervals(driverRegistry *drivers.Registry, devType string, intervals map[string]int) error {
	schedulable, err := SchedulableCommandIDs(driverRegistry, devType)
	if err != nil {
		return err
	}
	for id := range intervals {
		if _, ok := schedulable[id]; !ok {
			return fmt.Errorf("command_intervals: command %q is not a schedulable command of driver %q", id, devType)
		}
	}
	return nil
}

// NormalizeCommandIntervals returns a copy of intervals with negative values
// clamped to 0 (0 = disabled). Pure function; does not validate ids.
func NormalizeCommandIntervals(intervals map[string]int) map[string]int {
	normalized := make(map[string]int, len(intervals))
	for id, interval := range intervals {
		if interval < 0 {
			interval = 0
		}
		normalized[id] = interval
	}
	return normalized
}

// validateAndNormalizeCommandIntervals is the create-path entry point: it
// validates then normalizes, returning the JSON payload to persist (nil when
// the input map is empty). Kept as a thin composition of the shared helpers
// so POST and PUT share one validation authority.
func validateAndNormalizeCommandIntervals(driverRegistry *drivers.Registry, devType string, intervals map[string]int) (json.RawMessage, error) {
	if len(intervals) == 0 {
		return nil, nil
	}
	if err := ValidateCommandIntervals(driverRegistry, devType, intervals); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(NormalizeCommandIntervals(intervals))
	if err != nil {
		return nil, fmt.Errorf("failed to marshal command_intervals: %w", err)
	}
	return raw, nil
}
// CountManifestCandidates returns how many of the driver's schedulable commands
// would actually be encoded as per-command polling sub-frames in the
// ConfigManifest, given the intervals that will be stored.
//
// It mirrors nodemgr.CommandIsManifestCandidate (the production encoder's
// predicate): a command counts only when it is schedulable AND its effective
// interval is > 0. This is the number the collector compares against
// MAX_COMMANDS_PER_DEVICE, so it is also the number the write path must check.
//
// A nil/empty intervals map means "no per-command overrides stored", in which
// case each command falls back to its own template default interval — matching
// the read path in GET /edge-devices/:id/commands.
func CountManifestCandidates(driverRegistry *drivers.Registry, devType string, intervals map[string]int, fallbackIntervalMs int) (int, error) {
	drv, err := driverRegistry.Get(devType)
	if err != nil {
		return 0, fmt.Errorf("cannot count manifest candidates: driver for type %q is not registered", devType)
	}
	provider, ok := drv.(drivers.CommandTemplateProvider)
	if !ok {
		return 0, fmt.Errorf("cannot count manifest candidates: driver for type %q provides no command templates", devType)
	}
	count := 0
	for _, tmpl := range provider.GetCommandTemplates() {
		if !tmpl.Schedulable {
			continue
		}
		effective := tmpl.IntervalMs
		if v, ok := intervals[tmpl.ID]; ok {
			effective = v
		} else if len(intervals) == 0 && fallbackIntervalMs > 0 {
			effective = fallbackIntervalMs
		}
		if effective > 0 {
			count++
		}
	}
	return count, nil
}

// ValidateManifestCommandCapacity rejects a command-interval set that would
// make the ConfigManifest undeliverable to the collector.
//
// Why this exists: the encoder refuses to publish a manifest whose edge device
// carries more than MaxCommandsPerEdgeDevice polling commands. Without this
// gate the bad configuration is accepted (HTTP 200), the failure only surfaces
// later as a rejected push, and the device silently keeps running the previous
// configuration. Rejecting at write time turns a silent, repeating failure into
// an immediate, actionable error.
func ValidateManifestCommandCapacity(driverRegistry *drivers.Registry, devType string, intervals map[string]int, fallbackIntervalMs int) error {
	count, err := CountManifestCandidates(driverRegistry, devType, intervals, fallbackIntervalMs)
	if err != nil {
		return err
	}
	if count > nodemgr.MaxCommandsPerEdgeDevice {
		return fmt.Errorf("command_intervals enables %d polling commands, but the collector supports at most %d per edge device; set the remaining commands to 0 (disabled)", count, nodemgr.MaxCommandsPerEdgeDevice)
	}
	return nil
}
