package backfill

// partition_job.go — 单个分区的回填循环（契约 §4.4 step a~d）。
//
// ============ 游标协议（2026-11-07 P0 修复后）============
//
// 每批：
//	1. fetchFrameKeys(from=cursor, ...) 取本批帧键（复合游标，精确到 (ts, device_id)）
//	2. fetchFrameRows 一次范围查询取回全部行，按 (device, ts) 分组
//	3. 按设备分组编码（关键帧策略见 encodeBatch）
//	4. 批量写 telemetry_frame（ON CONFLICT DO NOTHING ⇒ 重跑幂等）
//	5. advanceWatermark 推进断点（**帧先落库，再推进**）
//
// ⚠ 游标**绝不**做时间加法（`Add(time.Nanosecond)` 之类）：PG timestamptz 精度是
// 微秒，加 1ns 等于没加 ⇒ `WHERE timestamp >= from` 每轮取回同一批 ⇒ 回填永不
// 完成、CPU 100%、frames_done 涨到源数据的上百倍。详见 scan.go 文件头。

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/models"
	"ehome/backend/internal/telemetry"
)

// backfillPartition 把一个分区从 watermark 回填到上界。
func (r *Runner) backfillPartition(ctx context.Context, part partition) error {
	if err := r.markPartitionRunning(ctx, part.name); err != nil {
		return err
	}
	row, err := r.loadProgressRow(ctx, part.name)
	if err != nil {
		return err
	}

	// 断点：复合游标 (ts, device_id)。**没有 +1ns** —— 见文件头。
	from := cursor{}
	if row.Watermark != nil {
		from = cursor{TS: row.Watermark.UTC(), DeviceID: row.WatermarkDeviceID}
	}

	slog.Info("backfill: partition start",
		"partition", part.name, "from", from.String(),
		"to", part.hi.Format(time.RFC3339Nano), "to_inclusive", part.hiInclusive,
		"batch_frames", r.cfg.BatchFrames)

	var clampedFrames, clampedFields int64
	for batchIdx := 0; ; batchIdx++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		keys, ferr := r.fetchFrameKeys(ctx, from, part, r.cfg.BatchFrames)
		if ferr != nil {
			return ferr
		}
		if len(keys) == 0 {
			break // 该区间没有更多帧了
		}

		devices := r.devicesOf(keys)
		next := cursorOf(keys[len(keys)-1])
		if len(devices) == 0 {
			// 全部设备被 DeviceFilter 过滤掉：只推进游标，不编码。
			if err := r.advanceWatermark(ctx, part.name, next, 0, 0); err != nil {
				return err
			}
			from = next
			continue
		}

		tsLo, tsHi := keys[0].TS, keys[len(keys)-1].TS
		rowsByFrame, rerr := r.fetchFrameRows(ctx, devices, tsLo, tsHi)
		if rerr != nil {
			return rerr
		}

		frames, rawByFrame, consumedRows, cf, cfd := r.encodeBatch(keys, rowsByFrame)
		clampedFrames += cf
		clampedFields += cfd

		if len(frames) > 0 {
			if err := r.persistFrames(ctx, frames, rawByFrame); err != nil {
				return err
			}
		}

		// 顺序纪律：帧先落库，再推进 watermark（反了会在崩溃时丢一整批）。
		if err := r.advanceWatermark(ctx, part.name, next, int64(len(frames)), consumedRows); err != nil {
			return err
		}
		from = next

		// 测试钩子：让「在第 N 批之后被打断」成为确定性事件（生产恒 nil）。
		if r.testHookAfterBatch != nil {
			if herr := r.testHookAfterBatch(batchIdx); herr != nil {
				return herr
			}
		}
	}

	if clampedFrames > 0 {
		slog.Warn("backfill: frames with out-of-range values preserved in raw column",
			"partition", part.name, "frames", clampedFrames, "fields", clampedFields)
	}
	return r.markPartitionDone(ctx, part)
}

// devicesOf 返回本批涉及的设备（去重 + 升序 + 应用 DeviceFilter）。
func (r *Runner) devicesOf(keys []frameKey) []uint {
	set := make(map[uint]struct{}, 8)
	for _, k := range keys {
		if len(r.cfg.DeviceFilter) > 0 && !containsUint(r.cfg.DeviceFilter, k.DeviceID) {
			continue
		}
		set[k.DeviceID] = struct{}{}
	}
	out := make([]uint, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// encodeBatch 把一批帧键编码成帧表行。
//
// ============ 为什么必须**按设备分组**而不是按时间顺序混排编码 ============
//
// 关键帧策略（契约 §4.5）要求「每个 (设备, 连续时间块) 首帧 = 关键帧，之后每
// KeyFrameInterval 强制一个」。而 telemetry.Encoder 的关键帧判定是**基于时间间隙**
// 的（距上次编码该 slot 超过 interval 才发关键帧）—— 那是为线上流式写入设计的
// 「丢帧后重建链」语义，**不会**在持续 1 Hz 的密集数据上定期发关键帧。
//
// 直接用会发生什么：回填 device14 一个月的 220 万帧，只有第一帧是关键帧，
// 之后是一条 220 万帧的增量链。一旦某帧 payload 损坏，解码器会把该 slot 的链
// 标记失效并丢弃后续所有增量帧直到下一个关键帧 —— 而下一个关键帧可能永远不来
// （要等时间间隙 > 60s，密集数据里不存在）⇒ 该 slot 从损坏点起永久不可解。
//
// 契约 §4.5 的「每 60s 强制一个」正是为此。实现方式：按设备分组，设备内按时间
// 顺序编码，每跨过 KeyFrameInterval 就 Reset 一次编码器 —— Reset 后该设备的下
// 一帧对每个 slot 都是关键帧。
//
// 按设备分组是**等价变换**（不是近似）：编码器状态本来就按 (deviceID, slot 签名)
// 分键，设备 A 的帧与设备 B 的帧在状态上完全独立，处理顺序不影响任何一个设备的链。
//
// 顺带解决内存问题：每次 Reset 清空 seqs map（按 (device,ts) 记账、无界增长），
// 因此不需要「每批整批 Reset」那种粗放做法。
func (r *Runner) encodeBatch(keys []frameKey, rowsByFrame map[frameKey][]rowValue) (
	frames []telemetry.EncodedFrame, rawByFrame map[int][]byte, consumedRows int64, clampedFrames, clampedFields int64,
) {
	byDevice := make(map[uint][]frameKey, 8)
	for _, k := range keys {
		byDevice[k.DeviceID] = append(byDevice[k.DeviceID], k)
	}
	devices := make([]uint, 0, len(byDevice))
	for d := range byDevice {
		devices = append(devices, d)
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i] < devices[j] })

	frames = make([]telemetry.EncodedFrame, 0, len(keys))
	rawByFrame = make(map[int][]byte)

	for _, dev := range devices {
		ks := byDevice[dev]
		sort.Slice(ks, func(i, j int) bool { return ks[i].TS.Before(ks[j].TS) })
		// 设备链起点：Reset 让该设备首帧对每个 slot 都是关键帧。
		r.enc.Reset()
		var lastKeyFrameAt time.Time
		for _, k := range ks {
			vals := rowsByFrame[k]
			if len(vals) == 0 {
				// 该帧的行不在预取范围内（如被 DeviceFilter 排除）——跳过但不算错误。
				continue
			}
			// 周期关键帧：距上一个关键帧 >= interval 就重置链。
			if !lastKeyFrameAt.IsZero() && k.TS.Sub(lastKeyFrameAt) >= r.cfg.KeyFrameInterval {
				r.enc.Reset()
			}
			consumedRows += int64(len(vals))
			readings := make([]telemetry.Reading, len(vals))
			for i := range vals {
				readings[i] = telemetry.Reading{Name: vals[i].Name, Value: vals[i].Value}
			}
			sortReadings(readings)

			// 超量程预判必须在编码前（编码器只给聚合计数，不暴露是哪一帧哪个字段）。
			clamped := detectClamped(readings)
			rawBytes, eraw := buildRawPayload(clamped)
			if eraw != nil {
				r.addError("构造 raw 失败 device=%d ts=%s: %v", k.DeviceID, k.TS.Format(time.RFC3339Nano), eraw)
				rawBytes = nil
			}

			frame, eerr := r.enc.EncodeFrame(k.DeviceID, k.TS, 0, readings)
			if eerr != nil {
				// 单帧编码失败不该中断整个回填（派生数据失败只记不挡）：记账、跳过、继续。
				r.addError("编码失败 device=%d ts=%s: %v", k.DeviceID, k.TS.Format(time.RFC3339Nano), eerr)
				continue
			}
			if frame.FrameKind == models.TelemetryFrameKindKey {
				lastKeyFrameAt = k.TS
			}
			if len(rawBytes) > 0 {
				rawByFrame[len(frames)] = rawBytes
				clampedFrames++
				clampedFields += int64(len(clamped))
			}
			frames = append(frames, frame)
		}
	}
	return frames, rawByFrame, consumedRows, clampedFrames, clampedFields
}

// persistFrames 批量写帧（ON CONFLICT DO NOTHING ⇒ 重跑幂等）。
//
// 为什么 DO NOTHING 而不是 DO UPDATE：回填的输入是**不可变历史**，同一
// (device, ts, seq) 已存在说明上一轮写过同样内容；覆盖写没有收益，却会让
// 「重跑不产生重复」这个验收无法用行数判定。
func (r *Runner) persistFrames(ctx context.Context, frames []telemetry.EncodedFrame, rawByFrame map[int][]byte) error {
	if len(frames) == 0 {
		return nil
	}
	rows := make([]models.TelemetryFrame, len(frames))
	for i := range frames {
		rows[i] = models.TelemetryFrame{
			DeviceID:  frames[i].DeviceID,
			TS:        frames[i].TS,
			Seq:       frames[i].Seq,
			FrameKind: frames[i].FrameKind,
			SlotID:    frames[i].SlotID,
			Payload:   frames[i].Payload,
			Raw:       rawByFrame[i],
		}
	}
	// 分块写：一条 INSERT 的**绑定变量数**有上限（SQLite 默认 32766，PG 65535）。
	// 7 列 × 5000 帧 = 35000 > SQLite 上限 ⇒ 整批失败 "too many SQL variables"
	// （实测在真实数据验收时撞到）。500 行 × 7 列 = 3500 个变量，两方言都安全。
	const chunk = 500
	for start := 0; start < len(rows); start += chunk {
		end := start + chunk
		if end > len(rows) {
			end = len(rows)
		}
		chunkRows := rows[start:end]
		if err := r.db.WithContext(ctx).Session(&gorm.Session{}).
			Clauses(clause.OnConflict{DoNothing: true}).
			Create(&chunkRows).Error; err != nil {
			return fmt.Errorf("persist frames [%d:%d] of %d: %w", start, end, len(rows), err)
		}
	}
	return nil
}

// containsUint 报告切片里是否有 v。
func containsUint(xs []uint, v uint) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// 编译期断言：状态表常量确实存在于 datalifecycle（防拼写漂移导致回填静默不跑）。
var _ = datalifecycle.BackfillDone
