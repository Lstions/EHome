package nodemgr

import (
	"encoding/json"
	"testing"

	"ehome/backend/internal/models"
	"ehome/backend/internal/websocket"
	"ehome/backend/pkg/frame"
	"ehome/backend/testutil"
)

func TestStatusReportPersistsBoundedRuntimePerformance(t *testing.T) {
	db := testutil.OpenTestDB(t)
	node := models.Node{NodeID: "node-perf", Name: "node", Status: "online", HardwareInfo: `{"channels":[]}`}
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
	control := frame.NewEncoder(0)
	control.EncodeVarint(1, 40)
	control.EncodeVarint(2, 2)
	control.EncodeVarint(3, 38)
	control.EncodeVarint(4, 4)
	status := frame.NewEncoder(frame.MsgStatusRpt)
	status.EncodeVarint(1, 30)
	status.EncodeString(2, "online")
	status.EncodeVarint(3, 1)
	status.EncodeVarint(4, 0)
	status.EncodeVarint(5, 0)
	status.EncodeBytes(9, perf.Bytes()[1:]) // nested frame has no message type
	status.EncodeBytes(10, control.Bytes()[1:])
	manager.handleStatusReport(node.NodeID, status.Bytes())

	var stored models.Node
	if err := db.Where("node_id = ?", node.NodeID).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.FreeHeapBytes != 200000 {
		t.Fatalf("free heap=%d", stored.FreeHeapBytes)
	}
	var info struct {
		Channels           []json.RawMessage        `json:"channels"`
		RuntimePerformance runtimePerformanceReport `json:"runtime_performance"`
		ControlStatistics  controlStatisticsReport  `json:"control_statistics"`
	}
	if err := json.Unmarshal([]byte(stored.HardwareInfo), &info); err != nil {
		t.Fatal(err)
	}
	if len(info.Channels) != 0 || info.RuntimePerformance.MinFreeHeapBytes != 180000 ||
		info.RuntimePerformance.SchedulerStackFreeWord != 1200 ||
		info.RuntimePerformance.WorkerStackFreeWord != 900 ||
		info.RuntimePerformance.MinCommandQueueSpaces != 8 ||
		info.ControlStatistics.Accepted != 40 || info.ControlStatistics.Rejected != 2 ||
		info.ControlStatistics.Completed != 38 || info.ControlStatistics.Replayed != 4 {
		t.Fatalf("hardware_info=%s", stored.HardwareInfo)
	}
}
