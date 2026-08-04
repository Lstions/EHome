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
// 首/末数据时间包络。无数据返回 (nil, nil)。
//
// 实现: ORDER BY timestamp ASC/DESC + LIMIT 1 取实际行, 走
// idx_unified_logical_ts 索引边界扫描 (与 MIN/MAX 同等量级, §3.4)。
// 不用 MIN/MAX 聚合 + *time.Time 扫描: mattn/go-sqlite3 把 timestamp
// 存为文本, 聚合结果绕过 GORM 的行级时间转换, Scan 报 unsupported。
func scopeTimeRange(ctx context.Context, db *gorm.DB, scope *Scope) (*time.Time, *time.Time, error) {
	first, err := scopeBoundaryTimestamp(ctx, db, scope, true)
	if err != nil {
		return nil, nil, err
	}
	last, err := scopeBoundaryTimestamp(ctx, db, scope, false)
	if err != nil {
		return nil, nil, err
	}
	return first, last, nil
}

// scopeOldestTimestamp returns the oldest timestamp across both data
// tables for the scope, or nil when the scope holds no rows. Same
// implementation note as scopeTimeRange (SQLite text-timestamp driver).
func scopeOldestTimestamp(ctx context.Context, db *gorm.DB, scope *Scope) (*time.Time, error) {
	return scopeBoundaryTimestamp(ctx, db, scope, true)
}

// scopeBoundaryTimestamp scans unified_data and device_data for the scope's
// earliest (first=true) or latest (first=false) data timestamp.
func scopeBoundaryTimestamp(ctx context.Context, db *gorm.DB, scope *Scope, first bool) (*time.Time, error) {
	cond, args := scope.Cond()
	order := "timestamp DESC"
	if first {
		order = "timestamp ASC"
	}
	var boundary *time.Time
	for _, table := range []string{"unified_data", "device_data"} {
		var ts *time.Time
		// SELECT timestamp 单列 + LIMIT 1: 行级扫描路径, 两方言一致。
		var rows []time.Time
		err := db.WithContext(ctx).Table(table).
			Select("timestamp").
			Where(cond, args...).
			Order(order).
			Limit(1).
			Scan(&rows).Error
		if err != nil {
			return nil, fmt.Errorf("boundary timestamp of %s: %w", table, err)
		}
		if len(rows) > 0 {
			ts = &rows[0]
		}
		if ts == nil {
			continue
		}
		if boundary == nil {
			boundary = ts
			continue
		}
		if first && ts.Before(*boundary) || !first && ts.After(*boundary) {
			boundary = ts
		}
	}
	return boundary, nil
}
