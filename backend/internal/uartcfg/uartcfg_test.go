package uartcfg

import (
	"encoding/hex"
	"strings"
	"testing"
)

// TestPadShortUART_AlignsWithFirmwareMinimum 是本次真机缺陷（2026-10-07）的回归护栏。
//
// 缺陷：2 字节的 UART bus_config（只配了引脚、还没配波特率）被**原样下发**，
// 而固件 bus_manager.c:443 要求 `bus_config_len >= 6`（它必须读 byte2..5 的波特率）
// ⇒ 设备 `preinstall rejected by resource plan: ESP_ERR_INVALID_SIZE`、`ConfigResult success=0`，
//   而后端返回 **201「通道创建成功」** ⇒ 操作员以为成功。
//
// 本用例断言：**短值必须被补齐到固件能接受的长度**，且**引脚逐字节保留**。
// 它凭什么会失败：把 PadShortUART 改回"原样返回 data, false"，本用例立刻红。
func TestPadShortUART_AlignsWithFirmwareMinimum(t *testing.T) {
	// 引脚 0x2b=43 / 0x2c=44（用户给的 S3 接线），2 字节 —— 真机上就是这么建出来的。
	short, err := hex.DecodeString("2b2c")
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}

	padded, changed := PadShortUART(short)
	if !changed {
		t.Fatalf("2 字节的 UART bus_config 必须被补齐（固件要求 >= %d），实得 changed=false", MinLen)
	}
	if len(padded) < MinLen {
		t.Fatalf("补齐后长度 %d < 固件下限 %d ⇒ 设备仍会 ESP_ERR_INVALID_SIZE", len(padded), MinLen)
	}
	// 引脚必须原样保留 —— 补齐不得改动用户给的接线。
	if padded[0] != 0x2b || padded[1] != 0x2c {
		t.Fatalf("补齐改动了引脚：got tx=0x%02x rx=0x%02x, want tx=0x2b rx=0x2c", padded[0], padded[1])
	}
	// 波特率必须是默认值（big-endian）。
	got := uint32(padded[2])<<24 | uint32(padded[3])<<16 | uint32(padded[4])<<8 | uint32(padded[5])
	if got != DefaultBaudrate {
		t.Fatalf("补齐后的波特率 = %d, want %d", got, DefaultBaudrate)
	}
}

// TestPadShortUART_LeavesLongValuesByteIdentical 是**反向**护栏：
// 已满足固件要求的 bus_config 必须**逐字节不动** —— 否则会篡改用户已配的波特率/DMA 位。
// 它凭什么会失败：让 PadShortUART 对长值也走补齐路径，本用例立刻红。
func TestPadShortUART_LeavesLongValuesByteIdentical(t *testing.T) {
	// 7 字节标准布局，波特率 38400（0x00009600）—— 刻意不等于默认 9600，
	// 这样"被补齐覆盖"一定会被发现。
	orig, _ := hex.DecodeString("2b2c0000960001")
	before := append([]byte(nil), orig...)

	padded, changed := PadShortUART(orig)
	if changed || padded != nil {
		t.Fatalf("已 >= %d 字节的 bus_config 不得被改动，实得 changed=%v padded=%x", MinLen, changed, padded)
	}
	if hex.EncodeToString(orig) != hex.EncodeToString(before) {
		t.Fatalf("原字节被就地改写：before=%x after=%x", before, orig)
	}
}

// TestPadShortUART_TooShortToGuess 记录一条**边界契约**：
// 连两个引脚都不够（< 2 字节）时不猜 —— 返回未改动，由调用方按"非法"处理。
// 若这里改成"补全"，会把一个非法输入伪装成合法通道。
func TestPadShortUART_TooShortToGuess(t *testing.T) {
	one, _ := hex.DecodeString("2b")
	padded, changed := PadShortUART(one)
	if changed || padded != nil {
		t.Fatalf("1 字节不足以确定引脚，不得补齐（否则会把非法输入伪装成合法），实得 changed=%v", changed)
	}
}

// TestBuild_MatchesFirmwareLayout 固定 Build 的字节布局（与固件读法逐字节对齐）。
func TestBuild_MatchesFirmwareLayout(t *testing.T) {
	got := Build(43, 44, 9600, true)
	want := "2b2c0000258001"
	if !strings.EqualFold(got, want) {
		t.Fatalf("Build(43,44,9600,dma=true) = %q, want %q", got, want)
	}
	// DMA=false 时 byte6 必须为 0（不擅自替用户打开 DMA）。
	if off := Build(43, 44, 9600, false); !strings.EqualFold(off, "2b2c0000258000") {
		t.Fatalf("Build(dma=false) = %q, want 2b2c0000258000", off)
	}
}

// TestPadShortUART_ResultIsAcceptedByBuilders 是**跨函数自证**：
// PadShortUART 的产物必须与 Build 的产物**同布局**（否则两条补齐路径会造出不同的字节）。
func TestPadShortUART_ResultIsAcceptedByBuilders(t *testing.T) {
	short, _ := hex.DecodeString("2b2c")
	padded, _ := PadShortUART(short)
	built := Build(43, 44, DefaultBaudrate, false)
	if hex.EncodeToString(padded) != strings.ToLower(built) {
		t.Fatalf("两条补齐路径产物不一致：PadShortUART=%x Build=%s", padded, built)
	}
}