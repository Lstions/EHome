package nodemgr

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
	"ehome/backend/pkg/logger"

	"gorm.io/gorm"
)

// manifestCapabilities is the subset of a node's ResourceReport that governs
// which GPIO pins / PWM resources may appear in a ConfigManifest. Binding is
// fail-closed: any GPIO/PWM config whose resource is absent from the report
// rejects the whole manifest.
type manifestCapabilities struct {
	Buses struct {
		GPIO []struct {
			Pin int `json:"pin"`
		} `json:"gpio"`
		PWM []struct {
			ID                string `json:"id"`
			Channel           uint8  `json:"channel"`
			MaxResolutionBits uint8  `json:"max_resolution_bits"`
		} `json:"pwm"`
	} `json:"buses"`
}

// normalizedManifestBusType maps a channel bus type (name or legacy numeric)
// to the canonical wire name. The third return value reports whether the type
// is a peripheral (GPIO/PWM) — peripheral channels are not encoded as
// transport channels and their legacy enabled form is a hard error.
func normalizedManifestBusType(value string) (string, bool, bool) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "UART", "1":
		return "UART", true, false
	case "I2C", "2":
		return "I2C", true, false
	case "SPI", "3":
		return "SPI", true, false
	case "GPIO", "4":
		return "GPIO", true, true
	case "ADC", "5":
		return "ADC", true, false
	case "PWM", "6":
		return "PWM", true, true
	default:
		return "", false, false
	}
}

// decodeManifestTransportPins extracts the GPIO pins occupied by a transport
// channel from its hex bus_config, so authority checks can detect conflicts
// between channels, GPIO configs and PWM routing.
func decodeManifestTransportPins(ch models.Channel, busType string) ([]int, error) {
	raw := strings.TrimSpace(ch.BusConfig)
	raw = strings.TrimPrefix(raw, `\x`)
	data, err := hex.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("enabled channel %d has malformed bus_config", ch.ID)
	}
	switch busType {
	case "UART", "I2C":
		if len(data) < 2 {
			return nil, fmt.Errorf("enabled channel %d has malformed bus_config", ch.ID)
		}
		return []int{int(data[0]), int(data[1])}, nil
	case "SPI":
		if len(data) != 9 {
			return nil, fmt.Errorf("enabled channel %d has malformed bus_config", ch.ID)
		}
		pins := []int{int(data[0])}
		if len(data) >= 9 {
			pins = append(pins, int(data[6]), int(data[7]), int(data[8]))
		}
		return pins, nil
	case "ADC":
		return nil, nil
	default:
		return nil, fmt.Errorf("enabled channel %d has unsupported bus type %q", ch.ID, ch.BusType)
	}
}

// validateManifestAuthority verifies that every transport channel, GPIO and
// PWM config that will be encoded is backed by the node's current
// ResourceReport, and that no two encoded resources claim the same GPIO pin.
// Returns the channels that are legal to encode (peripheral/disabled channels
// filtered out).
func validateManifestAuthority(node models.Node, allChannels []models.Channel, gpios []models.GPIOConfig, pwms []models.PWMConfig) ([]models.Channel, error) {
	var caps manifestCapabilities
	if strings.TrimSpace(node.Capabilities) == "" || json.Unmarshal([]byte(node.Capabilities), &caps) != nil {
		return nil, fmt.Errorf("node has not reported usable hardware resources")
	}
	gpioPins := make(map[int]bool, len(caps.Buses.GPIO))
	for _, resource := range caps.Buses.GPIO {
		gpioPins[resource.Pin] = true
	}
	pwmResources := make(map[string]struct{ channel, max uint8 }, len(caps.Buses.PWM))
	for _, resource := range caps.Buses.PWM {
		pwmResources[resource.ID] = struct{ channel, max uint8 }{resource.Channel, resource.MaxResolutionBits}
	}
	owners := make(map[int]string)
	claim := func(pin int, owner string) error {
		if prior, exists := owners[pin]; exists {
			return fmt.Errorf("GPIO pin %d conflict between %s and %s", pin, prior, owner)
		}
		owners[pin] = owner
		return nil
	}
	channels := make([]models.Channel, 0, len(allChannels))
	for _, ch := range allChannels {
		busType, known, peripheral := normalizedManifestBusType(ch.BusType)
		if !known {
			busType, known, peripheral = normalizedManifestBusType(ch.HardwareType)
		}
		if peripheral {
			if ch.Enabled {
				return nil, fmt.Errorf("legacy peripheral channel %d is still enabled", ch.ID)
			}
			continue
		}
		if !known {
			if ch.Enabled {
				return nil, fmt.Errorf("enabled channel %d has unsupported bus type %q", ch.ID, ch.BusType)
			}
			channels = append(channels, ch)
			continue
		}
		if ch.Enabled {
			pins, err := decodeManifestTransportPins(ch, busType)
			if err != nil {
				return nil, err
			}
			for _, pin := range pins {
				if err := claim(pin, fmt.Sprintf("channel %d", ch.ID)); err != nil {
					return nil, err
				}
			}
		}
		channels = append(channels, ch)
	}
	for _, cfg := range gpios {
		if !cfg.Enabled {
			continue // disabled GPIOs are not encoded into the manifest; hash input includes them but authority only governs the wire
		}
		if !gpioPins[cfg.Pin] {
			return nil, fmt.Errorf("GPIO pin %d is absent from current ResourceReport", cfg.Pin)
		}
		if cfg.Direction > 3 || cfg.InitialLevel > 1 {
			return nil, fmt.Errorf("GPIO pin %d has invalid scalar configuration", cfg.Pin)
		}
		if err := claim(cfg.Pin, fmt.Sprintf("GPIO %d", cfg.Pin)); err != nil {
			return nil, err
		}
	}
	for _, cfg := range pwms {
		if !cfg.Enabled {
			continue // disabled PWM configs are not encoded into the manifest; authority only governs the wire
		}
		resource, ok := pwmResources[cfg.HardwareID]
		if !ok || resource.channel != cfg.Channel {
			return nil, fmt.Errorf("PWM resource %q no longer matches current ResourceReport", cfg.HardwareID)
		}
		if !gpioPins[cfg.Pin] {
			return nil, fmt.Errorf("PWM %s route GPIO %d is absent from current ResourceReport", cfg.HardwareID, cfg.Pin)
		}
		if cfg.Duty > 10000 || cfg.Frequency == 0 || cfg.Resolution < 4 || cfg.Resolution > 20 ||
			resource.max == 0 || cfg.Resolution > resource.max || uint64(cfg.Frequency)*(uint64(1)<<cfg.Resolution) > 40000000 {
			return nil, fmt.Errorf("PWM %s has infeasible scalar configuration", cfg.HardwareID)
		}
		if err := claim(cfg.Pin, fmt.Sprintf("PWM %s", cfg.HardwareID)); err != nil {
			return nil, err
		}
	}
	return channels, nil
}

// validateManifestTemplateCapacity mirrors config_mgr's fixed template array on
// the collector. The server must not publish a manifest it already knows the
// collector cannot apply.
func validateManifestTemplateCapacity(templates []models.ConfigTemplate, maxTemplates int) error {
	if len(templates) > maxTemplates {
		return fmt.Errorf("manifest has %d templates; collector limit is %d", len(templates), maxTemplates)
	}
	return nil
}

// reconcileDriverTemplates ensures every driver CommandTemplate has a matching
// ConfigTemplate in DB. Auto-creates missing templates for self-healing.
// Capacity is checked before any mutation, so self-healing cannot manufacture a
// manifest the collector will reject.
//
// F2 fail-closed: runs inside the SendConfigManifestWithDecision transaction.
// A single template Create failure now returns an error (aborting the whole
// transaction, leaving no orphan templates) instead of the old warn+continue.
func reconcileDriverTemplates(db *gorm.DB, driverRegistry *drivers.Registry, nodeID string, existingTemplates []models.ConfigTemplate, maxTemplates int) (bool, error) {
	if driverRegistry == nil {
		return false, nil
	}
	// Collect all edge devices for this node and their driver commands
	type cmdNeed struct {
		chID       uint
		writeData  string
		readLength uint32
		delayMs    uint32
	}
	needed := make(map[string]cmdNeed) // key = normalized write_data

	var edges []models.EdgeDevice
	db.Where("node_id = ? AND enabled = true", nodeID).Find(&edges)
	for _, edge := range edges {
		drv, err := driverRegistry.Get(edge.Type)
		if err != nil {
			continue
		}
		provider, ok := drv.(drivers.CommandTemplateProvider)
		if !ok {
			continue
		}
		for _, cmd := range provider.GetCommandTemplates() {
			if !cmd.Schedulable || cmd.WriteData == "" {
				continue
			}
			key := strings.ToUpper(strings.TrimSpace(cmd.WriteData))
			needed[key] = cmdNeed{
				chID:       edge.ChannelID,
				writeData:  cmd.WriteData,
				readLength: cmd.ReadLength,
				delayMs:    cmd.DelayMs,
			}
		}
	}

	// Check which needed templates already exist
	existingKeys := make(map[string]bool)
	for _, t := range existingTemplates {
		existingKeys[strings.ToUpper(strings.TrimSpace(t.WriteData))] = true
	}

	missing := 0
	for key := range needed {
		if !existingKeys[key] {
			missing++
		}
	}
	if len(existingTemplates)+missing > maxTemplates {
		return false, fmt.Errorf("template reconciliation would create %d templates; collector limit is %d", len(existingTemplates)+missing, maxTemplates)
	}

	// Create missing templates after capacity preflight succeeds.
	created := false
	for key, need := range needed {
		if existingKeys[key] {
			continue
		}
		tmpl := models.ConfigTemplate{
			NodeID:     nodeID,
			WriteData:  need.writeData,
			ReadLength: need.readLength,
			DelayMs:    need.delayMs,
		}
		if err := db.Create(&tmpl).Error; err != nil {
			// F2 fail-closed: a failed Create aborts the whole transaction.
			return created, fmt.Errorf("auto-create ConfigTemplate (tx_hex_chars=%d): %w", len(need.writeData), err)
		}
		// Append template ID to channel's template_ids
		newID := strconv.FormatUint(uint64(tmpl.ID), 10)
		if err := db.Model(&models.Channel{}).Where("id = ?", need.chID).Update("template_ids",
			gorm.Expr("CASE WHEN template_ids = '' OR template_ids IS NULL THEN ? ELSE template_ids || ',' || ? END", newID, newID)).Error; err != nil {
			return created, fmt.Errorf("append template_id %d to channel %d: %w", tmpl.ID, need.chID, err)
		}
		logger.Infof("[reconcile] Auto-created ConfigTemplate id=%d tx_hex_chars=%d for channel=%d",
			tmpl.ID, len(need.writeData), need.chID)
		created = true
	}
	return created, nil
}

// getCommandTemplatesFromDriver returns command templates from a driver, or nil.
func getCommandTemplatesFromDriver(drv drivers.Driver) []drivers.CommandTemplate {
	if drv == nil {
		return nil
	}
	if provider, ok := drv.(drivers.CommandTemplateProvider); ok {
		return provider.GetCommandTemplates()
	}
	return nil
}

// findTemplateIDForCommand finds a ConfigTemplate ID that matches the given write_data hex.
// Returns 0 if no matching template is found — the caller should skip that command.
func findTemplateIDForCommand(templates []models.ConfigTemplate, writeData string) uint64 {
	normalized := strings.ToUpper(strings.TrimSpace(writeData))
	if normalized == "" {
		return 0
	}
	for _, t := range templates {
		tWrite := strings.ToUpper(strings.TrimSpace(t.WriteData))
		if tWrite == normalized {
			return uint64(t.ID)
		}
	}
	return 0 // no match — caller must skip this command
}

// findTemplateID returns the first template ID from Channel.TemplateIDs for
// the given channel and edge device. Returns 0 when the channel carries no
// template_ids or none of them parse — the caller must skip encoding rather
// than fall back to a magic template (F3: no more silent fallback=1).
func findTemplateID(ch models.Channel, edge models.EdgeDevice) uint64 {
	if ch.TemplateIDs != "" {
		for _, idStr := range strings.Split(ch.TemplateIDs, ",") {
			if id, err := strconv.ParseUint(strings.TrimSpace(idStr), 10, 32); err == nil {
				return id
			}
		}
	}
	return 0
}
