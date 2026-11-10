package api

// history_source.go — 历史点的**数据源抽象**与双读回落（task-18）。
//
// ============ 这个文件解决什么问题 ============
//
// L1/L2 之后，同一批物理量存了两个副本：
//
//	unified_data      132 MB/天（每字段一行）
//	telemetry_frame    18 MB/天（位打包增量，已无损覆盖）
//
// 但**帧表没有读路径**，所有图表仍读 unified_data ⇒ 老的 15 GB 分区永远删不掉，
// 「长期保留」就不成立。本文件给 history_points 的读取加一层数据源抽象，
// 让它能读帧表，并在帧表未覆盖时**回落**到 unified_data。
//
// ============ 三条硬约束（每条都有翻车方式）============
//
// **1) 聚合口径必须逐值相等**
//
// 现有 D1 口径是「**桶内最后值**」（历史原因：L1 收窄后 avg(value) 按行数加权，
// 被系统性拉偏 —— 见 history_points.go 的 D1 大段注释）。帧表解码后必须 produce
// **完全相同的点集**，不能变成平均值，否则同一时段在接线前后的图表形状不同，
// 用户会以为数据坏了。
//
// **2) 回退必须可靠（这是删老分区的安全前提）**
//
// 判断错方向的两种后果不对称：
//
//	· 帧表已覆盖却说"没覆盖" ⇒ 白回落一次，慢但**正确**；
//	· 帧表没覆盖却说"覆盖了" ⇒ 读到空/半截数据，**用户看到空图**。
//
// 因此覆盖判据一律**保守**：要求帧表覆盖区间完整包住请求窗口，且窗口内确有帧。
//
// **3) 不能静默扫几百万帧**
//
// 解码 N 帧的成本远超索引扫描。avg 模式：窗口 > frameDecodeMaxWindow 或帧数 >
// MaxDecodeFrames 时**不解码**，交给 rollup 的分钟聚合（预聚合，写时算好）。
// 超过 MaxDecodeWindow（24h）时 DecodeRange 本身会拒绝 —— 我们提前判掉，不让它
// 走到报错路径。
// change 模式（2026-11-10，task-5）：它有独立长窗通路 —— rollup 的 Min/Max/Last
// 做**精确预筛**（min==max 的分钟不可能翻转，可安全跳过），只精解候选分钟
// （生产 7 天实测 2.3%），30/90 天也只需解几万帧；见 change_longwindow.go。

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/telemetry"
	"ehome/backend/pkg/logger"
)

const (
	// frameReadEnv 是帧表读路径开关（默认 true —— 与 rollup 不同：帧表是**主路径**，
	// 且回退判据保守，开着不会让用户看到空图）。
	frameReadEnv = "EHOME_TELEMETRY_FRAME_READ"

	// frameDecodeMaxWindowEnv 是「短窗解码 / 长窗回落」的分界（默认 1h）。
	frameDecodeMaxWindowEnv = "EHOME_TELEMETRY_FRAME_DECODE_MAX_WINDOW"

	// defaultFrameDecodeMaxWindow 是分界默认值。
	//
	// 1 小时的依据：1 Hz 下 3,600 帧（MaxDecodeFrames=32,768 的 11%），解码耗时
	// 与 unified_data 的索引扫描同量级；再长就该走 rollup 或回落 SQL 聚合。
	defaultFrameDecodeMaxWindow = time.Hour

	// maxChangeDecodeWindow 是 change 模式**纯帧分段解码**的窗口上限（8 天，fallback 用）。
	//
	// ============ change 为什么需要单独的长窗通路（2026-11-10，task-5）============
	//
	// change 不能直接用 rollup 的聚合值：rollup payload 只有 avg/min/max/last/count，
	// 表达不了「首点 + 每次值变化」的翻转序列（用聚合值重建 0→1 跃变会算错触发次数，
	// 见 rollup_read.go 文件头的硬约束）。改造前它只能单次逐帧解码，受
	// frameDecodeMaxWindow（代码默认 1h / 生产 6h）与 MaxDecodeFrames(32768) 限制
	// ⇒ >6h 的 change 窗口**必然失败**（200 + 空数组，被前端当成「暂无数据」）。
	//
	// 现在有两条通路（见 change_longwindow.go）：
	//  1. 首选 **rollup 精确预筛 + 帧表精解候选分钟**：只解「这一分钟确实变过」的分钟
	//     （生产 7 天实测 2.3%），30/90 天也在秒级；窗口上限 maxChangeLongWindow(90d)。
	//  2. fallback **纯帧分段解码**（decodeFrameWindowSegmented）：预筛不可用时使用，
	//     窗口切成 ≤frameDecodeMaxWindow 的段逐段解码、拼接原始点、最后只归约一次。
	//
	// 这个常量（8 天）是 **fallback 的上限**，与 device_history_frames.go 的
	// maxFrameHistoryWindow 对齐（同一套分段机制；那里踩过「按段数定上限 = 上限随
	// 配置漂移」的坑，所以上限按时长定）。8 天 = 32 段 x 6h ≈ 69 万帧（1 Hz）
	// ≈ 9 s（12.8µs/帧），是 fallback 可接受的边界；30 天纯帧解码约 33 s，所以
	// >8 天的窗口必须依赖 rollup 预筛，预筛不可用时返回**诚实错误**。
	maxChangeDecodeWindow = maxFrameHistoryWindow
)

// frameReadEnabled / frameDecodeMaxWindow 是进程级状态（启动读一次 env，测试可覆盖）。
var (
	frameReadEnabled     = readBoolEnvDefault(frameReadEnv, true)
	frameDecodeMaxWindow = readDurationEnv(frameDecodeMaxWindowEnv, defaultFrameDecodeMaxWindow)
)

// SetFrameReadEnabled 覆写帧表读路径开关（测试/启动接线用）。
func SetFrameReadEnabled(enabled bool) { frameReadEnabled = enabled }

// FrameReadEnabled 报告帧表读路径是否启用。
func FrameReadEnabled() bool { return frameReadEnabled }

// SetFrameDecodeMaxWindow 覆写短窗分界（测试用）。
func SetFrameDecodeMaxWindow(d time.Duration) {
	if d > 0 {
		frameDecodeMaxWindow = d
	}
}

// FrameDecodeMaxWindow 返回当前短窗分界。
func FrameDecodeMaxWindow() time.Duration { return frameDecodeMaxWindow }

// readDurationEnv 读时长环境变量（支持 "1h" 与纯秒数），非法值回落默认。
func readDurationEnv(key string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	if secs, err := strconv.Atoi(raw); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	logger.Warnf("[frame-read] invalid %s=%q, fallback to %s", key, raw, def)
	return def
}

// ============ 数据源抽象 ============

// historySource 是「按 (device, category, window, maxPoints, mode) 取历史点」的
// 数据源接口。两个实现必须产出**逐值相等**的点集（除数据本身缺失外）。
//
// 为什么抽成接口而不是在 handler 里 if/else：
//
//	· 两个实现要能被**同一条用例**分别调用、逐值对拍（这是验收 §1 的唯一
//	  可信做法 —— 只测其中一个等于没测「相等」）；
//	· 将来 rollup 也可以作为一个实现接进来，选择逻辑集中在 dispatch 一处。
type historySource interface {
	// Name 用于日志与测试断言。
	Name() string
	// Points 返回聚合后的点集。返回空切片表示「本窗口内没有数据」，不是错误。
	Points(ctx context.Context, q historyPointQuery) ([]HistoryPoint, error)
}

// frameSource 读 telemetry_frame（解码 + Go 侧同口径分桶）。
type frameSource struct {
	db   *gorm.DB
	dict telemetry.SlotDictionary
}

func (s frameSource) Name() string { return "telemetry_frame" }

// Points 解码帧表并产出与**已删除的 SQL 路径**完全同口径的点集。
//
// 「同口径」现在由冻结基准守着（testdata/oracle_20261105.json +
// history_oracle_fixture_test.go）：那是 2026-11-05 生产 SQL 路径的输出快照。
// 原来的活对拍（TestFrameSourceEqualsUnifiedData）随 unified_data 一起退场了。
//
// 解码结果按 (device, window) 共享（见 decodeFrameWindow）：批端点一次请求要问
// 同一个窗口的 29 个 category，一个窗口只解一次。
func (s frameSource) Points(ctx context.Context, q historyPointQuery) ([]HistoryPoint, error) {
	// 批请求（BatchHint）用全字段 scope（一次服务 29 个 category），
	// 单 category 请求只解自己那一个字段。
	scope := q.Category
	if q.BatchHint {
		scope = frameDecodeAllFields
	}
	// ============ change 长窗：分段解码（2026-11-10）============
	//
	// 窗口 > frameDecodeMaxWindow 时把窗口切成 ≤frameDecodeMaxWindow 的段逐段解码，
	// 按时间序拼接**原始点**，最后只做一次 change 归约（下面的 aggregateFramePoints）。
	// 逐段归约会在每个接缝多出假翻转点，所以归约必须推迟到拼接之后。
	//
	// ⚠ 分段路径的 scope **必须是单字段**（q.Category），不能沿用批请求的 "*"：
	// 8 天 x 29 字段 ≈ 2000 万个点会直接把进程内存打爆。批请求里两个 change 类
	// （protection_status / fet_status）各自单字段解码一次，成本可接受（2 x 9 s 是
	// 最坏情况；诊断卡只请求其中一个）。
	segmented := q.Mode == AggModeChange && q.End.Sub(q.Start) > frameDecodeMaxWindow
	if segmented {
		scope = q.Category
		// ============ 长窗 change 首选 rollup 预筛（2026-11-10，task-5）============
		//
		// 纯帧分段解码 7 天 ≈ 60 万帧 ≈ 7.7 s、30 天 ≈ 33 s，不可接受。
		// change_longwindow.go 用 rollup 的分钟 Min/Max/Last 做**精确预筛**：
		// 只精解「这一分钟确实变过」的分钟（生产 7 天实测 2.3%），30/90 天降到
		// 秒级。预筛不可用（rollup 覆盖未验证 / 读取失败）时才回落纯帧分段解码。
		if pts, handled, perr := s.changeLongWindowPoints(ctx, q); perr != nil {
			return nil, perr
		} else if handled {
			return pts, nil
		}
		if q.End.Sub(q.Start) > maxChangeDecodeWindow {
			// 预筛不可用 + 超过纯帧分段解码上限：返回**诚实错误**，绝不返回空数组
			// （前端会把空数组渲染成「暂无数据」= 谎报没有保护触发）。
			return nil, fmt.Errorf(
				"change 长窗读路径不可用（rollup 覆盖未验证或读取失败），且窗口 %s 超过纯帧分段解码上限 %d 天；"+
					"请缩小窗口到 %d 天以内，或等待分钟聚合回填覆盖该窗口",
				q.End.Sub(q.Start).Truncate(time.Second),
				int(maxChangeDecodeWindow/(24*time.Hour)), int(maxChangeDecodeWindow/(24*time.Hour)))
		}
	}
	var (
		decoded map[string][]HistoryPoint
		err     error
	)
	if segmented {
		decoded, err = decodeFrameWindowSegmented(ctx, s.db, s.dict, q.EdgeDeviceID, q.Start, q.End, scope)
	} else {
		// 整窗路径：0 点 + 有帧 ⇒ 视为解码故障，必须回落（allowEmpty=false）。
		decoded, err = decodeFrameWindow(ctx, s.db, s.dict, q.EdgeDeviceID, q.Start, q.End, scope, false)
	}
	if err != nil {
		return nil, err
	}
	// ByName 已按时间升序（DecodeFrames 内部排序）。**必须复制**再交给聚合：
	// aggregateFramePoints 会就地 sort.SliceStable，而 ByName 是缓存里的共享
	// 切片 —— 不复制就是多个 goroutine 同时改同一底层数组（数据竞争）。
	cached := decoded[q.Category]
	raw := make([]HistoryPoint, len(cached))
	copy(raw, cached)
	// 走**与 SQL 完全同口径**的分桶（桶内最后值 / 变更点），见 aggregateFramePoints。
	return aggregateFramePoints(raw, q), nil
}

// aggregateFramePoints 把解码出的原始点按**与 SQL 路径完全一致的语义**聚合成图表点。
//
// 两个模式必须分别对齐 historyAvgSQLFor / historyChangeSQL：
//
//	· avg（D1 桶内最后值）：bucket = floor(epoch/W)*W，每桶保留**时间最大**的那个
//	  点，再裁掉桶起点早于 Start 的点（与 trimAvgPointsBeforeStart 同语义）；
//	· change：升序扫描，保留首点 + 每个「值变化」的点（含 1→0 的结束点），
//	  与 lag() ... IS DISTINCT FROM 完全等价。
//
// ============ 本实现不依赖「同 ts 多帧」的顺序（2026-11-08 记录）============
//
// 「桶内最后值」在**同 ts 多点**时会依赖输入顺序，而输入顺序来自
// DecodeFrames 的排序。这件事在生产上不成立，两层原因（代码 + 实测）：
//
//	① 代码：Seq 的 key 是 (deviceID, ts.UnixNano())，**不含 slot**
//	   （internal/telemetry/encoder.go:111-114 的 seqKey 定义、:257-259 的自增）：
//
//		type seqKey struct {
//			deviceID uint
//			ts       int64 // UnixNano
//		}
//		// seq：同一 (device, ts) 内的自增序号。
//		sk := seqKey{deviceID: deviceID, ts: at.UnixNano()}
//
//	   即 Seq 的语义是「**同一纳秒内第几帧**」。而生产写路径的 ts 来自
//	   time.Now()，纳秒精度下两帧同 ts 几乎不可能 ⇒ 跨 slot 同 ts 不会发生。
//
//	② 实测：ts_with_multiple_slots = 0（设备 15，2026-11-05 全天）。
//
// 若将来出现（例如 ts 被截断到毫秒），DecodeFrames 的排序键 (TS, Seq) **仍是
// 正确语义**（Seq 恰好就是「同一时间戳内第几帧」），但「桶内最后值」会真的依赖它
// —— 届时需要重新评估，而不是认为这里天然安全。
func aggregateFramePoints(raw []HistoryPoint, q historyPointQuery) []HistoryPoint {
	if len(raw) == 0 {
		return []HistoryPoint{}
	}
	// 解码顺序已是时间升序（DecodeFrames 内部排序），这里再稳一次以防调用方换了源。
	sort.SliceStable(raw, func(i, j int) bool { return raw[i].Timestamp.Before(raw[j].Timestamp) })

	// ============ change 语义：SQL 与 Go 的一处结构性差异（NaN，2026-11-08）============
	//
	// SQL: lag(v) IS DISTINCT FROM v —— 对 NaN 遵循 **SQL 标准**：
	//      `NaN IS NOT DISTINCT FROM NaN` ⇒ 连续两个 NaN 视为「未变化」，不输出第二个。
	// Go : raw[i].Value != prev —— 对 NaN 遵循 **IEEE754**：
	//      `NaN != NaN` 为 true ⇒ 连续两个 NaN 视为「变化」，**会输出第二个**。
	//
	// 所以「连续两帧都是 NaN」时两条路必然分歧（点数不同 ⇒ 逐点对拍必红）。
	// 这不是本实现能修的 bug，是 SQL 标准与 IEEE754 的语义差。
	//
	// 为什么现在不是问题（两层）：
	//
	//	① 帧表是**量化编码**（FieldSpec.Quantize 把 float 映射成整数码点），
	//	   NaN 物理上无法表达 —— round(NaN) 无定义，编码期就进不来；
	//	② 生产实测（2026-11-08）：unified_data 中 value=NaN 的行数 = 0，
	//	   ±Inf 也是 0，值域 -48.9 .. 4786.4。
	//
	// ⚠ 这是「运气」不是「结构保证」。若将来某个解析器产出 NaN 且写入路径未拦截：
	//   unified_data（float8 列）会存下来，帧表侧会在编码时被拒/丢弃 ——
	//   那时两条路必然不等，且**对拍用例会红**（这是好事：它会暴露问题）。
	//   但若那时对拍用例已被删除，这个差异就会无声无息。
	//
	// 参考：实测 PG 行为 `SELECT 'NaN'::float8 = 'NaN'::float8` = true；
	//      Go 侧 `math.NaN() != math.NaN()` = true。两边对同一个数学对象给出相反答案。
	if q.Mode == AggModeChange {
		out := make([]HistoryPoint, 0, len(raw))
		var prev float64
		for i := range raw {
			if i == 0 || raw[i].Value != prev {
				out = append(out, raw[i])
			}
			prev = raw[i].Value
		}
		return out
	}

	widthSeconds := bucketWidthSeconds(historyWindowSeconds(q.Start, q.End), q.MaxPoints)
	if widthSeconds <= 0 {
		// 不该发生（调用方先过 historyAggregationEligible），保守返回空让上层回落。
		return []HistoryPoint{}
	}
	// 桶号必须与 SQL 路径**逐位相同**：historyBucketExpr 用的是**绝对 epoch** 的
	// floor(epoch/W)*W（不是相对 Start 的偏移）。用相对偏移会让两条路径的桶边界
	// 错开最多 W 秒 —— 聚合值看起来"差不多"却永远对不上，而且很难看出是这里。
	lastByBucket := make(map[int64]float64, len(raw))
	order := make([]int64, 0, len(raw))
	for i := range raw {
		bucket := floorDiv(raw[i].Timestamp.Unix(), widthSeconds) * widthSeconds
		if _, seen := lastByBucket[bucket]; !seen {
			order = append(order, bucket)
		}
		// 升序遍历 ⇒ 后到者覆盖先到者 = 桶内最后值。
		lastByBucket[bucket] = raw[i].Value
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	out := make([]HistoryPoint, 0, len(order))
	for _, b := range order {
		// 输出的是**桶起点**（与 SQL 的 to_timestamp(floor(epoch/W)*W) 同形状），
		// 不是桶内最后一条数据的时间戳 —— 用数据时间戳会让两条路径的点集对不上。
		out = append(out, HistoryPoint{Timestamp: time.Unix(b, 0).UTC(), Value: lastByBucket[b]})
	}
	// 与 SQL 路径的 trimAvgPointsBeforeStart 同语义：桶起点早于 Start 的裁掉
	// （floor 对齐会把首桶推到 Start 之前最多 W-1 秒）。
	cut := 0
	for cut < len(out) && out[cut].Timestamp.Before(q.Start) {
		cut++
	}
	if cut > 0 {
		out = out[cut:]
	}
	return out
}

// floorDiv 是向下取整的整数除法（Go 的 / 向零截断，负数会出错）。
func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// ============ 槽字典注入 ============

// historyFrameDict 是帧表解码用的 slot 字典（由 main.go 在启动时注入）。
//
// 为什么注入而不是在 api 包内 NewDBSlots(db)：DBSlots 要全量 WarmUp（生产约
// 7,400 个 slot / 约 15 MB），每个请求建一份会反复全表扫描。main.go 已经有
// 一份与线上写路径**共用**的字典，注入进来才能保证「解码用的 slot 规格」与
// 「编码时用的」是同一份 —— 两套字典会解出错位的值（§8 的静默错数）。
var (
	historyFrameDictMu sync.RWMutex
	historyFrameDict   telemetry.SlotDictionary
)

// SetHistoryFrameDictionary 注入帧表解码用的 slot 字典（main.go 启动接线）。
// 传 nil 等价于禁用帧表读路径（覆盖判据会直接返回 false）。
func SetHistoryFrameDictionary(dict telemetry.SlotDictionary) {
	historyFrameDictMu.Lock()
	defer historyFrameDictMu.Unlock()
	historyFrameDict = dict
}

// lazyHistoryFrameDict 是**未注入时**的惰性字典（首次使用时从 db 装载并缓存）。
//
// 为什么需要它：main.go 的接线不在本任务的写范围内，但不能因此让帧表读路径
// 永远不可用（那就是「接了线却不通电」）。惰性装载与 main.go 的 telemetryDict
// 是**同一份数据**（都读 telemetry_slot 表），因此解码规格与编码侧一致。
// 注入优先：一旦 main.go 调了 SetHistoryFrameDictionary，就用注入的那份。
var (
	lazyDictMu   sync.Mutex
	lazyDict     telemetry.SlotDictionary
	lazyDictOnce sync.Once
)

// ResetLazyFrameDictionary 清空惰性字典（测试用）。
func ResetLazyFrameDictionary() {
	lazyDictMu.Lock()
	defer lazyDictMu.Unlock()
	lazyDict = nil
	lazyDictOnce = sync.Once{}
}

// frameDictionaryFor 返回可用于解码的 slot 字典：注入优先，否则惰性装载。
func frameDictionaryFor(db *gorm.DB) telemetry.SlotDictionary {
	if dict := HistoryFrameDictionary(); dict != nil {
		return dict
	}
	if db == nil {
		return nil
	}
	lazyDictOnce.Do(func() {
		dict := telemetry.NewDBSlots(db)
		if err := dict.WarmUp(); err != nil {
			logger.Warnf("[frame-read] lazy slot dictionary warm-up failed: %v", err)
			return
		}
		lazyDictMu.Lock()
		lazyDict = dict
		lazyDictMu.Unlock()
	})
	lazyDictMu.Lock()
	defer lazyDictMu.Unlock()
	return lazyDict
}

// HistoryFrameDictionary 返回当前注入的 slot 字典（可能为 nil）。
func HistoryFrameDictionary() telemetry.SlotDictionary {
	historyFrameDictMu.RLock()
	defer historyFrameDictMu.RUnlock()
	return historyFrameDict
}

// ============ 覆盖判据：已整体删除（2026-11-08，unified_data DROP 之后）============
//
// 删除清单（18 个符号）：
//
//	probeFrameCoverage / frameCovered / ResetFrameCoverageCache
//	frameCoverage / frameCoverageKey / frameCoverageEntry / frameCoverageFlight
//	frameCoverageCache / frameCoverageMu / frameCoverageTTL
//	frameCoverageHeadSlack / frameCoverageTailSlack
//
// ============ 为什么可以整体删（结构性论证）============
//
// 这个模块的**唯一目的**是回答一个问题：「帧表覆盖了这个窗口吗？不覆盖就回落
// unified_data」。它是一次**选源判据**，不是一个独立的业务功能。
//
// 硬切换之后：
//
//	· 回落目标（unified_data）已 DROP => 「不覆盖就回落」这个动作无处可去；
//	· selectHistorySource 已改成两档（帧表可用 / 不可用），不再询问覆盖情况；
//	· probeFrameCoverage 生产零调用者（grep 确认只剩自身定义）。
//
// ============ 为什么留着它比删掉更危险（这是关键）============
//
// 覆盖判据的**四种成因**需要的动作各不相同：
//
//	① 无帧         => 应返回空点集（确无数据）
//	② 帧数超上限   => 应报错（数据变了但读不出来，静默返回空是骗人）
//	③ 头部未覆盖   => 应只给窗口内的部分（前面那段确实没有）
//	④ 尾部滞后     => 应给已有数据（不是没数据，是还没落盘）
//
// 而一个布尔值只能表达「覆盖 / 不覆盖」，无法区分这四种 —— 压成布尔之后，
// 其中三种会变成**错误的「返回空」**：用户看到空图表，而后端一切正常。
//
// 这正是「前提消失后，依赖该前提的机制会从保护变成伤害」：
// 覆盖判据的全部合理性建立在「不覆盖时能回落到另一份数据」之上。
// 前提没了，它剩下的行为只有「决定要不要返回空」。
//
// ============ 保留的部分 ============
//
//	probeDeviceLastDataAt（1000 行附近）**必须留** —— 它现在只服务 rollup 读路径的
//	尾巴空帧诊断（rollup_read.go 的 tailEmptyLooksLikeLag）；那里的语义是
//	「尾巴空是滞后还是真没数据」，与 unified_data 无关。
//
// frameReadEligible 是纯函数形式的**前提**判据（不含 DB 查询，可独立单测）。
//
// 五条必须同时成立：
//  1. 开关打开；
//  2. 有注入的 slot 字典（没字典解不了码）；
//  3. 是聚合路径（avg/change）—— raw 模式要返回原始行，帧表给不了那个形状；
//  4. 设备维度有效；
//  5. 窗口有效且不超过短窗分界，同时不超过 DecodeRange 的硬上限。
func frameReadEligible(enabled bool, dict telemetry.SlotDictionary, q historyPointQuery) bool {
	if !enabled || dict == nil || q.EdgeDeviceID == 0 {
		return false
	}
	if q.Mode != AggModeAvg && q.Mode != AggModeChange {
		return false
	}
	if !q.End.After(q.Start) {
		return false
	}
	window := q.End.Sub(q.Start)
	if q.Mode == AggModeChange {
		// change 有独立的长窗通路（rollup 预筛 / 纯帧分段解码），见 change_longwindow.go。
		// 上限是 maxChangeLongWindow(90d)；超过它 → frameReadUnavailableError 给诚实错误。
		return window <= maxChangeLongWindow
	}
	// avg（连续量）：短窗逐帧解码；> frameDecodeMaxWindow 本该由 rollup 服务，
	// 不能在这里逐帧解（会撞 MaxDecodeFrames 并静默截断）。
	if window > frameDecodeMaxWindow || window > telemetry.MaxDecodeWindow {
		return false
	}
	return true
}

// selectHistorySource 选择本次请求的数据源。
// 硬切换后**帧表是唯一数据源，没有回落**（2026-11-08）。
//
// ============ 为什么不再判「帧表是否覆盖该窗口」============
//
// 覆盖判据存在的唯一理由是「不覆盖就回落 unified_data」。回落删掉之后，
// 它只会带来两种错误（下面两条）。⚠ 该判据已于 2026-11-08 整体删除，
// 见本文件前面的墓碑段；此处保留论证是因为它是「为什么不再判覆盖」的理由本身：
//
//	· frames > MaxDecodeFrames  => 被判「未覆盖」=> 返回空（把超限伪装成没数据）
//	· 窗口头部早于设备首帧     => 被判「未覆盖」=> 返回空（丢掉后面真实存在的数据）
//
// 两种都是「明明有数据却返回空」，比不做判断更糟。
//
// ============ 一条判据不该为不同情况做不同的事 ============
//
// 覆盖判据把 4 种成因（无帧 / 帧超限 / 头部未覆盖 / 尾部写入滞后）压成一个布尔，
// 就只能有**一个动作**。而没有回落之后，这 4 种需要的动作各不相同：
//
//	无帧         => 返回空点集（正常：设备没上报）
//	帧超限       => 报错（用户可缩小窗口）  ← decodeFrameWindowNow 的 FramesTruncated 检查
//	头部未覆盖   => 解码只给窗口内的点（本来就是）
//	尾部写入滞后 => 返回已有数据（晚 2 秒，可接受）
//
// 压成布尔之后，你不是「简化了」，你是「把本该区分的四种情况强行统一成一个动作，
// 然后其中三种都错了」。
//
// 真正该保留的失败信号是「解码失败」（有帧但 0 点解出，字典版本不匹配 / payload 全坏），
// 那由 decodeFrameWindowNow 的 allowEmpty=false 判据负责，与覆盖无关。
func selectHistorySource(ctx context.Context, db *gorm.DB, q historyPointQuery) (historySource, error) {
	_ = ctx // 保留签名：调用方可能已有 ctx（未来若要做 ctx 相关的选源逻辑不用改签名）
	dict := HistoryFrameDictionary()
	if !frameReadEligible(frameReadEnabled, dict, q) {
		return nil, frameReadUnavailableError(dict, q)
	}
	return frameSource{db: db, dict: dict}, nil
}

// frameReadUnavailableError 把 frameReadEligible=false 的**六种成因分别说清**。
//
// 为什么不用一个笼统的 "frame read unavailable"：六种成因的处置完全不同 ——
// 配置错要改环境变量、启动未完成要等、调用方用错要改代码、窗口超限要缩小窗口
// （avg 与 change 的上限不同，见各自分支）。
// 一句笼统的话会让排障的人从零开始猜。
func frameReadUnavailableError(dict telemetry.SlotDictionary, q historyPointQuery) error {
	switch {
	case !frameReadEnabled:
		return fmt.Errorf("帧表读路径被关闭（EHOME_TELEMETRY_FRAME_READ=false）；硬切换后它是唯一数据源，" +
			"关闭即意味着所有历史图表不可用 —— 请打开该开关并重启")
	case dict == nil:
		return fmt.Errorf("帧表 slot 字典尚未就绪（启动预热未完成）；请等待服务启动完成后重试")
	case q.EdgeDeviceID == 0:
		return fmt.Errorf("请求缺少设备维度（edge_device_id=0）；聚合历史必须指定设备")
	case q.Mode != AggModeAvg && q.Mode != AggModeChange:
		return fmt.Errorf("帧表读路径只服务 avg/change 聚合模式（请求 mode=%q）；raw 原始行请走原始数据接口", q.Mode)
	case !q.End.After(q.Start):
		return fmt.Errorf("时间窗口无效（start=%s end=%s）；end 必须晚于 start",
			q.Start.Format(time.RFC3339), q.End.Format(time.RFC3339))
	case q.Mode == AggModeChange && q.End.Sub(q.Start) > maxChangeLongWindow:
		// change 的窗口上限（90 天）比 avg 的短窗分界宽松得多：它有独立的长窗通路
		// （rollup 精确预筛 + 帧表精解，见 change_longwindow.go）。超过 90 天没有业务
		// 场景（诊断卡最大档位就是 90 天），且 rollup 分区保留期之外也读不到分钟桶。
		return fmt.Errorf("change 模式（翻转序列）窗口上限 %d 天；请求窗口 %s 超过该上限。"+
			"change 语义必须逐帧重建翻转序列（不能走分钟聚合，否则触发次数会算错）；"+
			"请把窗口缩小到 %d 天以内",
			int(maxChangeLongWindow/(24*time.Hour)), q.End.Sub(q.Start).Truncate(time.Second),
			int(maxChangeLongWindow/(24*time.Hour)))
	default:
		// 「窗口超过帧表单次解码上限」：请求窗口超过 frameDecodeMaxWindow（代码默认 1h / 生产 6h）。
		//
		// 这是**现实约束**而非故障：更长的 avg 窗口本该由 rollup 承担。走到这里说明
		// rollup 也没能服务这个窗口，所以确实答不了 —— 返回错误比返回空诚实。
		return fmt.Errorf("查询窗口 %s 超过帧表单次解码上限 %s，且该窗口尚未被分钟聚合覆盖；"+
			"请缩小到 %s 以内（例如先看近 1 小时），或等待分钟聚合回填推进到该窗口",
			q.End.Sub(q.Start).Truncate(time.Second), frameDecodeMaxWindow, frameDecodeMaxWindow)
	}
}

// queryHistoryPointsWithFallback 是对外的统一读入口：先选源，失败再回落。
//
// **第二道回落**（选源成功但解码失败时）：帧表可能因为 slot 字典落后（新 slot
// 尚未 WarmUp）、payload 损坏等原因解不出来。这时必须回落，而不是把空点集
// 当结果返回 —— 用户看到空图比看到略慢的正确图糟糕得多。
// ============ 解码失败就是失败，不再回落（2026-11-08 行为变更）============
//
// 原实现：帧表解码失败 => 回落 unified_data。目的「宁可慢也给对图」。
//
// 硬切换后回落目标不存在（DROP unified_data），且更根本的是：**静默回落会
// 把「存储故障」伪装成「没数据」**。用户看到空图/旧图，不会报障；运维不知道
// 存储坏了。返回 500 会立刻被发现，那才是对的。
//
// 这是一条**行为变更**，已写进 docs/规范/unified_data硬切换-读路径契约变更-2026-11-08.md：
//
//	帧表解码失败 => 500（不再是静默回落）
func queryHistoryPointsWithFallback(ctx context.Context, db *gorm.DB, q historyPointQuery) ([]HistoryPoint, error) {
	src, err := selectHistorySource(ctx, db, q)
	if err != nil {
		return nil, err
	}
	return src.Points(ctx, q)
}

// ============ 解码结果共享（批请求的关键优化）============

// frameDecodeCache 缓存「某设备某窗口的全部字段解码结果」，供同一批请求内的
// 多个 category 复用。
//
// ============ 为什么这个缓存是必需的，而不只是"锦上添花" ============
//
// 生产图表请求走的是 **batch 端点**（/unified-data/historical-batch）：一个
// device14 的 1 小时窗口要同时问 29 个 category。若每个 category 各自解码，
// 就是 29 次「取 3,600 帧 + 解 3,600 帧」—— 实测单次 25 ms ⇒ 首屏 725 ms。
//
// 而帧表的物理形态决定了**一次解码能服务全部字段**：一帧一行，payload 里是
// 整个 slot 的字段。DecodeFrames 的 Fields 过滤只影响**输出**，不影响解码
// 成本（它本来就要还原整个 slot 状态才能推进增量链）。所以：
//
//	· 一个窗口只解码一次 ⇒ 全部 29 个 category 共享；
//	· 批请求的总帧读取成本从 29×3,600 行降到 3,600 行。
//
// 这也正是帧表相对 unified_data 的**结构性优势**：unified_data 一字段一行，
// 29 个 category 必须扫 29 次（各自走不同的索引范围）；帧表一帧一行，扫一次。

const (
	// frameDecodeCacheTTL 是解码结果的存活时间。
	//
	// 15 s 的依据：一次批请求的全部 goroutine 在毫秒级内完成（必然命中）；
	// 相邻两次轮询若窗口不同则不命中、相同则复用。太长会让「回填刚推进」的
	// 窗口继续走旧结果，15 s 是轮询周期与新鲜度的折中。
	frameDecodeCacheTTL = 15 * time.Second
	// frameDecodeCacheLimit 是缓存条目上限。超限整体清空（缓存语义，清了只是
	// 下次重新解码）。正常负载下通常只有最近一两个窗口。
	frameDecodeCacheLimit = 64

	// frameDecodeAllFields 是 frameDecodeKey.Scope 的"全部字段"标记。
	frameDecodeAllFields = "*"
)

// frameDecodeKey 是一次解码的缓存键。窗口按 Unix 秒对齐 —— 同一批请求的多个
// category 用的是同一组 start/end（同一个 HTTP 查询参数），因此必然命中。
type frameDecodeKey struct {
	DeviceID uint
	// AllowEmpty 参与缓存键：同一 (device, from, to, scope) 在「允许空集」与
	// 「空集视为故障」两种语义下**结果不同**（一个返回空 map、一个返回 error）。
	// 不参与键会让尾巴路径缓存的空结果被整窗路径复用 ⇒ 整窗静默返回空点集。
	AllowEmpty bool
	From       int64
	To         int64
	// Scope 是**解码范围**：
	//
	//	· "*" = 该 slot 的全部字段（批端点用：一次解码服务 29 个 category）；
	//	· 字段名 = 只解这一个字段（单 category 请求用：29 字段全解是 29 倍浪费）。
	//
	// 为什么必须进 key 而不是"统一全解"：实测生产形状下，全解 104,400 个点要
	// ~28 ms，而单字段只解 3,600 个点只要 ~5 ms。单字段请求走全解是 5 倍浪费。
	Scope string
}

// frameDecodeEntry 是一次解码的结果：**全部字段**的原始点（未分桶）。
//
// 保存未分桶的原始点而不是每个 category 的聚合结果，是因为聚合参数（MaxPoints）
// 在同一批请求里相同、但 category 不同；保存原始点让任意 category 都能复用。
type frameDecodeEntry struct {
	// ByName 是**按字段名分组**的原始点（已按时间升序）。
	//
	// 为什么预先分组而不是存一个大切片让每个 category 各自过滤：批请求有 29 个
	// category，大切片方案每次都要扫全部点（29 × 10 万次比较）；分组后每个
	// category 是一次 map 查找。
	ByName   map[string][]HistoryPoint
	ExpireAt time.Time
}

// frameFlight 是一次进行中的解码（单飞语义）。
//
// ============ 为什么必须有单飞，而不只是缓存 ============
//
// 批端点的 29 个 goroutine **同时**进入本函数。只有「查缓存」的话，它们会全部
// 未命中，然后**各解码一遍** —— 实测批请求 297 ms（unified 的 3.93 倍），带宽
// 与 CPU 都被浪费 29 次。单飞让后来者等第一次的结果：只需解码一次。
type frameFlight struct {
	done   chan struct{}
	byName map[string][]HistoryPoint
	err    error
}

var (
	frameDecodeMu    sync.Mutex
	frameDecodeCache = make(map[frameDecodeKey]frameDecodeEntry)
	frameDecodeFlig  = make(map[frameDecodeKey]*frameFlight)
)

// ResetFrameDecodeCache 清空解码缓存与在途单飞（测试用）。
func ResetFrameDecodeCache() {
	frameDecodeMu.Lock()
	defer frameDecodeMu.Unlock()
	frameDecodeCache = make(map[frameDecodeKey]frameDecodeEntry)
	frameDecodeFlig = make(map[frameDecodeKey]*frameFlight)
}

// decodeFrameWindow 取得（或解码并缓存）该设备该窗口的**全部字段**原始点。
//
// 三层：进程内缓存 → 单飞（进行中的解码）→ 真正解码。
func decodeFrameWindow(ctx context.Context, db *gorm.DB, dict telemetry.SlotDictionary, deviceID uint, from, to time.Time, scope string, allowEmpty bool) (map[string][]HistoryPoint, error) {
	key := frameDecodeKey{DeviceID: deviceID, From: from.Unix(), To: to.Unix(), Scope: scope, AllowEmpty: allowEmpty}
	now := time.Now()

	frameDecodeMu.Lock()
	if ent, ok := frameDecodeCache[key]; ok && now.Before(ent.ExpireAt) {
		frameDecodeMu.Unlock()
		return ent.ByName, nil
	}
	if fl, ok := frameDecodeFlig[key]; ok {
		frameDecodeMu.Unlock()
		select {
		case <-fl.done:
			return fl.byName, fl.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	fl := &frameFlight{done: make(chan struct{})}
	frameDecodeFlig[key] = fl
	frameDecodeMu.Unlock()

	// 解码在后台 goroutine 里做：等待者的 ctx 取消不应影响解码本身 ——
	// 否则「第一个请求超时」会把其余 28 个一起弄失败。
	go func() {
		defer close(fl.done)
		byName, err := decodeFrameWindowNow(db, dict, deviceID, from, to, scope, allowEmpty)
		fl.byName, fl.err = byName, err
		frameDecodeMu.Lock()
		delete(frameDecodeFlig, key)
		if err == nil {
			frameDecodeCache[key] = frameDecodeEntry{ByName: byName, ExpireAt: time.Now().Add(frameDecodeCacheTTL)}
			if len(frameDecodeCache) > frameDecodeCacheLimit {
				frameDecodeCache = make(map[frameDecodeKey]frameDecodeEntry)
			}
		}
		frameDecodeMu.Unlock()
	}()

	select {
	case <-fl.done:
		return fl.byName, fl.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// decodeFrameWindowSegmented 把 [from, to) 切成 ≤frameDecodeMaxWindow 的段，逐段解码后
// 按时间序拼接**原始点**（未归约）。返回的 map[字段名] 内的点集仍按时间升序。
//
// ============ 为什么必须「先拼接、后归约」（2026-11-10，task-5）============
//
// change 归约 = 首点 + 每个「值变化」的点，它依赖**相邻原始点**的比较：
//
//	逐段归约再拼接 => 每段都会各自输出「本段首点」，即使它与上一段末点值相同
//	                  => 每个接缝多 1 个假翻转点（前端会把触发次数算多）；
//	先拼接原始点 => 归约只看一次完整序列 => 与单段解码**逐点相同**（有单测钉住）。
//
// 这是本函数存在的唯一理由：它是 change 长窗在 rollup 预筛不可用时的 fallback
// （首选路径见 change_longwindow.go）。
//
// ============ 每段 allowEmpty=true 的理由 ============
//
// 单段可能合法地没有任何点（设备在段内离线 / 该字段在段内无采样）。整窗的
// 「有帧但 0 点 = 解码坏了」判据放在**调用方拼接之后**做（见 frameSource.Points），
// 不能在段内做：段内做会把「这段没数据」误判成故障，整窗 500。
//
// 段边界是**严格半开** [segStart, segEnd)，与 decodeFrameWindow 内部
// ts >= from && ts < to 的裁剪口径一致 ⇒ 相邻段既不重叠也不留缝。
func decodeFrameWindowSegmented(
	ctx context.Context, db *gorm.DB, dict telemetry.SlotDictionary,
	deviceID uint, from, to time.Time, scope string,
) (map[string][]HistoryPoint, error) {
	if !to.After(from) {
		return nil, fmt.Errorf("segmented frame decode: empty range [%s, %s)",
			from.Format(time.RFC3339), to.Format(time.RFC3339))
	}
	out := make(map[string][]HistoryPoint, 32)
	for segStart := from; segStart.Before(to); {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		segEnd := segStart.Add(frameDecodeMaxWindow)
		if segEnd.After(to) {
			segEnd = to
		}
		part, err := decodeFrameWindow(ctx, db, dict, deviceID, segStart, segEnd, scope, true)
		if err != nil {
			return nil, fmt.Errorf("segmented frame decode [%s, %s): %w",
				segStart.Format(time.RFC3339), segEnd.Format(time.RFC3339), err)
		}
		for name, pts := range part {
			out[name] = append(out[name], pts...)
		}
		segStart = segEnd
	}
	return out, nil
}

// decodeHookForTest 是**测试专用**的解码次数计数器（生产恒为 nil）。
//
// 为什么需要它：判定「批请求共享一次解码」必须能**数**解码发生了几次。
// 只看耗时会被机器抖动干扰；数次数是确定性的。
var decodeHookForTest *int64

// decodeFrameWindowNow 是真正的一次解码（调用方保证不会有并发重复）。
// maxSegmentDecodeTime 是**单段**帧解码的硬超时（2026-11-08）。
//
// 为什么需要它：解码跑在 singleflight 的后台 goroutine 里，用的是 Background
// 而不是请求 ctx（否则首个等待者超时会连累并发请求）。但纯 Background 意味着
// 段耗时**无上界** —— 帧表 IO 抖动时，客户端断开后解码还在跑，且不知道跑多久。
//
// 这个常量把 B 方案从「无界」变成「有界」。
//
// 取值：6h 窗口 = 21,600 帧；生产实测 24h（4 段）解码约 1.85s => 单段约 460ms。
// 5s = 10 倍余量。超过它说明 IO/内存异常，放弃这一段比无限期占资源更好。
const maxSegmentDecodeTime = 5 * time.Second

func decodeFrameWindowNow(db *gorm.DB, dict telemetry.SlotDictionary, deviceID uint, from, to time.Time, scope string, allowEmpty bool) (map[string][]HistoryPoint, error) {
	// scope == "*" ⇒ 传 nil（解全部字段）；否则只解那一个字段。
	//
	// DecodeFrames 的 Fields 过滤只影响**产出**（不改变解码遍历），因此单字段
	// 解码的真实收益是"少产出 28/29 的 Point 对象"，那正是成本大头。
	var fields []string
	if scope != frameDecodeAllFields {
		fields = []string{scope}
	}
	// ============ 段内超时：让「后台解码」有界而不是无界（2026-11-08）============
	//
	// 这里**不能**用请求 ctx：解码跑在 singleflight 的后台 goroutine 里，
	// 若第一个等待者超时就中止解码，其余并发等同一段的请求会一起失败
	// （见下面 spawnDecode 的注释）。
	//
	// 但也**不能**用纯 context.Background()：那样段耗时无上界 ——
	// 帧表 IO 抖动时，客户端早已断开解码还在跑，且不知道要跑多久。
	//
	// 权衡结果：Background + **单段超时**。超时的语义是「这一段真的卡住了」，
	// 不是「某个请求被取消了」—— 所以不会连累并发等待者。
	//
	// 取值依据：6h 窗口 = 21,600 帧；生产实测 24h（4 段）解码约 1.85s ⇒ 单段约 460ms。
	// 5s 是 10 倍余量。超过它说明 IO/内存异常，主动放弃这一段（返回错误让上层回落），
	// 比无限期占着 CPU/内存更好。
	decodeCtx, cancelDecode := context.WithTimeout(context.Background(), maxSegmentDecodeTime)
	defer cancelDecode()
	points, stats, err := telemetry.DecodeRangeAnchored(decodeCtx, db, dict, deviceID, from, to, fields)
	if decodeHookForTest != nil {
		atomic.AddInt64(decodeHookForTest, 1)
	}
	if err != nil {
		return nil, err
	}
	// 「解出 0 个点」不等于「这段时间没数据」—— 见 frameSource.Points 的注释。
	//
	// ==================== allowEmpty 的适用边界（2026-11-07 P0）====================
	//
	// 这条判据在**整窗路径**上是必需的：「有帧但一个点都解不出」= 解码坏了（字典版本
	// 不匹配 / slot 丢失 / 帧全坏），此时必须回落，绝不能返回空点集（用户看到空图）。
	//
	// 但 **rollup 的实时尾巴**不能这么判。尾巴窗口是 [now-120s, now)，只有 2 分钟：
	//
	//
	//	· 告警类字段（alarm_*）只在状态翻转时才上报 ⇒ 这 2 分钟很可能一个采样都没有；
	//	· 低频字段（额定容量、固件版本之类）可能几小时才报一次；
	//	· 值未变 + 增量帧不产点，也可能让某个字段在这段为空。
	//
	// 这些都是**完全正常的状态**，不是解码故障。若按整窗判据报 error，尾巴就取不到，
	// 而「尾巴取不到 ⇒ 整窗回落」⇒ 长窗整段回落到 unified_data。删表之后的表现不是
	// 「空页」那么明显，而是**静默截断**：
	//
	//	收藏夹里的 30 天图：grid_voltage 正常显示 30 天，
	//	                    pv1_voltage 只剩最近 7 天，没有任何报错。
	//
	// 这比空页更难发现 —— 用户以为「这个字段历史就是少」。
	//
	// 所以 allowEmpty 只由**尾巴路径**传 true；整窗路径保持不变（保守）。
	// ============ 截断必须报错，不能静默给残缺数据（2026-11-08，P0）============
	//
	// 窗口内帧数超过 MaxDecodeFrames（默认 32768）时，decodeRangeBounded 会
	// **保留最新、丢弃最旧**。这是「我们主动丢数据」，与「断链」（数据形态，正常）
	// 是两件事 —— 所以它有自己的计数 FramesTruncated，不再混进 BrokenChain。
	//
	// ============ 必须报错而不是返回空，也不是静默返回 ✅ ============
	//
	//	返回空   => 用户会问「怎么没数据」                => 会去查
	//	静默残缺 => 用户看到一条到某时刻为止的图，**不会问** => 永远不查
	//
	// 后者更危险：它看起来完全正常。实测形态举例 —— 30 天图表画到第 22 天就没了，
	// 没有任何报错，用户以为「这个字段历史就是这么短」。
	//
	// 报错时给出**可执行信息**（缩小窗口 / 提高上限），用户缩小窗口后就能拿到正确数据。
	if stats.FramesTruncated > 0 {
		return nil, fmt.Errorf("frame decode truncated: %d oldest frames dropped "+
			"(window has more than %d frames, limit=%d); 缩小时间窗，或提高 EHOME_TELEMETRY_DECODE_MAX_FRAMES",
			stats.FramesTruncated, stats.FramesSeen+stats.FramesTruncated, telemetry.MaxDecodeFrames)
	}
	if !allowEmpty && len(points) == 0 && stats.FramesSeen > 0 {
		return nil, fmt.Errorf("frame decode produced no points: seen=%d decoded=%d unknown_slot=%d corrupt=%d broken_chain=%d truncated=%d",
			stats.FramesSeen, stats.FramesDecoded, stats.FramesSkippedUnknownSlot,
			stats.FramesSkippedCorrupt, stats.FramesSkippedBrokenChain, stats.FramesTruncated)
	}
	out := make(map[string][]HistoryPoint, 32)
	for i := range points {
		out[points[i].Name] = append(out[points[i].Name], HistoryPoint{
			Timestamp: points[i].TS.UTC(),
			Value:     points[i].Value,
		})
	}
	return out, nil
}

// probeDeviceLastDataAt 读设备最后一次成功上报的时刻（不知道就返回零值）。
//
// 为什么容忍失败：帧表读路径的正确性不该被"设备行读不到"拖垮 —— 读不到时退回
// 保守的 end 锚点（行为与改造前一致），而不是让整个覆盖探测失败。
//
// 为什么单独一次查询而不是并进上面的 count/min/max：那条 SQL 已经是对分区表的
// 聚合扫描，再挂一个子查询会让执行计划变复杂；而这里是主键点查（O(1)），
// 且覆盖探测结果有 30 s 缓存，代价可忽略。
func probeDeviceLastDataAt(ctx context.Context, db *gorm.DB, deviceID uint) time.Time {
	var row struct {
		LastDataAt *time.Time
	}
	if err := db.WithContext(ctx).Raw(
		"SELECT last_data_at FROM edge_devices WHERE id = ?", deviceID).Scan(&row).Error; err != nil {
		return time.Time{}
	}
	if row.LastDataAt == nil {
		return time.Time{}
	}
	return row.LastDataAt.UTC()
}
