package nodemgr

import (
	"testing"

	"ehome/backend/internal/deviceaction"
)

// ==================== G6: parseHardwareID 与地址门禁的第二个口径 (2026-09-21) ====================
//
// 后端有两个 hardware_id 解析口径:
//
//	(a) deviceaction.ParseHardwareAddress — **门禁**, 报错, 强制 1..254,
//	    ""/"0" 归一为历史默认地址 1。
//	(b) nodemgr.parseHardwareID (sender.go) — 编码器, parse-or-0, 从不报错。
//
// (b) 的输出经 sender_snapshot.go 编进 ConfigManifest 字段 9 (edge_device_groups)
// 的子字段 2, 固件存进 config_edge_device_t.hardware_id 后目前没有任何寻址消费者
// —— 真正上总线的地址来自 ChannelCmdV2 里编译好的 step, 那条路径走 (a)。
//
// 本测试不改变任何线上行为 (契约要求: 动 manifest 是协议变更, 风险高),
// 只把"同一输入两处结果不同"钉住, 让口径漂移不可能悄悄发生。
// 若将来有人让 (b) 与 (a) 对齐, 这张表会变红, 必须同时给出固件侧结论。

func TestParseHardwareIDDivergesFromAddressGate(t *testing.T) {
	tests := []struct {
		name string
		in   string
		// parseHardwareID 的结果
		wantEncoded uint64
		// ParseHardwareAddress 的判定: 是否接受
		gateAccepts bool
		// gateAccepts 为真时解析出的地址
		wantGateAddr uint8
		// 两处口径是否一致 (值相同 / 都接受同一域)
		diverges bool
		// 差异说明 (写进失败信息, 让人一眼看懂为什么这张表存在)
		why string
	}{
		{"hex_agree", "0x76", 0x76, true, 0x76, false, "both read 0x76 as 118"},
		{"decimal_agree", "5", 5, true, 5, false, "both read 5 as 5"},
		{"uppercase_hex_agree", "0X76", 0x76, true, 0x76, false, "0X prefix accepted by both"},
		{"empty_diverges", "", 0, true, 1, true, "encoder yields 0; gate treats empty as the legacy default address 1"},
		{"zero_diverges", "0", 0, true, 1, true, "encoder yields 0; gate treats \"0\" as the legacy default address 1"},
		{"whitespace_diverges", "   ", 0, true, 1, true, "encoder yields 0; gate trims to empty then defaults to 1"},
		{"upper_bound_diverges", "254", 254, true, 254, false, "254 is legal in both"},
		{"above_bound_diverges", "255", 255, false, 0, true, "encoder silently encodes 255; gate rejects (reserved, >254)"},
		{"hex_above_bound_diverges", "0xFF", 255, false, 0, true, "encoder silently encodes 255; gate rejects"},
		{"hex_zero_diverges", "0x00", 0, false, 0, true, "encoder yields 0; gate rejects (below lower bound, not the \"default\" form)"},
		{"huge_decimal_diverges", "999999", 999999, false, 0, true, "encoder encodes an illegal WIRE value; gate rejects"},
		{"negative_diverges", "-1", 0, false, 0, true, "encoder silently yields 0; gate rejects"},
		{"float_diverges", "1.5", 0, false, 0, true, "encoder silently yields 0; gate rejects"},
		{"letter_suffix_diverges", "12abc", 0, false, 0, true, "encoder silently yields 0; gate rejects"},
		{"the_incident_value_diverges", "UART1", 0, false, 0, true, "the 2026-09-20 bus name: encoder silently yields 0, gate rejects loudly"},
		{"lowercase_bus_name_diverges", "uart1", 0, false, 0, true, "same as UART1, case-insensitive parse failure"},
		{"padded_legal_diverges", " 1 ", 1, true, 1, false, "both trim and read 1"},
		{"padded_illegal_diverges", " UART1 ", 0, false, 0, true, "encoder yields 0; gate rejects after trimming"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			encoded := parseHardwareID(tc.in)
			if encoded != tc.wantEncoded {
				t.Fatalf("parseHardwareID(%q)=%d, want %d", tc.in, encoded, tc.wantEncoded)
			}

			addr, err := deviceaction.ParseHardwareAddress(tc.in)
			if tc.gateAccepts {
				if err != nil {
					t.Fatalf("ParseHardwareAddress(%q) unexpectedly rejected: %v", tc.in, err)
				}
				if addr != tc.wantGateAddr {
					t.Fatalf("ParseHardwareAddress(%q)=%d, want %d", tc.in, addr, tc.wantGateAddr)
				}
			} else {
				if err == nil {
					t.Fatalf("ParseHardwareAddress(%q) accepted an illegal address (=%d); the gate is the single truth source and must reject it", tc.in, addr)
				}
			}

			// Two 口径 "agree" only when BOTH accept the value AND read it as the
			// same number. An encoder that keeps producing a number for a value the
			// gate rejects is the divergence this test exists to pin.
			agree := err == nil && addr == uint8(encoded)
			if tc.diverges {
				if agree {
					t.Fatalf("input %q no longer diverges between the two 口径 (encoded=%d gate=%d/%v): %s — if the encoder was aligned with the gate on purpose, this table AND the firmware-side conclusion must be updated together", tc.in, encoded, addr, err, tc.why)
				}
				return
			}
			if !agree {
				t.Fatalf("input %q unexpectedly diverges (encoded=%d gate=%d/%v): %s", tc.in, encoded, addr, err, tc.why)
			}
		})
	}
}

// 这条把"分歧不是理论推演, 而是可触发"钉死: 对同一个事故值, 两个口径给出
// 完全不同的结果, 且编码器那一侧**不报错**。
func TestParseHardwareIDNeverReportsErrors(t *testing.T) {
	for _, in := range []string{"UART1", "999999", "-1", "0xFF", "abc", "0x"} {
		// The encoder never signals failure: it either parses something or silently
		// returns 0. That silence is the property being pinned here.
		_ = parseHardwareID(in)
		if _, err := deviceaction.ParseHardwareAddress(in); err == nil {
			t.Fatalf("ParseHardwareAddress(%q) must reject this value; the two 口径 would then be indistinguishable", in)
		}
	}
}

// parseHardwareID 的既有表格 (sender_test.go TestParseHardwareID) 必须继续成立:
// 本卡只加注释与诊断测试, 不改 manifest 行为。
func TestParseHardwareIDBehaviourUnchanged(t *testing.T) {
	if got := parseHardwareID("0x76"); got != 0x76 {
		t.Fatalf("0x76 -> %d, want 118", got)
	}
	if got := parseHardwareID("5"); got != 5 {
		t.Fatalf("5 -> %d, want 5", got)
	}
	if got := parseHardwareID(""); got != 0 {
		t.Fatalf("empty -> %d, want 0", got)
	}
	if got := parseHardwareID("0xFFFF"); got != 0xFFFF {
		t.Fatalf("0xFFFF -> %d, want 65535 (legacy behaviour preserved)", got)
	}
	if got := parseHardwareID("999999"); got != 999999 {
		t.Fatalf("999999 -> %d, want 999999 (legacy behaviour preserved)", got)
	}
}
