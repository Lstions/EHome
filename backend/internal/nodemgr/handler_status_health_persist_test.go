package nodemgr

import (
	"testing"

	"ehome/backend/internal/models"
	"ehome/backend/internal/websocket"
	"ehome/backend/pkg/frame"
	"ehome/backend/testutil"
)

// statusFrameWithHealth 构造一条带 ChannelHealth(field 7) 的 StatusReport。
// 固件只在命令有错时才发这个子帧（handler_data.c 会跳过 error_count==0 的项）。
func statusFrameWithHealth(uptimeSec uint64, channelID, edgeDeviceID, cmdIndex, errCount, commStatus uint64) []byte {
	ed := frame.NewEncoder(0)
	ed.EncodeVarint(1, edgeDeviceID)
	ed.EncodeVarint(2, cmdIndex)
	ed.EncodeVarint(3, errCount)
	ed.EncodeVarint(4, commStatus)

	ch := frame.NewEncoder(0)
	ch.EncodeVarint(1, channelID)
	ch.EncodeBytes(2, ed.Bytes()[1:]) // nested frame has no message type

	enc := frame.NewEncoder(frame.MsgStatusRpt)
	enc.EncodeVarint(1, uptimeSec)
	enc.EncodeString(2, "online")
	enc.EncodeVarint(3, 1)
	enc.EncodeVarint(5, 0)
	enc.EncodeBytes(7, ch.Bytes()[1:])
	return enc.Bytes()
}

// 回归背景（2026-09-30）：
// 固件把「传感器无应答」编码进 StatusReport field 7，但后端只 validate 后丢弃，
// edge_devices.error_code 恒为 0 —— 实测某设备静默 7 天，服务端仍显示正常。
// 本用例锁定：健康子帧必须落到 edge_devices.error_code。
func TestStatusReportPersistsEdgeDeviceHealth(t *testing.T) {
	db := testutil.OpenTestDB(t)
	node := models.Node{NodeID: "node-health", Name: "node", Status: "online", HardwareInfo: `{"channels":[]}`}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	dev := models.EdgeDevice{
		NodeID: node.NodeID, Name: "silent-sensor", Type: "sn3001_rain",
		ChannelID: 1, Enabled: true, Status: "active",
	}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatal(err)
	}
	hub := websocket.NewHub()
	go hub.Run()
	manager := NewManager(db, nil, hub, nil, nil, nil)

	// 固件上报：该命令超时，error_count=3 -> comm_status=3(FAULT)
	manager.handleStatusReport(node.NodeID,
		statusFrameWithHealth(120, 1, uint64(dev.ID), 0, 3, 3))

	var stored models.EdgeDevice
	if err := db.First(&stored, dev.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.ErrorCode != 3 {
		t.Fatalf("edge_devices.error_code 应为 3（固件上报的 comm_status），实际 %d"+
			"—— 若为 0 说明健康子帧又被丢弃了", stored.ErrorCode)
	}
}

// 无健康子帧的上报不得改动 error_code（不能把未知当健康而清零）。
func TestStatusReportWithoutHealthKeepsErrorCode(t *testing.T) {
	db := testutil.OpenTestDB(t)
	node := models.Node{NodeID: "node-keep", Name: "node", Status: "online", HardwareInfo: `{"channels":[]}`}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	dev := models.EdgeDevice{
		NodeID: node.NodeID, Name: "sensor", Type: "sn3001_rain",
		ChannelID: 1, Enabled: true, Status: "active", ErrorCode: 2,
	}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatal(err)
	}
	hub := websocket.NewHub()
	go hub.Run()
	manager := NewManager(db, nil, hub, nil, nil, nil)

	// 不带 field 7 的普通上报
	manager.handleStatusReport(node.NodeID, statusFrame(130))

	var stored models.EdgeDevice
	if err := db.First(&stored, dev.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.ErrorCode != 2 {
		t.Fatalf("无健康子帧时不得改动 error_code：期望 2，实际 %d", stored.ErrorCode)
	}
}

// 解码器必须与校验器一致：合法帧解出的字段值正确。
func TestDecodeChannelHealthFields(t *testing.T) {
	raw := statusFrameWithHealth(120, 7, 42, 5, 9, 1)
	// 取出 field 7 的字节
	dec, err := frame.NewDecoder(raw)
	if err != nil {
		t.Fatal(err)
	}
	var payload []byte
	for {
		field, err := dec.NextField()
		if err != nil {
			break
		}
		if field.FieldNum == 7 {
			payload = frame.GetBytes(field)
		}
	}
	if payload == nil {
		t.Fatal("field 7 未找到")
	}
	out, err := decodeChannelHealth(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("应解出 1 条，实际 %d", len(out))
	}
	got := out[0]
	if got.ChannelID != 7 || got.EdgeDeviceID != 42 || got.CommandIndex != 5 ||
		got.ErrorCount != 9 || got.CommStatus != 1 {
		t.Fatalf("解出的字段不匹配: %+v", got)
	}
}
