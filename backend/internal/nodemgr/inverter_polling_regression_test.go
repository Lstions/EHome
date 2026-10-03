package nodemgr

import (
	"encoding/hex"
	"encoding/json"
	"testing"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
)

// 现场缺陷回归（2026-10-03，节点 30EDA0A9A808 / 边设备 4 "逆变器"）
// ================================================================
//
// 现象：UART1 上的泰琪丰逆变器从未收到任何指令。用户的物理证据是决定性的 ——
// TTL↔RS232 转接板的 Tx/Rx 指示灯从未亮过，说明固件根本没有在 UART1 上发送
// 过任何字节（若只是电平不匹配，TX 灯仍会亮，只是对端收不到）。
//
// 根因链（本条测试钉住其中最关键的一环）：
//   驱动 GetCommandTemplates() 返回 nil                     <- 演进方案 C6 (2026-09-06)
//     -> CommandIsManifestCandidate 对任何命令都为 false
//     -> encodeConfigManifest 为该边设备编码 0 条命令
//        （生产日志原文：edge_device 4 (channel 4): no valid template_id in
//          channel.template_ids "" (resolved 0), skipping command encoding）
//     -> 固件 scheduler_add_channel 登记 command_count = 0
//     -> 调度器从不投递 CMD_SAMPLE -> UART1 TX 引脚永不变化 -> 转接板灯不亮
//
// 因此"驱动至少要声明一条可调度命令"是该设备可被采集的必要条件。
// 本测试用真实注册表与真实驱动，防止有人再次清空它的模板集合而不自知。

func newInverterEdgeFixture() (*drivers.Registry, models.Channel, models.EdgeDevice) {
	registry := drivers.NewRegistry()
	drivers.RegisterBuiltInDrivers(registry)

	ch := models.Channel{
		ID: 4, NodeID: "NODE001", HardwareType: "UART", HardwareID: "UART1",
		BusType: "UART", Enabled: true,
		// 现场该通道的 template_ids 就是空串 —— 这正是编码器无模板可用的直接原因。
		TemplateIDs: "",
	}
	dev := models.EdgeDevice{
		ID: 4, NodeID: "NODE001", Name: "逆变器", Type: "techfine_inverter",
		ChannelID: 4, Enabled: true,
	}
	return registry, ch, dev
}

// TestInverterDriverDeclaresSchedulableCommands 守住必要条件本身。
func TestInverterDriverDeclaresSchedulableCommands(t *testing.T) {
	registry, _, _ := newInverterEdgeFixture()
	drv, err := registry.Get("techfine_inverter")
	if err != nil {
		t.Fatalf("techfine_inverter not registered: %v", err)
	}
	cmds := getCommandTemplatesFromDriver(drv)
	if len(cmds) == 0 {
		t.Fatal("techfine_inverter declares no command templates — the inverter can never be polled " +
			"(UART1 will never transmit; this is the 2026-10-03 field defect)")
	}
	schedulable := 0
	for _, c := range cmds {
		if c.Schedulable {
			schedulable++
		}
	}
	if schedulable == 0 {
		t.Fatal("techfine_inverter declares templates but none is Schedulable — " +
			"CommandIsManifestCandidate rejects all of them, so 0 commands get encoded")
	}
}

// TestInverterCommandBecomesManifestCandidate 证明"驱动声明了命令"确实能变成
// 一条会被编码下发（进而被固件调度）的 manifest 命令 —— 而不是停在驱动层。
func TestInverterCommandBecomesManifestCandidate(t *testing.T) {
	registry, _, _ := newInverterEdgeFixture()
	drv, err := registry.Get("techfine_inverter")
	if err != nil {
		t.Fatalf("registry.Get: %v", err)
	}

	// 模拟"用户在界面上把 read_status 的采集周期设为 1000ms"。
	stored := map[string]int{"read_status": 1000}
	templates := []models.ConfigTemplate{
		// 注意 WriteData 必须与驱动的 WriteData 完全一致（hex 大小写归一后比较），
		// 这正是 createTemplatesFromDriver 在真实创建流程里写入 config_templates 的值。
		{ID: 101, WriteData: "485354530D"},
	}

	candidates := 0
	for _, cmd := range getCommandTemplatesFromDriver(drv) {
		if CommandIsManifestCandidate(cmd, stored, templates) {
			candidates++
		}
	}
	if candidates == 0 {
		t.Fatal("no techfine command qualifies as a manifest candidate even with " +
			"read_status interval=1000 and a matching ConfigTemplate — " +
			"the manifest would encode 0 commands and UART1 would stay silent")
	}

	// 反向对照：没有匹配模板时必须是 0 —— 证明上面的非零不是恒真断言。
	noTemplates := []models.ConfigTemplate{}
	zero := 0
	for _, cmd := range getCommandTemplatesFromDriver(drv) {
		if CommandIsManifestCandidate(cmd, stored, noTemplates) {
			zero++
		}
	}
	if zero != 0 {
		t.Fatalf("candidate count must be 0 when no ConfigTemplate matches, got %d "+
			"(otherwise this test cannot distinguish 'fixed' from 'always true')", zero)
	}

	// 再一个反向对照：interval=0（用户在界面上关掉）必须不产生候选。
	disabled := map[string]int{"read_status": 0}
	off := 0
	for _, cmd := range getCommandTemplatesFromDriver(drv) {
		if CommandIsManifestCandidate(cmd, disabled, templates) {
			off++
		}
	}
	if off != 0 {
		t.Fatalf("interval=0 must disable polling, got %d candidates", off)
	}
}

// TestInverterIntervalMapMatchesDriverCommandIDs 守住"界面可配置"的契约：
// 用户能在 command_intervals 里写的 key 必须真的是驱动的命令 id，
// 否则 API 侧 ValidateCommandIntervals 会 400，用户根本无法开启采集。
func TestInverterIntervalMapMatchesDriverCommandIDs(t *testing.T) {
	registry, _, _ := newInverterEdgeFixture()
	drv, err := registry.Get("techfine_inverter")
	if err != nil {
		t.Fatalf("registry.Get: %v", err)
	}
	known := map[string]bool{}
	for _, cmd := range getCommandTemplatesFromDriver(drv) {
		known[cmd.ID] = true
	}
	// 现场需求："像 BMS 一样可配置参数定期采集"。这些是要能开起来的。
	for _, id := range []string{"read_status", "read_grid", "read_output", "read_battery"} {
		if !known[id] {
			t.Errorf("command id %q is not schedulable for techfine_inverter — "+
				"the UI cannot enable periodic polling for it", id)
		}
	}
	// 必须与 ControlActions 的 read_* 命名一致，避免"动作能读、轮询读不到"的割裂。
	for id := range known {
		if len(id) < 5 || id[:5] != "read_" {
			t.Errorf("unexpected command id %q; techfine polling ids should mirror ControlActions' read_* names", id)
		}
	}
}

// TestInverterEdgeDeviceCapacityAllowsThreePolls 守住编码器上限（MaxCommandsPerEdgeDevice=3）
// 与"用户可选多条"之间的边界：默认只开 1 条，用户最多能开 3 条。
func TestInverterEdgeDeviceCapacityAllowsThreePolls(t *testing.T) {
	registry, _, _ := newInverterEdgeFixture()
	drv, _ := registry.Get("techfine_inverter")
	all := getCommandTemplatesFromDriver(drv)
	if len(all) < 3 {
		t.Fatalf("expected the inverter to expose at least 3 pollable commands, got %d", len(all))
	}
	templates := []models.ConfigTemplate{}
	for _, cmd := range all {
		templates = append(templates, models.ConfigTemplate{ID: uint(len(templates) + 1), WriteData: cmd.WriteData})
	}
	// 恰好 3 条启用：必须放行（用户在界面上的合法配置）
	stored := map[string]int{}
	for i := 0; i < 3; i++ {
		stored[all[i].ID] = 5000
	}
	n := 0
	for _, cmd := range all {
		if CommandIsManifestCandidate(cmd, stored, templates) {
			n++
		}
	}
	if n != 3 {
		t.Fatalf("3 enabled polls should yield exactly 3 candidates, got %d", n)
	}
	if n > MaxCommandsPerEdgeDevice {
		t.Fatalf("candidate count %d exceeds collector limit %d", n, MaxCommandsPerEdgeDevice)
	}

	// 第 4 条启用时，候选数会到 4 —— 但 CommandIsManifestCandidate 只是**谓词**，
	// 它不做容量控制。容量门禁在写入侧（api.ValidateManifestCommandCapacity）与
	// 编码前（validateManifestScheduleCapacity* ），两者都必须能挡住 4 条。
	//
	// 这里显式记录这个分工，避免后来者误以为谓词会拒绝超额配置 ——
	// 那正是 2026-09-17 生产事故的形态：写入侧放行、编码侧拒绝、整包下发失败。
	stored[all[3].ID] = 5000
	n4 := 0
	for _, cmd := range all {
		if CommandIsManifestCandidate(cmd, stored, templates) {
			n4++
		}
	}
	if n4 != 4 {
		t.Fatalf("predicate should count all 4 enabled polls (capacity is enforced elsewhere), got %d", n4)
	}
	if n4 <= MaxCommandsPerEdgeDevice {
		t.Fatalf("fixture is not exercising the over-limit case: %d candidates vs limit %d",
			n4, MaxCommandsPerEdgeDevice)
	}
	// 真正的编码前门禁必须拒绝它。用真实的门禁函数（快照版）验证 ——
	// 它正是 sendConfigManifest 在下发前调用的那一个。
	reg2, ch2, dev2 := newInverterEdgeFixture()
	// 4 条全部启用：把全部命令写进 command_intervals
	four := map[string]int{}
	for _, c := range all {
		four[c.ID] = 5000
	}
	iv4, _ := json.Marshal(four)
	dev2.CommandIntervals = iv4
	snap := &manifestSnapshot{
		templates:      templates,
		channels:       []models.Channel{ch2},
		edgeDevices:    []models.EdgeDevice{dev2},
		edgesByChannel: map[uint][]models.EdgeDevice{4: {dev2}},
	}
	// 4 条启用 -> 必须被拒
	err := validateManifestScheduleCapacityFromSnapshot(snap, reg2,
		[]models.Channel{ch2}, true, manifestLimits{maxTemplates: maxManifestTemplates,
			maxChannels: maxManifestChannels, maxTemplateIDs: maxLegacyTemplateIDs})
	if err == nil {
		t.Fatal("encoder-side capacity gate accepted 4 polling commands; the collector limit is 3")
	}
	// 3 条启用 -> 必须放行（否则用户根本无法把 3 条都开起来）
	three := map[string]int{}
	for i := 0; i < 3; i++ {
		three[all[i].ID] = 5000
	}
	dev3 := dev2
	iv, _ := json.Marshal(three)
	dev3.CommandIntervals = iv
	snap3 := &manifestSnapshot{
		templates:      templates,
		channels:       []models.Channel{ch2},
		edgeDevices:    []models.EdgeDevice{dev3},
		edgesByChannel: map[uint][]models.EdgeDevice{4: {dev3}},
	}
	if err := validateManifestScheduleCapacityFromSnapshot(snap3, reg2,
		[]models.Channel{ch2}, true, manifestLimits{maxTemplates: maxManifestTemplates,
			maxChannels: maxManifestChannels, maxTemplateIDs: maxLegacyTemplateIDs}); err != nil {
		t.Fatalf("3 polling commands must be accepted (collector limit is 3): %v", err)
	}
}

// TestInverterTemplateWriteDataIsCRTerminated 守住协议细节：泰琪丰查询帧是
// ASCII 行且必须以 CR 结尾（固件按行空闲定帧）。少了 CR，逆变器不会应答。
func TestInverterTemplateWriteDataIsCRTerminated(t *testing.T) {
	registry, _, _ := newInverterEdgeFixture()
	drv, err := registry.Get("techfine_inverter")
	if err != nil {
		t.Fatalf("registry.Get: %v", err)
	}
	for _, cmd := range getCommandTemplatesFromDriver(drv) {
		if cmd.WriteData == "" {
			t.Errorf("%s: empty WriteData — nothing would be transmitted", cmd.ID)
			continue
		}
		// WriteData 是 hex；解码后检查结尾是否为 0x0D
		decoded, err := hexDecode(cmd.WriteData)
		if err != nil {
			t.Errorf("%s: WriteData %q is not valid hex: %v", cmd.ID, cmd.WriteData, err)
			continue
		}
		if len(decoded) == 0 || decoded[len(decoded)-1] != 0x0D {
			t.Errorf("%s: frame %q does not end with CR (0x0D) — the GB3024 will not answer",
				cmd.ID, string(decoded))
		}
		if cmd.ReadLength != 0 {
			t.Errorf("%s: ReadLength=%d; ASCII line protocol needs 0 (line-idle framing), "+
				"otherwise every response is rejected as a short read (error 0x03)",
				cmd.ID, cmd.ReadLength)
		}
	}
}

// hexDecode 是测试内的极小辅助，避免为一行解码引入新依赖。
func hexDecode(s string) ([]byte, error) {
	return hex.DecodeString(s)
}
