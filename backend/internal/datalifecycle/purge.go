package datalifecycle

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
)

// Batch sizes per §4.3 锁交互说明: PostgreSQL 每批 1 万行独立事务;
// SQLite (测试环境) 写锁为库级, 缩小至 1 千行。
const (
	purgeBatchSizePostgres = 10000
	purgeBatchSizeSQLite   = 1000
	// purgeBatchSleep spaces batches to avoid WAL bloat / replica lag.
	purgeBatchSleep = 200 * time.Millisecond
	// maxPurgeBatches bounds one logical device's purge loop (safety net).
	maxPurgeBatches = 100000
)

// PurgeOutcome classifies what happened to one logical device this run.
type PurgeOutcome string

const (
	// Purged — data hard-deleted, FK edges dissolved, logical_device row removed.
	Purged PurgeOutcome = "purged"
	// AbandonedLivingInstance — v3.3-N1 守卫: 已有存活实例, 放弃 purge 并清标志。
	AbandonedLivingInstance PurgeOutcome = "abandoned_living_instance"
	// DeferredPendingMerge — v3.3-N1 守卫: 是某 pending 合并的目标/源, 顺延。
	DeferredPendingMerge PurgeOutcome = "deferred_pending_merge"
	// PurgeFailed — 数据删除失败 (保留标志, 下次重试)。
	PurgeFailed PurgeOutcome = "failed"
)

// PurgeResult reports one logical device's purge outcome.
type PurgeResult struct {
	LogicalID   uint         `json:"logical_id"`
	Outcome     PurgeOutcome `json:"outcome"`
	RowsDeleted int64        `json:"rows_deleted"`
	Error       string       `json:"error,omitempty"`
}

// Purger is the daily purge background task (方案 §4.3 任务 2). It follows
// the logstream.LogCleanup goroutine pattern: Start once, Stop on shutdown.
type Purger struct {
	db           *gorm.DB
	interval     time.Duration
	initialDelay time.Duration
	batchSleep   time.Duration
	batchSize    int // 0 → dialect default (PG 1万 / SQLite 1千)
	startOnce    sync.Once
	stopCh       chan struct{}
	stopOnce     sync.Once
	wg           sync.WaitGroup
}

// NewPurger creates the purge worker with defaults: first run 30s after
// startup, then every 24h.
func NewPurger(db *gorm.DB) *Purger {
	return &Purger{
		db:           db,
		interval:     24 * time.Hour,
		initialDelay: 30 * time.Second,
		batchSleep:   purgeBatchSleep,
		stopCh:       make(chan struct{}),
	}
}

// SetSchedule overrides timing (tests only).
func (p *Purger) SetSchedule(interval, initialDelay, batchSleep time.Duration) {
	if interval > 0 {
		p.interval = interval
	}
	if initialDelay > 0 {
		p.initialDelay = initialDelay
	}
	p.batchSleep = batchSleep // 0 allowed in tests
}

// SetBatchSize overrides the per-transaction delete batch size (tests only).
func (p *Purger) SetBatchSize(n int) {
	if n > 0 {
		p.batchSize = n
	}
}

// Start launches the purge goroutine once.
func (p *Purger) Start() {
	p.startOnce.Do(func() {
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			p.run()
		}()
	})
}

// Stop signals the goroutine to exit and waits for it.
func (p *Purger) Stop() {
	p.stopOnce.Do(func() { close(p.stopCh) })
	p.wg.Wait()
}

func (p *Purger) run() {
	// First run shortly after startup so freshly requested purges don't
	// wait a full day; then daily.
	timer := time.NewTimer(p.initialDelay)
	defer timer.Stop()
	select {
	case <-timer.C:
		p.runOnceLogged()
	case <-p.stopCh:
		return
	}
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			p.runOnceLogged()
		case <-p.stopCh:
			return
		}
	}
}

func (p *Purger) runOnceLogged() {
	results, err := p.RunOnce(context.Background())
	if err != nil {
		slog.Warn("datalifecycle: purge run failed", "error", err)
		return
	}
	for _, r := range results {
		switch r.Outcome {
		case Purged:
			slog.Info("datalifecycle: purged logical device",
				"logical_id", r.LogicalID, "rows_deleted", r.RowsDeleted)
		case AbandonedLivingInstance:
			slog.Info("datalifecycle: purge abandoned (living instance reappeared)",
				"logical_id", r.LogicalID)
		case DeferredPendingMerge:
			slog.Info("datalifecycle: purge deferred (pending merge)",
				"logical_id", r.LogicalID)
		case PurgeFailed:
			slog.Warn("datalifecycle: purge failed",
				"logical_id", r.LogicalID, "error", r.Error)
		}
	}
}

// RunOnce processes every logical device with purge_requested=TRUE and
// returns per-device outcomes. It is the testable core of the worker.
func (p *Purger) RunOnce(ctx context.Context) ([]PurgeResult, error) {
	var targets []models.LogicalDevice
	if err := p.db.WithContext(ctx).
		Where("purge_requested = ?", true).
		Order("id").
		Find(&targets).Error; err != nil {
		return nil, fmt.Errorf("datalifecycle: scan purge_requested: %w", err)
	}
	results := make([]PurgeResult, 0, len(targets))
	for i := range targets {
		select {
		case <-ctx.Done():
			return results, ctx.Err()
		default:
		}
		results = append(results, p.purgeOne(ctx, &targets[i]))
	}
	return results, nil
}

// purgeOne applies the v3.3-N1 guards then hard-deletes the logical
// device's data in batches and tears down references in FK-safe order.
func (p *Purger) purgeOne(ctx context.Context, ld *models.LogicalDevice) PurgeResult {
	result := PurgeResult{LogicalID: ld.ID}

	// 守卫 1: 已有存活实例 → 放弃 purge 并清标志。重建行为本身即撤回删除
	// 意图, 此时删数据 = 静默清空用户刚选择继承的历史 (v3.3-N1)。
	living, err := countLivingInstances(p.db, ld.ID)
	if err != nil {
		result.Outcome = PurgeFailed
		result.Error = err.Error()
		return result
	}
	if living > 0 {
		if err := p.db.WithContext(ctx).Model(&models.LogicalDevice{}).
			Where("id = ?", ld.ID).
			Update("purge_requested", false).Error; err != nil {
			result.Outcome = PurgeFailed
			result.Error = fmt.Sprintf("clear purge_requested: %v", err)
			return result
		}
		result.Outcome = AbandonedLivingInstance
		return result
	}

	// 守卫 2: 是某 pending 合并的目标或源 → 顺延至合并终态后再执行。
	pendingTarget, err := p.isPendingMergeParticipant(ctx, ld.ID)
	if err != nil {
		result.Outcome = PurgeFailed
		result.Error = err.Error()
		return result
	}
	if pendingTarget {
		result.Outcome = DeferredPendingMerge
		return result
	}

	scope, err := ResolveScope(p.db, ld.ID)
	if err != nil {
		result.Outcome = PurgeFailed
		result.Error = err.Error()
		return result
	}

	// 分批硬删 unified_data / device_data (§4.3: 每批独立事务 + 批间 sleep)。
	deleted, err := p.deleteScopedRows(ctx, scope)
	if err != nil {
		result.Outcome = PurgeFailed
		result.Error = err.Error()
		return result
	}
	result.RowsDeleted = deleted

	// 完成后清理 (§4.3 任务 2 + §2.5):
	// 1. calibration_cache 孤儿行 (数据已清除的实例的工厂校准值)。
	if len(scope.InstanceIDs) > 0 {
		if err := p.db.WithContext(ctx).
			Where("edge_device_id IN ?", scope.InstanceIDs).
			Delete(&models.CalibrationCache{}).Error; err != nil {
			result.Outcome = PurgeFailed
			result.Error = fmt.Sprintf("clean calibration_cache: %v", err)
			return result
		}
	}
	// 2. F6 出边: 解除软删实例的 FK 引用 (Unscoped 含软删实例)。
	if err := p.db.WithContext(ctx).Unscoped().
		Model(&models.EdgeDevice{}).
		Where("logical_device_id = ?", ld.ID).
		Update("logical_device_id", nil).Error; err != nil {
		result.Outcome = PurgeFailed
		result.Error = fmt.Sprintf("detach edge_devices (F6): %v", err)
		return result
	}
	// 3. B4 入边: 解除已完成源的 merged_into 指向。
	if err := p.db.WithContext(ctx).
		Model(&models.LogicalDevice{}).
		Where("merged_into = ?", ld.ID).
		Updates(map[string]interface{}{"merged_into": nil, "merge_status": nil}).Error; err != nil {
		result.Outcome = PurgeFailed
		result.Error = fmt.Sprintf("detach merge sources (B4): %v", err)
		return result
	}
	// 4. DELETE logical_device 行。
	if err := p.db.WithContext(ctx).
		Delete(&models.LogicalDevice{}, ld.ID).Error; err != nil {
		result.Outcome = PurgeFailed
		result.Error = fmt.Sprintf("delete logical_device row: %v", err)
		return result
	}
	result.Outcome = Purged
	return result
}

// isPendingMergeParticipant reports whether the logical device is the target
// of any pending merge or itself a pending source.
func (p *Purger) isPendingMergeParticipant(ctx context.Context, logicalID uint) (bool, error) {
	var incoming int64
	if err := p.db.WithContext(ctx).Model(&models.LogicalDevice{}).
		Where("merged_into = ? AND merge_status = ?", logicalID, models.MergeStatusPending).
		Count(&incoming).Error; err != nil {
		return false, fmt.Errorf("check pending merge target: %w", err)
	}
	if incoming > 0 {
		return true, nil
	}
	var self models.LogicalDevice
	if err := p.db.WithContext(ctx).First(&self, logicalID).Error; err != nil {
		return false, fmt.Errorf("reload logical_device %d: %w", logicalID, err)
	}
	if self.MergedInto != nil && self.MergeStatus != nil && *self.MergeStatus == models.MergeStatusPending {
		return true, nil
	}
	return false, nil
}

// deleteScopedRows batch-deletes all rows in unified_data and device_data
// matching the scope. Each batch is an independent transaction sized by
// dialect; batches sleep between each other to limit WAL pressure.
func (p *Purger) deleteScopedRows(ctx context.Context, scope *Scope) (int64, error) {
	batchSize := p.batchSize
	if batchSize <= 0 {
		batchSize = purgeBatchSizePostgres
		if p.db.Dialector != nil && p.db.Dialector.Name() != "postgres" {
			batchSize = purgeBatchSizeSQLite
		}
	}
	cond, args := scope.Cond()
	var total int64
	for _, table := range []string{"unified_data", "device_data"} {
		for batch := 0; batch < maxPurgeBatches; batch++ {
			var affected int64
			err := p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				// DELETE ... WHERE id IN (SELECT id ... LIMIT ?): deterministic
				// batch slicing that works on both PostgreSQL and SQLite.
				res := tx.Exec(
					fmt.Sprintf("DELETE FROM %s WHERE id IN (SELECT id FROM %s WHERE %s LIMIT ?)", table, table, cond),
					append(append([]interface{}{}, args...), batchSize)...,
				)
				affected = res.RowsAffected
				return res.Error
			})
			if err != nil {
				return total, fmt.Errorf("batch delete %s: %w", table, err)
			}
			total += affected
			if affected < int64(batchSize) {
				break // table drained
			}
			if p.batchSleep > 0 {
				select {
				case <-ctx.Done():
					return total, ctx.Err()
				case <-time.After(p.batchSleep):
				}
			}
		}
	}
	return total, nil
}
