package api

type ControlPolicy struct {
	LegacyDeviceWriteMode         string
	RawDiagnosticsEnabled         bool
	allowUnsafeRawForTests        bool
	allowUnsafeLegacyForTests     bool
	allowLegacyReadBridgeForTests bool
}

func resolveControlPolicy(policies ...ControlPolicy) ControlPolicy {
	policy := ControlPolicy{LegacyDeviceWriteMode: "disabled"}
	if len(policies) > 0 {
		policy = policies[0]
	}
	if policy.LegacyDeviceWriteMode != "bridge" {
		policy.LegacyDeviceWriteMode = "disabled"
	}
	return policy
}

func (p ControlPolicy) legacyWritesEnabled() bool {
	// "bridge" is reserved for the future CommandExecution adapter. Phase 0-A
	// must never turn it into the existing fire-and-forget implementation.
	return p.allowUnsafeLegacyForTests
}

func (p ControlPolicy) legacyReadBridgeEnabled() bool {
	// The old direct read adapter is retained only to support a narrowly scoped
	// migration test. It is intentionally not backed by a configuration value:
	// production must use CommandExecution + ChannelCmdV2.
	return p.allowLegacyReadBridgeForTests
}

func (p ControlPolicy) rawWritesEnabled() bool {
	// RawDiagnosticsEnabled only advertises operator intent. Until the audited
	// diagnostics service exists, production requests remain closed.
	return p.allowUnsafeRawForTests
}
