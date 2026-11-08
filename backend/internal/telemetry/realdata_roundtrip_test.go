package telemetry

import (
	"fmt"
	"math"
	"sort"
	"testing"
	"time"

	"ehome/backend/internal/models"
)

// 真实数据「解析 → 编码 → 解码」往返对拍（方案 §9.1 的硬验收）。
//
// # 断言（缺一不可）
//
//  1. **误差**：每个变化值 解码值 与 原始值 之差 <= 该字段步长/2（量化上界）；
//  2. **缺失**：解码出的读数个数 == 原始读数个数（0 丢失），且字段集合一致；
//  3. **不漂移**：按时间累积还原（增量链跨 86,342 帧）后，**末帧全量快照**
//     与原始末帧逐字段一致（误差同上）。这一条专门抓"链断了但没报错、
//     后续值整体错位"—— 单帧误差检查抓不到它，因为错位的值每个单看都合法。
//
// # 为什么用生产 CSV 而不是构造数据
//
// 构造数据能轻易让任意方案通过。方案 §4.5 的全部数字（16.7 B / 4.7 B /
// 1.63 MB）都是从这一天真实数据测出来的，用同一天同一批帧才有对账意义。
func TestRealDataEncodeDecodeRoundTrip(t *testing.T) {
	path := locateRealdata(t)
	if path == "" {
		t.Skipf("真实数据夹具缺席（%s）；跳过往返测试。设 EHOME_TELEMETRY_REALDATA 指定路径。", realdataRelPath)
	}

	cases := []struct {
		name     string
		deviceID uint
	}{
		{"device14-BMS-29字段固定帧", realDeviceBMS},
		{"device15-逆变器-7种帧类型", realDeviceInverter},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frames := loadRealFrames(t, path, tc.deviceID, realdataFrom, realdataTo, 0)
			if len(frames) < 1000 {
				t.Fatalf("device %d: 只载入 %d 帧，不足 24h 验收要求（生产实测 86,342 / 56,125）",
					tc.deviceID, len(frames))
			}
			window := frames[len(frames)-1].TS.Sub(frames[0].TS)
			if window < 23*time.Hour {
				t.Fatalf("device %d: 窗口只有 %s，验收要求 >= 24h 的数据", tc.deviceID, window)
			}

			dict := NewMemorySlots()
			enc := NewEncoder(EncoderConfig{}, dict)

			encoded := make([]models.TelemetryFrame, 0, len(frames))
			for _, fr := range frames {
				ef, err := enc.EncodeFrame(fr.DeviceID, fr.TS, fr.CommandID, fr.Readings)
				if err != nil {
					t.Fatalf("encode frame at %s: %v", fr.TS, err)
				}
				encoded = append(encoded, models.TelemetryFrame{
					DeviceID:  ef.DeviceID,
					TS:        ef.TS,
					Seq:       ef.Seq,
					FrameKind: ef.FrameKind,
					SlotID:    ef.SlotID,
					Payload:   ef.Payload,
				})
			}

			// ── 解码：走**生产同一条**状态机（DecodeFrames）──
			points, stats, err := DecodeFrames(encoded, dict, DecodeOptions{})
			if err != nil {
				t.Fatalf("decode: %v", err)
			}

			assertNoCorruptChains(t, stats)
			assertNoMissingValues(t, len(frames), stats)

			// 逐值对拍：把解码读数按 (ts, name) 索引，与原值比对。
			type key struct {
				ts   int64
				name string
			}
			decodedVals := make(map[key]float64, len(points))
			for _, p := range points {
				decodedVals[key{p.TS.UnixNano(), p.Name}] = p.Value
			}

			totalValues := 0
			missing := 0
			maxErrRatio := 0.0
			worst := ""
			for _, fr := range frames {
				for _, r := range fr.Readings {
					totalValues++
					got, ok := decodedVals[key{fr.TS.UnixNano(), r.Name}]
					if !ok {
						missing++
						if missing <= 5 {
							t.Errorf("缺失: device=%d ts=%s field=%s 未出现在解码结果里",
								fr.DeviceID, fr.TS.Format(time.RFC3339Nano), r.Name)
						}
						continue
					}
					sp, known := SpecFor(r.Name)
					if !known {
						t.Fatalf("字段 %q 未登记量化规格（真实数据不应出现该情况）", r.Name)
					}
					diff := math.Abs(got - r.Value)
					if diff > sp.MaxError()+1e-9 {
						t.Errorf("超差: device=%d ts=%s field=%s 原值=%v 解码=%v 误差=%v > 步长/2=%v",
							fr.DeviceID, fr.TS.Format(time.RFC3339Nano), r.Name, r.Value, got, diff, sp.MaxError())
					}
					if sp.MaxError() > 0 {
						if ratio := diff / sp.MaxError(); ratio > maxErrRatio {
							maxErrRatio = ratio
							worst = fmt.Sprintf("%s @ %s: 原值=%v 解码=%v 误差=%.6g (步长/2=%.6g)",
								r.Name, fr.TS.Format(time.RFC3339Nano), r.Value, got, diff, sp.MaxError())
						}
					}
				}
			}
			if missing != 0 {
				t.Fatalf("缺失值 %d / %d（要求 0）", missing, totalValues)
			}

			// ── 不漂移断言：末帧全量快照 vs 原始末帧 ──
			last := frames[len(frames)-1]
			snapshot := snapshotOfLastFrame(points)
			for _, r := range last.Readings {
				sp, _ := SpecFor(r.Name)
				got, ok := snapshot[r.Name]
				if !ok {
					t.Errorf("末帧快照缺字段 %q（增量链在最后一段断了？）", r.Name)
					continue
				}
				if diff := math.Abs(got - r.Value); diff > sp.MaxError()+1e-9 {
					t.Errorf("漂移: 末帧字段 %q 原值=%v 解码=%v 误差=%v > 步长/2=%v",
						r.Name, r.Value, got, diff, sp.MaxError())
				}
			}

			// ── 体积统计（与方案 §4.5 对账）──
			var payloadBytes int64
			keyFrames := 0
			for _, ef := range encoded {
				payloadBytes += int64(len(ef.Payload))
				if ef.FrameKind == models.TelemetryFrameKindKey {
					keyFrames++
				}
			}
			avg := float64(payloadBytes) / float64(len(encoded))

			t.Logf("device=%d 帧=%d 值=%d 窗口=%s | payload总=%dB 平均=%.2fB 关键帧=%d | 最大误差比=%.3f (%s)",
				tc.deviceID, len(frames), totalValues, window.Truncate(time.Second),
				payloadBytes, avg, keyFrames, maxErrRatio, worst)
		})
	}
}

// assertNoCorruptChains 断言解码过程没有坏帧/断链（§8 风险表的"增量链断裂"）。
func assertNoCorruptChains(t *testing.T, stats DecodeStats) {
	t.Helper()
	if stats.FramesSkippedCorrupt != 0 {
		t.Errorf("坏 payload 帧数 = %d（要求 0）", stats.FramesSkippedCorrupt)
	}
	if stats.FramesSkippedUnknownSlot != 0 {
		t.Errorf("未知 slot 帧数 = %d（要求 0，说明字典与编码器不一致）", stats.FramesSkippedUnknownSlot)
	}
	if stats.FramesSkippedBrokenChain != 0 {
		t.Errorf("断链跳过帧数 = %d（要求 0）", stats.FramesSkippedBrokenChain)
	}
	if stats.MissingBaseline != 0 {
		t.Errorf("无基线字段数 = %d（要求 0，说明增量链起点不对）", stats.MissingBaseline)
	}
}

// assertNoMissingValues 断言每个输入帧都被解码（没有静默丢弃）。
func assertNoMissingValues(t *testing.T, inputFrames int, stats DecodeStats) {
	t.Helper()
	if stats.FramesDecoded != int64(inputFrames) {
		t.Errorf("解码帧数 = %d，输入 = %d（要求相等）", stats.FramesDecoded, inputFrames)
	}
	if stats.FramesSeen != int64(inputFrames) {
		t.Errorf("输入帧数统计 = %d，实际 %d", stats.FramesSeen, inputFrames)
	}
}

// snapshotOfLastFrame 从升序读数里取每个字段的最后值。
func snapshotOfLastFrame(points []Point) map[string]float64 {
	out := make(map[string]float64, 32)
	for _, p := range points {
		out[p.Name] = p.Value
	}
	return out
}

// TestRealDataSlotDictionary 钉住 §4.4：device15 有 **7 种**帧类型，
// 每种拿到**独立** slot；device14 只有 1 种。硬编码槽位会在这里红。
func TestRealDataSlotDictionary(t *testing.T) {
	path := locateRealdata(t)
	if path == "" {
		t.Skipf("真实数据夹具缺席（%s）", realdataRelPath)
	}

	dict := NewMemorySlots()
	enc := NewEncoder(EncoderConfig{}, dict)

	fieldSetCount := map[uint]int{}
	for _, deviceID := range []uint{realDeviceBMS, realDeviceInverter} {
		frames := loadRealFrames(t, path, deviceID, realdataFrom, realdataTo, 0)
		sigs := make(map[string]int)
		for _, fr := range frames {
			names := make([]string, len(fr.Readings))
			for i, r := range fr.Readings {
				names[i] = r.Name
			}
			sig := SignatureOf(fr.CommandID, SpecVersion, names)
			sigs[sig]++
			if _, err := enc.EncodeFrame(fr.DeviceID, fr.TS, fr.CommandID, fr.Readings); err != nil {
				t.Fatalf("encode: %v", err)
			}
		}
		fieldSetCount[deviceID] = len(sigs)
		// 每种帧类型的字段数分布（诊断输出，便于与方案 §4.4 表格对账）
		sizeDist := map[int]int{}
		for _, fr := range frames {
			sizeDist[len(fr.Readings)]++
		}
		sizes := make([]int, 0, len(sizeDist))
		for n := range sizeDist {
			sizes = append(sizes, n)
		}
		sort.Ints(sizes)
		t.Logf("device=%d 帧类型数=%d 字段数分布=%v", deviceID, len(sigs), sizes)
	}

	if got := fieldSetCount[realDeviceBMS]; got != 1 {
		t.Errorf("device14 帧类型数 = %d，方案 §4.4 实测为 1（恒 29 字段）", got)
	}
	if got := fieldSetCount[realDeviceInverter]; got != 7 {
		t.Errorf("device15 帧类型数 = %d，方案 §4.4 实测为 7（3/14/2/5/5/9/4 字段）", got)
	}
	// 每种帧类型必须拿到不同 slot（否则就是"写串"）
	all := dict.All()
	if len(all) != 1+7 {
		t.Errorf("slot 字典共 %d 个 slot，期望 1(device14) + 7(device15) = 8 —— 硬编码槽位会把不同指令的值写串", len(all))
	}
}
