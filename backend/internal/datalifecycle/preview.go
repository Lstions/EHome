package datalifecycle

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
)

// PreviewSource 是合并预览中单个源的信息 (§3.4 preview 端点)。
// first/last_data_at 用 MIN/MAX(timestamp) 走 idx_unified_logical_ts
// 索引范围扫描 (非全表 COUNT); row_estimate 用估算 (§1.3 降级语义:
// 超时/失败省略)。
type PreviewSource struct {
	ID          uint       `json:"id"`
	Name        string     `json:"name"`
	DeviceType  string     `json:"device_type"`
	FirstDataAt *time.Time `json:"first_data_at"`
	LastDataAt  *time.Time `json:"last_data_at"`
	RowEstimate *int64     `json:"row_estimate,omitempty"`
	// OverlapWithOthers: 本源时间范围与其余任一源相交 (§3.4 重叠提示)。
	OverlapWithOthers bool `json:"overlap_with_others"`
}

// MergePreview 是 POST /logical-devices/merge/preview 的响应。
type MergePreview struct {
	Sources []PreviewSource `json:"sources"`
	// TargetRetentionDays: 新建目标将采用的 retention_days (系统级快照,
	// v3.3-N2), 供用户预览确认。
	TargetRetentionDays int `json:"target_retention_days"`
}

// PreviewMerge 收集各源的时间范围/数据量估算 (方案 §3.4 合并预览)。
// ctx 约束整个预估段 (MIN/MAX + row_estimate), 超时按 §1.3 降级:
// row_estimate 省略, 时间范围查询本身轻量 (索引扫描) 不受影响。
func PreviewMerge(ctx context.Context, db *gorm.DB, sourceIDs []uint) (*MergePreview, error) {
	if len(sourceIDs) < 2 {
		return nil, fmt.Errorf("preview requires at least 2 source logical devices")
	}
	var devices []models.LogicalDevice
	if err := db.Where("id IN ?", sourceIDs).Find(&devices).Error; err != nil {
		return nil, fmt.Errorf("load preview sources: %w", err)
	}
	if len(devices) != len(sourceIDs) {
		return nil, fmt.Errorf("one or more source logical devices not found")
	}
	byID := make(map[uint]models.LogicalDevice, len(devices))
	for _, d := range devices {
		byID[d.ID] = d
	}

	preview := &MergePreview{TargetRetentionDays: SystemRetentionDays()}
	type timeRange struct {
		first, last *time.Time
	}
	ranges := make([]timeRange, len(sourceIDs))

	for i, id := range sourceIDs {
		src := byID[id]
		ps := PreviewSource{ID: src.ID, Name: src.Name, DeviceType: src.DeviceType}

		scope, err := ResolveScope(db, id)
		if err != nil {
			return nil, err
		}
		first, last, err := scopeTimeRange(ctx, db, scope)
		if err != nil {
			return nil, err
		}
		ps.FirstDataAt = first
		ps.LastDataAt = last
		ranges[i] = timeRange{first: first, last: last}

		if rows, ok := EstimateScopeRows(ctx, db, scope); ok {
			ps.RowEstimate = &rows
		}
		preview.Sources = append(preview.Sources, ps)
	}

	// 重叠判定: 两两区间相交 (闭区间, 端点相接算重叠)。
	for i := range preview.Sources {
		ri := ranges[i]
		if ri.first == nil || ri.last == nil {
			continue
		}
		for j := range preview.Sources {
			if i == j {
				continue
			}
			rj := ranges[j]
			if rj.first == nil || rj.last == nil {
				continue
			}
			if !ri.first.After(*rj.last) && !rj.first.After(*ri.last) {
				preview.Sources[i].OverlapWithOthers = true
				break
			}
		}
	}
	return preview, nil
}

// ScopeTimeRange exports the scope time envelope (first/last data timestamp)
// for API responses (管理列表最后数据时间). See scopeTimeRange.
func ScopeTimeRange(ctx context.Context, db *gorm.DB, scope *Scope) (*time.Time, *time.Time, error) {
	return scopeTimeRange(ctx, db, scope)
}

// scopeTimeRange 返回 scope 在 unified_data/device_data 两表中的
// MIN/MAX(timestamp) 包络。无数据返回 (nil, nil)。MIN/MAX 对空集
// 返回 NULL → 扫描进 *time.Time 得 nil (database/sql 原生支持)。
func scopeTimeRange(ctx context.Context, db *gorm.DB, scope *Scope) (*time.Time, *time.Time, error) {
	cond, args := scope.Cond()
	var first, last *time.Time
	for _, table := range []string{"unified_data", "device_data"} {
		var lo, hi *time.Time
		err := db.WithContext(ctx).Raw(
			fmt.Sprintf("SELECT MIN(timestamp), MAX(timestamp) FROM %s WHERE %s", table, cond),
			args...,
		).Row().Scan(&lo, &hi)
		if err != nil {
			return nil, nil, fmt.Errorf("time range of %s: %w", table, err)
		}
		if lo != nil && (first == nil || lo.Before(*first)) {
			first = lo
		}
		if hi != nil && (last == nil || hi.After(*last)) {
			last = hi
		}
	}
	return first, last, nil
}
