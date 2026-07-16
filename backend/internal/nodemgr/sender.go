package nodemgr

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/events"
	"ehome/backend/internal/models"
	"ehome/backend/internal/mqtt"
	"ehome/backend/internal/redis"
	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/logger"

	"gorm.io/gorm"
)

// SendPeriphCmd sends a peripheral control command (GPIO/PWM) to a device.
// Uses QoS 1. ESP-MQTT subscribes at QoS 1 and does not complete the QoS 2
// handshake for inbound commands, which leaves broker inflight deliveries stuck.
// Request IDs provide command correlation; peripheral actions are idempotent.
// resourceID is a GPIO pin for GPIO commands and a reported PWM channel for PWM commands.
func (m *Manager) SendPeriphCmd(deviceID string, periphType uint8, resourceID uint8,
	action uint8, value uint32, config []byte) error {
	_, err := m.SendPeriphCmdWithID(deviceID, periphType, resourceID, action, value, config)
	return err
}

func (m *Manager) SendPeriphCmdWithID(deviceID string, periphType uint8, resourceID uint8,
	action uint8, value uint32, config []byte) (uint32, error) {
	return m.sendPeriphCmdWithPreviousValue(deviceID, periphType, resourceID, action, value, config, 0)
}

func (m *Manager) SendPeriphCmdWithPreviousValue(deviceID string, periphType uint8, resourceID uint8,
	action uint8, value uint32, config []byte, previousValue uint32) (uint32, error) {
	return m.sendPeriphCmdWithPreviousValue(deviceID, periphType, resourceID, action, value, config, previousValue)
}

func (m *Manager) sendPeriphCmdWithPreviousValue(deviceID string, periphType uint8, resourceID uint8,
	action uint8, value uint32, config []byte, previousValue uint32) (uint32, error) {
	if atomic.LoadUint32(&nextPeriphRequestID) == 0 {
		var seed [4]byte
		if _, err := rand.Read(seed[:]); err == nil {
			atomic.CompareAndSwapUint32(&nextPeriphRequestID, 0, uint32(seed[0])<<24|uint32(seed[1])<<16|uint32(seed[2])<<8|uint32(seed[3]))
		}
	}
	requestID := atomic.AddUint32(&nextPeriphRequestID, 1)
	meta := periphRequestMeta{deviceID: deviceID, periphType: periphType, resourceID: resourceID, action: action, created: time.Now(), previousValue: previousValue, provisionalValue: value}
	if periphType == 2 && m.db != nil {
		var cfg models.PWMConfig
		if err := m.db.Where("node_id = ? AND channel = ?", deviceID, resourceID).First(&cfg).Error; err == nil {
			meta.hardwareID, meta.pin = cfg.HardwareID, cfg.Pin
		}
	}
	m.periphMu.Lock()
	if m.periphPending == nil {
		m.periphPending = make(map[uint32]periphRequestMeta)
	}
	if m.periphLatest == nil {
		m.periphLatest = make(map[string]uint32)
	}
	latestKey := fmt.Sprintf("%s:%d:%d:%d", deviceID, periphType, resourceID, action)
	previousLatest, hadPreviousLatest := m.periphLatest[latestKey]
	if previousMeta, ok := m.periphPending[previousLatest]; ok && periphType == 2 && (action == 0 || action == 1) && previousMeta.action == action {
		meta.previousValue = previousMeta.previousValue
	}
	m.periphPending[requestID] = meta
	m.periphLatest[latestKey] = requestID
	for id, meta := range m.periphPending {
		if time.Since(meta.created) > time.Minute {
			delete(m.periphPending, id)
			key := fmt.Sprintf("%s:%d:%d:%d", meta.deviceID, meta.periphType, meta.resourceID, meta.action)
			if m.periphLatest[key] == id {
				delete(m.periphLatest, key)
			}
		}
	}
	enc := frame.NewEncoder(frame.MsgPeriphCmd)
	enc.EncodeVarint(1, uint64(requestID))  // field 1: request_id
	enc.EncodeVarint(2, uint64(periphType)) // field 2: periph_type
	enc.EncodeVarint(3, uint64(resourceID)) // field 3: resource_id
	enc.EncodeVarint(4, uint64(action))     // field 4: action
	if value > 0 {
		enc.EncodeVarint(5, uint64(value)) // field 5: value (optional)
	}
	if len(config) > 0 {
		enc.EncodeBytes(6, config) // field 6: config (optional)
	}

	logger.Infof("[%s] SendPeriphCmd: type=%d resource_id=%d action=%d value=%d config_len=%d reqID=%d",
		deviceID, periphType, resourceID, action, value, len(config), requestID)

	topic := mqtt.ControlTopicForNode(deviceID)
	if m.mqtt == nil {
		delete(m.periphPending, requestID)
		if m.periphLatest[latestKey] == requestID {
			if hadPreviousLatest {
				if _, stillPending := m.periphPending[previousLatest]; stillPending {
					m.periphLatest[latestKey] = previousLatest
				} else {
					delete(m.periphLatest, latestKey)
				}
			} else {
				delete(m.periphLatest, latestKey)
			}
		}
		m.periphMu.Unlock()
		return 0, fmt.Errorf("MQTT client not connected")
	}
	if err := m.mqtt.Publish(topic, enc.Bytes()); err != nil {
		delete(m.periphPending, requestID)
		if m.periphLatest[latestKey] == requestID {
			if hadPreviousLatest {
				if _, stillPending := m.periphPending[previousLatest]; stillPending {
					m.periphLatest[latestKey] = previousLatest
				} else {
					delete(m.periphLatest, latestKey)
				}
			} else {
				delete(m.periphLatest, latestKey)
			}
		}
		m.periphMu.Unlock()
		return 0, err
	}
	m.periphMu.Unlock()
	return requestID, nil
}

// nextPeriphRequestID is the atomic counter for PeriphCmd request IDs.
var nextPeriphRequestID uint32

// SendPing sends a Ping message to a device and records timestamp in Redis for verification
// F7.6: Track the ping for retry on timeout
func (m *Manager) SendPing(deviceID string) error {
	ts := time.Now().UnixMicro()
	enc := frame.NewEncoder(frame.MsgPing)
	enc.EncodeVarint(1, uint64(ts))

	// Store ping timestamp in Redis for anti-forgery verification (TTL=30s)
	if redis.Client != nil {
		redis.Client.Set(context.Background(), fmt.Sprintf("ping:%s", deviceID), ts, 30*time.Second)
	}

	// F7.6: Register pending ping for retry/timeout
	if m.pingTracker != nil {
		m.pingTracker.Track(deviceID, ts, func(latencyMs int64, success bool) {
			if !success {
				logger.Warnf("[%s] Ping failed after %d retries (timeout)", deviceID, m.pingTracker.maxRetry)
				m.wsHub.BroadcastEvent(events.PingResult, map[string]interface{}{
					"device_id":  deviceID,
					"node_id":    deviceID, // v2.2 新增
					"latency_ms": -1,
					"timestamp":  time.Now().Unix(),
					"verified":   false,
					"reason":     "timeout",
				})
			}
		})
	}

	topic := mqtt.TopicForNode(deviceID)
	return m.mqtt.Publish(topic, enc.Bytes())
}

// SendWriteCommand sends a WriteCommand to a device
// P3-5: Uses QoS 2 (exactly-once) for critical write operations
func (m *Manager) SendWriteCommand(deviceID string, channelID uint32, data []byte, readSize uint32) error {
	// Record TX in terminal
	m.termMgr.RecordTX(deviceID, uint(channelID), data)

	logger.Infof("[sender] SendWriteCommand: device=%s ch=%d data_hex=%x readSize=%d", deviceID, channelID, data, readSize)

	enc := frame.NewEncoder(frame.MsgWriteCmd)
	enc.EncodeVarint(1, uint64(time.Now().UnixNano())) // request_id
	enc.EncodeVarint(2, uint64(channelID))
	enc.EncodeBytes(3, data)
	if readSize > 0 {
		enc.EncodeVarint(4, uint64(readSize))
	}

	topic := mqtt.ControlTopicForNode(deviceID)
	return m.mqtt.PublishQoS2(topic, enc.Bytes())
}

// SendScanRequest sends a ScanRequest to a device (I2C mode)
func (m *Manager) SendScanRequest(deviceID string, hardwareID uint32) error {
	enc := frame.NewEncoder(frame.MsgScanReq)
	enc.EncodeString(1, fmt.Sprintf("scan-%d", time.Now().Unix()))
	enc.EncodeVarint(2, uint64(hardwareID))

	topic := mqtt.TopicForNode(deviceID)
	return m.mqtt.Publish(topic, enc.Bytes())
}

// SendModbusScanRequest sends a ScanRequest to a device (Modbus mode)
// field 1: request_id (string)
// field 3: scan_type = 2 (MODBUS)
// field 4: start_addr
// field 5: end_addr
// field 6: timeout_ms (per-address timeout)
func (m *Manager) SendModbusScanRequest(deviceID string, startAddr, endAddr, timeoutMs int) (string, error) {
	requestID := fmt.Sprintf("scan-%d", time.Now().UnixMilli())

	enc := frame.NewEncoder(frame.MsgScanReq)
	enc.EncodeString(1, requestID)
	enc.EncodeVarint(3, 2) // scan_type = MODBUS

	if startAddr > 0 {
		enc.EncodeVarint(4, uint64(startAddr))
	} else {
		enc.EncodeVarint(4, 1) // default start from 1
	}
	if endAddr > 0 {
		enc.EncodeVarint(5, uint64(endAddr))
	} else {
		enc.EncodeVarint(5, 247) // default end at 247
	}
	if timeoutMs > 0 {
		enc.EncodeVarint(6, uint64(timeoutMs))
	} else {
		enc.EncodeVarint(6, 200) // default 200ms
	}

	topic := mqtt.TopicForNode(deviceID)
	if err := m.mqtt.Publish(topic, enc.Bytes()); err != nil {
		return "", err
	}
	return requestID, nil
}

// SendQueryRequest sends a QueryReq (type=0x0E) to a device
func (m *Manager) SendQueryRequest(deviceID string, queryType uint32) error {
	enc := frame.NewEncoder(frame.MsgQueryReq)
	enc.EncodeString(1, fmt.Sprintf("query-%d", time.Now().UnixMilli()))
	enc.EncodeVarint(2, uint64(queryType))

	topic := mqtt.TopicForNode(deviceID)
	return m.mqtt.Publish(topic, enc.Bytes())
}

// SendHelloAck sends a HelloAck message to a device (0x12, SVR→ESP)
func (m *Manager) SendHelloAck(deviceID string, serverTime uint64, features uint32) error {
	enc := frame.NewEncoder(frame.MsgHelloAck)
	enc.EncodeVarint(1, serverTime)
	enc.EncodeVarint(2, uint64(features))

	topic := mqtt.TopicForNode(deviceID)
	return m.mqtt.Publish(topic, enc.Bytes())
}

// SendConfigQuery sends a ConfigQuery (type=0x10) to a device
func (m *Manager) SendConfigQuery(deviceID string) error {
	enc := frame.NewEncoder(frame.MsgConfigQuery)
	enc.EncodeString(1, fmt.Sprintf("cfgq-%d", time.Now().UnixMilli()))

	topic := mqtt.TopicForNode(deviceID)
	return m.mqtt.Publish(topic, enc.Bytes())
}

// SendQueryResources sends a QueryResources (0x1A) to a device, requesting it to send a ResourceReport.
// Returns the request_id for correlation.
func (m *Manager) SendQueryResources(deviceID string) (string, error) {
	requestID := fmt.Sprintf("res-%d", time.Now().UnixMilli())

	enc := frame.NewEncoder(frame.MsgQueryResources)
	enc.EncodeString(1, requestID)

	topic := mqtt.TopicForNode(deviceID)
	if err := m.mqtt.Publish(topic, enc.Bytes()); err != nil {
		return "", fmt.Errorf("failed to publish QueryResources: %w", err)
	}

	logger.Infof("[%s] QueryResources sent: request_id=%s", deviceID, requestID)
	return requestID, nil
}

// ServerMaxProtocolVersion is the highest protocol version this server supports.
// Negotiated version = min(device-reported, ServerMaxProtocolVersion).
const ServerMaxProtocolVersion = "2.5"

// parseProtocolVersion parses a version string like "2.2" or "2.3" into a float64.
// Returns 0 for empty or unparseable strings.
func parseProtocolVersion(v string) float64 {
	parts := strings.Split(v, ".")
	if len(parts) >= 2 {
		major, _ := strconv.ParseFloat(parts[0]+"."+parts[1], 64)
		return major
	}
	return 0
}

// parseHardwareID converts a hardware ID string to uint64.
// "0x76" → 118, "5" → 5, "" → 0
func parseHardwareID(s string) uint64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if strings.HasPrefix(strings.ToLower(s), "0x") {
		v, err := strconv.ParseUint(s[2:], 16, 64)
		if err == nil {
			return v
		}
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err == nil {
		return v
	}
	return 0
}

// findTemplateID returns a template ID for the given channel and edge device.
// Uses the first template_id from Channel.TemplateIDs; falls back to 1.
func findTemplateID(ch models.Channel, edge models.EdgeDevice) uint64 {
	if ch.TemplateIDs != "" {
		for _, idStr := range strings.Split(ch.TemplateIDs, ",") {
			if id, err := strconv.ParseUint(strings.TrimSpace(idStr), 10, 32); err == nil {
				return id
			}
		}
	}
	return 1
}

// SendConfigManifestWithDecision sends a ConfigManifest (0x04) with v2.1 sync metadata.
// The decision carries sync_id, epoch, manifest_id, and reason from SyncGate.
// ProtocolVersion >= 2.3 uses field 9 (edge_device_groups); older versions use field 3+4.
func (m *Manager) SendConfigManifestWithDecision(decision SyncDecision) error {
	deviceID := decision.DeviceID
	fail := func(err error) error {
		logger.Warnf("[sync_id=%s] ConfigManifest rejected: device=%s error=%v", decision.SyncID, deviceID, err)
		result := m.db.Model(&models.Node{}).
			Where("node_id = ? AND (last_sync_id = ? OR last_sync_id = '' OR last_sync_id IS NULL)", deviceID, decision.SyncID).
			Updates(map[string]interface{}{"config_sync_state": "failed", "config_status": "failed", "last_sync_id": decision.SyncID})
		if result.Error != nil {
			return fmt.Errorf("%w; persist failed sync state: %v", err, result.Error)
		}
		return err
	}

	var node models.Node
	if err := m.db.Where("node_id = ?", deviceID).First(&node).Error; err != nil {
		logger.Infof("[%s] Collector not found for config", deviceID)
		return fail(fmt.Errorf("collector not found: %w", err))
	}

	var templates []models.ConfigTemplate
	m.db.Where("node_id = ?", node.NodeID).Find(&templates)

	// Self-healing: reconcile driver CommandTemplates with DB ConfigTemplates.
	// When a driver's command has no matching ConfigTemplate (e.g. device created
	// before createTemplatesFromDriver was deployed), auto-create the missing
	// template. This eliminates the fragile write_data hex-string matching gap.
	reconciled := reconcileDriverTemplates(m.db, node.NodeID, templates)
	if reconciled {
		// Reload templates after reconciliation
		m.db.Where("node_id = ?", node.NodeID).Find(&templates)
	}

	var allChannels []models.Channel
	if err := m.db.Where("node_id = ?", node.NodeID).Find(&allChannels).Error; err != nil {
		return fail(fmt.Errorf("load channels: %w", err))
	}
	var gpioConfigs []models.GPIOConfig
	if err := m.db.Where("node_id = ? AND enabled = ?", node.NodeID, true).Order("pin ASC").Find(&gpioConfigs).Error; err != nil {
		return fail(fmt.Errorf("load GPIO configs: %w", err))
	}
	var pwmConfigs []models.PWMConfig
	if err := m.db.Where("node_id = ? AND enabled = ?", node.NodeID, true).Order("channel ASC, hardware_id ASC").Find(&pwmConfigs).Error; err != nil {
		return fail(fmt.Errorf("load PWM configs: %w", err))
	}
	channels, err := validateManifestAuthority(node, allChannels, gpioConfigs, pwmConfigs)
	if err != nil {
		return fail(err)
	}

	manifestID := decision.ManifestID
	if manifestID == "" {
		manifestID = fmt.Sprintf("v2-%d", time.Now().UnixMilli())
	}

	enc := frame.NewEncoder(frame.MsgConfigMfst)
	enc.EncodeString(1, manifestID)

	// v2.2: field 2 epoch removed, field 9 sync_reason removed

	// Determine encoding path based on protocol version
	useV2 := parseProtocolVersion(node.ProtocolVersion) >= 2.3

	// Encode templates (field 3, repeated sub-structure)
	// v2 path still needs templates — C6's schedule_v2_channel uses
	// config_mgr_get_template(template_id) to look up write_data/read_length/delay_ms.
	for _, tmpl := range templates {
		subEnc := frame.SubEncoder()
		subEnc.EncodeVarint(1, uint64(tmpl.ID))
		if tmpl.WriteData != "" {
			writeHex := tmpl.WriteData
			if strings.HasPrefix(writeHex, "\\x") || strings.HasPrefix(writeHex, "0x") {
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
			} else if strings.HasPrefix(busConfigData, "\\x") {
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
			// New path: field 9 edge_device_groups (repeated sub-messages)
			var edges []models.EdgeDevice
			m.db.Where("channel_id = ? AND node_id = ? AND enabled = true", ch.ID, node.NodeID).Find(&edges)
			logger.Infof("[%s] ConfigManifest ch=%d: useV2=true, found %d edge_devices (query: channel_id=%d node_id=%s)", deviceID, ch.ID, len(edges), ch.ID, node.NodeID)

			for _, edge := range edges {
				grpEnc := frame.SubEncoder()
				grpEnc.EncodeVarint(1, uint64(edge.ID))
				grpEnc.EncodeVarint(2, parseHardwareID(edge.HardwareID))

				// Per-command intervals: check driver CommandTemplates for multi-command support
				cmdIntervals := make(map[string]int)
				if len(edge.CommandIntervals) > 0 {
					json.Unmarshal(edge.CommandIntervals, &cmdIntervals)
				}
				drv, _ := drivers.Get(edge.Type)
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
						tmplID := findTemplateIDForCommand(templates, t.WriteData)
						if tmplID == 0 {
							logger.Warnf("[%s] No ConfigTemplate found for command %s (write_data=%s), skipping",
								deviceID, t.ID, t.WriteData)
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
					cmdEnc := frame.SubEncoder()
					cmdEnc.EncodeVarint(1, findTemplateID(ch, edge))
					cmdEnc.EncodeVarint(2, uint64(interval))
					cmdEnc.EncodeBool(3, edge.Enabled)
					grpEnc.EncodeSubFrame(3, cmdEnc.Bytes())
				}

				subEnc.EncodeSubFrame(9, grpEnc.Bytes())
			}
		}

		enc.EncodeSubFrame(4, subEnc.Bytes())
	}

	// Field 5: dma_channel_configs (repeated DmaChannelConfig sub-messages)
	// These are loaded from node.DmaChannels if present, or from DB config
	var dmaConfigs []models.DmaChannelConfig
	if node.Config != "" {
		var cfg map[string]interface{}
		if err := json.Unmarshal([]byte(node.Config), &cfg); err != nil {
			logger.Warnf("[%s] Failed to parse node.Config JSON in sender: %v", deviceID, err)
		} else if dc, ok := cfg["dma_configs"]; ok {
			if dcJSON, err := json.Marshal(dc); err == nil {
				if err := json.Unmarshal(dcJSON, &dmaConfigs); err != nil {
					logger.Warnf("[%s] Failed to parse dma_configs for sender: %v", deviceID, err)
				} else {
					logger.Infof("[%s] Loaded %d dma_configs from node.Config", deviceID, len(dmaConfigs))
				}
			}
		} else {
			logger.Infof("[%s] No dma_configs key in node.Config (keys: %v)", deviceID, func() []string {
				keys := make([]string, 0, len(cfg))
				for k := range cfg {
					keys = append(keys, k)
				}
				return keys
			}())
		}
	}
	for _, dc := range dmaConfigs {
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
	// Read from node DB fields (LogStreamEnabled, LogStreamLevel)
	{
		lsEnc := frame.SubEncoder()
		lsEnc.EncodeBool(1, node.LogStreamEnabled)
		lsEnc.EncodeVarint(2, uint64(node.LogStreamLevel))
		enc.EncodeSubFrame(10, lsEnc.Bytes())
	}

	// Field 11: gpio_configs (repeated sub-messages, v3.0)
	// Only encode for protocol_version >= 2.4 (older firmware ignores unknown fields)
	if parseProtocolVersion(node.ProtocolVersion) >= 2.4 {
		for _, gc := range gpioConfigs {
			subEnc := frame.SubEncoder()
			subEnc.EncodeVarint(1, uint64(gc.Pin))          // sub-field 1: pin
			subEnc.EncodeVarint(2, uint64(gc.Direction))    // sub-field 2: direction
			subEnc.EncodeVarint(3, uint64(gc.InitialLevel)) // sub-field 3: initial_level
			enc.EncodeSubFrame(11, subEnc.Bytes())
		}

		// Field 12: pwm_configs (repeated sub-messages, v3.0)
		for _, pc := range pwmConfigs {
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

	topic := mqtt.ControlTopicForNode(deviceID)
	payload := enc.Bytes()

	// DEBUG: Log first 120 bytes of ConfigManifest hex
	hexLen := len(payload)
	if hexLen > 120 {
		hexLen = 120
	}
	logger.Infof("[%s] ConfigManifest hex (%d bytes): %s", deviceID, len(payload), hex.EncodeToString(payload[:hexLen]))

	// Persist the exact generation before publish so an immediate valid ACK
	// cannot race ahead of backend authority.
	now := time.Now()
	if err := m.db.Model(&models.Node{}).Where("node_id = ?", deviceID).Updates(map[string]interface{}{
		"config_version": manifestID, "config_sync_state": "syncing",
		"last_sync_at": now, "last_sync_id": decision.SyncID,
	}).Error; err != nil {
		return fail(fmt.Errorf("persist syncing state: %w", err))
	}

	if err := m.mqtt.Publish(topic, payload); err != nil {
		logger.Infof("[%s] Failed to send config: %v", deviceID, err)
		return fail(fmt.Errorf("publish config manifest: %w", err))
	} else {
		logger.Infof("[sync_id=%s] ConfigManifest sent: device=%s id=%s reason=%s %d templates, %d channels",
			decision.SyncID, deviceID, manifestID, decision.Reason, len(templates), len(channels))

	}
	return nil
}

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

// reconcileDriverTemplates ensures every driver CommandTemplate has a matching
// ConfigTemplate in DB. Auto-creates missing templates for self-healing.
// Returns true if any templates were created.
func reconcileDriverTemplates(db *gorm.DB, nodeID string, existingTemplates []models.ConfigTemplate) bool {
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
		drv, err := drivers.Get(edge.Type)
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

	// Create missing templates
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
			logger.Warnf("[reconcile] Failed to auto-create template for write_data=%s: %v", need.writeData, err)
			continue
		}
		// Append template ID to channel's template_ids
		newID := strconv.FormatUint(uint64(tmpl.ID), 10)
		db.Model(&models.Channel{}).Where("id = ?", need.chID).Update("template_ids",
			gorm.Expr("CASE WHEN template_ids = '' OR template_ids IS NULL THEN ? ELSE template_ids || ',' || ? END", newID, newID))
		logger.Infof("[reconcile] Auto-created ConfigTemplate id=%d write_data=%s for channel=%d",
			tmpl.ID, need.writeData, need.chID)
		created = true
	}
	return created
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
