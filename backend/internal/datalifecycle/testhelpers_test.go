package datalifecycle

// testhelpers_test.go — 测试夹具（与生产代码无关）。
//
// 历史: seedDevice 原在 identity_test.go 里，随逻辑设备层一起被删。保留下来的
// 原因很简单: 8 个清理器与分区迁移的测试都需要一台边缘设备作为数据主语，
// 而它与「身份」无关 —— 只是 node + channel + edge_device 三行。

import (
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
)

// seedDevice inserts a minimal node+channel+edge_device row and returns it.
func seedDevice(t *testing.T, db *gorm.DB, name, devType, hardwareID string, softDeleted bool) *models.EdgeDevice {
	t.Helper()
	if err := db.FirstOrCreate(&models.Node{NodeID: "NODE001", Name: "n", Status: "online"}).Error; err != nil {
		t.Fatalf("seed node: %v", err)
	}
	var ch models.Channel
	if err := db.Where("node_id = ? AND hardware_type = ?", "NODE001", "I2C").FirstOrCreate(&models.Channel{
		NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true,
	}).Error; err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	dev := &models.EdgeDevice{
		Name:       name,
		Type:       devType,
		NodeID:     "NODE001",
		ChannelID:  ch.ID,
		HardwareID: hardwareID,
	}
	if err := db.Create(dev).Error; err != nil {
		t.Fatalf("seed edge device: %v", err)
	}
	if softDeleted {
		if err := db.Delete(dev).Error; err != nil {
			t.Fatalf("soft delete: %v", err)
		}
	}
	return dev
}

// seedExpiredFrame 写一行**过期**帧，用于「保留期主路径会删时序数据」的断言。
//
// 2026-11-11: 这些用例原先 seed 的是 models.UnifiedData。两张原始表退役后，
// TimeseriesTables() 只剩 telemetry_frame —— retention / purge 的时序清理**只**
// 作用于帧表，所以夹具必须换成帧行。否则「到期的时序数据被删」这条语义在测试里
// 没有任何被测对象：断言会得到 0 行，看起来像是"保留期失效"，而实际是夹具还指向
// 一张不再被清理的表。
//
// ⚠ 调用方必须先用 openFrameLifecycleDB(t) 或在测试库里 AutoMigrate 帧表
// （testutil 的 allModels 里刻意没有它，见 models/telemetry_frame.go 的范式说明）。
func seedExpiredFrame(t *testing.T, db *gorm.DB, deviceID uint, ts time.Time) {
	t.Helper()
	if err := db.Create(&models.TelemetryFrame{
		DeviceID:  deviceID,
		TS:        ts,
		Seq:       0,
		FrameKind: models.TelemetryFrameKindKey,
		SlotID:    1,
		Payload:   []byte{0x01, 0x00},
	}).Error; err != nil {
		t.Fatalf("seed telemetry_frame device=%d ts=%s: %v", deviceID, ts.Format(time.RFC3339), err)
	}
}
