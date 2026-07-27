package api

import (
	"encoding/json"
	"testing"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

func TestExecutePostActionKeepsAddressInstanceScoped(t *testing.T) {
	db := testutil.OpenTestDB(t)
	config := models.DeviceConfig{Name: "shared", DeviceType: "sn3001_rain", Connection: json.RawMessage(`{"default_params":{"address":1}}`), Status: "active"}
	if err := db.Create(&config).Error; err != nil {
		t.Fatal(err)
	}
	edge := models.EdgeDevice{NodeID: "node-legacy", DeviceConfigID: config.ID, Type: "sn3001_rain", HardwareID: "1", Enabled: true, Status: "active"}
	if err := db.Create(&edge).Error; err != nil {
		t.Fatal(err)
	}
	if err := executePostAction(db, edge, "update_connection_address", map[string]interface{}{"new_addr": 2}); err != nil {
		t.Fatal(err)
	}
	var gotEdge models.EdgeDevice
	if err := db.First(&gotEdge, edge.ID).Error; err != nil {
		t.Fatal(err)
	}
	if gotEdge.HardwareID != "2" {
		t.Fatalf("hardware_id=%q, want 2", gotEdge.HardwareID)
	}
	var gotConfig models.DeviceConfig
	if err := db.First(&gotConfig, config.ID).Error; err != nil {
		t.Fatal(err)
	}
	if string(gotConfig.Connection) != string(config.Connection) {
		t.Fatalf("shared connection changed from %s to %s", config.Connection, gotConfig.Connection)
	}
}

func TestExecutePostActionBaudUpdatesChannelNotDeviceConfig(t *testing.T) {
	db := testutil.OpenTestDB(t)
	config := models.DeviceConfig{Name: "shared", DeviceType: "sn3001_rain", Connection: json.RawMessage(`{"default_params":{"baud_rate":4800}}`), Status: "active"}
	if err := db.Create(&config).Error; err != nil {
		t.Fatal(err)
	}
	channel := models.Channel{NodeID: "node-legacy", BusType: "UART", HardwareType: "uart", BusConfig: "1415000012C0", Enabled: true}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	edge := models.EdgeDevice{NodeID: channel.NodeID, ChannelID: channel.ID, DeviceConfigID: config.ID, Type: "sn3001_rain", HardwareID: "1", Enabled: true, Status: "active"}
	if err := db.Create(&edge).Error; err != nil {
		t.Fatal(err)
	}
	if err := executePostAction(db, edge, "update_connection_baud", map[string]interface{}{"new_baud": 9600}); err != nil {
		t.Fatal(err)
	}
	var gotChannel models.Channel
	if err := db.First(&gotChannel, channel.ID).Error; err != nil {
		t.Fatal(err)
	}
	if gotChannel.BusConfig != "141500002580" {
		t.Fatalf("bus_config=%q, want 141500002580", gotChannel.BusConfig)
	}
	var gotConfig models.DeviceConfig
	if err := db.First(&gotConfig, config.ID).Error; err != nil {
		t.Fatal(err)
	}
	if string(gotConfig.Connection) != string(config.Connection) {
		t.Fatalf("shared connection changed from %s to %s", config.Connection, gotConfig.Connection)
	}
}
