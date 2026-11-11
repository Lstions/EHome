package databus

// unified_write_switch_test.go — 停写开关的验收（2026-11-08）。
//
// 用的路是**真实调用链**：构造真实 consumer 并让它处理一个真实 DataEvent，
// 然后直接数表、数 sink —— 不模拟、不打桩。
//
// 守四件事（Lead 的验收要求）：
//   unified_data        行数不增长   ← 唯一应该停的
//   latest 缓存         仍被更新     ← /latest-data 依赖它
//   L2 帧表 / L3 rollup 仍被调用     ← 门控必须在它们之后
//   跳过计数器           增长         ← 「停了」与「上游断了」不能同形

import (
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
	"ehome/backend/internal/telemetry"
	"ehome/backend/pkg/metrics"
	"ehome/backend/pkg/parser"
)

// setupSwitchDevice 造 node/channel/device，返回 deviceID 与 nodeID。
func setupSwitchDevice(t *testing.T, suffix string) (*gorm.DB, string, uint) {
	t.Helper()
	db := newConsumerTestDB(t)
	node := models.Node{NodeID: "node-" + suffix}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	channel := models.Channel{NodeID: node.NodeID, HardwareID: "UART0"}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	device := models.EdgeDevice{
		Name: "dev-" + suffix, NodeID: node.NodeID, ChannelID: channel.ID,
		Type: "plain_test", Status: "active",
	}
	if err := db.Create(&device).Error; err != nil {
		t.Fatal(err)
	}
	return db, node.NodeID, device.ID
}

// TestUnifiedWriteSwitch_OnWritesRows 是**对照组**：开关打开时行数应增长。
//
// 没有它，「开关关掉后行数不增长」可能只是因为这条测试路径本来就不写库。
func TestUnifiedWriteSwitch_OnWritesRows(t *testing.T) {
	db, nodeID, devID := setupSwitchDevice(t, "switch-on")

	consumer := newSensorParserTestConsumer(db, passthroughReassembler{}, &plainTestDriver{})
	consumer.unifiedWriteEnabled = true
	latestHits := 0
	consumer.latestSink = func(models.UnifiedData) { latestHits++ }

	consumer.Handle(DataEvent{
		DeviceID: nodeID, EdgeDeviceID: uint64(devID), RequestID: 1, RawData: []byte("response"),
	})

	var n int64
	db.Model(&models.UnifiedData{}).Where("device_id = ?", devID).Count(&n)
	if n == 0 {
		t.Fatal("开关打开时 unified_data 未写入 —— 对照组不成立，后续用例的\"不增长\"就没有意义")
	}
	if latestHits == 0 {
		t.Fatal("latestSink 未被调用")
	}
	t.Logf("开关 ON：写入 %d 行，latestSink %d 次", n, latestHits)
}

// TestUnifiedWriteSwitch_OffStopsUnifiedDataOnly 是核心语义断言。
func TestUnifiedWriteSwitch_OffStopsUnifiedDataOnly(t *testing.T) {
	db, nodeID, devID := setupSwitchDevice(t, "switch-off")

	consumer := newSensorParserTestConsumer(db, passthroughReassembler{}, &plainTestDriver{})
	consumer.unifiedWriteEnabled = false
	latestHits := 0
	consumer.latestSink = func(models.UnifiedData) { latestHits++ }
	// L2 帧表与 L3 rollup 的 sink 计数：门控必须在这两者之后。
	frameHits, rollupHits := 0, 0
	consumer.telemetrySink = func(uint, time.Time, int64, []telemetry.Reading, []byte) { frameHits++ }
	consumer.telemetryRollupSink = func(uint, []parser.Field, time.Time) { rollupHits++ }

	skippedBefore := counterValue(t, metrics.UnifiedWritesSkippedTotal)
	consumer.Handle(DataEvent{
		DeviceID: nodeID, EdgeDeviceID: uint64(devID), RequestID: 1, RawData: []byte("response"),
	})

	var n int64
	db.Model(&models.UnifiedData{}).Where("device_id = ?", devID).Count(&n)
	if n != 0 {
		t.Fatalf("开关关掉后仍写入 %d 行 unified_data", n)
	}
	if latestHits == 0 {
		t.Fatal("latestSink 未被调用 —— 门控位置错了：它必须在 latestSink **之后**，" +
			"否则停写后 /latest-data 会退化成「最后一次上报的旧值」")
	}
	if frameHits == 0 {
		t.Fatal("telemetrySink（L2 帧表）未被调用 —— 门控被挪到了它之前，" +
			"那会把帧表一起闸掉，而「图表没数据」看起来却像「停写生效了」")
	}
	if rollupHits == 0 {
		t.Fatal("telemetryRollupSink（L3 分钟桶）未被调用 —— 同上")
	}
	if got := counterValue(t, metrics.UnifiedWritesSkippedTotal) - skippedBefore; got == 0 {
		t.Fatal("跳过计数器未增长 —— 否则「按预期停了」与「上游断了」在行数上同形")
	}
	t.Logf("开关 OFF：unified_data 0 行，latestSink %d 次，跳过计数 +%v", latestHits,
		counterValue(t, metrics.UnifiedWritesSkippedTotal)-skippedBefore)
}
