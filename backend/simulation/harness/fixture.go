//go:build simulation

// 本文件提供"数据能真正落库"的共用夹具。
//
// 为什么必须是共用件：后续 SIM-CHAN/DATA/EDGE/DS/AUTO/ALERT/CMD/OTA/RT 等
// 数十个场景都需要"让一个仿真节点上报的数据变成 unified_data 行"。若各任务
// 自行发明一套，会产生多份重复实现，且极易各自踩坑（漏 field 7 → 静默变成
// passive 事件；漏 EdgeDevice → 消费者直接 return；解析器字段对不上 → 不入库）。
package harness

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
)

// ---------- 解析路径选型（为什么是 ConfigParser） ----------
//
// backend/internal/databus/consumers_heavy.go 的 SensorParserConsumer.Handle()
// 判定顺序是：
//
//	drv, _ := c.driverRegistry.Get(device.Type)
//	_, calibrationAware := drv.(drivers.CalibrationAwareDriver)
//	if !calibrationAware && device.DeviceConfigID > 0 { 用 parser.NewConfigParser(dc.Parser) }
//	else { 走驱动注册表，其中 CalibrationAware 驱动要求 CalibrationCache 行 }
//
// 因此只要 EdgeDevice.Type 不是 CalibrationAware 驱动，就走 ConfigParser，
// **完全不碰标定缓存**。全部内置驱动里只有 BMP280Driver 实现了
// ParseDataWithCalibration（CalibrationAwareDriver）；其余注册类型
// （lk_th01 / sn3000 / prs3001 / sn3001_rain / jiabaida_bms /
// techfine_inverter / generic_modbus / generic_i2c）都不是。
//
// 夹具刻意选一个**未注册**的 device_type（sim_generic）：Get 不到时 drv 为 nil，
// 类型断言同样为 false，照样走 ConfigParser；而且不会被任何驱动内置的协议
// 解析分支抢走（SN3000/PRS3001/SN3001 在 ConfigParser 失败时会回落到各自的
// legacy 硬编码协议解析，那会让夹具的字节布局失去确定性）。
//
// 前置依赖：无标定、无驱动注册、无 ConfigTemplate。
// 唯一硬要求是 DeviceConfig.Parser 合法且 data_format="binary"。

const (
	// fixtureDeviceType 是夹具专用的设备类型。它刻意不在驱动注册表内，
	// 以保证解析 100% 走 DeviceConfig.Parser（见上方选型说明）。
	fixtureDeviceType = "sim_generic"
	// fixtureHardwareType 必须与通道的 hardware_type/bus_type 一致
	// （validateDeviceConfigForChannel / validateTransportChannelType 都会校验）。
	fixtureHardwareType = "uart"

	// FixtureTempCategory / FixtureLevelCategory 是夹具默认提供的两个类别：
	//   sim_temp  int16  scale 0.01 → 物理量范围 ±327.67，分辨率 0.01
	//   sim_level uint16 scale 1    → 物理量范围 0..65535，分辨率 1（取整！）
	FixtureTempCategory  = "sim_temp"
	FixtureLevelCategory = "sim_level"

	fixtureSelfCheckTimeout = 30 * time.Second
)

// Fixture 是一套"节点 + 通道 + 设备配置(解析器) + 边缘设备"的已就绪组合，
// 其上报的数据会被真实入库到 unified_data。
type Fixture struct {
	Node           *Device
	NodeID         string
	ChannelID      uint
	EdgeDeviceID   uint
	DeviceConfigID uint
	// Categories 是解析器定义的物理量名，顺序与帧内字节布局一致。
	Categories []string

	env     *Env
	session *Session

	mu     sync.Mutex
	values map[string]float64
	// quantum 是每个类别的量化步长（= parser scale），断言容差取 step/2。
	quantum map[string]float64
}

// fixtureFieldRule 与 pkg/parser.FieldRule 同构（harness 只需要"生成 JSON"，
// 不需要解析能力，因此不 import parser 包）。
type fixtureFieldRule struct {
	Name   string  `json:"name"`
	Type   string  `json:"type"`
	Unit   string  `json:"unit"`
	Scale  float64 `json:"scale"`
	Offset float64 `json:"offset"`
	Length int     `json:"length"`
	Endian string  `json:"endian"`
}

// buildFixtureParser 生成夹具的 DeviceConfig.Parser。
// data_format=binary：直接按偏移取字节，不涉及 Modbus 头/CRC 剥离。
// 4 字节布局 = int16(sim_temp, scale 0.01) + uint16(sim_level, scale 1)。
func buildFixtureParser() json.RawMessage {
	rules := []fixtureFieldRule{
		{Name: FixtureTempCategory, Type: "int16", Unit: "C", Scale: 0.01, Offset: 0, Length: 2, Endian: "big"},
		{Name: FixtureLevelCategory, Type: "uint16", Unit: "cm", Scale: 1, Offset: 2, Length: 2, Endian: "big"},
	}
	body, err := json.Marshal(map[string]any{"data_format": "binary", "fields": rules})
	if err != nil {
		panic("harness: 序列化夹具解析器失败: " + err.Error())
	}
	return body
}

// ProvisionSimpleDevice 在场景命名空间内建好
// 节点 + 通道 + 设备配置(解析器) + 边缘设备，并做一次哨兵上报自检，
// 确认这条链真的能把数据写进 unified_data。
//
// 自检的意义：后续场景的失败必须能归因到"场景断言"，而不是"夹具没配好"。
// 任何一步没打通都会在这里以一个指明步骤的 error 结束。
func (e *Env) ProvisionSimpleDevice(scenarioID, suffix string) (*Fixture, error) {
	slug := sceneSlug(scenarioID, suffix)
	fixture := &Fixture{
		env:        e,
		session:    e.Admin,
		Categories: []string{FixtureTempCategory, FixtureLevelCategory},
		values:     map[string]float64{FixtureTempCategory: 0, FixtureLevelCategory: 0},
		quantum:    map[string]float64{FixtureTempCategory: 0.01, FixtureLevelCategory: 1},
	}
	if fixture.session == nil {
		return nil, fmt.Errorf("夹具需要已登录的管理员会话（Env.Admin 为 nil）")
	}

	// 步骤 1：创建设备配置（解析器）。走真实 HTTP，不直接写库。
	configResp := fixture.session.Post("/api/v1/device-configs", map[string]any{
		"name":          e.NS(scenarioID, suffix+"-cfg"),
		"description":   "场景仿真夹具（ConfigParser binary 4 字节布局）",
		"device_type":   fixtureDeviceType,
		"hardware_type": fixtureHardwareType,
		"protocol":      "stream",
		"status":        "active",
		"parser":        buildFixtureParser(),
	})
	if configResp.Status != 201 {
		return nil, fmt.Errorf("步骤 1（创建 DeviceConfig）失败: %s", configResp.context())
	}
	fixture.DeviceConfigID = uint(configResp.DataInt("id"))

	// 步骤 2：启动仿真节点并完成 Hello + ResourceReport。
	// 顺序不可颠倒：Hello 会清空能力字段（见 Device.Hello 注释）。
	fixture.Node = e.Device(slug)
	fixture.NodeID = fixture.Node.NodeID
	if err := fixture.Node.Connect(); err != nil {
		return nil, fmt.Errorf("步骤 2（连接 MQTT）失败: %w", err)
	}
	fixture.Node.HelloThenReport("2.6", "SIM-GENERIC", 1, nil)

	// 步骤 3：创建边缘设备并内联创建通道（一次 HTTP 调用，避免两阶段提交
	// 产生孤儿通道）。type 由 device_config_id 派生，因此不要求驱动注册表
	// 认识 sim_generic。
	edgeResp := fixture.session.Post("/api/v1/edge-devices", map[string]any{
		"name":             e.NS(scenarioID, suffix+"-dev"),
		"node_id":          fixture.NodeID,
		"device_config_id": fixture.DeviceConfigID,
		"interval_ms":      0, // 0 = 不调度，上报节奏完全由场景掌控
		"channel": map[string]any{
			"hardware_type": fixtureHardwareType,
			"hardware_id":   "0x01",
		},
	})
	if edgeResp.Status != 201 {
		return nil, fmt.Errorf("步骤 3（创建边缘设备+通道）失败: %s", edgeResp.context())
	}
	fixture.EdgeDeviceID = uint(edgeResp.DataInt("id"))
	fixture.ChannelID = uint(edgeResp.DataInt("channel_id"))

	// 步骤 4：让仿真器知道自己的身份，之后 DataReport 自动携带 edge_device_id。
	fixture.Node.EdgeDeviceID = uint32(fixture.EdgeDeviceID)
	fixture.Node.ChannelID = uint32(fixture.ChannelID)

	// 步骤 5：哨兵上报自检 —— 直到该行真的出现在 unified_data 里才算夹具可用。
	if err := fixture.selfCheck(); err != nil {
		return nil, err
	}
	e.Evidence("fixture_node_id", fixture.NodeID)
	e.Evidence("fixture_edge_device_id", fixture.EdgeDeviceID)
	e.Evidence("fixture_channel_id", fixture.ChannelID)
	e.Evidence("fixture_categories", fixture.Categories)
	return fixture, nil
}

// selfCheck 做一次上报并轮询等待落库，失败时给出分步诊断。
func (f *Fixture) selfCheck() error {
	const sentinel = 21.37
	if err := f.Report(FixtureTempCategory, sentinel); err != nil {
		return fmt.Errorf("步骤 5（哨兵上报）失败: %w", err)
	}
	deadline := time.Now().Add(fixtureSelfCheckTimeout)
	var lastErr error
	for {
		value, err := f.Latest(FixtureTempCategory)
		if err == nil {
			if math.Abs(value-sentinel) <= f.tolerance(FixtureTempCategory) {
				f.env.Evidence("fixture_selfcheck_value", value)
				return nil
			}
			lastErr = fmt.Errorf("落库值 %v 与哨兵值 %v 不符", value, sentinel)
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("步骤 5（自检）失败：哨兵上报后 %s 内 unified_data 未出现预期行。"+
				"排查顺序：通道是否创建 / 边缘设备是否绑定 device_config_id / parser JSON 是否合法 / "+
				"DataReport 是否携带 edge_device_id / 字段名是否匹配。最后错误: %w", fixtureSelfCheckTimeout, lastErr)
		}
		time.Sleep(defaultPollInterval)
	}
}

// Report 上报单个类别（其余类别沿用上一次的值，保证帧长始终满足解析器要求）。
func (f *Fixture) Report(category string, value float64) error {
	return f.ReportMany(map[string]float64{category: value})
}

// ReportMany 用一帧上报多个类别。
func (f *Fixture) ReportMany(values map[string]float64) error {
	f.mu.Lock()
	for category, value := range values {
		if _, known := f.values[category]; !known {
			f.mu.Unlock()
			return fmt.Errorf("类别 %q 不在夹具的解析器定义内（可用: %s）", category, strings.Join(f.Categories, ", "))
		}
		f.values[category] = value
	}
	current := make(map[string]float64, len(f.values))
	for category, value := range f.values {
		current[category] = value
	}
	f.mu.Unlock()

	payload, err := f.encode(current)
	if err != nil {
		return err
	}
	return f.Node.DataReport(uint32(f.ChannelID), uint64(time.Now().UnixMilli()), payload)
}

// encode 按解析器的字段规则把物理量编码成帧负载。
func (f *Fixture) encode(values map[string]float64) ([]byte, error) {
	tempRaw := int(math.Round(values[FixtureTempCategory] / f.quantum[FixtureTempCategory]))
	levelRaw := uint32(math.Round(values[FixtureLevelCategory] / f.quantum[FixtureLevelCategory]))
	tempBytes, err := EncodeInt16BigEndian(tempRaw)
	if err != nil {
		return nil, fmt.Errorf("类别 %s 的值 %v 无法编码: %w", FixtureTempCategory, values[FixtureTempCategory], err)
	}
	levelBytes, err := EncodeUint16BigEndian(levelRaw)
	if err != nil {
		return nil, fmt.Errorf("类别 %s 的值 %v 无法编码: %w", FixtureLevelCategory, values[FixtureLevelCategory], err)
	}
	return append(tempBytes, levelBytes...), nil
}

// tolerance 是量化容差：上报值先除以 scale 取整再乘回，误差上界是半个量化步长。
func (f *Fixture) tolerance(category string) float64 {
	return f.quantum[category]/2 + 1e-9
}

// Latest 从场景库读取该夹具某个类别的最新落库值（只读断言，设计 §3 原则 2）。
func (f *Fixture) Latest(category string) (float64, error) {
	var value float64
	err := f.env.SQL().QueryRow(
		"SELECT value FROM unified_data WHERE device_id = $1 AND sensor_name = $2 ORDER BY timestamp DESC, id DESC LIMIT 1",
		f.EdgeDeviceID, category).Scan(&value)
	if err == sql.ErrNoRows {
		return 0, fmt.Errorf("unified_data 中尚无 (edge_device=%d, sensor=%s) 的行", f.EdgeDeviceID, category)
	}
	if err != nil {
		return 0, fmt.Errorf("查询 unified_data 失败: %w", err)
	}
	return value, nil
}

// CountRows 统计该夹具某类别已落库的行数。
func (f *Fixture) CountRows(category string) (int64, error) {
	var count int64
	if err := f.env.SQL().QueryRow(
		"SELECT count(*) FROM unified_data WHERE device_id = $1 AND sensor_name = $2",
		f.EdgeDeviceID, category).Scan(&count); err != nil {
		return 0, fmt.Errorf("统计 unified_data 失败: %w", err)
	}
	return count, nil
}

// AwaitValue 轮询等待某类别的最新值收敛到 want（容差 = 半个量化步长）。
func (f *Fixture) AwaitValue(category string, want float64, timeout time.Duration) {
	f.env.T.Helper()
	f.env.Eventually(timeout, func() error {
		got, err := f.Latest(category)
		if err != nil {
			return err
		}
		if math.Abs(got-want) > f.tolerance(category) {
			return fmt.Errorf("%s 最新值 %v 与期望 %v 不符（容差 %v）", category, got, want, f.tolerance(category))
		}
		return nil
	})
}

// AwaitRows 轮询等待某类别的落库行数达到 want。
func (f *Fixture) AwaitRows(category string, want int64, timeout time.Duration) {
	f.env.T.Helper()
	f.env.Eventually(timeout, func() error {
		got, err := f.CountRows(category)
		if err != nil {
			return err
		}
		if got < want {
			return fmt.Errorf("%s 落库行数 %d < 期望 %d", category, got, want)
		}
		return nil
	})
}

// StartReporting 以 interval 周期上报（模拟真实节点节奏，设计 §5.3），
// 返回的 stop 函数由场景放进 t.Cleanup。这里的 ticker 属于"仿真器模拟上报
// 节奏"，不作为任何断言的同步手段。
func (f *Fixture) StartReporting(interval time.Duration) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	counter := 0
	f.Node.Loop(ctx, interval, func() error {
		f.mu.Lock()
		counter++
		temp := 20 + float64(counter%10)
		level := float64(100 + counter%20)
		f.mu.Unlock()
		return f.ReportMany(map[string]float64{FixtureTempCategory: temp, FixtureLevelCategory: level})
	})
	return cancel
}

// Cleanup 删除夹具创建的资源（场景自我清理，设计 §5.6）。
// 删除后清除 Device 的反查缓存，避免把数据继续写到已删实例。
func (f *Fixture) Cleanup() {
	if f.Node != nil {
		f.Node.Close()
		f.Node.InvalidateEdgeCache()
	}
	if f.session == nil || f.DeviceConfigID == 0 {
		return
	}
	if f.EdgeDeviceID != 0 {
		f.session.Delete(fmt.Sprintf("/api/v1/edge-devices/%d", f.EdgeDeviceID))
	}
	f.session.Delete(fmt.Sprintf("/api/v1/device-configs/%d", f.DeviceConfigID))
}
