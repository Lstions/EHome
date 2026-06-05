package collector

import (
	"context"
	"fmt"
	"time"

	"ehome/backend/internal/events"
	"ehome/backend/internal/models"
	"ehome/backend/internal/mqtt"
	"ehome/backend/internal/redis"
	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/logger"
	"ehome/backend/pkg/metrics"
)

// handleWriteResponse processes WriteResponse (type=0x07)
func (m *Manager) handleWriteResponse(deviceID string, payload []byte) {
	dec, err := frame.NewDecoder(payload)
	if err != nil {
		logger.Infof("[%s] Failed to decode WriteResponse: %v", deviceID, err)
		return
	}

	var requestID uint64
	var success bool
	var errorCode uint64
	var errorMsg string

	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		switch field.FieldNum {
		case 1:
			requestID = frame.GetUint64(field)
		case 2:
			success = frame.GetBool(field)
		case 3:
			errorCode = frame.GetUint64(field)
		case 4:
			errorMsg = frame.GetString(field)
		}
	}

	logger.Infof("[%s] WriteResponse: request_id=%d success=%v err=%d msg=%s",
		deviceID, requestID, success, errorCode, errorMsg)

	// Route to PendingWriteManager
	m.pendingWrite.HandleResponse(uint32(requestID), success, uint32(errorCode), errorMsg)
}

// handlePong processes Pong (type=0x09) with anti-forgery verification
func (m *Manager) handlePong(deviceID string, payload []byte) {
	dec, err := frame.NewDecoder(payload)
	if err != nil {
		logger.Infof("[%s] Failed to decode Pong: %v", deviceID, err)
		return
	}

	var timestamp uint64
	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		if field.FieldNum == 1 {
			timestamp = frame.GetUint64(field)
		}
	}

	// Anti-forgery: verify this Pong matches a Ping we sent
	if redis.Client != nil {
		pingKey := fmt.Sprintf("ping:%s", deviceID)
		storedTs, err := redis.Client.Get(context.Background(), pingKey).Int64()
		if err != nil {
			logger.Warnf("[%s] Pong rejected: no matching Ping found in Redis", deviceID)
			return
		}
		if storedTs != int64(timestamp) {
			logger.Warnf("[%s] Pong rejected: timestamp mismatch (expected %d, got %d)", deviceID, storedTs, timestamp)
			return
		}
		// Delete used ping key (one-time use)
		redis.Client.Del(context.Background(), pingKey)
	}

	// Calculate RTT
	rtt := time.Since(time.UnixMicro(int64(timestamp)))
	logger.Infof("[%s] Pong verified, RTT=%v", deviceID, rtt)
	metrics.PingRTT.Observe(float64(rtt.Milliseconds()))

	// F7.6: Complete the pending ping record
	if m.pingTracker != nil {
		if rec, ok := m.pingTracker.Complete(deviceID); ok {
			if rec.callback != nil {
				rec.callback(rtt.Milliseconds(), true)
			}
		}
	}

	// F7.5: Store RTT in DB for historical analysis
	now := time.Now()
	m.db.Model(&models.Node{}).
		Where("node_id = ?", deviceID).
		Updates(map[string]interface{}{
			"ping_latency_ms": rtt.Milliseconds(),
			"last_ping_at":    &now,
		})

	// WebSocket push (v2.2: ping_result 事件名不变)
	m.wsHub.BroadcastEvent(events.PingResult, map[string]interface{}{
		"device_id":  deviceID,
		"node_id":    deviceID, // v2.2 新增 (同一值)
		"latency_ms": rtt.Milliseconds(),
		"timestamp":  time.Now().Unix(),
		"verified":   true,
	})
}

// handleScanReport processes ScanReport (type=0x0C)
func (m *Manager) handleScanReport(deviceID string, payload []byte) {
	dec, err := frame.NewDecoder(payload)
	if err != nil {
		logger.Infof("[%s] Failed to decode ScanReport: %v", deviceID, err)
		return
	}

	var requestID string
	var hardwareID uint64
	var success bool
	var addresses []byte // field 4: found device addresses

	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		switch field.FieldNum {
		case 1:
			requestID = frame.GetString(field)
		case 2:
			hardwareID = frame.GetUint64(field)
		case 3:
			success = frame.GetBool(field)
		case 4:
			addresses = frame.GetBytes(field)
		}
	}

	logger.Infof("[%s] ScanReport: request=%s hw=%d success=%v addrs=%x",
		deviceID, requestID, hardwareID, success, addresses)

	// Broadcast scan result via WebSocket (v2.2: scan_result 事件名不变)
	m.wsHub.BroadcastEvent(events.ScanResult, map[string]interface{}{
		"device_id":     deviceID,
		"node_id":       deviceID, // v2.2 新增 (同一值)
		"request_id":    requestID,
		"hardware_id":   hardwareID,
		"success":       success,
		"addresses":     fmt.Sprintf("%x", addresses),
		"address_count": len(addresses),
	})
}

// handleQueryResponse processes QueryRsp (type=0x0F)
func (m *Manager) handleQueryResponse(deviceID string, payload []byte) {
	dec, err := frame.NewDecoder(payload)
	if err != nil {
		logger.Infof("[%s] Failed to decode QueryRsp: %v", deviceID, err)
		return
	}

	var requestID string
	var success bool
	var errorMsg string

	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		switch field.FieldNum {
		case 1:
			requestID = frame.GetString(field)
		case 2:
			success = frame.GetBool(field)
		case 3:
			errorMsg = frame.GetString(field)
		}
	}

	logger.Infof("[%s] QueryRsp: request=%s success=%v err=%s", deviceID, requestID, success, errorMsg)
}

// handleConfigSyncRequest processes ConfigSyncRequest (type=0x13)
// v2.1: device actively requests config sync.
func (m *Manager) handleConfigSyncRequest(deviceID string, payload []byte) {
	dec, err := frame.NewDecoder(payload)
	if err != nil {
		logger.Infof("[%s] Failed to decode ConfigSyncRequest: %v", deviceID, err)
		return
	}

	var reason string
	var currentEpoch uint64
	var currentManifestID string

	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		switch field.FieldNum {
		case 1:
			reason = frame.GetString(field)
		case 2:
			currentEpoch = frame.GetUint64(field)
		case 3:
			currentManifestID = frame.GetString(field)
		}
	}

	logger.Infof("[%s] ConfigSyncRequest: reason=%s epoch=%d manifest=%s",
		deviceID, reason, currentEpoch, currentManifestID)

	q := &ConfigQueryMsg{
		Reason:            reason,
		CurrentEpoch:      currentEpoch,
		CurrentManifestID: currentManifestID,
	}

	decision := m.syncGate.OnConfigQuery(deviceID, q)
	if decision.Action == SyncActionFull {
		logger.Infof("[sync_id=%s] ConfigSyncRequest push: device=%s reason=%s",
			decision.SyncID, deviceID, decision.Reason)
		m.SendConfigManifestWithDecision(decision)
	} else {
		logger.Infof("[sync_id=%s] ConfigSyncRequest skip: device=%s in_sync", decision.SyncID, deviceID)
	}
}

// handlePing processes MsgPing (0x08) from device.
// BUG-12: Device can ping the server; server responds with PongAck (0x18).
func (m *Manager) handlePing(deviceID string, payload []byte) {
	dec, err := frame.NewDecoder(payload)
	if err != nil {
		logger.Infof("[%s] Failed to decode Ping: %v", deviceID, err)
		return
	}

	var timestamp uint64
	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		if field.FieldNum == 1 {
			timestamp = frame.GetUint64(field)
		}
	}

	logger.Infof("[%s] Received MsgPing (0x08): timestamp=%d", deviceID, timestamp)

	// Respond with PongAck (0x18)
	if err := m.SendPongAck(deviceID, timestamp); err != nil {
		logger.Warnf("[%s] Failed to send PongAck: %v", deviceID, err)
	}
}

// SendPongAck sends a PongAck (0x18) message to a device in response to MsgPing.
func (m *Manager) SendPongAck(deviceID string, pingTimestamp uint64) error {
	enc := frame.NewEncoder(frame.MsgPongAck)
	enc.EncodeVarint(1, pingTimestamp) // echo back the ping timestamp
	enc.EncodeVarint(2, uint64(time.Now().UnixMilli())) // server timestamp

	topic := mqtt.TopicForNode(deviceID)
	return m.mqtt.Publish(topic, enc.Bytes())
}
