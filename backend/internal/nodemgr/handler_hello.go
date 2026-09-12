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

	// deviceID is already the string node_id (e.g. "F0F5BD02F35C")

	// Persist the node registration *before* acknowledging it: a device that
	// receives HelloAck believes it is registered, so the Ack must never be sent
	// for a handshake whose row failed to persist (fail-closed).
	reg, err := m.registerNodeFromHello(deviceID, hello)
	if err != nil {
		logger.Errorf("[%s] Rejecting Hello without HelloAck: node registration was not persisted: %v", deviceID, err)
		return
	}

	// Populate node_id → node.ID cache for worker pool lookups. A zero primary
	// key must never enter the cache, otherwise every later DataReport of this
	// device would be processed under node ID 0.
	storeNodeIDCache(deviceID, reg.node.ID)

	// HelloAck (0x12) confirms a registration that is already durable.
	serverTime := uint64(time.Now().UnixMilli())
	if err := m.SendHelloAck(deviceID, serverTime, 0, handshakeNonce); err != nil {
		logger.Infof("[%s] Failed to send HelloAck: %v", deviceID, err)
	} else {
		logger.Infof("[%s] HelloAck sent: server_time=%d features=0 nonce=%d", deviceID, serverTime, handshakeNonce)
	}

	oldStatus := reg.oldStatus

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
	if reg.created || oldStatus == "offline" || oldStatus == "" {
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

// nodeRegistration is the outcome of the Hello → nodes upsert.
type nodeRegistration struct {
	node      models.Node // persisted row (primary key populated)
	oldStatus string      // status fed into transition side effects ("" = first registration)
	created   bool        // a new row was inserted
	revived   bool        // a soft-deleted row was restored and reused
}

// registerNodeFromHello upserts the nodes row for a validated Hello.
//
// The dedup lookup is deliberately Unscoped(): deleting a node is a soft delete
// (models.Node.DeletedAt), and the same physical device coming back after a
// power cycle must reuse its former row instead of being inserted again. Reuse
// preserves the node's historical data lineage (unified_data ownership and the
// NodeEvent timeline), which is the correct model for "this device
// re-registered". nodes.node_id carries a table-wide unique index (it also
// covers soft-deleted rows), so an Unscoped First can match at most one row.
//
// Every write error is returned to the caller so handleHello can abort before
// sending HelloAck: silently swallowing a unique-key collision here is exactly
// what produced "device believes it is registered, center has no row".
func (m *Manager) registerNodeFromHello(deviceID string, hello parsedHello) (nodeRegistration, error) {
	var reg nodeRegistration
	var node models.Node

	result := m.db.Unscoped().Where("node_id = ?", deviceID).First(&node)
	if result.Error != nil && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return reg, fmt.Errorf("lookup node: %w", result.Error)
	}

	now := time.Now()
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		node = models.Node{
			NodeID:          deviceID,
			Model:           hello.Model,
			FirmwareVersion: hello.FirmwareVersion,
			ProtocolVersion: negotiatedProtocolVersion(hello.ProtocolVersion),
			Status:          "online",
			LastSeen:        &now,
			LastOnlineTime:  &now,
			UptimeSeconds:   0,
			ConfigEpoch:     hello.ConfigEpoch,
			LastManifestID:  hello.LastManifest,
		}
		if err := m.db.Create(&node).Error; err != nil {
			return reg, fmt.Errorf("create node: %w", err)
		}
		if err := m.db.Create(&models.NodeEvent{
			NodeID:    deviceID,
			EventType: "online",
			NewStatus: "online",
		}).Error; err != nil {
			// Auxiliary write: the node row is already durable, so this must not
			// fail the handshake — but it must not vanish silently either.
			logger.Errorf("[%s] Failed to record online NodeEvent: %v", deviceID, err)
		}
		reg.node = node
		reg.created = true
		return reg, nil
	}

	// Reuse the existing row — including a soft-deleted one being restored.
	reg.oldStatus = node.Status
	reg.revived = node.DeletedAt.Valid
	if reg.revived {
		// A soft-deleted row is not part of the live node set: this handshake is a
		// fresh registration, so suppress the stale pre-deletion status for the
		// transition logic (last_online_time, NodeEvent, device init, HA discovery).
		reg.oldStatus = ""
		node.DeletedAt = gorm.DeletedAt{}
	}

	node.FirmwareVersion = hello.FirmwareVersion
	node.Model = hello.Model
	node.ProtocolVersion = negotiatedProtocolVersion(hello.ProtocolVersion)
	node.Status = "online"
	node.LastSeen = &now
	// last_online_time 只在 offline→online 转换时设置，在线期间不覆盖
	if reg.oldStatus != "online" {
		node.LastOnlineTime = &now
	}
	node.ConfigEpoch = hello.ConfigEpoch
	node.LastManifestID = hello.LastManifest
	// Hello starts a new firmware generation; invalidate the previous
	// capability report until ResourceReport arrives for this boot.
	node.BootID = ""
	node.ResourceReportedAt = nil
	node.CommandEngineRevision = 0
	node.CommandEngineCapabilities = "{}"

	// A scoped Save would append "AND deleted_at IS NULL" and silently update
	// zero rows while restoring a soft-deleted node, so the restore runs
	// Unscoped and its error is checked.
	saveTx := m.db
	if reg.revived {
		saveTx = m.db.Unscoped()
	}
	if err := saveTx.Save(&node).Error; err != nil {
		return reg, fmt.Errorf("update node: %w", err)
	}

	if reg.oldStatus != "online" {
		if err := m.db.Create(&models.NodeEvent{
			NodeID:    deviceID,
			EventType: "online",
			OldStatus: reg.oldStatus,
			NewStatus: "online",
		}).Error; err != nil {
			logger.Errorf("[%s] Failed to record online NodeEvent: %v", deviceID, err)
		}
	}

	reg.node = node
	return reg, nil
}

// storeNodeIDCache records deviceID → node.ID for worker pool lookups.
// A zero primary key is never cached: it would make every subsequent
// DataReport of this device resolve to node ID 0.
func storeNodeIDCache(deviceID string, nodeID uint) {
	if nodeID == 0 {
		logger.Warnf("[%s] Refusing to cache zero node primary key in node ID cache", deviceID)
		return
	}
	nodeIDCache.Store(deviceID, nodeIDCacheEntry{nodeID: nodeID, writtenAt: time.Now()})
}
