package collector

import (
	"context"
	"fmt"
	"time"

	"ehome/backend/internal/models"
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
	m.db.Model(&models.Collector{}).
		Where("device_id = ?", deviceID).
		Updates(map[string]interface{}{
			"ping_latency_ms": rtt.Milliseconds(),
			"last_ping_at":    &now,
		})

	// WebSocket push
	m.wsHub.BroadcastEvent("ping_result", map[string]interface{}{
		"device_id":   deviceID,
		"latency_ms":  rtt.Milliseconds(),
		"timestamp":   time.Now().Unix(),
		"verified":    true,
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

	// Broadcast scan result via WebSocket
	m.wsHub.BroadcastEvent("scan_result", map[string]interface{}{
		"device_id":    deviceID,
		"request_id":  requestID,
		"hardware_id": hardwareID,
		"success":     success,
		"addresses":   fmt.Sprintf("%x", addresses),
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
