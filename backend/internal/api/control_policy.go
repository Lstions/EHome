package api

type ControlPolicy struct {
	RawDiagnosticsEnabled bool
}

func resolveControlPolicy(policies ...ControlPolicy) ControlPolicy {
	if len(policies) > 0 {
		return policies[0]
	}
	return ControlPolicy{}
}

func (p ControlPolicy) rawWritesEnabled() bool {
	// RawDiagnosticsEnabled is the operator kill switch for the unaudited raw
	// diagnostics/write surface; it defaults to off.
	return p.RawDiagnosticsEnabled
}
