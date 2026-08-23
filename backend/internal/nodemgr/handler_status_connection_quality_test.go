package nodemgr

import (
	"encoding/json"
	"testing"

	"ehome/backend/internal/models"
	"ehome/backend/internal/websocket"
	"ehome/backend/pkg/frame"
	"ehome/backend/testutil"
)

// Field 28 present, node online, RTT known → wifi_rssi + blended quality written.
func TestStatusReportRuntimePerformanceWiFiRssi(t *testing.T) {
	db := testutil.OpenTestDB(t)
	node := models.Node{
		NodeID:        "node-rssi",
		Name:          "node",
		Status:        "offline",
		HardwareInfo:  `{"channels":[]}`,
		PingLatencyMs: 260, // rttScore = 50
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	hub := websocket.NewHub()
	go hub.Run()
	manager := NewManager(db, nil, hub, nil, nil, nil)

	perf := frame.NewEncoder(0)
	perf.EncodeVarint(1, 200000) // required: free heap
	perf.EncodeVarint(2, 180000)
	perf.EncodeVarint(3, 1200)
	perf.EncodeVarint(4, 900)
	perf.EncodeVarint(5, 8)
	perf.EncodeVarint(28, 70) // |RSSI| = 70 → -70 dBm → rssiScore = 50

	status := frame.NewEncoder(frame.MsgStatusRpt)
	status.EncodeVarint(1, 30)
	status.EncodeString(2, "online")
	status.EncodeVarint(3, 1)
	status.EncodeVarint(4, 0)
	status.EncodeVarint(5, 0)
	status.EncodeBytes(9, perf.Bytes()[1:])
	manager.handleStatusReport(node.NodeID, status.Bytes())

	var stored models.Node
	if err := db.Where("node_id = ?", node.NodeID).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.WiFiRSSI != -70 {
		t.Fatalf("wifi_rssi=%d want -70", stored.WiFiRSSI)
	}
	// 0.6*50 + 0.4*50 = 50
	if stored.ConnectionQuality != 50 {
		t.Fatalf("connection_quality=%d want 50", stored.ConnectionQuality)
	}
	var info struct {
		RuntimePerformance runtimePerformanceReport `json:"runtime_performance"`
	}
	if err := json.Unmarshal([]byte(stored.HardwareInfo), &info); err != nil {
		t.Fatal(err)
	}
	if info.RuntimePerformance.WiFiRssiAbs != 70 {
		t.Fatalf("wifi_rssi_abs=%d want 70", info.RuntimePerformance.WiFiRssiAbs)
	}
}

// Field 28 absent + RTT known → quality from RTT only; wifi_rssi untouched.
func TestStatusReportRuntimePerformanceNoRssi(t *testing.T) {
	db := testutil.OpenTestDB(t)
	node := models.Node{
		NodeID:        "node-norssi",
		Name:          "node",
		Status:        "online",
		HardwareInfo:  `{"channels":[]}`,
		PingLatencyMs: 20, // rttScore = 100
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	hub := websocket.NewHub()
	go hub.Run()
	manager := NewManager(db, nil, hub, nil, nil, nil)

	perf := frame.NewEncoder(0)
	perf.EncodeVarint(1, 200000)
	perf.EncodeVarint(2, 180000)
	perf.EncodeVarint(3, 1200)
	perf.EncodeVarint(4, 900)
	perf.EncodeVarint(5, 8)
	// no field 28

	status := frame.NewEncoder(frame.MsgStatusRpt)
	status.EncodeVarint(1, 30)
	status.EncodeString(2, "online")
	status.EncodeVarint(3, 1)
	status.EncodeVarint(4, 0)
	status.EncodeVarint(5, 0)
	status.EncodeBytes(9, perf.Bytes()[1:])
	manager.handleStatusReport(node.NodeID, status.Bytes())

	var stored models.Node
	if err := db.Where("node_id = ?", node.NodeID).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.WiFiRSSI != 0 {
		t.Fatalf("wifi_rssi=%d want 0 (untouched)", stored.WiFiRSSI)
	}
	if stored.ConnectionQuality != 100 {
		t.Fatalf("connection_quality=%d want 100 (rtt only)", stored.ConnectionQuality)
	}
}

// Field 28 present but node reports non-online → quality must NOT be overwritten.
func TestStatusReportOfflineSkipsQuality(t *testing.T) {
	db := testutil.OpenTestDB(t)
	node := models.Node{
		NodeID:            "node-offline",
		Name:              "node",
		Status:            "online",
		HardwareInfo:      `{"channels":[]}`,
		PingLatencyMs:     20,
		ConnectionQuality: 88, // pre-existing score must survive an offline report
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	hub := websocket.NewHub()
	go hub.Run()
	manager := NewManager(db, nil, hub, nil, nil, nil)

	perf := frame.NewEncoder(0)
	perf.EncodeVarint(1, 200000)
	perf.EncodeVarint(2, 180000)
	perf.EncodeVarint(3, 1200)
	perf.EncodeVarint(4, 900)
	perf.EncodeVarint(5, 8)
	perf.EncodeVarint(28, 70)

	status := frame.NewEncoder(frame.MsgStatusRpt)
	status.EncodeVarint(1, 30)
	status.EncodeString(2, "offline")
	status.EncodeVarint(3, 1)
	status.EncodeVarint(4, 0)
	status.EncodeVarint(5, 0)
	status.EncodeBytes(9, perf.Bytes()[1:])
	manager.handleStatusReport(node.NodeID, status.Bytes())

	var stored models.Node
	if err := db.Where("node_id = ?", node.NodeID).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.ConnectionQuality != 88 {
		t.Fatalf("connection_quality=%d want 88 (untouched on offline)", stored.ConnectionQuality)
	}
	// wifi_rssi is still written: it's a raw sample, not a derived score.
	if stored.WiFiRSSI != -70 {
		t.Fatalf("wifi_rssi=%d want -70", stored.WiFiRSSI)
	}
}
