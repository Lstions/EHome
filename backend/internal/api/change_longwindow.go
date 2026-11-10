package api

// change_longwindow.go — change 模式（翻转序列）的**长窗读路径**：
// rollup 分钟统计**精确预筛** + 帧表**精解候选分钟**（2026-11-10，task-5）。
//
// ============ 这个文件解决什么问题 ============
//
// change 类（protection_status / fet_status）的图表语义是「首点 + 每次值变化」，
// 不能用 rollup 的 avg/min/max/last 重建（会算错触发次数，见 rollup_read.go 文件头）。
// 而帧表单次解码有 frameDecodeMaxWindow（生产 6h）与 MaxDecodeFrames(32768) 两道护栏
// ⇒ 改造前 >6h 的 change 窗口**必然失败**（返回 200 + 空数组，被前端渲染成「暂无数据」）。
//
// BMS 诊断页「保护触发历史卡」的档位是 7/30/90 天，默认 30 天 ⇒ 永远没数据。
// 纯帧分段解码 30 天要解约 260 万帧（实测约 12.8µs/帧 ⇒ 33 s），不可接受。
//
// ============ 为什么 rollup 的 min/max 是**精确**预筛（不是近似）============
//
// rollup 的写入侧（RollupCollector.Observe，main.go 接线）对**每一帧的每一个字段**
// 累加 Min/Max/Last/Count —— 包括被 L1 跳过入库的帧（main.go 注释：「L1 跳过 INSERT
// 的帧仍然是真实采样，必须进分钟桶」）。因此对任意一分钟桶：
//
//	· Min == Max ⇒ 该分钟内所有被采样的值**完全相同** ⇒ 该分钟内**必然 0 次翻转**；
//	· Min != Max ⇒ 该分钟内至少发生过一次变化 ⇒ 精解这一分钟（≈60 帧）即可拿到精确序列。
//
// 第一条是**精确结论**：跳过 min==max 的分钟不会漏掉任何翻转点。
// 跨分钟的变化另有一条判据（见 changeCandidateIndexes）：
//
//	last(上一分钟) != min(当前分钟) ⇒ 变化发生在当前分钟的首个采样点 ⇒ 当前分钟必须精解
//	（要拿到该点的时间戳；只比较值会导致图表上触发时刻错位最多 60 s）。
//
// ============ 精解后的「一次归约」============
//
// 候选分钟解出的是**原始点**，按时间序与实时尾巴的原始点拼接后，统一交给
// aggregateFramePoints 做**一次** change 归约：
//
//	跳过的分钟采样值全部相同（= 上一个已知值）⇒ 它们不可能产生翻转点 ⇒
//	在原始点序列里删掉它们不改变归约结果（有对拍单测：与全量分段解码逐点相等）。
//
// ============ 成本（生产 dev14 只读量测，Lead 2026-11-10）============
//
//	7 天  = 10,020 个分钟桶：min!=max 的桶 88 个（0.88%）+ 跨桶变化 140 个
//	      ⇒ 候选分钟 228 个（2.3%）⇒ 精解约 1.4 万帧 ≈ 0.2 s
//	30 天 ≈ 980 个候选分钟 ⇒ 约 5.9 万帧 ≈ 0.75 s；加 rollup 读取/解析 ≈ 1.5 s 量级
//	90 天 ≈ 3000 个候选分钟 ⇒ 约 18 万帧 ≈ 2.3 s；加 rollup 读取/解析 ≈ 4~6 s 量级
//	（前端诊断卡超时 30 s，余量充足）
//
// ============ 零数据库写入 ============
//
// 本文件只有 SELECT（rollup 期 + 帧表精解）。不建表、不加列、不写任何行 ——
// 数据库增长 0 字节。
//
// ============ 长链例外（旧 encoder 的存量帧）============
//
// 2026-11-08 15:57Z ~ 2026-11-10 05:21Z 的存量帧没有周期关键帧 ⇒ 任一分钟的精解
// 都要从 11-08 那个锚点解 10 万+ 帧。逐候选分钟解会把成本乘上候选数，所以对
// 「锚点距离 > 2min」的候选做**按锚点合并**：同一条长链上的候选分钟合成一段，
// 一次解码解完（见 decodeChangeCandidateMinutes / decodeLongChainRange）。
// 这段存量数据随分区保留期自然老化，之后所有候选都是「锚点 ≤60s」的快路径。

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync/atomic"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/telemetry"
	"ehome/backend/pkg/logger"
)

const (
	// maxChangeLongWindow 是 change 模式（rollup 预筛路径）允许的最大窗口：90 天。
	//
	// 取值依据：前端 BMS 诊断卡的最大档位就是「近 90 天」（ProtectionHistoryCard.vue）；
	// 更长的窗口没有业务场景，且 rollup 分区保留期之外也读不到分钟桶。
	// 超过它返回**诚实错误**（前端显示「加载失败」），而不是空数组（「暂无数据」）。
	maxChangeLongWindow = 90 * 24 * time.Hour

	// changePrefilterRollupPageSize 是 rollup 分钟桶的**分页读取**大小。
	//
	// 为什么不一次性读全窗口：90 天 = 129,600 桶 x 1.47 KB/payload ≈ 190 MB，
	// 一次物化会占掉容器内存的 1/8。分页 2,000 桶 ≈ 3 MB/页，峰值可控。
	changePrefilterRollupPageSize = 2000

	// changeLongChainAnchorThreshold 判定「这一分钟的锚点太远」：锚点距离超过它
	// 就不逐分钟解，而是把同一条长链上的候选合并成一段一次解完。
	//
	// 2 分钟的依据：正常（关键帧每小时/每分钟周期）情况下锚点距离 ≤ KeyFrameInterval(60s)；
	// 超过 2min 说明落在旧 encoder 的无关键帧区间，逐分钟解会重复付出长链代价。
	changeLongChainAnchorThreshold = 2 * time.Minute

	// changeLongChainMergeGap 是同一条长链内相邻候选分钟允许合并的最大间隙。
	// 长链区间内每个候选都要从同一个锚点解起，合并的边际成本只有中间那段帧；
	// 2 小时足够把旧存量区间（1.5 天）合并成 1~2 段，同时避免跨越多个不相关区间。
	changeLongChainMergeGap = 2 * time.Hour

	// changeLongChainMaxFrames 是长链段单次解码允许的最大帧数（防内存/耗时失控）。
	//
	// 300,000 帧 ≈ 3.8 s（12.8µs/帧）≈ 60~120 MB 峰值（旧 decoder 一次性物化）。
	// 旧存量区间最坏约 13 万帧（锚点 11-08 15:57 到 11-10 05:21），在限内。
	maxChangeLongChainFrames = 300000

	// changePrefilterDecodeTimeout 是长链段解码的硬超时（单段）。
	changePrefilterDecodeTimeout = 20 * time.Second
)

// changeRollupMinute 是 change 预筛需要的一分钟统计量（rollup payload 的子集）。
type changeRollupMinute struct {
	Start time.Time
	Min   float64
	Max   float64
	Last  float64
	Count uint32
}

// changePrefilterDecodeHookForTest 是**测试专用**的帧解码次数计数器（生产恒为 nil）。
//
// 预筛的核心收益是「少解码」：候选分钟数 2.3%、跳过 97.7%。只看耗时会被机器抖动
// 干扰，数解码次数才是确定性的断言。
var changePrefilterDecodeHookForTest *int64

// changeLongWindowPoints 用 rollup 预筛 + 帧表精解重建 change 序列（长窗）。
//
// 返回 (points, true, nil)  成功 —— points 是**最终**图表点（已做 change 归约）；
// 返回 (nil, false, nil)    预筛不可用（rollup 覆盖未验证 / 读取失败 / 整窗在实时段）——
//                           调用方必须回落纯帧分段解码（≤8 天内可服务），>8 天则报诚实错误；
// 返回 (nil, false, err)    真错误（帧解码失败 / 超长链 / 超过 change 点上限）——
//                           直接上抛（前端显示「加载失败」，绝不谎报「暂无数据」）。
func (s frameSource) changeLongWindowPoints(ctx context.Context, q historyPointQuery) ([]HistoryPoint, bool, error) {
	window := q.End.Sub(q.Start)
	if window <= frameDecodeMaxWindow {
		// 短窗走单次解码：更便宜，且不依赖 rollup 覆盖。
		return nil, false, nil
	}
	if window > maxChangeLongWindow {
		return nil, false, fmt.Errorf(
			"change 模式（翻转序列）窗口上限 %d 天；请求窗口 %s 超过该上限。"+
				"change 语义必须逐帧重建翻转序列（不能走分钟聚合）；请缩小窗口",
			int(maxChangeLongWindow/(24*time.Hour)), window.Truncate(time.Second))
	}
	if s.db == nil || s.dict == nil {
		return nil, false, nil
	}

	// ── ⓪ 主体 / 实时尾巴切分：rollup 只负责「已落定的完整分钟」──
	//
	// 与 rollup_read.go 的 cut + tail 拼接同一套口径，但这里**必须截到整分钟**：
	// cutoff 落在某一分钟的中间时，该分钟的 rollup 桶里已经含有 cutoff 之后的帧样本；
	// 若主体用 [start, cutoff) 而尾巴用 [cutoff, end)，这一分钟会被两边各算一次
	// ⇒ 接缝处多出翻转点。截到分钟边界后：
	//
	//	主体 [start, bodyEnd) 的每个桶都完整落在 bodyEnd 之前；
	//	尾巴 [bodyEnd, end) 从整分钟边界开始，与主体首尾相接、不重叠不留缝。
	cutoff := datalifecycle.RollupCoverageCutoff().UTC()
	bodyEnd := q.End
	if bodyEnd.After(cutoff) {
		bodyEnd = cutoff.Truncate(time.Minute)
	}
	if !bodyEnd.After(q.Start) {
		// 整窗都在实时段（> frameDecodeMaxWindow 的窗口不会走到这里，保守返回）。
		return nil, false, nil
	}

	// ── ① 覆盖判据：rollup 必须**已验证覆盖**主体窗口，否则预筛不成立 ──
	//
	// 覆盖未验证时不能「按现有桶凑」：缺失的分钟可能是真没数据，也可能是回填没跑；
	// 后者会让预筛把「有翻转的分钟」当成「没数据」⇒ 静默少算触发次数。
	covered, cerr := datalifecycle.RollupWindowCovered(s.db, q.EdgeDeviceID, q.Start, bodyEnd)
	if cerr != nil {
		logger.Warnf("[change-prefilter] coverage check failed, fallback to pure frame decode: device=%d err=%v",
			q.EdgeDeviceID, cerr)
		return nil, false, nil
	}
	if !covered {
		logger.Infof("[change-prefilter] window not covered by rollup, fallback to pure frame decode: device=%d %s..%s",
			q.EdgeDeviceID, q.Start.Format(time.RFC3339), q.End.Format(time.RFC3339))
		return nil, false, nil
	}

	// ── ② 读分钟统计（分页，避免 90 天 payload 一次性物化）──
	//
	// 起点**向下取整到分钟**：q.Start 落在某分钟中间时（前端窗口基本都是 now-N 天，
	// 必然在其中），该分钟桶里 [Start, 桶末) 的采样也属于本窗口。若从 q.Start 直接读，
	// 会把这一分钟整桶排除 ⇒ 窗口左边缘最多丢 1 分钟的采样 ⇒ 可能漏掉一次翻转。
	// 解码时会把首候选分钟的起点裁到 q.Start（见 decodeChangeCandidateMinutes）。
	rollupFrom := q.Start.Truncate(time.Minute)
	minutes, badRows, err := readChangeRollupMinutes(ctx, s.db, q.EdgeDeviceID, q.Category, rollupFrom, bodyEnd)
	if err != nil {
		logger.Warnf("[change-prefilter] read rollup failed, fallback to pure frame decode: device=%d field=%s err=%v",
			q.EdgeDeviceID, q.Category, err)
		return nil, false, nil
	}
	if badRows > 0 {
		// 坏 payload 是派生数据损坏：跳过并计数。极少见，但必须可观测 —— 跳过的桶
		// 可能藏着翻转点，所以只 Warn 不静默。
		logger.Warnf("[change-prefilter] skipped %d corrupt rollup payload rows: device=%d field=%s",
			badRows, q.EdgeDeviceID, q.Category)
	}

	// ── ③ 精确预筛出候选分钟 → 回帧表精解 ──
	var raw []HistoryPoint
	if len(minutes) > 0 {
		candidates := changeCandidateIndexes(minutes)
		pts, derr := decodeChangeCandidateMinutes(ctx, s.db, s.dict, q.EdgeDeviceID, q.Category,
			q.Start, bodyEnd, minutes, candidates)
		if derr != nil {
			return nil, false, derr
		}
		raw = append(raw, pts...)
	}

	// ── ④ 实时尾巴：rollup 还没有的最近 ≤3 分钟，用帧表解原始点 ──
	if q.End.After(bodyEnd) {
		tail, terr := decodeChangeLiveTail(ctx, s.db, s.dict, q.EdgeDeviceID, q.Category, bodyEnd, q.End)
		if terr != nil {
			// 尾巴取不到 ⇒ 不能给「缺最后 2 分钟的 change 序列」（会少算/错算最后一次触发）。
			// 交回调用方：≤8 天窗口还能用纯帧分段解码兜底；>8 天会返回诚实错误。
			logger.Warnf("[change-prefilter] live tail decode failed, fallback: device=%d field=%s %s..%s err=%v",
				q.EdgeDeviceID, q.Category, bodyEnd.Format(time.RFC3339), q.End.Format(time.RFC3339), terr)
			return nil, false, nil
		}
		raw = append(raw, tail...)
	}

	// ── ⑤ 空结果判定：覆盖已验证 ⇒ 该字段确实没采样；但「帧表有帧却 0 点」是解码故障 ──
	if len(raw) == 0 {
		if historyFrameCountUpTo(ctx, s.db, q.EdgeDeviceID, q.Start, q.End, 1) > 0 {
			return nil, false, fmt.Errorf(
				"frame decode produced no points for change long window: device=%d field=%s window=%s "+
					"（帧表有帧但一个点都没解出 —— 解码故障，不是「暂无数据」）",
				q.EdgeDeviceID, q.Category, window.Truncate(time.Second))
		}
		return []HistoryPoint{}, true, nil
	}

	// ── ⑥ **只做一次** change 归约 ──
	//
	// 候选分钟解出的是原始点；跳过的分钟采样值恒等于上一个已知值，删掉它们不改变
	// 归约结果（见文件头）。归约在这里统一做一次，绝不能在精解时逐分钟做
	// —— 那样每个候选分钟都会各自输出「本分钟首点」，接缝处会多出假翻转点。
	out := aggregateFramePoints(raw, q)
	if exceedsChangePointCap(q.Mode, len(out)) {
		return nil, false, fmt.Errorf(
			"change 点集 %d 超过上限 %d（窗口 %s）；请缩小时间窗",
			len(out), maxChangePoints, window.Truncate(time.Second))
	}
	return out, true, nil
}

// readChangeRollupMinutes 分页读取 [from, to) 内某设备某字段的分钟统计（升序）。
//
// 与 datalifecycle.ReadRollupStats 的**读取口径**一致（坏行跳过并计数、字段缺行的
// 桶跳过 —— 该分钟该字段没有采样，不是坏数据），区别只有两个：
//   - 分页（keyset: bucket >= cursor）控制峰值内存，90 天不一次性物化 190 MB payload；
//   - 只保留预筛需要的 4 个值，不把整份 payload 的 map 留着。
func readChangeRollupMinutes(ctx context.Context, db *gorm.DB, deviceID uint, field string, from, to time.Time) ([]changeRollupMinute, int, error) {
	if db == nil {
		return nil, 0, fmt.Errorf("change prefilter: nil db")
	}
	out := make([]changeRollupMinute, 0, 1024)
	bad := 0
	cursor := from
	for {
		if err := ctx.Err(); err != nil {
			return nil, bad, err
		}
		type row struct {
			Bucket  time.Time
			Payload []byte
		}
		var rows []row
		if err := db.WithContext(ctx).Raw(
			"SELECT bucket, payload FROM "+datalifecycle.TelemetryRollupTable+
				" WHERE device_id = ? AND bucket >= ? AND bucket < ? ORDER BY bucket LIMIT ?",
			deviceID, cursor, to, changePrefilterRollupPageSize,
		).Scan(&rows).Error; err != nil {
			return nil, bad, err
		}
		if len(rows) == 0 {
			break
		}
		for _, r := range rows {
			decoded, derr := datalifecycle.DecodeRollupPayload(r.Payload)
			if derr != nil {
				bad++
				continue
			}
			st, ok := decoded[field]
			if !ok {
				continue // 该分钟没有这个字段的样本：不是坏数据，也不是候选
			}
			out = append(out, changeRollupMinute{
				Start: r.Bucket.UTC(),
				Min:   st.Min,
				Max:   st.Max,
				Last:  st.Last,
				Count: st.Count,
			})
		}
		// keyset 下一页：bucket 是分钟粒度，+1s 一定跳到下一分钟的桶（不会跳桶）。
		next := rows[len(rows)-1].Bucket.Add(time.Second)
		if !next.After(cursor) {
			next = cursor.Add(time.Second)
		}
		cursor = next
	}
	return out, bad, nil
}

// changeCandidateIndexes 精确标出「必须回帧表精解」的分钟下标（minutes 必须升序）。
//
// 三条判据（都是**精确**结论，不是启发式）：
//
//	① Min != Max ⇒ 该分钟内部发生过变化 ⇒ 必解；
//	② 该分钟 Min == Max 但 != 上一个已知值 ⇒ 变化发生在该分钟的**首个采样点** ⇒ 必解
//	   （要拿到那个采样点的精确时间戳；只比较值会让触发时刻错位最多 60 s）；
//	③ 窗口内第一个有采样的分钟 ⇒ 必解（change 序列的「首点」）。
//
// 被跳过的分钟（非候选）满足：Min == Max == 上一个已知值 ⇒ 分钟内所有采样同值、
// 且与上一个采样同值 ⇒ 不可能产生翻转点。
//
// NaN 处理：帧表量化编码表达不了 NaN（见 aggregateFramePoints 的注释），但 rollup
// 回填自 unified_data 理论上可能有。保守把含 NaN 的分钟标成候选，并清空「上一个
// 已知值」—— 宁可多解一分钟，不可漏判一次翻转。
func changeCandidateIndexes(minutes []changeRollupMinute) []int {
	out := make([]int, 0, 16)
	var prev float64
	havePrev := false
	for i := range minutes {
		m := minutes[i]
		candidate := false
		switch {
		case math.IsNaN(m.Min) || math.IsNaN(m.Max) || math.IsNaN(m.Last):
			candidate = true
			havePrev = false
		case m.Count == 0:
			// 理论上不会出现（字段在 payload 里就说明 count>=1）；保守解。
			candidate = true
			havePrev = false
		case m.Min != m.Max:
			candidate = true
		case !havePrev:
			candidate = true
		case m.Min != prev:
			candidate = true
		}
		if candidate {
			out = append(out, i)
		}
		if !math.IsNaN(m.Last) {
			prev = m.Last
			havePrev = true
		}
	}
	return out
}

// changeCandidate 是一个候选分钟的解码计划（分钟边界已裁到窗口内）。
type changeCandidate struct {
	start time.Time
	end   time.Time
	// anchor 是 ≤ start 的最后一个关键帧；anchorDist 是 start 到锚点的距离。
	// 长链（anchorDist > changeLongChainAnchorThreshold）需要合并解码，否则
	// 每个候选都会重复解一遍锚点之后的全部帧。
	anchor     time.Time
	hasAnchor  bool
	anchorDist time.Duration
}

// decodeChangeCandidateMinutes 精解候选分钟，返回**原始点**（未归约、按时间升序）。
func decodeChangeCandidateMinutes(
	ctx context.Context, db *gorm.DB, dict telemetry.SlotDictionary,
	deviceID uint, field string, windowStart, windowEnd time.Time,
	minutes []changeRollupMinute, candidates []int,
) ([]HistoryPoint, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	// ① 计算候选的解码窗口与锚点距离。
	plans := make([]changeCandidate, 0, len(candidates))
	for _, idx := range candidates {
		start := minutes[idx].Start
		if start.Before(windowStart) {
			start = windowStart
		}
		end := minutes[idx].Start.Add(time.Minute)
		if end.After(windowEnd) {
			end = windowEnd
		}
		if !end.After(start) {
			continue
		}
		anchor, ok, err := telemetry.KeyFrameAnchor(ctx, db, deviceID, start)
		if err != nil {
			return nil, fmt.Errorf("change prefilter: keyframe anchor for device %d at %s: %w",
				deviceID, start.Format(time.RFC3339), err)
		}
		var dist time.Duration
		if ok && anchor.Before(start) {
			dist = start.Sub(anchor)
		}
		plans = append(plans, changeCandidate{start: start, end: end, anchor: anchor.UTC(), hasAnchor: ok, anchorDist: dist})
	}

	// ② 逐候选 / 逐长链段解码。
	raw := make([]HistoryPoint, 0, len(plans)*60)
	for i := 0; i < len(plans); {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if plans[i].anchorDist > changeLongChainAnchorThreshold {
			// 长链：把同一条链上、间隙 ≤ mergeGap 的连续候选合并成一段，一次解完。
			j := i
			for j+1 < len(plans) &&
				plans[j+1].anchorDist > changeLongChainAnchorThreshold &&
				plans[j+1].start.Sub(plans[j].end) <= changeLongChainMergeGap {
				j++
			}
			pts, err := decodeChangeLongChainRange(ctx, db, dict, deviceID, field,
				plans[i].start, plans[j].end, plans[i])
			if err != nil {
				return nil, err
			}
			raw = append(raw, pts...)
			i = j + 1
			continue
		}
		// 普通候选：锚点距离 ≤ 2min，单分钟解码（走 decodeFrameWindow 的缓存/单飞）。
		//
		// allowEmpty=true：候选分钟理论上必有采样（rollup Count>0），但**断链**也可能
		// 让该分钟的帧全部解不出来 —— 那是数据形态（与全量解码同表现），不是故障。
		// 真正的「整窗解码故障」由 changeLongWindowPoints ⑤ 的「有帧但 0 点」统一判。
		decoded, err := decodeFrameWindow(ctx, db, dict, deviceID, plans[i].start, plans[i].end, field, true)
		if err != nil {
			return nil, fmt.Errorf("change prefilter: decode candidate minute %s: %w",
				plans[i].start.Format(time.RFC3339), err)
		}
		if changePrefilterDecodeHookForTest != nil {
			atomic.AddInt64(changePrefilterDecodeHookForTest, 1)
		}
		raw = append(raw, decoded[field]...)
		i++
	}

	// 长链段的点可能跨多个候选分钟且与普通候选交错；统一按时间序排列，
	// 供 aggregateFramePoints 做一次归约（它内部也会再稳一次）。
	sort.SliceStable(raw, func(a, b int) bool { return raw[a].Timestamp.Before(raw[b].Timestamp) })
	return raw, nil
}

// decodeChangeLongChainRange 解一段「锚点很远」的候选区间，返回该区间内的原始点。
//
// 为什么不能逐分钟 decodeFrameWindow：decodeFrameWindow 内部会把 from 锚回
// 「≤ from 的最后一个关键帧」再解 —— 旧 encoder 的存量区间没有周期关键帧，
// 每一分钟都要重复解 10 万+ 帧。合并成一段后每帧只解一次。
func decodeChangeLongChainRange(
	ctx context.Context, db *gorm.DB, dict telemetry.SlotDictionary,
	deviceID uint, field string, start, end time.Time, first changeCandidate,
) ([]HistoryPoint, error) {
	// 解码实际起点是锚点（DecodeRangeAnchoredBounded 内部锚回），帧数上限要按
	// 「锚点 → end」算，而不是按 [start, end) 算。
	countFrom := start
	if first.hasAnchor && first.anchor.Before(start) {
		countFrom = first.anchor
	}
	n := historyFrameCountUpTo(ctx, db, deviceID, countFrom, end, maxChangeLongChainFrames)
	if n > int64(maxChangeLongChainFrames) {
		return nil, fmt.Errorf(
			"change prefilter: 候选区间 %s..%s 落在超长关键帧间隔内（锚点 %s 起至少 %d 帧），"+
				"超过单段解码上限 %d 帧；请缩小查询窗口",
			start.Format(time.RFC3339), end.Format(time.RFC3339), countFrom.Format(time.RFC3339),
			n, maxChangeLongChainFrames)
	}
	maxFrames := int(n) + 256
	span := end.Sub(start)
	decodeCtx, cancel := context.WithTimeout(ctx, changePrefilterDecodeTimeout)
	defer cancel()
	points, stats, err := telemetry.DecodeRangeAnchoredBounded(
		decodeCtx, db, dict, deviceID, start, end, []string{field}, span+time.Second, maxFrames)
	if changePrefilterDecodeHookForTest != nil {
		atomic.AddInt64(changePrefilterDecodeHookForTest, 1)
	}
	if err != nil {
		return nil, fmt.Errorf("change prefilter: decode long-chain range %s..%s: %w",
			start.Format(time.RFC3339), end.Format(time.RFC3339), err)
	}
	if stats.FramesTruncated > 0 {
		return nil, fmt.Errorf("change prefilter: long-chain range %s..%s truncated: %d frames dropped",
			start.Format(time.RFC3339), end.Format(time.RFC3339), stats.FramesTruncated)
	}
	out := make([]HistoryPoint, 0, len(points))
	for i := range points {
		if points[i].Name != field {
			continue
		}
		if points[i].TS.Before(start) || !points[i].TS.Before(end) {
			continue
		}
		out = append(out, HistoryPoint{Timestamp: points[i].TS.UTC(), Value: points[i].Value})
	}
	return out, nil
}

// decodeChangeLiveTail 解实时尾巴 [from, to) 的原始点（rollup 尚未落定的最近几分钟）。
//
// 空尾巴是**正常状态**（设备安静 / 该字段这段没有采样），不报错；但「帧表有帧却
// 0 点」要 Warn（与 rollup_read.go 的 rollupLiveTailPoints 同口径）。
func decodeChangeLiveTail(
	ctx context.Context, db *gorm.DB, dict telemetry.SlotDictionary,
	deviceID uint, field string, from, to time.Time,
) ([]HistoryPoint, error) {
	if !to.After(from) {
		return nil, nil
	}
	decoded, err := decodeFrameWindow(ctx, db, dict, deviceID, from, to, field, true)
	if err != nil {
		return nil, err
	}
	if changePrefilterDecodeHookForTest != nil {
		atomic.AddInt64(changePrefilterDecodeHookForTest, 1)
	}
	pts := decoded[field]
	if len(pts) == 0 {
		if n := historyFrameCountUpTo(ctx, db, deviceID, from, to, 1); n > 0 {
			logger.Warnf("[change-prefilter] live tail has frames but decoded 0 points: device=%d field=%s %s..%s",
				deviceID, field, from.Format(time.RFC3339), to.Format(time.RFC3339))
		}
		return nil, nil
	}
	out := make([]HistoryPoint, len(pts))
	copy(out, pts)
	return out, nil
}
