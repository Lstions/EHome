package databus

import (
	"testing"

	"ehome/backend/internal/models"
	"ehome/backend/internal/websocket"
)

// 回归背景（2026-09-30）：
// 固件把「命令超时」放进 StatusReport 的 EdgeDeviceHealth 子帧，后端据此写
// edge_devices.error_code（见 nodemgr/handler_status.go）。但固件**恢复后子帧
// 直接消失**，服务端收不到显式的「已恢复」信号 —— 若不在成功采样路径清零，
// 一次超时留下的 error_code 会永久粘住，设备永远显示「异常」。
//
// 本用例锁定：成功采到数据即清零 error_code。
func TestSensorParserConsumer_SuccessfulIngestClearsErrorCode(t *testing.T) {
	db := newConsumerTestDB(t)
	node, device := ingestGateFixture(t, db, "clear-err-node", "Clear Err Node")

	// 模拟固件此前上报过通信故障
	if err := db.Model(&models.EdgeDevice{}).Where("id = ?", device.ID).
		Update("error_code", 3).Error; err != nil {
		t.Fatal(err)
	}

	hub := websocket.NewHub()
	go hub.Run()
	consumer := newIngestGateConsumer(t, db, hub, passthroughReassembler{})

	// 设备恢复：成功采到一帧数据
	consumer.Handle(DataEvent{
		DeviceID: node.NodeID, EdgeDeviceID: uint64(device.ID), RequestID: 9, RawData: []byte{0x01},
	})

	var stored models.EdgeDevice
	if err := db.First(&stored, device.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.ErrorCode != 0 {
		t.Fatalf("成功采样后 error_code 应清零（否则一次超时永久粘住），实际 %d",
			stored.ErrorCode)
	}
	if stored.Status != "active" {
		t.Fatalf("成功采样后 status 应为 active，实际 %q", stored.Status)
	}
}

// 对照组：设备原本健康（error_code=0）时，成功采样不得把它改成非 0。
func TestSensorParserConsumer_SuccessfulIngestKeepsHealthyDeviceAtZero(t *testing.T) {
	db := newConsumerTestDB(t)
	node, device := ingestGateFixture(t, db, "healthy-node", "Healthy Node")

	hub := websocket.NewHub()
	go hub.Run()
	consumer := newIngestGateConsumer(t, db, hub, passthroughReassembler{})

	consumer.Handle(DataEvent{
		DeviceID: node.NodeID, EdgeDeviceID: uint64(device.ID), RequestID: 9, RawData: []byte{0x01},
	})

	var stored models.EdgeDevice
	if err := db.First(&stored, device.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.ErrorCode != 0 {
		t.Fatalf("健康设备的 error_code 应保持 0，实际 %d", stored.ErrorCode)
	}
}
