package datalifecycle

// rollup_backfill_frames.go — 回填的**帧表聚合源**（P0 残留缺口修复，2026-11-07）。
//
// ============ 缺口的形态 ============
//
// discoverSource 脱钩之后，枚举能推出「昨天」了 —— 但 aggregateBatch 仍然只读
// unified_data。于是停写之后：
//
//	枚举出 (device, 昨天) -> 聚合读 unified_data 得 0 行 -> buckets=0
//	-> MarkRollupCoverage(missing)
//
// 而 **missing 是粘性的**：unitAlreadyDone 认为它已验证完成，永不重试。
// live collector 漏掉的分钟因此**永久补不回来**，「次日自愈」这个性质消失了。
//
// 这比图表空隐蔽得多：读路径把 missing 当 verified -> 图照常出 -> 表面一切正常。
//
// ============ 修法：按**日期**选源，不是按「本次查询返回 0 行」============
//
//	该 (device, day) 在 unified_data 里有行  => 用 unified_data 聚合（行为与现在完全一致）
//	该日 unified_data 无行                   => 用帧表聚合
//
// 为什么按日期而不是按返回结果：按结果的话，同一天可能因查询抖动/边界差异时有时无，
// 导致同一天被两种口径各写一次；按日期则来源是**确定的、可复现的**，还能写进 coverage。
//
// 为什么过渡期不用帧表：帧表聚合的 avg 是**全采样**口径，unified_data 是 L1 收窄后口径。
// 无差别切换会让历史 rollup 的**数值变化** —— 而「图表数字变了」这类回归无法自动断言。
// 所以过渡期保持 unified_data，停写后自然切到帧表，**历史零变化**。
//
// ============ 为什么必须按 batch 解码，不能按天 ============
//
// MaxDecodeFrames = 32768（硬编码），而单设备单日 56,125~86,342 帧。
// decodeRangeBounded 超限时**只保留最后 32768 帧且不返回 error** ——
// 按天解码会静默丢掉一天的前半段，rollup 照样写出来。这是最危险的一类无界静默截断。
// 所以解码窗口跟着回填批次走（BatchSeconds 默认 3600 => 约 3600 帧/批，安全）。

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// FramePoint 是帧解码产出的一个 (时刻, 字段, 值)。
//
// 为什么要这个中性类型而不是直接 import telemetry.Point：
// **internal/telemetry 的测试包 import 了 datalifecycle**（pg_partition_test.go），
// 所以 datalifecycle 的生产代码一旦 import telemetry 就是 test-only 的 import cycle。
// 用注入 + 中性类型把依赖方向掰回来：datalifecycle 不认识 telemetry，由 main.go 接线。
type FramePoint struct {
	TS    time.Time
	Name  string
	Value float64
}

// frameDecoder 由 main.go 注入（见 SetFrameDecoder）。nil 表示帧源不可用。
var frameDecoder func(ctx context.Context, db *gorm.DB, deviceID uint, from, to time.Time) ([]FramePoint, error)

// SetFrameDecoder 注入帧解码实现（main.go 接线；返回 restore 便于测试）。
func SetFrameDecoder(fn func(ctx context.Context, db *gorm.DB, deviceID uint, from, to time.Time) ([]FramePoint, error)) (restore func()) {
	prev := frameDecoder
	frameDecoder = fn
	return func() { frameDecoder = prev }
}

// rollupSourceUnified / rollupSourceFrames 是 coverage 元数据里的来源标记。
//
// 为什么要记：将来排查「为什么这天的 avg 比那天高」时，第一件事就是问
// 「它从哪个源算的」—— 两个口径（全采样 vs 收窄后）本来就不等值。
const (
	RollupSourceUnified = "unified"
	RollupSourceFrames  = "frames"
)

// framesDecoderMaxFrames 是单次帧解码的帧数护栏。
//
// 与 telemetry.MaxDecodeFrames 同源（32768），**显式写在这里**是为了让人看到
// 「按批解码」不是随便定的。
//
// 不能直接引用 telemetry.MaxDecodeFrames：会引入 test-only import cycle（见文件头）。
const framesDecoderMaxFrames = 32768

// framesBatchLimit 是单次帧聚合允许的最大窗口。
//
// 超过它就把窗口切片再解码 —— 这是防「有人把 BatchSeconds 调成一天」的护栏：
// 那样会撞上 MaxDecodeFrames 的截断（只留最后 32768 帧）。
//
// ============ 它是 cap，不是步长 ============
//
//	外层（rollup_backfill.go）: for t := dayStart; ...; t = t.Add(batch)
//	内层（本文件）:           for t := from; t.Before(to); t = t.Add(framesBatchLimit)
//	                          segTo := min(t+framesBatchLimit, to)   ← 被外层 to 卡住
//
// 所以 **实际段宽 = min(framesBatchLimit, BatchSeconds)**。
// BatchSeconds 默认 3600（见 defaultRollupBackfillBatchSeconds）⇒ 默认段宽 = 1h
// ⇒ 1h × 1fps = 3,600 帧，上限 32,768 ⇒ **余量 9 倍**。
//
// 这个常量只在「有人把 BatchSeconds 设成 > 1h」时才起作用。
// 把它从 6h 收到 1h（2026-11-08）是**纯防御性收紧**：
// 最坏情况从「BatchSeconds=24h ⇒ 段宽 6h ⇒ 21,600 帧（余量 1.5 倍）」
// 变成「段宽 1h ⇒ 3,600 帧（余量 9 倍）」。**默认行为完全不变**。
//
// ⚠ 段宽多大都**不能**替代截断检查：10 fps 的设备（interval_ms=100 合法）在 1h 段内
// 就是 36,000 帧 > 32,768。main.go 注入的解码器必须显式检查 FramesTruncated 并报错。
//
// （2026-11-08 修正：我先把它误读成「实现用 6h、注释算 1h」，据此写过一版
//   声称「注释和实现不一致」的注释。核代码后确认**实现也是 1h** —— cap 被外层
//   batch 卡住。留此记录，避免下次再犯「把 cap 当 step」。）
const framesBatchLimit = time.Hour

// unifiedDataTablePresent 探测 unified_data 表是否存在，结果**缓存到 runner 上**。
//
// ============ 为什么必须用「表存在性」而不是「查询报错」来判定（2026-11-08）============
//
// DROP unified_data 之后，逐日探测的那条 SQL 会报 `relation "unified_data" does not exist`。
// 最省事的写法是 `if err != nil { return false, nil }` —— **那是错的**：
//
//	表不存在   => 正常终态，应走帧表源
//	连接断了   => 异常，必须让回填失败并报警
//
// 两者都会被 err != nil 命中，混为一谈的后果是：数据库真挂了你却在跑「帧表源」
// 并一路标 missing（**missing 是粘性的**，等于把一次故障写成永久无数据）。
//
// 所以用表存在性区分：这是**结构状态**，不是错误分类。
func (r *rollupBackfillRunner) unifiedDataTablePresent() bool {
	r.unifiedMu.Lock()
	defer r.unifiedMu.Unlock()
	if r.unifiedPresent != nil {
		return *r.unifiedPresent
	}
	present := r.db != nil && r.db.Migrator().HasTable("unified_data")
	r.unifiedPresent = &present
	return present
}

// dayHasUnifiedRows 判断该 (device, day) 在 unified_data 里是否有行。
//
// 这是「按日期选源」的判据。用 LIMIT 1 语义（count 到 1 就够），走
// (device_id, timestamp) 索引，单日范围扫描。
//
// 前置条件：调用方必须**先**确认 unified_data 表存在（unifiedDataTablePresent）。
// 表不存在时本函数会返回 error —— 那是刻意的：这是「不该被调用」，不是「没有行」。
func (r *rollupBackfillRunner) dayHasUnifiedRows(ctx context.Context, deviceID uint, dayStart, dayEnd time.Time) (bool, error) {
	var n int64
	err := r.db.WithContext(ctx).Raw(
		"SELECT count(*) FROM (SELECT 1 FROM unified_data "+
			"WHERE device_id = ? AND \"timestamp\" >= ? AND \"timestamp\" < ? LIMIT 1) t",
		deviceID, dayStart.UTC(), dayEnd.UTC()).Scan(&n).Error
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// aggregateBatchFromFrames 用帧表聚合一个批次内的分钟统计。
//
// 返回结构与 aggregateBatch 完全一致（rollupBackfillAggRow），这样上层
// 「按 bucket 累积 -> 计数 -> 写库 -> 标 coverage」的流程两种来源共用一份。
func (r *rollupBackfillRunner) aggregateBatchFromFrames(
	ctx context.Context, deviceID uint, from, to time.Time,
) ([]rollupBackfillAggRow, error) {
	if frameDecoder == nil {
		return nil, nil
	}
	// 统计累加器：bucket -> sensor -> 累加态
	type acc struct {
		sum   float64
		min   float64
		max   float64
		last  float64
		lastT time.Time
		count int64
	}
	byBucket := make(map[int64]map[string]*acc, 64)

	for t := from; t.Before(to); t = t.Add(framesBatchLimit) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		segTo := t.Add(framesBatchLimit)
		if segTo.After(to) {
			segTo = to
		}
		// 按段解码：每段独立锚定到最近关键帧，状态自足。
		//
		// 段窗口 = min(framesBatchLimit, 外层 batch) <= framesBatchLimit（现 1h）
		// => 默认 1h × 1fps = 3,600 帧，相对 MaxDecodeFrames(32,768) 余量 9 倍
		//    （按 1 fps 估；更快的设备见 framesBatchLimit 的注释）。
		//
		// ⚠ 这里的「余量」是**估算**，不是保证 —— 10 fps 的设备（interval_ms=100 合法）
		// 在 1h 段内就是 36,000 帧 > 32,768。真实保证来自 main.go 注入的解码器里
		// 对 stats.FramesTruncated 的显式检查。
		// 解码实现由 main.go 注入（见 SetFrameDecoder），datalifecycle 不认识 telemetry。
		points, err := frameDecoder(ctx, r.db, deviceID, t.UTC(), segTo.UTC())
		if err != nil {
			return nil, err
		}
		for _, pt := range points {
			bucket := pt.TS.UTC().Unix() / 60 * 60
			bg := byBucket[bucket]
			if bg == nil {
				bg = make(map[string]*acc, 32)
				byBucket[bucket] = bg
			}
			a := bg[pt.Name]
			if a == nil {
				a = &acc{min: pt.Value, max: pt.Value, last: pt.Value, lastT: pt.TS}
				bg[pt.Name] = a
			}
			a.sum += pt.Value
			a.count++
			if pt.Value < a.min {
				a.min = pt.Value
			}
			if pt.Value > a.max {
				a.max = pt.Value
			}
			// 桶内最后值 = 时间最晚的那个（points 已按时间升序，但同桶内仍显式比较，
			// 不依赖解码器的排序保证 —— 这条不变量是 D1 口径的基础）。
			if !pt.TS.Before(a.lastT) {
				a.last = pt.Value
				a.lastT = pt.TS
			}
		}
	}

	out := make([]rollupBackfillAggRow, 0, len(byBucket)*8)
	for bucket, fields := range byBucket {
		for name, a := range fields {
			if a.count == 0 {
				continue
			}
			out = append(out, rollupBackfillAggRow{
				Bucket:     bucket,
				SensorName: name,
				AvgV:       a.sum / float64(a.count),
				MinV:       a.min,
				MaxV:       a.max,
				LastV:      a.last,
				CntV:       a.count,
			})
		}
	}
	return out, nil
}
