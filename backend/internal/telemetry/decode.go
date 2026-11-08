package telemetry

import (
	"fmt"
	"sort"
	"time"

	"ehome/backend/internal/models"
)

// Point 是解码产物里的一个读数。
//
// 语义：在时间 TS 上，字段 Name 的重建值 = Value。与 unified_data 的一行
// 信息量相当（device + ts + name + value），只是不再逐行存。
type Point struct {
	DeviceID uint
	TS       time.Time
	Seq      int16
	Name     string
	Value    float64
}

// DecodeStats 是一次解码的可观测统计。
//
// 为什么要这些计数：增量链是**有状态**的，解错时不会报错，只会产出一串
// 看起来合理但已经错位的值（§8 风险表"增量链断裂"）。因此把"跳过了多少帧、
// 失效了多少条链"做成显式计数，让影子校验能发现"解码成功但数据不对"之前
// 就先看到链健康度异常。
type DecodeStats struct {
	// FramesSeen 输入帧数。
	FramesSeen int64
	// FramesDecoded 成功解码的帧数。
	FramesDecoded int64
	// FramesSkippedUnknownSlot 未知 slot（字典里没有该 slot_id）。
	FramesSkippedUnknownSlot int64
	// FramesSkippedCorrupt payload 解不开（截断/保留位/尾部多余字节）。
	FramesSkippedCorrupt int64
	// FramesSkippedBrokenChain 链已失效且本帧不是关键帧 ⇒ 跳过。
	//
	// ⚠ 这是**数据形态**（关键帧间隔 > 窗口起点，正常现象），不是我们的行为。
	FramesSkippedBrokenChain int64
	// FramesTruncated **我们主动丢弃的最旧帧数**（窗口内帧数超过解码上限）。
	//
	// ============ 为什么必须与 BrokenChain 分开（2026-11-08）============
	//
	// 原实现把它加进 FramesSkippedBrokenChain。那是个**静默缺陷**：
	//
	//	BrokenChain = 数据形态（正常，关键帧间隔内跳过是设计行为）
	//	Truncated   = 我们的行为（主动丢数据，异常）
	//
	// 混用一个字段时，「我们丢了数据」会被读成「正常断链」⇒ 没人会去查。
	// 而调用方（decodeFrameWindowNow）只在「0 点」时才看统计 ⇒ 截断场景 points 非空
	// ⇒ **这个信号永远不会被读到**。
	//
	// 后果不是返回空（用户会问），而是**静默返回残缺数据**（用户看到一条到某时刻
	// 为止的图，不会问）。见 decodeFrameWindowNow 对此的新处置。
	FramesTruncated int64
	// ChainInvalidations 链失效次数（每次坏帧一次）。
	ChainInvalidations int64
	// MissingBaseline 增量帧里字段既未 changed 又没有基线（链起点不对）。
	MissingBaseline int64
	// KeyFrames 关键帧数。
	KeyFrames int64
	// Points 产出读数个数。
	Points int64
}

// DecodeOptions 是 DecodeFrames 的参数。
type DecodeOptions struct {
	// Fields 是要产出的字段（nil / 空 = 该 slot 的全部字段）。
	Fields []string
	// MaxFrames 是输入帧数上限（0 = 不限制）。超出时**保留最新的** MaxFrames 帧：
	// 解码的目标通常是"最近状态"，丢最老的帧比丢最新的更安全。
	MaxFrames int
}

// DecodeFrames 是解码的**纯状态机**：输入按时间升序的帧，输出读数序列。
//
// 与 DB 无关（store.go / decode_range.go 负责取帧），因此真实数据往返测试
// 用的就是**生产同一条**解码路径 —— 不是测试里另写一套。
//
// 累积语义（§5.1「按 slot 顺序累积状态」）：
//   - 每个 slot 维护一份「字段名 → 当前值」状态；
//   - 关键帧：状态整体重置为本帧的值（该 slot 全部有效字段带值）；
//   - 增量帧：changed 字段更新状态，未 changed 字段沿用状态；
//   - 缺值字段（NaN）：不更新状态，也不产出该帧的读数；
//   - 坏 payload：该 slot 的链标记为失效，**丢弃后续增量帧直到下一个关键帧**
//     （§8：宁可少一段数据，也不要错位一整天）。
func DecodeFrames(frames []models.TelemetryFrame, dict SlotDictionary, opts DecodeOptions) ([]Point, DecodeStats, error) {
	if dict == nil {
		return nil, DecodeStats{}, fmt.Errorf("telemetry: nil slot dictionary")
	}
	stats := DecodeStats{}
	if len(frames) == 0 {
		return nil, stats, nil
	}

	ordered := make([]models.TelemetryFrame, len(frames))
	copy(ordered, frames)
	sort.SliceStable(ordered, func(i, j int) bool {
		if !ordered[i].TS.Equal(ordered[j].TS) {
			return ordered[i].TS.Before(ordered[j].TS)
		}
		return ordered[i].Seq < ordered[j].Seq
	})
	if opts.MaxFrames > 0 && len(ordered) > opts.MaxFrames {
		ordered = ordered[len(ordered)-opts.MaxFrames:]
	}
	stats.FramesSeen = int64(len(ordered))

	want := make(map[string]struct{}, len(opts.Fields))
	for _, f := range opts.Fields {
		want[f] = struct{}{}
	}
	all := len(want) == 0

	type chain struct {
		values      map[string]float64
		invalidated bool
	}
	chains := make(map[int16]*chain)

	points := make([]Point, 0, len(ordered)*8)
	for i := range ordered {
		fr := &ordered[i]
		slot, ok := dict.ByID(fr.SlotID)
		if !ok || slot == nil {
			stats.FramesSkippedUnknownSlot++
			continue
		}
		st := chains[fr.SlotID]
		if st == nil {
			st = &chain{values: make(map[string]float64, slot.NumFields())}
			chains[fr.SlotID] = st
		}
		fd, err := DecodeFrame(fr.Payload, slot.Specs())
		if err != nil {
			stats.FramesSkippedCorrupt++
			if !st.invalidated {
				st.invalidated = true
				stats.ChainInvalidations++
			}
			continue
		}
		if n := fd.NumFields(); n != slot.NumFields() {
			// payload 声明的字段数与 slot 不一致：说明写入方用了另一个 slot 版本。
			// 按坏帧处置（断链），绝不按下标硬对 —— 那会把 A 字段的值写进 B。
			stats.FramesSkippedCorrupt++
			if !st.invalidated {
				st.invalidated = true
				stats.ChainInvalidations++
			}
			continue
		}
		if st.invalidated {
			if !fd.KeyFrame {
				stats.FramesSkippedBrokenChain++
				continue
			}
			st.invalidated = false
		}
		if fd.KeyFrame {
			stats.KeyFrames++
			// 关键帧重置整个 slot 状态：它是链的绝对锚点。
			st.values = make(map[string]float64, slot.NumFields())
		} else if fr.FrameKind == models.TelemetryFrameKindKey {
			// 列值与 payload flag 不一致：payload 是解码真值，按 payload 走，
			// 但计数让影子校验能发现写入侧 bug。
			stats.MissingBaseline++
		}

		specs := slot.Specs()
		for idx := range slot.Fields {
			name := slot.Fields[idx].Name
			if !all {
				if _, ok := want[name]; !ok {
					continue
				}
			}
			if fd.FieldInvalid(idx) {
				// 本帧该字段缺失：不更新、不产出（保留上一次已知值给快照用）。
				continue
			}
			if fd.Changed[idx] {
				st.values[name] = specs[idx].Dequantize(fd.Codes[idx])
			} else if _, has := st.values[name]; !has {
				// 增量帧声明"未变化"，但我们没有基线 ⇒ 这个字段此刻的值不可知。
				stats.MissingBaseline++
				continue
			}
			points = append(points, Point{
				DeviceID: fr.DeviceID,
				TS:       fr.TS,
				Seq:      fr.Seq,
				Name:     name,
				Value:    st.values[name],
			})
		}
		stats.FramesDecoded++
	}
	stats.Points = int64(len(points))
	return points, stats, nil
}

// SnapshotFromFrames 用同一套状态机求「当前全量快照」：每个字段取最后一次已知值。
func SnapshotFromFrames(frames []models.TelemetryFrame, dict SlotDictionary) (map[string]float64, DecodeStats, error) {
	points, stats, err := DecodeFrames(frames, dict, DecodeOptions{})
	if err != nil {
		return nil, stats, err
	}
	out := make(map[string]float64, 32)
	// points 按时间升序，后者覆盖前者 ⇒ 天然得到"最新值"。
	for _, p := range points {
		out[p.Name] = p.Value
	}
	return out, stats, nil
}
