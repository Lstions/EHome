package main

import (
	"fmt"
	"os"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/drivers"
)

// runCommandIntervalsCleanupCLI implements `ehomectl command-intervals cleanup`.
// 演进方案 C2/M4: removes non-schedulable keys from every
// edge_devices.command_intervals before the PUT validation gate goes live.
// Idempotent — running twice yields the same database state.
func runCommandIntervalsCleanupCLI() {
	if len(os.Args) < 3 || os.Args[2] != "cleanup" {
		fatal("usage: ehomectl command-intervals cleanup")
	}
	db := connectDB()
	registry := drivers.NewRegistry()
	drivers.RegisterBuiltInDrivers(registry)

	report, err := datalifecycle.CleanupCommandIntervals(db, registry)
	if err != nil {
		fatal(fmt.Sprintf("command-intervals cleanup failed: %v", err))
	}
	fmt.Printf("command-intervals cleanup: devices_scanned=%d devices_cleaned=%d keys_removed=%d\n",
		report.DevicesScanned, report.DevicesCleaned, report.KeysRemoved)
}
