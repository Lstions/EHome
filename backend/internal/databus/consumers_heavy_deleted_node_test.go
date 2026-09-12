package databus

import (
	"testing"
	"time"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/events"
	"ehome/backend/internal/models"
	"ehome/backend/internal/websocket"

	"gorm.io/gorm"
)

// ingestGateFixture 建一个 (节点, 通道, 边缘设备) 三元组。软删用例与存活
// 对照用例共用它, 保证两组夹具除 nodes.deleted_at 外完全一致。
func ingestGateFixture(t *testing.T, db *gorm.DB, serial, name string) (models.Node, models.EdgeDevice) {
	t.Helper()
	node := models.Node{NodeID: serial, Name: name, Status: "active"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	channel := models.Channel{NodeID: node.NodeID, HardwareID: "UART0"}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	device := models.EdgeDevice{Name: name + "-dev", NodeID: node.NodeID, ChannelID: channel.ID,
		Type: "plain_test", Status: "active", Enabled: true}
	if err := db.Create(&device).Error; err != nil {
		t.Fatalf("create edge device: %v", err)
	}
	return node, device
}

func newIngestGateConsumer(t *testing.T, db *gorm.DB, hub *websocket.Hub, reassembler Reassembler) *SensorParserConsumer {
	t.Helper()
	registry := drivers.NewRegistry()
	registry.Register(&plainTestDriver{})
	return NewSensorParserConsumerWithRegistry(db, hub, nil, reassembler, registry)
}

func countRows(t *testing.T, db *gorm.DB, model interface{}) int64 {
	t.Helper()
	var n int64
	if err := db.Model(model).Count(&n).Error; err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return n
}

// collectWS 在 window 内收满 wantTypes 里的事件类型; 收满即返回。
// wantTypes 为空时只把 window 内到达的事件收集起来 (负向断言用)。
func collectWS(sub chan websocket.Event, window time.Duration, wantTypes ...string) map[string]map[string]interface{} {
	got := map[string]map[string]interface{}{}
	deadline := time.After(window)
	for len(got) < len(wantTypes) {
		select {
		case evt := <-sub:
			payload, _ := evt.Payload.(map[string]interface{})
			got[evt.Type] = payload
		case <-deadline:
			return got
		}
	}
	return got
}

// TestSensorParserConsumer_SoftDeletedNodeIngestIsRejected 是 SIM-NODE-006 缺陷的
// 回归锁: 节点注销是 nodes 软删, 其 channels/edge_devices 行按设计保留, 因此旧
// 通道上的上报仍会解析出 edge_device 并命中 —— 摄入边界必须在"命中之后、写入之前"
// fail-closed: 不写 unified_data / device_data, 也不产生引用该节点的 WS 推送。
func TestSensorParserConsumer_SoftDeletedNodeIngestIsRejected(t *testing.T) {
	db := newConsumerTestDB(t)
	node, device := ingestGateFixture(t, db, "gate-deleted-node", "Retired Node")
	if err := db.Delete(&node).Error; err != nil {
		t.Fatalf("soft delete node: %v", err)
	}

	// 前置事实: 软删节点的 edge_device 行仍在, 且 Preload("Node") 退化为零值。
	var preloaded models.EdgeDevice
	if err := db.Preload("Node").First(&preloaded, device.ID).Error; err != nil {
		t.Fatalf("edge device row must survive node deletion: %v", err)
	}
	if preloaded.Node.ID != 0 {
		t.Fatalf("precondition broken: Preload(Node).ID = %d, want 0 for a soft-deleted node", preloaded.Node.ID)
	}

	hub := websocket.NewHub()
	go hub.Run()
	sub := hub.Subscribe()
	defer hub.Unsubscribe(sub)

	reassembler := &recordingReassembler{}
	consumer := newIngestGateConsumer(t, db, hub, reassembler)
	consumer.Handle(DataEvent{
		DeviceID: node.NodeID, EdgeDeviceID: uint64(device.ID), RequestID: 7, RawData: []byte{0x01},
	})

	if got := countRows(t, db, &models.UnifiedData{}); got != 0 {
		t.Errorf("unified_data rows = %d, want 0 for a soft-deleted node", got)
	}
	if got := countRows(t, db, &models.DeviceData{}); got != 0 {
		t.Errorf("device_data rows = %d, want 0 for a soft-deleted node", got)
	}
	if !reassembler.consumedRequest(7) {
		t.Error("reassembly buffer for the dropped frame was not released (Consume not called)")
	}
	if reason := consumer.nodeIngestDropReason(node.NodeID); reason != "node_soft_deleted" {
		t.Errorf("drop reason = %q, want %q", reason, "node_soft_deleted")
	}

	if got := collectWS(sub, 500*time.Millisecond); len(got) != 0 {
		t.Errorf("soft-deleted node produced WS pushes: %v", got)
	}
}

// TestSensorParserConsumer_SoftDeletedNodeIngestIsRejected_LegacyChannelIndex
// 覆盖另一条寻址分支 (固件未带 edge_device_id, channel_id 是 0 基通道序号):
// 该分支下同样不得放行已注销节点的上报。
func TestSensorParserConsumer_SoftDeletedNodeIngestIsRejected_LegacyChannelIndex(t *testing.T) {
	db := newConsumerTestDB(t)
	node, _ := ingestGateFixture(t, db, "gate-deleted-node-legacy", "Retired Legacy Node")
	if err := db.Delete(&node).Error; err != nil {
		t.Fatalf("soft delete node: %v", err)
	}

	consumer := newIngestGateConsumer(t, db, nil, passthroughReassembler{})
	// EdgeDeviceID=0 + ChannelID=0 → 走 0 基通道列表索引回退分支。
	consumer.Handle(DataEvent{
		DeviceID: node.NodeID, EdgeDeviceID: 0, ChannelID: 0, RequestID: 8, RawData: []byte{0x01},
	})

	if got := countRows(t, db, &models.UnifiedData{}); got != 0 {
		t.Errorf("unified_data rows = %d, want 0 for a soft-deleted node via channel-index lookup", got)
	}
	if got := countRows(t, db, &models.DeviceData{}); got != 0 {
		t.Errorf("device_data rows = %d, want 0 for a soft-deleted node via channel-index lookup", got)
	}
}

// TestSensorParserConsumer_LiveNodeIngestStillPersists 是对照组: 与软删用例
// 完全相同的夹具, 仅 nodes 行未删 —— 必须照常写 unified_data/device_data 并
// 广播 node_id/node_name 正确的 data_update。没有它, "把整条链路关掉" 也能让
// 上面的断言通过。
func TestSensorParserConsumer_LiveNodeIngestStillPersists(t *testing.T) {
	db := newConsumerTestDB(t)
	node, device := ingestGateFixture(t, db, "gate-live-node", "Live Node")

	hub := websocket.NewHub()
	go hub.Run()
	sub := hub.Subscribe()
	defer hub.Unsubscribe(sub)

	consumer := newIngestGateConsumer(t, db, hub, passthroughReassembler{})
	consumer.Handle(DataEvent{
		DeviceID: node.NodeID, EdgeDeviceID: uint64(device.ID), RequestID: 9, RawData: []byte{0x01},
	})

	if got := countRows(t, db, &models.UnifiedData{}); got != 1 {
		t.Fatalf("unified_data rows = %d, want 1 for a live node", got)
	}
	if got := countRows(t, db, &models.DeviceData{}); got != 1 {
		t.Fatalf("device_data rows = %d, want 1 for a live node", got)
	}

	got := collectWS(sub, 3*time.Second, events.DataUpdate, events.ChannelData)
	dataUpdate, ok := got[events.DataUpdate]
	if !ok {
		t.Fatalf("live node did not broadcast data_update; events=%v", got)
	}
	// data_update 的 node_id 必须与全站一致：字符串节点 ID（node.NodeID），
	// 而不是数据库数值主键。历史上这里曾用 device.Node.ID，与同一函数里
	// channel_data 的 evt.DeviceID 语义冲突（详见 consumers_heavy.go 的注释）。
	if got, ok := dataUpdate["node_id"].(string); !ok || got != node.NodeID {
		t.Errorf("data_update node_id = %v (%T), want %q (string node_id)",
			dataUpdate["node_id"], dataUpdate["node_id"], node.NodeID)
	}
	if dataUpdate["node_id"] == float64(node.ID) || dataUpdate["node_id"] == node.ID {
		t.Errorf("data_update node_id 退化为数值主键 %d；必须是字符串节点 ID %q",
			node.ID, node.NodeID)
	}
	if dataUpdate["edge_device_id"] != float64(device.ID) && dataUpdate["edge_device_id"] != device.ID {
		t.Errorf("data_update edge_device_id = %v, want %d", dataUpdate["edge_device_id"], device.ID)
	}
	if dataUpdate["node_name"] != node.Name {
		t.Errorf("data_update node_name = %v, want %q", dataUpdate["node_name"], node.Name)
	}
	if _, ok := got[events.ChannelData]; !ok {
		t.Errorf("live node did not broadcast channel_data; events=%v", got)
	}
}

// TestSensorParserConsumer_NodeIngestDropReason 锁定丢弃原因分类: 现场排查需要
// 区分"节点被软删"与"节点从未存在", 两者都必须拒收。
func TestSensorParserConsumer_NodeIngestDropReason(t *testing.T) {
	db := newConsumerTestDB(t)
	node, _ := ingestGateFixture(t, db, "gate-reason-node", "Reason Node")
	consumer := newIngestGateConsumer(t, db, nil, passthroughReassembler{})

	if reason := consumer.nodeIngestDropReason(node.NodeID); reason != "node_unresolved" {
		t.Errorf("live node reason = %q, want %q (defensive branch)", reason, "node_unresolved")
	}
	if reason := consumer.nodeIngestDropReason("no-such-node"); reason != "node_missing" {
		t.Errorf("missing node reason = %q, want %q", reason, "node_missing")
	}
	if err := db.Delete(&node).Error; err != nil {
		t.Fatalf("soft delete node: %v", err)
	}
	if reason := consumer.nodeIngestDropReason(node.NodeID); reason != "node_soft_deleted" {
		t.Errorf("soft-deleted node reason = %q, want %q", reason, "node_soft_deleted")
	}
	if reason := consumer.nodeIngestDropReason(""); reason != "node_id_empty" {
		t.Errorf("empty node id reason = %q, want %q", reason, "node_id_empty")
	}
}
