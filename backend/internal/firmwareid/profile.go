package firmwareid

import "fmt"

// BuildProfile 由芯片/容量/PSRAM 反推构建档位名（如 s3p-n16 / c6-n8）。
//
// 它是包格式 INV-3 的交叉核对点：清单只声明 profile 一个字符串（编码了
// chip + flash + psram），服务端用**镜像自证**的 chip/flash 加上清单里的
// psram 还原出真值再比对 —— 能抓住「s3p 的清单配了 s3 的 bin」，抓不住 psram
// 本身（镜像头里没有这一位，这是构建期元数据的固有约束）。
//
// pack_firmware.py 的 --profile 校验与 ehomectl firmware verify 共用同一条规则。
// 缺容量或未知芯片时返回 ""（不猜，方案 §2.4 R5）。
func BuildProfile(chip string, flashMB uint16, psram bool) string {
	var short string
	switch CanonicalChip(chip) {
	case ChipS3:
		short = "s3"
	case ChipC6:
		short = "c6"
	default:
		return ""
	}
	if flashMB == 0 {
		return ""
	}
	if psram {
		short += "p"
	}
	return fmt.Sprintf("%s-n%d", short, flashMB)
}
