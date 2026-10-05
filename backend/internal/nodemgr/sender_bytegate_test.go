package nodemgr

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
	"ehome/backend/testutil"

	"gorm.io/gorm"
)

// =====================================================================
// R1: ConfigManifest 编码后字节门禁（单次 MQTT 下行事件上限）
//
// 事实基础（全部读自固件树，不引用设计文档的估算值）：
//   - CONFIG_MQTT_BUFFER_SIZE = 2048   (esp32-collector/sdkconfig.defaults:24)
//   - 下行 topic = "nodes/<node_id>/control" (ehome_mqtt.c:665)
//   - 固件无下行重组：超过 in_buffer 的 PUBLISH 被 esp-mqtt 拆成多个
//     MQTT_EVENT_DATA，每个都当完整帧交给 msg_handler_process。
//   - config_mgr.h 的存储上限：MAX_TEMPLATES=16、MAX_TEMPLATE_IDS=8、
//     MAX_GPIO_CONFIGS=12、MAX_PWM_CONFIGS=8、MAX_DMA_CONFIGS=8、
//     MAX_EDGE_DEVICES_PER_CH=5、MAX_COMMANDS_PER_DEVICE=3、
//     bus_config[64]、manifest_id[32]、sync_id[64]。
//     MAX_CHANNELS：S3=5，C6=4（硬件上限，非调参项）。
//
// 本文件用**真实编码器 encodeConfigManifest** 测最坏帧长，并锁定门禁边界。
// =====================================================================

const r1WorstDeviceID = "r1-bytegate-worst"

// r1WorstWriteData 是固件 config_template_t.write_data[64] 的上限：64 字节二进制。
func r1WorstWriteData(fill string) string { return strings.Repeat(fill, 64) }

var r1CmdWriteData = []string{r1WorstWriteData("aa"), r1WorstWriteData("bb"), r1WorstWriteData("cc")}

// r1StubDriver 只实现 CommandTemplateProvider，用于把 3 条可调度命令编码进
// edge_device_groups（field 9）子帧。
type r1StubDriver struct {
	typ  string
	cmds []drivers.CommandTemplate
}

func (d *r1StubDriver) DeviceType() string                             { return d.typ }
func (d *r1StubDriver) DeviceName() string                             { return d.typ }
func (d *r1StubDriver) OEM() string                                    { return "test" }
func (d *r1StubDriver) Category() string                               { return "test" }
func (d *r1StubDriver) HardwareTypes() []string                        { return []string{"uart"} }
func (d *r1StubDriver) GetSensorDefinitions() []drivers.SensorData     { return nil }
func (d *r1StubDriver) ParseData([]byte) ([]drivers.SensorData, error) { return nil, nil }
func (d *r1StubDriver) GetCommandTemplates() []drivers.CommandTemplate { return d.cmds }

func r1StubCommands() []drivers.CommandTemplate {
	cmds := make([]drivers.CommandTemplate, 0, 3)
	for i, wd := range r1CmdWriteData {
		cmds = append(cmds, drivers.CommandTemplate{
			ID: fmt.Sprintf("cmd-%d", i), Type: "read", WriteData: wd,
			ReadLength: 0xFFFF, DelayMs: 0xFFFFFFFF, IntervalMs: 60000, Schedulable: true,
		})
	}
	return cmds
}

func r1StubRegistry() *drivers.Registry {
	registry := drivers.NewRegistry()
	registry.Register(&r1StubDriver{typ: r1WorstDeviceID, cmds: r1StubCommands()})
	return registry
}

// r1MaxTemplatesFor 返回一个固件上限下的最坏模板集：16 个模板，write_data 64 B，
// read_length/delay_ms 取最大 varint。前 3 个承载命令 write_data。
func r1MaxTemplatesFor(nodeID string) []models.ConfigTemplate {
	templates := make([]models.ConfigTemplate, 0, maxManifestTemplates)
	for i := 0; i < maxManifestTemplates; i++ {
		wd := r1WorstWriteData("dd")
		if i < len(r1CmdWriteData) {
			wd = r1CmdWriteData[i]
		}
		templates = append(templates, models.ConfigTemplate{
			ID: uint(i + 1), NodeID: nodeID, WriteData: wd,
			ReadLength: 0xFFFF, DelayMs: 0xFFFFFFFF,
		})
	}
	return templates
}

// r1MaxBusConfig 生成 64 B bus_config；前两字节是 UART TX/RX 引脚，逐通道唯一，
// 否则 validateManifestAuthority 会判 GPIO 冲突（那是引脚规划问题，不是编码上限问题）。
func r1MaxBusConfig(channelIndex int) string {
	bus := make([]byte, 64)
	bus[0] = byte(2*channelIndex + 1)
	bus[1] = byte(2*channelIndex + 2)
	for i := 2; i < len(bus); i++ {
		bus[i] = 0xAB
	}
	return hex.EncodeToString(bus)
}

// r1MaxGPIOs / r1MaxPWMs / r1MaxDMAs 取固件上限且各标量取会写且最大的值。
func r1MaxGPIOs(nodeID string, count int) []models.GPIOConfig {
	gpios := make([]models.GPIOConfig, 0, count)
	for i := 0; i < count; i++ {
		gpios = append(gpios, models.GPIOConfig{NodeID: nodeID, Pin: 11 + i, Direction: 3, InitialLevel: 1, Enabled: true})
	}
	return gpios
}

func r1MaxPWMs(nodeID string, count int) []models.PWMConfig {
	pwms := make([]models.PWMConfig, 0, count)
	for i := 0; i < count; i++ {
		pwms = append(pwms, models.PWMConfig{
			NodeID: nodeID, HardwareID: fmt.Sprintf("PWM%d", i), Channel: uint8(i), Pin: 23 + i,
			// 最大合法值：freq*2^res <= 40e6；res 取最小合法值 4 才能让 frequency
			// 的 varint 最长（2,500,000 -> 4 B）。
			Frequency: 2500000, Duty: 10000, Resolution: 4, AutoStart: true, Enabled: true,
		})
	}
	return pwms
}

func r1MaxDMAs(count int) []models.DmaChannelConfig {
	dmas := make([]models.DmaChannelConfig, 0, count)
	for i := 0; i < count; i++ {
		dmas = append(dmas, models.DmaChannelConfig{DmaID: uint32(i), Enabled: true, BindTo: "0123456789abcdef"})
	}
	return dmas
}

// r1MaxSnapshot 构造"固件上限下的最坏快照"：16 模板（64 B write_data）、
// 每通道 5 edge × 3 command、64 B bus_config、12 GPIO、8 PWM、8 DMA、
// 31 B manifest_id、36 B sync_id、log_stream 打开。
// channelCount 传固件 MAX_CHANNELS：S3=5，C6=4。
func r1MaxSnapshot(channelCount int) (*manifestSnapshot, []models.Channel, *drivers.Registry) {
	templates := r1MaxTemplatesFor(r1WorstDeviceID)
	channels := make([]models.Channel, 0, channelCount)
	edgesByChannel := make(map[uint][]models.EdgeDevice, channelCount)
	nextEdgeID := uint(1)
	for c := 0; c < channelCount; c++ {
		chID := uint(c + 1)
		channels = append(channels, models.Channel{
			ID: chID, NodeID: r1WorstDeviceID, BusType: "UART", HardwareType: "UART",
			Enabled: true, DmaEnabled: true, IntervalMs: 60000,
			TemplateIDs: "1,2,3,4,5,6,7,8", BusConfig: r1MaxBusConfig(c),
		})
		for e := 0; e < maxEdgeDevicesPerChannel; e++ {
			edgesByChannel[chID] = append(edgesByChannel[chID], models.EdgeDevice{
				ID: nextEdgeID, NodeID: r1WorstDeviceID, ChannelID: chID, Type: r1WorstDeviceID,
				HardwareID: "0xFF", IntervalMs: 60000, Enabled: true,
			})
			nextEdgeID++
		}
	}
	snap := &manifestSnapshot{
		node: models.Node{
			NodeID: r1WorstDeviceID, ProtocolVersion: "2.6",
			LogStreamEnabled: true, LogStreamLevel: 4,
		},
		templates: templates, channels: channels, edgeDevices: nil,
		dmaConfigs: r1MaxDMAs(8), gpioConfigs: r1MaxGPIOs(r1WorstDeviceID, 12),
		pwmConfigs: r1MaxPWMs(r1WorstDeviceID, 8), edgesByChannel: edgesByChannel,
	}
	return snap, channels, r1StubRegistry()
}

func r1Encode(t *testing.T, snap *manifestSnapshot, channels []models.Channel, registry *drivers.Registry, manifestID, syncID string) []byte {
	t.Helper()
	payload, err := encodeConfigManifest(snap, channels, true, SyncDecision{
		DeviceID: r1WorstDeviceID, SyncID: syncID, Action: SyncActionFull, Reason: "r1-byte-gate",
	}, registry, manifestID)
	if err != nil {
		t.Fatalf("encodeConfigManifest: %v", err)
	}
	return payload
}

// TestR1ManifestWorstCaseEncodedBytes 用真实编码器测量固件上限下的最坏帧长。
// S3（MAX_CHANNELS=5）与 C6（MAX_CHANNELS=4）分别打印并锁定。
func TestR1ManifestWorstCaseEncodedBytes(t *testing.T) {
	for _, tc := range []struct {
		profile  string
		channels int
		want     int
	}{
		{"S3", 5, 3100},
		{"C6", 4, 2838},
	} {
		snap, channels, registry := r1MaxSnapshot(tc.channels)
		manifestID := "v2-" + strings.Repeat("a", 28) // 31 B = manifest_id[32]-1
		syncID := strings.Repeat("s", 36)             // 36 B = 后端 uuid.New().String()
		payload := r1Encode(t, snap, channels, registry, manifestID, syncID)
		t.Logf("R1 worst-case ConfigManifest %s (MAX_CHANNELS=%d): %d bytes (single-event limit %d, over by %d)",
			tc.profile, tc.channels, len(payload), MaxManifestWireBytes, len(payload)-MaxManifestWireBytes)
		if tc.want != 0 && len(payload) != tc.want {
			t.Errorf("%s worst-case encoded length = %d, want %d (pinned; update only together with the V3 §8 R1 measurement)",
				tc.profile, len(payload), tc.want)
		}
		if len(payload) <= MaxManifestWireBytes {
			t.Fatalf("%s worst case %d B must exceed the %d B single-event limit — if not, R1 is gone and this test must be rewritten",
				tc.profile, len(payload), MaxManifestWireBytes)
		}
	}
}

// TestR1ManifestWorstCaseVsDocumentedEstimate 记录真实测量与文档估算的差距。
func TestR1ManifestWorstCaseVsDocumentedEstimate(t *testing.T) {
	const documentedEstimate = 2463 // 文档 §2.4 的 S3 最坏估算
	snap, channels, registry := r1MaxSnapshot(5)
	payload := r1Encode(t, snap, channels, registry, "v2-"+strings.Repeat("a", 28), strings.Repeat("s", 36))
	t.Logf("R1: measured S3 worst case %d B vs documented estimate %d B (delta %+d B); vs single-event limit %d B (delta %+d B)",
		len(payload), documentedEstimate, len(payload)-documentedEstimate,
		MaxManifestWireBytes, len(payload)-MaxManifestWireBytes)
	if len(payload) <= documentedEstimate {
		t.Errorf("measured worst case %d B no longer exceeds the documented estimate %d B; the V3 §8 R1 numbers must be re-derived", len(payload), documentedEstimate)
	}
}

// TestR1ByteGateBoundary 锁定门禁的精确边界：2011 放行、2012 拒绝，
// 且错误信息必须同时含实际字节数与上限。
func TestR1ByteGateBoundary(t *testing.T) {
	if err := checkManifestWireBytes(MaxManifestWireBytes); err != nil {
		t.Fatalf("%d B must pass (exactly at limit), got %v", MaxManifestWireBytes, err)
	}
	if err := checkManifestWireBytes(1); err != nil {
		t.Fatalf("1 B must pass, got %v", err)
	}
	err := checkManifestWireBytes(MaxManifestWireBytes + 1)
	if err == nil {
		t.Fatalf("%d B must be rejected", MaxManifestWireBytes+1)
	}
	msg := err.Error()
	if !strings.Contains(msg, fmt.Sprintf("%d", MaxManifestWireBytes+1)) {
		t.Errorf("error must contain the actual byte count %d: %q", MaxManifestWireBytes+1, msg)
	}
	if !strings.Contains(msg, fmt.Sprintf("%d", MaxManifestWireBytes)) {
		t.Errorf("error must contain the limit %d: %q", MaxManifestWireBytes, msg)
	}
}

// r1SplitExtra 把 extra 拆成 syncID/manifestID 的长度增量：
// (syncLen-1)+(idLen-1) == extra，syncLen<=36（uuid），idLen<=31（manifest_id[32]-1）。
func r1SplitExtra(extra int) (int, int) {
	syncLen := 1 + extra
	if syncLen > 36 {
		syncLen = 36
	}
	idLen := 1 + (extra - (syncLen - 1))
	return syncLen, idLen
}

// r1SizedSnapshot 构造可调大小的真实快照（用于精确命中 2011/2012 B 边界）：
// templateCount 个模板（第 0 个 write_data 长 firstWriteBytes 字节，其余 64 B）、
// 5 通道（第 0 个 bus_config 长 busBytes 字节，其余 64 B）、
// 每通道 edgesPerChannel 个 edge（每个 3 条可调度命令）、无 GPIO/PWM/DMA。
func r1SizedSnapshot(templateCount, firstWriteBytes, busBytes, edgesPerChannel int) (*manifestSnapshot, []models.Channel) {
	templates := make([]models.ConfigTemplate, 0, templateCount)
	for i := 0; i < templateCount; i++ {
		wdBytes := 64
		if i == 0 {
			wdBytes = firstWriteBytes
		}
		templates = append(templates, models.ConfigTemplate{
			ID: uint(i + 1), NodeID: "r1-sized", WriteData: strings.Repeat("ab", wdBytes),
			ReadLength: 0xFFFF, DelayMs: 0xFFFFFFFF,
		})
	}
	channels := make([]models.Channel, 0, 5)
	edgesByChannel := make(map[uint][]models.EdgeDevice, 5)
	nextEdgeID := uint(1)
	for c := 0; c < 5; c++ {
		n := 64
		if c == 0 {
			n = busBytes
		}
		chID := uint(c + 1)
		channels = append(channels, models.Channel{
			ID: chID, NodeID: "r1-sized", BusType: "UART", HardwareType: "UART",
			Enabled: true, DmaEnabled: true, IntervalMs: 60000, BusConfig: strings.Repeat("ab", n),
		})
		for e := 0; e < edgesPerChannel; e++ {
			edgesByChannel[chID] = append(edgesByChannel[chID], models.EdgeDevice{
				ID: nextEdgeID, NodeID: "r1-sized", ChannelID: chID, Type: r1WorstDeviceID,
				HardwareID: "0xFF", IntervalMs: 60000, Enabled: true,
			})
			nextEdgeID++
		}
	}
	snap := &manifestSnapshot{
		node:           models.Node{NodeID: "r1-sized", ProtocolVersion: "2.6"},
		templates:      templates,
		channels:       channels,
		edgesByChannel: edgesByChannel,
	}
	return snap, channels
}

// TestR1ByteGateBoundaryRealEncoding 用真实编码器构造恰好 2011 B 与 2012 B 的
// ConfigManifest：2011 必须放行、2012 必须被拒。这证明门禁边界不是纸面算术，
// 而是真实 encodeConfigManifest 输出上的判断。
//
// 杠杆（每个的字节代价都精确可预测，因为编码器对固定长度字段不做压缩）：
//   - 删一个 edge 组（3 条命令子帧）：−42 B
//   - 第 0 个模板 write_data 每短 1 B：−1 B
//   - 第 0 个通道 bus_config 每短 1 B：−1 B
//   - sync_id + manifest_id 每长 1 B：+1 B（最长 +35/+30）
func TestR1ByteGateBoundaryRealEncoding(t *testing.T) {
	target := MaxManifestWireBytes
	registry := r1StubRegistry()
	const maxEdges = 5 * maxEdgeDevicesPerChannel
	build := func(edges, wBytes, busBytes, idLen, syncLen int) []byte {
		snap, chs := r1SizedSnapshot(maxManifestTemplates, wBytes, busBytes, edges)
		return r1Encode(t, snap, chs, registry, strings.Repeat("m", idLen), strings.Repeat("s", syncLen))
	}
	// 先按整组 edge 粗调（每组约 37 B），再按 write_data 逐字节精调到 2011 B。
	// 每删一组 edge 帧长只降 36–37 B，所以只要把 over 压进 0..63，write_data
	// 杠杆（1 B/字节，长度 varint 不跨档）就能精确命中。
	var payload2011, payload2012 []byte
	for edges := maxEdges; edges >= 0 && payload2011 == nil; edges-- {
		over := len(build(edges, 64, 64, 1, 1)) - target
		if over < 0 || over > 63 {
			continue
		}
		payload2011 = build(edges, 64-over, 64, 1, 1)
		if len(payload2011) != target {
			t.Fatalf("constructed %d B, want exactly %d B (edges=%d over=%d)", len(payload2011), target, edges, over)
		}
		// 拒绝边界：manifest_id 从 1 B 加到 2 B（field 1 长度 +1）。
		payload2012 = build(edges, 64-over, 64, 2, 1)
	}
	if payload2011 == nil {
		t.Fatalf("could not construct a real %d B manifest from encoder output", target)
	}
	if len(payload2012) != target+1 {
		t.Fatalf("constructed %d B for the reject boundary, want %d B", len(payload2012), target+1)
	}

	if err := checkManifestWireBytes(len(payload2011)); err != nil {
		t.Errorf("real %d B manifest must pass, got %v", len(payload2011), err)
	}
	err := checkManifestWireBytes(len(payload2012))
	if err == nil {
		t.Fatalf("real %d B manifest must be rejected", len(payload2012))
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("%d", len(payload2012))) ||
		!strings.Contains(err.Error(), fmt.Sprintf("%d", MaxManifestWireBytes)) {
		t.Errorf("rejection must name both the actual size and the limit: %q", err.Error())
	}
	t.Logf("R1 gate boundary pinned with real encoder output: %d B passes, %d B rejected", len(payload2011), len(payload2012))
}

// TestR1WorstCaseByteBudgetBreakdown 逐项测量最坏帧的字节预算构成，
// 供 §8 R1 决定 phase 2b 需要收紧哪些字段上限、各能省多少。
func TestR1WorstCaseByteBudgetBreakdown(t *testing.T) {
	registry := r1StubRegistry()
	buildFull := func(templates, channels, edges, gpios, pwms, dmas int, logStream bool, idLen, syncLen int) int {
		snap := &manifestSnapshot{
			node: models.Node{NodeID: r1WorstDeviceID, ProtocolVersion: "2.6",
				LogStreamEnabled: logStream, LogStreamLevel: 4},
			templates:      r1MaxTemplatesFor(r1WorstDeviceID)[:templates],
			channels:       nil,
			edgesByChannel: map[uint][]models.EdgeDevice{},
			dmaConfigs:     r1MaxDMAs(dmas),
			gpioConfigs:    r1MaxGPIOs(r1WorstDeviceID, gpios),
			pwmConfigs:     r1MaxPWMs(r1WorstDeviceID, pwms),
		}
		nextEdge := uint(1)
		for c := 0; c < channels; c++ {
			chID := uint(c + 1)
			snap.channels = append(snap.channels, models.Channel{
				ID: chID, NodeID: r1WorstDeviceID, BusType: "UART", HardwareType: "UART",
				Enabled: true, DmaEnabled: true, IntervalMs: 60000,
				TemplateIDs: "1,2,3,4,5,6,7,8", BusConfig: r1MaxBusConfig(c),
			})
			for e := 0; e < edges; e++ {
				snap.edgesByChannel[chID] = append(snap.edgesByChannel[chID], models.EdgeDevice{
					ID: nextEdge, NodeID: r1WorstDeviceID, ChannelID: chID, Type: r1WorstDeviceID,
					HardwareID: "0xFF", IntervalMs: 60000, Enabled: true,
				})
				nextEdge++
			}
		}
		return len(r1Encode(t, snap, snap.channels, registry, strings.Repeat("m", idLen), strings.Repeat("s", syncLen)))
	}
	// 逐项累加，差值即该项的字节预算。
	type step struct {
		name string
		size int
	}
	steps := []step{
		{"空 manifest（无模板/通道/外设）", buildFull(0, 0, 0, 0, 0, 0, false, 1, 1)},
		{"+ log_stream 子帧", buildFull(0, 0, 0, 0, 0, 0, true, 1, 1)},
		{"+ 16 模板（write_data 64 B, read=0xFFFF, delay=0xFFFFFFFF）", buildFull(16, 0, 0, 0, 0, 0, true, 1, 1)},
		{"+ 5 通道（bus_config 64 B, 8 template_ids）", buildFull(16, 5, 0, 0, 0, 0, true, 1, 1)},
		{"+ 每通道 5 edge × 3 命令", buildFull(16, 5, 5, 0, 0, 0, true, 1, 1)},
		{"+ 12 GPIO", buildFull(16, 5, 5, 12, 0, 0, true, 1, 1)},
		{"+ 8 PWM", buildFull(16, 5, 5, 12, 8, 0, true, 1, 1)},
		{"+ 8 DMA", buildFull(16, 5, 5, 12, 8, 8, true, 1, 1)},
		{"+ manifest_id 31 B / sync_id 36 B", buildFull(16, 5, 5, 12, 8, 8, true, 31, 36)},
	}
	prev := 0
	for i, s := range steps {
		delta := s.size - prev
		prev = s.size
		t.Logf("R1 budget %-52s %5d B (delta %+d)", s.name, s.size, delta)
		if i == len(steps)-1 {
			if s.size != 3100 {
				t.Errorf("cumulative worst case = %d B, want 3100 B (pinned)", s.size)
			}
		}
	}
}

// r1WorstCapabilitiesJSON 覆盖最坏快照用到的全部 GPIO/PWM 资源（引脚 1..30、PWM 0..7）。
func r1WorstCapabilitiesJSON() string {
	var b strings.Builder
	b.WriteString(`{"buses":{"gpio":[`)
	for i := 1; i <= 30; i++ {
		if i > 1 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"id":"GPIO%d","pin":%d}`, i, i)
	}
	b.WriteString(`],"pwm":[`)
	for i := 0; i < 8; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"id":"PWM%d","channel":%d,"max_resolution_bits":20}`, i, i)
	}
	b.WriteString(`]}}`)
	return b.String()
}

// r1SeedWorstCaseDB 把固件上限下的最坏配置写进测试库，走完整的
// SendConfigManifestWithDecision 路径（authority + capacity + reconcile + encode + gate）。
func r1SeedWorstCaseDB(t *testing.T, channelCount int) *gorm.DB {
	t.Helper()
	db := testutil.OpenTestDB(t)
	if err := db.Create(&models.Node{
		NodeID: r1WorstDeviceID, Status: "online", ProtocolVersion: "2.6",
		Capabilities:     r1WorstCapabilitiesJSON(),
		Config:           `{"dma_configs":[{"dma_id":0,"enabled":true,"bind_to":"0123456789abcdef"},{"dma_id":1,"enabled":true,"bind_to":"0123456789abcdef"},{"dma_id":2,"enabled":true,"bind_to":"0123456789abcdef"},{"dma_id":3,"enabled":true,"bind_to":"0123456789abcdef"},{"dma_id":4,"enabled":true,"bind_to":"0123456789abcdef"},{"dma_id":5,"enabled":true,"bind_to":"0123456789abcdef"},{"dma_id":6,"enabled":true,"bind_to":"0123456789abcdef"},{"dma_id":7,"enabled":true,"bind_to":"0123456789abcdef"}]}`,
		LogStreamEnabled: true,
		LogStreamLevel:   4,
	}).Error; err != nil {
		t.Fatal(err)
	}
	for _, tmpl := range r1MaxTemplatesFor(r1WorstDeviceID) {
		tmpl.ID = 0
		if err := db.Create(&tmpl).Error; err != nil {
			t.Fatal(err)
		}
	}
	for c := 0; c < channelCount; c++ {
		ch := models.Channel{
			NodeID: r1WorstDeviceID, BusType: "UART", HardwareType: "UART",
			Enabled: true, DmaEnabled: true, IntervalMs: 60000,
			TemplateIDs: "1,2,3,4,5,6,7,8", BusConfig: r1MaxBusConfig(c),
		}
		if err := db.Create(&ch).Error; err != nil {
			t.Fatal(err)
		}
		for e := 0; e < maxEdgeDevicesPerChannel; e++ {
			if err := db.Create(&models.EdgeDevice{
				NodeID: r1WorstDeviceID, ChannelID: ch.ID, Type: r1WorstDeviceID,
				Name: fmt.Sprintf("edge-%d-%d", c, e), HardwareID: "0xFF",
				IntervalMs: 60000, Enabled: true,
			}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, gpio := range r1MaxGPIOs(r1WorstDeviceID, 12) {
		if err := db.Create(&gpio).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, pwm := range r1MaxPWMs(r1WorstDeviceID, 8) {
		if err := db.Create(&pwm).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db
}

// TestR1WorstCaseRejectedBySendPath 端到端：最坏配置必须被门禁拒绝、不发布、
// 且节点被 fail() 驱动到 config_status=failed，错误信息含实际字节数与上限。
func TestR1WorstCaseRejectedBySendPath(t *testing.T) {
	db := r1SeedWorstCaseDB(t, 5)
	mgr, mock := newManifestTestManager(t, db, r1StubRegistry())
	err := mgr.SendConfigManifestWithDecision(SyncDecision{
		DeviceID: r1WorstDeviceID, SyncID: strings.Repeat("s", 36),
		Action: SyncActionFull, Reason: "r1-byte-gate",
	})
	if err == nil {
		t.Fatal("worst-case manifest above the single-event limit must be rejected, got nil")
	}
	if len(mock.publishedPayload) != 0 {
		t.Fatalf("over-limit manifest must not be published, got %d bytes", len(mock.publishedPayload))
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("%d", MaxManifestWireBytes)) {
		t.Errorf("error must contain the limit %d: %v", MaxManifestWireBytes, err)
	}
	if !strings.Contains(err.Error(), "bytes") {
		t.Errorf("error must be diagnosable (actual byte count): %v", err)
	}
	var node models.Node
	if err := db.Where("node_id = ?", r1WorstDeviceID).First(&node).Error; err != nil {
		t.Fatal(err)
	}
	if node.ConfigStatus != "failed" || node.ConfigSyncState != "failed" {
		t.Errorf("rejected manifest must mark the node failed: config_status=%q config_sync_state=%q", node.ConfigStatus, node.ConfigSyncState)
	}
	t.Logf("R1 end-to-end rejection: %v", err)
}

// TestR1NormalConfigStillPublishes 防"门禁误杀"：常规配置仍能正常发布。
func TestR1NormalConfigStillPublishes(t *testing.T) {
	db := testutil.OpenTestDB(t)
	seedManifestSnapshotDB(t, db, "r1-bytegate-normal")
	registry := drivers.NewRegistry()
	registry.Register(&drivers.JiabaidaBMSDriver{})
	mgr, mock := newManifestTestManager(t, db, registry)
	if err := mgr.SendConfigManifestWithDecision(SyncDecision{
		DeviceID: "r1-bytegate-normal", SyncID: "r1-normal", Action: SyncActionFull, Reason: "r1-normal",
	}); err != nil {
		t.Fatalf("normal config must still publish, got %v", err)
	}
	if len(mock.publishedPayload) == 0 {
		t.Fatal("normal config was not published")
	}
	if len(mock.publishedPayload) > MaxManifestWireBytes {
		t.Fatalf("normal config published %d B, above the gate limit %d", len(mock.publishedPayload), MaxManifestWireBytes)
	}
	t.Logf("R1 normal config published: %d B (limit %d)", len(mock.publishedPayload), MaxManifestWireBytes)
}
