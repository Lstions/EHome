package databus

import (
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"ehome/backend/internal/models"
	"ehome/backend/internal/telemetry"
)

// consumers_heavy_lifecycle_test.go — 写入归属口径的回归（device_id == EdgeDevice.ID）。
//
// 本文件原先是「§八 双写」测试：验证实例的逻辑身份经 followMergeChain 解析到
// 最终合并目标后，unified_data/device_data 的 logical_device_id 两处都写同一个目标。
//
// 2026-10-23 逻辑设备概念层整体移除（.contract/logical-device-removal.md §2/§3）：
//   - models.UnifiedData / models.DeviceData 的 LogicalDeviceID 列已删除；
//   - 写入路径不再有任何身份解析（不再每帧一次 ResolveMergeTarget SQL 往返）。
//
// 2026-11-11 device_data 整表退役（docs/设计/原始数据退役-改造方案-2026-11-11.md §2.1）：
// 本文件原本还断言「device_data 那一行的 device_id 也对」。现在原始行不再落库，
// 那条断言的对象改为**帧表**（telemetry_frame）—— 它承接了"同一帧的压缩表示"，
// 而且 device_id 口径与 unified_data 完全一致（都是 EdgeDevice.ID）。
//
// 所以本文件现在守的是**新的唯一口径**：
//   unified_data.device_id == telemetry_frame.device_id == edge_devices.id，逐字节自洽。
// 覆盖迁移不能丢的两个真实语义：
//   1. 双写仍然发生（unified_data 一行/字段 + 帧表一行/帧），且归属同一台边缘设备 ——
//      这是「历史连续可查」的基础；
//   2. 不同边缘设备的数据**不会**互相串味（原测试用「有身份 / 无身份」两台设备
//      表达这一点，现在用两台不同的边缘设备表达，断言更强：行的归属完全由
//      device_id 决定）。
//
// ⚠ 帧表不在 testutil/AutoMigrate 的清单里（生产由 partition_mgr 建分区母表），
// 因此本文件的测试库必须**显式**建它 —— 见 newLifecycleConsumerTestDB。

// newLifecycleConsumerTestDB 构造只含双写所需表的测试库。
//
// 帧表用 AutoMigrate 建平表：SQLite 内存库用完即弃，不需要分区，而 GORM 从
// 模型 tag 建出的主键 (device_id, ts, seq) 与生产语义一致（列名/类型都对齐）。
func newLifecycleConsumerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.Node{}, &models.Channel{}, &models.EdgeDevice{},
		&models.ConfigTemplate{}, &models.UnifiedData{},
		&models.TelemetryFrame{}, &models.TelemetrySlot{},
		&models.CalibrationCache{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// frameDeviceIDs 读出帧表里出现过的 device_id（升序去重）。
//
// 用 Raw 而不是 GORM Find：帧表在测试库里可能为空，Find 到空切片时要区分
// "没有行"与"出错"；这里只要 id 列表，Raw 最直白。
func frameDeviceIDs(t *testing.T, db *gorm.DB) []uint {
	t.Helper()
	var ids []uint
	if err := db.Raw("SELECT DISTINCT device_id FROM telemetry_frame ORDER BY device_id").
		Scan(&ids).Error; err != nil {
		t.Fatalf("select distinct device_id from telemetry_frame: %v", err)
	}
	return ids
}

// TestSensorParserConsumer_DualWriteUsesEdgeDeviceID 验证双写的两条记录都归属
// 同一台边缘设备（unified_data.device_id == telemetry_frame.device_id == edge_devices.id）。
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
	// 帧表旁路必须显式接线：不接 = 整条 L2 路径不存在（nil 零成本语义），
	// 那样本用例对帧表的断言就会永远"通过"（0 行也满足"都等于 device.ID"）。
	var frameDeviceIDsSeen []uint
	consumer.SetTelemetrySink(func(deviceID uint, _ time.Time, _ int64, _ []telemetry.Reading, _ []byte) {
		frameDeviceIDsSeen = append(frameDeviceIDsSeen, deviceID)
	})
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

	// 帧旁路的 device_id 口径必须与 unified_data 相同（否则两处历史对不上）。
	if len(frameDeviceIDsSeen) == 0 {
		t.Fatal("telemetry sink 未被调用 —— L2 帧表旁路没接线，本用例的帧断言会假通过")
	}
	for _, id := range frameDeviceIDsSeen {
		if id != device.ID {
			t.Errorf("telemetry_frame 的 device_id = %d, want edge device %d", id, device.ID)
		}
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
	sinkDeviceIDs := map[uint]int{}
	consumer.SetTelemetrySink(func(deviceID uint, _ time.Time, _ int64, _ []telemetry.Reading, _ []byte) {
		sinkDeviceIDs[deviceID]++
	})
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

	// 帧旁路同口径：两台设备各一帧，互不串味。
	if sinkDeviceIDs[devA.ID] == 0 || sinkDeviceIDs[devB.ID] == 0 {
		t.Fatalf("帧旁路的 device_id 分布 = %v，两台设备都必须有帧", sinkDeviceIDs)
	}
	for id := range sinkDeviceIDs {
		if id != devA.ID && id != devB.ID {
			t.Fatalf("帧旁路 device_id = %d，不属于任何一台测试设备", id)
		}
	}
}
