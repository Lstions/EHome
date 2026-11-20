package nodemgr

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"ehome/backend/pkg/frame"
)

// i2cScanResult is the value delivered from handleScanReport to a waiting
// SendI2CScan call. A scan that finds no device is still a successful scan:
// Success=true, Addresses empty.
type i2cScanResult struct {
	Success   bool
	Addresses []uint64
}

var nextI2CScanID uint64

// SendI2CScan publishes a ScanReq and waits for the matching ScanReport.
//
// The previous API stub returned an empty list without touching the device
// (handler_node.go: "requires MQTT broadcast I2C_SCAN"). The firmware side
// already has ScanReq -> ScanRpt, so the only missing piece was correlating
// the asynchronous report with the HTTP request.
func (m *Manager) SendI2CScan(ctx context.Context, deviceID string, hardwareID uint32) (string, []uint64, error) {
	requestID := fmt.Sprintf("i2c-%d-%d", time.Now().UnixNano(), atomic.AddUint64(&nextI2CScanID, 1))
	resultCh := make(chan i2cScanResult, 1)
	m.scanWaiters.Store(requestID, resultCh)
	defer m.scanWaiters.Delete(requestID)

	enc := frame.NewEncoder(frame.MsgScanReq)
	enc.EncodeString(1, requestID)
	enc.EncodeVarint(2, uint64(hardwareID))
	if err := m.downlink.Publish(deviceID, enc.Bytes()); err != nil {
		return requestID, nil, fmt.Errorf("failed to trigger I2C scan: %w", err)
	}

	select {
	case result := <-resultCh:
		if !result.Success {
			return requestID, nil, fmt.Errorf("device rejected I2C scan")
		}
		return requestID, result.Addresses, nil
	case <-ctx.Done():
		return requestID, nil, fmt.Errorf("I2C scan timed out: %w", ctx.Err())
	}
}

func (m *Manager) deliverScanResult(requestID string, success bool, addresses []uint64) {
	if requestID == "" {
		return
	}
	value, ok := m.scanWaiters.Load(requestID)
	if !ok {
		return
	}
	ch, ok := value.(chan i2cScanResult)
	if !ok {
		return
	}
	select {
	case ch <- i2cScanResult{Success: success, Addresses: addresses}:
	default:
	}
}
