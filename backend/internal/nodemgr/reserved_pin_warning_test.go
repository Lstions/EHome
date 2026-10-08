package nodemgr

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"ehome/backend/internal/models"
)

// TestReservedPinName 钉住后端那份"保留脚副本"的内容。
//
// ⚠ 为什么需要它：这张表是**固件 hw_tables.h 的手工副本**（后端没有更好的来源），
// 手工副本会腐烂。本用例的作用不是"证明表是对的"（只有固件知道），
// 而是**让任何改动都必须是被看见的** —— 改表就会改这个用例。
//
// 数据来源：esp32-collector/components/hw_profile/include/hw_tables.h
//
//	ESP32S3: HW_RESERVED_BOOT=0, USB_DN=19, USB_DP=20, LED=48
//	ESP32C6: HW_RESERVED_BOOT=9, USB_DN=12, USB_DP=13, LED=8
func TestReservedPinName(t *testing.T) {
	cases := []struct {
		platform string
		pin      int
		want     string
	}{
		{"ESP32S3", 0, "BOOT/strapping"},
		{"ESP32S3", 19, "USB_D-"},
		{"ESP32S3", 20, "USB_D+"},
		{"ESP32S3", 48, "RGB LED (WS2812)"},
		{"ESP32C6", 9, "BOOT/strapping"},
		{"ESP32C6", 12, "USB_D-"},
		{"ESP32C6", 13, "USB_D+"},
		{"ESP32C6", 8, "RGB LED (WS2812)"},
		// 别名都要认（Hello 写入的 platform 实测为 "ESP32C6"/"ESP32S3"）。
		{"esp32s3", 48, "RGB LED (WS2812)"},
		{"ESP32-S3", 48, "RGB LED (WS2812)"},
		{"C6", 8, "RGB LED (WS2812)"},
		// 非保留脚必须**不**命中（否则告警会变成噪音，很快被忽略）。
		{"ESP32S3", 43, ""},
		{"ESP32S3", 47, ""},
		{"ESP32C6", 21, ""},
		{"ESP32C6", 22, ""},
		// 未知平台不猜（不能拿 S3 的表去套新芯片）。
		{"ESP32H2", 48, ""},
		{"", 48, ""},
	}
	for _, tc := range cases {
		got, ok := reservedPinName(tc.platform, tc.pin)
		if tc.want == "" {
			if ok {
				t.Errorf("reservedPinName(%q, %d) = %q, true —— 非保留脚不该命中（告警会变噪音）",
					tc.platform, tc.pin, got)
			}
			continue
		}
		if !ok || got != tc.want {
			t.Errorf("reservedPinName(%q, %d) = %q, %v; want %q, true",
				tc.platform, tc.pin, got, ok, tc.want)
		}
	}
}

// TestReservedPinsMatchFirmwareTables 是**跨仓一致性**判据（后端副本 vs 固件真源）。
//
// ⚠ 这是本组用例里最有价值的一条：后端那张表是手工副本，
// 一旦固件改了 HW_RESERVED_* 而没人同步后端，这里就会红。
// 与 protoframe 的 firmwareTLSRecordLen 同族（读固件源文件核对，而不是信任副本）。
//
// 它凭什么会失败：把 reservedPinName 里 S3 的 48 改成别的值，本用例立刻红。
func TestReservedPinsMatchFirmwareTables(t *testing.T) {
	fw := readFirmwareHeader(t)
	for _, tc := range []struct {
		target, define string
		pins           []int
	}{
		{"ESP32S3", "HW_RESERVED_BOOT", []int{0}},
		{"ESP32S3", "HW_RESERVED_USB_DN", []int{19}},
		{"ESP32S3", "HW_RESERVED_USB_DP", []int{20}},
		{"ESP32S3", "HW_RESERVED_LED", []int{48}},
		{"ESP32C6", "HW_RESERVED_BOOT", []int{9}},
		{"ESP32C6", "HW_RESERVED_USB_DN", []int{12}},
		{"ESP32C6", "HW_RESERVED_USB_DP", []int{13}},
		{"ESP32C6", "HW_RESERVED_LED", []int{8}},
	} {
		for _, pin := range tc.pins {
			if !fw.reservedInFirmware(tc.target, tc.define, pin) {
				t.Errorf("固件 hw_tables.h 里 %s=%d 已不存在或已改值（后端副本过期）", tc.define, pin)
			}
			if _, ok := reservedPinName(tc.target, pin); !ok {
				t.Errorf("固件把 %s=%d 标为保留，而后端 reservedPinName(%s, %d) 不认识 ⇒ 副本过期",
					tc.define, pin, tc.target, pin)
			}
		}
	}
	_ = models.Node{}
}

// ===== 跨仓一致性 helper（后端"保留脚副本" vs 固件真源）=====
//
// ⚠ 为什么必须真去读固件文件、而不是把数字再抄一遍进测试：
//
//	本用例要防的正是"后端的副本与固件脱节"。若测试里也抄一份数字，
//	两份副本会**一起过期**，用例永远是绿的 —— 那是假绿的标准形态。
//	（与 pkg/protoframe 的 firmwareTLSRecordLen、
//	  internal/commandexec 的 readFirmwareFile 同族。）
//
// ⚠⚠ 第一版这里有个**真 bug**，值得记：hw_tables.h 用 #ifdef/#elif 分成 S3 段与 C6 段，
//
//	而两段的宏**同名不同值**（HW_RESERVED_BOOT 在 S3=0、C6=9）。
//	第一版直接在全文件做 FindStringSubmatch ⇒ 永远命中 S3 那一段的 0，
//	C6 四条全部报"副本过期"。**必须按 target 分段解析**，
//	否则这个跨仓门禁要么假红（像这次）、要么在别的写法下假绿。
type firmwareTables struct {
	// defines[target][name] = value
	defines map[string]map[string]int
}

// value 取某 target 下某宏的值。target 形如 "ESP32S3"/"ESP32C6"。
func (f firmwareTables) value(target, name string) (int, bool) {
	m, ok := f.defines[strings.ToUpper(target)]
	if !ok {
		return 0, false
	}
	v, ok := m[name]
	return v, ok
}

// reservedInFirmware 判断固件里该 target 的该保留宏是否等于 pin。
func (f firmwareTables) reservedInFirmware(target, name string, pin int) bool {
	v, ok := f.value(target, name)
	return ok && v == pin
}

// ⚠ 必须把 ifdef 排在 if 前面：正则交替最左优先，写成 (?:if|elif) 时
//
//	#ifdef 里的 "if" 会先匹配、接着 \s+ 撞到 "def" 而失败 ⇒ #ifdef 永远匹配不到。
//	（这个坑在固件侧门禁里踩过一次，见 §197。）
var fwTargetRe = regexp.MustCompile(`#\s*(?:ifdef|if|elif)\s+(?:defined\s*\(\s*)?CONFIG_IDF_TARGET_(\w+)`)

// ⚠ 捕获组必须含 HW_RESERVED_ 前缀：第一版只捕获了 BOOT，
//
//	而查表用的是全名 HW_RESERVED_BOOT ⇒ 8 条全部报「副本过期」（假红）。
var fwReservedRe = regexp.MustCompile(`#define[ \t]+(HW_RESERVED_\w+)[ \t]+([0-9]+)`)

// readFirmwareHeader 按 target 分段解析固件的 hw_tables.h —— 保留脚的**真源**。
//
// ⚠ 工作目录：go test 的 cwd 是包目录（backend/internal/nodemgr），仓库根在其上三级。
func readFirmwareHeader(t *testing.T) firmwareTables {
	t.Helper()
	candidates := []string{
		filepath.Join("..", "..", "..", "esp32-collector", "components", "hw_profile", "include", "hw_tables.h"),
		filepath.Join("esp32-collector", "components", "hw_profile", "include", "hw_tables.h"),
	}
	var raw string
	for _, c := range candidates {
		b, err := os.ReadFile(c)
		if err != nil {
			continue
		}
		// ⚠ 空文件会让解析结果为空 ⇒ 用例会以"固件里没有这个定义"报错，
		//   看起来像固件有问题。必须显式区分"读不到"与"内容不对"。
		if len(b) == 0 {
			t.Fatalf("固件头 %s 为空 —— 门禁会因空文件而假绿", c)
		}
		raw = string(b)
		break
	}
	if raw == "" {
		t.Skipf("找不到固件 hw_tables.h（尝试过 %v）—— 跨仓判据跳过，⚠ 这不等于通过", candidates)
	}

	out := firmwareTables{defines: map[string]map[string]int{}}
	target := ""
	for _, line := range strings.Split(raw, "\n") {
		if m := fwTargetRe.FindStringSubmatch(line); m != nil {
			target = strings.ToUpper(m[1])
			if out.defines[target] == nil {
				out.defines[target] = map[string]int{}
			}
			continue
		}
		if false && strings.HasPrefix(strings.TrimSpace(line), "#else") ||
			strings.HasPrefix(strings.TrimSpace(line), "#endif") {
			target = ""
			continue
		}
		if target == "" {
			continue
		}
		if m := fwReservedRe.FindStringSubmatch(line); m != nil {
			if v, err := strconv.Atoi(m[2]); err == nil {
				out.defines[target][m[1]] = v
			}
		}
	}

	// ⚠ 解析器的自检断言：否则"一个都没解析到"会被当成"固件没有保留脚"（假绿）。
	if len(out.defines) < 2 {
		t.Fatalf("只解析出 %d 个 target（应 >= 2：ESP32S3/ESP32C6）⇒ 解析器多半写错了",
			len(out.defines))
	}
	for tgt, m := range out.defines {
		if len(m) < 4 {
			t.Fatalf("target %s 只解析出 %d 个保留脚（应 >= 4：BOOT/USB_DN/USB_DP/LED）⇒ 解析器多半写错了",
				tgt, len(m))
		}
	}
	return out
}
