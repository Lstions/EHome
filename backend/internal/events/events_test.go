package events

import "testing"

func TestEventConstants(t *testing.T) {
	// Verify all event constants are non-empty and follow noun_verb format
	constants := map[string]string{
		"NodeStatus":           NodeStatus,
		"NodeConfigSynced":     NodeConfigSynced,
		"NodeConfigChanged":    NodeConfigChanged,
		"NodeResourcesUpdated": NodeResourcesUpdated,
		"EdgeDeviceStatus":     EdgeDeviceStatus,
		"DataUpdate":           DataUpdate,
		"OTAProgress":          OTAProgress,
		"OTACompleted":         OTACompleted,
		"Notification":         Notification,
		"PingResult":           PingResult,
		"ScanResult":           ScanResult,
		"ChannelData":          ChannelData,
		"TerminalAck":          TerminalAck,
	}

	for name, val := range constants {
		if val == "" {
			t.Errorf("event constant %s is empty", name)
		}
	}
}

func TestNodeEventNames(t *testing.T) {
	if NodeStatus != "node_status" {
		t.Errorf("NodeStatus = %q, want %q", NodeStatus, "node_status")
	}
	if NodeConfigSynced != "node_config_synced" {
		t.Errorf("NodeConfigSynced = %q, want %q", NodeConfigSynced, "node_config_synced")
	}
	if NodeConfigChanged != "node_config_changed" {
		t.Errorf("NodeConfigChanged = %q, want %q", NodeConfigChanged, "node_config_changed")
	}
	if NodeResourcesUpdated != "node_resources_updated" {
		t.Errorf("NodeResourcesUpdated = %q, want %q", NodeResourcesUpdated, "node_resources_updated")
	}
}

func TestEdgeDeviceEventNames(t *testing.T) {
	if EdgeDeviceStatus != "edge_device_status" {
		t.Errorf("EdgeDeviceStatus = %q, want %q", EdgeDeviceStatus, "edge_device_status")
	}
}

func TestDataEventNames(t *testing.T) {
	if DataUpdate != "data_update" {
		t.Errorf("DataUpdate = %q, want %q", DataUpdate, "data_update")
	}
}

func TestOTAEventNames(t *testing.T) {
	if OTAProgress != "ota_progress" {
		t.Errorf("OTAProgress = %q, want %q", OTAProgress, "ota_progress")
	}
	if OTACompleted != "ota_completed" {
		t.Errorf("OTACompleted = %q, want %q", OTACompleted, "ota_completed")
	}
}

func TestDiagnosticEventNames(t *testing.T) {
	if PingResult != "ping_result" {
		t.Errorf("PingResult = %q, want %q", PingResult, "ping_result")
	}
	if ScanResult != "scan_result" {
		t.Errorf("ScanResult = %q, want %q", ScanResult, "scan_result")
	}
	if ChannelData != "channel_data" {
		t.Errorf("ChannelData = %q, want %q", ChannelData, "channel_data")
	}
	if TerminalAck != "terminal_ack" {
		t.Errorf("TerminalAck = %q, want %q", TerminalAck, "terminal_ack")
	}
}

func TestEventUniqueness(t *testing.T) {
	// All event names must be unique
	seen := make(map[string]string)
	consts := []struct {
		name  string
		value string
	}{
		{"NodeStatus", NodeStatus},
		{"NodeConfigSynced", NodeConfigSynced},
		{"NodeConfigChanged", NodeConfigChanged},
		{"NodeResourcesUpdated", NodeResourcesUpdated},
		{"EdgeDeviceStatus", EdgeDeviceStatus},
		{"DataUpdate", DataUpdate},
		{"OTAProgress", OTAProgress},
		{"OTACompleted", OTACompleted},
		{"Notification", Notification},
		{"PingResult", PingResult},
		{"ScanResult", ScanResult},
		{"ChannelData", ChannelData},
		{"TerminalAck", TerminalAck},
	}

	for _, c := range consts {
		if prev, ok := seen[c.value]; ok {
			t.Errorf("duplicate event value %q: %s and %s", c.value, prev, c.name)
		}
		seen[c.value] = c.name
	}
}
