package datalifecycle

// testhelpers_test.go — 测试夹具（与生产代码无关）。
//
// 历史: seedDevice 原在 identity_test.go 里，随逻辑设备层一起被删。保留下来的
// 原因很简单: 8 个清理器与分区迁移的测试都需要一台边缘设备作为数据主语，
// 而它与「身份」无关 —— 只是 node + channel + edge_device 三行。

import (
	"testing"

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
