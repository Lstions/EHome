package databus

import (
	"testing"
	"time"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/events"
	"ehome/backend/internal/models"
	"ehome/backend/internal/websocket"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newWSPayloadTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.Node{}, &models.Channel{}, &models.EdgeDevice{},
		&models.ConfigTemplate{}, &models.UnifiedData{}, &models.DeviceData{},
		&models.CalibrationCache{}, &models.LogicalDevice{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// TestSensorParserConsumerBroadcastsCanonicalWSFieldNames locks the C3 contract:
// the parsed data WS payloads use current terminology (node_id/node_name,
// edge_device_id/edge_device_name) and no longer emit the legacy collector_* or
// sensor_device_* names.
func TestSensorParserConsumerBroadcastsCanonicalWSFieldNames(t *testing.T) {
	db := newWSPayloadTestDB(t)

	node := models.Node{NodeID: "node-ws-canonical", Name: "Canonical Node", Status: "active"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	channel := models.Channel{NodeID: node.NodeID, HardwareID: "UART0"}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	device := models.EdgeDevice{Name: "Canonical Device", NodeID: node.NodeID, ChannelID: channel.ID,
		Type: "plain_test", Status: "active", Enabled: true}
	if err := db.Create(&device).Error; err != nil {
		t.Fatal(err)
	}

	hub := websocket.NewHub()
	go hub.Run()
	sub := hub.Subscribe()
	defer hub.Unsubscribe(sub)

	registry := drivers.NewRegistry()
	registry.Register(&plainTestDriver{})
	consumer := NewSensorParserConsumerWithRegistry(db, hub, nil, passthroughReassembler{}, registry)
	consumer.Handle(DataEvent{
		DeviceID: node.NodeID, EdgeDeviceID: uint64(device.ID), RequestID: 1,
		RawData: []byte{0x01},
	})

	got := map[string]map[string]interface{}{}
	deadline := time.After(3 * time.Second)
	for len(got) < 2 {
		select {
		case evt := <-sub:
			if evt.Type == events.DataUpdate || evt.Type == events.ChannelData {
				payload, _ := evt.Payload.(map[string]interface{})
				got[evt.Type] = payload
			}
		case <-deadline:
			t.Fatalf("timed out waiting for data_update and channel_data, got %d events", len(got))
		}
	}

	dataUpdate := got[events.DataUpdate]
	for _, key := range []string{"edge_device_id", "edge_device_name", "node_id", "node_name", "channel_id", "data", "collected_at"} {
		if _, ok := dataUpdate[key]; !ok {
			t.Errorf("data_update missing canonical field %q: %v", key, dataUpdate)
		}
	}
	// node_id is the STRING node serial (node.NodeID) — the same value channel_data,
	// edge_device_status and the REST API publish. It used to leak the numeric
	// primary key, so field-presence alone is not enough: pin type and value.
	if got := dataUpdate["node_id"]; got != node.NodeID {
		t.Errorf("data_update node_id = %v (%T), want string node serial %q", got, got, node.NodeID)
	}
	if _, isString := dataUpdate["node_id"].(string); !isString {
		t.Errorf("data_update node_id must be a string node serial, got %T (%v)", dataUpdate["node_id"], dataUpdate["node_id"])
	}
	for _, key := range []string{"collector_id", "collector_name", "device_id", "device_name", "sensor_device_id"} {
		if _, ok := dataUpdate[key]; ok {
			t.Errorf("data_update must not carry legacy field %q: %v", key, dataUpdate)
		}
	}

	channelData := got[events.ChannelData]
	for _, key := range []string{"edge_device_id", "edge_device_name", "node_id", "channel_id", "data"} {
		if _, ok := channelData[key]; !ok {
			t.Errorf("channel_data missing canonical field %q: %v", key, channelData)
		}
	}
	// Both payloads must agree on node_id; divergence was the defect.
	if got := channelData["node_id"]; got != node.NodeID {
		t.Errorf("channel_data node_id = %v (%T), want string node serial %q", got, got, node.NodeID)
	}
	if channelData["node_id"] != dataUpdate["node_id"] {
		t.Errorf("channel_data node_id = %v and data_update node_id = %v must match", channelData["node_id"], dataUpdate["node_id"])
	}
	for _, key := range []string{"sensor_device_id", "sensor_device_name", "sensor_type"} {
		if _, ok := channelData[key]; ok {
			t.Errorf("channel_data must not carry legacy field %q: %v", key, channelData)
		}
	}
}
