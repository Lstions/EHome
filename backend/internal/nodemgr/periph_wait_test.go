package nodemgr

import (
	"context"
	"testing"
	"time"
)

func TestWaitPeriphResultConsumesEarlyResult(t *testing.T) {
	m := &Manager{}
	m.deliverPeriphResult(42, PeriphResult{Success: true, Value: 2048, PeriphType: 3, ResourceID: 19, Action: 0})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := m.WaitPeriphResult(ctx, 42)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if !result.Success || result.Value != 2048 || result.PeriphType != 3 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestWaitPeriphResultWakesWaiter(t *testing.T) {
	m := &Manager{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		time.Sleep(10 * time.Millisecond)
		m.deliverPeriphResult(43, PeriphResult{Success: true, Value: 100, PeriphType: 3, ResourceID: 19, Action: 0})
	}()
	result, err := m.WaitPeriphResult(ctx, 43)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if result.Value != 100 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestWaitPeriphResultTimeout(t *testing.T) {
	m := &Manager{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := m.WaitPeriphResult(ctx, 44); err == nil {
		t.Fatal("expected timeout")
	}
}
