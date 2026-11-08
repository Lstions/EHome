package backfill

// persist.go — 状态表的读写与断点推进（断点续跑的落地部分）。

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"ehome/backend/internal/datalifecycle"
)

// loadProgressRow 取某分区的状态行；不存在时**创建** pending 行。
//
// 为什么"不存在就创建"而不是返回零值：状态行是断点的唯一载体。若允许返回
// 零值，第一次跑到一半崩溃时会没有行可更新，下次仍然从头 —— 断点续跑失效。
func (r *Runner) loadProgressRow(ctx context.Context, partName string) (*datalifecycle.BackfillProgressRow, error) {
	row := &datalifecycle.BackfillProgressRow{}
	err := r.db.WithContext(ctx).
		Where("table_name = ? AND part_name = ?", r.srcTable(), partName).
		First(row).Error
	switch {
	case err == nil:
		return row, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		row = &datalifecycle.BackfillProgressRow{
			SrcTable: r.srcTable(),
			PartName: partName,
			State:    datalifecycle.BackfillPending,
		}
		if cerr := r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).
			Create(row).Error; cerr != nil {
			return nil, fmt.Errorf("create progress row for %s: %w", partName, cerr)
		}
		return row, nil
	default:
		return nil, fmt.Errorf("load progress row for %s: %w", partName, err)
	}
}

// loadProgressRowNoCreate 只读；不存在返回 (nil, nil)。
func (r *Runner) loadProgressRowNoCreate(ctx context.Context, partName string) (*datalifecycle.BackfillProgressRow, error) {
	row := &datalifecycle.BackfillProgressRow{}
	err := r.db.WithContext(ctx).
		Where("table_name = ? AND part_name = ?", r.srcTable(), partName).
		First(row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return row, nil
}

// reloadProgressRows 把全部状态行读进 Progress.Partitions（进度接口/ETA 用）。
func (r *Runner) reloadProgressRows(ctx context.Context, parts []partition) error {
	items := make([]PartitionProgress, 0, len(parts))
	var framesDone, rowsDone int64
	for _, p := range parts {
		row, err := r.loadProgressRowNoCreate(ctx, p.name)
		if err != nil {
			return fmt.Errorf("reload progress for %s: %w", p.name, err)
		}
		item := PartitionProgress{Name: p.name, State: datalifecycle.BackfillPending}
		if row != nil {
			item.State = row.State
			item.Watermark = row.Watermark
			item.FramesDone = row.FramesDone
			item.RowsDone = row.RowsDone
			item.Error = row.Error
			framesDone += row.FramesDone
			rowsDone += row.RowsDone
		}
		items = append(items, item)
	}
	r.mu.Lock()
	r.progress.Partitions = items
	r.progress.FramesDone = framesDone
	r.progress.RowsDone = rowsDone
	r.mu.Unlock()
	r.recomputePercent()
	return nil
}

// markPartitionRunning 置 running + started_at（首次才写 started_at）。
func (r *Runner) markPartitionRunning(ctx context.Context, partName string) error {
	now := time.Now().UTC()
	err := r.db.WithContext(ctx).Model(&datalifecycle.BackfillProgressRow{}).
		Where("table_name = ? AND part_name = ?", r.srcTable(), partName).
		Updates(map[string]interface{}{
			"state":      datalifecycle.BackfillRunning,
			"error":      "",
			"started_at": gorm.Expr("COALESCE(started_at, ?)", now),
		}).Error
	if err != nil {
		return fmt.Errorf("mark %s running: %w", partName, err)
	}
	// 同步内存快照：只写库会让 Progress() 里的分区状态停在 pending，
	// 进度接口与 percent 都会读到过期状态。
	r.setPartitionState(partName, func(p *PartitionProgress) {
		p.State = datalifecycle.BackfillRunning
	})
	return nil
}

// markPartitionDone 置 done + finished_at。
func (r *Runner) markPartitionDone(ctx context.Context, part partition) error {
	partName := part.name
	now := time.Now().UTC()
	err := r.db.WithContext(ctx).Model(&datalifecycle.BackfillProgressRow{}).
		Where("table_name = ? AND part_name = ?", r.srcTable(), partName).
		Updates(map[string]interface{}{
			"state":       datalifecycle.BackfillDone,
			"finished_at": now,
			"error":       "",
		}).Error
	if err != nil {
		return fmt.Errorf("mark %s done: %w", partName, err)
	}
	// 同步内存快照 —— 这是 percent 偏低 bug 的**直接原因**：
	// 空分区不经过 advanceWatermark，只在这里被标 done。不同步 ⇒ 内存里永远是
	// pending ⇒ recomputePercent 的 switch 不匹配任何分支 ⇒ 该分区计 0 分。
	r.setPartitionState(partName, func(p *PartitionProgress) {
		p.State = datalifecycle.BackfillDone
		hi := part.hi
		p.Watermark = &hi
	})
	return nil
}

// setPartitionState 就地更新内存里某个分区的进度快照（不存在则补建一条）。
//
// 为什么需要这个函数（2026-11-07 percent 偏低 bug 的根因）：
// markPartitionRunning / markPartitionDone / markPartitionFailed 原本**只写数据库**，
// 不碰 r.progress.Partitions。而 recomputePercent 读的正是内存快照 —— 于是：
//
//	· 空分区（0 行）永远不经过 advanceWatermark，只在 markPartitionDone 里被标 done；
//	· 只写库 ⇒ 内存里它的 State 永远是 pending ⇒ recomputePercent 的 switch 不匹配
//	  任何分支 ⇒ 该分区计 0 分；
//	· 生产 6 个月分区里 4 个是空的 ⇒ 分母含 6 个月、分子只算 2 个月 ⇒ 进度条严重偏低。
//
// 同时补建「不在列表里」的条目：避免 reload 之后才出现的分区状态丢失。
func (r *Runner) setPartitionState(partName string, mutate func(*PartitionProgress)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.progress.Partitions {
		if r.progress.Partitions[i].Name == partName {
			mutate(&r.progress.Partitions[i])
			r.recomputePercentLocked()
			return
		}
	}
	item := PartitionProgress{Name: partName, State: datalifecycle.BackfillPending}
	mutate(&item)
	r.progress.Partitions = append(r.progress.Partitions, item)
	r.recomputePercentLocked()
}

// markPartitionFailed 置 failed 并记录原因（**保留 watermark**，下次从断点重试）。
func (r *Runner) markPartitionFailed(ctx context.Context, partName string, cause error) {
	if cause == nil {
		return
	}
	err := r.db.WithContext(context.WithoutCancel(ctx)).
		Model(&datalifecycle.BackfillProgressRow{}).
		Where("table_name = ? AND part_name = ?", r.srcTable(), partName).
		Updates(map[string]interface{}{
			"state": datalifecycle.BackfillFailed,
			"error": cause.Error(),
		}).Error
	if err != nil {
		// 记录失败状态本身失败：只能打日志，不能再套一层错误（会把原始原因盖掉）。
		r.addError("标记分区 %s 失败状态时出错: %v（原始错误: %v）", partName, err, cause)
	}
	// 同步内存快照（与 markPartitionRunning/Done 同口径）。
	r.setPartitionState(partName, func(p *PartitionProgress) {
		p.State = datalifecycle.BackfillFailed
		p.Error = cause.Error()
	})
}

// advanceWatermark 提交一批的进度：更新 watermark / frames_done / rows_done。
//
// 这条 UPDATE 是**断点的唯一写入点**。它必须在 frames 已经成功写入
// telemetry_frame **之后**执行 —— 顺序反了（先推进 watermark 再写帧）会在
// 崩溃时丢一整批：watermark 说"这批做完了"，而帧表里没有。
func (r *Runner) advanceWatermark(ctx context.Context, partName string, at cursor, deltaFrames, deltaRows int64) error {
	err := r.db.WithContext(ctx).Model(&datalifecycle.BackfillProgressRow{}).
		Where("table_name = ? AND part_name = ?", r.srcTable(), partName).
		Updates(map[string]interface{}{
			// 复合游标两列**必须一起写**：只写 ts 会让续跑按「跳过整段同 ts 帧」恢复。
			"watermark":           at.TS,
			"watermark_device_id": at.DeviceID,
			"frames_done":         gorm.Expr("frames_done + ?", deltaFrames),
			"rows_done":           gorm.Expr("rows_done + ?", deltaRows),
		}).Error
	if err != nil {
		return fmt.Errorf("advance watermark of %s to %s: %w", partName, at, err)
	}
	r.mu.Lock()
	r.progress.FramesDone += deltaFrames
	r.progress.RowsDone += deltaRows
	for i := range r.progress.Partitions {
		if r.progress.Partitions[i].Name == partName {
			r.progress.Partitions[i].FramesDone += deltaFrames
			r.progress.Partitions[i].RowsDone += deltaRows
			wm := at.TS
			r.progress.Partitions[i].Watermark = &wm
			r.progress.Partitions[i].State = datalifecycle.BackfillRunning
		}
	}
	r.mu.Unlock()
	r.recomputePercent()
	return nil
}

// recomputePercent 重算百分比（对外入口，自己加锁）。
func (r *Runner) recomputePercent() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recomputePercentLocked()
}

// recomputePercentLocked 是 recomputePercent 的无锁版本（调用方已持有 r.mu）。
//
// ============ 权重口径：**实测行数**，不是时间跨度 ============
//
// 2026-11-07 生产实测：6 个月分区里只有 202610/202611 有数据，其余 4 个是空分区
// （0 行）。原实现按时间跨度等权 ⇒ 分母 = 6 个月、分子最多 = 2 个月
// ⇒ **percent 天花板 33%**，而实际帧数已经 45.2%。进度条严重谎报。
//
// 而且不能靠「把空分区也标 done」来修：那会让 4 个空分区在启动瞬间各贡献 1/6
// ⇒ 进度条一开局就跳到 66.7%，是另一种谎报（还更糟 —— 用户会以为快好了）。
//
// 正确口径：**空分区零权重**。权重 = 该分区总行数（discoverPartitions 时
// count(*) 实测一次，生产 72.7M 行约 1.5s，相对 20~40 分钟任务可忽略）。于是：
//
//	· 4 个空分区权重 0，不进分母也不进分子（它们毫秒级跑完，本就零成本）；
//	· 2 个有数据的分区按真实数据量分摊 100%；
//	· percent ≈ 已消费行数 / 总行数，与 frames_done/总帧数 同口径。
//
// 降级：若 count(*) 全部失败（rows 全为 0），退回时间跨度加权；
// 绝不因为「算不出权重」就显示 0%。
func (r *Runner) recomputePercentLocked() {
	if len(r.parts) == 0 {
		r.progress.Percent = 0
		return
	}
	stateByPart := make(map[string]PartitionProgress, len(r.progress.Partitions))
	for _, p := range r.progress.Partitions {
		stateByPart[p.Name] = p
	}

	// 先确定总权重：优先用实测行数；全为 0（计数失败 / 空库）才退回时间跨度。
	totalRows := int64(0)
	for _, p := range r.parts {
		totalRows += p.rows
	}
	useRows := totalRows > 0

	var totalWeight, doneWeight float64
	for _, p := range r.parts {
		var w float64
		if useRows {
			w = float64(p.rows)
		} else {
			w = float64(p.span())
			if w <= 0 {
				w = 1
			}
		}
		totalWeight += w
		if w <= 0 {
			// 空分区（0 行）零权重：既不进分母也不进分子。
			continue
		}

		st := stateByPart[p.name]
		switch st.State {
		case datalifecycle.BackfillDone:
			// 已完成的整分区贡献满权重（即使编码失败导致少消费几行）。
			doneWeight += w
		case datalifecycle.BackfillRunning:
			if useRows {
				// 有实测行数时用**已消费行数**插值 —— 精确，且与
				// frames_done/总帧数 同口径（不是时间估算）。
				if frac := float64(st.RowsDone) / float64(p.rows); frac > 0 {
					if frac > 1 {
						frac = 1
					}
					doneWeight += w * frac
				}
			} else if st.Watermark != nil {
				// 计数失败时的降级：按 watermark 在分区内的位置线性插值。
				if frac := st.Watermark.Sub(p.lo).Seconds() / p.span().Seconds(); frac > 0 {
					if frac > 1 {
						frac = 1
					}
					doneWeight += w * frac
				}
			}
		}
	}
	if totalWeight <= 0 {
		// 全部是空分区：没有数据可回填 ⇒ 进度 100（任务确实无活可干）。
		r.progress.Percent = 100
		return
	}
	pct := doneWeight / totalWeight * 100
	if pct > 100 {
		pct = 100
	}
	if pct < 0 {
		pct = 0
	}
	r.progress.Percent = float64(int(pct*10)) / 10
}
