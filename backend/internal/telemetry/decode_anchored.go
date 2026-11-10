package telemetry

// decode_anchored.go — 带**关键帧锚点**的范围解码（帧表读路径的地基）。
//
// ============ 为什么 DecodeRange 不能直接用 ============
//
// DecodeRange 从 `from` 开始取帧。若 `from` 落在一条增量链的**中间**（绝大多数
// 情况都是），那么从 from 到下一个关键帧之间的帧全是「增量但无基线」⇒ 解码器
// 按 §8 的断链规则**跳过它们**（MissingBaseline / FramesSkippedBrokenChain），
// 于是窗口开头最多 60 秒（关键帧间隔）的数据**静默消失**。
//
// 对齐 unified_data 时这是致命的：SQL 路径读的是真实行，帧表路径会少掉开头
// 一段 ⇒ 「逐值相等」验收永远过不了，而且图表在窗口左边缘会有个诡异的缺口。
//
// ============ 做法 ============
//
// 先把 `from` 向前扩到「≤ from 的最后一个关键帧」再解码，然后把结果裁回
// [from, to)。最多多解 60 秒的帧（1 Hz 下 60 帧），成本可忽略。
//
// 找不到锚点（窗口早于该设备的第一帧）时**不报错**：直接从 from 解，能解多少
// 解多少 —— 调用方拿到的是「帧表里真实存在的那部分」，缺的部分由上层决定
// 是否回落到原始表。

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
)

// KeyFrameAnchor 取 ≤ at 的最后一个关键帧时间戳（该设备）。
//
// 返回 (time.Time{}, false, nil) 表示没有关键帧（窗口早于首帧，或该设备无帧）。
func KeyFrameAnchor(ctx context.Context, db *gorm.DB, deviceID uint, at time.Time) (time.Time, bool, error) {
	if db == nil {
		return time.Time{}, false, fmt.Errorf("telemetry: nil db")
	}
	var anchors []time.Time
	err := db.WithContext(ctx).
		Model(&models.TelemetryFrame{}).
		Where("device_id = ? AND frame_kind = ? AND ts <= ?", deviceID, models.TelemetryFrameKindKey, at).
		Order("ts DESC").Limit(1).
		Pluck("ts", &anchors).Error
	if err != nil {
		return time.Time{}, false, fmt.Errorf("telemetry: keyframe anchor for device %d at %s: %w",
			deviceID, at.Format(time.RFC3339), err)
	}
	if len(anchors) == 0 {
		return time.Time{}, false, nil
	}
	return anchors[0].UTC(), true, nil
}

// DecodeRangeAnchored 是 DecodeRange 的**关键帧对齐**版本：
// 把 from 向前扩到 ≤ from 的最后一个关键帧，解完再裁回 [from, to)。
//
// 使用生产护栏：时间窗 MaxDecodeWindow；帧数上限是 maxScanFrames（8 ×
// MaxDecodeFrames = 262144）而不是 MaxDecodeFrames。
//
// ============ 为什么帧数护栏放宽了（2026-11-10）============
//
// 旧实现的帧数上限同时承担了「一次查询取多少帧」和「最多解码多少帧」两件事：
// 超限时保留最新、丢弃最老 ⇒ 当锚点距窗口起点超过 32768 帧时，**锚点帧被丢掉**，
// 之后的增量帧全部无基线 ⇒ 0 点 / FramesTruncated 报错（生产 dev14 在
// 2026-11-08~11-10 的 37 小时历史损伤就是这么炸的：任何起点落在其中的窗口都
// 只剩 11-08 15:57:58 一个锚点，一旦跨度超过 ≈9h06m 就必炸）。
//
// 现在 decodeRangeBounded 按 (ts,seq) keyset 分页解码，锚点帧永远参与，帧数上限
// 变成**扫描预算** maxScanFrames：它只限制「锚点距窗口多远还能解」，正常窗口
// （≤ 24h）在 37h 损伤段上最坏扫描 ≈61h ≈ 221k 帧，仍在 262144 内。
//
// 需要放宽护栏的场景（验收对拍要对齐 1d/7d 口径）请显式用
// DecodeRangeAnchoredBounded，别改常量：常量是全局的，改它会同时放松**所有**读路径。
func DecodeRangeAnchored(ctx context.Context, db *gorm.DB, dict SlotDictionary, deviceID uint, from, to time.Time, fields []string) ([]Point, DecodeStats, error) {
	return DecodeRangeAnchoredBounded(ctx, db, dict, deviceID, from, to, fields, MaxDecodeWindow, maxScanFrames)
}

// DecodeRangeAnchoredBounded 是 DecodeRangeAnchored 的**显式护栏**版本。
//
// maxWindow / maxFrames 由调用方给：生产传常量（见上），验收对拍可临时放宽以
// 验证「同一批数据在 1d/7d 窗口上两种数据源聚合结果逐值相等」—— 那是**聚合
// 口径**的等价性证明，与生产护栏是两件事。
//
// maxFrames 的语义是**扫描预算**（从关键帧锚点起最多扫多少帧），不是「请求窗口
// 内最多解码多少帧」：分页解码必须从锚点一路推进才能建立链状态，所以按扫描量
// 计费才是真实成本。<=0 时用 maxScanFrames。
//
// 扫描预算耗尽仍有剩余帧时**不静默**：剩余未解码帧数记进 stats.FramesTruncated，
// 调用方（history_source / preview / rollup backfill）已有的检查会据此报错/回落。
func DecodeRangeAnchoredBounded(ctx context.Context, db *gorm.DB, dict SlotDictionary, deviceID uint, from, to time.Time, fields []string, maxWindow time.Duration, maxFrames int) ([]Point, DecodeStats, error) {
	if err := ctx.Err(); err != nil {
		return nil, DecodeStats{}, err
	}
	if !to.After(from) {
		return nil, DecodeStats{}, fmt.Errorf("telemetry: empty range [%s, %s)", from.Format(time.RFC3339), to.Format(time.RFC3339))
	}
	if maxWindow <= 0 {
		maxWindow = MaxDecodeWindow
	}
	if maxFrames <= 0 {
		// 分页解码的默认预算 = maxScanFrames（不是 MaxDecodeFrames）：32768 那条
		// 一次查询上限正是旧实现的墙，不能再当作生产默认。
		maxFrames = maxScanFrames
	}
	if to.Sub(from) > maxWindow {
		return nil, DecodeStats{}, &RangeTooLargeError{Window: to.Sub(from)}
	}

	decodeFrom := from
	if anchor, ok, err := KeyFrameAnchor(ctx, db, deviceID, from); err != nil {
		return nil, DecodeStats{}, err
	} else if ok && anchor.Before(from) {
		decodeFrom = anchor
	}

	points, stats, err := decodeRangeBounded(ctx, db, dict, deviceID, decodeFrom, from, to, fields, maxFrames)
	if err != nil {
		return nil, stats, err
	}
	if decodeFrom.Equal(from) {
		return points, stats, nil
	}
	// 裁回 [from, to)：锚点段的值已经喂进了解码状态（这正是我们要的），
	// 但它们的点不属于本次窗口。
	cut := 0
	for cut < len(points) && points[cut].TS.Before(from) {
		cut++
	}
	if cut > 0 {
		points = points[cut:]
	}
	return points, stats, nil
}

// decodeRangeBounded 是 DecodeRange 的**分页/流式**版本（2026-11-10 去 32768 帧墙）。
//
// ============ 为什么必须分页（旧实现的两个致命点）============
//
// 旧实现：一次 queryFrames(scanFrom, to, maxFrames+1)，超限时
// frames = frames[len-maxFrames:]（保留最新、丢弃最老）。当关键帧锚点距窗口起点
// 超过 maxFrames 时，**锚点帧被丢掉** ⇒ 后继增量帧全部无基线 ⇒ 0 点 / 报错。
// 生产 dev14 在 2026-11-08~11-10 的 37 小时历史损伤（存量帧没有周期关键帧）就是
// 这种形态：任何起点落在其中的窗口都只剩 11-08 15:57:58 一个锚点，跨度一超
// ≈9h06m（MaxDecodeFrames @1Hz）就必炸。
//
// 现在按 (ts, seq) keyset 分页（decodeScanPageSize 帧/页），逐页 Feed 同一个
// Decoder：链状态跨页保持，**锚点帧永远参与解码**；只有 [windowFrom, to) 的点
// 会被收集（锚点预热段不产点，控制内存）。
//
// ============ 硬上限与「不静默」 ============
//
// 扫描预算 maxFrames（<=0 用 maxScanFrames）：预算耗尽但还有剩余帧时，把**剩余
// 未解码帧数**如实记进 FramesTruncated，绝不再「丢掉锚点后假装解完」。
// 正常路径（生产窗口）由 maxScanFrames 覆盖，不会触发；触发即调用方报错/回落。
func decodeRangeBounded(ctx context.Context, db *gorm.DB, dict SlotDictionary, deviceID uint, scanFrom, windowFrom, to time.Time, fields []string, maxFrames int) ([]Point, DecodeStats, error) {
	if maxFrames <= 0 {
		maxFrames = maxScanFrames
	}
	st := &Store{db: db, dict: dict}
	dec, err := NewDecoder(dict, DecodeOptions{Fields: fields})
	if err != nil {
		return nil, DecodeStats{}, err
	}
	if windowFrom.After(scanFrom) {
		// 锚点预热段（scanFrom <= TS < windowFrom）仍更新链状态，但不生成 Point。
		dec.emitFrom = windowFrom
	}

	points := make([]Point, 0, decodeScanPageSize)
	var cursor *frameCursor
	scanned := 0
	for {
		// 每页检查一次 ctx：调用方单段超时（history_source 5s）必须能中断长扫描。
		if err := ctx.Err(); err != nil {
			return nil, dec.Stats(), err
		}
		limit := decodeScanPageSize
		if remain := maxFrames - scanned; remain < limit {
			limit = remain
		}
		// 多取 1 帧用于判断「是否还有更多帧」：避免把「恰好扫满预算」误报成截断。
		page, err := st.queryFramesPage(deviceID, scanFrom, to, cursor, limit+1)
		if err != nil {
			return nil, dec.Stats(), err
		}
		more := len(page) > limit
		if more {
			page = page[:limit]
		}
		if len(page) > 0 {
			pts, _ := dec.Feed(page)
			points = append(points, pts...)
			last := page[len(page)-1]
			cursor = &frameCursor{ts: last.TS, seq: last.Seq}
			scanned += len(page)
		}
		if !more {
			break
		}
		if scanned >= maxFrames {
			// 预算耗尽且还有帧：如实报截断。走到这里说明「锚点距窗口起点太远」，
			// 调用方按 FramesTruncated>0 报错（用户缩小窗口/走 rollup），而不是拿到
			// 一份看起来正常的残缺数据。
			remaining, cerr := st.countFramesAfter(deviceID, scanFrom, to, cursor)
			if cerr != nil {
				return nil, dec.Stats(), cerr
			}
			stats := dec.Stats()
			stats.FramesTruncated += remaining
			return points, stats, nil
		}
	}
	return points, dec.Stats(), nil
}
