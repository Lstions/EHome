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
// 使用生产护栏（MaxDecodeWindow / MaxDecodeFrames）。需要放宽护栏的场景
// （验收对拍要对齐 1d/7d 口径）请显式用 DecodeRangeAnchoredBounded，别改常量：
// 常量是全局的，改它会同时放松**所有**读路径。
func DecodeRangeAnchored(ctx context.Context, db *gorm.DB, dict SlotDictionary, deviceID uint, from, to time.Time, fields []string) ([]Point, DecodeStats, error) {
	return DecodeRangeAnchoredBounded(ctx, db, dict, deviceID, from, to, fields, MaxDecodeWindow, MaxDecodeFrames)
}

// DecodeRangeAnchoredBounded 是 DecodeRangeAnchored 的**显式护栏**版本。
//
// maxWindow / maxFrames 由调用方给：生产传常量（见上），验收对拍可临时放宽以
// 验证「同一批数据在 1d/7d 窗口上两种数据源聚合结果逐值相等」—— 那是**聚合
// 口径**的等价性证明，与生产护栏是两件事。
//
// maxWindow <= 0 或 maxFrames <= 0 时用生产常量。
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
		maxFrames = MaxDecodeFrames
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

	points, stats, err := decodeRangeBounded(ctx, db, dict, deviceID, decodeFrom, to, fields, maxFrames)
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

// decodeRangeBounded 是 DecodeRange 的可变帧数上限版本。
//
// 为什么单独写而不是给 DecodeRange 加参数：DecodeRange 是被别的包直接调用的
// 公开签名（store_db_test 等），改签名会波及无关调用点。这里只复制「取帧 →
// 截断 → 解码」三步，语义与 DecodeRange 逐行一致（含"超出时保留最新"）。
func decodeRangeBounded(ctx context.Context, db *gorm.DB, dict SlotDictionary, deviceID uint, from, to time.Time, fields []string, maxFrames int) ([]Point, DecodeStats, error) {
	st := &Store{db: db, dict: dict}
	frames, err := st.queryFrames(deviceID, from, to, maxFrames+1)
	if err != nil {
		return nil, DecodeStats{}, err
	}
	truncated := 0
	if len(frames) > maxFrames {
		truncated = len(frames) - maxFrames
		frames = frames[len(frames)-maxFrames:]
	}
	points, stats, err := DecodeFrames(frames, dict, DecodeOptions{Fields: fields})
	if err != nil {
		return nil, stats, err
	}
	// 截断**单独计数**，不混进 BrokenChain —— 理由见 DecodeStats.FramesTruncated 的注释。
	//
	// 混进去的时候，这个数字会被读成「正常断链」，而正常断链是设计行为 ⇒ 无人追查。
	stats.FramesTruncated += int64(truncated)
	return points, stats, nil
}
