package datalifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
)

// 搬迁批次参数 (§4.3 锁交互说明, 与 purge/backfill 同策略):
// PostgreSQL 每批 1 万行独立事务; SQLite (测试环境) 库级写锁缩小至 1 千行。
const (
	migrateBatchSizePostgres = 10000
	migrateBatchSizeSQLite   = 1000
	migrateBatchSleep        = 200 * time.Millisecond
	// maxMergeRetries — v3.2-F3: 重试 3 次超限才 failed + 源复位。
	maxMergeRetries = 3
)

// Notification.Source 字段约定 (§六前端清单 D-2 路由扩展):
//   - merge_failed      合并搬迁失败 (SourceID = merge_jobs.id)
//   - retention_expiring 保留期临期提醒 (SourceID = logical_devices.id)
const (
	NotificationSourceMergeFailed       = "merge_failed"
	NotificationSourceRetentionExpiring = "retention_expiring"
)

// MigrateResult reports one merge job's migration outcome for a run.
type MigrateResult struct {
	JobID        uint   `json:"job_id"`
	SourceID     uint   `json:"source_id"`
	TargetID     uint   `json:"target_id"`
	RowsMigrated int64  `json:"rows_migrated"` // 本次运行新增搬迁行数
	Done         bool   `json:"done"`
	Failed       bool   `json:"failed"`
	Err          string `json:"error,omitempty"`
}

// Migrator is the §4.3 任务 3 worker: 把 merge_status='pending' 源的
// unified_data/device_data 行搬迁到目标 logical_device。按 id 范围分批
// (每批独立事务), 水位持久化在 merge_jobs 支持断点续跑; 失败发
// Notification 并按水位重试, 超限 (3 次) 才 failed + 源复位 + 再通知
// (v3.2-F3)。生命周期同 Purger: Start once, Stop on shutdown。
type Migrator struct {
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

// NewMigrator creates the merge-migration worker: first run 10s after
// startup (合并请求后尽快开工), then every 60s picking up pending jobs.
func NewMigrator(db *gorm.DB) *Migrator {
	return &Migrator{
		db:           db,
		interval:     60 * time.Second,
		initialDelay: 10 * time.Second,
		batchSleep:   migrateBatchSleep,
		stopCh:       make(chan struct{}),
	}
}

// SetSchedule overrides timing (tests only).
func (m *Migrator) SetSchedule(interval, initialDelay, batchSleep time.Duration) {
	if interval > 0 {
		m.interval = interval
	}
	if initialDelay > 0 {
		m.initialDelay = initialDelay
	}
	m.batchSleep = batchSleep // 0 allowed in tests
}

// SetBatchSize overrides the per-transaction batch size (tests only).
func (m *Migrator) SetBatchSize(n int) {
	if n > 0 {
		m.batchSize = n
	}
}

// Start launches the migrator goroutine once.
func (m *Migrator) Start() {
	m.startOnce.Do(func() {
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.run()
		}()
	})
}

// Stop signals the goroutine to exit and waits for it.
func (m *Migrator) Stop() {
	m.stopOnce.Do(func() { close(m.stopCh) })
	m.wg.Wait()
}

func (m *Migrator) run() {
	timer := time.NewTimer(m.initialDelay)
	defer timer.Stop()
	select {
	case <-timer.C:
		m.runOnceLogged()
	case <-m.stopCh:
		return
	}
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			m.runOnceLogged()
		case <-m.stopCh:
			return
		}
	}
}

func (m *Migrator) runOnceLogged() {
	results, err := m.RunOnce(context.Background())
	if err != nil {
		slog.Warn("datalifecycle: merge migration run failed", "error", err)
		return
	}
	for _, r := range results {
		switch {
		case r.Done:
			slog.Info("datalifecycle: merge migration done",
				"job_id", r.JobID, "source", r.SourceID, "target", r.TargetID,
				"rows_migrated", r.RowsMigrated)
		case r.Failed:
			slog.Warn("datalifecycle: merge migration failed permanently",
				"job_id", r.JobID, "source", r.SourceID, "error", r.Err)
		case r.Err != "":
			slog.Warn("datalifecycle: merge migration attempt failed (will retry)",
				"job_id", r.JobID, "source", r.SourceID, "error", r.Err)
		}
	}
}

// RunOnce processes every pending/running merge job and returns per-job
// outcomes. It is the testable core of the worker.
func (m *Migrator) RunOnce(ctx context.Context) ([]MigrateResult, error) {
	var jobs []models.MergeJob
	if err := m.db.WithContext(ctx).
		Where("status IN ?", []string{models.MergeJobPending, models.MergeJobRunning}).
		Order("id").
		Find(&jobs).Error; err != nil {
		return nil, fmt.Errorf("datalifecycle: scan merge jobs: %w", err)
	}
	results := make([]MigrateResult, 0, len(jobs))
	for i := range jobs {
		select {
		case <-ctx.Done():
			return results, ctx.Err()
		default:
		}
		results = append(results, m.migrateOne(ctx, &jobs[i]))
	}
	return results, nil
}

// migrateOne moves one source's data rows to the target in batched
// independent transactions with persisted watermark (断点续跑).
// Context cancellation (shutdown) is NOT counted as a retry attempt —
// the job stays running and resumes at the watermark next run.
func (m *Migrator) migrateOne(ctx context.Context, job *models.MergeJob) MigrateResult {
	result := MigrateResult{JobID: job.ID, SourceID: job.SourceLogicalID, TargetID: job.TargetLogicalID}

	// pending → running (幂等: 已在 running 的作业续跑)。
	if job.Status == models.MergeJobPending {
		if err := m.db.WithContext(ctx).Model(&models.MergeJob{}).
			Where("id = ?", job.ID).
			Update("status", models.MergeJobRunning).Error; err != nil {
			result.Err = fmt.Sprintf("mark running: %v", err)
			if !contextDone(ctx) {
				m.failAttempt(ctx, job, &result, err)
			}
			return result
		}
		job.Status = models.MergeJobRunning
	}

	// 源数据范围与查询协议同一 helper (§4.3 v3.1-Q2): 含 NULL-logical
	// 旧行的实例兜底, 且对源自身的入边 pending 源做传递 UNION。
	// 注意: 只处理 merge_status='pending' 的源 (§4.3 任务 3)——上游若
	// 已复位 (failed 后) 则其作业不再匹配 pending/running 状态。
	scope, err := ResolveScope(m.db, job.SourceLogicalID)
	if err != nil {
		result.Err = err.Error()
		if !contextDone(ctx) {
			m.failAttempt(ctx, job, &result, err)
		}
		return result
	}

	phases := []string{models.MergePhaseUnifiedData, models.MergePhaseDeviceData}
	startIdx := 0
	if job.WatermarkPhase == models.MergePhaseDeviceData {
		startIdx = 1
	}
	for idx := startIdx; idx < len(phases); idx++ {
		table := phases[idx]
		if idx != startIdx {
			// phase 切换: 水位归零, 持久化后续跑不丢阶段。
			job.WatermarkID = 0
			job.WatermarkPhase = table
			if err := m.db.WithContext(ctx).Model(&models.MergeJob{}).
				Where("id = ?", job.ID).
				Updates(map[string]interface{}{"watermark_id": 0, "watermark_phase": table}).Error; err != nil {
				result.Err = fmt.Sprintf("switch phase to %s: %v", table, err)
				if !contextDone(ctx) {
					m.failAttempt(ctx, job, &result, err)
				}
				return result
			}
		}
		migrated, err := m.migrateTable(ctx, job, table, scope, &result)
		result.RowsMigrated += migrated
		if err != nil {
			result.Err = fmt.Sprintf("migrate %s: %v", table, err)
			if !contextDone(ctx) {
				m.failAttempt(ctx, job, &result, err)
			}
			return result
		}
	}

	// 两表扫尽: 同事务内置 job done + 源 merge_status=done。
	now := time.Now()
	err = m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.MergeJob{}).Where("id = ?", job.ID).
			Updates(map[string]interface{}{"status": models.MergeJobDone, "finished_at": now}).Error; err != nil {
			return err
		}
		return tx.Model(&models.LogicalDevice{}).Where("id = ?", job.SourceLogicalID).
			Update("merge_status", models.MergeStatusDone).Error
	})
	if err != nil {
		result.Err = fmt.Sprintf("finish merge: %v", err)
		if !contextDone(ctx) {
			m.failAttempt(ctx, job, &result, err)
		}
		return result
	}
	result.Done = true
	return result
}

// contextDone reports ctx cancellation — shutdown interruptions must not
// consume the retry budget (断点续跑语义).
func contextDone(ctx context.Context) bool {
	return errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded)
}

// migrateTable batch-moves one table's source rows to the target.
// Returns rows migrated this run; the watermark persists per batch so a
// crash resumes at the last completed window (断点续跑, §4.3)。
func (m *Migrator) migrateTable(ctx context.Context, job *models.MergeJob, table string, scope *Scope, result *MigrateResult) (int64, error) {
	batchSize := m.batchSize
	if batchSize <= 0 {
		batchSize = migrateBatchSizePostgres
		if m.db.Dialector != nil && m.db.Dialector.Name() != "postgres" {
			batchSize = migrateBatchSizeSQLite
		}
	}
	maxID, err := tableMaxID(ctx, m.db, table)
	if err != nil {
		return 0, err
	}
	cond, args := scope.Cond()

	watermark := job.WatermarkID
	var migrated int64
	for watermark < maxID {
		select {
		case <-ctx.Done():
			return migrated, ctx.Err()
		default:
		}
		windowEnd := watermark + int64(batchSize)
		if windowEnd > maxID {
			windowEnd = maxID
		}

		var affected int64
		err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			// 批内原子: 行搬迁 + 水位推进同事务——崩溃时整批回滚,
			// 重试窗口内的行仍属源 scope, UPDATE 幂等不重复计数。
			res := tx.Exec(
				fmt.Sprintf("UPDATE %s SET logical_device_id = ? WHERE id > ? AND id <= ? AND %s", table, cond),
				append([]interface{}{job.TargetLogicalID, watermark, windowEnd}, args...)...,
			)
			if res.Error != nil {
				return res.Error
			}
			affected = res.RowsAffected
			return tx.Model(&models.MergeJob{}).Where("id = ?", job.ID).
				Updates(map[string]interface{}{
					"watermark_id":  windowEnd,
					"migrated_rows": gorm.Expr("migrated_rows + ?", affected),
				}).Error
		})
		if err != nil {
			return migrated, fmt.Errorf("batch %d..%d: %w", watermark, windowEnd, err)
		}
		migrated += affected
		watermark = windowEnd

		if watermark >= maxID {
			break
		}
		if m.batchSleep > 0 {
			select {
			case <-ctx.Done():
				return migrated, ctx.Err()
			case <-time.After(m.batchSleep):
			}
		}
	}
	return migrated, nil
}

// failAttempt applies the v3.2-F3 failure policy: retry_count++,
// notification on first failure, and after maxMergeRetries permanent
// failure — job failed, source merged_into/merge_status reset, final
// notification. Watermark stays put so a retry resumes where it stopped.
func (m *Migrator) failAttempt(ctx context.Context, job *models.MergeJob, result *MigrateResult, cause error) {
	retries := job.RetryCount + 1
	job.RetryCount = retries

	if retries >= maxMergeRetries {
		now := time.Now()
		txErr := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&models.MergeJob{}).Where("id = ?", job.ID).
				Updates(map[string]interface{}{
					"status":      models.MergeJobFailed,
					"retry_count": retries,
					"finished_at": now,
				}).Error; err != nil {
				return err
			}
			// 源复位: merged_into=NULL, merge_status=NULL (§3.4)。
			return tx.Model(&models.LogicalDevice{}).Where("id = ?", job.SourceLogicalID).
				Updates(map[string]interface{}{"merged_into": nil, "merge_status": nil}).Error
		})
		if txErr != nil {
			slog.Error("datalifecycle: failed to finalize merge failure",
				"job_id", job.ID, "error", txErr)
			return
		}
		result.Failed = true
		m.notify(ctx, models.Notification{
			Type:        "error",
			Title:       "数据合并失败（已放弃重试）",
			Message:     fmt.Sprintf("逻辑设备 #%d 的数据搬迁重试 %d 次后仍失败: %v。源已复位，可重新发起合并。", job.SourceLogicalID, retries, cause),
			Description: "请在逻辑设备管理页查看并重试合并。",
			Source:      NotificationSourceMergeFailed,
			SourceID:    strconv.FormatUint(uint64(job.ID), 10),
		})
		return
	}

	// 未超限: 保留水位, 下次运行续跑; 首次失败发通知 (避免每轮重复通知)。
	if err := m.db.WithContext(ctx).Model(&models.MergeJob{}).Where("id = ?", job.ID).
		Update("retry_count", retries).Error; err != nil {
		slog.Error("datalifecycle: failed to bump merge retry_count",
			"job_id", job.ID, "error", err)
	}
	if retries == 1 {
		m.notify(ctx, models.Notification{
			Type:        "warning",
			Title:       "数据合并搬迁受阻",
			Message:     fmt.Sprintf("逻辑设备 #%d 的数据搬迁失败: %v。将按水位自动重试（剩余 %d 次）。", job.SourceLogicalID, cause, maxMergeRetries-retries),
			Description: "若持续失败将在重试耗尽后放弃并复位源设备。",
			Source:      NotificationSourceMergeFailed,
			SourceID:    strconv.FormatUint(uint64(job.ID), 10),
		})
	}
}

// notify persists a Notification row (best-effort; notification failure
// must not mask the underlying error).
func (m *Migrator) notify(ctx context.Context, n models.Notification) {
	if err := m.db.WithContext(ctx).Create(&n).Error; err != nil {
		slog.Error("datalifecycle: create notification failed",
			"source", n.Source, "error", err)
	}
}
