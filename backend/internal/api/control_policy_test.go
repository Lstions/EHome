package api

import "testing"

func TestControlPolicyFailsClosedUntilBridgesExist(t *testing.T) {
	policy := resolveControlPolicy(ControlPolicy{
		LegacyDeviceWriteMode: "bridge",
		RawDiagnosticsEnabled: true,
	})
	if policy.legacyWritesEnabled() {
		t.Fatal("bridge mode enabled the legacy fire-and-forget write path")
	}
	if policy.rawWritesEnabled() {
		t.Fatal("raw diagnostics flag enabled an unaudited raw write path")
	}
	if policy.legacyReadBridgeEnabled() {
		t.Fatal("public control configuration enabled the retired legacy read bridge")
	}
}

func TestControlPolicyRejectsUnknownLegacyMode(t *testing.T) {
	policy := resolveControlPolicy(ControlPolicy{LegacyDeviceWriteMode: "direct"})
	if policy.LegacyDeviceWriteMode != "disabled" {
		t.Fatalf("legacy mode = %q, want disabled", policy.LegacyDeviceWriteMode)
	}
}
