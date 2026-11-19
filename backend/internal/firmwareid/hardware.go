package firmwareid

import "strings"

// 硬件档案取值。flashMB=0 / psram="" 表示未识别（方案 §5.3）。
const (
	PSRAMYes = "yes"
	PSRAMNo  = "no"
)

// 槽容量常量表（与分区 CSV 同源，方案 §6.3）：8MB→0x380000、16MB→0x680000。
var slotCapacityByFlashMB = map[uint16]uint64{
	8:  0x380000,
	16: 0x680000,
}

// SlotCapacity 返回该容量档位的应用分区上限；未知容量返回 (0, false)。
//
// 上传（FW_TOO_LARGE）与闸门（OTA_GATE_IMAGE_TOO_LARGE）共用这一份常量，
// 避免两处各写一张表后漂移。
func SlotCapacity(flashMB uint16) (uint64, bool) {
	cap, ok := slotCapacityByFlashMB[flashMB]
	return cap, ok
}

// ParseNodeHardware 从设备上报的 platform / model 串推导硬件档案。
//
// 依据（esp32-collector/main/app_state.c:generate_model_name）：
//   - model 默认是 CONFIG_IDF_TARGET（"esp32s3"/"esp32c6"）；
//   - PSRAM 版是 "esp32s3p"（构建期 CONFIG_COLLECTOR_PSRAM=y 且运行期真的探到 PSRAM）；
//   - 容量**不在** model 里（DEF-5：型号是编译目标而非硬件档案）。
//
// 所以：芯片与 PSRAM 可从串里解析，容量只有串里显式带 -n8/-n16（未来 profile 串）时才有；
// 解析不出来一律留空/0，交给「硬件未识别」单条闸门，绝不猜（方案 §2.4 R5 / RK-7）。
func ParseNodeHardware(platform, model string) (chip string, flashMB uint16, psram string) {
	// model 比 platform 信息多（带 p 位），优先它；platform 兜底芯片。
	for _, raw := range []string{model, platform} {
		if c, f, p, ok := parseHardwareToken(raw); ok {
			return c, f, p
		}
	}
	return "", 0, ""
}

func parseHardwareToken(raw string) (chip string, flashMB uint16, psram string, ok bool) {
	token := strings.ToLower(strings.TrimSpace(raw))
	if token == "" {
		return "", 0, "", false
	}
	token = strings.TrimPrefix(token, "esp32")
	// 档位串里 "-" 与 "_" 都出现过（esp32c6-n8 / s3p_n16），统一拆分。
	parts := strings.FieldsFunc(token, func(r rune) bool { return r == '-' || r == '_' })
	if len(parts) == 0 {
		return "", 0, "", false
	}
	short := parts[0]
	withPSRAM := strings.HasSuffix(short, "p")
	short = strings.TrimSuffix(short, "p")
	switch short {
	case "s3":
		chip = ChipS3
	case "c6":
		chip = ChipC6
	default:
		return "", 0, "", false
	}
	// 串里没写 p 位就是「无 PSRAM」——固件只在真正探到 PSRAM 时才拼 "p"。
	psram = PSRAMNo
	if withPSRAM {
		psram = PSRAMYes
	}
	for _, part := range parts[1:] {
		switch part {
		case "n8":
			flashMB = 8
		case "n16":
			flashMB = 16
		}
	}
	return chip, flashMB, psram, true
}
