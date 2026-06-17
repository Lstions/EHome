package nodemgr

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"ehome/backend/internal/events"
	"ehome/backend/internal/models"
	"ehome/backend/internal/mqtt"
	"ehome/backend/internal/redis"
	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/logger"
)

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
func (m *Manager) SendWriteCommand(deviceID string, channelID uint32, data []byte, readSize uint32) error {
	// Record TX in terminal
	m.termMgr.RecordTX(deviceID, uint(channelID), data)

	enc := frame.NewEncoder(frame.MsgWriteCmd)
	enc.EncodeVarint(1, uint64(time.Now().UnixNano())) // request_id
	enc.EncodeVarint(2, uint64(channelID))
	enc.EncodeBytes(3, data)
	if readSize > 0 {
		enc.EncodeVarint(4, uint64(readSize))
	}

	topic := mqtt.TopicForNode(deviceID)
	return m.mqtt.Publish(topic, enc.Bytes())
}

// SendScanRequest sends a ScanRequest to a device
func (m *Manager) SendScanRequest(deviceID string, hardwareID uint32) error {
	enc := frame.NewEncoder(frame.MsgScanReq)
	enc.EncodeString(1, fmt.Sprintf("scan-%d", time.Now().Unix()))
	enc.EncodeVarint(2, uint64(hardwareID))

	topic := mqtt.TopicForNode(deviceID)
	return m.mqtt.Publish(topic, enc.Bytes())
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

// SendConfigManifestWithDecision sends a ConfigManifest (0x04) with v2.1 sync metadata.
// The decision carries sync_id, epoch, manifest_id, and reason from SyncGate.
func (m *Manager) SendConfigManifestWithDecision(decision SyncDecision) {
	deviceID := decision.DeviceID

	var node models.Node
	if err := m.db.Where("node_id = ?", deviceID).First(&node).Error; err != nil {
		logger.Infof("[%s] Collector not found for config", deviceID)
		return
	}

	var templates []models.ConfigTemplate
	m.db.Where("node_id = ?", node.NodeID).Find(&templates)

	var channels []models.Channel
	m.db.Where("node_id = ?", node.NodeID).Find(&channels)

	manifestID := decision.ManifestID
	if manifestID == "" {
		manifestID = fmt.Sprintf("v2-%d", time.Now().UnixMilli())
	}

	enc := frame.NewEncoder(frame.MsgConfigMfst)
	enc.EncodeString(1, manifestID)

	// v2.1: field 2 = config_epoch
	enc.EncodeVarint(2, decision.Epoch)

	// Encode templates (field 3, repeated sub-structure)
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
		subEnc.EncodeBool(5, ch.Enabled)

		// Bus type
		busTypeMap := map[string]uint8{
			"UART": 1, "1": 1,
			"I2C": 2, "2": 2,
			"SPI": 3, "3": 3,
			"GPIO": 4, "4": 4,
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

		enc.EncodeSubFrame(4, subEnc.Bytes())
	}

	// Field 5: dma_channel_configs (repeated DmaChannelConfig sub-messages)
	// These are loaded from node.DmaChannels if present, or from DB config
	var dmaConfigs []models.DmaChannelConfig
	if node.Config != "" {
		var cfg map[string]interface{}
		if json.Unmarshal([]byte(node.Config), &cfg) == nil {
			if dc, ok := cfg["dma_configs"]; ok {
				if dcJSON, err := json.Marshal(dc); err == nil {
					json.Unmarshal(dcJSON, &dmaConfigs)
				}
			}
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

	// v2.1: field 8 = sync_id, field 9 = sync_reason
	enc.EncodeString(8, decision.SyncID)
	enc.EncodeString(9, string(decision.Reason))

	topic := mqtt.TopicForNode(deviceID)
	if err := m.mqtt.Publish(topic, enc.Bytes()); err != nil {
		logger.Infof("[%s] Failed to send config: %v", deviceID, err)
	} else {
		logger.Infof("[sync_id=%s] ConfigManifest sent: device=%s id=%s epoch=%d reason=%s %d templates, %d channels",
			decision.SyncID, deviceID, manifestID, decision.Epoch, decision.Reason, len(templates), len(channels))

		// Update DB with new manifest ID and mark as syncing
		now := time.Now()
		m.db.Model(&models.Node{}).Where("node_id = ?", deviceID).Updates(map[string]interface{}{
			"config_version":    manifestID,
			"config_sync_state": "syncing",
			"last_sync_at":      now,
			"last_sync_id":      decision.SyncID,
		})
		m.hashMgr.UpdateLastSent(deviceID)
	}
}
