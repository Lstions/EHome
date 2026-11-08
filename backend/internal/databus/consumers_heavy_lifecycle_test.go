package databus

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"ehome/backend/internal/models"
)

// 本文件原先是「§八 双写」测试：验证实例的逻辑身份经 followMergeChain 解析到
// 最终合并目标后，unified_data/device_data 的 logical_device_id 两处都写同一个目标。
//
// 2026-10-23 逻辑设备概念层整体移除（.contract/logical-device-removal.md §2/§3）：
//   - models.UnifiedData / models.DeviceData 的 LogicalDeviceID 列已删除；
//   - 写入路径不再有任何身份解析（不再每帧一次 ResolveMergeTarget SQL 往返）。
//
// 因此本文件断言的对象从「逻辑身份链」变成**新的唯一口径**：
//   unified_data.device_id / device_data.device_id == 边缘设备 ID，逐字节自洽。
// 覆盖迁移不能丢的两个真实语义：
//   1. 双写仍然发生（unified_data 一行/字段 + device_data 一行原始字节），
//      且两边的 device_id 指向同一台边缘设备 —— 这是「历史连续可查」的基础；
//   2. 不同边缘设备的数据**不会**互相串味（原测试用「有身份 / 无身份」两台设备
//      表达这一点，现在用两台不同的边缘设备表达，断言更强：行的归属完全由
//      device_id 决定）。

// newLifecycleConsumerTestDB 构造只含双写所需表的测试库。
func newLifecycleConsumerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.Node{}, &models.Channel{}, &models.EdgeDevice{},
		&models.ConfigTemplate{}, &models.UnifiedData{}, &models.DeviceData{},
		&models.CalibrationCache{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// TestSensorParserConsumer_DualWriteUsesEdgeDeviceID 验证双写的两条记录都归属
// 同一台边缘设备（unified_data.device_id == device_data.device_id == edge_devices.id）。
func TestSensorParserConsumer_DualWriteUsesEdgeDeviceID(t *testing.T) {
	db := newLifecycleConsumerTestDB(t)

	node := models.Node{NodeID: "node-doublewrite"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	channel := models.Channel{NodeID: node.NodeID, HardwareID: "I2C0"}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	device := models.EdgeDevice{Name: "dw", NodeID: node.NodeID, ChannelID: channel.ID,
		Type: "plain_test", Status: "active"}
	if err := db.Create(&device).Error; err != nil {
		t.Fatal(err)
	}

	consumer := newSensorParserTestConsumer(db, passthroughReassembler{}, &plainTestDriver{})
	consumer.Handle(DataEvent{
		DeviceID: node.NodeID, EdgeDeviceID: uint64(device.ID), RequestID: 1,
		RawData: []byte{0x01},
	})

	var unified []models.UnifiedData
	if err := db.Find(&unified).Error; err != nil {
		t.Fatal(err)
	}
	if len(unified) == 0 {
		t.Fatal("no unified_data rows persisted")
	}
	for _, row := range unified {
		if row.DeviceID != device.ID {
			t.Errorf("unified_data device_id = %d, want edge device %d", row.DeviceID, device.ID)
		}
	}

	var raw []models.DeviceData
	if err := db.Find(&raw).Error; err != nil {
		t.Fatal(err)
	}
	if len(raw) != 1 {
		t.Fatalf("device_data rows = %d, want 1", len(raw))
	}
	if raw[0].DeviceID != device.ID {
		t.Errorf("device_data device_id = %d, want edge device %d", raw[0].DeviceID, device.ID)
	}
}

// TestSensorParserConsumer_DualWriteKeepsDevicesApart 验证两台边缘设备的数据
// 各自归属、互不串味（原「有逻辑身份 / 无逻辑身份」双设备用例的替身）。
//
// 这条守的是「移除身份层后归属判定不能退化」：写入口径一旦不是 device_id，
// 两台设备的行就会混在一起，而页面上表现为「设备 A 的历史里出现了设备 B 的值」。
func TestSensorParserConsumer_DualWriteKeepsDevicesApart(t *testing.T) {
	db := newLifecycleConsumerTestDB(t)

	node := models.Node{NodeID: "node-doublewrite-2"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	channel := models.Channel{NodeID: node.NodeID, HardwareID: "I2C0"}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	devA := models.EdgeDevice{Name: "a", NodeID: node.NodeID, ChannelID: channel.ID,
		Type: "plain_test", Status: "active"}
	if err := db.Create(&devA).Error; err != nil {
		t.Fatal(err)
	}
	devB := models.EdgeDevice{Name: "b", NodeID: node.NodeID, ChannelID: channel.ID,
		Type: "plain_test", Status: "active"}
	if err := db.Create(&devB).Error; err != nil {
		t.Fatal(err)
	}

	consumer := newSensorParserTestConsumer(db, passthroughReassembler{}, &plainTestDriver{})
	consumer.Handle(DataEvent{
		DeviceID: node.NodeID, EdgeDeviceID: uint64(devA.ID), RequestID: 1,
		RawData: []byte{0x01},
	})
	consumer.Handle(DataEvent{
		DeviceID: node.NodeID, EdgeDeviceID: uint64(devB.ID), RequestID: 2,
		RawData: []byte{0x02},
	})

	var rows []models.UnifiedData
	if err := db.Order("device_id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("no unified_data rows")
	}
	perDevice := map[uint]int{}
	for _, row := range rows {
		if row.DeviceID != devA.ID && row.DeviceID != devB.ID {
			t.Fatalf("unified_data device_id = %d，不属于任何一台测试设备（归属口径被破坏）", row.DeviceID)
		}
		perDevice[row.DeviceID]++
	}
	if perDevice[devA.ID] == 0 || perDevice[devB.ID] == 0 {
		t.Fatalf("两台设备的行数 = %v，每台都必须有自己的行", perDevice)
	}

	var raw []models.DeviceData
	if err := db.Order("device_id").Find(&raw).Error; err != nil {
		t.Fatal(err)
	}
	if len(raw) != 2 {
		t.Fatalf("device_data rows = %d, want 2", len(raw))
	}
	for _, row := range raw {
		if row.DeviceID != devA.ID && row.DeviceID != devB.ID {
			t.Fatalf("device_data device_id = %d，不属于任何一台测试设备", row.DeviceID)
		}
	}
}
