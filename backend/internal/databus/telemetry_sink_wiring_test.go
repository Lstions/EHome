package databus

// telemetry_sink_wiring_test.go — L2 双写 / L3 rollup / 最新值缓存的接线门禁。
//
// # 2026-11-13 重写（unified_data 整表退役）
//
// 原文件的全部用例都以「L1 门禁把某一帧判为 skip，但 sink 仍必须被调用」为构造。
// unified_data 退役后 L1 门禁与 DB 写入一起删除，那个前提不复存在 —— 但**它要
// 防止的那类缺陷仍然存在**：sink 被接到某个"写库成功"分支上（写库已不存在，
// 等价于"接到一个永不进入的分支"）⇒ 图表/rollup 静默无数据。
//
// 因此本文件改为直接断言「解析成功路径」的三个不变量：
//   1. 每一帧解析成功都必须调用 L2/L3/latest 三个 sink（各一次，不重不漏）；
//   2. 传参口径：EdgeDevice.ID / 同一个 now / commandTemplateID / 重组后的 raw；
//   3. nil sink 安全（默认关闭零成本），解析失败不得调用任何 sink。

import (
	"errors"
	"testing"
	"time"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
	"ehome/backend/internal/telemetry"
	"ehome/backend/pkg/parser"

	"gorm.io/gorm"
)

// sinkTestDriver 产生一组固定物理量；err 非 nil 时解析失败。
type sinkTestDriver struct {
	values map[string]float64
	err    error
}

func (d *sinkTestDriver) DeviceType() string      { return "sink_test" }
func (d *sinkTestDriver) DeviceName() string      { return "sink test" }
func (d *sinkTestDriver) OEM() string             { return "test" }
func (d *sinkTestDriver) Category() string        { return "test" }
func (d *sinkTestDriver) HardwareTypes() []string { return []string{"UART"} }
func (d *sinkTestDriver) GetSensorDefinitions() []drivers.SensorData { return nil }
func (d *sinkTestDriver) GetCommandTemplates() []drivers.CommandTemplate { return nil }
func (d *sinkTestDriver) ParseData([]byte) ([]drivers.SensorData, error) {
	if d.err != nil {
		return nil, d.err
	}
	out := make([]drivers.SensorData, 0, len(d.values))
	for name, value := range d.values {
		out = append(out, drivers.SensorData{Name: name, Value: value, Unit: "V"})
	}
	return out, nil
}

func newSinkTestConsumer(t *testing.T, db *gorm.DB, drv drivers.Driver) (*SensorParserConsumer, models.Node, models.EdgeDevice) {
	t.Helper()
	node := models.Node{NodeID: "node-sink", Status: "online"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	ch := models.Channel{NodeID: node.NodeID, HardwareID: "UART0"}
	if err := db.Create(&ch).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	device := models.EdgeDevice{Name: "sink-dev", NodeID: node.NodeID, ChannelID: ch.ID,
		Type: "sink_test", Status: "active", Enabled: true}
	if err := db.Create(&device).Error; err != nil {
		t.Fatalf("create edge device: %v", err)
	}
	registry := drivers.NewRegistry()
	registry.Register(drv)
	return NewSensorParserConsumerWithRegistry(db, nil, passthroughReassembler{}, registry), node, device
}

func sinkFrame(nodeID string, deviceID uint) DataEvent {
	return DataEvent{DeviceID: nodeID, EdgeDeviceID: uint64(deviceID), RequestID: 1, RawData: []byte("response")}
}

func TestTelemetrySinksCalledOnEveryParsedFrame(t *testing.T) {
	db := newConsumerTestDB(t)
	consumer, node, device := newSinkTestConsumer(t, db, &sinkTestDriver{values: map[string]float64{"voltage": 12}})

	var (
		l2Calls, l3Calls, latestCalls int
		l2Devices                     []uint
		l3Devices                     []uint
		l3Fields                      []int
		l2Raw                         [][]byte
		l2Ats, l3Ats                  []time.Time
	)
	consumer.SetTelemetrySink(func(deviceID uint, at time.Time, commandTemplateID int64, readings []telemetry.Reading, raw []byte) {
		l2Calls++
		l2Devices = append(l2Devices, deviceID)
		l2Ats = append(l2Ats, at)
		l2Raw = append(l2Raw, append([]byte(nil), raw...))
	})
	consumer.SetTelemetryRollupSink(func(deviceID uint, fields []parser.Field, at time.Time) {
		l3Calls++
		l3Devices = append(l3Devices, deviceID)
		l3Fields = append(l3Fields, len(fields))
		l3Ats = append(l3Ats, at)
	})
	consumer.SetLatestSink(func(models.UnifiedData) { latestCalls++ })

	consumer.Handle(sinkFrame(node.NodeID, device.ID))
	consumer.Handle(sinkFrame(node.NodeID, device.ID))

	if l2Calls != 2 || l3Calls != 2 || latestCalls != 2 {
		t.Fatalf("sink 调用次数 L2=%d L3=%d latest=%d, want 2/2/2（每一帧解析成功都必须到三个 sink，"+
			"任何一个接到永不进入的分支都会静默少数据）", l2Calls, l3Calls, latestCalls)
	}
	for i := range l2Devices {
		if l2Devices[i] != device.ID {
			t.Errorf("L2 第 %d 次 deviceID = %d, want %d（必须是 EdgeDevice.ID）", i, l2Devices[i], device.ID)
		}
		if l2Ats[i].IsZero() {
			t.Errorf("L2 第 %d 次 at 为零值 —— 三处 sink 必须用同一个 now", i)
		}
		if len(l2Raw[i]) == 0 {
			t.Errorf("L2 第 %d 次 raw 为空 —— 关键帧 raw 落库是原始字节的唯一追溯来源", i)
		}
	}
	for i := range l3Devices {
		if l3Devices[i] != device.ID {
			t.Errorf("L3 第 %d 次 deviceID = %d, want %d", i, l3Devices[i], device.ID)
		}
		if l3Fields[i] != 1 {
			t.Errorf("L3 第 %d 次 fields 数 = %d, want 1（应传本帧全部物理量）", i, l3Fields[i])
		}
		if l3Ats[i].IsZero() {
			t.Errorf("L3 第 %d 次 at 为零值", i)
		}
	}
}

// nil sink 安全：默认关闭时不得 panic，也不得影响 latestSink。
func TestTelemetrySinksNilAreSafe(t *testing.T) {
	db := newConsumerTestDB(t)
	consumer, node, device := newSinkTestConsumer(t, db, &sinkTestDriver{values: map[string]float64{"voltage": 12}})

	latestCalls := 0
	consumer.SetLatestSink(func(models.UnifiedData) { latestCalls++ })
	consumer.Handle(sinkFrame(node.NodeID, device.ID)) // 不应 panic

	if latestCalls != 1 {
		t.Fatalf("latestSink 调用次数 = %d, want 1（nil 的 L2/L3 不得拖累最新值缓存）", latestCalls)
	}
}

// 解析失败不得调用任何 sink：否则会在帧表里写入"半帧"或让 rollup 吃到空样本。
func TestTelemetrySinksNotCalledOnParseError(t *testing.T) {
	db := newConsumerTestDB(t)
	consumer, node, device := newSinkTestConsumer(t, db, &sinkTestDriver{err: errors.New("bad frame")})

	calls := 0
	consumer.SetTelemetrySink(func(uint, time.Time, int64, []telemetry.Reading, []byte) { calls++ })
	consumer.SetTelemetryRollupSink(func(uint, []parser.Field, time.Time) { calls++ })
	consumer.SetLatestSink(func(models.UnifiedData) { calls++ })

	consumer.Handle(sinkFrame(node.NodeID, device.ID))

	if calls != 0 {
		t.Fatalf("解析失败仍调用了 %d 次 sink —— 帧表/rollup 会吃到不存在的样本", calls)
	}
}
