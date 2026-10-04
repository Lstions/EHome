package api

import (
	"strings"
	"testing"

	"ehome/backend/internal/models"
)

// TestEnsureUARTBusConfig_AcceptsAllThreeHardwareIDForms 是 2026-10-04 回归的锁。
//
// 事故：784f210 给 UART 通道加了一个「按 hardware_id 匹配上报资源」的能力门禁，
// 但当时只做了 EqualFold(entry.ID, hardware_id)。hardware_id 在真实数据里有
// 三种写法，全都必须能命中：
//
//	"UART1"  生产通道 / 前端下拉框
//	"uart1"  固件 hw_tables.c 的小写 id（仿真 harness 也用它）
//	"0x01"   仿真夹具与部分前端历史写法：把「第几个串口」写成十六进制
//
// 后果不是「少补一个字段」，而是**建通道直接 400** —— 本来能用的请求整个被拒。
// 实测：仿真 140 个场景里 9 个因此变红，CI 的 backend-scenarios 从绿转红
// （0ad5975 success -> 784f210 failure）。
//
// 若有人再把匹配收紧回「只比 ID 字符串」，后两种写法会立刻变红。
func TestEnsureUARTBusConfig_AcceptsAllThreeHardwareIDForms(t *testing.T) {
	// 固件真实上报形态：id 小写、带 port（对标 sim harness/device.go:595-599）。
	node := &models.Node{
		NodeID: "SIMNODE",
		Capabilities: `{"buses":{"uart":[` +
			`{"id":"uart0","port":1,"default_tx_pin":4,"default_rx_pin":5,"max_baud":115200},` +
			`{"id":"uart1","port":2,"default_tx_pin":20,"default_rx_pin":21,"max_baud":115200}]}}`,
	}

	cases := []struct {
		name       string
		hardwareID string
		wantTx     int
		wantRx     int
	}{
		{"固件小写 id", "uart0", 4, 5},
		{"生产大写 id", "UART1", 20, 21},
		{"仿真十六进制 0x01 -> port 1", "0x01", 4, 5},
		{"仿真十六进制 0x02 -> port 2", "0x02", 20, 21},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ch := &models.Channel{HardwareID: tc.hardwareID, BusType: "UART", HardwareType: "uart"}
			if err := ensureUARTBusConfig(node, ch); err != nil {
				t.Fatalf("hardware_id %q 应能匹配到上报资源并补齐 bus_config，实得错误：%v", tc.hardwareID, err)
			}
			if strings.TrimSpace(ch.BusConfig) == "" {
				t.Fatalf("hardware_id %q 补齐后 bus_config 仍为空", tc.hardwareID)
			}
			pins, err := channelRoutePins(*ch)
			if err != nil {
				t.Fatalf("补齐后的 bus_config %q 无法解析引脚：%v", ch.BusConfig, err)
			}
			if len(pins) < 2 || pins[0] != tc.wantTx || pins[1] != tc.wantRx {
				t.Fatalf("hardware_id %q 补齐出的引脚 = %v，期望 TX=%d RX=%d", tc.hardwareID, pins, tc.wantTx, tc.wantRx)
			}
		})
	}
}

// TestEnsureUARTBusConfig_UnknownPortIsActionable 保证「端口写法认得出、但节点
// 没上报该端口」时报错可行动（点明缺哪个端口 + 实际有哪些），而不是笼统的
// 「找不到资源」——后者会让现场排查多绕一圈。
func TestEnsureUARTBusConfig_UnknownPortIsActionable(t *testing.T) {
	node := &models.Node{
		NodeID:       "SIMNODE",
		Capabilities: `{"buses":{"uart":[{"id":"uart0","port":1,"default_tx_pin":4,"default_rx_pin":5}]}}`,
	}
	ch := &models.Channel{HardwareID: "0x03", BusType: "UART", HardwareType: "uart"}

	err := ensureUARTBusConfig(node, ch)
	if err == nil {
		t.Fatal("hardware_id 0x03 指向未上报的端口 3，应当报错")
	}
	if !strings.Contains(err.Error(), "端口 3") {
		t.Fatalf("报错应点明缺失的端口号，实得：%v", err)
	}
	if !strings.Contains(err.Error(), "uart0(port=1)") {
		t.Fatalf("报错应列出实际已上报的端口，实得：%v", err)
	}
	if !strings.Contains(err.Error(), errUARTCapabilityUnavailable.Error()) {
		t.Fatalf("报错应可映射为能力缺失（400 而非 500），实得：%v", err)
	}
}

// TestParseUARTPortToken 钉死端口解析的边界：只认明确的十进制/十六进制端口，
// 绝不把 "UART1" 这类 id 误解析成 0 —— 否则会静默匹配到 port 0 的资源，
// 把用户的 UART1 通道补成 UART0 的引脚。
func TestParseUARTPortToken(t *testing.T) {
	cases := []struct {
		in    string
		want  int
		valid bool
	}{
		{"0x01", 1, true},
		{"0X1", 1, true},
		{"1", 1, true},
		{"0", 0, true},
		{"0x00", 0, true},
		{"UART1", 0, false},
		{"uart1", 0, false},
		{"", 0, false},
		{"0x", 0, false},
		{"0xZZ", 0, false},
		{"12x", 0, false},
	}
	for _, tc := range cases {
		got, ok := parseUARTPortToken(tc.in)
		if ok != tc.valid || (ok && got != tc.want) {
			t.Fatalf("parseUARTPortToken(%q) = (%d,%v)，期望 (%d,%v)", tc.in, got, ok, tc.want, tc.valid)
		}
	}
}
