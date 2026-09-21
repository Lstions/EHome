//go:build simulation

package harness

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"ehome/backend/pkg/frame"
)

// 协议常量直接取真实实现的冻结值，避免仿真是"另一套协议"。
const (
	// serverMaxProtocolVersion 必须与 backend/internal/nodemgr/sender.go 的
	// ServerMaxProtocolVersion 完全一致：parseHello 要求精确相等。
	serverMaxProtocolVersion = "2.6"
	helloAckTimeout          = 5 * time.Second
	helloAttempts            = 4
)

// ReceivedFrame 是仿真器收到的一帧下行数据。
type ReceivedFrame struct {
	Topic   string
	MsgType uint8
	Raw     []byte
	At      time.Time
}

// Device 是节点仿真器：真实 MQTT 连接 + 真实二进制帧（复用 pkg/frame）。
//
// 安全红线（设计 §7-2）：Connect / 所有 Publish 之前强制校验 NodeID 以
// "sim-" 开头，不合规直接返回 error。同网段可能有真实设备在跑，
// 这条校验是"绝不干扰真实设备"的唯一执行点。
type Device struct {
	NodeID       string
	EdgeDeviceID uint32 // >0 时 DataReport 携带 field 7（调度采样语义）
	ChannelID    uint32

	env      *Env
	broker   string
	client   mqtt.Client
	clientID string

	mu        sync.Mutex
	connected bool
	frames    []ReceivedFrame
	loopErrs  []error
	closed    bool
	// edgeCache 缓存 channelID → edge_device_id 的反查结果，
	// 避免每一帧上报都打一次库（虚拟家庭持续上报时会很密）。
	edgeCache map[uint32]uint32

	// LastHelloNonceValue 是最近一次 Hello 使用的握手 nonce（v2.6 关联字段），
	// HelloAck 必须原样回显它。经 LastHelloNonce() 读取。
	LastHelloNonceValue uint32

	sequence uint64
}

func newDevice(env *Env, nodeID string) *Device {
	return &Device{
		NodeID:   nodeID,
		env:      env,
		broker:   env.MQTTAddr,
		clientID: fmt.Sprintf("ehome-sim-%s-%s", env.RunID, randHex(4)),
	}
}

// UpTopic 是节点上报主题（backend/internal/mqtt: nodes/<id>/up）。
func (d *Device) UpTopic() string { return "nodes/" + d.NodeID + "/up" }

// DownTopic 是节点下行主题（ping / HelloAck / 清单等）。
func (d *Device) DownTopic() string { return "nodes/" + d.NodeID + "/down" }

// ControlTopic 是可靠下行主题（QoS1，指令与配置清单）。
func (d *Device) ControlTopic() string { return "nodes/" + d.NodeID + "/control" }

// assertSimNode 是红线执行点：任何发布/订阅之前都必须通过。
func (d *Device) assertSimNode() error {
	if !strings.HasPrefix(d.NodeID, "sim-") {
		return fmt.Errorf("拒绝操作节点 %q：NodeID 必须以 sim- 开头（安全红线，设计 §7-2）", d.NodeID)
	}
	for _, topic := range []string{d.UpTopic(), d.DownTopic(), d.ControlTopic()} {
		if !strings.HasPrefix(topic, "nodes/sim-") {
			return fmt.Errorf("拒绝使用主题 %q：只允许 nodes/sim-*/...（安全红线，设计 §7-2）", topic)
		}
	}
	return nil
}

// Connect 建立真实 MQTT 连接，并订阅 nodes/<id>/down 与 nodes/<id>/control。
func (d *Device) Connect() error {
	if err := d.assertSimNode(); err != nil {
		return err
	}
	d.mu.Lock()
	if d.connected {
		d.mu.Unlock()
		return nil
	}
	d.mu.Unlock()

	options := mqtt.NewClientOptions().
		AddBroker(d.broker).
		SetClientID(d.clientID).
		SetCleanSession(true).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectTimeout(5 * time.Second).
		SetKeepAlive(20 * time.Second).
		SetPingTimeout(10 * time.Second).
		SetOrderMatters(false)

	client := mqtt.NewClient(options)
	token := client.Connect()
	if !token.WaitTimeout(10 * time.Second) {
		return fmt.Errorf("连接 MQTT %s 超时（节点 %s）", d.broker, d.NodeID)
	}
	if err := token.Error(); err != nil {
		return fmt.Errorf("连接 MQTT %s 失败（节点 %s）: %w", d.broker, d.NodeID, err)
	}

	for _, topic := range []string{d.DownTopic(), d.ControlTopic()} {
		subToken := client.Subscribe(topic, 1, d.onMessage)
		if !subToken.WaitTimeout(10 * time.Second) {
			client.Disconnect(100)
			return fmt.Errorf("订阅 %s 超时", topic)
		}
		if err := subToken.Error(); err != nil {
			client.Disconnect(100)
			return fmt.Errorf("订阅 %s 失败: %w", topic, err)
		}
	}

	d.mu.Lock()
	d.client = client
	d.connected = true
	d.mu.Unlock()
	return nil
}

// Close 断开连接；可重复调用（收尾与场景自清理都会调）。
func (d *Device) Close() {
	d.mu.Lock()
	if d.closed || d.client == nil {
		d.closed = true
		d.connected = false
		d.mu.Unlock()
		return
	}
	client := d.client
	d.client = nil
	d.closed = true
	d.connected = false
	d.mu.Unlock()
	client.Disconnect(200)
}

func (d *Device) onMessage(_ mqtt.Client, message mqtt.Message) {
	payload := append([]byte(nil), message.Payload()...)
	received := ReceivedFrame{Topic: message.Topic(), Raw: payload, At: time.Now()}
	if len(payload) > 0 {
		received.MsgType = payload[0]
	}
	d.mu.Lock()
	d.frames = append(d.frames, received)
	d.mu.Unlock()
}

// FrameSeq 返回当前已接收帧总数，用作 AwaitFrameAfter 的游标。
func (d *Device) FrameSeq() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.frames)
}

// FramesOf 返回指定消息类型的已接收帧快照。
func (d *Device) FramesOf(msgType uint8) []ReceivedFrame {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]ReceivedFrame, 0, len(d.frames))
	for _, item := range d.frames {
		if item.MsgType == msgType {
			out = append(out, item)
		}
	}
	return out
}

// AwaitFrame 轮询等待指定类型的下行帧（设计 §5.3）。
// 解码器返回前已经过 pkg/frame 校验；失败时给出收到过的帧类型清单。
func (d *Device) AwaitFrame(msgType uint8, timeout time.Duration) (*frame.Decoder, error) {
	return d.AwaitFrameAfter(msgType, 0, timeout)
}

// AwaitFrameAfter 只接受序号 >= after 的帧（避免命中握手期的历史帧）。
func (d *Device) AwaitFrameAfter(msgType uint8, after int, timeout time.Duration) (*frame.Decoder, error) {
	raw, err := d.AwaitRawAfter(msgType, after, timeout)
	if err != nil {
		return nil, err
	}
	return frame.NewDecoder(raw)
}

// AwaitRawAfter 返回匹配帧的原始字节。
func (d *Device) AwaitRawAfter(msgType uint8, after int, timeout time.Duration) ([]byte, error) {
	deadline := time.Now().Add(timeout)
	for {
		d.mu.Lock()
		if after < 0 {
			after = 0
		}
		for i := after; i < len(d.frames); i++ {
			if d.frames[i].MsgType == msgType {
				raw := append([]byte(nil), d.frames[i].Raw...)
				d.mu.Unlock()
				return raw, nil
			}
		}
		seen := make([]string, 0, len(d.frames))
		for _, item := range d.frames {
			seen = append(seen, fmt.Sprintf("%s(0x%02X@%s)", frame.MsgTypeName(item.MsgType), item.MsgType, item.At.Format("15:04:05.000")))
		}
		d.mu.Unlock()
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("等待帧 0x%02X(%s) 超时 %s（节点 %s）；期间收到: [%s]",
				msgType, frame.MsgTypeName(msgType), timeout, d.NodeID, strings.Join(seen, ", "))
		}
		time.Sleep(framePollInterval)
	}
}

// AwaitFieldsAfter 等待匹配帧并解码为字段表（便于断言字段值）。
func (d *Device) AwaitFieldsAfter(msgType uint8, after int, timeout time.Duration) (map[uint8]frame.Field, error) {
	raw, err := d.AwaitRawAfter(msgType, after, timeout)
	if err != nil {
		return nil, err
	}
	return DecodeFrameFields(raw)
}

// DecodeFrameFields 把一帧原始字节解码成 field_num → Field 的映射。
// 供场景断言下行帧内容（例如 HelloAck 的 nonce 回显、Ping 的时间戳）。
func DecodeFrameFields(raw []byte) (map[uint8]frame.Field, error) {
	decoder, err := frame.NewDecoder(raw)
	if err != nil {
		return nil, err
	}
	return DecodeDecoderFields(decoder)
}

// DecodeDecoderFields 从已构造的 Decoder 当前位置继续解码出字段表。
// Device.Hello 返回的就是 Decoder，场景可直接用它断言 HelloAck 字段。
func DecodeDecoderFields(decoder *frame.Decoder) (map[uint8]frame.Field, error) {
	if decoder == nil {
		return nil, fmt.Errorf("decoder 为 nil")
	}
	fields := map[uint8]frame.Field{}
	for {
		field, err := decoder.NextField()
		if errors.Is(err, frame.ErrEndOfFrame) {
			break
		}
		if err != nil {
			return nil, err
		}
		fields[field.FieldNum] = *field
	}
	return fields, nil
}

// LastHelloNonce 返回最近一次 Hello 使用的握手 nonce（v2.6 关联字段）。
// HelloAck 必须原样回显它，这是"握手指的是同一次尝试"的证据。
func (d *Device) LastHelloNonce() uint32 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.LastHelloNonceValue
}

// Pong 回应服务端的 Ping（0x08）——真实固件行为，避免服务端进入重试。
// tsMillis 必须回显 Ping 携带的时间戳。
func (d *Device) Pong(tsMillis uint64) error {
	encoder := frame.NewEncoder(frame.MsgPong)
	encoder.EncodeVarint(1, tsMillis)
	return d.publish(d.UpTopic(), encoder.Bytes())
}

// publish 是所有上行发布的唯一出口：先过红线校验，再上真实 broker。
func (d *Device) publish(topic string, payload []byte) error {
	if err := d.assertSimNode(); err != nil {
		return err
	}
	d.mu.Lock()
	client := d.client
	connected := d.connected
	d.mu.Unlock()
	if !connected || client == nil {
		return fmt.Errorf("节点 %s 尚未连接 MQTT（先调用 Connect）", d.NodeID)
	}
	token := client.Publish(topic, 1, false, payload)
	if !token.WaitTimeout(5 * time.Second) {
		return fmt.Errorf("发布到 %s 超时", topic)
	}
	return token.Error()
}

// sendHello 编码并发布一帧 Hello（0x01）。
// 字段编号严格对齐 backend/internal/nodemgr/handler_hello.go:parseHello。
func (d *Device) sendHello(protocolVersion, firmwareVersion, model string, channelCount int, configEpoch uint64, nvsHasConfig bool) (uint32, error) {
	nonce := randomNonce()
	encoder := frame.NewEncoder(frame.MsgHello)
	encoder.EncodeString(1, d.NodeID)
	encoder.EncodeString(2, firmwareVersion)
	encoder.EncodeString(3, model)
	encoder.EncodeVarint(4, uint64(channelCount))
	encoder.EncodeVarint(5, configEpoch)
	encoder.EncodeBool(6, nvsHasConfig)
	encoder.EncodeString(7, "") // last_manifest
	encoder.EncodeString(8, protocolVersion)
	encoder.EncodeVarint(frame.HelloFieldHandshakeNonce, uint64(nonce))
	if err := d.publish(d.UpTopic(), encoder.Bytes()); err != nil {
		return 0, err
	}
	d.mu.Lock()
	d.LastHelloNonceValue = nonce
	d.mu.Unlock()
	return nonce, nil
}

// Hello 完成一次真实握手：发布 Hello 并等待 HelloAck（设计 §5.3）。
//
// 为什么带重试：服务端 MQTT 订阅建立与仿真器首次发布之间存在窗口，
// 落在窗口内的 QoS1 上行会被 broker 直接丢弃（无持久会话）。重试是
// 真实固件的行为，也让场景免于用 sleep 去"等订阅建好"。
//
// ⚠ 顺序陷阱（命令执行门禁的致命细节）：
// backend/internal/nodemgr/handler_hello.go 在**每次 Hello 时**主动清空
// node 的 BootID / ResourceReportedAt / CommandEngineRevision /
// CommandEngineCapabilities。而 commandexec 的 currentCapabilities() 要求
// BootID 非空、ResourceReportedAt 落在能力窗口内（commandexec.MaxCapabilityAge，
// 当前 15 分钟 = 固件上报周期 10 分钟 + 裕量 5 分钟；口径以该常量的推导为准，
// 不在注释里复制数字）、CommandEngineRevision != 0。
// 因此 **每次 Hello 之后都必须重新发送 ResourceReport**，顺序不能反 ——
// 先 ResourceReport 再 Hello 会让能力被清空，所有下发类场景只能断言"被拒绝"。
func (d *Device) Hello(version, model string, channelCount int) *frame.Decoder {
	d.env.T.Helper()
	start := d.FrameSeq()
	var lastErr error
	for attempt := 0; attempt < helloAttempts; attempt++ {
		if _, err := d.sendHello(serverMaxProtocolVersion, version, model, channelCount, 0, false); err != nil {
			lastErr = err
			time.Sleep(300 * time.Millisecond)
			continue
		}
		decoder, err := d.AwaitFrameAfter(frame.MsgHelloAck, start, helloAckTimeout)
		if err == nil {
			return decoder
		}
		lastErr = err
	}
	d.env.Fatalf("节点 %s Hello 握手失败（%d 次尝试）: %v", d.NodeID, helloAttempts, lastErr)
	return nil
}

// DataReport 上报一帧数据（0x03）。
//
// 为什么必须携带字段 7（edge_device_id）：
// backend/internal/databus/bus.go 的 IsPassive() = (RequestID == 0 && EdgeDeviceID == 0)，
// ShouldParse() 直接依赖它。request_id 与 edge_device_id 同时为 0 的上行会被
// 判定为"无关联透传数据"，SensorParserConsumer 根本不处理 —— 数据不会落库。
// 固件在调度采样时携带 edge_device_id，仿真器因此默认按 (node_id, channel_id)
// 反查真实 EdgeDevice 主键并填入；也可显式设置 d.EdgeDeviceID 覆盖。
func (d *Device) DataReport(channelID uint32, tsMillis uint64, payload []byte) error {
	edgeDeviceID := d.EdgeDeviceID
	if edgeDeviceID == 0 {
		resolved, err := d.ResolveEdgeDeviceID(channelID)
		if err != nil {
			return err
		}
		edgeDeviceID = resolved
	}
	encoder := frame.NewEncoder(frame.MsgDataRpt)
	encoder.EncodeVarint(1, uint64(channelID))
	encoder.EncodeVarint(2, tsMillis)
	d.mu.Lock()
	d.sequence++
	sequence := d.sequence
	d.mu.Unlock()
	encoder.EncodeVarint(3, sequence)
	encoder.EncodeBytes(4, payload)
	encoder.EncodeVarint(7, uint64(edgeDeviceID))
	return d.publish(d.UpTopic(), encoder.Bytes())
}

// ResolveEdgeDeviceID 按 (node_id, channel_id) 反查边缘设备主键（带进程内缓存）。
// 查不到时不返回 0：那会让上报静默变成 passive 事件，是最难排查的一类假失败。
func (d *Device) ResolveEdgeDeviceID(channelID uint32) (uint32, error) {
	d.mu.Lock()
	if cached, ok := d.edgeCache[channelID]; ok {
		d.mu.Unlock()
		return cached, nil
	}
	d.mu.Unlock()

	var id uint32
	err := d.env.SQL().QueryRow(
		"SELECT id FROM edge_devices WHERE node_id = $1 AND channel_id = $2 AND deleted_at IS NULL ORDER BY id ASC LIMIT 1",
		d.NodeID, channelID).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("节点 %s 的通道 %d 上没有可用的边缘设备"+
			"（DataReport 必须携带 edge_device_id 才会被解析入库）: %w", d.NodeID, channelID, err)
	}
	d.mu.Lock()
	if d.edgeCache == nil {
		d.edgeCache = map[uint32]uint32{}
	}
	d.edgeCache[channelID] = id
	d.mu.Unlock()
	return id, nil
}

// InvalidateEdgeCache 清除 (channel, edge_device) 反查缓存。
// 设备被删除/重建后必须调用，否则会往已删除的实例写数据。
func (d *Device) InvalidateEdgeCache() {
	d.mu.Lock()
	d.edgeCache = nil
	d.mu.Unlock()
}

// StatusReport 上报一帧状态（0x02）。
// 必填字段：1 uptime、2 status、5 sync_state（见 handleStatusReport 的校验）。
// rssi 非 0 时以 field 28（|RSSI| dBm）嵌入 runtime_performance 子帧，
// 与 ESP32 固件的编码一致，从而驱动节点列表的连接质量字段。
func (d *Device) StatusReport(uptimeSec uint64, status string, rssi int) error {
	encoder := frame.NewEncoder(frame.MsgStatusRpt)
	encoder.EncodeVarint(1, uptimeSec)
	encoder.EncodeString(2, status)
	encoder.EncodeVarint(3, uint64(d.ChannelID))
	encoder.EncodeVarint(4, 0)
	encoder.EncodeVarint(5, 0) // sync_state: 0 = idle
	if rssi != 0 {
		absolute := rssi
		if absolute < 0 {
			absolute = -absolute
		}
		sub := frame.SubEncoder()
		sub.EncodeVarint(1, 180000) // free_heap_bytes
		sub.EncodeVarint(2, 150000) // min_free_heap_bytes
		sub.EncodeVarint(3, 1024)   // scheduler_stack_free_words
		sub.EncodeVarint(4, 2048)   // worker_stack_free_words
		sub.EncodeVarint(5, 8)      // min_command_queue_spaces
		sub.EncodeVarint(28, uint64(absolute))
		encoder.EncodeBytes(9, sub.Bytes())
	}
	return d.publish(d.UpTopic(), encoder.Bytes())
}

// Loop 以 interval 周期执行 fn，模拟真实节点的上报节奏（设计 §5.3）。
// 这里的 sleep/ticker 属于"仿真器模拟上报节奏"，不作为任何断言的同步手段；
// fn 的错误被收集而非终止测试（本函数运行在独立 goroutine，禁止 t.Fatalf）。
func (d *Device) Loop(ctx context.Context, interval time.Duration, fn func() error) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		if err := fn(); err != nil {
			d.recordLoopError(err)
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := fn(); err != nil {
					d.recordLoopError(err)
				}
			}
		}
	}()
}

func (d *Device) recordLoopError(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.loopErrs) < 16 {
		d.loopErrs = append(d.loopErrs, err)
	}
}

// LoopErrors 返回 Loop 内累计的错误（供场景在结束时断言"上报期间无错误"）。
func (d *Device) LoopErrors() []error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]error(nil), d.loopErrs...)
}

// ---------- 资源与配置上报（0x19 / 0x05 / 0x11） ----------

// ReportedChannel 描述固件实际应用的通道，对应 ResourceReport 的
// channels_blob 里的 channel_entry。
type ReportedChannel struct {
	ID      uint64
	Enabled bool
}

// ResourceReportData 是 ResourceReport(0x19) 的输入。
//
// 为什么仿真器必须能发这一帧：commandexec 的运行时门禁全部 fail-closed，
// 且这些事实**只能**由 MQTT 帧写入，HTTP 侧没有任何写入口：
//   - currentCapabilities()：boot_id 非空 + resource_reported_at 在能力窗口内
//     （commandexec.MaxCapabilityAge，当前 15 分钟）+
//     command_engine_revision != 0 + 能力齐全；
//   - requireReportedActionChannel()：hardware_info.channels[] 含
//     {id: <channelID>, enabled: true}；
//   - requireAppliedManifest()：config_version == manifest &&
//     config_status == "applied" && config_sync_state == "in_sync"（由 ConfigResult 写入）。
type ResourceReportData struct {
	Platform string
	Channels []ReportedChannel
	BootID   string // 长度 1..32
	Revision uint32

	SupportsChannelCmdV2 bool
	SupportsBoundedBatch bool
	SupportsFinally      bool
	MaxBatchSteps        uint32
	MaxTXBytes           uint32
	MaxRXBytes           uint32
	MaxStepTimeoutMS     uint32
	RAMDedupEntries      uint32
}

// withDefaults 填入让服务端 decodeCommandEngine 接受的合法值。
// 边界来自 decodeCommandEngine 的校验：
//   - max_tx_bytes ∈ [1,128]、max_rx_bytes ∈ [1,256]、max_step_timeout_ms ∈ [1,30000]；
//   - supports_bounded_batch 为真时 max_batch_steps ∈ [2,8]，为假时必须为 0；
//   - supports_channel_cmd_v2 为真时 supports_finally 必须也为真。
func (r ResourceReportData) withDefaults() ResourceReportData {
	if r.Platform == "" {
		r.Platform = "ESP32-C6"
	}
	if r.BootID == "" {
		r.BootID = "sim-boot"
	}
	if len(r.BootID) > 32 {
		r.BootID = r.BootID[:32]
	}
	if r.Revision == 0 {
		r.Revision = 1
	}
	if r.SupportsBoundedBatch && r.MaxBatchSteps == 0 {
		r.MaxBatchSteps = 4
	}
	if !r.SupportsBoundedBatch {
		r.MaxBatchSteps = 0
	}
	if r.MaxTXBytes == 0 {
		r.MaxTXBytes = 64
	}
	if r.MaxRXBytes == 0 {
		r.MaxRXBytes = 128
	}
	if r.MaxStepTimeoutMS == 0 {
		r.MaxStepTimeoutMS = 5000
	}
	if r.RAMDedupEntries == 0 {
		r.RAMDedupEntries = 8
	}
	return r
}

// ResourceReport 发布一帧 ResourceReport(0x19)。
//
// 调用顺序：必须在 Hello() **之后**（Hello 会清空能力字段，见 Hello 的注释）。
func (d *Device) ResourceReport(report ResourceReportData) error {
	if err := d.assertSimNode(); err != nil {
		return err
	}
	report = report.withDefaults()

	// buses_blob：至少给一条 UART 总线，让 transport 能力有实际内容。
	// 每个 field 必须是 length-delimited 的 bus entry（服务端
	// validateNestedResourceBlob 会拒绝其它 wire type）。
	buses := frame.SubEncoder()
	uart := frame.SubEncoder()
	uart.EncodeString(1, "uart0")
	uart.EncodeVarint(2, 1)      // port
	uart.EncodeVarint(3, 4)      // default_tx_pin
	uart.EncodeVarint(4, 5)      // default_rx_pin
	uart.EncodeVarint(5, 115200) // max_baud
	buses.EncodeBytes(1, uart.Bytes())

	// channels_blob：repeated field 1，每个是 channel_entry 子消息。
	// entry 内 field 1 = id、field 5 = enabled（服务端 schema 的必填项是 field 1）。
	channels := frame.SubEncoder()
	for _, channel := range report.Channels {
		entry := frame.SubEncoder()
		entry.EncodeVarint(1, channel.ID)
		entry.EncodeBool(5, channel.Enabled)
		channels.EncodeBytes(1, entry.Bytes())
	}

	// command_engine 子消息：十个字段全部必填，缺一即整帧被拒收。
	engine := frame.SubEncoder()
	engine.EncodeVarint(1, uint64(report.Revision))
	engine.EncodeString(2, report.BootID)
	engine.EncodeBool(3, report.SupportsChannelCmdV2)
	engine.EncodeBool(4, report.SupportsBoundedBatch)
	engine.EncodeBool(5, report.SupportsFinally)
	engine.EncodeVarint(6, uint64(report.MaxBatchSteps))
	engine.EncodeVarint(7, uint64(report.MaxTXBytes))
	engine.EncodeVarint(8, uint64(report.MaxRXBytes))
	engine.EncodeVarint(9, uint64(report.MaxStepTimeoutMS))
	engine.EncodeVarint(10, uint64(report.RAMDedupEntries))

	encoder := frame.NewEncoder(frame.MsgResourceReport)
	encoder.EncodeString(1, report.Platform)
	encoder.EncodeVarint(2, uint64(len(report.Channels))) // resource_count
	encoder.EncodeBytes(3, buses.Bytes())
	encoder.EncodeBytes(4, channels.Bytes())
	encoder.EncodeBytes(9, engine.Bytes())
	return d.publish(d.UpTopic(), encoder.Bytes())
}

// ConfigResult 发布一帧 ConfigResult(0x05)，宣告清单应用结果。
//
// 该帧是 requireAppliedManifest() 的唯一写入路径：成功时服务端把节点置为
// config_status="applied" + config_sync_state="in_sync"。字段 1/2/4 必填
// （manifest_id / success / sync_id），且服务端要求当前节点的
// config_version==manifestID 且 config_sync_state=="syncing" 且
// last_sync_id==syncID，否则按过期结果忽略。
func (d *Device) ConfigResult(manifestID, syncID string, success bool) error {
	if err := d.assertSimNode(); err != nil {
		return err
	}
	encoder := frame.NewEncoder(frame.MsgConfigRslt)
	encoder.EncodeString(1, manifestID)
	encoder.EncodeBool(2, success)
	encoder.EncodeVarint(3, 0) // config_epoch
	encoder.EncodeString(4, syncID)
	return d.publish(d.UpTopic(), encoder.Bytes())
}

// ConfigReport 发布一帧 ConfigReport(0x11)，回报名单容量实况。
// 服务端只记录与比对，不改变状态；用于 SIM-CHAN-005 的"节点主动拉取"口径。
func (d *Device) ConfigReport(manifestID string, channelCount int) error {
	if err := d.assertSimNode(); err != nil {
		return err
	}
	encoder := frame.NewEncoder(frame.MsgConfigReport)
	encoder.EncodeString(1, fmt.Sprintf("cfgrpt-%d", time.Now().UnixMilli()))
	encoder.EncodeString(2, manifestID)
	encoder.EncodeVarint(3, 0)
	encoder.EncodeVarint(4, uint64(channelCount))
	encoder.EncodeVarint(5, 0)
	return d.publish(d.UpTopic(), encoder.Bytes())
}

// HelloThenReport 是 Hello + ResourceReport 的原子组合，
// 供夹具与后续场景使用，从根上避免"先报资源再握手"的顺序错误。
func (d *Device) HelloThenReport(version, model string, channelCount int, channels []ReportedChannel) *frame.Decoder {
	d.env.T.Helper()
	decoder := d.Hello(version, model, channelCount)
	report := ResourceReportData{
		Channels:             channels,
		BootID:               fmt.Sprintf("sim-boot-%s-%d", d.env.RunID, time.Now().UnixMilli()),
		SupportsChannelCmdV2: true,
		SupportsBoundedBatch: true,
		SupportsFinally:      true,
	}
	if err := d.ResourceReport(report); err != nil {
		d.env.Fatalf("节点 %s ResourceReport 失败: %v", d.NodeID, err)
	}
	return decoder
}

func randomNonce() uint32 {
	buffer := make([]byte, 4)
	if _, err := rand.Read(buffer); err != nil {
		panic("harness: 生成 nonce 失败: " + err.Error())
	}
	nonce := binary.BigEndian.Uint32(buffer)
	if nonce == 0 {
		nonce = 1
	}
	return nonce
}

// ---------- 能力快照年龄（MaxCapabilityAge 的可控夹具） ----------

// CapabilitySnapshotAge 回报该节点的 ResourceReport 快照已经"有多旧"，
// 读法与 commandexec.currentCapabilities() **同源**：都是
// now() - nodes.resource_reported_at。它只读，不写任何东西。
//
// 为什么场景需要它：能力窗口年龄（commandexec.MaxCapabilityAge）此前在仿真里
// 没有任何端到端回归 —— 仿真器每次都能让快照保持新鲜，因此"把窗口从 5 分钟
// 放宽到 15 分钟"这类改动无论对错，138 个场景都照样全绿。本函数与
// AgeCapabilitySnapshot 一起把"年龄"变成可断言、可造坏的量。
func (d *Device) CapabilitySnapshotAge() (time.Duration, error) {
	raw, err := d.rawResourceReportedAt()
	if err != nil {
		return 0, err
	}
	if !raw.Valid {
		// 与 currentCapabilities() 的 nil 分支同一语义：没有快照 ≠ 年龄很大。
		return 0, fmt.Errorf("节点 %s 没有 resource_reported_at（能力快照不存在）", d.NodeID)
	}
	return time.Since(raw.Time.UTC()), nil
}

// AgeCapabilitySnapshot 把该节点的能力快照"做旧"到至少 age 之前，返回是否
// 确实发生了写库（false = 现有快照本来就比目标时刻更旧，无需改动）。
//
// ⚠ 取舍声明（对照设计 §3 原则 2）：
// 设计原则 2 要求"不得用直连 DB 替代本可走 API 的断言"，并把直连 DB 限定在
// (a) 界面看不到的持久化事实、(b) 轮询收敛条件。本函数属于**第 (c) 类，
// 由本次修复显式追加并在此留痕**：它不是断言，而是**造前置条件的写入**。
//   - 观测侧仍然全部走 API：年龄由 CapabilitySnapshotAge 读、动作可用性由
//     GET /edge-devices/:id/actions 读、拒绝语义由 POST .../operations 的响应
//     读、陈旧节点计数由 GET /api/v1/metrics/summary 读。没有任何断言依赖本写入；
//   - 它之所以必须存在：resource_reported_at 在生产里**只有**一个写入者
//     （backend/internal/nodemgr/handler_resources.go:876 的 time.Now().UTC()），
//     ResourceReport(0x19) 帧里**没有**任何时间戳字段，MaxCapabilityAge 也没有
//     环境变量或配置入口。因此"让一个健康节点真的变陈旧"在不等待 15 分钟真实
//     时间的前提下，只能改这一列；
//   - 与真实硬件的关系：真实节点若停止上报能力，服务端不会再刷新该列，节点就会
//     自然变陈旧 —— 本函数精确复现"距离上一次 ResourceReport 已经过了 age"这一
//     状态，而不改变任何其它字段（boot_id / revision / capabilities / manifest 全部保持）。
//
// 与"改库绕过门禁"的区别：这些字段**没有被修改**，门禁照常按真实数据判定；
// 变的只是"这份真实数据有多旧"。
func (d *Device) AgeCapabilitySnapshot(age time.Duration) (bool, error) {
	if age <= 0 {
		return false, fmt.Errorf("AgeCapabilitySnapshot 需要正的 age（收到 %s）；默认路径请直接不调用本函数", age)
	}
	current, err := d.rawResourceReportedAt()
	if err != nil {
		return false, err
	}
	if current.Valid && time.Since(current.Time.UTC()) >= age {
		return false, nil // 本来就够旧，幂等返回。
	}
	target := time.Now().UTC().Add(-age)
	result, err := d.env.SQL().Exec(
		"UPDATE nodes SET resource_reported_at = $1 WHERE node_id = $2", target, d.NodeID)
	if err != nil {
		return false, fmt.Errorf("做旧节点 %s 的能力快照失败: %w", d.NodeID, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("做旧节点 %s 后无法确认影响行数: %w", d.NodeID, err)
	}
	if rows != 1 {
		return false, fmt.Errorf("做旧节点 %s 的能力快照影响了 %d 行，期望恰好 1 行", d.NodeID, rows)
	}
	return true, nil
}

// rawResourceReportedAt 读取该节点当前的 resource_reported_at 原始值。
// 返回值 .Valid=false 表示该列为 NULL（节点从未上报过能力）。
func (d *Device) rawResourceReportedAt() (sql.NullTime, error) {
	var reported sql.NullTime
	if err := d.env.SQL().QueryRow(
		"SELECT resource_reported_at FROM nodes WHERE node_id = $1", d.NodeID).Scan(&reported); err != nil {
		return reported, fmt.Errorf("读取节点 %s 的 resource_reported_at 失败: %w", d.NodeID, err)
	}
	return reported, nil
}

// EncodeInt16BigEndian 供夹具按 ConfigParser 的 binary 规则编码字段值。
func EncodeInt16BigEndian(value int) ([]byte, error) {
	if value < math.MinInt16 || value > math.MaxInt16 {
		return nil, fmt.Errorf("值 %d 超出 int16 范围", value)
	}
	out := make([]byte, 2)
	binary.BigEndian.PutUint16(out, uint16(int16(value)))
	return out, nil
}

// EncodeUint16BigEndian 供夹具编码无符号 16 位字段。
func EncodeUint16BigEndian(value uint32) ([]byte, error) {
	if value > math.MaxUint16 {
		return nil, fmt.Errorf("值 %d 超出 uint16 范围", value)
	}
	out := make([]byte, 2)
	binary.BigEndian.PutUint16(out, uint16(value))
	return out, nil
}
