package deviceinit

import (
	"testing"
)

func TestNewOrchestrator(t *testing.T) {
	orch := NewOrchestrator(nil, nil)
	if orch == nil {
		t.Fatal("expected non-nil orchestrator")
	}
	if orch.cache == nil {
		t.Error("cache should be initialized")
	}
	if orch.pendingResp == nil {
		t.Error("pendingResp should be initialized")
	}
}

func TestGetInitSequence_BMP280(t *testing.T) {
	orch := NewOrchestrator(nil, nil)
	steps := orch.GetInitSequence("bmp280")
	if len(steps) != 5 {
		t.Errorf("expected 5 init steps for bmp280, got %d", len(steps))
	}
	if steps[0].Name != "reset" {
		t.Errorf("first step should be reset, got %s", steps[0].Name)
	}
}

func TestGetInitSequence_LKTH01(t *testing.T) {
	orch := NewOrchestrator(nil, nil)
	steps := orch.GetInitSequence("lk_th01")
	if len(steps) != 2 {
		t.Errorf("expected 2 init steps for lk_th01, got %d", len(steps))
	}
	if steps[0].Name != "reset" {
		t.Errorf("first step should be reset, got %s", steps[0].Name)
	}
}

func TestGetInitSequence_Unknown(t *testing.T) {
	orch := NewOrchestrator(nil, nil)
	steps := orch.GetInitSequence("unknown_device")
	if steps != nil {
		t.Errorf("expected nil for unknown device type, got %v", steps)
	}
}

func TestIsInitialized(t *testing.T) {
	orch := NewOrchestrator(nil, nil)

	if orch.IsInitialized("bmp280") {
		t.Error("should not be initialized initially")
	}

	// Manually set init state
	orch.mu.Lock()
	orch.cache["bmp280"] = &InitState{
		DeviceType: "bmp280",
		Completed:  true,
	}
	orch.mu.Unlock()

	if !orch.IsInitialized("bmp280") {
		t.Error("should be initialized after setting state")
	}
}

func TestClearCache(t *testing.T) {
	orch := NewOrchestrator(nil, nil)

	orch.mu.Lock()
	orch.cache["bmp280"] = &InitState{DeviceType: "bmp280", Completed: true}
	orch.mu.Unlock()

	orch.ClearCache("bmp280")

	if orch.IsInitialized("bmp280") {
		t.Error("should not be initialized after clear")
	}
}

func TestHasActiveInit(t *testing.T) {
	orch := NewOrchestrator(nil, nil)

	if orch.HasActiveInit("bmp280") {
		t.Error("should not have active init initially")
	}

	// Set an incomplete init state
	orch.mu.Lock()
	orch.cache["bmp280"] = &InitState{
		DeviceType: "bmp280",
		Completed:  false,
	}
	orch.mu.Unlock()

	if !orch.HasActiveInit("bmp280") {
		t.Error("should have active init for incomplete state")
	}

	// Complete it
	orch.cache["bmp280"].Completed = true
	if orch.HasActiveInit("bmp280") {
		t.Error("should not have active init for completed state")
	}
}

func TestInitIfNeeded_NoSequence(t *testing.T) {
	orch := NewOrchestrator(nil, nil)
	result := orch.InitIfNeeded("dev1", 1, "unknown_device")
	if result {
		t.Error("should not trigger init for unknown device type")
	}
}

func TestInitIfNeeded_AlreadyInitialized(t *testing.T) {
	orch := NewOrchestrator(nil, nil)

	// Mark as already initialized
	orch.mu.Lock()
	orch.cache["bmp280"] = &InitState{DeviceType: "bmp280", Completed: true}
	orch.mu.Unlock()

	result := orch.InitIfNeeded("dev1", 1, "bmp280")
	if result {
		t.Error("should not trigger init for already initialized device")
	}
}

func TestHandleWriteResponse_NoPending(t *testing.T) {
	orch := NewOrchestrator(nil, nil)
	// Should not panic with no pending responses
	orch.HandleWriteResponse(1, []byte{0x01, 0x02})
}
