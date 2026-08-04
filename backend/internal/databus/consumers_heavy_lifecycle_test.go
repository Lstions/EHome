package databus

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"ehome/backend/internal/models"
)

// newLifecycleConsumerTestDB extends newConsumerTestDB with the
// logical_devices table needed by the §八 双写 tests.
func newLifecycleConsumerTestDB(t *testing.T) *gorm.DB {
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

func strPtrLifecycle(s string) *string { return &s }

// TestSensorParserConsumer_DoubleWriteResolvesToFinalMergeTarget covers §八:
// an instance whose logical identity points at a mid-chain merge source must
// write logical_device_id resolved through followMergeChain to the FINAL
// target (v3.2-F1: 写入与查询同链) in BOTH unified_data and device_data.
func TestSensorParserConsumer_DoubleWriteResolvesToFinalMergeTarget(t *testing.T) {
	db := newLifecycleConsumerTestDB(t)

	finalLD := models.LogicalDevice{IdentityKey: "t:final", Name: "final", DeviceType: "plain_test"}
	if err := db.Create(&finalLD).Error; err != nil {
		t.Fatal(err)
	}
	midLD := models.LogicalDevice{IdentityKey: "t:mid", Name: "mid", DeviceType: "plain_test",
		MergedInto: &finalLD.ID, MergeStatus: strPtrLifecycle(models.MergeStatusDone)}
	if err := db.Create(&midLD).Error; err != nil {
		t.Fatal(err)
	}

	node := models.Node{NodeID: "node-doublewrite"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	channel := models.Channel{NodeID: node.NodeID, HardwareID: "I2C0"}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	device := models.EdgeDevice{Name: "dw", NodeID: node.NodeID, ChannelID: channel.ID,
		Type: "plain_test", Status: "active", LogicalDeviceID: &midLD.ID}
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
			t.Errorf("unified_data device_id = %d, want lineage %d", row.DeviceID, device.ID)
		}
		if row.LogicalDeviceID == nil || *row.LogicalDeviceID != finalLD.ID {
			t.Errorf("unified_data logical_device_id = %v, want final merge target %d", row.LogicalDeviceID, finalLD.ID)
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
		t.Errorf("device_data device_id = %d, want lineage %d", raw[0].DeviceID, device.ID)
	}
	if raw[0].LogicalDeviceID == nil || *raw[0].LogicalDeviceID != finalLD.ID {
		t.Errorf("device_data logical_device_id = %v, want final merge target %d", raw[0].LogicalDeviceID, finalLD.ID)
	}
}

// TestSensorParserConsumer_DoubleWritePlainIdentity covers §八: an instance
// whose logical identity has no merge edge writes its own logical id — and
// an instance with NO logical identity (pre-backfill) keeps NULL, to be
// covered by the dataScopeCondition OR fallback branch.
func TestSensorParserConsumer_DoubleWritePlainIdentityAndLegacyNull(t *testing.T) {
	db := newLifecycleConsumerTestDB(t)

	ld := models.LogicalDevice{IdentityKey: "t:plain", Name: "plain", DeviceType: "plain_test"}
	if err := db.Create(&ld).Error; err != nil {
		t.Fatal(err)
	}
	node := models.Node{NodeID: "node-doublewrite-2"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	channel := models.Channel{NodeID: node.NodeID, HardwareID: "I2C0"}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	withLD := models.EdgeDevice{Name: "with-ld", NodeID: node.NodeID, ChannelID: channel.ID,
		Type: "plain_test", Status: "active", LogicalDeviceID: &ld.ID}
	if err := db.Create(&withLD).Error; err != nil {
		t.Fatal(err)
	}
	legacy := models.EdgeDevice{Name: "legacy", NodeID: node.NodeID, ChannelID: channel.ID,
		Type: "plain_test", Status: "active"}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}

	consumer := newSensorParserTestConsumer(db, passthroughReassembler{}, &plainTestDriver{})
	consumer.Handle(DataEvent{
		DeviceID: node.NodeID, EdgeDeviceID: uint64(withLD.ID), RequestID: 1,
		RawData: []byte{0x01},
	})
	consumer.Handle(DataEvent{
		DeviceID: node.NodeID, EdgeDeviceID: uint64(legacy.ID), RequestID: 2,
		RawData: []byte{0x01},
	})

	var rows []models.UnifiedData
	if err := db.Order("device_id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("unified_data rows = %d, want 2", len(rows))
	}
	for _, row := range rows {
		switch row.DeviceID {
		case withLD.ID:
			if row.LogicalDeviceID == nil || *row.LogicalDeviceID != ld.ID {
				t.Errorf("instance with identity: logical_device_id = %v, want %d", row.LogicalDeviceID, ld.ID)
			}
		case legacy.ID:
			if row.LogicalDeviceID != nil {
				t.Errorf("legacy instance: logical_device_id = %v, want NULL (OR fallback covers it)", *row.LogicalDeviceID)
			}
		}
	}

	var raw []models.DeviceData
	if err := db.Order("device_id").Find(&raw).Error; err != nil {
		t.Fatal(err)
	}
	if len(raw) != 2 {
		t.Fatalf("device_data rows = %d, want 2", len(raw))
	}
	for _, row := range raw {
		if row.DeviceID == legacy.ID && row.LogicalDeviceID != nil {
			t.Errorf("legacy device_data logical_device_id = %v, want NULL", *row.LogicalDeviceID)
		}
		if row.DeviceID == withLD.ID && (row.LogicalDeviceID == nil || *row.LogicalDeviceID != ld.ID) {
			t.Errorf("identity device_data logical_device_id = %v, want %d", row.LogicalDeviceID, ld.ID)
		}
	}
}
