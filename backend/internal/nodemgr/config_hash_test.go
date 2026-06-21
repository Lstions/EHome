package nodemgr

import (
	"testing"
)

func TestCalcConfigHash_Deterministic(t *testing.T) {
	mgr := NewConfigHashManager()

	h1 := mgr.CalcConfigHash([]byte("templates+channels+dma"))
	h2 := mgr.CalcConfigHash([]byte("templates+channels+dma"))

	if h1 != h2 {
		t.Errorf("same input should produce same hash: %s vs %s", h1, h2)
	}
}

func TestCalcConfigHash_DifferentInput(t *testing.T) {
	mgr := NewConfigHashManager()

	h1 := mgr.CalcConfigHash([]byte("config-a"))
	h2 := mgr.CalcConfigHash([]byte("config-b"))

	if h1 == h2 {
		t.Error("different input should produce different hash")
	}
}

func TestCalcConfigHash_Format(t *testing.T) {
	mgr := NewConfigHashManager()

	h := mgr.CalcConfigHash([]byte("test"))
	if len(h) != 8 {
		t.Errorf("hash should be 8 hex chars, got %d", len(h))
	}
}
