package nodemgr

// manifest_crosslang_test.go -- ConfigManifest(0x04) 跨语言对锚：**后端 Go 真实编码器**
// ↔ **固件 C 真实解码器**。
//
// # 为什么需要它（本卡要补的缺口）
//
// protocol/vectors/wire_primitives.txt 的 20 个 case 覆盖了 writecmd / hello /
// hello_ack / varint 边界 / bytes 原语 / tag / device_op 家族，**没有 0x04**。
// 而 0x04 恰好是：最复杂载荷（最坏实测 S3 3100 B / C6 2838 B）；配置事务这条
// "项目历史上最危险的路径"；3.0 配置同步的关键路径；且后端 encodeConfigManifest（Go）
// 与固件 parse_manifest（C）是**两套独立实现**。
//
// 两端各自的单测都绿 —— 但没有任何东西证明它们**互相能懂**。
// 这正是 S0 向量存在的理由（"两端可能同时自洽却互相不通"），只是没覆盖到 0x04。
//
// # 这个测试怎么对锚
//
//   1. 构造一份代表性 manifestSnapshot（含边界值）；
//   2. 调**真实生产编码器** encodeConfigManifest() 产出 0x04 载荷；
//   3. 把载荷的 hex 写到临时文件；
//   4. 执行固件侧解码器 firmware_manifest_crosslang（原样编入
//      components/config_mgr/config_mgr.c，调 config_mgr_stage_manifest() 生产解码路径）；
//   5. 解码器把解出的字段逐条打到 stdout，本测试**逐字段**比对并打印对照表。
//
// # 诚实边界
//
//   - 不跑 TLS、不跑真机（与 device_e2e_firmware_test.go 同一套边界）；
//   - 本用例走"文件传字节"而不是 socket：**帧层面的分片/粘包**由既有
//     TestCrossLanguageFirmwareClientOverSocket 覆盖；0x04 这一卡要钉的是
//     "**字段语义**两端是否一致"，不是再证一次分片。
//   - 固件**不会发** 0x04：MSG_CONFIG_MFST 在固件里只出现在接收路径
//     （handler_config.c 头注释 "Receives:"、msg_handler.c 的 case、
//     config_mgr.c 的 parse、app_callbacks.c 的下行判定），没有发送点。
//     ⇒ 反向（固件编码 → 后端解码）**该方向不存在**，本卡不硬造。

import (
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ehome/backend/internal/models"
)

const crosslangNodeID = "crosslang-node-1"

// crosslangSnapshot 构造一份**代表性** manifest：2 template、2 channel，
// 字段值刻意取边界：
//   - manifest_id 取 char[32] 允许的最长（31 字符）；
//   - sync_id 取 40 字符（< 固件 sync_id[64]-1）；
//   - write_data 一空一非空（空 ⇒ 编码器省略 field 2）；
//   - read_length=127 / delay_ms=128 正好跨过 varint 的 1→2 字节分界；
//   - bus_config 非空（bytes 原语）；
//   - 一个 channel enabled=true，一个 false（避免"恰好都相同"掩盖问题）。
func crosslangSnapshot() (*manifestSnapshot, []models.Channel, string, string) {
	manifestID := "mf-" + strings.Repeat("a", 28)     // 31 B == sizeof(manifest_id)-1
	syncID := "0123456789abcdef0123456789abcdef0123" // 40 B < 63

	templates := []models.ConfigTemplate{
		{ID: 1, NodeID: crosslangNodeID, WriteData: "0x01020304", ReadLength: 127, DelayMs: 128},
		{ID: 2, NodeID: crosslangNodeID, WriteData: "", ReadLength: 0, DelayMs: 0},
	}
	channels := []models.Channel{
		{
			ID: 1, NodeID: crosslangNodeID, HardwareType: "UART", HardwareID: "0x01",
			IntervalMs: 127, BusType: "UART", BusConfig: "06070100", Enabled: true,
			TemplateIDs: "1", DmaEnabled: true,
		},
		{
			ID: 2, NodeID: crosslangNodeID, HardwareType: "I2C", HardwareID: "0x76",
			IntervalMs: 128, BusType: "I2C", BusConfig: "4c", Enabled: false,
			TemplateIDs: "2", DmaEnabled: false,
		},
	}
	snap := &manifestSnapshot{
		node: models.Node{
			NodeID: crosslangNodeID, ProtocolVersion: "2.6",
			LogStreamEnabled: true, LogStreamLevel: 4,
		},
		templates: templates,
		channels:  channels,
		// 刻意留空：edgeDevices / dmaConfigs / gpioConfigs / pwmConfigs
		// 不在最小闭环内（见报告"未验证项"）。
	}
	return snap, channels, manifestID, syncID
}

// runFirmwareDecoder 把载荷交给固件解码器，返回 key->value。
func runFirmwareDecoder(t *testing.T, bin string, payload []byte) map[string]string {
	t.Helper()
	dir := t.TempDir()
	hexPath := filepath.Join(dir, "payload.hex")
	if err := os.WriteFile(hexPath, []byte(hex.EncodeToString(payload)), 0o600); err != nil {
		t.Fatalf("写 hex 文件失败: %v", err)
	}
	cmd := exec.Command(bin, hexPath)
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("固件解码器失败: %v\n载荷 %d B\n输出:\n%s", err, len(payload), out.String())
	}
	got := map[string]string{}
	for _, line := range strings.Split(out.String(), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		got[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	if got["DECODE"] != "OK" {
		t.Fatalf("固件侧 DECODE != OK，原始输出:\n%s", out.String())
	}
	return got
}

type fieldRow struct {
	Field string
	GoVal string
	CVal  string
}

// TestConfigManifestCrossLanguageGoEncodeCDecode 是本卡的主用例。
func TestConfigManifestCrossLanguageGoEncodeCDecode(t *testing.T) {
	bin := os.Getenv("EHOME_FW_MANIFEST_DECODER")
	if bin == "" {
		// 刻意 Skip 而不是静默通过 —— 由 tools/run_firmware_backend_e2e.sh 负责设置。
		t.Skip("EHOME_FW_MANIFEST_DECODER 未设置：本用例需固件侧 0x04 解码器。" +
			"请用 tools/run_firmware_backend_e2e.sh 运行（它会先构建再设置该变量）")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("EHOME_FW_MANIFEST_DECODER=%q 指向的文件不存在: %v", bin, err)
	}

	snap, channels, manifestID, syncID := crosslangSnapshot()

	payload, err := encodeConfigManifest(snap, channels, true, SyncDecision{
		DeviceID: crosslangNodeID, SyncID: syncID, Action: SyncActionFull, Reason: "crosslang-anchor",
	}, nil, manifestID)
	if err != nil {
		t.Fatalf("encodeConfigManifest: %v", err)
	}
	if len(payload) < 1 || payload[0] != 0x04 {
		t.Fatalf("载荷首字节 = 0x%02X, 期望 0x04 (MSG_CONFIG_MFST)", payload[0])
	}
	t.Logf("Go 编码器产出 %d B 的 0x04 载荷（首字节 0x%02X = MSG_CONFIG_MFST）", len(payload), payload[0])

	got := runFirmwareDecoder(t, bin, payload)

	var rows []fieldRow
	add := func(field, want, key string) {
		rows = append(rows, fieldRow{field, want, got[key]})
	}

	// ---- 顶层 ----
	add("manifest_id (f1)", manifestID, "MANIFEST_ID")
	add("sync_id (f8)", syncID, "SYNC_ID")
	add("template_count (f3 x N)", strconv.Itoa(len(snap.templates)), "TEMPLATE_COUNT")
	add("channel_count (f4 x N)", strconv.Itoa(len(channels)), "CHANNEL_COUNT")
	add("log_stream.enabled (f10.1)", "1", "LOG_STREAM_ENABLED")
	add("log_stream.level (f10.2)", "4", "LOG_STREAM_LEVEL")

	// ---- template 子字段 ----
	for i, tm := range snap.templates {
		writeHex := strings.TrimPrefix(tm.WriteData, "0x")
		writeHex = strings.TrimPrefix(writeHex, "\x5cx") // 兼容 0x / \x 两种前缀写法
		if writeHex == "" {
			writeHex = "-" // 固件侧空 bytes 打印为 "-"
		}
		add(fmt.Sprintf("T%d.id (f3.1)", i), strconv.Itoa(int(tm.ID)), fmt.Sprintf("T%d.ID", i))
		add(fmt.Sprintf("T%d.write_data (f3.2)", i), writeHex, fmt.Sprintf("T%d.WRITE_DATA", i))
		add(fmt.Sprintf("T%d.read_length (f3.3)", i), strconv.Itoa(int(tm.ReadLength)), fmt.Sprintf("T%d.READ_LENGTH", i))
		add(fmt.Sprintf("T%d.delay_ms (f3.4)", i), strconv.Itoa(int(tm.DelayMs)), fmt.Sprintf("T%d.DELAY_MS", i))
	}

	// ---- channel 子字段 ----
	// useV2=true：编码器**故意不写** channel 子字段 2/3/4
	// （hardware_id / template_ids / interval_ms）—— v2 走 edge_device_groups (f9)。
	// 因此固件解出 0 是**设计如此**，不是漂移。下面显式记录，
	// 既不伪装成"一致"，也不谎报成"漂移"。
	busTypeNum := map[string]int{"UART": 1, "I2C": 2, "SPI": 3, "USB": 4, "ADC": 5}
	for i, ch := range channels {
		bt := strconv.Itoa(busTypeNum[strings.ToUpper(ch.BusType)])
		busCfg := strings.ToLower(ch.BusConfig)
		if busCfg == "" {
			busCfg = "-"
		}
		en := "0"
		if ch.Enabled {
			en = "1"
		}
		dma := "0"
		if ch.DmaEnabled {
			dma = "1"
		}
		add(fmt.Sprintf("C%d.id (f4.1)", i), strconv.Itoa(int(ch.ID)), fmt.Sprintf("C%d.ID", i))
		add(fmt.Sprintf("C%d.enabled (f4.5)", i), en, fmt.Sprintf("C%d.ENABLED", i))
		add(fmt.Sprintf("C%d.bus_type (f4.6)", i), bt, fmt.Sprintf("C%d.BUS_TYPE", i))
		add(fmt.Sprintf("C%d.bus_config (f4.7)", i), busCfg, fmt.Sprintf("C%d.BUS_CONFIG", i))
		add(fmt.Sprintf("C%d.dma_enabled (f4.8)", i), dma, fmt.Sprintf("C%d.DMA_ENABLED", i))
	}

	// ---- 输出对照表 + 判定 ----
	var bad []fieldRow
	t.Logf("── 逐字段对照表（Go 编码值 vs 固件解出值）──")
	for _, r := range rows {
		mark := "OK "
		if r.GoVal != r.CVal {
			mark = "XX"
			bad = append(bad, r)
		}
		t.Logf("  %s %-26s go=%-34s c=%-34s", mark, r.Field, r.GoVal, r.CVal)
	}

	t.Logf("── v2 路径不下发的字段（预期固件解出 0，不是漂移）──")
	t.Logf("  C0.HARDWARE_ID=%s C0.INTERVAL_MS=%s（编码器只在 !useV2 时写 f4.2/f4.4）",
		got["C0.HARDWARE_ID"], got["C0.INTERVAL_MS"])

	if len(bad) > 0 {
		var sb strings.Builder
		for _, r := range bad {
			fmt.Fprintf(&sb, "\n  %s: go=%q c=%q", r.Field, r.GoVal, r.CVal)
		}
		t.Fatalf("跨语言对锚失败：%d 个字段不一致%s\n"+
			"两端各自的单测可能都绿 —— 这正是 S0 要拦的「同时自洽却互相不通」。",
			len(bad), sb.String())
	}
	t.Logf("PASS 0x04 跨语言对锚：%d 个字段全部一致（Go 编码器 <-> 固件生产解码器）", len(rows))
}
