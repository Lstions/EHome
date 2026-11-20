package nodemgr

import (
	"context"
	"fmt"
	"time"
)

// PeriphResult is the validated payload of one PeriphRsp, used by synchronous
// callers such as the ADC read API. It is intentionally the same shape the
// WebSocket event exposes, so both paths cannot disagree about success/value.
type PeriphResult struct {
	Success    bool
	Value      uint32
	ErrorCode  uint8
	PeriphType uint8
	ResourceID uint8
	Action     uint8
}

// WaitPeriphResult waits for the PeriphRsp with requestID. Results that arrive
// between send and this wait are retained briefly, so the caller cannot lose a
// fast response to a scheduling race.
func (m *Manager) WaitPeriphResult(ctx context.Context, requestID uint32) (PeriphResult, error) {
	if value, ok := m.periphResults.LoadAndDelete(requestID); ok {
		return value.(PeriphResult), nil
	}
	waitCh := make(chan PeriphResult, 1)
	actual, loaded := m.periphWaiters.LoadOrStore(requestID, waitCh)
	if loaded {
		waitCh = actual.(chan PeriphResult)
	}
	defer m.periphWaiters.Delete(requestID)

	for {
		if value, ok := m.periphResults.LoadAndDelete(requestID); ok {
			return value.(PeriphResult), nil
		}
		select {
		case result := <-waitCh:
			return result, nil
		case <-ctx.Done():
			return PeriphResult{}, fmt.Errorf("peripheral response timed out: %w", ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// SendPeriphCmdAndWait publishes a PeriphCmd and waits for its matching
// PeriphRsp. Used by API endpoints that need a value (ADC read).
func (m *Manager) SendPeriphCmdAndWait(ctx context.Context, deviceID string,
	periphType, resourceID, action uint8, value uint32, config []byte) (uint32, PeriphResult, error) {
	requestID, err := m.SendPeriphCmdWithID(deviceID, periphType, resourceID, action, value, config)
	if err != nil {
		return 0, PeriphResult{}, err
	}
	result, err := m.WaitPeriphResult(ctx, requestID)
	return requestID, result, err
}

func (m *Manager) deliverPeriphResult(requestID uint32, result PeriphResult) {
	if value, ok := m.periphWaiters.LoadAndDelete(requestID); ok {
		ch := value.(chan PeriphResult)
		select {
		case ch <- result:
		default:
		}
		return
	}
	// The HTTP caller may not have reached WaitPeriphResult yet. Keep it for a
	// short bounded window; WaitPeriphResult consumes and removes it.
	m.periphResults.Store(requestID, result)
	time.AfterFunc(10*time.Second, func() { m.periphResults.Delete(requestID) })
}
