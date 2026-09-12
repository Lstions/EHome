package offlinedetector

import (
	"testing"
	"time"
)

// TestOnEdgeDeviceOffline_InvokesHook 验证边缘设备离线会触发注入的钩子
// (设计/数据源主备与故障转移.md §4)。
func TestOnEdgeDeviceOffline_InvokesHook(t *testing.T) {
	d := NewDetector(nil, nil)
	got := make(chan uint, 1)
	d.SetDeviceOfflineHook(func(id uint) { got <- id })

	d.OnEdgeDeviceOffline(42)

	select {
	case id := <-got:
		if id != 42 {
			t.Fatalf("hook edge_device_id = %d, want 42", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("device offline hook was not invoked")
	}
}

// TestOnEdgeDeviceOffline_HookPanicDoesNotPropagate 验证钩子 panic 不影响
// 离线检测主流程 (钩子非 nil 时被 recover 吞掉)。
func TestOnEdgeDeviceOffline_HookPanicDoesNotPropagate(t *testing.T) {
	d := NewDetector(nil, nil)
	d.SetDeviceOfflineHook(func(id uint) { panic("boom") })

	d.OnEdgeDeviceOffline(7) // 不得 panic
	time.Sleep(50 * time.Millisecond)
}

// TestOnEdgeDeviceOffline_NilHook 验证未注入钩子时安全跳过。
func TestOnEdgeDeviceOffline_NilHook(t *testing.T) {
	d := NewDetector(nil, nil)
	d.OnEdgeDeviceOffline(1) // 不得 panic
}
