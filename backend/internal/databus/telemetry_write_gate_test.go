package databus

// telemetry_write_gate_test.go — L1 写入收窄（设计/遥测存储优化方案-2026-11-06.md §3 / §7 阶段1）单测。
//
// SQLite 足够: 收窄是**进程内决策**（值比较 + 心跳时间差），与方言无关；
// 库只用来断言"到底写了多少行"。
//
// 任务书要求的五个场景:
//  1. 值不变 ⇒ 跳过；
//  2. 值变化 ⇒ 必写；
//  3. 心跳超时 ⇒ 必写；
//  4. 写库失败 ⇒ **不更新状态**（否则该值永久丢失）；
//  5. 进程重启 ⇒ 首帧全量写。
//
// 另加四条守护:
//  6. 影子模式默认**不跳**（只统计）—— 「升级镜像写入行为不变」的硬约束；
//  7. latestSink 在"被跳过"时**仍然更新** —— 方案 §7 阶段1 明确要求
//     "把 latestSink 调用提到 INSERT 之前"（否则缓存冻结）；
//  8. nil gate（未装配）退化为全量写 —— 不得静默开始丢行；
//  9. NaN 永远被写（NaN != NaN，不会被"值未变"误跳过）。

import (
	"math"
	"testing"
	"time"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// countingDriver 解析出固定字段集，用来构造"值变/不变"的帧序列。
// values 是 map（指针可改），让同一个 consumer 能在两帧之间切换解析结果。
type countingDriver struct {
	values map[string]float64
}

func (d *countingDriver) DeviceType() string                         { return "counting_test" }
func (d *countingDriver) DeviceName() string                         { return "counting test" }
func (d *countingDriver) OEM() string                                { return "test" }
func (d *countingDriver) Category() string                           { return "test" }
func (d *countingDriver) HardwareTypes() []string                    { return []string{"UART"} }
func (d *countingDriver) GetSensorDefinitions() []drivers.SensorData { return nil }
func (d *countingDriver) ParseData(raw []byte) ([]drivers.SensorData, error) {
	out := make([]drivers.SensorData, 0, len(d.values))
	for name, v := range d.values {
		out = append(out, drivers.SensorData{Name: name, Value: v, Unit: "V"})
	}
	return out, nil
}

// newWriteGateTestDB 建只含收窄用例所需表的 SQLite 库。
func newWriteGateTestDB(t *testing.T) *gorm.DB {
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

// seedWriteGateDevice 建 node+channel+edge_device 三行并返回它们。
func seedWriteGateDevice(t *testing.T, db *gorm.DB, nodeID string) (models.Node, models.EdgeDevice) {
	t.Helper()
	node := models.Node{NodeID: nodeID, Name: nodeID, Status: "active"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("seed node: %v", err)
	}
	channel := models.Channel{NodeID: node.NodeID, HardwareID: "UART0"}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	device := models.EdgeDevice{Name: nodeID + "-dev", NodeID: node.NodeID, ChannelID: channel.ID,
		Type: "counting_test", Status: "active", Enabled: true}
	if err := db.Create(&device).Error; err != nil {
		t.Fatalf("seed device: %v", err)
	}
	return node, device
}

// newWriteGateConsumer 构造只注入 countingDriver 的 consumer，并返回该驱动指针
// （测试可改它的 values 来模拟"下一帧值变了"）。
func newWriteGateConsumer(t *testing.T, db *gorm.DB, values map[string]float64) (*SensorParserConsumer, *countingDriver) {
	t.Helper()
	driver := &countingDriver{values: values}
	registry := drivers.NewRegistry()
	registry.Register(driver)
	return NewSensorParserConsumerWithRegistry(db, nil, passthroughReassembler{}, registry), driver
}

// countUnifiedRows 统计某设备的 unified_data 行数。
func countUnifiedRows(t *testing.T, db *gorm.DB, deviceID uint) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&models.UnifiedData{}).Where("device_id = ?", deviceID).Count(&n).Error; err != nil {
		t.Fatalf("count unified_data: %v", err)
	}
	return n
}

// frame 是构造一帧 DataEvent 的简写（EdgeDeviceID 走"显式边缘设备"分支）。
func frame(nodeID string, deviceID uint) DataEvent {
	return DataEvent{DeviceID: nodeID, EdgeDeviceID: uint64(deviceID), RequestID: 1, RawData: []byte{0x01}}
}

// TestWriteGate_SkipsUnchangedValues 场景 1: 值不变且未到心跳 ⇒ 不写新行。
func TestWriteGate_SkipsUnchangedValues(t *testing.T) {
	db := newWriteGateTestDB(t)
	node, device := seedWriteGateDevice(t, db, "node-gate-skip")

	consumer, _ := newWriteGateConsumer(t, db, map[string]float64{"voltage": 12})
	// 心跳设得足够大，确保本用例只测"值不变"这一支。
	consumer.SetTelemetryHeartbeat(time.Hour)

	consumer.Handle(frame(node.NodeID, device.ID))
	if got := countUnifiedRows(t, db, device.ID); got != 1 {
		t.Fatalf("首帧行数 = %d, want 1（首帧必须写）", got)
	}
	for i := 0; i < 4; i++ {
		consumer.Handle(frame(node.NodeID, device.ID))
	}
	if got := countUnifiedRows(t, db, device.ID); got != 1 {
		t.Errorf("值不变时行数 = %d, want 1（值未变且未到心跳，应全部跳过）", got)
	}
}

// TestWriteGate_WritesChangedValues 场景 2: 值变化 ⇒ 必写（即使心跳未到）。
func TestWriteGate_WritesChangedValues(t *testing.T) {
	db := newWriteGateTestDB(t)
	node, device := seedWriteGateDevice(t, db, "node-gate-change")

	consumer, driver := newWriteGateConsumer(t, db, map[string]float64{"voltage": 12})
	consumer.SetTelemetryHeartbeat(time.Hour)
	consumer.Handle(frame(node.NodeID, device.ID))
	if got := countUnifiedRows(t, db, device.ID); got != 1 {
		t.Fatalf("首帧行数 = %d, want 1", got)
	}

	// 同一个 consumer + 同一个 (设备, 字段)，只有值变了 ⇒ 必须落库。
	driver.values = map[string]float64{"voltage": 13}
	consumer.Handle(frame(node.NodeID, device.ID))
	if got := countUnifiedRows(t, db, device.ID); got != 2 {
		t.Errorf("值变化后行数 = %d, want 2（值变化必须写，与心跳无关）", got)
	}
}

// TestWriteGate_WritesOnHeartbeat 场景 3: 值不变但距上次落库 ≥ 心跳 ⇒ 强制写。
//
// 直接用 gate 层断言时间语义（边界明确、不依赖真实时钟）。
func TestWriteGate_WritesOnHeartbeat(t *testing.T) {
	db := newWriteGateTestDB(t)
	_, device := seedWriteGateDevice(t, db, "node-gate-heartbeat")

	gate := newTelemetryWriteGate(telemetryWritePolicy{heartbeat: 60 * time.Second})
	base := time.Now()
	records := []models.UnifiedData{{DeviceID: device.ID, SensorName: "voltage", Value: 12, Unit: "V", Timestamp: base}}

	// 首帧: write_first。
	if _, stat := gate.filter(device.ID, records, base); stat.First != 1 {
		t.Fatalf("首帧应判定为 write_first，got %+v", stat)
	}
	gate.commit(device.ID, records, base)

	if _, stat := gate.filter(device.ID, records, base.Add(30*time.Second)); stat.Skipped != 1 {
		t.Errorf("30s 后同值应 skip，got %+v", stat)
	}
	// 59.999s: 仍 skip（判据是 >= 心跳，不是 >）。
	if _, stat := gate.filter(device.ID, records, base.Add(60*time.Second-time.Nanosecond)); stat.Skipped != 1 {
		t.Errorf("59.999s 应仍 skip（判据是 >=），got %+v", stat)
	}
	// 恰好 60s: 心跳到点 ⇒ 必写。
	wouldWrite, stat := gate.filter(device.ID, records, base.Add(60*time.Second))
	if stat.Heartbeat != 1 || len(wouldWrite) != 1 {
		t.Errorf("心跳到点应判定为 write_heartbeat 且必须落库，got stat=%+v wouldWrite=%d", stat, len(wouldWrite))
	}
}

// TestWriteGate_WriteFailureDoesNotAdvanceState 场景 4（最重要的一条）:
// 写库失败时**不得**推进状态，否则该值会被永久判成"未变化"而丢失。
func TestWriteGate_WriteFailureDoesNotAdvanceState(t *testing.T) {
	db := newWriteGateTestDB(t)
	node, device := seedWriteGateDevice(t, db, "node-gate-fail")

	consumer, _ := newWriteGateConsumer(t, db, map[string]float64{"voltage": 12})
	consumer.SetTelemetryHeartbeat(time.Hour)
	consumer.Handle(frame(node.NodeID, device.ID))
	if got := countUnifiedRows(t, db, device.ID); got != 1 {
		t.Fatalf("首帧行数 = %d, want 1", got)
	}

	// 让后续 INSERT 全部失败（删表），模拟写库故障。
	if err := db.Migrator().DropTable(&models.UnifiedData{}); err != nil {
		t.Fatalf("drop unified_data: %v", err)
	}

	// 复刻 Handle 的写库段: filter → Create(失败) → **不 commit**。
	now := time.Now()
	changed := []models.UnifiedData{{DeviceID: device.ID, SensorName: "voltage", Value: 99, Unit: "V", Timestamp: now}}
	persist, stat := consumer.writeGate.filter(device.ID, changed, now)
	if stat.Changed != 1 || len(persist) != 1 {
		t.Fatalf("值变化应判定为写，got %+v (persist=%d)", stat, len(persist))
	}
	if err := db.Session(&gorm.Session{}).Create(&persist).Error; err == nil {
		t.Fatal("表已删除，Create 应当失败 —— 用例前提不成立")
	}

	// 关键断言: 状态未推进 ⇒ 同值 99 的下一次判定仍是 write_changed。
	_, stat2 := consumer.writeGate.filter(device.ID, changed, now.Add(time.Second))
	if stat2.Changed != 1 {
		t.Errorf("写库失败后状态被推进了（got %+v）—— 新值被记成\"已落库\"，"+
			"该值此后再也不会落库（方案 §3.3 缓存一致性）", stat2)
	}
}

// TestWriteGate_RestartWritesEverything 场景 5: 进程重启（状态表为空）⇒ 首帧全量写。
func TestWriteGate_RestartWritesEverything(t *testing.T) {
	db := newWriteGateTestDB(t)
	node, device := seedWriteGateDevice(t, db, "node-gate-restart")

	values := map[string]float64{"a": 1, "b": 2, "c": 3}
	first, _ := newWriteGateConsumer(t, db, values)
	first.SetTelemetryHeartbeat(time.Hour)
	first.Handle(frame(node.NodeID, device.ID))
	if got := countUnifiedRows(t, db, device.ID); got != 3 {
		t.Fatalf("首帧行数 = %d, want 3（每字段一行）", got)
	}

	// 模拟重启: 全新 consumer（空状态表）+ 同值输入 ⇒ 仍必须全量写。
	restarted, _ := newWriteGateConsumer(t, db, values)
	restarted.SetTelemetryHeartbeat(time.Hour)
	restarted.Handle(frame(node.NodeID, device.ID))
	if got := countUnifiedRows(t, db, device.ID); got != 6 {
		t.Errorf("重启后首帧行数 = %d, want 6（状态表为空 ⇒ 每字段都按首帧写一遍）", got)
	}
}

// TestWriteGate_NoShadowMode_UnchangedValuesAreSkipped 场景 6: 收窄**无条件生效**。
//
// 历史: 本条用例原名 TestWriteGate_ShadowModeNeverSkips，断言"影子模式下三帧同值
// 一行不少写"。2026-11-07 用户裁决删除开关、L1 无条件生效（契约 §1/§3.3），
// 该前提**已不存在** —— 于是把它反过来钉住新契约:
//
//	三帧同值 + 心跳 1h ⇒ 只写首帧，后两帧必须 skip（否则「删除开关」等于没做）。
//
// 这条同时是「影子模式没有被偷偷留下」的行为级证据（源码级证据是
// TestNoTelemetrySwitchesExist）。
func TestWriteGate_UnchangedValuesAreSkippedUnconditionally(t *testing.T) {
	db := newWriteGateTestDB(t)
	node, device := seedWriteGateDevice(t, db, "node-gate-unconditional")

	consumer, _ := newWriteGateConsumer(t, db, map[string]float64{"voltage": 12})
	consumer.SetTelemetryHeartbeat(time.Hour)

	for i := 0; i < 3; i++ {
		consumer.Handle(frame(node.NodeID, device.ID))
	}
	if got := countUnifiedRows(t, db, device.ID); got != 1 {
		t.Errorf("三帧同值行数 = %d, want 1（首帧必写、后两帧值未变且未到心跳 ⇒ 必须跳过）", got)
	}

	// gate 层再确认一次: 第 2 帧应判 skip 且 persist 为空（不再有"假想跳过"）。
	gate := newTelemetryWriteGate(telemetryWritePolicy{heartbeat: time.Hour})
	rec := []models.UnifiedData{{DeviceID: device.ID, SensorName: "voltage", Value: 12, Timestamp: time.Now()}}
	t0 := time.Now()
	gate.filter(device.ID, rec, t0)
	gate.commit(device.ID, rec, t0)
	persist, stat := gate.filter(device.ID, rec, t0.Add(time.Second))
	if stat.Skipped != 1 {
		t.Errorf("同值未到心跳应判 skip，got %+v", stat)
	}
	if len(persist) != 0 {
		t.Errorf("skip 的行不得出现在 persist 里，got %d 行", len(persist))
	}
}

// TestWriteGate_LatestSinkRunsEvenWhenSkipped 场景 7: latestSink 不受跳过影响。
//
// 方案 §7 阶段1 原文: "同时把 latestSink 调用提到 INSERT 之前（现在它在 Create
// 成功的 else 分支里，跳过会冻结缓存）"。
func TestWriteGate_LatestSinkRunsEvenWhenSkipped(t *testing.T) {
	db := newWriteGateTestDB(t)
	node, device := seedWriteGateDevice(t, db, "node-gate-sink")

	consumer, _ := newWriteGateConsumer(t, db, map[string]float64{"voltage": 12})
	consumer.SetTelemetryHeartbeat(time.Hour)

	var seen []float64
	consumer.SetLatestSink(func(rec models.UnifiedData) { seen = append(seen, rec.Value) })

	consumer.Handle(frame(node.NodeID, device.ID))
	if len(seen) != 1 {
		t.Fatalf("首帧 latestSink 调用次数 = %d, want 1", len(seen))
	}
	// 同值帧会被跳过 INSERT，但 latestSink 仍必须被调用。
	consumer.Handle(frame(node.NodeID, device.ID))
	if len(seen) != 2 {
		t.Errorf("被跳过的帧没有更新 latestSink（调用次数 %d, want 2）—— "+
			"最新值缓存会冻结在旧值，用户看到\"数据不动了\"（方案 §7 阶段1）", len(seen))
	}
	if got := countUnifiedRows(t, db, device.ID); got != 1 {
		t.Errorf("unified_data 行数 = %d, want 1", got)
	}
}

// TestWriteGate_TwoDifferentCachesOnFailure 守「两处缓存口径不同」这条最容易被
// 读错的地方（方案 §3.3 与 §7 阶段1 各说了一半）:
//
//	· **收窄状态表**（last 已落库值 + 时间）: 写库失败 ⇒ **不得**推进。
//	  否则新值被记成"已落库"，后续同值帧全被判成"未变化"而跳过，该值永久丢失
//	  （方案 §3.3「缓存一致性」一行）。
//	· **最新值缓存**（latestSink → api.SetLatestValue）: 写库失败**仍然**要更新。
//	  它表达的是"设备当前状态"，不是"库里有几行"；少更新一次会让告警/自动化/前端
//	  读到旧值（方案 §7 阶段1 要求把 latestSink 提到 INSERT 之前，正是为此）。
//
// 两个断言必须同时成立 —— 只做其一都会引入一类静默数据问题。
func TestWriteGate_TwoDifferentCachesOnFailure(t *testing.T) {
	db := newWriteGateTestDB(t)
	node, device := seedWriteGateDevice(t, db, "node-gate-two-caches")

	consumer, driver := newWriteGateConsumer(t, db, map[string]float64{"voltage": 12})
	consumer.SetTelemetryHeartbeat(time.Hour)

	var seen []float64
	consumer.SetLatestSink(func(rec models.UnifiedData) { seen = append(seen, rec.Value) })
	consumer.Handle(frame(node.NodeID, device.ID))
	if len(seen) != 1 {
		t.Fatalf("首帧 latestSink 调用次数 = %d, want 1", len(seen))
	}

	// 让写库失败。
	if err := db.Migrator().DropTable(&models.UnifiedData{}); err != nil {
		t.Fatalf("drop unified_data: %v", err)
	}
	// 值变化的一帧：**同一个** consumer（状态表里已有 voltage=12），走真实 Handle
	// 路径（latestSink 先跑 → filter 判 write_changed → Create 失败 → 不 commit）。
	driver.values = map[string]float64{"voltage": 42}
	consumer.Handle(frame(node.NodeID, device.ID))

	// 断言 A: 最新值缓存**仍然**被更新（写库失败不影响它）。
	if len(seen) != 2 || seen[1] != 42 {
		t.Errorf("写库失败时 latestSink 仍必须被调用并带新值 42，got %v", seen)
	}
	// 断言 B: 收窄状态表**没有**被推进 ⇒ 同值 42 下一次仍判为 write_changed
	// （而不是 First —— First 说明状态表被清空，那是另一类故障）。
	now := time.Now()
	rec := []models.UnifiedData{{DeviceID: device.ID, SensorName: "voltage", Value: 42, Timestamp: now}}
	_, stat := consumer.writeGate.filter(device.ID, rec, now)
	if stat.Changed != 1 {
		t.Errorf("写库失败后收窄状态被推进了（got %+v，want Changed=1）—— "+
			"新值被记成\"已落库\"，该值此后再也不会落库（方案 §3.3 缓存一致性）", stat)
	}
}

// TestWriteGate_NilGateWritesEverything 场景 8: 未装配（nil gate）必须全量写，
// 绝不能静默开始丢行。
func TestWriteGate_NilGateWritesEverything(t *testing.T) {
	db := newWriteGateTestDB(t)
	_, device := seedWriteGateDevice(t, db, "node-gate-nil")

	var gate *telemetryWriteGate // nil
	records := []models.UnifiedData{
		{DeviceID: device.ID, SensorName: "a", Value: 1},
		{DeviceID: device.ID, SensorName: "b", Value: 2},
	}
	persist, stat := gate.filter(device.ID, records, time.Now())
	if len(persist) != 2 || stat.Total != 2 {
		t.Errorf("nil gate 必须退化为全量写，got persist=%d stat=%+v", len(persist), stat)
	}
	gate.commit(device.ID, records, time.Now()) // 不得 panic
	gate.record(stat)                           // 不得 panic
	gate.reset()                                // 不得 panic
}

// TestWriteGate_NaNIsAlwaysWritten 场景 9: NaN != NaN，带 NaN 的字段永远不会
// 被"值未变"误跳过。
func TestWriteGate_NaNIsAlwaysWritten(t *testing.T) {
	db := newWriteGateTestDB(t)
	_, device := seedWriteGateDevice(t, db, "node-gate-nan")

	gate := newTelemetryWriteGate(telemetryWritePolicy{heartbeat: time.Hour})
	nan := math.NaN()
	rec := []models.UnifiedData{{DeviceID: device.ID, SensorName: "x", Value: nan}}
	t0 := time.Now()
	gate.filter(device.ID, rec, t0)
	gate.commit(device.ID, rec, t0)
	if _, stat := gate.filter(device.ID, rec, t0.Add(time.Second)); stat.Changed != 1 {
		t.Errorf("NaN 值必须每帧都写（NaN != NaN），got %+v", stat)
	}
}
