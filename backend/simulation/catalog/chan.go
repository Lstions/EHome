//go:build simulation

// SIM-CHAN：通道与配置清单（设计 docs/设计/场景仿真验证框架.md §9）。
//
// 本域的核心不变量是"中心端改配置 → 节点真的收到新清单"：断言落在节点实际
// 收到的 ConfigManifest（0x04）二进制帧上，而不是接口返回 200。帧解析复用
// backend/pkg/frame，与固件侧共用同一套编解码定义。
//
// 命名约定（设计 §4.1）：本文件是包 catalog 内的 CHAN 域，所有包级声明一律
// 以域小写短名 chan 开头。
package catalog

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"ehome/backend/pkg/frame"
	"ehome/backend/simulation/harness"
)

// chanDomain 是本文件注册的场景域（设计 §6 表前缀列去掉 SIM-）。
const chanDomain Domain = "CHAN"

// chanWireBusI2C 是清单里通道子结构 bus_type 的线上枚举（I2C = 2），
// 取值来自 nodemgr.encodeConfigManifest 的 busTypeMap。
const chanWireBusI2C = 2

func init() {
	Register(Scenario{
		ID:     "SIM-CHAN-001",
		Title:  "为节点新增通道并下发清单，节点收到含该通道的配置清单",
		Domain: chanDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-CHAN；docs/设计/通道.md §配置下发",
		Run:    chanScenario001,
	})
	Register(Scenario{
		ID:     "SIM-CHAN-002",
		Title:  "修改通道采样参数后清单同步下发，节点收到新参数",
		Domain: chanDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-CHAN；docs/设计/同步机制.md §清单代次",
		Run:    chanScenario002,
	})
	Register(Scenario{
		ID:     "SIM-CHAN-003",
		Title:  "删除通道后新清单不再包含该通道",
		Domain: chanDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-CHAN；docs/设计/同步机制.md §清单代次",
		Run:    chanScenario003,
	})
	Register(Scenario{
		ID:     "SIM-CHAN-004",
		Title:  "通道参数非法（缺字段/类型冲突/外设类型）被拒绝且原配置不变",
		Domain: chanDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-CHAN；docs/设计/通道.md §校验",
		Run:    chanScenario004,
	})
	Register(Scenario{
		ID:     "SIM-CHAN-005",
		Title:  "节点主动拉取配置，得到与中心端一致的清单",
		Domain: chanDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-CHAN；docs/设计/同步机制.md §ConfigSyncRequest",
		Run:    chanScenario005,
	})
}

// ---------------------------------------------------------------------------
// 通道夹具
// ---------------------------------------------------------------------------

// chanDTO 是 /api/v1/channels 返回的元素。
type chanDTO struct {
	ID           uint   `json:"id"`
	NodeID       string `json:"node_id"`
	HardwareType string `json:"hardware_type"`
	HardwareID   string `json:"hardware_id"`
	IntervalMs   int    `json:"interval_ms"`
	BusType      string `json:"bus_type"`
	BusConfig    string `json:"bus_config"`
	Enabled      bool   `json:"enabled"`
	TemplateIDs  string `json:"template_ids"`
}

// chanCreate 通过真实 HTTP 建立通道，返回通道主键。
//
// bus_config 的编码是真实约束：I2C/UART 必须给"至少 2 字节"的十六进制引脚串，
// 否则 handler 在事务里校验失败（见交付报告的偏差清单）。
func chanCreate(e *harness.Env, nodeID, hardwareType, hardwareID, busConfig string, intervalMs int) uint {
	e.T.Helper()
	resp := e.Admin.Post("/api/v1/channels", map[string]any{
		"node_id":       nodeID,
		"hardware_type": hardwareType,
		"bus_type":      strings.ToUpper(hardwareType),
		"hardware_id":   hardwareID,
		"bus_config":    busConfig,
		"interval_ms":   intervalMs,
	})
	resp.Expect(http.StatusCreated)
	var channel chanDTO
	resp.Decode(&channel)
	if channel.ID == 0 {
		e.T.Fatalf("创建通道未返回主键: %s", resp.BodyString())
	}
	nodeCleanupDelete(e, fmt.Sprintf("/api/v1/channels/%d", channel.ID))
	return channel.ID
}

// chanGet 读取通道详情（用于"被拒绝后配置不变"的断言）。
func chanGet(e *harness.Env, channelID uint) chanDTO {
	e.T.Helper()
	resp := e.Admin.Get(fmt.Sprintf("/api/v1/channels/%d", channelID))
	resp.Expect(http.StatusOK)
	var channel chanDTO
	resp.Decode(&channel)
	return channel
}

// ---------------------------------------------------------------------------
// ConfigManifest 线上帧解析
// ---------------------------------------------------------------------------

// chanManifest 是节点实际收到的配置清单（0x04）在测试侧的结构化视图。
type chanManifest struct {
	ManifestID string
	SyncID     string
	Channels   []chanManifestChannel
}

// chanManifestChannel 对应清单字段 4 的一个通道子结构。
type chanManifestChannel struct {
	ID         uint
	Enabled    bool
	BusType    uint64
	BusConfig  []byte
	HardwareID string   // 仅协议 < 2.3 的旧编码携带
	IntervalMs uint64   // 仅协议 < 2.3 的旧编码携带
	TemplateID []uint64 // 仅协议 < 2.3 的旧编码携带
	Edges      []chanManifestEdge
}

// chanManifestEdge 对应清单字段 9（edge_device_groups）的一个子结构。
type chanManifestEdge struct {
	EdgeDeviceID uint
	HardwareID   uint64
	Commands     []chanManifestCommand
}

// chanManifestCommand 对应边缘设备组内字段 3 的调度指令。
type chanManifestCommand struct {
	TemplateID uint64
	IntervalMs uint64
	Enabled    bool
}

// chanParseManifest 解析一份配置清单。
//
// 只读我们断言的字段，其余一律忽略——协议会继续演进，测试不该因为新增字段
// 而变红。
func chanParseManifest(raw []byte) (*chanManifest, error) {
	dec, err := frame.NewDecoder(raw)
	if err != nil {
		return nil, err
	}
	if dec.MsgType() != frame.MsgConfigMfst {
		return nil, fmt.Errorf("不是配置清单帧: 0x%02X", dec.MsgType())
	}
	manifest := &chanManifest{}
	for {
		field, err := dec.NextField()
		if errors.Is(err, frame.ErrEndOfFrame) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch field.FieldNum {
		case 1:
			manifest.ManifestID = frame.GetString(field)
		case 4:
			channel, err := chanParseManifestChannel(frame.GetBytes(field))
			if err != nil {
				return nil, err
			}
			manifest.Channels = append(manifest.Channels, *channel)
		case 8:
			manifest.SyncID = frame.GetString(field)
		}
	}
	return manifest, nil
}

func chanParseManifestChannel(payload []byte) (*chanManifestChannel, error) {
	sub, err := frame.NewSubDecoder(payload)
	if err != nil {
		return nil, fmt.Errorf("通道子结构为空: %w", err)
	}
	channel := &chanManifestChannel{}
	for {
		field, err := sub.NextField()
		if errors.Is(err, frame.ErrEndOfFrame) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch field.FieldNum {
		case 1:
			channel.ID = uint(frame.GetUint64(field))
		case 2:
			channel.HardwareID = frame.GetString(field)
		case 3:
			channel.TemplateID = append(channel.TemplateID, frame.GetUint64(field))
		case 4:
			channel.IntervalMs = frame.GetUint64(field)
		case 5:
			channel.Enabled = frame.GetBool(field)
		case 6:
			channel.BusType = frame.GetUint64(field)
		case 7:
			channel.BusConfig = append([]byte(nil), frame.GetBytes(field)...)
		case 9:
			edge, err := chanParseManifestEdge(frame.GetBytes(field))
			if err != nil {
				return nil, err
			}
			channel.Edges = append(channel.Edges, *edge)
		}
	}
	return channel, nil
}

func chanParseManifestEdge(payload []byte) (*chanManifestEdge, error) {
	sub, err := frame.NewSubDecoder(payload)
	if err != nil {
		return nil, fmt.Errorf("边缘设备组为空: %w", err)
	}
	edge := &chanManifestEdge{}
	for {
		field, err := sub.NextField()
		if errors.Is(err, frame.ErrEndOfFrame) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch field.FieldNum {
		case 1:
			edge.EdgeDeviceID = uint(frame.GetUint64(field))
		case 2:
			edge.HardwareID = frame.GetUint64(field)
		case 3:
			command, err := chanParseManifestCommand(frame.GetBytes(field))
			if err != nil {
				return nil, err
			}
			edge.Commands = append(edge.Commands, *command)
		}
	}
	return edge, nil
}

func chanParseManifestCommand(payload []byte) (*chanManifestCommand, error) {
	sub, err := frame.NewSubDecoder(payload)
	if err != nil {
		return nil, fmt.Errorf("调度指令为空: %w", err)
	}
	command := &chanManifestCommand{}
	for {
		field, err := sub.NextField()
		if errors.Is(err, frame.ErrEndOfFrame) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch field.FieldNum {
		case 1:
			command.TemplateID = frame.GetUint64(field)
		case 2:
			command.IntervalMs = frame.GetUint64(field)
		case 3:
			command.Enabled = frame.GetBool(field)
		}
	}
	return command, nil
}

// chanManifestCount 返回节点已收到的清单份数（供"只看新增帧"的游标使用）。
func chanManifestCount(dev *harness.Device) int {
	return len(dev.FramesOf(frame.MsgConfigMfst))
}

// chanAwaitManifest 从第 after 份之后等待一份满足 pick 的配置清单。
//
// 为什么用轮询而不是 sleep：清单下发是"HTTP 写 → 事件总线 → SyncGate →
// MQTT 发布"的异步链（设计 §3 原则 3）。用 EventuallyError 轮询既有超时，
// 又不会把 sleep 当同步手段；期间收到的其它清单（例如握手触发的补推）不会
// 让断言通过，只会被跳过。
func chanAwaitManifest(e *harness.Env, dev *harness.Device, after int, timeout time.Duration, pick func(*chanManifest) error) *chanManifest {
	e.T.Helper()
	var found *chanManifest
	var last error
	err := e.EventuallyError(timeout, func() error {
		frames := dev.FramesOf(frame.MsgConfigMfst)
		for i := after; i < len(frames); i++ {
			manifest, parseErr := chanParseManifest(frames[i].Raw)
			if parseErr != nil {
				return fmt.Errorf("解析配置清单失败: %w", parseErr)
			}
			if pickErr := pick(manifest); pickErr == nil {
				found = manifest
				return nil
			} else {
				last = pickErr
			}
		}
		return fmt.Errorf("尚未收到满足条件的配置清单（新增 %d 份，最后一份不满足的原因: %v）", len(frames)-after, last)
	})
	if err != nil {
		e.Fatalf("等待配置清单失败 node=%s: %v", dev.NodeID, err)
	}
	return found
}

// chanManifestChannelByID 在清单里找指定通道。
func chanManifestChannelByID(manifest *chanManifest, channelID uint) *chanManifestChannel {
	for i := range manifest.Channels {
		if manifest.Channels[i].ID == channelID {
			return &manifest.Channels[i]
		}
	}
	return nil
}

// chanManifestChannelIDs 返回清单里的通道主键（升序），便于集合比较。
func chanManifestChannelIDs(manifest *chanManifest) []uint {
	ids := make([]uint, 0, len(manifest.Channels))
	for _, channel := range manifest.Channels {
		ids = append(ids, channel.ID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// chanNodeConfig 是 GET /api/v1/nodes/:id/config 的清单内容。
type chanNodeConfig struct {
	Node struct {
		NodeID          string `json:"node_id"`
		ConfigVersion   string `json:"config_version"`
		ProtocolVersion string `json:"protocol_version"`
	} `json:"node"`
	Channels []chanDTO `json:"channels"`
}

// chanGetNodeConfig 读取中心端的节点配置清单。
//
// 该端点的响应体是"双层信封"：data.data 才是清单（真实行为，见交付报告）。
func chanGetNodeConfig(e *harness.Env, nodeID string) chanNodeConfig {
	e.T.Helper()
	resp := e.Admin.Get("/api/v1/nodes/" + nodeID + "/config")
	resp.Expect(http.StatusOK)
	var wrapper struct {
		Data chanNodeConfig `json:"data"`
	}
	resp.Decode(&wrapper)
	return wrapper.Data
}

// chanNodeChannelIDs 返回中心端记录的该节点通道主键（升序）。
func chanNodeChannelIDs(e *harness.Env, nodeID string) []uint {
	e.T.Helper()
	config := chanGetNodeConfig(e, nodeID)
	ids := make([]uint, 0, len(config.Channels))
	for _, channel := range config.Channels {
		ids = append(ids, channel.ID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// ---------------------------------------------------------------------------
// SIM-CHAN-001
// ---------------------------------------------------------------------------

// 守护的不变量：新增通道后节点必须收到一份"包含该通道"的配置清单，且清单
// 里的通道参数（总线类型、引脚配置）与中心端记录一致。
func chanScenario001(e *harness.Env) {
	dev := nodeProvision(e, "SIM-CHAN-001", "n1", "通道清单节点")
	nodeID := dev.NodeID
	nodeHello(e, dev, 0)

	channelID := chanCreate(e, nodeID, "I2C", "0x76", "0102", 5000)
	manifest := chanAwaitManifest(e, dev, 0, 30*time.Second, func(manifest *chanManifest) error {
		if chanManifestChannelByID(manifest, channelID) == nil {
			return fmt.Errorf("清单 %s 不含通道 %d（含 %v）", manifest.ManifestID, channelID, chanManifestChannelIDs(manifest))
		}
		return nil
	})

	channel := chanManifestChannelByID(manifest, channelID)
	if !channel.Enabled {
		e.T.Fatalf("清单里的通道 %d 未启用", channelID)
	}
	if channel.BusType != chanWireBusI2C {
		e.T.Fatalf("清单里的通道总线类型=%d，期望 %d(I2C)", channel.BusType, chanWireBusI2C)
	}
	if !bytes.Equal(channel.BusConfig, []byte{0x01, 0x02}) {
		e.T.Fatalf("清单里的通道引脚配置=%x，期望 0102", channel.BusConfig)
	}
	if manifest.ManifestID == "" {
		e.T.Fatalf("清单缺少 manifest_id：节点无法回执这一代配置")
	}

	// 交叉核对：中心端接口记录的通道集合必须与下发清单一致。
	center := chanNodeChannelIDs(e, nodeID)
	if len(center) != len(manifest.Channels) {
		e.T.Fatalf("中心端通道数=%d，清单通道数=%d（center=%v manifest=%v）",
			len(center), len(manifest.Channels), center, chanManifestChannelIDs(manifest))
	}

	e.Evidence("manifest_id", manifest.ManifestID)
	e.Evidence("channel_id", channelID)
	e.Evidence("channel_bus_config", fmt.Sprintf("%x", channel.BusConfig))
}

// ---------------------------------------------------------------------------
// SIM-CHAN-002
// ---------------------------------------------------------------------------

// 守护的不变量：通道参数变化必须产生"新的一代"清单并推到节点上。
//
// 两层断言，对应协议真实编码：
//  1. 采样周期（interval_ms）参与清单哈希 → 改完之后必须收到 manifest_id 不同
//     的新清单（证明变更被中心端承认并重新下发）；
//  2. 引脚配置（bus_config）直接编码在清单通道子结构的字段 7 上 → 改完之后
//     节点必须收到携带新字节的清单。
//
// 之所以不直接断言"清单里 interval_ms 字段变了"：协议 ≥2.3 的编码把采样周期
// 放进字段 9（边缘设备调度组），对没有驱动指令模板的通用设备不落线，因此用
// "代次变化 + 引脚字节"作为等价且更强的证据。
func chanScenario002(e *harness.Env) {
	dev := nodeProvision(e, "SIM-CHAN-002", "n1", "通道参数节点")
	nodeID := dev.NodeID
	nodeHello(e, dev, 0)

	channelID := chanCreate(e, nodeID, "I2C", "0x76", "0102", 5000)
	initial := chanAwaitManifest(e, dev, 0, 30*time.Second, func(manifest *chanManifest) error {
		if chanManifestChannelByID(manifest, channelID) == nil {
			return fmt.Errorf("初始清单不含通道 %d", channelID)
		}
		return nil
	})

	// 第一步：只改采样周期。
	cursor := chanManifestCount(dev)
	e.Admin.Put(fmt.Sprintf("/api/v1/channels/%d", channelID), map[string]any{
		"interval_ms": 2500,
	}).Expect(http.StatusOK)

	updated := chanAwaitManifest(e, dev, cursor, 30*time.Second, func(manifest *chanManifest) error {
		if manifest.ManifestID == initial.ManifestID {
			return fmt.Errorf("收到的仍是旧代次清单 %s", manifest.ManifestID)
		}
		if chanManifestChannelByID(manifest, channelID) == nil {
			return fmt.Errorf("新清单不含通道 %d", channelID)
		}
		return nil
	})
	if updated.ManifestID == initial.ManifestID {
		e.T.Fatalf("采样周期变更后清单代次未变化: %s", initial.ManifestID)
	}

	// 第二步：改引脚配置，断言新的字节真的上了线。
	cursor = chanManifestCount(dev)
	e.Admin.Put(fmt.Sprintf("/api/v1/channels/%d", channelID), map[string]any{
		"bus_config": "0304",
	}).Expect(http.StatusOK)

	manifest := chanAwaitManifest(e, dev, cursor, 30*time.Second, func(manifest *chanManifest) error {
		channel := chanManifestChannelByID(manifest, channelID)
		if channel == nil {
			return fmt.Errorf("清单不含通道 %d", channelID)
		}
		if !bytes.Equal(channel.BusConfig, []byte{0x03, 0x04}) {
			return fmt.Errorf("清单里引脚配置=%x，期望 0304", channel.BusConfig)
		}
		return nil
	})

	// 中心端记录也必须同步，避免"下发对了但库里没改"。
	center := chanGet(e, channelID)
	if center.IntervalMs != 2500 {
		e.T.Fatalf("中心端 interval_ms=%d，期望 2500", center.IntervalMs)
	}
	if center.BusConfig != "0304" {
		e.T.Fatalf("中心端 bus_config=%q，期望 0304", center.BusConfig)
	}

	e.Evidence("initial_manifest_id", initial.ManifestID)
	e.Evidence("updated_manifest_id", manifest.ManifestID)
	e.Evidence("bus_config_on_wire", fmt.Sprintf("%x", chanManifestChannelByID(manifest, channelID).BusConfig))
}

// ---------------------------------------------------------------------------
// SIM-CHAN-003
// ---------------------------------------------------------------------------

// 守护的不变量：删掉的通道不能继续留在下发给节点的清单里；同时保留的通道
// 必须还在（防止把"空清单"误判成成功）。
//
// 两个通道刻意使用不同引脚（0102 / 0304）：清单下发前会做引脚占用仲裁，
// 同引脚的通道会让整份清单被拒，那不是本场景要证明的东西。
func chanScenario003(e *harness.Env) {
	dev := nodeProvision(e, "SIM-CHAN-003", "n1", "通道删除节点")
	nodeID := dev.NodeID
	nodeHello(e, dev, 0)

	keepID := chanCreate(e, nodeID, "I2C", "0x76", "0102", 5000)
	dropID := chanCreate(e, nodeID, "UART", "1", "0304", 5000)

	chanAwaitManifest(e, dev, 0, 30*time.Second, func(manifest *chanManifest) error {
		if chanManifestChannelByID(manifest, keepID) == nil || chanManifestChannelByID(manifest, dropID) == nil {
			return fmt.Errorf("尚未收到同时含 %d 与 %d 的清单（当前 %v）", keepID, dropID, chanManifestChannelIDs(manifest))
		}
		return nil
	})

	cursor := chanManifestCount(dev)
	e.Admin.Delete(fmt.Sprintf("/api/v1/channels/%d", dropID)).Expect(http.StatusOK)

	manifest := chanAwaitManifest(e, dev, cursor, 30*time.Second, func(manifest *chanManifest) error {
		if chanManifestChannelByID(manifest, dropID) != nil {
			return fmt.Errorf("清单仍含已删除通道 %d", dropID)
		}
		if chanManifestChannelByID(manifest, keepID) == nil {
			return fmt.Errorf("清单丢了保留通道 %d", keepID)
		}
		return nil
	})

	if ids := chanManifestChannelIDs(manifest); len(ids) != 1 || ids[0] != keepID {
		e.T.Fatalf("删除后清单通道集合=%v，期望 [%d]", ids, keepID)
	}
	if center := chanNodeChannelIDs(e, nodeID); len(center) != 1 || center[0] != keepID {
		e.T.Fatalf("删除后中心端通道集合=%v，期望 [%d]", center, keepID)
	}

	e.Evidence("manifest_after_delete", chanManifestChannelIDs(manifest))
	e.Evidence("dropped_channel_id", dropID)
}

// ---------------------------------------------------------------------------
// SIM-CHAN-004
// ---------------------------------------------------------------------------

// 守护的不变量：非法通道参数必须被拒绝，且拒绝不能"半途改库"——被拒绝后
// 中心端的通道配置必须与拒绝前一模一样。
//
// 覆盖四类非法输入（都是真实校验路径）：
//   - hardware_type 与 bus_type 不一致；
//   - GPIO/PWM 外设类型被当成通道；
//   - 空硬件类型（缺字段）；
//   - 节点级配置更新里夹带外设总线类型。
func chanScenario004(e *harness.Env) {
	dev := nodeProvision(e, "SIM-CHAN-004", "n1", "通道校验节点")
	nodeID := dev.NodeID

	channelID := chanCreate(e, nodeID, "I2C", "0x76", "0102", 5000)
	before := chanGet(e, channelID)

	// 1) 类型冲突
	e.Admin.Put(fmt.Sprintf("/api/v1/channels/%d", channelID), map[string]any{
		"hardware_type": "I2C",
		"bus_type":      "SPI",
	}).Expect(http.StatusBadRequest)

	// 2) 外设被当成通道
	e.Admin.Put(fmt.Sprintf("/api/v1/channels/%d", channelID), map[string]any{
		"bus_type": "GPIO",
	}).Expect(http.StatusBadRequest)

	// 3) 空硬件类型（显式缺字段）
	e.Admin.Put(fmt.Sprintf("/api/v1/channels/%d", channelID), map[string]any{
		"hardware_type": "",
	}).Expect(http.StatusBadRequest)

	// 4) 空更新体
	e.Admin.Put(fmt.Sprintf("/api/v1/channels/%d", channelID), map[string]any{}).
		Expect(http.StatusBadRequest)

	// 5) 节点级清单更新里夹带外设类型
	e.Admin.Put("/api/v1/nodes/"+nodeID+"/config", map[string]any{
		"channels": []map[string]any{{"id": channelID, "bus_type": "GPIO"}},
	}).Expect(http.StatusBadRequest)

	// 被拒绝之后，配置必须逐字段不变。
	after := chanGet(e, channelID)
	if after != before {
		e.T.Fatalf("非法参数被拒绝后通道配置发生了变化:\n before=%+v\n after =%+v", before, after)
	}
	config := chanGetNodeConfig(e, nodeID)
	if len(config.Channels) != 1 || config.Channels[0] != before {
		e.T.Fatalf("非法参数被拒绝后节点清单发生了变化: %+v（期望 %+v）", config.Channels, before)
	}

	e.Evidence("channel_before", fmt.Sprintf("%+v", before))
	e.Evidence("channel_after", fmt.Sprintf("%+v", after))
}

// ---------------------------------------------------------------------------
// SIM-CHAN-005
// ---------------------------------------------------------------------------

// chanConfigSyncer 是"节点主动拉取配置"（ConfigSyncRequest 0x13）的发布者。
//
// 为什么场景自己发这一帧：设计 §5.3 冻结的 Device 方法清单里没有 ConfigSyncRequest，
// 而"设备主动拉配置"只有这一条真实入口（nodemgr/handler_response.go 处理 0x13），
// 没有等价 HTTP 端点。这里只发一帧控制面报文，帧编码仍复用 backend/pkg/frame，
// 落库路径完全不经过本函数（不另造数据通路）。
type chanConfigSyncer struct {
	broker string
	nodeID string
}

// Publish 把 ConfigSyncRequest 发到 nodes/<节点>/up。
// 安全红线（设计 §7-2）：NodeID 前缀不合规直接拒绝发布。
func (p chanConfigSyncer) Publish(manifestID string) error {
	if !strings.HasPrefix(p.nodeID, "sim-") {
		return fmt.Errorf("拒绝为节点 %q 发布：NodeID 必须以 sim- 开头（安全红线，设计 §7-2）", p.nodeID)
	}
	encoder := frame.NewEncoder(frame.MsgConfigSyncReq)
	encoder.EncodeString(1, "sim-recon") // reason：设备侧的自由文本
	encoder.EncodeVarint(2, 0)           // current_epoch
	encoder.EncodeString(3, manifestID)  // 设备持有的（此处刻意是过期的）清单代次
	options := mqtt.NewClientOptions().
		AddBroker(p.broker).
		SetClientID(fmt.Sprintf("ehome-sim-cfgsync-%s-%d", p.nodeID, time.Now().UnixNano())).
		SetConnectTimeout(5 * time.Second)
	client := mqtt.NewClient(options)
	if token := client.Connect(); token.WaitTimeout(5*time.Second) && token.Error() != nil {
		return fmt.Errorf("连接 MQTT 失败: %w", token.Error())
	}
	defer client.Disconnect(100)
	token := client.Publish("nodes/"+p.nodeID+"/up", 1, false, encoder.Bytes())
	if !token.WaitTimeout(5 * time.Second) {
		return fmt.Errorf("发布 ConfigSyncRequest 超时")
	}
	return token.Error()
}

// 守护的不变量：节点主动索要配置时，拿到的清单必须与中心端当前记录完全一致
// （同一 manifest_id、同一通道集合）——否则设备会拿着旧配置继续跑。
func chanScenario005(e *harness.Env) {
	dev := nodeProvision(e, "SIM-CHAN-005", "n1", "主动拉取节点")
	nodeID := dev.NodeID
	nodeHello(e, dev, 0)
	channelID := chanCreate(e, nodeID, "I2C", "0x76", "0102", 5000)

	// 故意报一个过期的 manifest_id：设备"重启后 NVS 里还是旧配置"的真实场景。
	cursor := chanManifestCount(dev)
	puller := chanConfigSyncer{broker: e.MQTTAddr, nodeID: nodeID}
	if err := puller.Publish("v2-stale-manifest"); err != nil {
		e.T.Fatalf("节点主动拉取配置失败 node=%s: %v", nodeID, err)
	}

	manifest := chanAwaitManifest(e, dev, cursor, 30*time.Second, func(manifest *chanManifest) error {
		if chanManifestChannelByID(manifest, channelID) == nil {
			return fmt.Errorf("拉取到的清单不含当前通道 %d", channelID)
		}
		return nil
	})

	// 与中心端逐项核对：代次 + 通道集合。
	center := chanGetNodeConfig(e, nodeID)
	if manifest.ManifestID != center.Node.ConfigVersion {
		e.T.Fatalf("节点拉取到的清单代次=%q，中心端当前代次=%q", manifest.ManifestID, center.Node.ConfigVersion)
	}
	centerIDs := make([]uint, 0, len(center.Channels))
	for _, channel := range center.Channels {
		centerIDs = append(centerIDs, channel.ID)
	}
	sort.Slice(centerIDs, func(i, j int) bool { return centerIDs[i] < centerIDs[j] })
	ids := chanManifestChannelIDs(manifest)
	if len(ids) != len(centerIDs) {
		e.T.Fatalf("拉取清单通道集合=%v，中心端=%v", ids, centerIDs)
	}
	for i := range ids {
		if ids[i] != centerIDs[i] {
			e.T.Fatalf("拉取清单通道集合=%v，中心端=%v", ids, centerIDs)
		}
	}

	e.Evidence("pulled_manifest_id", manifest.ManifestID)
	e.Evidence("center_config_version", center.Node.ConfigVersion)
	e.Evidence("pulled_channels", ids)
}
