package api

// L3 读路径分派层（设计/遥测存储优化方案-2026-11-06.md §5.3）。
//
// ============ 这一层解决什么 ============
//
// L2 之后，历史数据有两个来源：
//   1. 原始表（unified_data / 将来 telemetry_frame 解码）—— 短窗，点级精度；
//   2. 分钟级宽行预聚合（telemetry_rollup_1m）—— 长窗，1 分钟粒度就够。
//
// §5.3 的判据是「短窗解码 / 长窗 rollup」，但**解码器属于 L2（task-3）尚未交付**，
// 所以本层先只接 rollup 一侧，且默认**关闭**（EHOME_TELEMETRY_ROLLUP_READ=false）：
//   · 关：走原有路径，一个字节的行为都不变（这是硬约束，见下）；
//   · 开：窗口 > 阈值且是 avg 语义 ⇒ 读 rollup；否则回落原路径。
// L2 解码器到位后，在 decodeRollupHistoryPoints 的 TODO 处接上即可（签名已定）。
//
// ============ 为什么必须默认关闭 ============
//
// 生产上 telemetry_rollup_1m 现在是**空表**（写入侧还没接线，见
// datalifecycle/telemetry_rollup.go 的接线说明）。若默认开启，长窗图表会从
// "有数据的原始表"切到"空表"，用户看到的是**空图**——比慢更糟。因此开关默认
// false，且**即使开启**，rollup 返回 0 行时也必须回落原路径（fail-open 到旧数据源，
// 不是返回空）。这两条都有行为测试（rollup_read_test.go）。
//
// ============ 与既有聚合路径的关系 ============
//
// history_points.go 的 avg/change 是**降采样**（to_timestamp(floor(epoch/W)*W) +
// avg）——它仍然要扫原始行。rollup 是**预聚合**（写入时算好）。两者语义在
// 「avg」上等价（都是桶内均值，只是桶宽不同：rollup 固定 60s，降采样是 W 秒），
// 但**点集不同**：rollup 返回的是每分钟一个点，降采样返回的是每 W 秒一个点。
// 因此本层只在「用户要的点数明显少于分钟数」时启动（长窗），短窗不启用 ——
// 否则会把 1 小时图从 3600 个点变成 60 个点，属于静默降级。
//
// change 模式（状态翻转序列）**绝不走 rollup**：rollup 的 payload 里没有
// 翻转信息，用 avg/min/max/last 重建 0→1 跃变会算错（history_points.go 的
// maxChangePoints 注释同款理由：错数据比报错更坏）。

// ============ task-22（L3 rollup 历史回填）补的两条硬约束 ============
//
// ① **覆盖率判据**：旧实现只要查到数据就 handled=true。回填进行到一半时，窗口里
//    一半分钟有 rollup、一半没有 ⇒ 直接返回**半截数据** ⇒ 用户看到图表中间凭空
//    少一段。这比空图更难发现：横轴还在，只是那几天的线没了。
//
//    判据读的是**回填时写下的元数据**（datalifecycle.RollupWindowCovered →
//    telemetry_rollup_coverage），不是运行时推算。为什么不能推算，见
//    datalifecycle/rollup_coverage.go 文件头的两条致命缺陷：
//      · 设备天然有停机间隙，按窗口长度硬算「应有桶数」会**误伤正确的系统**；
//      · 算应有桶数必须查 unified_data，而那正是回填完成后要删掉的表。
//
// ② **口径必须是桶内最后值（Last），不是均值（Avg）**：
//
//    unified_data 路径的 D1 口径是 (array_agg(value ORDER BY timestamp DESC))[1]。
//    为什么不是 avg：L1 收窄后 unified_data 不再是完整 1 Hz 序列（恒定值段每 60s
//    只有一个心跳行），avg(value) 按**行数**平均 = 按变化频率加权，图表被系统性拉偏。
//
//    而 rollup 的 payload 里同时有 Avg 和 Last，旧实现用的是 **Avg**。这是个会
//    改变图表语义的 bug：同一个图表，窗口从 5h 拉到 7h（跨过 6h 的 rollup 分界），
//    线条会从**阶梯线变成平滑线** —— 用户会以为数据变了。这比「数值差一点」
//    严重得多，是**视觉语义的突变**。
//
//    修正方式见 buildRollupD1Points：用与 D1 完全相同的桶宽公式重建分桶，
//    每桶取「桶内最后一个分钟」的 Last ⇒ 点集与 unified 路径逐点对齐，可直接对拍。
//
//    ⚠ Avg 不删除（payload 里本来就有），将来要开均值视图再加**显式参数**
//    （如 agg=avg_true），不在本次偷偷换口径。Lead 裁决（2026-11-07）：长窗不该
//    显示均值 —— unified 的 avg 在 L1 之后本来就是错的，rollup 的 avg 是对的，
//    两者混用会让「同一个 agg=avg 参数在不同窗口有不同含义」，是最坏的一种 API 语义。

import (
	"context"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/pkg/logger"

	"gorm.io/gorm"
)

const (
	// rollupReadEnv 是读路径开关的环境变量名（默认 false）。
	rollupReadEnv = "EHOME_TELEMETRY_ROLLUP_READ"

	// rollupMinWidthSeconds 是 rollup 能忠实表达的最小桶宽（1 分钟 = 它的原生粒度）。
	//
	// rollup 的粒度是 1 分钟，比 1 分钟还细的桶它**无法忠实表达**（不能凭空插值）。
	// 遇到 W < 60 的请求时退化为「一分钟一个点」而不是硬凑 W —— 后者会伪造精度。
	//
	// 这个退化是**安全**的：W = ceil(windowSeconds/maxPoints) < 60 ⇒
	// windowSeconds/60 < maxPoints ⇒ 分钟数 < maxPoints ⇒ 一分钟一个点永远不超
	// maxPoints，不会把「更快」变成「返回 100 倍数据」。
	rollupMinWidthSeconds int64 = 60

	// rollupShortWindowEnv 是「短窗/长窗」分界（默认 6h，方案 §5.3）。
	rollupShortWindowEnv = "EHOME_TELEMETRY_ROLLUP_SHORT_WINDOW"

	// defaultRollupShortWindow 是分界默认值：≤6h 走原路径，>6h 才考虑 rollup。
	// 依据（方案 §5.1 实测）：24h device14 = 86,342 帧 / 1.0 MB / 解码后 19 MB；
	// 1h = 3,600 帧 / 43 KB / 0.8 MB。6h 约 4.7 MB、110 MB 内存 —— 仍属"解码可行"，
	// 而 24h 的 19 MB 已接近单请求可接受上限，故分界取 6h。
	defaultRollupShortWindow = 6 * time.Hour
)

// rollupRead 是读路径开关的进程级状态。
//
// 为什么用可变量 + Setter 而不是每次读 env：既有约定是"启动时读一次、无热加载"
// （config.go 的注记），测试需要能切换，故用 Setter。
var rollupReadEnabled = readBoolEnvDefault(rollupReadEnv, false)

// SetRollupReadEnabled 覆写开关（仅供测试与启动接线使用；生产走环境变量）。
func SetRollupReadEnabled(enabled bool) { rollupReadEnabled = enabled }

// RollupReadEnabled 报告当前是否启用 rollup 读路径。
func RollupReadEnabled() bool { return rollupReadEnabled }

// RollupShortWindow 返回短窗/长窗分界；非法值回落默认 6h。
func RollupShortWindow() time.Duration {
	raw := strings.TrimSpace(os.Getenv(rollupShortWindowEnv))
	if raw == "" {
		return defaultRollupShortWindow
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	if secs, err := strconv.Atoi(raw); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	logger.Warnf("[rollup-read] invalid %s=%q, fallback to %s", rollupShortWindowEnv, raw, defaultRollupShortWindow)
	return defaultRollupShortWindow
}

// readBoolEnvDefault 读布尔环境变量（1/true/yes/on 视为真），非法值回落默认。
func readBoolEnvDefault(key string, def bool) bool {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch raw {
	case "":
		return def
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		logger.Warnf("[rollup-read] invalid %s=%q (want bool), fallback to %v", key, raw, def)
		return def
	}
}

// shouldUseRollup 是**纯函数**分派判据（可独立单测，不需要 DB/时间窗口以外的东西）。
//
// 五条必须同时成立：
//  1. 开关打开；
//  2. 聚合模式是 avg —— change 模式要翻转序列，rollup 表达不了；
//  3. 窗口 > 短窗阈值（否则 1 小时图会从 3600 点变 60 点，是静默降级）；
//  4. 设备维度有效（deviceID != 0）；
//  5. 窗口有效（end > start）。
func shouldUseRollup(enabled bool, mode AggMode, start, end time.Time, deviceID uint) bool {
	if !enabled || deviceID == 0 {
		return false
	}
	if mode != AggModeAvg {
		return false
	}
	if !end.After(start) {
		return false
	}
	return end.Sub(start) > RollupShortWindow()
}

// rollupHistoryPoints 用 rollup 表填充 avg 模式的图表点。
//
// 返回 (points, handled, err)：
//   - handled=false: 本次请求不该/不能由 rollup 服务（没数据、坏了、或判据不满足），
//     调用方**必须**回落原路径。这是 fail-open，不是错误；
//   - handled=true : points 就是最终结果（可能为空 —— 那说明窗口内确实没数据，
//     与"rollup 表还是空表"是两件事，由调用方区分：空表时 ReadRollupStats 走
//     的是 handled=false）。
func rollupHistoryPoints(ctx context.Context, db *gorm.DB, q historyPointQuery) ([]HistoryPoint, bool, error) {
	if db == nil {
		return nil, false, nil
	}

	// ── ⓪ 窗口末端截断：rollup 只负责「已经稳定」的主体，尾巴交给帧表 ──
	//
	// **这一步决定任务目标能不能达成，不是优化。**
	//
	// 前端的 preset（1h/24h/7d）与后端 hours=N 形态，`End` **都是 time.Now()**
	// （handler_data.go 的 hours 分支）。而 liveLag 护栏会挡掉「末端在
	// now-liveLag 之后」的窗口 —— 两者叠加的后果是：**长窗 rollup 永远不生效**，
	// 删掉 unified_data 之后 7d/30d 图表会变成空页。
	//
	// 所以不能整窗回落，而是**截断 + 拼接**：
	//
	//	rollup 服务  [Start, min(End, now-liveLag))   ← 主体，便宜
	//	帧表解码服务 [now-liveLag, End)                 ← 尾巴（≤2 分钟，几百帧，毫秒级）
	//
	// 尾巴**必须用帧表，不能用 unified_data**：unified_data 正是我们要删的表。
	// 帧表在最近 2 分钟是完整的（L2 是实时主写入路径）。
	if q.EdgeDeviceID == 0 {
		return nil, false, nil
	}
	cutoff := datalifecycle.RollupCoverageCutoff()
	bodyEnd := q.End
	tailFrom := time.Time{}
	if q.End.After(cutoff) {
		if !cutoff.After(q.Start) {
			// 整个窗口都落在实时段里：rollup 无事可做，交回调用方走原路径。
			return nil, false, nil
		}
		bodyEnd = cutoff
		tailFrom = cutoff
	}

	// ── ① 覆盖率判据（只看主体窗口）──
	//
	// 旧实现只要查到数据就 handled=true。回填进行到一半时，窗口里一半分钟有
	// rollup、一半没有 ⇒ 直接返回**半截数据** ⇒ 用户看到图表中间凭空少一段。
	// 这比空图更难发现：横轴还在，只是那几天的线没了。
	//
	// 判据读的是**回填时写下的元数据**（telemetry_rollup_coverage），不是运行时
	// 推算。为什么不能推算，见文件头「一、」两条缺陷（停机设备误伤 / 依赖即将
	// 被删除的 unified_data）。
	covered, cerr := datalifecycle.RollupWindowCovered(db, q.EdgeDeviceID, q.Start, bodyEnd)
	if cerr != nil {
		// 元数据表不存在是最常见的形态（未迁移/精简部署）：回落，不报 500。
		logger.Warnf("[rollup-read] coverage check failed, falling back to raw path: device=%d err=%v",
			q.EdgeDeviceID, cerr)
		return nil, false, nil
	}
	if !covered {
		// **回落是正常路径**，不是错误：回填还没覆盖这个窗口时就应该回落。
		// 用 Debug 而不是 Warn —— 长窗图表每轮询一次就 Warn 会把日志刷爆。
		logger.Debugf("[rollup-read] window not fully backfilled, falling back: device=%d %s..%s",
			q.EdgeDeviceID, q.Start.Format(time.RFC3339), q.End.Format(time.RFC3339))
		return nil, false, nil
	}

	// ── ② 读分钟级统计（Last/Avg/Min/Max 都在 payload 里）──
	buckets, stats, _, err := datalifecycle.ReadRollupStats(ctx, db, q.EdgeDeviceID, q.Category, q.Start, bodyEnd)
	if err != nil {
		logger.Warnf("[rollup-read] read rollup failed, falling back to raw path: device=%d field=%s err=%v",
			q.EdgeDeviceID, q.Category, err)
		return nil, false, nil
	}
	if len(buckets) == 0 {
		// 覆盖元数据说这些天「已验证」（可能是 missing=本来就没数据），而该字段
		// 一条行都没有 ⇒ 这段时间确实没有该字段的值。
		//
		// 直接返回空点集 + handled=true 是**正确**的（不是"空图"故障）：
		// 回落的 unified_data 查询会返回同样的空。而删掉老分区之后回落的代价是
		// 一次无用的全表扫描，所以这里如实回答。
		return []HistoryPoint{}, true, nil
	}

	// ── ③ 按 D1 口径重建（桶内最后值，不是均值）──
	points := buildRollupD1Points(buckets, stats, q)

	// ── ④ 帧表尾巴拼接 ──
	//
	// 拼接不是"把两个源的价值混起来"，而是在**时间维度**上把两个不相交的区间接上：
	// rollup 覆盖 [Start, cutoff)，帧表覆盖 [cutoff, End)。边界上唯一的交叠是
	// 「跨 cutoff 的那个 W 桶」：
	//
	//	· rollup 给的是 cutoff 之前最后一个分钟的 Last；
	//	· 帧表给的是该桶内 cutoff 之后最后一个采样。
	//
	// 后者才是真正的「桶内最后值」⇒ **同一时间戳上帧表段优先**（tail wins）。
	// 这条规则让拼接结果与"单源 unified_data 查询整个窗口"逐点一致 ——
	// 对拍用例 TestRollupParityWithLiveTail 就是钉这一点的。
	if !tailFrom.IsZero() {
		tail, ok := rollupLiveTailPoints(ctx, db, q, tailFrom)
		if !ok {
			// 尾巴取不到（帧表缺失/字典未注入/解码失败）⇒ **整窗回落**。
			// 绝不能只返回主体：那会让图表右边的 2 分钟凭空消失，
			// 而用户看到的是"数据停了"。宁可慢，不可缺。
			logger.Debugf("[rollup-read] live tail unavailable, falling back: device=%d %s..%s",
				q.EdgeDeviceID, tailFrom.Format(time.RFC3339), q.End.Format(time.RFC3339))
			return nil, false, nil
		}
		points = mergeRollupWithTail(points, tail)
	}
	if len(points) == 0 {
		return []HistoryPoint{}, true, nil
	}
	// 与降采样路径的可比性：兜底再夹一次（正常路径下 ③ 已保证 <= maxPoints+1）。
	if q.MaxPoints > 0 {
		points = downsampleHistoryPoints(points, q.MaxPoints)
	}
	return points, true, nil
}

// buildRollupD1Points 把分钟级 rollup 行重建成**与 unified_data 路径逐点对齐**的点集。
//
// ============ 为什么必须重建，而不是直接返回分钟点 ============
//
// unified_data 路径（D1）返回的是：桶宽 W = ceil(windowSeconds/maxPoints) 秒、
// 每桶取**桶内最后值**、点的时间戳是**桶起点**（to_timestamp(floor(epoch/W)*W)）。
//
// 若 rollup 直接返回分钟点：
//
//	· 30 天窗口 = 43,200 个点（然后被均匀抽稀到 500）—— 抽稀是"每 N 个取一个"，
//	  与"桶内最后值"完全不同的语义，且点的时间戳与 unified 路径对不上；
//	· 对拍不可能通过（点数、时间戳都不同）；
//	· 更糟的是同一个图表在 5h（unified）与 7h（rollup）两种粒度间切换时，
//	  全区间的点位置会整体跳变。
//
// 所以这里用**与 D1 完全相同的桶宽公式和绝对 epoch 对齐**重新分桶，每桶取
// 「桶内最后一个分钟」的 Last（= 该桶最后一个已落库值 = D1 的桶内最后值）。
// 这样 1d/7d/30d 的点数与时间戳与 unified 路径**逐点相同**，可以直接逐点对拍。
//
// ============ W < 60 秒怎么办 ============
//
// rollup 粒度是 1 分钟，比 1 分钟还细的桶它**无法忠实表达**（不能凭空插值）。
// 此时退化为「一分钟一个点」，而不是硬凑 W —— 后者会伪造精度。
//
// 这个退化是安全的：W = ceil(windowSeconds/maxPoints) < 60 ⇒
// windowSeconds/60 < maxPoints ⇒ 分钟数 < maxPoints ⇒ 一分钟一个点永远不超
// maxPoints（不会把"更快"变成"返回 100 倍数据"）。
//
// 实际边界（maxPoints=500 时）：窗口 > 8.3 小时才会让 W >= 60。而 rollup 只在
// 窗口 > 6 小时的区间启用，所以 6h~8.3h 这一小段走「一分钟一个点」，其余走分桶。
func buildRollupD1Points(buckets []time.Time, stats []datalifecycle.RollupFieldStat, q historyPointQuery) []HistoryPoint {
	widthSeconds := bucketWidthSeconds(historyWindowSeconds(q.Start, q.End), q.MaxPoints)
	if widthSeconds <= 0 {
		// 调用方没给 maxPoints / 窗口非法：按 rollup 原生粒度给，不猜。
		widthSeconds = rollupMinWidthSeconds
	}
	if widthSeconds < rollupMinWidthSeconds {
		// 退化路径：一分钟一个点（rollup 原生分辨率）。
		out := make([]HistoryPoint, 0, len(buckets))
		for i := range buckets {
			out = append(out, HistoryPoint{Timestamp: buckets[i].UTC(), Value: stats[i].Last})
		}
		return out
	}

	// 分桶：key = floor(epoch/W)*W（与 historyBucketExpr 的 PG 分支逐位相同）。
	// buckets 已按时间升序（ReadRollupStats 保证），所以"后到者覆盖先到者"
	// 恰好就是「桶内最后一个分钟」。
	bucketKey := func(t time.Time) int64 {
		return floorDiv(t.UTC().Unix(), widthSeconds) * widthSeconds
	}
	last := make(map[int64]float64, len(buckets))
	order := make([]int64, 0, len(buckets))
	for i := range buckets {
		k := bucketKey(buckets[i])
		if _, seen := last[k]; !seen {
			order = append(order, k)
		}
		last[k] = stats[i].Last
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })

	out := make([]HistoryPoint, 0, len(order))
	for _, k := range order {
		out = append(out, HistoryPoint{Timestamp: time.Unix(k, 0).UTC(), Value: last[k]})
	}
	// 与 trimAvgPointsBeforeStart 同语义：首桶起点可能早于 Start（floor 对齐最多
	// 早 W-1 秒），裁掉以保持「返回点都在 [start, end] 内」。
	cut := 0
	for cut < len(out) && out[cut].Timestamp.Before(q.Start) {
		cut++
	}
	if cut > 0 {
		out = out[cut:]
	}
	return out
}

// downsampleHistoryPoints 把点集均匀抽到 <= maxPoints（保留首尾）。
//
// 与 downsampleUnifiedData 同语义，但作用于轻量点（HistoryPoint）。
// 为什么必须做: rollup 的粒度是 60s，30 天 = 43,200 点；不降采样会让
// 「rollup 更快」变成「rollup 返回 100 倍数据」。
func downsampleHistoryPoints(points []HistoryPoint, maxPoints int) []HistoryPoint {
	if maxPoints <= 0 || len(points) <= maxPoints {
		return points
	}
	step := len(points) / maxPoints
	if step < 2 {
		step = 2
	}
	out := make([]HistoryPoint, 0, maxPoints+2)
	out = append(out, points[0])
	for i := step; i < len(points)-1; i += step {
		out = append(out, points[i])
	}
	out = append(out, points[len(points)-1])
	return out
}

// rollupLiveTailPoints 用**帧表解码**产出实时尾巴 [from, q.End) 的聚合点。
//
// ============ 为什么必须是帧表，不能是 unified_data ============
//
// 这条路径存在的全部理由是「删掉 unified_data 之后长窗仍然能画」。
// 尾巴若去读 unified_data，删表那天就整体失效 —— 自相矛盾。
//
// 帧表在最近 2 分钟是完整的：L2 是实时主写入路径（每一帧解析结果都编码进去），
// 不经过 L1 的值未变跳过。所以「最近 2 分钟」这段帧表反而比 unified_data 更全。
//
// 返回 ok=false 表示**取不到**（帧表缺失 / 字典未注入 / 解码失败），调用方必须
// 整窗回落 —— 绝不能把"没有尾巴"当成"尾巴确实是空的"。
func rollupLiveTailPoints(ctx context.Context, db *gorm.DB, q historyPointQuery, from time.Time) ([]HistoryPoint, bool) {
	dict := frameDictionaryFor(db)
	if dict == nil {
		return nil, false
	}
	// allowEmpty=true：这是 [now-120s, now) 的 2 分钟尾巴，目标字段在这段里没有采样是
	// **正常状态**（告警类字段只在翻转时上报、低频字段几小时一次）。若按整窗判据报
	// error，尾巴取不到 ⇒ 整窗回落 ⇒ 长窗静默截断。详见 decodeFrameWindowNow 的注释。
	decoded, err := decodeFrameWindow(ctx, db, dict, q.EdgeDeviceID, from, q.End, q.Category, true)
	if err != nil {
		return nil, false
	}
	cached := decoded[q.Category]
	// 必须复制：aggregateFramePoints 会就地排序，而 cached 是缓存里的共享切片。
	raw := make([]HistoryPoint, len(cached))
	copy(raw, cached)
	// ⚠ **前置条件（2026-11-07 boot-api 实测，务必读）**：
	//
	// 本函数假设「帧表已经落盘了最近 liveLag 的帧」。**这个假设目前不成立**：
	//
	//   telemetry/dualwrite.go 的 dualWriteBatchSize = 128，且 run() 的 select 里
	//   **没有任何 time.Ticker**（grep 实测 proc 为 0）—— 落盘只有两个触发点：
	//     ① 攒满 128 帧；② 进程退出。
	//
	// 按生产历史帧速率（36 天 3,454,432 帧 ≈ 66.6 帧/分）折算，128 帧 ≈ **115 秒**；
	// 全速双设备约 77 秒；设备越慢越久，**没有时间上界**。
	//
	// 后果：last liveLag(120s) 的帧大概率还躺在内存 batch 里 ⇒ 本函数返回**空尾巴**。
	// 这是**安全**的（见下面返回 (empty, true) 的分支：图表右边缘停在 cutoff，
	// 不会出现空洞或错值），但意味着「帧表尾巴」当前实际上补不到那 2 分钟。
	//
	// 要让这条路径真正生效，前提是给 DualWriter 加**定时 flush**（如 2s ticker）。
	// 那属于 internal/telemetry 的写路径，改动需单独指派 —— 在它就位之前，
	// end=now 的窗口右边缘会短 ≈ rollup flush 滞后（≤120s，有界）。
	//
	points := aggregateFramePoints(raw, q)
	if len(points) == 0 {
		// 空尾巴**不是失败**：设备在最后 2 分钟没有上报（离线/告警字段无翻转/低频字段）
		// 是正常状态。
		//
		// 但「设备明显刚报过、帧表却没有帧」是真缺口（落盘滞后 / 分区错位），必须可观测。
		// 这里只 Warn **不回落**：删掉 unified_data 之后，「回落」等于读一张不存在的表
		// ⇒ 整窗空页；而「少 2 分钟尾巴」至少把 30 天里的 29.998 天正确显示出来。
		// 两者都错，但后者错得小得多，且不会让用户失去整段历史。
		//
		// 补充实测（2026-11-07）：帧表**不存在**时解码器返回 err=nil + 全零 stats，
		// 也就是说"表被删/分区丢失"在这里表现为"没有帧"而不是错误。所以本函数的
		// ok=false 分支实际只在真正的 DB 错误时触发。这个降级是可接受的（同上：
		// 少 2 分钟 << 整窗空页），但要知道它存在。
		if last := probeDeviceLastDataAt(ctx, db, q.EdgeDeviceID); tailEmptyLooksLikeLag(last, from) {
			logger.Warnf("[rollup-read] live tail empty although device reported at %s (tail window %s..%s, device=%d field=%s) — frames may be lagging",
				last.Format(time.RFC3339), from.Format(time.RFC3339), q.End.Format(time.RFC3339),
				q.EdgeDeviceID, q.Category)
		}
	}
	// ⚠ 传**原始的 q**（Start/End 是整个窗口）而不是尾巴子窗口：
	// aggregateFramePoints 用 historyWindowSeconds(Start, End) 算桶宽 W，
	// 必须与主体、与 unified_data 路径用**同一个 W**，否则桶边界错开 ⇒ 拼接处出现
	// 假跳变。用尾巴子窗口算会得到更小的 W，那是错的。
	return points, true
}

// mergeRollupWithTail 把主体点集与尾巴点集按时间戳合并，**同一时间戳尾巴优先**。
//
// 为什么尾巴优先：边界上那个跨 cutoff 的 W 桶，rollup 只能给到"cutoff 前最后一个
// 分钟"，而帧表给的是该桶内真正的最后一个采样 —— 后者才符合 D1「桶内最后值」。
//
// 两侧都已按时间升序且时间戳是桶起点（绝对 epoch 对齐），所以按时间戳去重即可。
// tailEmptyLooksLikeLag 判断「尾巴空」是否属于**真缺口**（帧没落盘）而非「设备安静」。
//
// ============ 为什么要把它抽成纯函数（2026-11-08）============
//
// 它原来是 rollupLiveTailPoints 里一个内联 if 的条件。抽出来的理由不是「复用」，
// 是**可测**：那段代码的产出是一条 logger.Warnf，而日志无法被可靠断言
// （要么注入 logger —— 脆；要么 grep 日志 —— 更脆）。
//
// 而这个判断本身是**整个 rollup 尾巴路径上唯一的故障可观测点**：
//
//	① 设备安静（last_data_at 早于尾巴窗口起点）=> 正常，不该告警
//	② 设备刚报过（last_data_at 在窗口内）但帧表没帧 => 真故障（落盘滞后/分区错位）
//
// 两者在「点集为空」这个症状上完全一样，唯一的区别就是它。
// 抽成纯函数之后，三种情况（零值 / 早于 / 窗口内）都能被穷举钉住。
//
// ⚠ 它的数据来源 probeDeviceLastDataAt 读的是 edge_devices.last_data_at。
// 那个函数原本是 frameCovered（覆盖判据）的锚点参数，覆盖判据已于 2026-11-08 删除；
// **但它必须留**，因为这里是它唯一还在服务的用途。详见 probeDeviceLastDataAt 的注释。
//
// 参数：
//
//	lastDataAt 设备最后一次上报时刻（零值 = 设备从没报过 / 读取失败）
//	from       尾巴窗口的起点（尾巴是 [from, now)）
func tailEmptyLooksLikeLag(lastDataAt, from time.Time) bool {
	// 零值（从没报过 / 查询失败）：无法判断 ⇒ 不当成故障（宁可漏报也不误报）。
	if lastDataAt.IsZero() {
		return false
	}
	// 设备最后一次上报**早于**尾巴窗口 ⇒ 设备已安静，尾巴空是正常的。
	// 用 After 而不是 !Before：恰好等于 from 时（边界）按「不在窗口内」处理，
	// 与下面 Warnf 里 last.After(from) 的原语义一致。
	return lastDataAt.After(from)
}

func mergeRollupWithTail(body, tail []HistoryPoint) []HistoryPoint {
	if len(tail) == 0 {
		return body
	}
	byTS := make(map[int64]float64, len(body)+len(tail))
	for i := range body {
		byTS[body[i].Timestamp.Unix()] = body[i].Value
	}
	for i := range tail {
		// tail wins：后写覆盖先写。
		byTS[tail[i].Timestamp.Unix()] = tail[i].Value
	}
	keys := make([]int64, 0, len(byTS))
	for k := range byTS {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	out := make([]HistoryPoint, 0, len(keys))
	for _, k := range keys {
		out = append(out, HistoryPoint{Timestamp: time.Unix(k, 0).UTC(), Value: byTS[k]})
	}
	// 不做额外裁剪：body 侧已在 buildRollupD1Points 里裁过 Start，tail 侧的桶
	// 全部 >= cutoff > Start。两侧时间戳都是桶起点且已排序，合并后天然满足
	// 「返回点都在 [Start, End] 内、严格递增」两条契约。
	return out
}
