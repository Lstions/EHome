package telemetry

import (
	"sort"
	"testing"
	"time"

	"ehome/backend/internal/models"
)

// TestRealDataPayloadReport 产出 §4.5 对账所需的**逐设备/逐 slot** 实测数字。
//
// 它不新增断言（断言在 TestRealDataEncodeDecodeRoundTrip），只把体积/payload
// 分布打成日志，供交付说明与方案 §4.5 逐项对账。之所以做成测试而不是一次性
// 脚本：这些数字是"方案是否成立"的核心证据，必须在每次改动后仍可复现。
func TestRealDataPayloadReport(t *testing.T) {
	path := locateRealdata(t)
	if path == "" {
		t.Skipf("真实数据夹具缺席（%s）", realdataRelPath)
	}

	totalFrames := 0
	var totalBytes int64
	for _, deviceID := range []uint{realDeviceBMS, realDeviceInverter} {
		frames := loadRealFrames(t, path, deviceID, realdataFrom, realdataTo, 0)
		dict := NewMemorySlots()
		enc := NewEncoder(EncoderConfig{}, dict)

		// 逐 slot 统计
		bySlot := map[int16]*struct {
			frames  int
			bytes   int64
			changed int64
		}{}
		encoded := make([]models.TelemetryFrame, 0, len(frames))
		for _, fr := range frames {
			ef, err := enc.EncodeFrame(fr.DeviceID, fr.TS, fr.CommandID, fr.Readings)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			st := bySlot[ef.SlotID]
			if st == nil {
				st = &struct {
					frames  int
					bytes   int64
					changed int64
				}{}
				bySlot[ef.SlotID] = st
			}
			st.frames++
			st.bytes += int64(len(ef.Payload))
			encoded = append(encoded, models.TelemetryFrame{
				DeviceID: ef.DeviceID, TS: ef.TS, Seq: ef.Seq,
				FrameKind: ef.FrameKind, SlotID: ef.SlotID, Payload: ef.Payload,
			})
		}

		// 与 unified_data 现状对比：该设备该天的行数 = 解析出的值总数
		rows := 0
		for _, fr := range frames {
			rows += len(fr.Readings)
		}
		var bytes int64
		for _, ef := range encoded {
			bytes += int64(len(ef.Payload))
		}
		totalFrames += len(frames)
		totalBytes += bytes

		t.Logf("device=%d 帧=%d 行(现状)=%d payload总=%dB 平均=%.2fB 关键帧=%d 帧类型=%d",
			deviceID, len(frames), rows, bytes, float64(bytes)/float64(len(frames)),
			enc.Stats().KeyFrames, len(bySlot))
		// 逐 slot 明细（device15 的 7 种帧类型各有不同的压缩率）
		slots := make([]int, 0, len(bySlot))
		for id := range bySlot {
			slots = append(slots, int(id))
		}
		sort.Ints(slots)
		for _, id := range slots {
			st := bySlot[int16(id)]
			slot, ok := dict.ByID(int16(id))
			fields := -1
			if ok {
				fields = slot.NumFields()
			}
			t.Logf("  slot=%d 字段=%d 帧=%d 平均payload=%.2fB 总=%dB",
				id, fields, st.frames, float64(st.bytes)/float64(st.frames), st.bytes)
		}
		// 现状行成本（方案 §1.2 实测 178.7 B/行，含 5 条索引）
		const currentRowBytes = 178.7
		currentBytes := float64(rows) * currentRowBytes
		t.Logf("  → 现状估算 %.2f MB/天 vs 帧级 %.3f MB/天，压缩比 %.1fx",
			currentBytes/1e6, float64(bytes)/1e6, currentBytes/float64(bytes))
	}
	t.Logf("合计：帧=%d payload=%dB 平均=%.2fB（方案 §4.5：142,467 帧 / 12.1B / 1.63 MB/天）",
		totalFrames, totalBytes, float64(totalBytes)/float64(totalFrames))

	// ── 敏感性：关键帧间隔对体积的影响 ──
	//
	// device15 的两种帧类型（9 字段的温度/风扇帧 80.9s 才来一次、4 字段的
	// 电量帧 140.9s 才来一次）在 60s 关键帧策略下会被**强制补关键帧**，
	// 这是 4.82B/帧 略高于方案 4.7B 的唯一原因。这里量化该影响，
	// 以便判断"要不要为稀疏帧类型放宽心跳"。
	for _, interval := range []time.Duration{60 * time.Second, 24 * time.Hour} {
		var payload int64
		var n int
		var keys int
		for _, deviceID := range []uint{realDeviceBMS, realDeviceInverter} {
			frames := loadRealFrames(t, path, deviceID, realdataFrom, realdataTo, 0)
			dict := NewMemorySlots()
			enc := NewEncoder(EncoderConfig{KeyFrameInterval: interval}, dict)
			for _, fr := range frames {
				ef, err := enc.EncodeFrame(fr.DeviceID, fr.TS, fr.CommandID, fr.Readings)
				if err != nil {
					t.Fatalf("encode: %v", err)
				}
				payload += int64(len(ef.Payload))
				n++
				if ef.FrameKind == 1 {
					keys++
				}
			}
		}
		t.Logf("关键帧间隔=%s：帧=%d 关键帧=%d 平均payload=%.2fB 总=%.3fMB/天",
			interval, n, keys, float64(payload)/float64(n), float64(payload)/1e6)
	}
}
