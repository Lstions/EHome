package datalifecycle

// purge_edge_device.go — 「删除设备时同时删除其历史数据」的后台任务。
//
// 语义（与逻辑设备层存在时的 purge 任务完全一致，只是标记落在设备上）：
//   DELETE /api/v1/edge-devices/:id?delete_data=true
//     → 设备行软删 + edge_devices.data_purge_requested = true
//     → 本任务每日分批硬删该 device_id 在两个时序表里的全部行
//     → 清 calibration_cache 孤儿
//     → 硬删设备行 (Unscoped DELETE)
//
// 为什么异步而不是在 API 事务里删: 一台设备可能有上千万行历史，同步删除会
// 让 HTTP 请求挂几分钟并长时间持锁；分批 + 批间 sleep 是既有的锁交互纪律
// （见 batch_delete.go 的常量说明）。
//
// 为什么「复活即撤回删除意图」: 用户在删完点「试试看恢复」/重建同一条设备行时，
// 若后台仍在推进硬删，就会静默清空他刚选择保留的历史 —— 这是不可恢复的数据
// 丢失。因此每轮先检查设备是否又变成存活状态，是则清标记并放弃。

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/metrics"
)

// PurgeOutcome classifies what happened to one device this run.
type PurgeOutcome string

const (
	// Purged — 数据硬删完成，设备行已物理删除。
	Purged PurgeOutcome = "purged"
	// AbandonedRevived — 设备在删除后又被恢复（存活），放弃 purge 并清标志。
	AbandonedRevived PurgeOutcome = "abandoned_revived"
	// PurgeFailed — 删除失败（保留标志，下次重试）。
	PurgeFailed PurgeOutcome = "failed"
)

// purgeStaleClaimThreshold 是「陈旧认领」的判定阈值：只有认领时间早于
// now - purgeStaleClaimThreshold 的 data_purge_started_at 才会在 RunOnce 开头被清掉。
//
// 为什么需要一个阈值而不是「清零所有认领」（原实现）:
//   - purge 是异步分批任务（一台设备可能上千万行、分钟级）。认领 → 删完 →
//     清认领之间是一个**长窗口**；若无条件清零，第二个 purger 进程（滚动发布、
//     误起两个实例、运维手工跑一次）会在开头把第一个进程**在途**的认领清掉，
//     随后 restore 又能成功清 deleted_at —— purge 手里的认领仍然在删，
//     于是一次「恢复」照样被硬删（不可恢复数据丢失，F-01 的窗口重新打开）。
//   - 1h 足够覆盖单台设备的清理时长: PG 每批 1 万行 + 批间 200ms，即使 2900 万行
//     也约 10 分钟（生产最大设备 29M，见 .contract §11.2），远小于 1h；且认领是
//     **每台设备在真正开始处理前**才取的，认领年龄 ≈ 单台设备的处理时长。
//   - 反过来，进程崩溃留下的认领最多让该设备延迟到下一轮（默认 24h）才被清理，
//     下一轮时它已 >1h，会被正常回收；不会永久卡死。
//
// ⚠ 多实例部署的边界（诚实声明）: 阈值只是「尽力避免误清」，不是租约。
// 若未来需要多实例同时跑 purger（或单台设备清理可能超过 1h），必须改成
// 租约/心跳（认领时写 owner + 定期续期，只有过期租约才可被他人抢占），
// 并把阈值换成租约 TTL。当前部署形态是单容器（docker compose 单实例），
// 阈值方案足够；DevicePurger 仍应在部署文档里标注「同一时刻只应有一个 purger」。
const purgeStaleClaimThreshold = time.Hour

// PurgeResult reports one device's purge outcome.
type PurgeResult struct {
	EdgeDeviceID uint         `json:"edge_device_id"`
	Outcome      PurgeOutcome `json:"outcome"`
	RowsDeleted  int64        `json:"rows_deleted"`
	Error        string       `json:"error,omitempty"`
}

// DevicePurger is the daily purge background task.
type DevicePurger struct {
	db           *gorm.DB
	interval     time.Duration
	initialDelay time.Duration
	batchSleep   time.Duration
	batchSize    int // 0 → dialect default (PG 1万 / SQLite 1千)
	startOnce    sync.Once
	stopCh       chan struct{}
	stopOnce     sync.Once
	wakeCh       chan struct{}
	wg           sync.WaitGroup
}

// NewDevicePurger creates the purge worker with defaults: first run 30s after
// startup, then every 24h.
func NewDevicePurger(db *gorm.DB) *DevicePurger {
	return &DevicePurger{
		db:           db,
		interval:     24 * time.Hour,
		initialDelay: 30 * time.Second,
		batchSleep:   purgeBatchSleep,
		stopCh:       make(chan struct{}),
		wakeCh:       make(chan struct{}, 1),
	}
}

// SetSchedule overrides timing (tests only).
func (p *DevicePurger) SetSchedule(interval, initialDelay, batchSleep time.Duration) {
	if interval > 0 {
		p.interval = interval
	}
	if initialDelay > 0 {
		p.initialDelay = initialDelay
	}
	p.batchSleep = batchSleep // 0 allowed in tests
}

// SetBatchSize overrides the per-transaction delete batch size (tests only).
func (p *DevicePurger) SetBatchSize(n int) {
	if n > 0 {
		p.batchSize = n
	}
}

// Start launches the purge goroutine once.
func (p *DevicePurger) Start() {
	p.startOnce.Do(func() {
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			p.run()
		}()
	})
}

// Wake 请求**立即**跑一轮清理（非阻塞；多次请求合并为一次）。
//
// 用途（2026-10-25）：历史设备「彻底删除」提交后由 API 层唤醒，用户不必等到
// 每日任务（最长 24h）才真正删掉数据。清理本身仍走 RunOnce 的认领协议，
// 与定时任务并发也安全（认领 + 条件删除，重复调用幂等）。
func (p *DevicePurger) Wake() {
	select {
	case p.wakeCh <- struct{}{}:
	default: // 已有一个待处理的唤醒请求，合并即可
	}
}

// Stop signals the goroutine to exit and waits for it.
func (p *DevicePurger) Stop() {
	p.stopOnce.Do(func() { close(p.stopCh) })
	p.wg.Wait()
}

func (p *DevicePurger) run() {
	// First run shortly after startup so freshly requested purges don't
	// wait a full day; then every interval.
	//
	// Wake（2026-10-25）：API 提交「历史设备彻底删除」后立即唤醒跑一轮。
	// runOnceLogged 是串行的（本 goroutine 内），Wake 与定时触发在运行期间
	// 只会排队一次；RunOnce 本身幂等（无目标设备即 no-op），不会互相踩踏。
	timer := time.NewTimer(p.initialDelay)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			p.runOnceLogged()
			timer.Reset(p.interval)
		case <-p.wakeCh:
			p.runOnceLogged()
		case <-p.stopCh:
			return
		}
	}
}

func (p *DevicePurger) runOnceLogged() {
	results, err := p.RunOnce(context.Background())
	if err != nil {
		slog.Warn("datalifecycle: device purge run failed", "error", err)
		return
	}
	for _, r := range results {
		switch r.Outcome {
		case Purged:
			slog.Info("datalifecycle: purged edge device data",
				"edge_device_id", r.EdgeDeviceID, "rows_deleted", r.RowsDeleted)
		case AbandonedRevived:
			slog.Info("datalifecycle: purge abandoned (device revived)",
				"edge_device_id", r.EdgeDeviceID)
		case PurgeFailed:
			slog.Warn("datalifecycle: purge failed",
				"edge_device_id", r.EdgeDeviceID, "error", r.Error)
		}
	}
}

// RunOnce processes every device with data_purge_requested=TRUE and returns
// per-device outcomes. It is the testable core of the worker.
//
// 2026-10-25 并发契约（用户反馈「恢复设备功能逻辑还有大问题」）：
// 旧实现只在一开始扫描时看一次 deleted_at，随后按内存快照一路删到底 —— 用户在
// purge 运行期间点「恢复」时，purge 会继续删掉他刚选择保留的历史，并把已复活的
// 设备行物理删掉（不可恢复的数据丢失）。现在的协议：
//
//	· 每台设备先**原子认领**（data_purge_started_at），认领条件是
//	  「仍软删 + 仍请求删数据 + 尚未被认领」；
//	· restore 在事务里对同一行加行锁并检查认领：已认领 ⇒ 409，不会复活；
//	· 认领失败（已被恢复/已被他人处理）⇒ 若设备已复活则撤回删除意图；
//	· 每轮开始只清**陈旧**认领（> purgeStaleClaimThreshold），崩溃恢复的同时
//	  不误清另一进程在途的认领（见 purgeStaleClaimThreshold 注释）。
func (p *DevicePurger) RunOnce(ctx context.Context) ([]PurgeResult, error) {
	// 1) 崩溃恢复：只清「认领早于 now-1h」的陈旧认领。
	//    不能无条件清零 —— 那会把第二个 purger 进程在途的认领清掉，让 restore
	//    在 purge 仍推进时成功复活，重新打开「恢复后仍被硬删」的窗口（F-01）。
	cutoff := time.Now().Add(-purgeStaleClaimThreshold)
	reset := p.db.WithContext(ctx).Unscoped().Model(&models.EdgeDevice{}).
		Where("data_purge_started_at IS NOT NULL AND data_purge_started_at < ?", cutoff).
		Update("data_purge_started_at", nil)
	if reset.Error != nil {
		metrics.LifecycleTaskFailures.WithLabelValues("purge").Inc()
		return nil, fmt.Errorf("datalifecycle: reset stale purge claims: %w", reset.Error)
	}
	if reset.RowsAffected > 0 {
		slog.Warn("datalifecycle: reset stale purge claims",
			"count", reset.RowsAffected, "older_than", purgeStaleClaimThreshold.String(),
			"hint", "another purger instance may have crashed; if this repeats, investigate instance leaks")
	}

	var targets []models.EdgeDevice
	if err := p.db.WithContext(ctx).Unscoped().
		Where("data_purge_requested = ? AND data_purge_started_at IS NULL", true).
		Order("id").
		Find(&targets).Error; err != nil {
		metrics.LifecycleTaskFailures.WithLabelValues("purge").Inc()
		return nil, fmt.Errorf("datalifecycle: scan data_purge_requested: %w", err)
	}
	results := make([]PurgeResult, 0, len(targets))
	for i := range targets {
		select {
		case <-ctx.Done():
			return results, ctx.Err()
		default:
		}
		dev := &targets[i]
		// 2) 原子认领：与 restore 的行锁形成互斥（见函数头注释）。
		claimed, err := p.claim(ctx, dev.ID)
		if err != nil {
			metrics.LifecycleTaskFailures.WithLabelValues("purge").Inc()
			results = append(results, PurgeResult{EdgeDeviceID: dev.ID, Outcome: PurgeFailed, Error: err.Error()})
			continue
		}
		if !claimed {
			// 认领失败：设备已被恢复（或已被处理）。若它不再软删则撤回删除意图 ——
			// 与旧实现的 AbandonedRevived 语义一致。
			var fresh models.EdgeDevice
			if err := p.db.WithContext(ctx).Unscoped().Select("id", "deleted_at").
				First(&fresh, dev.ID).Error; err == nil && !fresh.DeletedAt.Valid {
				if cerr := p.db.WithContext(ctx).Unscoped().Model(&models.EdgeDevice{}).
					Where("id = ?", dev.ID).
					Update("data_purge_requested", false).Error; cerr != nil {
					results = append(results, PurgeResult{EdgeDeviceID: dev.ID, Outcome: PurgeFailed, Error: cerr.Error()})
					continue
				}
				results = append(results, PurgeResult{EdgeDeviceID: dev.ID, Outcome: AbandonedRevived})
			}
			continue
		}
		res := p.purgeOne(ctx, dev)
		switch res.Outcome {
		case PurgeFailed:
			metrics.LifecycleTaskFailures.WithLabelValues("purge").Inc()
		case Purged:
			if res.RowsDeleted > 0 {
				metrics.LifecyclePurgedRows.WithLabelValues("purge").Add(float64(res.RowsDeleted))
			}
		}
		results = append(results, res)
	}
	return results, nil
}

// purgeOne hard-deletes one device's data in batches, then tears down the
// device row itself. Precondition: the device's purge claim has been taken
// (data_purge_started_at != NULL) by RunOnce.
func (p *DevicePurger) purgeOne(ctx context.Context, dev *models.EdgeDevice) PurgeResult {
	result := PurgeResult{EdgeDeviceID: dev.ID}

	// 防御性复核：认领后仍确认「软删 + 仍请求删数据」。restore 看到认领会 409，
	// 所以正常路径不会复活；这里兜住直接改库/历史并发写造成的状态漂移，
	// 一旦发现复活就立刻放弃（一行都不删）。
	var fresh models.EdgeDevice
	err := p.db.WithContext(ctx).Unscoped().Select("id", "deleted_at", "data_purge_requested").
		First(&fresh, dev.ID).Error
	if err == nil && (!fresh.DeletedAt.Valid || !fresh.DataPurgeRequested) {
		if cerr := p.clearClaim(ctx, dev.ID, !fresh.DeletedAt.Valid); cerr != nil {
			result.Outcome = PurgeFailed
			result.Error = cerr.Error()
			return result
		}
		result.Outcome = AbandonedRevived
		return result
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		result.Outcome = PurgeFailed
		result.Error = fmt.Sprintf("reload edge device: %v", err)
		return result
	}

	// 分批硬删两个时序表里该设备的全部行。
	deleted, err := p.deleteDeviceRows(ctx, dev.ID)
	if err != nil {
		result.Outcome = PurgeFailed
		result.Error = err.Error()
		p.clearClaimLogged(ctx, dev.ID, false)
		return result
	}
	result.RowsDeleted = deleted

	// 台账/投影表也要清：只删时序表会留下「设备行已不存在、但字段目录/数据源
	// 台账里还挂着它」的幽灵（GET /sensor-fields、GET /data-sources 仍能看到）。
	if err := p.cleanLedgerRows(ctx, dev.ID); err != nil {
		result.Outcome = PurgeFailed
		result.Error = err.Error()
		p.clearClaimLogged(ctx, dev.ID, false)
		return result
	}

	// 清理该设备的工厂校准行（数据已清除，校准值不再有意义）。
	if err := p.db.WithContext(ctx).
		Where("edge_device_id = ?", dev.ID).
		Delete(&models.CalibrationCache{}).Error; err != nil {
		result.Outcome = PurgeFailed
		result.Error = fmt.Sprintf("clean calibration_cache: %v", err)
		p.clearClaimLogged(ctx, dev.ID, false)
		return result
	}
	// 物理删除设备行（软删行已无数据，留着只会让列表/统计永久带着幽灵）。
	// 带条件删除：认领仍在 + 仍软删才删，避免把任何被并发改活的行误删。
	del := p.db.WithContext(ctx).Unscoped().
		Where("id = ? AND deleted_at IS NOT NULL AND data_purge_started_at IS NOT NULL", dev.ID).
		Delete(&models.EdgeDevice{})
	if del.Error != nil {
		result.Outcome = PurgeFailed
		result.Error = fmt.Sprintf("delete edge_device row: %v", del.Error)
		p.clearClaimLogged(ctx, dev.ID, false)
		return result
	}
	if del.RowsAffected == 0 {
		// 行在删除前被改活/已被删：放弃本轮，不谎报成功。
		p.clearClaimLogged(ctx, dev.ID, true)
		result.Outcome = AbandonedRevived
		return result
	}
	result.Outcome = Purged
	return result
}

// clearClaimLogged 是 clearClaim 的「失败只记日志」包装。
//
// 为什么不能写下划线忽略返回值（原写法）: purgeOne 的主错误已经写进
// PurgeResult 返回给调用方，清认领失败不应掩盖它；但认领残留会让 restore 在该
// 设备上一直 409（最长到下一轮 RunOnce 的陈旧认领回收，默认 24h），必须留下
// 可排查的错误日志，否则运维只看到「恢复不了」而不知道为什么。
func (p *DevicePurger) clearClaimLogged(ctx context.Context, deviceID uint, clearRequested bool) {
	if err := p.clearClaim(ctx, deviceID, clearRequested); err != nil {
		slog.Error("datalifecycle: clear purge claim failed (device may stay 409 until stale-claim reset)",
			"edge_device_id", deviceID, "clear_requested", clearRequested, "error", err)
	}
}

// claim 原子认领一台设备的硬删任务。只有「仍软删 + 仍请求删数据 + 尚未认领」
// 才成功（RowsAffected=1）。restore 对同一行加行锁并检查认领，从而与这里互斥：
// 两者谁先拿到行锁谁生效，另一方看到的都是已定型的状态。
func (p *DevicePurger) claim(ctx context.Context, deviceID uint) (bool, error) {
	res := p.db.WithContext(ctx).Unscoped().Model(&models.EdgeDevice{}).
		Where("id = ? AND deleted_at IS NOT NULL AND data_purge_requested = ? AND data_purge_started_at IS NULL", deviceID, true).
		Update("data_purge_started_at", time.Now())
	if res.Error != nil {
		return false, fmt.Errorf("claim purge of edge device %d: %w", deviceID, res.Error)
	}
	return res.RowsAffected == 1, nil
}

// clearClaim 释放认领；clearRequested=true 时同时撤回删除意图（设备已复活时）。
func (p *DevicePurger) clearClaim(ctx context.Context, deviceID uint, clearRequested bool) error {
	updates := map[string]interface{}{"data_purge_started_at": nil}
	if clearRequested {
		updates["data_purge_requested"] = false
	}
	if err := p.db.WithContext(ctx).Unscoped().Model(&models.EdgeDevice{}).
		Where("id = ?", deviceID).Updates(updates).Error; err != nil {
		return fmt.Errorf("clear purge claim of edge device %d: %w", deviceID, err)
	}
	return nil
}

// cleanLedgerRows 清掉设备在台账/投影表里的残留行（sensor_field_obs / data_sources）。
// 表不存在时跳过（精简测试库/早期部署）。
func (p *DevicePurger) cleanLedgerRows(ctx context.Context, deviceID uint) error {
	if p.db.Migrator().HasTable("sensor_field_obs") {
		if err := p.db.WithContext(ctx).Exec("DELETE FROM sensor_field_obs WHERE device_id = ?", deviceID).Error; err != nil {
			return fmt.Errorf("clean sensor_field_obs: %w", err)
		}
	}
	if p.db.Migrator().HasTable("data_sources") {
		if err := p.db.WithContext(ctx).Exec("DELETE FROM data_sources WHERE edge_device_id = ?", deviceID).Error; err != nil {
			return fmt.Errorf("clean data_sources: %w", err)
		}
	}
	return nil
}

// deleteDeviceRows batch-deletes every row of one device from **all** time-series
// tables (unified_data / device_data / telemetry_frame). Each batch is an
// independent transaction sized by dialect; batches sleep between each other to
// limit WAL pressure.
func (p *DevicePurger) deleteDeviceRows(ctx context.Context, deviceID uint) (int64, error) {
	batchSize := p.batchSize
	if batchSize <= 0 {
		batchSize = purgeBatchSizePostgres
		if p.db.Dialector != nil && p.db.Dialector.Name() != "postgres" {
			batchSize = purgeBatchSizeSQLite
		}
	}
	var total int64
	// 表清单与 SQL 形状来自 TimeseriesTables()/deleteDeviceRowsFromTable
	// （telemetry_frame_lifecycle.go）：帧表没有 id 列、主键是 (device_id, ts, seq)，
	// 用 unified_data 的 "WHERE id IN (SELECT id ...)" 会直接报列不存在。
	//
	// 这是「彻底删除设备」的语义闭环：只删 unified_data/device_data 会留下
	// 「设备已物理删除、帧数据还在」的幽灵（评审 B-3）。
	for _, table := range existingTimeseriesTables(p.db) {
		for batch := 0; batch < maxPurgeBatches; batch++ {
			var affected int64
			err := p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				// DELETE ... WHERE <锚> IN (SELECT <锚> ... LIMIT ?): deterministic
				// batch slicing that works on both PostgreSQL and SQLite.
				n, derr := deleteDeviceRowsFromTable(tx, table, deviceID, batchSize)
				affected = n
				return derr
			})
			if err != nil {
				return total, fmt.Errorf("batch delete %s: %w", table.Name, err)
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
