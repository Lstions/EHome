package collector

import (
	"testing"
)

func TestConfigHashCalcConfigHash(t *testing.T) {
	mgr := NewConfigHashManager()

	h1 := mgr.CalcConfigHash([]byte("templates"), []byte("channels"))
	h2 := mgr.CalcConfigHash([]byte("templates"), []byte("channels"))
	h3 := mgr.CalcConfigHash([]byte("different"), []byte("channels"))

	if h1 != h2 {
		t.Errorf("same input should produce same hash: %s vs %s", h1, h2)
	}
	if h1 == h3 {
		t.Error("different input should produce different hash")
	}
	if len(h1) != 8 {
		t.Errorf("hash should be 8 hex chars, got %d", len(h1))
	}
}

func TestConfigHashShouldSendFirstTime(t *testing.T) {
	mgr := NewConfigHashManager()
	hash := mgr.CalcConfigHash([]byte("v1"), []byte("ch1"))

	if !mgr.ShouldSendConfig("device_a", hash) {
		t.Error("first config send should always be allowed")
	}
}

func TestConfigHashShouldSendDuplicateBlocked(t *testing.T) {
	mgr := NewConfigHashManager()
	hash := mgr.CalcConfigHash([]byte("v1"), []byte("ch1"))

	mgr.ShouldSendConfig("device_a", hash)

	// Same hash should be blocked
	if mgr.ShouldSendConfig("device_a", hash) {
		t.Error("duplicate config (same hash) should be blocked")
	}
}

func TestConfigHashShouldSendDifferentHashAllowed(t *testing.T) {
	mgr := NewConfigHashManager()
	hash1 := mgr.CalcConfigHash([]byte("v1"), []byte("ch1"))
	hash2 := mgr.CalcConfigHash([]byte("v2"), []byte("ch1"))

	mgr.ShouldSendConfig("device_a", hash1)

	if !mgr.ShouldSendConfig("device_a", hash2) {
		t.Error("different hash should be allowed")
	}
}

func TestConfigHashShouldSendDifferentDevices(t *testing.T) {
	mgr := NewConfigHashManager()
	hash := mgr.CalcConfigHash([]byte("same"), []byte("same"))

	mgr.ShouldSendConfig("device_a", hash)

	if !mgr.ShouldSendConfig("device_b", hash) {
		t.Error("different device should be independent")
	}
}

func TestConfigHashReset(t *testing.T) {
	mgr := NewConfigHashManager()
	hash := mgr.CalcConfigHash([]byte("v1"), []byte("ch1"))

	mgr.ShouldSendConfig("device_a", hash)
	mgr.Reset("device_a")

	// After reset, same hash should be allowed again
	if !mgr.ShouldSendConfig("device_a", hash) {
		t.Error("after reset, config should be allowed")
	}
}

func TestConfigHashUpdateLastSent(t *testing.T) {
	mgr := NewConfigHashManager()
	hash := mgr.CalcConfigHash([]byte("v1"), []byte("ch1"))

	mgr.ShouldSendConfig("device_a", hash)
	mgr.UpdateLastSent("device_a")

	// Same hash still blocked (hash unchanged, just timestamp updated)
	if mgr.ShouldSendConfig("device_a", hash) {
		t.Error("same hash after UpdateLastSent should still be blocked")
	}
}
