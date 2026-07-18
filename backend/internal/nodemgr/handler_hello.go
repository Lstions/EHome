package nodemgr

import (
	"errors"
	"fmt"
	"math"
	"time"

	"ehome/backend/internal/events"
	"ehome/backend/internal/models"
	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/logger"

	"gorm.io/gorm"
)

type parsedHello struct {
	WireNodeID      string
	FirmwareVersion string
	Model           string
	ChannelCount    uint64
	ConfigEpoch     uint64
	NvsHasConfig    bool
	LastManifest    string
	ProtocolVersion string
	HandshakeNonce  uint32
}

// parseHello strictly validates the development protocol. Routing identity
// continues to come from the MQTT topic; the wire node_id is required as a
// consistency check, and handshake_nonce is correlation, not authentication.
func parseHello(payload []byte) (parsedHello, error) {
	var hello parsedHello
	dec, err := frame.NewDecoder(payload)
	if err != nil {
		return hello, fmt.Errorf("invalid Hello frame: %w", err)
	}
	if dec.MsgType() != frame.MsgHello {
		return hello, fmt.Errorf("invalid Hello message type 0x%02X", dec.MsgType())
	}

	var seen [frame.HelloFieldHandshakeNonce + 1]bool
	for {
		field, err := dec.NextField()
		if errors.Is(err, frame.ErrEndOfFrame) {
			break
		}
		if err != nil {
			return hello, fmt.Errorf("malformed Hello fields: %w", err)
		}

		if field.FieldNum >= 1 && field.FieldNum <= frame.HelloFieldHandshakeNonce {
			if seen[field.FieldNum] {
				return hello, fmt.Errorf("invalid Hello field %d: duplicate", field.FieldNum)
			}
			seen[field.FieldNum] = true
		}

		switch field.FieldNum {
		case 1: // node_id is validated but MQTT topic identity remains authoritative.
			if field.WireType != frame.WireLengthDelimited {
				return hello, fmt.Errorf("invalid Hello node_id wire type %d", field.WireType)
			}
			hello.WireNodeID = frame.GetString(field)
		case 2:
			if field.WireType != frame.WireLengthDelimited {
				return hello, fmt.Errorf("invalid Hello firmware_version wire type %d", field.WireType)
			}
			hello.FirmwareVersion = frame.GetString(field)
		case 3:
			if field.WireType != frame.WireLengthDelimited {
				return hello, fmt.Errorf("invalid Hello model wire type %d", field.WireType)
			}
			hello.Model = frame.GetString(field)
		case 4:
			if field.WireType != frame.WireVarint {
				return hello, fmt.Errorf("invalid Hello channel_count wire type %d", field.WireType)
			}
			hello.ChannelCount = frame.GetUint64(field)
		case 5:
			if field.WireType != frame.WireVarint {
				return hello, fmt.Errorf("invalid Hello config_epoch wire type %d", field.WireType)
			}
			hello.ConfigEpoch = frame.GetUint64(field)
		case 6:
			if field.WireType != frame.WireVarint {
				return hello, fmt.Errorf("invalid Hello nvs_has_config wire type %d", field.WireType)
			}
			hello.NvsHasConfig = frame.GetBool(field)
		case 7:
			if field.WireType != frame.WireLengthDelimited {
				return hello, fmt.Errorf("invalid Hello last_manifest wire type %d", field.WireType)
			}
			hello.LastManifest = frame.GetString(field)
		case 8:
			if field.WireType != frame.WireLengthDelimited {
				return hello, fmt.Errorf("invalid Hello protocol_version wire type %d", field.WireType)
			}
			hello.ProtocolVersion = frame.GetString(field)
		case frame.HelloFieldHandshakeNonce:
			if field.WireType != frame.WireVarint {
				return hello, fmt.Errorf("invalid Hello handshake_nonce wire type %d", field.WireType)
			}
			value := frame.GetUint64(field)
			if value > math.MaxUint32 {
				return hello, fmt.Errorf("invalid Hello handshake_nonce %d: uint32 overflow", value)
			}
			hello.HandshakeNonce = uint32(value)
		}
	}
	required := []uint8{1, 2, 3, 4, 5, 6, frame.HelloFieldProtocolVersion, frame.HelloFieldHandshakeNonce}
	for _, number := range required {
		if !seen[number] {
			return hello, fmt.Errorf("invalid Hello: missing required field %d", number)
		}
	}
	if hello.WireNodeID == "" || hello.FirmwareVersion == "" || hello.Model == "" {
		return hello, fmt.Errorf("invalid Hello: node_id, firmware_version, and model are required")
	}
	if hello.ProtocolVersion != ServerMaxProtocolVersion {
		return hello, fmt.Errorf("invalid Hello protocol_version %q, require %s", hello.ProtocolVersion, ServerMaxProtocolVersion)
	}
	if hello.HandshakeNonce == 0 {
		return hello, fmt.Errorf("invalid Hello handshake_nonce: zero")
	}

	return hello, nil
}

// negotiatedProtocolVersion returns the protocol version to use for a node,
// taking the minimum of the device-reported version and the server's max
// supported version. This ensures we never exceed the server's capability
// while respecting the device's limitation.
func negotiatedProtocolVersion(deviceReported string) string {
	dev, devOK := parseProtocolVersion(deviceReported)
	srv, srvOK := parseProtocolVersion(ServerMaxProtocolVersion)
	if !devOK || !srvOK {
		return deviceReported
	}
	if compareProtocolVersions(dev, srv) <= 0 {
		return deviceReported
	}
	return ServerMaxProtocolVersion
}

// handleHello processes Hello messages (type=0x01)
// v2.6 requires an exact protocol version and non-zero field 9 nonce.
func (m *Manager) handleHello(deviceID string, payload []byte) {
	hello, err := parseHello(payload)
	if err != nil {
		logger.Warnf("[%s] Rejecting invalid Hello before ACK: %v", deviceID, err)
		return
	}
	if hello.WireNodeID != deviceID {
		logger.Warnf("[%s] Rejecting Hello with mismatched wire node_id %q", deviceID, hello.WireNodeID)
		return
	}

	firmwareVersion := hello.FirmwareVersion
	model := hello.Model
	channelCount := hello.ChannelCount
	configEpoch := hello.ConfigEpoch
	nvsHasConfig := hello.NvsHasConfig
	lastManifest := hello.LastManifest
	protocolVersion := hello.ProtocolVersion
	handshakeNonce := hello.HandshakeNonce

	logger.Infof("[%s] Hello: fw=%s model=%s channels=%d epoch=%d nvs=%v manifest=%s proto=%s nonce=%d",
		deviceID, firmwareVersion, model, channelCount, configEpoch, nvsHasConfig, lastManifest, protocolVersion, handshakeNonce)

	// Immediately send HelloAck (0x12) to confirm handshake
	serverTime := uint64(time.Now().UnixMilli())
	if err := m.SendHelloAck(deviceID, serverTime, 0, handshakeNonce); err != nil {
		logger.Infof("[%s] Failed to send HelloAck: %v", deviceID, err)
	} else {
		logger.Infof("[%s] HelloAck sent: server_time=%d features=0 nonce=%d", deviceID, serverTime, handshakeNonce)
	}

	// deviceID is already the string node_id (e.g. "F0F5BD02F35C")

	// Upsert node
	var node models.Node
	result := m.db.Where("node_id = ?", deviceID).First(&node)
	now := time.Now()
	oldStatus := ""
	if result.Error == gorm.ErrRecordNotFound {
		node = models.Node{
			NodeID:          deviceID,
			Model:           model,
			FirmwareVersion: firmwareVersion,
			ProtocolVersion: negotiatedProtocolVersion(protocolVersion),
			Status:          "online",
			LastSeen:        &now,
			LastOnlineTime:  &now,
			UptimeSeconds:   0,
			ConfigEpoch:     configEpoch,
			LastManifestID:  lastManifest,
		}
		m.db.Create(&node)
		// Populate node_id → node.ID cache for worker pool lookups
		nodeIDCache.Store(deviceID, node.ID)
		m.db.Create(&models.NodeEvent{
			NodeID:    deviceID,
			EventType: "online",
			NewStatus: "online",
		})
	} else {
		oldStatus = node.Status
		node.FirmwareVersion = firmwareVersion
		node.Model = model
		node.ProtocolVersion = negotiatedProtocolVersion(protocolVersion)
		node.Status = "online"
		node.LastSeen = &now
		// last_online_time 只在 offline→online 转换时设置，在线期间不覆盖
		if oldStatus != "online" {
			node.LastOnlineTime = &now
		}
		node.ConfigEpoch = configEpoch
		node.LastManifestID = lastManifest
		m.db.Save(&node)
		if oldStatus != "online" {
			m.db.Create(&models.NodeEvent{
				NodeID:    deviceID,
				EventType: "online",
				OldStatus: oldStatus,
				NewStatus: "online",
			})
		}
	}

	// WebSocket push
	m.wsHub.BroadcastEvent(events.NodeStatus, map[string]interface{}{
		"node_id":  deviceID,
		"status":   "online",
		"model":    model,
		"firmware": firmwareVersion,
	})

	// OTA state reconciliation per docs §6.4.3: if device Hello reports
	// the target firmware version of an in-flight OTA task, mark it success.
	// HandleHelloOTACompletion takes deviceID (MQTT topic-derived node_id string) as first arg
	if m.otaMgr != nil {
		m.otaMgr.HandleHelloOTACompletion(deviceID, deviceID, firmwareVersion)
	}

	// === v2.1: SyncGate decision (replaces ad-hoc hash check) ===
	helloMsg := &HelloMsg{
		NodeID:          deviceID,
		FirmwareVersion: firmwareVersion,
		Model:           model,
		ChannelCount:    channelCount,
		ConfigEpoch:     configEpoch,
		NvsHasConfig:    nvsHasConfig,
		LastManifest:    lastManifest,
		ProtocolVersion: protocolVersion,
	}

	decision := m.syncGate.OnHello(deviceID, helloMsg)
	if decision.Action == SyncActionFull {
		logger.Infof("[sync_id=%s] Hello push: device=%s reason=%s", decision.SyncID, deviceID, decision.Reason)
		m.SendConfigManifestWithDecision(decision)
	} else {
		logger.Infof("[sync_id=%s] Hello skip: device=%s reason=%s", decision.SyncID, deviceID, decision.Reason)
	}

	// offline→online detection: trigger device initialization
	if oldStatus == "offline" || oldStatus == "" {
		m.triggerDeviceInit(deviceID, deviceID)
	}

	// HomeAssistant Discovery: publish on first registration or status change
	if result.Error == gorm.ErrRecordNotFound || oldStatus == "offline" || oldStatus == "" {
		m.publishHADiscovery(deviceID, deviceID)
	}

	// Async ping (with timeout and WaitGroup tracking)
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		// TODO: add context-aware timeout when SendPing supports context
		if err := m.SendPing(deviceID); err != nil {
			logger.Warnf("[%s] Ping failed: %v", deviceID, err)
		}
	}()
}
