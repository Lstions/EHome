package nodemgr

import (
	_ "ehome/backend/pkg/logger"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"ehome/backend/internal/databus"
	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
	"ehome/backend/pkg/logger"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func init() {
	_ = logger.Init("warn")
}

// newDataPipeTestRegistry 构造含全部内置驱动的注册表(供保留用例使用)。
func newDataPipeTestRegistry() *drivers.Registry {
	registry := drivers.NewRegistry()
	drivers.RegisterBuiltInDrivers(registry)
	return registry
}

// =============================================================================
// F1 迁移映射表 (datapipe_test.go 旧用例 → DataEventBus + SensorParserConsumer)
//
// 原测试文件直接调用 handler_data.go 的私有函数 parseAndStoreData /
// findEdgeDeviceByChannelID。这两个函数是 databus 迁移前的死代码双实现,
// 生产路径 handleDataReport → processDataReportJob → dataBus.Publish →
// SensorParserConsumer 已完全不经过它们。本文件将原断言逐条迁移到
// databus 消费者语义,并记录 4 处实质行为差异(全部接受新语义):
//
//  1. 校准语义   : 原路径靠 driver.ParseData 自身报错;新消费者强制查
//     CalibrationCache,缺失即拒收 (consumers_heavy.go:195-209)。
//     断言结果相同、触发机制不同。                                     → 接受新语义
//  2. 寻址语义   : 原 findEdgeDeviceByChannelID 直查 channel_id 不带 node_id
//     限定(跨节点碰撞隐患),edge_device_id 查不到 fall through;新消费者
//     edgeDeviceID 失败直接 return,channel 直查带 node_id 限定
//     (consumers_heavy.go:141-158)。                                    → 接受新语义(更严)
//  3. DeviceData : 原 if collectorID > 0 才写;新消费者无条件写
//     (consumers_heavy.go:299)。                                        → 接受新语义
//  4. WS 事件    : 原只发 DataUpdate;新消费者另发 ChannelData
//     (consumers_heavy.go:324-341)。前端 useRealtimeData 已订阅。      → 接受新语义
//
// 原断言点 → 新断言点 逐条对照:
//
//	TestDataPipeline_EndToEnd (:29-84)
//	  :68  parseAndStoreData(未校准 BMP280) → TestDataPipeline_EndToEnd (bus+consumer)
//	  :73-76  unified_data 为空            → assertUnifiedForDevice(0)
//	  :79-83  device_data 为空             → assertDeviceDataForDevice(0)
//
//	TestDataPipeline_UnknownDevice (:88-107)
//	  :100 parseAndStoreData(无设备通道)    → TestDataPipeline_UnknownDevice
//	  :103-106 unified_data 计数为 0       → assertUnifiedCount(0)
//
//	TestDataPipeline_EmptyRaw (:110-130)
//	  :123 parseAndStoreData(raw=[])       → TestDataPipeline_EmptyRaw
//	  :126-129 unified_data 计数为 0       → assertUnifiedCount(0)
//
//	TestFindEdgeDeviceByChannelID_C6IndexFallback (:206-305)
//	  :264 direct channel_id 命中          → "direct channel id" 行 (node-scoped)
//	  :274 C6 index 0 → ch1               → TestDataPipeline_C6ChannelIndexFallback
//	  :283 C6 index 1 → ch2               → TestDataPipeline_C6ChannelIndexFallback
//	  :292 edge_device_id 直查             → "edge device id" 行 (PK 精确)
//	  :301 越界 index 99 → not found      → "out of range index" 行 + "cross node" 行
//
// 保留且不迁移:TestDriverRegistry / TestBMP280Driver_Parse /
// TestBMP280Driver_GetSensorDefinitions / TestBMP280Driver_TooShort /
// TestBackpressure_Fallback / TestDriverRegistry_Isolated —— 这些用例不依赖
// 被删除的私有函数,继续保留其独立覆盖价值。
// =============================================================================

// passthroughReassembler 直接透传数据、不保留状态,用于不关心重组缓冲的用例。
type passthroughReassembler struct{}

func (passthroughReassembler) Append(requestID uint32, data []byte) []byte { return data }
func (passthroughReassembler) Consume(requestID uint32)                    {}

// datapipeSignalingConsumer 包装 SensorParserConsumer,在 Handle 返回后计数。
// SensorParserConsumer 的寻址失败路径(consumers_heavy.go:141-158)直接 return、
// 不调用 reassembler.Consume,因此重组缓冲不能作为全部路径的完成信号;
// Handle 返回则覆盖所有路径(成功/失败/拒收)。
// processed 被 bus worker goroutine 写入、测试 goroutine 读取,必须原子访问。
type datapipeSignalingConsumer struct {
	inner     *databus.SensorParserConsumer
	processed atomic.Int64
}

func (c *datapipeSignalingConsumer) Name() string { return c.inner.Name() }
func (c *datapipeSignalingConsumer) ShouldHandle(evt databus.DataEvent) bool {
	return c.inner.ShouldHandle(evt)
}
func (c *datapipeSignalingConsumer) Handle(evt databus.DataEvent) {
	c.inner.Handle(evt)
	c.processed.Add(1)
}

// datapipeEventBusTestConsumer 构建绑定 SensorParserConsumer 的 DataEventBus
// 测试夹具,end-to-end 覆盖 Publish → fanout → 消费者 Handle 全链路
// (与原 parseAndStoreData 直接调用不同,这覆盖了生产实际路径)。
type datapipeEventBusTestConsumer struct {
	bus      *databus.DataEventBus
	consumer *databus.SensorParserConsumer
	proxy    *datapipeSignalingConsumer
	posted   int64 // 已发布且 ShouldParse=true 的事件数(按序处理,processed>=posted 即完成)
}

func newDatapipeEventBusConsumer(db *gorm.DB, deviceActivity ...func(uint)) *datapipeEventBusTestConsumer {
	registry := drivers.NewRegistry()
	drivers.RegisterBuiltInDrivers(registry)
	consumer := databus.NewSensorParserConsumerWithRegistry(db, nil, nil, passthroughReassembler{}, registry, deviceActivity...)
	proxy := &datapipeSignalingConsumer{inner: consumer}
	bus := databus.NewDataEventBus()
	bus.Register(proxy)
	return &datapipeEventBusTestConsumer{bus: bus, consumer: consumer, proxy: proxy}
}

func (c *datapipeEventBusTestConsumer) Close() { c.bus.Stop() }

// postAndDrain 发布事件并等待消费者处理完成。DataEventBus 的 dispatch 与
// 每消费者 worker 均为单 goroutine,事件按发布顺序处理;processed 计数达到
// 已发布 ShouldParse 事件数即证明本事件已处理完毕。ShouldParse=false 的事件
// 不会进入消费者(ShouldHandle 短路),对 DB 的影响确定性为无,无需等待。
func (c *datapipeEventBusTestConsumer) postAndDrain(t *testing.T, evt databus.DataEvent) {
	t.Helper()
	c.bus.Publish(evt)
	if !evt.ShouldParse() {
		return
	}
	c.posted++
	target := c.posted
	eventuallyDatapipe(t, func() bool { return c.proxy.processed.Load() >= target },
		fmt.Sprintf("consumer did not finish processing event #%d", target))
}

func eventuallyDatapipe(t *testing.T, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(message)
}

// datapipePipelineDB 建立管道测试专用数据库。
// 注意:Channel.NodeID 必须存节点序列号(node.NodeID),不能存数字主键字串——
// 生产 channels.node_id 就是序列号,新消费者按 node_id=序列号 寻址;存数字串
// 会让寻址静默走"未找到设备"路径。这是 F1 迁移的关键 fixture 修正。
func datapipePipelineDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&models.Node{}, &models.Channel{}, &models.EdgeDevice{},
		&models.CalibrationCache{}, &models.UnifiedData{}, &models.DeviceData{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func datapipeCreateNode(db *gorm.DB, serial string) models.Node {
	node := models.Node{NodeID: serial, Model: "ESP32S3", FirmwareVersion: "1.0.0", Status: "online"}
	if err := db.Create(&node).Error; err != nil {
		panic(err)
	}
	return node
}

func datapipeCreateChannel(db *gorm.DB, nodeSerial string, hwID string) models.Channel {
	ch := models.Channel{NodeID: nodeSerial, HardwareID: hwID, IntervalMs: 5000, Enabled: true}
	if err := db.Create(&ch).Error; err != nil {
		panic(err)
	}
	return ch
}

func datapipeCreateDevice(db *gorm.DB, name, nodeSerial string, channelID uint, devType string) models.EdgeDevice {
	dev := models.EdgeDevice{Name: name, NodeID: nodeSerial, ChannelID: channelID, Type: devType}
	if err := db.Create(&dev).Error; err != nil {
		panic(err)
	}
	return dev
}

func assertUnifiedCount(t *testing.T, db *gorm.DB, want int, ctx string) {
	t.Helper()
	var count int64
	if err := db.Model(&models.UnifiedData{}).Count(&count).Error; err != nil {
		t.Fatalf("[%s] count unified_data: %v", ctx, err)
	}
	if int(count) != want {
		t.Errorf("[%s] unified_data count = %d, want %d", ctx, count, want)
	}
}

func assertDeviceDataCount(t *testing.T, db *gorm.DB, want int, ctx string) {
	t.Helper()
	var count int64
	if err := db.Model(&models.DeviceData{}).Count(&count).Error; err != nil {
		t.Fatalf("[%s] count device_data: %v", ctx, err)
	}
	if int(count) != want {
		t.Errorf("[%s] device_data count = %d, want %d", ctx, count, want)
	}
}

func assertUnifiedForDevice(t *testing.T, db *gorm.DB, deviceID uint, want int, ctx string) {
	t.Helper()
	var count int64
	if err := db.Model(&models.UnifiedData{}).Where("device_id = ?", deviceID).Count(&count).Error; err != nil {
		t.Fatalf("[%s] count unified_data: %v", ctx, err)
	}
	if int(count) != want {
		t.Errorf("[%s] unified_data(device=%d) = %d, want %d", ctx, deviceID, count, want)
	}
}

func assertDeviceDataForDevice(t *testing.T, db *gorm.DB, deviceID uint, want int, ctx string) {
	t.Helper()
	var count int64
	if err := db.Model(&models.DeviceData{}).Where("device_id = ?", deviceID).Count(&count).Error; err != nil {
		t.Fatalf("[%s] count device_data: %v", ctx, err)
	}
	if int(count) != want {
		t.Errorf("[%s] device_data(device=%d) = %d, want %d", ctx, deviceID, count, want)
	}
}

func countUnifiedForDevice(t *testing.T, db *gorm.DB, deviceID uint, ctx string) int {
	t.Helper()
	var count int64
	if err := db.Model(&models.UnifiedData{}).Where("device_id = ?", deviceID).Count(&count).Error; err != nil {
		t.Fatalf("[%s] count unified_data: %v", ctx, err)
	}
	return int(count)
}

func countDeviceDataForDevice(t *testing.T, db *gorm.DB, deviceID uint, ctx string) int {
	t.Helper()
	var count int64
	if err := db.Model(&models.DeviceData{}).Where("device_id = ?", deviceID).Count(&count).Error; err != nil {
		t.Fatalf("[%s] count device_data: %v", ctx, err)
	}
	return int(count)
}

// TestDataPipeline_UnknownDevice:通道存在但无设备 → 静默跳过不落库
func TestDataPipeline_UnknownDevice(t *testing.T) {
	db := datapipePipelineDB(t)
	node := datapipeCreateNode(db, "3002")
	ch := datapipeCreateChannel(db, node.NodeID, "1")

	fc := newDatapipeEventBusConsumer(db)
	defer fc.Close()

	// 新语义:未知设备 → 未找到 edge_device → return 且无任何落库
	fc.postAndDrain(t, databus.DataEvent{
		DeviceID: node.NodeID, ChannelID: uint64(ch.ID),
		RequestID: 1, RawData: []byte{1, 2, 3, 4, 5, 6},
	})
	assertUnifiedCount(t, db, 0, "unknown device")
	assertDeviceDataCount(t, db, 0, "unknown device")
}

// TestDataPipeline_EmptyRaw:空 payload(ShouldParse→false)不进入消费者处理
func TestDataPipeline_EmptyRaw(t *testing.T) {
	db := datapipePipelineDB(t)
	node := datapipeCreateNode(db, "3003")
	ch := datapipeCreateChannel(db, node.NodeID, "1")
	datapipeCreateDevice(db, "sn3000-empty", node.NodeID, ch.ID, "sn3000")

	fc := newDatapipeEventBusConsumer(db)
	defer fc.Close()

	fc.postAndDrain(t, databus.DataEvent{
		DeviceID: node.NodeID, ChannelID: uint64(ch.ID),
		RequestID: 1, RawData: []byte{},
	})
	// 空数据在 bus 分发阶段即被 ShouldParse 过滤,消费者不执行
	assertUnifiedCount(t, db, 0, "empty raw")
	assertDeviceDataCount(t, db, 0, "empty raw")
}

// TestDataPipeline_EndToEnd:完整链路(未校准 BMP280 fail-closed)
// 原断言 :73-76 期望 0 条 unified_data、:79-83 期望 0 条 device_data。
// 原实现靠 BMP280Driver.ParseData 自身拒绝未校准样本;新消费者在进入
// driver 前强制查 CalibrationCache,缺失即拒收 (consumers_heavy.go:195-209)。
func TestDataPipeline_EndToEnd(t *testing.T) {
	db := datapipePipelineDB(t)
	node := datapipeCreateNode(db, "3001")
	ch := datapipeCreateChannel(db, node.NodeID, "1")
	dev := datapipeCreateDevice(db, "BMP280-Test", node.NodeID, ch.ID, "bmp280")

	fc := newDatapipeEventBusConsumer(db, func(uint) {})
	defer fc.Close()

	rawData := []byte{0x00, 0x41, 0x6e, 0xeb, 0x67, 0x32}
	fc.postAndDrain(t, databus.DataEvent{
		DeviceID: node.NodeID, ChannelID: uint64(ch.ID), EdgeDeviceID: uint64(dev.ID),
		RequestID: 1, RawData: rawData,
	})
	// 校准缺失 → 拒收:unified_data 与 device_data 均为 0
	assertUnifiedForDevice(t, db, dev.ID, 0, "uncalibrated bmp280")
	assertDeviceDataForDevice(t, db, dev.ID, 0, "uncalibrated bmp280")
}

// TestDataPipeline_EndToEnd_Calibrated:注入校准后同一负载应成功解析落库
// (等价覆盖原"解析成功"分支,顺带验证 CalibrationCache 门控双向)
func TestDataPipeline_EndToEnd_Calibrated(t *testing.T) {
	db := datapipePipelineDB(t)
	node := datapipeCreateNode(db, "3001-cal")
	ch := datapipeCreateChannel(db, node.NodeID, "1")
	dev := datapipeCreateDevice(db, "BMP280-Test", node.NodeID, ch.ID, "bmp280")

	calibration := []byte{0x70, 0x6b, 0x43, 0x67, 0x18, 0xfc, 0x7d, 0x8e, 0x43, 0xd6, 0xd0, 0x0b, 0x27, 0x0b, 0x8c, 0x00, 0xf9, 0xff, 0x8c, 0x3c, 0xf8, 0xc6, 0x70, 0x17}
	if err := db.Create(&models.CalibrationCache{
		NodeID: node.NodeID, EdgeDeviceID: dev.ID, DeviceType: dev.Type,
		Data: fmt.Sprintf("%x", calibration),
	}).Error; err != nil {
		t.Fatalf("create calibration: %v", err)
	}

	fc := newDatapipeEventBusConsumer(db)
	defer fc.Close()

	rawData := []byte{0x65, 0x5a, 0xc0, 0x7e, 0xed, 0x00}
	fc.postAndDrain(t, databus.DataEvent{
		DeviceID: node.NodeID, ChannelID: uint64(ch.ID), EdgeDeviceID: uint64(dev.ID),
		RequestID: 1, RawData: rawData,
	})
	// BMP280 有 2 个解析字段;device_data 新语义无条件写 1 行
	assertUnifiedForDevice(t, db, dev.ID, 2, "calibrated bmp280")
	assertDeviceDataForDevice(t, db, dev.ID, 1, "calibrated bmp280")
}

// TestDataPipeline_FormatDecision:请求级校验语义 — 错误码 / 空数据事件
// ShouldParse=false 时消费者不执行,这两类事件被证伪为"不会被解析落库"。
func TestDataPipeline_FormatDecision(t *testing.T) {
	db := datapipePipelineDB(t)
	node := datapipeCreateNode(db, "3001-err")
	ch := datapipeCreateChannel(db, node.NodeID, "1")
	dev := datapipeCreateDevice(db, "fmt-dev", node.NodeID, ch.ID, "bmp280")

	fc := newDatapipeEventBusConsumer(db)
	defer fc.Close()

	// 错误码事件:ShouldParse=false,消费者不执行
	fc.postAndDrain(t, databus.DataEvent{
		DeviceID: node.NodeID, ChannelID: uint64(ch.ID), EdgeDeviceID: uint64(dev.ID),
		RequestID: 1, ErrorCode: 2, RawData: []byte{1, 2, 3, 4, 5, 6},
	})
	assertUnifiedForDevice(t, db, dev.ID, 0, "error code")
	assertDeviceDataForDevice(t, db, dev.ID, 0, "error code")

	// 空数据事件:ShouldParse=false,消费者不执行
	fc.postAndDrain(t, databus.DataEvent{
		DeviceID: node.NodeID, ChannelID: uint64(ch.ID), EdgeDeviceID: uint64(dev.ID),
		RequestID: 2, RawData: []byte{},
	})
	assertUnifiedForDevice(t, db, dev.ID, 0, "empty raw")
	assertDeviceDataForDevice(t, db, dev.ID, 0, "empty raw")
}

// TestFindEdgeDeviceByChannelID_C6IndexFallback:寻址语义在新消费者下的等价覆盖。
// 原 findEdgeDeviceByChannelID 允许直查 channel_id 失败后 fall through 到
// 跨节点通道索引;新 consumer 的 channel 直查带 node_id 限定
// (consumers_heavy.go:145),edge_device_id 失败直接 return (:141-143)。
// 迁移后的表驱动用例覆盖三个寻址层级 + 越界/跨节点拒绝(接受新语义,更严)。
func TestFindEdgeDeviceByChannelID_C6IndexFallback(t *testing.T) {
	db := datapipePipelineDB(t)
	node := datapipeCreateNode(db, "AABBCCDDEE01")
	ch1 := datapipeCreateChannel(db, node.NodeID, "1")
	ch2 := datapipeCreateChannel(db, node.NodeID, "0x76")
	d1 := datapipeCreateDevice(db, "LK_TH01-Ch1", node.NodeID, ch1.ID, "lk_th01")
	d2 := datapipeCreateDevice(db, "LK_TH01-Ch2", node.NodeID, ch2.ID, "lk_th01")

	// 第二个节点,用于验证 node_id 限定:channel_id 相同也不能跨节点解析
	node2 := datapipeCreateNode(db, "OTHER-NODE-02")
	ch3 := models.Channel{ID: 200, NodeID: node2.NodeID, HardwareID: "1", IntervalMs: 5000, Enabled: true}
	if err := db.Create(&ch3).Error; err != nil {
		t.Fatal(err)
	}
	d3 := datapipeCreateDevice(db, "LK_TH01-Other", node2.NodeID, ch3.ID, "lk_th01")

	fc := newDatapipeEventBusConsumer(db)
	defer fc.Close()

	payload := []byte("LK-TH01-response") // ≥4 字节,LKTH01 解析出 temperature+humidity

	// 子测试共享同一 db,用「发布前后行数增量」断言,避免跨子测试累计误报。
	cases := []struct {
		name           string
		evt            databus.DataEvent
		wantDevice     uint // 期望落到哪个设备(0=无设备命中)
		wantUnified    int
		wantDeviceData int
	}{
		{
			name: "edge device id (PK fast path)",
			evt: databus.DataEvent{
				DeviceID: node.NodeID, ChannelID: 0, EdgeDeviceID: uint64(d2.ID),
				RequestID: 10, RawData: payload,
			},
			wantDevice: d2.ID, wantUnified: 2, wantDeviceData: 1,
		},
		{
			name: "direct channel id (node-scoped)",
			evt: databus.DataEvent{
				DeviceID: node.NodeID, ChannelID: uint64(ch1.ID),
				RequestID: 11, RawData: payload,
			},
			wantDevice: d1.ID, wantUnified: 2, wantDeviceData: 1,
		},
		{
			name: "out of range index",
			evt: databus.DataEvent{
				DeviceID: node.NodeID, ChannelID: 99,
				RequestID: 12, RawData: payload,
			},
			wantDevice: 0, wantUnified: 0, wantDeviceData: 0,
		},
		{
			name: "cross node channel id is not resolved",
			evt: databus.DataEvent{
				// node 没有 id=200 的通道;旧实现会 fall through 到全局索引并
				// 命中 node2 的 ch3/设备 d3 —— 新语义带 node_id 限定,拒绝。
				DeviceID: node.NodeID, ChannelID: 200,
				RequestID: 13, RawData: payload,
			},
			wantDevice: 0, wantUnified: 0, wantDeviceData: 0,
		},
	}
	allDevices := []uint{d1.ID, d2.ID, d3.ID}
	unifiedBaseline := map[uint]int{}
	deviceDataBaseline := map[uint]int{}
	for _, id := range allDevices {
		unifiedBaseline[id] = countUnifiedForDevice(t, db, id, "baseline")
		deviceDataBaseline[id] = countDeviceDataForDevice(t, db, id, "baseline")
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			beforeUnified := map[uint]int{}
			beforeDeviceData := map[uint]int{}
			for _, id := range allDevices {
				beforeUnified[id] = countUnifiedForDevice(t, db, id, tc.name+" before")
				beforeDeviceData[id] = countDeviceDataForDevice(t, db, id, tc.name+" before")
			}
			fc.postAndDrain(t, tc.evt)
			for _, id := range allDevices {
				unifiedDelta := countUnifiedForDevice(t, db, id, tc.name) - beforeUnified[id]
				deviceDataDelta := countDeviceDataForDevice(t, db, id, tc.name) - beforeDeviceData[id]
				wantU, wantD := 0, 0
				if id == tc.wantDevice {
					wantU, wantD = tc.wantUnified, tc.wantDeviceData
				}
				if unifiedDelta != wantU {
					t.Errorf("[%s] unified_data delta(device=%d) = %d, want %d", tc.name, id, unifiedDelta, wantU)
				}
				if deviceDataDelta != wantD {
					t.Errorf("[%s] device_data delta(device=%d) = %d, want %d", tc.name, id, deviceDataDelta, wantD)
				}
			}
		})
	}
	// 基线本身必须为 0(全部设备在用例开始前无数据),保证增量断言语义干净
	for _, id := range allDevices {
		if unifiedBaseline[id] != 0 || deviceDataBaseline[id] != 0 {
			t.Errorf("device %d baseline not clean: unified=%d device_data=%d", id, unifiedBaseline[id], deviceDataBaseline[id])
		}
	}
}

// TestDataPipeline_C6ChannelIndexFallback:文件原有 C6 索引 fallback(channels.id
// 远离 0/1 时,channel_id=0/1 被当作节点通道列表下标)。
func TestDataPipeline_C6ChannelIndexFallback(t *testing.T) {
	db := datapipePipelineDB(t)
	node := datapipeCreateNode(db, "C6IDX")
	// 显式 DB id 远离 0/1,避免与 C6 0-based 下标碰撞
	ch1 := models.Channel{ID: 100, NodeID: node.NodeID, HardwareID: "1", BusType: "I2C", IntervalMs: 5000, Enabled: true}
	if err := db.Create(&ch1).Error; err != nil {
		t.Fatal(err)
	}
	ch2 := models.Channel{ID: 101, NodeID: node.NodeID, HardwareID: "0x76", BusType: "I2C", IntervalMs: 1000, Enabled: true}
	if err := db.Create(&ch2).Error; err != nil {
		t.Fatal(err)
	}
	d1 := datapipeCreateDevice(db, "LK_TH01-Ch1", node.NodeID, ch1.ID, "lk_th01")
	d2 := datapipeCreateDevice(db, "LK_TH01-Ch2", node.NodeID, ch2.ID, "lk_th01")

	fc := newDatapipeEventBusConsumer(db)
	defer fc.Close()

	payload := []byte("LK-TH01-response")

	// C6 发送 channel_id=0 → node 通道列表(按 id 升序)下标 0 → ch1 (real id=100)
	fc.postAndDrain(t, databus.DataEvent{
		DeviceID: node.NodeID, ChannelID: 0, RequestID: 1, RawData: payload,
	})
	assertUnifiedForDevice(t, db, d1.ID, 2, "c6 idx 0 -> ch1")
	assertDeviceDataForDevice(t, db, d1.ID, 1, "c6 idx 0 -> ch1")
	assertUnifiedForDevice(t, db, d2.ID, 0, "c6 idx 0 (ch2 untouched)")
	assertDeviceDataForDevice(t, db, d2.ID, 0, "c6 idx 0 (ch2 untouched)")

	// C6 发送 channel_id=1 → 下标 1 → ch2 (real id=101)
	fc.postAndDrain(t, databus.DataEvent{
		DeviceID: node.NodeID, ChannelID: 1, RequestID: 2, RawData: payload,
	})
	assertUnifiedForDevice(t, db, d1.ID, 2, "c6 idx 1 (ch1 unchanged)")
	assertDeviceDataForDevice(t, db, d1.ID, 1, "c6 idx 1 (ch1 unchanged)")
	assertUnifiedForDevice(t, db, d2.ID, 2, "c6 idx 1 -> ch2")
	assertDeviceDataForDevice(t, db, d2.ID, 1, "c6 idx 1 -> ch2")
}

// ---------------------------------------------------------------------------
// 保留的独立驱动/注册表/背压用例 (不依赖被删除的私有函数,继续覆盖驱动与
// worker 层行为)
// ---------------------------------------------------------------------------

// TestDriverRegistry: ensure built-in drivers are registered
func TestDriverRegistry(t *testing.T) {
	types := newDataPipeTestRegistry().List()
	expected := map[string]bool{
		"bmp280":  false,
		"lk_th01": false,
		"sn3000":  false,
	}
	for _, t := range types {
		if _, ok := expected[t]; ok {
			expected[t] = true
		}
	}
	for name, found := range expected {
		if !found {
			t.Errorf("driver %s not registered", name)
		}
	}
}

// TestBMP280Driver_Parse: uncalibrated samples must be rejected.
func TestBMP280Driver_Parse(t *testing.T) {
	d := &drivers.BMP280Driver{}
	if _, err := d.ParseData([]byte{0x00, 0x41, 0x6e, 0xeb, 0x67, 0x32}); err == nil {
		t.Fatal("uncalibrated BMP280 sample must fail closed")
	}
}

// TestBMP280Driver_GetSensorDefinitions: for HA Discovery (R8 T6)
func TestBMP280Driver_GetSensorDefinitions(t *testing.T) {
	d := &drivers.BMP280Driver{}
	sensors := d.GetSensorDefinitions()
	if len(sensors) != 2 {
		t.Errorf("expected 2 sensor defs, got %d", len(sensors))
	}
}

// TestBMP280Driver_TooShort: malformed data should error not panic
func TestBMP280Driver_TooShort(t *testing.T) {
	d := &drivers.BMP280Driver{}
	_, err := d.ParseData([]byte{0x01, 0x02})
	if err == nil {
		t.Error("expected error for short data")
	}
}

// TestBackpressure_Fallback: when worker pool is full, processDataReportJob
// is called synchronously (F4.5 from R8)
func TestBackpressure_Fallback(t *testing.T) {
	// Simulate by setting dataCh to nil and calling processDataReportJob
	// directly - should not panic. dataBus==nil → 直接返回。
	db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	mgr := &Manager{db: db, driverRegistry: newDataPipeTestRegistry()}

	job := dataReportJob{
		deviceID:  "test",
		channelID: 1,
		rawData:   []byte{},
	}
	// Should not panic even with no channel/device
	mgr.processDataReportJob(job)
	// Give it a moment in case of async
	time.Sleep(50 * time.Millisecond)
}

func TestDriverRegistry_Isolated(t *testing.T) {
	types := newDataPipeTestRegistry().List()
	t.Logf("registered: %v", types)
}
