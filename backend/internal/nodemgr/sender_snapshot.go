package nodemgr

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/logger"
	"ehome/backend/pkg/metrics"

	"gorm.io/gorm"
)

// manifestSnapshot is the consistent read set used for both config hash
// computation and ConfigManifest encoding. It is loaded inside a single
// REPEATABLE READ transaction so the manifestID's hash input and the encoded
// byte stream come from the same snapshot — that is what makes the device's
// echoed config_hash ever match the server (F2: manifest 快照化 + hash 一致性).
type manifestSnapshot struct {
	node           models.Node
	templates      []models.ConfigTemplate
	channels       []models.Channel
	edgeDevices    []models.EdgeDevice
	deviceConfigs  []models.DeviceConfig
	dmaConfigs     []models.DmaChannelConfig
	gpioConfigs    []models.GPIOConfig
	pwmConfigs     []models.PWMConfig
	edgesByChannel map[uint][]models.EdgeDevice // enabled edges grouped by channel (single query)
}

// loadManifestSnapshot reads every entity that participates in hash calculation
// or manifest encoding, using the same query orders as buildHashData. When
// templates is nil it loads them; callers that reconciled inside the same
// transaction pass a nil slice to force a reload. edgeDevices is loaded once
// and grouped by channel, replacing the previous duplicate per-channel edge
// queries in the encoder and validateManifestScheduleCapacity.
func (m *Manager) loadManifestSnapshot(tx *gorm.DB, node models.Node, templates []models.ConfigTemplate) (*manifestSnapshot, error) {
	snap := &manifestSnapshot{
		node:           node,
		edgesByChannel: make(map[uint][]models.EdgeDevice),
	}
	if templates == nil {
		if err := tx.Order("id ASC").Where("node_id = ?", node.NodeID).Find(&snap.templates).Error; err != nil {
			return nil, fmt.Errorf("load templates: %w", err)
		}
	} else {
		snap.templates = templates
	}
	if err := tx.Order("id ASC").Where("node_id = ?", node.NodeID).Find(&snap.channels).Error; err != nil {
		return nil, fmt.Errorf("load channels: %w", err)
	}
	if err := tx.Order("id ASC").Where("node_id = ? AND enabled = true", node.NodeID).Find(&snap.edgeDevices).Error; err != nil {
		return nil, fmt.Errorf("load edge devices: %w", err)
	}
	deviceConfigIDs := make([]uint, 0, len(snap.edgeDevices))
	seenDeviceConfigIDs := make(map[uint]struct{}, len(snap.edgeDevices))
	for _, ed := range snap.edgeDevices {
		if ed.DeviceConfigID > 0 {
			if _, ok := seenDeviceConfigIDs[ed.DeviceConfigID]; !ok {
				seenDeviceConfigIDs[ed.DeviceConfigID] = struct{}{}
				deviceConfigIDs = append(deviceConfigIDs, ed.DeviceConfigID)
			}
		}
	}
	if len(deviceConfigIDs) > 0 {
		if err := tx.Order("id ASC").Where("id IN ?", deviceConfigIDs).Find(&snap.deviceConfigs).Error; err != nil {
			return nil, fmt.Errorf("load device configs: %w", err)
		}
	}
	snap.dmaConfigs = parseManifestDMAConfigs(node)
	if err := tx.Order("pin ASC").Where("node_id = ?", node.NodeID).Find(&snap.gpioConfigs).Error; err != nil {
		return nil, fmt.Errorf("load GPIO configs: %w", err)
	}
	if err := tx.Order("pin ASC").Where("node_id = ?", node.NodeID).Find(&snap.pwmConfigs).Error; err != nil {
		return nil, fmt.Errorf("load PWM configs: %w", err)
	}
	for _, ed := range snap.edgeDevices {
		snap.edgesByChannel[ed.ChannelID] = append(snap.edgesByChannel[ed.ChannelID], ed)
	}
	return snap, nil
}

// parseManifestDMAConfigs extracts dma_configs from node.Config JSON. Shared
// between hash calculation and manifest encoding so both operate on the same
// parsed slice.
func parseManifestDMAConfigs(node models.Node) []models.DmaChannelConfig {
	var dmaConfigs []models.DmaChannelConfig
	if node.Config == "" {
		return dmaConfigs
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal([]byte(node.Config), &cfg); err != nil {
		logger.Warnf("[%s] Failed to parse node.Config JSON in sender: %v", node.NodeID, err)
		return dmaConfigs
	}
	dc, ok := cfg["dma_configs"]
	if !ok {
		return dmaConfigs
	}
	dcJSON, err := json.Marshal(dc)
	if err != nil {
		return dmaConfigs
	}
	if err := json.Unmarshal(dcJSON, &dmaConfigs); err != nil {
		logger.Warnf("[%s] Failed to parse dma_configs for sender: %v", node.NodeID, err)
		return dmaConfigs
	}
	logger.Infof("[%s] Loaded %d dma_configs from node.Config", node.NodeID, len(dmaConfigs))
	return dmaConfigs
}

// calcHashFromSnapshot computes the deterministic config hash and manifest ID
// for a snapshot. This is the single source of truth for manifest identity:
// every ConfigManifest encoded from a snapshot derives its manifestID from
// here (when the decision does not already carry one), so the device-echoed
// hash always matches the server hash for an unchanged database.
func (m *Manager) calcHashFromSnapshot(snap *manifestSnapshot) ConfigHashResult {
	hashData := m.buildHashData(snap.templates, snap.channels, snap.edgeDevices, snap.deviceConfigs, snap.dmaConfigs, snap.gpioConfigs, snap.pwmConfigs)
	// v2.5: include log_stream config in hash so changes trigger manifest push
	hashData = append(hashData, []byte(fmt.Sprintf("ls:%v:%d:", snap.node.LogStreamEnabled, snap.node.LogStreamLevel))...)
	hash := m.hashMgr.CalcConfigHash(hashData)
	return ConfigHashResult{
		Hash:         hash,
		ManifestID:   fmt.Sprintf("v2-%s", hash),
		ChannelCount: len(snap.channels),
	}
}

// validateManifestScheduleCapacityFromSnapshot mirrors config_mgr's fixed
// arrays for the selected wire format, counting exactly what
// encodeConfigManifest will emit. It reads edge devices from the snapshot
// (single query) instead of issuing a per-channel DB query.
func validateManifestScheduleCapacityFromSnapshot(snap *manifestSnapshot, registry *drivers.Registry, channels []models.Channel, useV2 bool, limits manifestLimits) error {
	if len(channels) > limits.maxChannels {
		return fmt.Errorf("manifest has %d channels; collector limit is %d", len(channels), limits.maxChannels)
	}
	for _, channel := range channels {
		if !useV2 {
			count := 0
			for _, value := range strings.Split(channel.TemplateIDs, ",") {
				if strings.TrimSpace(value) != "" {
					count++
				}
			}
			if count > limits.maxTemplateIDs {
				return fmt.Errorf("channel %d has %d template ids; collector limit is %d", channel.ID, count, limits.maxTemplateIDs)
			}
			continue
		}

		edges := snap.edgesByChannel[channel.ID]
		if len(edges) > maxEdgeDevicesPerChannel {
			return fmt.Errorf("channel %d has %d edge devices; collector limit is %d", channel.ID, len(edges), maxEdgeDevicesPerChannel)
		}
		for _, edge := range edges {
			var drv drivers.Driver
			if registry != nil {
				drv, _ = registry.Get(edge.Type)
			}
			commandCount := 0 // F3: legacy single-command only counts when a valid template_id exists.
			driverCommands := getCommandTemplatesFromDriver(drv)
			if len(driverCommands) > 0 {
				intervals := make(map[string]int)
				if len(edge.CommandIntervals) > 0 {
					_ = json.Unmarshal(edge.CommandIntervals, &intervals)
				}
				for _, command := range driverCommands {
					if !command.Schedulable {
						continue
					}
					interval := command.IntervalMs
					if value, ok := intervals[command.ID]; ok {
						interval = value
					}
					if interval > 0 && findTemplateIDForCommand(snap.templates, command.WriteData) != 0 {
						commandCount++
					}
				}
			} else if findTemplateID(channel, edge) != 0 && manifestTemplateExists(snap.templates, findTemplateID(channel, edge)) {
				// Legacy single-command branch: encoded only when the channel's
				// template_ids resolves to a template that exists in this snapshot.
				commandCount = 1
			}
			if commandCount > maxCommandsPerEdgeDevice {
				return fmt.Errorf("edge device %d on channel %d has %d commands; collector limit is %d", edge.ID, channel.ID, commandCount, maxCommandsPerEdgeDevice)
			}
		}
	}
	return nil
}

// manifestTemplateExists reports whether a template with the given ID is
// present in the snapshot's template set. Used by the legacy single-command
// branch to refuse dangling template_id references (F3).
func manifestTemplateExists(templates []models.ConfigTemplate, id uint64) bool {
	for _, t := range templates {
		if uint64(t.ID) == id {
			return true
		}
	}
	return false
}

// encodeConfigManifest produces the ConfigManifest (0x04) wire bytes from a
// single consistent snapshot. manifestID is written verbatim into field 1, so
// the identifier SyncGate derived (or that was computed from this snapshot via
// calcHashFromSnapshot) and the encoded bytes are guaranteed same-source.
// ProtocolVersion >= 2.3 uses field 9 (edge_device_groups); older versions use
// field 3+4.
func encodeConfigManifest(snap *manifestSnapshot, channels []models.Channel, useV2 bool, decision SyncDecision, registry *drivers.Registry, manifestID string) ([]byte, error) {
	node := snap.node
	deviceID := decision.DeviceID

	enc := frame.NewEncoder(frame.MsgConfigMfst)
	enc.EncodeString(1, manifestID)

	// v2.2: field 2 epoch removed, field 9 sync_reason removed

	// Encode templates (field 3, repeated sub-structure)
	// v2 path still needs templates — C6's schedule_v2_channel uses
	// config_mgr_get_template(template_id) to look up write_data/read_length/delay_ms.
	for _, tmpl := range snap.templates {
		subEnc := frame.SubEncoder()
		subEnc.EncodeVarint(1, uint64(tmpl.ID))
		if tmpl.WriteData != "" {
			writeHex := tmpl.WriteData
			if strings.HasPrefix(writeHex, `\x`) || strings.HasPrefix(writeHex, "0x") {
				writeHex = writeHex[2:]
			}
			if writeBytes, err := hex.DecodeString(writeHex); err == nil && len(writeBytes) > 0 {
				subEnc.EncodeBytes(2, writeBytes)
			}
		}
		if tmpl.ReadLength > 0 {
			subEnc.EncodeVarint(3, uint64(tmpl.ReadLength))
		}
		if tmpl.DelayMs > 0 {
			subEnc.EncodeVarint(4, uint64(tmpl.DelayMs))
		}
		enc.EncodeSubFrame(3, subEnc.Bytes())
	}

	// Encode channels (field 4, repeated sub-structure)
	for _, ch := range channels {
		subEnc := frame.SubEncoder()
		subEnc.EncodeVarint(1, uint64(ch.ID))

		if !useV2 {
			// Old path: field 2 hardware_id, field 3 template_ids, field 4 interval_ms
			subEnc.EncodeString(2, ch.HardwareID)

			// Packed repeated template_ids (field 3)
			if ch.TemplateIDs != "" {
				for _, idStr := range strings.Split(ch.TemplateIDs, ",") {
					if id, err := strconv.ParseUint(strings.TrimSpace(idStr), 10, 32); err == nil {
						subEnc.EncodeVarint(3, id)
					}
				}
			}

			subEnc.EncodeVarint(4, uint64(ch.IntervalMs))
		}

		subEnc.EncodeBool(5, ch.Enabled)

		// Bus type
		busTypeMap := map[string]uint8{
			"UART": 1, "1": 1,
			"I2C": 2, "2": 2,
			"SPI": 3, "3": 3,
			"ADC": 5, "5": 5,
		}
		if bt, ok := busTypeMap[strings.ToUpper(ch.BusType)]; ok {
			subEnc.EncodeVarint(6, uint64(bt))
		}

		// Bus config
		busConfigData := ch.BusConfig
		if busConfigData == "" {
			busConfigData = ch.Config
		}
		if busConfigData != "" {
			/* Try hex decode first — PostgreSQL bytea may already be binary in-memory */
			if decoded, err := hex.DecodeString(busConfigData); err == nil && len(decoded) > 0 {
				subEnc.EncodeBytes(7, decoded)
			} else if strings.HasPrefix(busConfigData, `\x`) {
				hexStr := busConfigData[2:]
				if decoded, err := hex.DecodeString(hexStr); err == nil && len(decoded) > 0 {
					subEnc.EncodeBytes(7, decoded)
				} else {
					subEnc.EncodeString(7, busConfigData)
				}
			} else {
				subEnc.EncodeString(7, busConfigData)
			}
		}

		// Field 8: dma_enabled
		subEnc.EncodeBool(8, ch.DmaEnabled)

		if useV2 {
			// New path: field 9 edge_device_groups (repeated sub-messages).
			// Edges come from the shared snapshot — single query, no per-channel re-read.
			edges := snap.edgesByChannel[ch.ID]
			logger.Infof("[%s] ConfigManifest ch=%d: useV2=true, found %d edge_devices", deviceID, ch.ID, len(edges))

			for _, edge := range edges {
				grpEnc := frame.SubEncoder()
				grpEnc.EncodeVarint(1, uint64(edge.ID))
				grpEnc.EncodeVarint(2, parseHardwareID(edge.HardwareID))

				// Per-command intervals: check driver CommandTemplates for multi-command support
				cmdIntervals := make(map[string]int)
				if len(edge.CommandIntervals) > 0 {
					json.Unmarshal(edge.CommandIntervals, &cmdIntervals)
				}
				var drv drivers.Driver
				if registry != nil {
					drv, _ = registry.Get(edge.Type)
				}
				driverCmds := getCommandTemplatesFromDriver(drv)

				if len(driverCmds) > 0 {
					// Multiple commands: encode only Schedulable commands
					for _, t := range driverCmds {
						if !t.Schedulable {
							continue // one-shot trigger, not for ConfigManifest
						}
						interval := t.IntervalMs
						if v, ok := cmdIntervals[t.ID]; ok {
							interval = v
						}
						if interval <= 0 {
							continue // disabled
						}
						tmplID := findTemplateIDForCommand(snap.templates, t.WriteData)
						if tmplID == 0 {
							logger.Warnf("[%s] No ConfigTemplate found for command %s, skipping",
								deviceID, t.ID)
							continue
						}
						cmdEnc := frame.SubEncoder()
						cmdEnc.EncodeVarint(1, tmplID)
						cmdEnc.EncodeVarint(2, uint64(interval))
						cmdEnc.EncodeBool(3, true)
						grpEnc.EncodeSubFrame(3, cmdEnc.Bytes())
					}
				} else {
					// Single command: use edge default interval
					interval := edge.IntervalMs
					if v, ok := cmdIntervals["default"]; ok {
						interval = v
					}
					tmplID := findTemplateID(ch, edge)
					if tmplID == 0 || !manifestTemplateExists(snap.templates, tmplID) {
						// F3: no usable template_id (channel.template_ids empty,
						// dangling, or unparseable). Never encode varint 0 (wire-legal
						// but meaningless on the device) nor a dangling reference —
						// skip this edge's command sub-frame. The edge group below is
						// still encoded via EncodeSubFrame(9) with zero commands; the
						// firmware parses an empty command group (command_count=0) and
						// schedules nothing for it, so the device stops sampling this
						// edge until the channel's template_ids are repaired.
						logger.Warnf("[%s] edge_device %d (channel %d): no valid template_id in channel.template_ids %q (resolved %d), skipping command encoding", deviceID, edge.ID, ch.ID, ch.TemplateIDs, tmplID)
						metrics.ManifestCommandSkippedNoTemplate.WithLabelValues(deviceID).Inc()
					} else {
						cmdEnc := frame.SubEncoder()
						cmdEnc.EncodeVarint(1, tmplID)
						cmdEnc.EncodeVarint(2, uint64(interval))
						cmdEnc.EncodeBool(3, edge.Enabled)
						grpEnc.EncodeSubFrame(3, cmdEnc.Bytes())
					}
				}

				subEnc.EncodeSubFrame(9, grpEnc.Bytes())
			}
		}

		enc.EncodeSubFrame(4, subEnc.Bytes())
	}

	// Field 5: dma_channel_configs (repeated DmaChannelConfig sub-messages)
	for _, dc := range snap.dmaConfigs {
		subEnc := frame.SubEncoder()
		subEnc.EncodeVarint(1, uint64(dc.DmaID))
		enabled := uint64(0)
		if dc.Enabled {
			enabled = 1
		}
		subEnc.EncodeVarint(2, enabled)
		if dc.BindTo != "" {
			subEnc.EncodeString(3, dc.BindTo)
		}
		enc.EncodeSubFrame(5, subEnc.Bytes())
	}

	// v2.5: field 10 = log_stream config (sub-frame: 1=enabled, 2=level)
	{
		lsEnc := frame.SubEncoder()
		lsEnc.EncodeBool(1, node.LogStreamEnabled)
		lsEnc.EncodeVarint(2, uint64(node.LogStreamLevel))
		enc.EncodeSubFrame(10, lsEnc.Bytes())
	}

	// Field 11: gpio_configs (repeated sub-messages, v3.0)
	// Only encode for protocol_version >= 2.4 (older firmware ignores unknown fields)
	if protocolVersionAtLeast(node.ProtocolVersion, protocolVersion{major: 2, minor: 4}) {
		for _, gc := range snap.gpioConfigs {
			if !gc.Enabled {
				continue // hash input includes disabled rows; the wire only carries enabled ones
			}
			subEnc := frame.SubEncoder()
			subEnc.EncodeVarint(1, uint64(gc.Pin))          // sub-field 1: pin
			subEnc.EncodeVarint(2, uint64(gc.Direction))    // sub-field 2: direction
			subEnc.EncodeVarint(3, uint64(gc.InitialLevel)) // sub-field 3: initial_level
			enc.EncodeSubFrame(11, subEnc.Bytes())
		}

		// Field 12: pwm_configs (repeated sub-messages, v3.0)
		pwmForEncode := make([]models.PWMConfig, 0, len(snap.pwmConfigs))
		for _, pc := range snap.pwmConfigs {
			if pc.Enabled {
				pwmForEncode = append(pwmForEncode, pc)
			}
		}
		// Preserve the historical deterministic encode order: channel ASC, hardware_id ASC.
		sort.Slice(pwmForEncode, func(i, j int) bool {
			if pwmForEncode[i].Channel != pwmForEncode[j].Channel {
				return pwmForEncode[i].Channel < pwmForEncode[j].Channel
			}
			return pwmForEncode[i].HardwareID < pwmForEncode[j].HardwareID
		})
		for _, pc := range pwmForEncode {
			subEnc := frame.SubEncoder()
			subEnc.EncodeVarint(1, uint64(pc.Channel))    // sub-field 1: channel
			subEnc.EncodeVarint(2, uint64(pc.Pin))        // sub-field 2: pin
			subEnc.EncodeVarint(3, uint64(pc.Frequency))  // sub-field 3: frequency
			subEnc.EncodeVarint(4, uint64(pc.Duty))       // sub-field 4: duty
			subEnc.EncodeVarint(5, uint64(pc.Resolution)) // sub-field 5: resolution
			subEnc.EncodeBool(6, pc.AutoStart)            // sub-field 6: auto_start
			enc.EncodeSubFrame(12, subEnc.Bytes())
		}
	}

	// v2.2: field 8 = sync_id (field 9 sync_reason removed)
	enc.EncodeString(8, decision.SyncID)

	payload := enc.Bytes()

	// DEBUG: Log first 120 bytes of ConfigManifest hex
	hexLen := len(payload)
	if hexLen > 120 {
		hexLen = 120
	}
	logger.Infof("[%s] ConfigManifest hex (%d bytes): %s", deviceID, len(payload), hex.EncodeToString(payload[:hexLen]))
	return payload, nil
}
