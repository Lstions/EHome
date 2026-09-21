package api

import (
	"encoding/json"
	"sort"
	"testing"

	"ehome/backend/internal/drivers"
)

// The composition root passes a parser-overridden registry
// (RegisterBuiltInDriversWithParsers); the fallback resolves through
// RegisterBuiltInDrivers. The address gate must see the SAME catalog either way,
// otherwise the node-config route and the edge-device route could disagree about
// which types are address-requiring (the exact class of drift G1 was).
func TestResolveDriverRegistryExposesSameCatalogAsCompositionRoot(t *testing.T) {
	root := drivers.NewRegistry()
	drivers.RegisterBuiltInDriversWithParsers(root, map[string]json.RawMessage{"sn3001_rain": json.RawMessage("{}")})
	fallback := resolveDriverRegistry()

	a, b := append([]string(nil), root.List()...), append([]string(nil), fallback.List()...)
	sort.Strings(a)
	sort.Strings(b)
	if len(a) != len(b) {
		t.Fatalf("catalog size differs: composition root=%d fallback=%d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("catalog differs at %d: %q vs %q", i, a[i], b[i])
		}
	}
	// And the gate must classify the incident type identically on both.
	for _, reg := range []*drivers.Registry{root, fallback} {
		req, cataloged := driverRequiresTargetAddress(reg, "sn3001_rain")
		if !cataloged || !req {
			t.Fatalf("sn3001_rain must be cataloged and address-requiring, got cataloged=%v req=%v", cataloged, req)
		}
	}
}
