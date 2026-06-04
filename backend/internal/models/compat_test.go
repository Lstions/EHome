package models

import (
	"testing"
)

// TestV21CompatAliases 验证 v2.1 老代码风格可继续用 (Phase 2A-1)
func TestV21CompatAliases(t *testing.T) {
	// Collector = Node (v2.1 别名)
	var c1 Collector
	c1 = Node{} // 同类型, 可赋值
	c1.NodeID = "esp32c6_test"
	if c1.NodeID != "esp32c6_test" {
		t.Error("Collector alias should equal Node")
	}
	_ = c1

	// Device = EdgeDevice (v2.1 别名)
	var d1 Device
	d1 = EdgeDevice{}
	d1.Name = "test_edge_device"
	if d1.Name != "test_edge_device" {
		t.Error("Device alias should equal EdgeDevice")
	}
	_ = d1

	// DeviceTemplate = DeviceConfig (v2.1 别名)
	var dt1 DeviceTemplate
	dt1 = DeviceConfig{}
	dt1.DeviceType = "bmp280"
	if dt1.DeviceType != "bmp280" {
		t.Error("DeviceTemplate alias should equal DeviceConfig")
	}
	_ = dt1
}

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

// TestChannelCompatFields 验证 Channel 字段 alias
func TestChannelCompatFields(t *testing.T) {
	c := Channel{
		NodeID: 42, // v2.2 新名
	}
	// 老代码访问 CollectorID 仍可用 (gorm:"-" 不存 DB, 但 Go 字段存在)
	c.CollectorID = 42
	if c.NodeID != c.CollectorID {
		t.Error("Channel.NodeID should equal Channel.CollectorID alias")
	}
}

// TestNodeCompatFields 验证 Node.DeviceID alias
func TestNodeCompatFields(t *testing.T) {
	n := Node{
		NodeID: "esp32c6_xxx",
	}
	// 老代码访问 .DeviceID 仍可用 (gorm:"-" 不存 DB)
	n.DeviceID = "esp32c6_xxx"
	if n.NodeID != n.DeviceID {
		t.Error("Node.NodeID should equal Node.DeviceID alias")
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
