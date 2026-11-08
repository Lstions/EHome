package databus

// s3_fixture_test.go —— S3 探针与 task19 端到端用例共用的**测试夹具**。
//
// ============ 审计导航（读这一节就够，不用看 git history）============
//
// **本文件只有夹具，没有一个 Test 函数。**
// 要审计「S3 相关的门禁在哪」，看 s3_duplicate_device_data_probe_test.go。
//
// 可以随时验证这一条：
//
//	grep -c '^func Test' backend/internal/databus/s3_fixture_test.go   # => 0
//	grep -c '^func Test' backend/internal/databus/s3_duplicate_device_data_probe_test.go
//
// 历史（2026-11-08）：本文件原叫 s3_duplicate_device_data_probe_test.go，
// 那个名字让审计者以为里面全是被测物，实际是 8 个夹具 + 3 条探针混在一起。
// 拆开之后两个名字各自名实相符。
//
// ============ 为什么它从 s3_duplicate_device_data_probe_test.go 里拆出来 ============
//
// 原文件名字叫 probe，但里面同时住着两类东西：
//   · 8 个夹具（本文件）；
//   · 3 条试真正断言行为的探针测试（留在 s3_duplicate_device_data_probe_test.go）。
//
// 后者被 device_data_write_shape_task19_test.go 使用（newS3Fixture / s3WaitForRows），
// 也就是说这个文件其实是**测试基础设施**。名字带 probe 会让下一个做同样清理的人
// 把它当临时物删掉 —— 实测移走它直接编译失败：
//
//	vet: device_data_write_shape_task19_test.go:128: undefined: newS3Fixture
//
// 拆开之后两个名字都准确：本文件只放夹具，探针文件只放断言。
//
// ============ 夹具解决的具体问题 ============
//
// 1) SQLite `:memory:` 给**每条新连接**一个独立的空库，而总线上的两个消费者
//    各在自己的 goroutine 里跑、会拿到不同连接 ⇒ 必须把连接池钉为 1，
//    否则测试会看到 "no such table: device_data" —— 一个与 S3 无关的假失败。
// 2) bmp280 是**校准感知**驱动：缺 CalibrationCache 会被消费者 fail-closed 拒收，
//    那样永远走不到"解析形状那次写入"，探针会得出错误的"只有一行"结论。
// 3) 断言"只有 1 行"是否定性断言，不能靠 sleep 猜。s3BarrierConsumer 在**内层
//    Handle 返回后**关 channel，把"写完了"变成可观测的确定性事件。

import (
	"encoding/hex"
	"sync"
	"testing"
	"time"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// s3Fixture 造出"一个可解析的调度采样"所需的全部上下文。
type s3Fixture struct {
	db     *gorm.DB
	node   models.Node
	device models.EdgeDevice
}

func newS3Fixture(t *testing.T) s3Fixture {
	t.Helper()
	// 必须自己建库并把连接池钉为 1:
	//   · SQLite `:memory:` 给**每条新连接**一个独立的空库;
	//   · 总线上的两个消费者各自在自己的 goroutine 里跑, 会拿到不同连接;
	//   · 不钉池的话, 消费者写的那个"库"与测试读的那个"库"不是同一个,
	//     测试会看到 "no such table: device_data" —— 一个与 S3 无关的假失败。
	// (handler_data_test.go / datapipe_test.go 同款处理。)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.AutoMigrate(&models.Node{}, &models.Channel{}, &models.EdgeDevice{},
		&models.UnifiedData{}, &models.DeviceData{}, &models.CalibrationCache{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	node := models.Node{NodeID: "S3-NODE", Name: "s3"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	channel := models.Channel{NodeID: node.NodeID, HardwareID: "I2C0"}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	device := models.EdgeDevice{Name: "s3-sensor", NodeID: node.NodeID, ChannelID: channel.ID, Type: "bmp280", Status: "active"}
	if err := db.Create(&device).Error; err != nil {
		t.Fatal(err)
	}
	// bmp280 是"校准感知"驱动: 缺 CalibrationCache 会被消费者 fail-closed 拒收,
	// 那样就永远走不到解析形状那次写入, 探针会得出错误的"只有一行"结论。
	calibration := []byte{0x70, 0x6b, 0x43, 0x67, 0x18, 0xfc, 0x7d, 0x8e, 0x43, 0xd6, 0xd0, 0x0b, 0x27, 0x0b, 0x8c, 0x00, 0xf9, 0xff, 0x8c, 0x3c, 0xf8, 0xc6, 0x70, 0x17}
	if err := db.Create(&models.CalibrationCache{
		NodeID: node.NodeID, EdgeDeviceID: device.ID, DeviceType: device.Type,
		Data: hex.EncodeToString(calibration),
	}).Error; err != nil {
		t.Fatal(err)
	}
	return s3Fixture{db: db, node: node, device: device}
}

// s3Sample 是一条会被两个消费者同时接收的调度采样。
func s3Sample(f s3Fixture) DataEvent {
	return DataEvent{
		DeviceID: f.node.NodeID, EdgeDeviceID: uint64(f.device.ID), ChannelID: uint64(f.device.ChannelID),
		RequestID: 1, Sequence: 7, RawData: []byte{0x65, 0x5a, 0xc0, 0x7e, 0xed, 0x00},
		ReceivedAt: time.Now(),
	}
}

// s3BarrierConsumer 包住 SensorParserConsumer, 在**它的 Handle 返回后**发信号。
//
// 为什么需要它: 断言"只有 1 行"是**否定性**断言 —— 若只是 sleep 一小段就断言,
// 有可能把"解析者还没来得及写"误判成"解析者不会写"。用 Handle 的**返回**做屏障,
// 就把"写完了"变成可观测的确定性事件, 不再依赖时长猜测。
type s3BarrierConsumer struct {
	inner *SensorParserConsumer
	done  chan struct{}
	once  sync.Once
}

func (c *s3BarrierConsumer) Name() string { return "sensor_parser" } // 与内层同名: 注册键一致

func (c *s3BarrierConsumer) ShouldHandle(evt DataEvent) bool {
	return c.inner.ShouldHandle(evt)
}

func (c *s3BarrierConsumer) Handle(evt DataEvent) {
	c.inner.Handle(evt)
	c.once.Do(func() { close(c.done) })
}

// s3WiredBus 把两个"会写 device_data"的消费者挂到真实总线上,
// 并返回解析侧的完成屏障 (若解析者从未被调用, 屏障永不关闭 —— 调用方需自行判断)。
func s3WiredBus(t *testing.T, f s3Fixture) (*DataEventBus, *s3BarrierConsumer) {
	t.Helper()
	registry := drivers.NewRegistry()
	registry.Register(&drivers.BMP280Driver{})
	inner := NewSensorParserConsumerWithRegistry(f.db, nil, passthroughReassembler{}, registry)
	barrier := &s3BarrierConsumer{inner: inner, done: make(chan struct{})}
	bus := NewDataEventBus()
	bus.Register(NewDBPersistConsumer(f.db))
	bus.Register(barrier)
	t.Cleanup(bus.Stop)
	return bus, barrier
}

// deviceDataRows 读出全部 device_data 行 (按 id 升序 = 写入顺序)。
func deviceDataRows(t *testing.T, db *gorm.DB) []models.DeviceData {
	t.Helper()
	var rows []models.DeviceData
	if err := db.Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

// s3WaitForRows 轮询到行数**恰好**为 want, 或超时后**显式失败** (不静默继续)。
//
// 用"恰好相等"而不是"大于等于": 多出来的行同样是缺陷, 不该被 >= 掩盖。
func s3WaitForRows(t *testing.T, db *gorm.DB, want int, ctx string) []models.DeviceData {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var last []models.DeviceData
	for time.Now().Before(deadline) {
		last = deviceDataRows(t, db)
		if len(last) == want {
			return last
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("%s: 2s 内 device_data 行数未达到 %d (实际 %d)", ctx, want, len(last))
	return nil
}
