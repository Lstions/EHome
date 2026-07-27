package api

import "ehome/backend/internal/drivers"

// resolveDriverRegistry keeps package-level route tests isolated while the
// production composition root always passes its single registered instance.
func resolveDriverRegistry(registries ...*drivers.Registry) *drivers.Registry {
	if len(registries) > 0 && registries[0] != nil {
		return registries[0]
	}
	registry := drivers.NewRegistry()
	drivers.RegisterBuiltInDrivers(registry)
	return registry
}
