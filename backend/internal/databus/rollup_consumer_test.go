package databus

import (
	"testing"

	"ehome/backend/internal/models"
)

// 缺口1 (7bd01007) 补测: RollupConsumer.Upsert 的 guard 语义与
// SensorParserConsumer 的 rollupSink 回调接线。
// PG 聚合语义由 rollup_consumer_pg_test.go 覆盖 (make test-integration)。

func TestRollupConsumer_UpsertNilGuards(t *testing.T) {
	db := newConsumerTestDB(t)
	records := []models.UnifiedData{{DeviceID: 1, SensorName: "x", Value: 1}}

	// nil receiver / nil db / 空记录 均不得 panic。
	var nilConsumer *RollupConsumer
	nilConsumer.Upsert(records)

	empty := NewRollupConsumer(nil)
	empty.Upsert(records)

	normal := NewRollupConsumer(db)
	normal.Upsert(nil)
	normal.Upsert([]models.UnifiedData{})

	if got := NewRollupConsumer(db).Name(); got != "rollup" {
		t.Errorf("Name() = %q, want rollup", got)
	}
}

func TestRollupConsumer_UpsertNoOpOnSQLite(t *testing.T) {
	// SQLite 无 rollup 表: Upsert 必须在方言检查处 no-op 返回,
	// 不得尝试 INSERT (否则报 no such table)。
	db := newConsumerTestDB(t)
	rc := NewRollupConsumer(db)
	rc.Upsert([]models.UnifiedData{
		{ID: 1, DeviceID: 1, SensorName: "s", Value: 42},
	})
}

// TestSensorParserConsumer_RollupSinkReceivesPersistedRecords 验证
// 解析持久化成功后 rollupSink 收到同一批记录 (v3.4 §3.2.2 接线)。
func TestSensorParserConsumer_RollupSinkReceivesPersistedRecords(t *testing.T) {
	db := newConsumerTestDB(t)

	node := models.Node{NodeID: "node-rollup-sink"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	channel := models.Channel{NodeID: node.NodeID, HardwareID: "I2C0"}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	device := models.EdgeDevice{Name: "rollup-dev", NodeID: node.NodeID, ChannelID: channel.ID,
		Type: "plain_test", Status: "active"}
	if err := db.Create(&device).Error; err != nil {
		t.Fatal(err)
	}

	var got []models.UnifiedData
	consumer := newSensorParserTestConsumer(db, passthroughReassembler{}, &plainTestDriver{})
	consumer.SetRollupSink(func(records []models.UnifiedData) {
		got = append(got, records...)
	})

	consumer.Handle(DataEvent{
		DeviceID: node.NodeID, EdgeDeviceID: uint64(device.ID), RequestID: 1,
		RawData: []byte{0x01},
	})

	if len(got) == 0 {
		t.Fatal("rollupSink not invoked after successful persist")
	}
	// sink 收到的记录必须与落库记录一致 (device_id 解析正确)。
	var persisted []models.UnifiedData
	if err := db.Find(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if len(got) != len(persisted) {
		t.Fatalf("sink records = %d, persisted = %d", len(got), len(persisted))
	}
	for i := range got {
		if got[i].DeviceID != device.ID || got[i].SensorName != "plain_value" {
			t.Errorf("sink record %d = %+v, want device %d sensor plain_value", i, got[i], device.ID)
		}
	}
}

// TestSensorParserConsumer_NoSinkNoError 回归: 未注入 sink 时链路不受影响。
func TestSensorParserConsumer_NoSinkNoError(t *testing.T) {
	db := newConsumerTestDB(t)
	node := models.Node{NodeID: "node-no-sink"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	channel := models.Channel{NodeID: node.NodeID, HardwareID: "I2C0"}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	device := models.EdgeDevice{Name: "no-sink", NodeID: node.NodeID, ChannelID: channel.ID,
		Type: "plain_test", Status: "active"}
	if err := db.Create(&device).Error; err != nil {
		t.Fatal(err)
	}

	consumer := newSensorParserTestConsumer(db, passthroughReassembler{}, &plainTestDriver{})
	// 未 SetRollupSink — 不得 panic。
	consumer.Handle(DataEvent{
		DeviceID: node.NodeID, EdgeDeviceID: uint64(device.ID), RequestID: 1,
		RawData: []byte{0x01},
	})
	var count int64
	db.Model(&models.UnifiedData{}).Count(&count)
	if count == 0 {
		t.Error("expected unified_data rows without rollup sink")
	}
}
