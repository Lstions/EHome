package nodemgr

import (
	"context"
	"errors"
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
	// WriteRsp has no EdgeDeviceID, so node ID is the source correlation. Route
	// both success and failure; the orchestrator only completes write-only on
	// success, while a read step remains bound to its DataReport response.
	if m.deviceInit != nil && requestID != 0 {
		m.deviceInit.HandleWriteResponse(deviceID, uint32(requestID), success, uint32(errorCode), errorMsg)
	}
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
	var addressesRaw []byte // field 4: found device addresses (raw bytes)

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
			addressesRaw = frame.GetBytes(field)
		}
	}

	// Parse raw address bytes into integer list
	// Each address is a single byte (Modbus slave address 1-247)
	var addresses []uint64
	for _, b := range addressesRaw {
		addresses = append(addresses, uint64(b))
	}

	logger.Infof("[%s] ScanReport: request=%s hw=%d success=%v addrs=%v",
		deviceID, requestID, hardwareID, success, addresses)

	// Broadcast scan result via WebSocket (v2.2: scan_result 事件名不变)
	m.wsHub.BroadcastEvent(events.ScanResult, map[string]interface{}{
		"device_id":     deviceID,
		"node_id":       deviceID, // v2.2 新增 (同一值)
		"request_id":    requestID,
		"hardware_id":   hardwareID,
		"success":       success,
		"addresses":     addresses,
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
//
// Note: The caller (Manager.HandleMessage via dispatch in manager.go) already
// strips the frame header (0x0B/type/version), so payload here is the bare
// frame body. NewDecoder(payload) is therefore correct — NOT a double decode.
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
	enc.EncodeVarint(1, pingTimestamp)                  // echo back the ping timestamp
	enc.EncodeVarint(2, uint64(time.Now().UnixMilli())) // server timestamp

	topic := mqtt.TopicForNode(deviceID)
	return m.mqtt.Publish(topic, enc.Bytes())
}

// handlePeriphResponse processes PeriphRsp (type=0x1C) — GPIO/PWM operation result.
// Decodes the response and pushes it via WebSocket as a PeriphResult event.
func (m *Manager) handlePeriphResponse(deviceID string, payload []byte) {
	dec, err := frame.NewDecoder(payload)
	if err != nil {
		logger.Infof("[%s] Failed to decode PeriphRsp: %v", deviceID, err)
		return
	}

	var requestID uint64
	var success bool
	var value uint64
	var errorCode uint64
	var periphType uint64
	var resourceID uint64
	var running bool
	var action uint64
	var runningRaw uint64
	seen := map[uint8]bool{}

	for {
		field, err := dec.NextField()
		if errors.Is(err, frame.ErrEndOfFrame) {
			break
		}
		if err != nil {
			logger.Warnf("[%s] malformed PeriphRsp: %v", deviceID, err)
			return
		}
		if field.WireType != frame.WireVarint {
			logger.Warnf("[%s] invalid PeriphRsp wire type", deviceID)
			return
		}
		if field.FieldNum < 1 || field.FieldNum > 8 || seen[field.FieldNum] {
			logger.Warnf("[%s] duplicate or unknown PeriphRsp field %d", deviceID, field.FieldNum)
			return
		}
		seen[field.FieldNum] = true
		switch field.FieldNum {
		case 1:
			requestID = frame.GetUint64(field)
		case 2:
			success = frame.GetBool(field)
		case 3:
			value = frame.GetUint64(field)
		case 4:
			errorCode = frame.GetUint64(field)
		case 5:
			periphType = frame.GetUint64(field) // optional, for async events
		case 6:
			resourceID = frame.GetUint64(field) // optional, GPIO pin or PWM channel
		case 8:
			runningRaw = frame.GetUint64(field)
			if runningRaw > 1 {
				logger.Warnf("[%s] non-canonical PeriphRsp running", deviceID)
				return
			}
			running = runningRaw == 1
		case 7:
			action = frame.GetUint64(field)
		}
	}
	for _, required := range []uint8{1, 2, 5, 6, 7} {
		if !seen[required] {
			logger.Warnf("[%s] PeriphRsp missing required field %d", deviceID, required)
			return
		}
	}
	if successValue, ok := func() (uint64, bool) {
		dec2, err := frame.NewDecoder(payload)
		if err != nil {
			return 0, false
		}
		for {
			f, err := dec2.NextField()
			if err != nil {
				return 0, false
			}
			if f.FieldNum == 2 {
				return frame.GetUint64(f), true
			}
		}
	}(); !ok || successValue > 1 {
		logger.Warnf("[%s] non-canonical PeriphRsp success", deviceID)
		return
	}
	if requestID == 0 {
		logger.Warnf("[%s] PeriphRsp missing request identity", deviceID)
		return
	}
	if requestID > uint64(^uint32(0)) || value > uint64(^uint32(0)) ||
		errorCode > 255 || periphType > 255 || resourceID > 255 || action > 255 {
		logger.Warnf("[%s] PeriphRsp numeric overflow", deviceID)
		return
	}
	m.periphIntentMu.Lock()
	defer m.periphIntentMu.Unlock()
	m.periphMu.Lock()
	meta, pending := m.periphPending[uint32(requestID)]
	latestKey := fmt.Sprintf("%s:%d:%d:%d", deviceID, periphType, resourceID, action)
	latestID := m.periphLatest[latestKey]
	if !pending || meta.deviceID != deviceID || uint64(meta.periphType) != periphType || uint64(meta.resourceID) != resourceID || uint64(meta.action) != action {
		m.periphMu.Unlock()
		logger.Warnf("[%s] rejecting unmatched PeriphRsp request_id=%d", deviceID, requestID)
		return
	}
	if latestID != uint32(requestID) {
		delete(m.periphPending, uint32(requestID))
		m.periphMu.Unlock()
		logger.Warnf("[%s] rejecting superseded PeriphRsp request_id=%d latest=%d", deviceID, requestID, latestID)
		return
	}
	if periphType == 2 && !seen[8] {
		m.periphMu.Unlock()
		logger.Warnf("[%s] PWM PeriphRsp missing running field", deviceID)
		return
	}
	if !success && periphType == 2 && (action == 0 || action == 1) && m.db != nil {
		column := "duty"
		restored := interface{}(uint16(meta.previousValue))
		if action == 1 {
			column = "frequency"
			restored = meta.previousValue
		}
		result := m.db.Model(&models.PWMConfig{}).
			Where("node_id = ? AND channel = ? AND "+column+" = ?", deviceID, uint8(resourceID), meta.provisionalValue).
			Update(column, restored)
		if result.Error != nil || result.RowsAffected != 1 {
			m.periphMu.Unlock()
			logger.Errorf("[%s] failed to compensate rejected PWM %s: err=%v rows=%d", deviceID, column, result.Error, result.RowsAffected)
			return
		}
		value = uint64(meta.previousValue)
	}
	delete(m.periphPending, uint32(requestID))
	delete(m.periphLatest, latestKey)
	m.periphMu.Unlock()

	var hardwareID string
	var channel interface{}
	var pin interface{}
	if periphType == 1 {
		pin = resourceID
	} else if periphType == 2 {
		channel = resourceID
		hardwareID = meta.hardwareID
		pin = meta.pin
	}

	logger.Infof("[%s] PeriphRsp: request_id=%d success=%v value=%d error_code=%d periph_type=%d resource_id=%d",
		deviceID, requestID, success, value, errorCode, periphType, resourceID)

	// WebSocket push — PeriphResult event
	m.wsHub.BroadcastEvent(events.PeriphResult, map[string]interface{}{
		"node_id":     deviceID,
		"request_id":  requestID,
		"success":     success,
		"value":       value,
		"error_code":  errorCode,
		"periph_type": periphType,
		"resource_id": resourceID,
		"hardware_id": hardwareID,
		"channel":     channel,
		"pin":         pin,
		"running":     running,
		"action":      action,
	})
}
