package datalifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
)

// 大表回填批次参数 (方案 §七-2 / §4.3 锁交互说明)。
// PostgreSQL 每批 1 万行独立事务; SQLite (测试环境) 写锁为库级,
// 缩小至 1 千行——与 purge.go 同策略。
const (
	backfillBatchSizePostgres = 10000
	backfillBatchSizeSQLite   = 1000
	// backfillBatchSleep spaces batches to avoid WAL bloat / replica lag.
	backfillBatchSleep = 200 * time.Millisecond
	// maxBackfillBatches bounds one table's backfill loop (safety net).
	maxBackfillBatches = 100000
)

// backfillTables lists the tables the §七 M step backfills
// (logical_device_id by device_id).
var backfillTables = []string{"unified_data", "device_data"}

// BackfillResult reports one table's backfill outcome for a run.
type BackfillResult struct {
	Table        string `json:"table"`
	RowsUpdated  int64  `json:"rows_updated"`
	Batches      int64  `json:"batches"`
	ResumedFrom  int64  `json:"resumed_from"` // watermark at run start (0 = fresh)
	RowsMissing  int64  `json:"rows_missing"` // 校验自检: 仍 NULL 且实例有逻辑身份的行数
	VerifyPassed bool   `json:"verify_passed"`
	Err          string `json:"error,omitempty"`
}

// Backfiller is the §七 M 迁移步骤 worker: 分批回填 unified_data /
// device_data 的 logical_device_id, 水位持久化支持断点续跑 (§4.3)。
// 生命周期同 Purger: Start once, Stop on shutdown。
//
// 启动钩子 (main.go) 只调 RunOnce 一次; 回填是幂等断点续跑的一次性
// 迁移, 完成 (status=done) 后重启秒级短路, 无需周期重扫, 不做
// interval 循环 (避免为一次性迁移建常驻机制——复杂度预算)。
type Backfiller struct {
	db         *gorm.DB
	batchSleep time.Duration
	batchSize  int // 0 → dialect default (PG 1万 / SQLite 1千)
	startOnce  sync.Once
	stopCh     chan struct{}
	stopOnce   sync.Once
	wg         sync.WaitGroup
}

// NewBackfiller creates the backfill worker.
func NewBackfiller(db *gorm.DB) *Backfiller {
	return &Backfiller{
		db:         db,
		batchSleep: backfillBatchSleep,
		stopCh:     make(chan struct{}),
	}
}

// SetBatchSleep overrides the inter-batch sleep (tests only; 0 disables).
func (b *Backfiller) SetBatchSleep(d time.Duration) { b.batchSleep = d }

// SetBatchSize overrides the per-transaction update batch size (tests only).
func (b *Backfiller) SetBatchSize(n int) {
	if n > 0 {
		b.batchSize = n
	}
}

// Start launches the one-shot M 迁移 goroutine: 先建复合索引 (§1.1),
// 再分批回填 (§七)。整体异步——大表索引构建与回填墙钟都随存量数据量
// 增长, 不得阻塞服务启动; 两者都幂等, 失败下次启动重试
// (interrupt-safe; progress is watermarked, next startup resumes)。
func (b *Backfiller) Start() {
	b.startOnce.Do(func() {
		b.wg.Add(1)
		go func() {
			defer b.wg.Done()
			select {
			case <-b.stopCh:
				return
			default:
			}
			ctx := context.Background()
			// §1.1 复合索引: PG CONCURRENTLY (不阻塞写入), 失败仅告警
			// 不阻断回填——回填查询仍可走 device_id 索引。
			if err := EnsureLogicalDataIndexes(ctx, b.db); err != nil {
				slog.Warn("datalifecycle: index creation failed (retry next startup)",
					"error", err)
			}
			results, err := b.RunOnce(ctx)
			if err != nil {
				slog.Warn("datalifecycle: backfill run failed", "error", err)
				return
			}
			for _, r := range results {
				if r.Err != "" {
					slog.Warn("datalifecycle: backfill table failed",
						"table", r.Table, "error", r.Err)
					continue
				}
				if !r.VerifyPassed {
					slog.Warn("datalifecycle: backfill verify FAILED",
						"table", r.Table, "rows_missing", r.RowsMissing)
					continue
				}
				slog.Info("datalifecycle: backfill table done",
					"table", r.Table, "rows_updated", r.RowsUpdated,
					"batches", r.Batches, "resumed_from", r.ResumedFrom)
			}
		}()
	})
}

// Stop signals the goroutine to exit and waits for it.
func (b *Backfiller) Stop() {
	b.stopOnce.Do(func() { close(b.stopCh) })
	b.wg.Wait()
}

// RunOnce backfills both tables sequentially and returns per-table results.
// 每表独立: 单表失败记入该表 result.Err, 不阻塞另一表。
func (b *Backfiller) RunOnce(ctx context.Context) ([]BackfillResult, error) {
	results := make([]BackfillResult, 0, len(backfillTables))
	for _, table := range backfillTables {
		select {
		case <-ctx.Done():
			results = append(results, BackfillResult{Table: table, Err: ctx.Err().Error()})
			return results, ctx.Err()
		default:
		}
		results = append(results, b.backfillTable(ctx, table))
	}
	return results, nil
}

// backfillTable resumes one table's backfill from its watermark, batch by
// batch, then runs the §七 校验自检.
//
// 终止条件是**位置**而非 affected 计数: watermark >= maxID (本次运行的
// 起始 id 快照) 才算扫完。UPDATE 的幂等条件 (logical_device_id IS NULL)
// 会让已回填窗口 affected=0, 若按 affected<batchSize 判停, 稀疏/重复
// 窗口会在到达表尾前误判完成、漏掉后面的行。
func (b *Backfiller) backfillTable(ctx context.Context, table string) BackfillResult {
	res := BackfillResult{Table: table}

	job, err := ensureBackfillJob(ctx, b.db, models.BackfillJobTypeLogicalID, table)
	if err != nil {
		res.Err = err.Error()
		return res
	}
	res.ResumedFrom = job.WatermarkID
	if job.Status == models.BackfillStatusDone {
		// 已完成过: 仍做校验自检 (幂等且廉价), 不重扫数据。
		missing, verr := VerifyLogicalBackfill(ctx, b.db, table, job.WatermarkID)
		if verr != nil {
			res.Err = verr.Error()
			return res
		}
		res.RowsMissing = missing
		res.VerifyPassed = missing == 0
		return res
	}

	batchSize := b.batchSize
	if batchSize <= 0 {
		batchSize = backfillBatchSizePostgres
		if b.db.Dialector != nil && b.db.Dialector.Name() != "postgres" {
			batchSize = backfillBatchSizeSQLite
		}
	}

	// 回填范围上界: 本次运行开始时表内最大 id。之后新写入的行不在回填
	// 范围——M→P1 窗口期新行 logical_device_id 仍为 NULL 是预期状态,
	// 由 §六 dataScopeCondition OR 安全网兜住, P1 双写激活后自然消失。
	maxID, err := tableMaxID(ctx, b.db, table)
	if err != nil {
		res.Err = fmt.Sprintf("backfill %s: %v", table, err)
		return res
	}

	watermark := job.WatermarkID
	var batches int64
	var updated int64
	for watermark < maxID {
		select {
		case <-ctx.Done():
			// 中断: 水位已按批推进, 下次启动续跑 (id > watermark)。
			res.Err = ctx.Err().Error()
			res.RowsUpdated = updated
			res.Batches = batches
			return res
		default:
		}

		// 窗口末端钳制到 maxID: 水位语义是"已扫描/校验到的 id 上界",
		// 末端窗口越过 maxID 会把之后新写入的行 (M→P1 窗口期, §六 OR
		// 安全网覆盖) 错误地圈进校验范围。
		windowEnd := watermark + int64(batchSize)
		if windowEnd > maxID {
			windowEnd = maxID
		}

		newWM, affected, err := b.runOneBackfillBatch(ctx, table, watermark, windowEnd)
		if err != nil {
			res.Err = fmt.Sprintf("backfill %s: %v", table, err)
			res.RowsUpdated = updated
			res.Batches = batches
			return res
		}
		updated += affected
		batches++
		watermark = newWM

		// 水位推进: 条件 UPDATE (乐观锁) + 累加计数分开。多副本并发时
		// 只有一个能推进水位; 另一副本的推进是 no-op (不回退), 其行级
		// UPDATE 因 IS NULL 幂等条件 affected=0, 不重复计数。
		if err := advanceBackfillWatermark(ctx, b.db, job, newWM); err != nil {
			res.Err = fmt.Sprintf("advance watermark %s: %v", table, err)
			res.RowsUpdated = updated
			res.Batches = batches
			return res
		}
		if affected > 0 {
			if err := b.db.WithContext(ctx).Model(&models.BackfillJob{}).
				Where("id = ?", job.ID).
				UpdateColumns(map[string]interface{}{
					"rows_updated": gorm.Expr("rows_updated + ?", affected),
					"batch_count":  gorm.Expr("batch_count + 1"),
				}).Error; err != nil {
				res.Err = fmt.Sprintf("accumulate counters %s: %v", table, err)
				res.RowsUpdated = updated
				res.Batches = batches
				return res
			}
		}

		if batches >= maxBackfillBatches {
			res.Err = fmt.Sprintf("backfill %s: batch limit %d exceeded (watermark %d/%d)",
				table, maxBackfillBatches, watermark, maxID)
			res.RowsUpdated = updated
			res.Batches = batches
			return res
		}
		if watermark >= maxID {
			break // 扫尽本次运行范围
		}
		if b.batchSleep > 0 {
			select {
			case <-ctx.Done():
				res.Err = ctx.Err().Error()
				res.RowsUpdated = updated
				res.Batches = batches
				return res
			case <-time.After(b.batchSleep):
			}
		}
	}

	// 置 done (条件更新, 幂等; watermark 用 GREATEST 防并发副本回退)。
	done, err := finishBackfillJob(ctx, b.db, job, watermark)
	if err != nil {
		res.Err = fmt.Sprintf("finish job %s: %v", table, err)
		return res
	}
	if !done {
		// 并发完成竞态: 另一副本先置 done, 直接走校验。
		slog.Info("datalifecycle: backfill job finished by another replica",
			"table", table)
	}

	res.RowsUpdated = updated
	res.Batches = batches

	// 回填末尾自检 (方案 §七-3): 校验范围内 (id <= watermark) 仍 NULL 且
	// 实例存在 logical_device_id 的行应为 0。范围外的 NULL 行是 M→P1
	// 窗口期新写入 (§六 OR 安全网覆盖), 不属回填职责。
	// 非 0 → VerifyPassed=false, ehomectl 同步命令 exit 非零。
	missing, err := VerifyLogicalBackfill(ctx, b.db, table, watermark)
	if err != nil {
		res.Err = err.Error()
		return res
	}
	res.RowsMissing = missing
	res.VerifyPassed = missing == 0
	return res
}

// tableMaxID returns COALESCE(MAX(id), 0) of a data table — the backfill
// range upper bound for this run.
func tableMaxID(ctx context.Context, db *gorm.DB, table string) (int64, error) {
	var maxID int64
	err := db.WithContext(ctx).Raw(
		fmt.Sprintf("SELECT COALESCE(MAX(id), 0) FROM %s", table)).
		Scan(&maxID).Error
	if err != nil {
		return 0, fmt.Errorf("read max id of %s: %w", table, err)
	}
	return maxID, nil
}

// runOneBackfillBatch processes one id-window (watermark, windowEnd] in an
// independent transaction. 调用方负责窗口大小与末端钳制 (≤ maxID);
// 幂等条件 `logical_device_id IS NULL` 保证已回填行不重复处理。
//
// 软删实例的行同样回填: T1 启动补建 (BackfillLogicalDevices) 已对全量
// edge_devices (Unscoped, 含软删) 挂载 logical_device_id, 软删实例的历史
// 行属于该逻辑身份的数据范围 (§4.3 ResolveScope 的实例集是 Unscoped);
// backfill 完成后 scope 条件才能退化为仅 logical_device_id IN ?。Raw SQL
// 不受 GORM 软删 scope 影响, edge_devices 子查询天然 Unscoped。
func (b *Backfiller) runOneBackfillBatch(ctx context.Context, table string, watermark, windowEnd int64) (newWatermark int64, affected int64, err error) {
	newWatermark = windowEnd
	err = b.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Exec(fmt.Sprintf(`UPDATE %s
SET logical_device_id = (
    SELECT logical_device_id FROM edge_devices e
    WHERE e.id = %s.device_id
)
WHERE id > ? AND id <= ?
  AND logical_device_id IS NULL
  AND device_id IN (
    SELECT e2.id FROM edge_devices e2
    WHERE e2.logical_device_id IS NOT NULL
  )`, table, table), watermark, newWatermark)
		affected = res.RowsAffected
		return res.Error
	})
	return newWatermark, affected, err
}

// ensureBackfillJob loads or creates the watermark row for (jobType, table),
// with ON CONFLICT as a pure concurrency guard (多副本启动竞态, v3.2-F2
// 同模式) 并回读赢家。
func ensureBackfillJob(ctx context.Context, db *gorm.DB, jobType, table string) (*models.BackfillJob, error) {
	var job models.BackfillJob
	err := db.WithContext(ctx).
		Where("job_type = ? AND table_name = ?", jobType, table).
		First(&job).Error
	if err == nil {
		return &job, nil
	}
	if !isNotFound(err) {
		return nil, fmt.Errorf("lookup backfill job (%s,%s): %w", jobType, table, err)
	}
	job = models.BackfillJob{
		JobType: jobType,
		Table:   table,
		Status:  models.BackfillStatusRunning,
	}
	if err := db.WithContext(ctx).Create(&job).Error; err != nil {
		if !isUniqueViolation(err) {
			return nil, fmt.Errorf("create backfill job (%s,%s): %w", jobType, table, err)
		}
		// 并发创建竞态: 回读赢家。
		if err := db.WithContext(ctx).
			Where("job_type = ? AND table_name = ?", jobType, table).
			First(&job).Error; err != nil {
			return nil, fmt.Errorf("re-read backfill job (%s,%s): %w", jobType, table, err)
		}
	}
	return &job, nil
}

// advanceBackfillWatermark moves the watermark to newWM via a conditional
// UPDATE (乐观锁): 只有当前持有该水位值的副本能推进, 并发副本的推进
// 是 no-op (RowsAffected=0), 不会把水位回退。
func advanceBackfillWatermark(ctx context.Context, db *gorm.DB, job *models.BackfillJob, newWM int64) error {
	if newWM <= job.WatermarkID {
		return nil // nothing to advance
	}
	res := db.WithContext(ctx).Model(&models.BackfillJob{}).
		Where("id = ? AND watermark_id = ?", job.ID, job.WatermarkID).
		Update("watermark_id", newWM)
	if res.Error != nil {
		return res.Error
	}
	job.WatermarkID = newWM // local view follows our advance
	return nil
}

// finishBackfillJob flips the job to done once, conditional on still being
// running (幂等: 重复执行结果一致)。返回 false 表示别的副本先完成。
// watermark 取较大者 (CASE WHEN, PG/SQLite 双方言通用——SQLite 无
// GREATEST), 防止并发副本用更低的水位覆盖更快的副本。
func finishBackfillJob(ctx context.Context, db *gorm.DB, job *models.BackfillJob, finalWM int64) (bool, error) {
	res := db.WithContext(ctx).Model(&models.BackfillJob{}).
		Where("id = ? AND status = ?", job.ID, models.BackfillStatusRunning).
		Updates(map[string]interface{}{
			"status": models.BackfillStatusDone,
			"watermark_id": gorm.Expr(
				"CASE WHEN watermark_id > ? THEN watermark_id ELSE ? END", finalWM, finalWM),
			"finished_at": time.Now(),
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// VerifyLogicalBackfill counts rows within the verified range (id <= maxID)
// where logical_device_id is still NULL while the row's device instance
// carries a logical_device_id — those rows were eligible for backfill but
// missed. 方案 §七-3 要求为 0。
// 与 runOneBackfillBatch 的资格条件完全对称 (含软删实例), 否则校验会
// 把合法跳过的行误报为缺失。范围外的 NULL 行是 M→P1 窗口期新写入
// (§六 OR 安全网覆盖), 不属回填职责。maxID <= 0 表示空表, 直接返回 0。
func VerifyLogicalBackfill(ctx context.Context, db *gorm.DB, table string, maxID int64) (int64, error) {
	if maxID <= 0 {
		return 0, nil
	}
	var count int64
	err := db.WithContext(ctx).Raw(fmt.Sprintf(`SELECT COUNT(*) FROM %s
WHERE id <= ?
  AND logical_device_id IS NULL
  AND device_id IN (
    SELECT e.id FROM edge_devices e
    WHERE e.logical_device_id IS NOT NULL
  )`, table), maxID).Scan(&count).Error
	if err != nil {
		return 0, fmt.Errorf("verify backfill %s: %w", table, err)
	}
	return count, nil
}

func isNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// SQLite: "UNIQUE constraint failed: ..." / PostgreSQL: "duplicate key ..."
	return strings.Contains(msg, "UNIQUE") || strings.Contains(msg, "duplicate key")
}
