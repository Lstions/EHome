package backfill

// encode.go — 把「一帧的字段行」编码成帧表一行，含**超量程原文保留**（契约 §2.2/§4.5）。
//
// ============ 超量程为什么必须保留原文 ============
//
// 全库扫描结果（契约 §2.2）：非整数码点 0 行、越界 **5 行**，全部是
//
//	battery_voltage = 1,000,000,000,000  (1e12)
//
// 集中在 2026-10-19 08:53~08:55（逆变器解析器的一次已知缺陷，历史数据）。
//
// 该字段 spec 量程 0~204.7 V ⇒ 这 5 个值会被 clamp 到 204.7，**有损**。
//
// 三个选项：
//
//	a) 直接丢弃这 5 行       —— 违背「所有数据长期保留」的用户要求；
//	b) 篡改成邻近值           —— 伪造历史数据，最坏的选择；
//	c) 原文写进帧表的 raw 列  —— 信息不丢、不篡改、不阻塞迁移。✅
//
// 本文件实现 (c)：只在**真的发生 clamp** 的帧上写 raw，常规帧 raw 留 NULL
// （常规帧写 raw 会让体积从 463 MB 涨到几十 GB —— raw 是原始总线帧，平均 78 B）。
//
// raw 的内容是 JSON（不是原始总线字节，老数据本来也没有总线字节）：
//
//	{"clamped":[{"field":"battery_voltage","value":1e12,"spec_max":204.7}]}
//
// 为什么用 JSON 而不是自定义二进制：这是**人类排查**用的数据（5 行），
// 可读性 >> 体积。

import (
	"encoding/json"
	"fmt"
	"sort"

	"ehome/backend/internal/telemetry"
)

// clampedValue 是一处超量程的原始记录。
type clampedValue struct {
	Field    string  `json:"field"`
	Value    float64 `json:"value"`
	SpecMin  float64 `json:"spec_min"`
	SpecMax  float64 `json:"spec_max"`
	SpecStep float64 `json:"spec_step"`
}

// rawPayload 是写进 telemetry_frame.raw 的 JSON 结构。
type rawPayload struct {
	// Clamped 超量程字段的原文（编码时被 clamp，值不可从帧还原）。
	Clamped []clampedValue `json:"clamped,omitempty"`
	// Note 人类可读的说明（一行），避免运维看到 raw 有内容却不知道为什么。
	Note string `json:"note,omitempty"`
}

// sortReadings 按字段名排序（**确定性**是回填可重入的前提）。
//
// 为什么必须排序：
//
//	· slot 的 SignatureOf 内部会排序，所以 slot 不受影响；
//	· 但 EncodeFrame 的 readings 顺序会影响 `clamped` 计数的遍历顺序，
//	  进而影响 raw JSON 的字段顺序 ⇒ 同样的输入产生不同字节 ⇒
//	  「重跑一次结果是否相同」这个验收就没法断言。
func sortReadings(readings []telemetry.Reading) {
	sort.Slice(readings, func(i, j int) bool { return readings[i].Name < readings[j].Name })
}

// detectClamped 返回本帧中会触发 clamp 的字段（编码前**预判**）。
//
// 为什么不从编码结果里读：telemetry.Encoder 只把 clamped 计入聚合统计
// （EncodeStats.ClampedValues），不暴露「哪一帧的哪个字段」。回填需要精确到
// 字段才能写 raw，所以这里独立预判一次。
//
// 判据与 telemetry.FieldSpec.Quantize 完全一致（同一份 spec，同一套边界），
// 因此不会出现「预判说没超、实际超了」的漂移 —— 但要注意 Quantize 的边界是
// **闭区间** [Min, Max]，所以这里也用 <= / >=。
func detectClamped(readings []telemetry.Reading) []clampedValue {
	var out []clampedValue
	for _, rd := range readings {
		sp, _ := telemetry.SpecFor(rd.Name)
		if !sp.Valid() {
			continue
		}
		if _, clamped := sp.Quantize(rd.Value); !clamped {
			continue
		}
		out = append(out, clampedValue{
			Field:    rd.Name,
			Value:    rd.Value,
			SpecMin:  sp.Min,
			SpecMax:  sp.Max(),
			SpecStep: sp.Step,
		})
	}
	return out
}

// buildRawPayload 把超量程记录序列化成 raw 列的内容（无超量程返回 nil）。
//
// 返回 nil 而不是空 JSON：`raw` 列在常规帧上必须留 NULL（见文件头）。
func buildRawPayload(clamped []clampedValue) ([]byte, error) {
	if len(clamped) == 0 {
		return nil, nil
	}
	rp := rawPayload{
		Clamped: clamped,
		Note:    fmt.Sprintf("%d 个字段超出量化量程，已被 clamp；原文见 clamped 数组（契约 §2.2 的 battery_voltage 1e12 类数据）", len(clamped)),
	}
	b, err := json.Marshal(rp)
	if err != nil {
		return nil, fmt.Errorf("marshal raw payload: %w", err)
	}
	return b, nil
}
