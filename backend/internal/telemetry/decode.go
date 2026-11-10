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

// DecodeOptions 是解码参数。
type DecodeOptions struct {
	// Fields 是要产出的字段（nil / 空 = 该 slot 的全部字段）。
	Fields []string
	// MaxFrames 是**单次 Feed** 的输入帧数上限（0 = 不限制）。超出时**保留最新的**
	// MaxFrames 帧：解码的目标通常是"最近状态"，丢最老的帧比丢最新的更安全。
	//
	// 只对 DecodeFrames（一次性解码）生效；Decoder 分页 Feed 时由调用方的扫描预算
	// （maxScanFrames）控制总量，该字段保持 0。
	MaxFrames int
}

// decoderChain 是单个 slot 的累积状态。
type decoderChain struct {
	values      map[string]float64
	invalidated bool
}

// Decoder 是**有状态的**帧解码器：DecodeFrames 的逐帧状态机就是它。
//
// ============ 为什么要把状态机抽出来（2026-11-10）============
//
// 帧表读路径原来一次性 queryFrames(maxFrames+1) 再「保留最新、丢弃最老」。当锚点
// （≤from 的最后一个关键帧）距窗口起点超过 maxFrames 时，锚点帧被丢掉 ⇒ 之后的
// 增量帧全部无基线 ⇒ 0 点/报错（生产 dev14 2026-11-08~11-10 的 37 小时历史损伤即此）。
// 分页解码（decodeRangeBounded）需要**跨页保持链状态**，所以把状态机从 DecodeFrames
// 里抽成本类型：一页一页 Feed，锚点帧永远参与解码，只有扫描预算（maxScanFrames）
// 才是硬上限。
//
// 并发：**非并发安全**，一个 Decoder 只能被一个 goroutine 使用（每次读请求新建）。
type Decoder struct {
	dict SlotDictionary

	// opts 只用到 Fields（MaxFrames 通过 Feed 的批次截断生效，兼容 DecodeFrames）。
	opts DecodeOptions
	want map[string]struct{}
	all  bool

	chains map[int16]*decoderChain
	stats  DecodeStats

	// emitFrom 非零时只**产出** TS >= emitFrom 的点；更早的帧仍更新链状态（锚点
	// 预热段），但不生成 Point —— 分页解码靠它把内存限制在请求窗口内，而不是把
	// 预热段的点全攒起来再裁掉。
	emitFrom time.Time
}

// NewDecoder 构造有状态解码器。dict 为 nil 返回错误（与 DecodeFrames 一致）。
func NewDecoder(dict SlotDictionary, opts DecodeOptions) (*Decoder, error) {
	if dict == nil {
		return nil, fmt.Errorf("telemetry: nil slot dictionary")
	}
	want := make(map[string]struct{}, len(opts.Fields))
	for _, f := range opts.Fields {
		want[f] = struct{}{}
	}
	return &Decoder{
		dict:   dict,
		opts:   opts,
		want:   want,
		all:    len(want) == 0,
		chains: make(map[int16]*decoderChain),
	}, nil
}

// Stats 返回累计统计快照。
func (d *Decoder) Stats() DecodeStats { return d.stats }

// Feed 解码一批帧，返回**本批**产出的点与**累计**统计；跨调用保持链状态与全部计数。
//
// 批次内会按 (ts, seq) 做稳定排序（与 DecodeFrames 一致）。**批次之间**的先后由
// 调用方保证（keyset 分页查询天然按 (ts,seq) 升序）—— 跨批排序需要缓冲全量帧，
// 正是分页要避免的。
func (d *Decoder) Feed(frames []models.TelemetryFrame) ([]Point, DecodeStats) {
	if len(frames) == 0 {
		return nil, d.stats
	}
	ordered := make([]models.TelemetryFrame, len(frames))
	copy(ordered, frames)
	sort.SliceStable(ordered, func(i, j int) bool {
		if !ordered[i].TS.Equal(ordered[j].TS) {
			return ordered[i].TS.Before(ordered[j].TS)
		}
		return ordered[i].Seq < ordered[j].Seq
	})
	if d.opts.MaxFrames > 0 && len(ordered) > d.opts.MaxFrames {
		ordered = ordered[len(ordered)-d.opts.MaxFrames:]
	}
	d.stats.FramesSeen += int64(len(ordered))

	// 预分配提示：窗口内帧每帧平均 ~8 点（多字段 slot 会更多，append 会扩容）。
	// 锚点预热段（整页都早于 emitFrom）产出 0 点，不能为它预分配大数组 ——
	// 生产最坏情况一次扫描 ~40k 帧预热，按 len*8 预留会白占几十 MB。
	capHint := len(ordered) * 8
	if !d.emitFrom.IsZero() && ordered[0].TS.Before(d.emitFrom) {
		capHint = 0
	}
	points := make([]Point, 0, capHint)
	for i := range ordered {
		fr := &ordered[i]
		slot, ok := d.dict.ByID(fr.SlotID)
		if !ok || slot == nil {
			d.stats.FramesSkippedUnknownSlot++
			continue
		}
		st := d.chains[fr.SlotID]
		if st == nil {
			st = &decoderChain{values: make(map[string]float64, slot.NumFields())}
			d.chains[fr.SlotID] = st
		}
		fd, err := DecodeFrame(fr.Payload, slot.Specs())
		if err != nil {
			d.stats.FramesSkippedCorrupt++
			if !st.invalidated {
				st.invalidated = true
				d.stats.ChainInvalidations++
			}
			continue
		}
		if n := fd.NumFields(); n != slot.NumFields() {
			// payload 声明的字段数与 slot 不一致：说明写入方用了另一个 slot 版本。
			// 按坏帧处置（断链），绝不按下标硬对 —— 那会把 A 字段的值写进 B。
			d.stats.FramesSkippedCorrupt++
			if !st.invalidated {
				st.invalidated = true
				d.stats.ChainInvalidations++
			}
			continue
		}
		if st.invalidated {
			if !fd.KeyFrame {
				d.stats.FramesSkippedBrokenChain++
				continue
			}
			st.invalidated = false
		}
		if fd.KeyFrame {
			d.stats.KeyFrames++
			// 关键帧重置整个 slot 状态：它是链的绝对锚点。
			st.values = make(map[string]float64, slot.NumFields())
		} else if fr.FrameKind == models.TelemetryFrameKindKey {
			// 列值与 payload flag 不一致：payload 是解码真值，按 payload 走，
			// 但计数让影子校验能发现写入侧 bug。
			d.stats.MissingBaseline++
		}

		specs := slot.Specs()
		emit := d.emitFrom.IsZero() || !fr.TS.Before(d.emitFrom)
		for idx := range slot.Fields {
			name := slot.Fields[idx].Name
			if !d.all {
				if _, ok := d.want[name]; !ok {
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
				d.stats.MissingBaseline++
				continue
			}
			if emit {
				points = append(points, Point{
					DeviceID: fr.DeviceID,
					TS:       fr.TS,
					Seq:      fr.Seq,
					Name:     name,
					Value:    st.values[name],
				})
				d.stats.Points++
			}
		}
		d.stats.FramesDecoded++
	}
	return points, d.stats
}

// DecodeFrames 是解码的**纯状态机**一次运行：等价于 NewDecoder + Feed 一次。
//
// 与 DB 无关（store.go / decode_anchored.go 负责取帧），因此真实数据往返测试
// 用的就是**生产同一条**解码路径 —— 不是测试里另写一套。
//
// 累积语义（§5.1「按 slot 顺序累积状态」）：
//   - 每个 slot 维护一份「字段名 → 当前值」状态；
//   - 关键帧：状态整体重置为本帧的值（该 slot 全部有效字段带值）；
//   - 增量帧：changed 字段更新状态，未 changed 字段沿用状态；
//   - 缺值字段（NaN）：不更新状态，也不产出该帧的读数；
//   - 坏 payload：该 slot 的链标记为失效，**丢弃后续增量帧直到下一个关键帧**
//     （§8：宁可少一段数据，也不要错位一整天）。
//
// 需要跨多批帧保持状态（分页解码）时用 NewDecoder + Feed；本函数保持原有单次
// 语义不变（输入排序、MaxFrames 保留最新、stats 全部口径），既有用例即判据。
func DecodeFrames(frames []models.TelemetryFrame, dict SlotDictionary, opts DecodeOptions) ([]Point, DecodeStats, error) {
	dec, err := NewDecoder(dict, opts)
	if err != nil {
		return nil, DecodeStats{}, err
	}
	if len(frames) == 0 {
		return nil, DecodeStats{}, nil
	}
	points, stats := dec.Feed(frames)
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
