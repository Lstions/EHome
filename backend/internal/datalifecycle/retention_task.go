package datalifecycle

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
)

// retention 通知文案 (§4.2): 到期前 30/7 天各一条, 文案含延长保留期入口。
// 标题固定用于每日任务的去重查询 (同设备同层级窗口期内不重复发)。
const (
	retentionNoticeTitle30 = "数据保留期即将到期（剩余 30 天）"
	retentionNoticeTitle7  = "数据保留期即将到期（剩余 7 天）"
)

// RetentionResult reports one logical device's retention outcome.
type RetentionResult struct {
	LogicalID    uint   `json:"logical_id"`
	NotifiedTier int    `json:"notified_tier,omitempty"` // 30 / 7 / 0
	RowsDeleted  int64  `json:"rows_deleted"`
	Err          string `json:"error,omitempty"`
}

// RetentionTask is the §4.3 任务 1 daily worker: 保留期临期通知
// (30/7 天) + 到期分批硬删。清理范围与查询同一 scope 解析
// (v3.1-Q2), 与 purge 同样守卫 pending 合并参与者 (搬迁窗口内数据
// 归属未定, 不应用目标/源任一方的保留期)。
type RetentionTask struct {
	db           *gorm.DB
	interval     time.Duration
	initialDelay time.Duration
	batchSleep   time.Duration
	batchSize    int // 0 → dialect default (PG 1万 / SQLite 1千)
	// now is injectable for tests.
	now func() time.Time

	startOnce sync.Once
	stopCh    chan struct{}
	stopOnce  sync.Once
	wg        sync.WaitGroup
}

// NewRetentionTask creates the daily retention worker.
func NewRetentionTask(db *gorm.DB) *RetentionTask {
	return &RetentionTask{
		db:           db,
		interval:     24 * time.Hour,
		initialDelay: 40 * time.Second, // purge 30s 首发之后, 错峰启动
		batchSleep:   purgeBatchSleep,
		now:          time.Now,
		stopCh:       make(chan struct{}),
	}
}

// SetSchedule overrides timing (tests only).
func (r *RetentionTask) SetSchedule(interval, initialDelay, batchSleep time.Duration) {
	if interval > 0 {
		r.interval = interval
	}
	if initialDelay > 0 {
		r.initialDelay = initialDelay
	}
	r.batchSleep = batchSleep
}

// SetBatchSize overrides the per-transaction delete batch size (tests only).
func (r *RetentionTask) SetBatchSize(n int) {
	if n > 0 {
		r.batchSize = n
	}
}

// Start launches the retention goroutine once.
func (r *RetentionTask) Start() {
	r.startOnce.Do(func() {
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			r.run()
		}()
	})
}

// Stop signals the goroutine to exit and waits for it.
func (r *RetentionTask) Stop() {
	r.stopOnce.Do(func() { close(r.stopCh) })
	r.wg.Wait()
}

func (r *RetentionTask) run() {
	timer := time.NewTimer(r.initialDelay)
	defer timer.Stop()
	select {
	case <-timer.C:
		r.runOnceLogged()
	case <-r.stopCh:
		return
	}
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			r.runOnceLogged()
		case <-r.stopCh:
			return
		}
	}
}

func (r *RetentionTask) runOnceLogged() {
	results, err := r.RunOnce(context.Background())
	if err != nil {
		slog.Warn("datalifecycle: retention run failed", "error", err)
		return
	}
	for _, res := range results {
		if res.Err != "" {
			slog.Warn("datalifecycle: retention processing failed",
				"logical_id", res.LogicalID, "error", res.Err)
			continue
		}
		if res.RowsDeleted > 0 {
			slog.Info("datalifecycle: retention expired rows deleted",
				"logical_id", res.LogicalID, "rows_deleted", res.RowsDeleted)
		}
		if res.NotifiedTier > 0 {
			slog.Info("datalifecycle: retention expiry notice sent",
				"logical_id", res.LogicalID, "tier_days", res.NotifiedTier)
		}
	}
}

// RunOnce applies retention policy to every eligible logical device.
// Eligibility guards (与 purge 任务对齐):
//   - purge_requested=TRUE → purge 任务负责 (其数据将被整体删除)
//   - pending 合并参与者 (目标或源) → 顺延至合并终态 (搬迁窗口内数据
//     归属在移动, 保留期删除会误伤未搬迁分片或应用错误的 retention)
func (r *RetentionTask) RunOnce(ctx context.Context) ([]RetentionResult, error) {
	var devices []models.LogicalDevice
	if err := r.db.WithContext(ctx).
		Where("purge_requested = ?", false).
		Order("id").
		Find(&devices).Error; err != nil {
		return nil, fmt.Errorf("datalifecycle: scan logical devices: %w", err)
	}
	results := make([]RetentionResult, 0, len(devices))
	for i := range devices {
		select {
		case <-ctx.Done():
			return results, ctx.Err()
		default:
		}
		results = append(results, r.processOne(ctx, &devices[i]))
	}
	return results, nil
}

func (r *RetentionTask) processOne(ctx context.Context, ld *models.LogicalDevice) RetentionResult {
	result := RetentionResult{LogicalID: ld.ID}

	pending, err := isPendingMergeParticipant(ctx, r.db, ld.ID)
	if err != nil {
		result.Err = err.Error()
		return result
	}
	if pending {
		return result // 顺延 (无错误): 合并终态后下轮处理
	}

	scope, err := ResolveScope(r.db, ld.ID)
	if err != nil {
		result.Err = err.Error()
		return result
	}

	oldest, err := scopeOldestTimestamp(ctx, r.db, scope)
	if err != nil {
		result.Err = err.Error()
		return result
	}
	if oldest == nil {
		return result // 无数据, 保留期不适用
	}

	now := r.now()
	expiry := oldest.Add(time.Duration(ld.RetentionDays) * 24 * time.Hour)
	remaining := expiry.Sub(now)

	switch {
	case remaining > 30*24*time.Hour:
		// 未临期: 无需通知或清理。
	case remaining > 7*24*time.Hour:
		if sent := r.notifyExpiry(ctx, ld, retentionNoticeTitle30, 30, remaining); sent {
			result.NotifiedTier = 30
		}
	case remaining > 0:
		if sent := r.notifyExpiry(ctx, ld, retentionNoticeTitle7, 7, remaining); sent {
			result.NotifiedTier = 7
		}
	default:
		// 到期: 分批硬删 (§4.3 任务 1)。通知窗口已过, 文案已明确告知
		// 不可恢复; 此处只执行删除。
		deleted, derr := r.deleteExpired(ctx, scope, ld.RetentionDays, now)
		if derr != nil {
			result.Err = derr.Error()
			return result
		}
		result.RowsDeleted = deleted
	}
	return result
}

// notifyExpiry sends the tier notification unless the same (device, tier)
// notification was already sent within the tier window (每日任务去重)。
// Returns true when a notification was created.
func (r *RetentionTask) notifyExpiry(ctx context.Context, ld *models.LogicalDevice, title string, tierDays int, remaining time.Duration) bool {
	sourceID := strconv.FormatUint(uint64(ld.ID), 10)
	windowStart := r.now().Add(-time.Duration(tierDays) * 24 * time.Hour)
	var existing int64
	if err := r.db.WithContext(ctx).Model(&models.Notification{}).
		Where("source = ? AND source_id = ? AND title = ? AND created_at > ?",
			NotificationSourceRetentionExpiring, sourceID, title, windowStart).
		Count(&existing).Error; err != nil {
		slog.Warn("datalifecycle: retention notice dedup query failed",
			"logical_id", ld.ID, "error", err)
		return false
	}
	if existing > 0 {
		return false
	}
	daysLeft := int(remaining.Hours()/24) + 1
	n := models.Notification{
		Type:        "warning",
		Title:       title,
		Message:     fmt.Sprintf("逻辑设备「%s」（%s）最早的数据将在约 %d 天后到期硬删除，删除后不可恢复。", ld.Name, ld.DeviceType, daysLeft),
		Description: fmt.Sprintf("如需保留更久，请到「逻辑设备」管理页调大该设备的保留天数（当前 %d 天）。", ld.RetentionDays),
		Source:      NotificationSourceRetentionExpiring,
		SourceID:    sourceID,
	}
	if err := r.db.WithContext(ctx).Create(&n).Error; err != nil {
		slog.Error("datalifecycle: create retention notification failed",
			"logical_id", ld.ID, "error", err)
		return false
	}
	return true
}

// deleteExpired batch-deletes rows older than the retention cutoff.
// Same batching strategy as purge (§4.3 锁交互说明).
func (r *RetentionTask) deleteExpired(ctx context.Context, scope *Scope, retentionDays int, now time.Time) (int64, error) {
	batchSize := r.batchSize
	if batchSize <= 0 {
		batchSize = purgeBatchSizePostgres
		if r.db.Dialector != nil && r.db.Dialector.Name() != "postgres" {
			batchSize = purgeBatchSizeSQLite
		}
	}
	cutoff := now.Add(-time.Duration(retentionDays) * 24 * time.Hour)
	cond, args := scope.Cond()

	var total int64
	for _, table := range []string{"unified_data", "device_data"} {
		for batch := 0; batch < maxPurgeBatches; batch++ {
			var affected int64
			err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				res := tx.Exec(
					fmt.Sprintf("DELETE FROM %s WHERE id IN (SELECT id FROM %s WHERE %s AND timestamp < ? LIMIT ?)", table, table, cond),
					append(append([]interface{}{}, args...), cutoff, batchSize)...,
				)
				affected = res.RowsAffected
				return res.Error
			})
			if err != nil {
				return total, fmt.Errorf("retention delete %s: %w", table, err)
			}
			total += affected
			if affected < int64(batchSize) {
				break // 本表到期行删尽
			}
			if r.batchSleep > 0 {
				select {
				case <-ctx.Done():
					return total, ctx.Err()
				case <-time.After(r.batchSleep):
				}
			}
		}
	}
	return total, nil
}

// scopeOldestTimestamp returns MIN(timestamp) across both data tables for
// the scope, or nil when the scope holds no rows.
func scopeOldestTimestamp(ctx context.Context, db *gorm.DB, scope *Scope) (*time.Time, error) {
	cond, args := scope.Cond()
	var oldest *time.Time
	for _, table := range []string{"unified_data", "device_data"} {
		var lo *time.Time
		err := db.WithContext(ctx).Raw(
			fmt.Sprintf("SELECT MIN(timestamp) FROM %s WHERE %s", table, cond),
			args...,
		).Row().Scan(&lo)
		if err != nil {
			return nil, fmt.Errorf("oldest timestamp of %s: %w", table, err)
		}
		if lo != nil && (oldest == nil || lo.Before(*oldest)) {
			oldest = lo
		}
	}
	return oldest, nil
}
