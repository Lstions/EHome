package models

import (
	"testing"
)

// TestV22NewFields 验证 v2.2 新字段可用
func TestV22NewFields(t *testing.T) {
	ed := EdgeDevice{
		Name:           "BMP280 现场 A",
		NodeID:         1,
		ChannelID:      2,
		DeviceConfigID: 5, // v2.2 关键新增
		HardwareID:     0x76,
		IntervalMs:     1000,
		Enabled:        true,
		Status:         "active",
		InitState:      "pending",
		InitLastStep:   0,
		InitTotalSteps: 5,
	}
	if ed.DeviceConfigID != 5 {
		t.Error("device_config_id not set")
	}
	if ed.HardwareID != 0x76 {
		t.Error("hardware_id not set")
	}
	if ed.InitState != "pending" {
		t.Error("init_state not set")
	}
}

// TestEdgeDeviceLegacyFields 验证 v2.1 老字段保留
func TestEdgeDeviceLegacyFields(t *testing.T) {
	ed := EdgeDevice{
		Type:     "bmp280", // v2.1 字段, v2.2 保留 (从 DeviceConfig 同步)
		ParserID: "bosch.bmp280",
	}
	if ed.Type != "bmp280" {
		t.Error("Type field should be retained for compat")
	}
	if ed.ParserID != "bosch.bmp280" {
		t.Error("ParserID field should be retained for compat")
	}
}

// TestTableNames 验证 GORM 表名
func TestTableNames(t *testing.T) {
	if (Node{}).TableName() != "nodes" {
		t.Errorf("Node.TableName() = %s, want nodes", (Node{}).TableName())
	}
	if (EdgeDevice{}).TableName() != "edge_devices" {
		t.Errorf("EdgeDevice.TableName() = %s, want edge_devices", (EdgeDevice{}).TableName())
	}
}
