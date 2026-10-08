package databus

// S3 量化探针 (2026-10-07)。
//
// 审计 S3 断言: 同一个 DataEvent 会被**两个**消费者各写一行 device_data。
// 本文件先把这件事**测出来**, 再谈修法 —— 没有测量就动手等于改一个自己没确认的重复。
//
// 探针走**真实总线** (DataEventBus.Publish → fanout → 两个消费者), 不直接调 Handle,
// 否则就绕过了"两个消费者都会收到同一个事件"这个待验证的前提。
//
// 结论 (被下方用例钉住):
//   · 可解析事件 → 2 行 (两个消费者各一行, 形状不同);
//   · 三类不可解析事件 → **只有 1 行** (db_persist), 即两个写入者覆盖面不同,
//     不是纯冗余 —— 这是 Lead task-16 裁决"两个写入者都保留"的依据。
//
// 等待一律**确定性**: 不靠固定 Sleep。见 s3WaitForRows 与 s3BarrierConsumer。

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

// TestS3_Probe_OneEventWritesTwoRows 是 S3 的**量化测量**。
//
// 一个可解析 DataEvent 之后 device_data 恰好 2 行, 且两行形状不同、
// DeviceID 语义不同 (一个 0, 一个真实设备 id)。测量结果同时打印出来供报告引用。
func TestS3_Probe_OneEventWritesTwoRows(t *testing.T) {
	f := newS3Fixture(t)
	bus, barrier := s3WiredBus(t, f)

	bus.Publish(s3Sample(f))

	// 确定性屏障: 等解析者 Handle 返回 (它一定会被调用 —— ShouldParse 为真)。
	select {
	case <-barrier.done:
	case <-time.After(2 * time.Second):
		t.Fatal("2s 内解析者未完成 Handle —— 无法确定性断言行数")
	}

	rows := s3WaitForRows(t, f.db, 2, "可解析事件应写 2 行")
	t.Logf("=== S3 测量: 1 个 DataEvent → %d 行 device_data ===", len(rows))
	for i, row := range rows {
		t.Logf("row[%d] id=%d device_id=%d node_id=%q logical_device_id=%v",
			i, row.ID, row.DeviceID, row.NodeID, row.LogicalDeviceID)
		t.Logf("        data_json=%s", row.DataJSON)
		t.Logf("        data_json 长度=%d B", len(row.DataJSON))
	}

	// 两个写入者形状必须不同, 否则"重复"可能只是无害的镜像。
	if rows[0].DataJSON == rows[1].DataJSON {
		t.Fatalf("两行 data_json 完全相同, 不是审计描述的双形状: %s", rows[0].DataJSON)
	}
	// DeviceID 语义必须不同: db_persist 不设 DeviceID(0), sensor_parser 设真实 id。
	zeroCount := 0
	for _, row := range rows {
		if row.DeviceID == 0 {
			zeroCount++
		}
	}
	if zeroCount != 1 {
		t.Fatalf("期望恰好 1 行的 device_id=0, 实际 %d 行", zeroCount)
	}
	t.Logf("=== 测量结论: 1 事件 → 2 行, 形状不同, DeviceID 语义不同 ===")
}

// TestS3_Probe_NonParseableEventWritesOnlyOneRow 是 S3 的**反面测量**。
//
// 证明"两类不可解析事件只写 1 行"。这是"两个写入者覆盖面不同、不是纯冗余"的
// 直接证据, 也是"不能删 DBPersistConsumer"的依据。
//
// 确定性: 对 ShouldHandle 为假的输入, 解析者**根本不会被调用** —— 这一点由
// 本用例直接断言 barrier.ShouldHandle(evt)==false 来确立 (纯函数, 无时序),
// 因此"它不会写"不依赖任何等待。
func TestS3_Probe_NonParseableEventWritesOnlyOneRow(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*DataEvent)
	}{
		{
			name: "error_code != 0 (关键样本, 0x03 独立上报)",
			mut:  func(e *DataEvent) { e.ErrorCode = 0x1100 },
		},
		{
			name: "raw_data 为空 (无可解析载荷)",
			mut:  func(e *DataEvent) { e.RawData = nil },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newS3Fixture(t)
			bus, barrier := s3WiredBus(t, f)

			evt := s3Sample(f)
			tc.mut(&evt)

			// ① 先确立"解析者不会被调用" —— 纯函数判定, 无时序假设。
			if barrier.ShouldHandle(evt) {
				t.Fatalf("本用例前提被破坏: 该输入下 ShouldParse() 应为假, 否则不构成只有 db_persist 会写的情形")
			}

			bus.Publish(evt)

			// ② db_persist 那一行必然出现 (ShouldPersist 为真) —— 确定性轮询。
			rows := s3WaitForRows(t, f.db, 1, "不可解析事件应写 1 行 (仅 db_persist)")

			// ③ 解析者不可能被调用 (①), 故此刻行数仍为 1 即为定论。
			if again := deviceDataRows(t, f.db); len(again) != 1 {
				t.Fatalf("行数从 1 变为 %d —— 解析者在 ShouldHandle=false 时仍写入了", len(again))
			}

			t.Logf("=== S3 测量[%s]: 1 个 DataEvent → %d 行 device_data ===", tc.name, len(rows))
			for i, row := range rows {
				t.Logf("  row[%d] device_id=%d data_json=%s", i, row.DeviceID, row.DataJSON)
			}
			if rows[0].DeviceID != 0 {
				t.Fatalf("唯一那行应来自 db_persist (device_id=0), 实际 device_id=%d", rows[0].DeviceID)
			}
			select {
			case <-barrier.done:
				t.Fatal("解析者屏障被触发, 与 ShouldHandle=false 矛盾")
			default:
			}
		})
	}
}

// TestS3_Probe_UnparseableByDriverWritesOnlyOneRow 覆盖"ShouldHandle 为真但解析被拒"。
//
// 这类事件 ShouldParse() 为真 (解析者**会**被调用), 但在 Handle 内部因校准缺失
// 提前 return —— 结果同样只有 db_persist 那一行。
//
// 它说明: 即便把 DBPersistConsumer.ShouldHandle 收窄成 !ShouldParse(),
// 也无法覆盖这一类, 因为"是否写入"取决于 Handle 内部的运行期结果。
//
// 确定性: 这类**会**调用解析者, 所以用 barrier.done 做屏障 (Handle 返回即写完),
// 不靠时长猜测。
func TestS3_Probe_UnparseableByDriverWritesOnlyOneRow(t *testing.T) {
	f := newS3Fixture(t)
	// 删掉校准: bmp280 是校准感知驱动, 缺失即拒收 (consumers_heavy.go 校准门)。
	if err := f.db.Where("edge_device_id = ?", f.device.ID).Delete(&models.CalibrationCache{}).Error; err != nil {
		t.Fatal(err)
	}
	bus, barrier := s3WiredBus(t, f)

	evt := s3Sample(f)
	// 前提: 该输入下解析者**会**被调用 (否则测的不是这一档)。
	if !barrier.ShouldHandle(evt) {
		t.Fatal("本用例前提被破坏: 校准缺失时 ShouldParse() 应为真, 否则覆盖不到 Handle 内拒收这一档")
	}

	bus.Publish(evt)

	// 等解析者 Handle 返回 (它内部已因校准缺失提前 return)。
	select {
	case <-barrier.done:
	case <-time.After(2 * time.Second):
		t.Fatal("2s 内解析者未完成 Handle —— 无法确定性断言行数")
	}

	rows := s3WaitForRows(t, f.db, 1, "校准缺失时解析被拒, 应只写 1 行")
	t.Logf("=== S3 测量[校准缺失, Handle 内拒收]: 1 个 DataEvent → %d 行 device_data ===", len(rows))
	for i, row := range rows {
		t.Logf("  row[%d] device_id=%d data_json=%s", i, row.DeviceID, row.DataJSON)
	}
}
