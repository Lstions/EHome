package datalifecycle

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
)

// Scope is the resolved data scope of a logical device (方案 §4.3/§六):
// the logical device itself plus any pending-merge sources feeding into it,
// and the full instance set (Unscoped) of those logical devices. Query and
// cleanup MUST share this resolution (v3.1-Q2 闭环) so pre-backfill rows
// with logical_device_id NULL are never missed.
type Scope struct {
	// LogicalIDs holds the target logical device and, when the target has
	// pending incoming merges, those pending sources (v3.2-终审 B1 搬迁窗口).
	LogicalIDs []uint
	// InstanceIDs holds every edge_device instance (including soft-deleted)
	// attached to any logical device in LogicalIDs.
	InstanceIDs []uint
}

// Cond renders the scope as a SQL condition with placeholders. The column
// names (logical_device_id / device_id) are identical in unified_data and
// device_data, so one condition serves both tables.
func (s *Scope) Cond() (cond string, args []interface{}) {
	switch {
	case len(s.LogicalIDs) == 0 && len(s.InstanceIDs) == 0:
		return "1 = 0", nil
	case len(s.LogicalIDs) == 0:
		// Instance-only scope (e.g. an instance whose logical device has not
		// been created yet): its rows are exactly the NULL-logical rows
		// written under that instance's device_id.
		return "logical_device_id IS NULL AND device_id IN ?", []interface{}{s.InstanceIDs}
	case len(s.InstanceIDs) == 0:
		return "logical_device_id IN ?", []interface{}{s.LogicalIDs}
	default:
		// The whole condition is wrapped in parentheses so callers can safely
		// append " AND ..." — AND binds tighter than OR, so without the outer
		// parentheses a composed time/sensor filter would apply only to the
		// NULL-logical fallback branch and the logical branch would leak rows
		// outside the filter (history/historical/historical-batch compose the
		// condition this way, §六).
		return "(logical_device_id IN ? OR (logical_device_id IS NULL AND device_id IN ?))",
			[]interface{}{s.LogicalIDs, s.InstanceIDs}
	}
}

// ResolveScope resolves the data scope of a logical device.
func ResolveScope(db *gorm.DB, logicalID uint) (*Scope, error) {
	scope := &Scope{LogicalIDs: []uint{logicalID}}

	// Pending incoming merges: rows of a pending source still belong to the
	// source scope, but queries resolving to the target must see them during
	// the migration window (v3.2-终审 B1).
	var pendingSources []models.LogicalDevice
	if err := db.Where("merged_into = ? AND merge_status = ?", logicalID, models.MergeStatusPending).
		Find(&pendingSources).Error; err != nil {
		return nil, fmt.Errorf("datalifecycle: resolve pending sources of logical_device %d: %w", logicalID, err)
	}
	for _, src := range pendingSources {
		scope.LogicalIDs = append(scope.LogicalIDs, src.ID)
	}

	// Instance set: Unscoped so soft-deleted instances are included.
	var instances []models.EdgeDevice
	if err := db.Unscoped().
		Where("logical_device_id IN ?", scope.LogicalIDs).
		Find(&instances).Error; err != nil {
		return nil, fmt.Errorf("datalifecycle: resolve instances of logical_device %d: %w", logicalID, err)
	}
	for _, inst := range instances {
		scope.InstanceIDs = append(scope.InstanceIDs, inst.ID)
	}
	return scope, nil
}

// ApplyScope returns a query on a data table (unified_data/device_data)
// restricted to the logical device's scope.
func ApplyScope(tx *gorm.DB, scope *Scope) *gorm.DB {
	cond, args := scope.Cond()
	return tx.Where(cond, args...)
}

// rowEstimateCap bounds truncated COUNTs (SQLite path) so the estimate can
// never trigger a full-table scan.
const rowEstimateCap = 100000

// EstimateTimeout is the §1.3 degradation deadline: when exceeded the info
// endpoint returns without a data-volume figure instead of blocking.
// T1.1: 端点估算段在该值上挂独立外层超时; 对未携带 deadline 的调用方
// ctx, 它也作为兜底 deadline 生效。
const EstimateTimeout = 3 * time.Second

var explainRowsRe = regexp.MustCompile(`rows=(\d+)`)

// EstimateRowCount estimates (never exact-counts, §1.3) the data rows
// belonging to a logical device across unified_data and device_data.
// PostgreSQL uses the planner's reltuples-derived estimate via EXPLAIN;
// SQLite uses a truncated COUNT capped at rowEstimateCap. The boolean
// return is false when the estimate is unavailable (timeout/parse failure),
// in which case callers must omit the figure (降级).
// ctx bounds the whole estimation segment (scope resolution + both tables):
// callers pass an endpoint-level timeout so the handler never blocks
// (T1.1).
func EstimateRowCount(ctx context.Context, db *gorm.DB, logicalID uint) (int64, bool) {
	ctx, cancel := estimateDeadline(ctx)
	defer cancel()
	db = db.WithContext(ctx)
	scope, err := ResolveScope(db, logicalID)
	if err != nil {
		return 0, false
	}
	return EstimateScopeRows(ctx, db, scope)
}

// EstimateScopeRows estimates data rows for an already-resolved scope.
// See EstimateRowCount for estimation and degradation semantics.
func EstimateScopeRows(ctx context.Context, db *gorm.DB, scope *Scope) (int64, bool) {
	ctx, cancel := estimateDeadline(ctx)
	defer cancel()
	db = db.WithContext(ctx)

	cond, args := scope.Cond()
	var total int64
	for _, table := range []string{"unified_data", "device_data"} {
		n, ok := estimateTable(ctx, db, table, cond, args)
		if !ok {
			return 0, false
		}
		total += n
	}
	return total, true
}

// estimateDeadline keeps the §1.3 degradation bound even when the caller's
// context carries no deadline; endpoint callers pass their own timeout.
func estimateDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, EstimateTimeout)
}

func estimateTable(ctx context.Context, db *gorm.DB, table, cond string, args []interface{}) (int64, bool) {
	if db.Dialector != nil && db.Dialector.Name() == "postgres" {
		return estimatePostgres(ctx, db, table, cond, args)
	}
	return estimateTruncatedCount(ctx, db, table, cond, args)
}

// estimatePostgres reads the planner row estimate (derived from
// pg_class.reltuples and column statistics) via EXPLAIN — O(1) planning,
// no data scan. Falls back to truncated COUNT when parsing fails.
// GORM Raw is used (not database/sql) so slice args in `IN ?` expand.
func estimatePostgres(ctx context.Context, db *gorm.DB, table, cond string, args []interface{}) (int64, bool) {
	query := fmt.Sprintf("EXPLAIN SELECT id FROM %s WHERE %s", table, cond)
	var lines []string
	if err := db.WithContext(ctx).Raw(query, args...).Scan(&lines).Error; err != nil {
		return estimateTruncatedCount(ctx, db, table, cond, args)
	}
	// The plan's top node is printed first; its rows= figure is the total
	// estimate for the whole plan.
	for _, line := range lines {
		if m := explainRowsRe.FindStringSubmatch(line); m != nil {
			n, perr := strconv.ParseInt(m[1], 10, 64)
			if perr == nil {
				return n, true
			}
		}
	}
	return estimateTruncatedCount(ctx, db, table, cond, args)
}

// estimateTruncatedCount counts at most rowEstimateCap rows — the §1.3
// "截断 COUNT" for SQLite, and the PostgreSQL fallback.
func estimateTruncatedCount(ctx context.Context, db *gorm.DB, table, cond string, args []interface{}) (int64, bool) {
	query := fmt.Sprintf("SELECT COUNT(*) FROM (SELECT 1 FROM %s WHERE %s LIMIT %d) AS capped", table, cond, rowEstimateCap)
	var n int64
	if err := db.WithContext(ctx).Raw(query, args...).Scan(&n).Error; err != nil {
		return 0, false
	}
	return n, true
}
