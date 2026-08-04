package datalifecycle

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

// startMerge 直接走 MergeDevices 建合并 (含 jobs), 返回 targetID。
func startMerge(t *testing.T, db *gorm.DB, srcs ...*models.LogicalDevice) uint {
	t.Helper()
	ids := make([]uint, len(srcs))
	for i, s := range srcs {
		ids[i] = s.ID
	}
	result, err := MergeDevices(db, &MergeRequest{TargetName: "target", SourceIDs: ids})
	if err != nil {
		t.Fatalf("start merge: %v", err)
	}
	return result.TargetID
}

// countLogicalRows counts data rows carrying the given logical_device_id.
func countLogicalRows(t *testing.T, db *gorm.DB, table string, logicalID uint) int64 {
	t.Helper()
	var n int64
	if err := db.Raw(fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE logical_device_id = ?", table), logicalID).Scan(&n).Error; err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestMigrator_FullMigration(t *testing.T) {
	db := testutil.OpenTestDB(t)
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	src1 := seedMergeSource(t, db, "src1", 3, base)
	src2 := seedMergeSource(t, db, "src2", 2, base)
	targetID := startMerge(t, db, src1, src2)

	m := NewMigrator(db)
	m.SetBatchSize(2) // 强制多批, 覆盖水位推进路径
	m.SetBatchSleep(0)
	results, err := m.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	for _, r := range results {
		if !r.Done || r.Err != "" {
			t.Errorf("job %d: done=%v err=%q", r.JobID, r.Done, r.Err)
		}
	}

	// 全部 5+5 unified 行搬到目标 (两源合计), device_data 同理。
	if got := countLogicalRows(t, db, "unified_data", targetID); got != 5 {
		t.Errorf("unified_data target rows = %d, want 5", got)
	}
	if got := countLogicalRows(t, db, "device_data", targetID); got != 5 {
		t.Errorf("device_data target rows = %d, want 5", got)
	}

	// 源 merge_status=done, jobs done + finished_at。
	for _, src := range []*models.LogicalDevice{src1, src2} {
		var ld models.LogicalDevice
		db.First(&ld, src.ID)
		if ld.MergeStatus == nil || *ld.MergeStatus != models.MergeStatusDone {
			t.Errorf("source %d merge_status = %v, want done", src.ID, ld.MergeStatus)
		}
	}
	var jobs []models.MergeJob
	db.Find(&jobs)
	for _, j := range jobs {
		if j.Status != models.MergeJobDone || j.FinishedAt == nil {
			t.Errorf("job %d status=%s finished_at=%v", j.ID, j.Status, j.FinishedAt)
		}
	}
	// 再次 RunOnce 无作业可处理 (幂等, 不重复搬迁)。
	results, err = m.RunOnce(context.Background())
	if err != nil || len(results) != 0 {
		t.Errorf("second run: results=%d err=%v, want idle", len(results), err)
	}
}

// TestMigrator_WatermarkResumeAndIdempotency — 批次中途失败后水位续跑,
// 已搬迁的行不重复计数 (同目标幂等)。
func TestMigrator_WatermarkResumeAndIdempotency(t *testing.T) {
	db := testutil.OpenTestDB(t)
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	src1 := seedMergeSource(t, db, "src1", 5, base) // 5 行, batchSize=2 → 3 批
	src2 := seedMergeSource(t, db, "src2", 1, base)
	targetID := startMerge(t, db, src1, src2)

	m := NewMigrator(db)
	m.SetBatchSize(2)
	m.SetBatchSleep(0)
	// 第一轮: src1 第 2 批 (windowEnd=4) 注入失败。两个 job 在同一表上
	// 走过相同窗口, 用 fail-once 闭包保证只有先到的 job1 中招。
	alreadyFailed := false
	m.SetBatchHook(func(table string, watermark, windowEnd int64) error {
		if table == "unified_data" && windowEnd == 4 && !alreadyFailed {
			alreadyFailed = true
			return errors.New("injected batch failure")
		}
		return nil
	})
	results, err := m.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("run 1 results = %d, want 2", len(results))
	}

	// src1 job: 失败 (水位停在 2, 已搬 2 行); src2 job 成功。
	var job1, job2 models.MergeJob
	db.Where("source_logical_id = ?", src1.ID).First(&job1)
	db.Where("source_logical_id = ?", src2.ID).First(&job2)
	if job1.WatermarkID != 2 || job1.WatermarkPhase != models.MergePhaseUnifiedData {
		t.Errorf("job1 watermark = %d/%s, want 2/unified_data", job1.WatermarkID, job1.WatermarkPhase)
	}
	if job1.MigratedRows != 2 {
		t.Errorf("job1 migrated_rows = %d, want 2", job1.MigratedRows)
	}
	if job1.RetryCount != 1 {
		t.Errorf("job1 retry_count = %d, want 1", job1.RetryCount)
	}
	if job2.Status != models.MergeJobDone {
		t.Errorf("job2 status = %s, want done", job2.Status)
	}
	// 首次失败发 warning 通知。
	var notifs []models.Notification
	db.Where("source = ?", NotificationSourceMergeFailed).Find(&notifs)
	if len(notifs) != 1 || notifs[0].Type != "warning" {
		t.Fatalf("notifications after first failure = %+v, want 1 warning", notifs)
	}

	// 第二轮: 清除注入, 续跑。水位从 2 继续, 已搬的 2 行不重复计数。
	// migrated_rows 累计 unified(5) + device_data(5) = 10; 若续跑重复
	// 计数会 > 10。
	m.SetBatchHook(nil)
	results, err = m.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	var doneJob *MigrateResult
	for i := range results {
		if results[i].JobID == job1.ID {
			doneJob = &results[i]
		}
	}
	if doneJob == nil || !doneJob.Done {
		t.Fatalf("job1 not done in run 2: %+v", results)
	}
	db.First(&job1, job1.ID)
	if job1.Status != models.MergeJobDone {
		t.Errorf("job1 status = %s, want done", job1.Status)
	}
	if job1.MigratedRows != 10 {
		t.Errorf("job1 migrated_rows = %d, want 10 (5 unified + 5 device, no double count)", job1.MigratedRows)
	}
	if got := countLogicalRows(t, db, "unified_data", targetID); got != 6 {
		t.Errorf("unified_data target rows = %d, want 6", got)
	}
	if got := countLogicalRows(t, db, "device_data", targetID); got != 6 {
		t.Errorf("device_data target rows = %d, want 6", got)
	}
}

// TestMigrator_RetryExhaustionResetsSource — 3 次重试超限: job failed +
// 源 merged_into/merge_status 复位 + 终态 error 通知。
func TestMigrator_RetryExhaustionResetsSource(t *testing.T) {
	db := testutil.OpenTestDB(t)
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	src1 := seedMergeSource(t, db, "src1", 2, base)
	src2 := seedMergeSource(t, db, "src2", 1, base)
	startMerge(t, db, src1, src2)

	m := NewMigrator(db)
	m.SetBatchSize(10)
	m.SetBatchSleep(0)
	m.SetBatchHook(func(string, int64, int64) error { return errors.New("permanent disk failure") })

	ctx := context.Background()
	for round := 0; round < 3; round++ {
		if _, err := m.RunOnce(ctx); err != nil {
			t.Fatalf("round %d: %v", round+1, err)
		}
	}
	var job1 models.MergeJob
	db.Where("source_logical_id = ?", src1.ID).First(&job1)
	if job1.Status != models.MergeJobFailed {
		t.Errorf("job1 status = %s, want failed", job1.Status)
	}
	if job1.RetryCount != 3 || job1.FinishedAt == nil {
		t.Errorf("job1 retry_count=%d finished_at=%v", job1.RetryCount, job1.FinishedAt)
	}

	// 源复位 → 可重新发起合并。
	var reloaded models.LogicalDevice
	db.First(&reloaded, src1.ID)
	if reloaded.MergedInto != nil || reloaded.MergeStatus != nil {
		t.Errorf("source not reset: merged_into=%v status=%v", reloaded.MergedInto, reloaded.MergeStatus)
	}

	// 通知: 每失败源 1 条 warning (首次失败) + 1 条 error (终态) = 2 源 ×2。
	var warnings, finals int64
	db.Model(&models.Notification{}).Where("source = ? AND type = ?", NotificationSourceMergeFailed, "warning").Count(&warnings)
	db.Model(&models.Notification{}).Where("source = ? AND type = ?", NotificationSourceMergeFailed, "error").Count(&finals)
	if warnings != 2 {
		t.Errorf("warning notifications = %d, want 2 (one per source job)", warnings)
	}
	if finals != 2 {
		t.Errorf("final error notifications = %d, want 2 (one per source job)", finals)
	}

	// failed 作业不再被 RunOnce 拾取。
	m.SetBatchHook(nil)
	results, err := m.RunOnce(ctx)
	if err != nil || len(results) != 0 {
		t.Errorf("run after failure: results=%d err=%v, want idle", len(results), err)
	}
}

// TestMigrator_PendingUnionVisibility — pending 窗口内 (§4.3 B1):
// 未搬迁源行对经目标解析的查询可见 (dataScopeCondition UNION)。
func TestMigrator_PendingUnionVisibility(t *testing.T) {
	db := testutil.OpenTestDB(t)
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	src1 := seedMergeSource(t, db, "src1", 3, base)
	src2 := seedMergeSource(t, db, "src2", 2, base)
	targetID := startMerge(t, db, src1, src2)

	// 搬迁前: 目标 scope 必须 UNION 入 pending 源的行。
	scope, err := ResolveScope(db, targetID)
	if err != nil {
		t.Fatalf("resolve scope: %v", err)
	}
	if len(scope.LogicalIDs) != 3 {
		t.Fatalf("scope logical ids = %v, want target + 2 pending sources", scope.LogicalIDs)
	}
	cond, args := scope.Cond()
	var visible int64
	db.Raw("SELECT COUNT(*) FROM unified_data WHERE "+cond, args...).Scan(&visible)
	if visible != 5 {
		t.Errorf("visible rows during pending = %d, want 5", visible)
	}

	// 搬迁完成后 (done): UNION 项消失, 行已在目标名下。
	m := NewMigrator(db)
	m.SetBatchSleep(0)
	if _, err := m.RunOnce(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	scope, err = ResolveScope(db, targetID)
	if err != nil {
		t.Fatalf("resolve scope after: %v", err)
	}
	if len(scope.LogicalIDs) != 1 {
		t.Errorf("scope after done = %v, want target only", scope.LogicalIDs)
	}
	if got := countLogicalRows(t, db, "unified_data", targetID); got != 5 {
		t.Errorf("target rows after migration = %d, want 5", got)
	}
}

// TestMigrator_PhaseSwitchPersists — phase 切换 (unified_data →
// device_data) 持久化: unified 搬完崩溃, 续跑从 device_data 从头开始,
// 不把 unified 的旧水位带进 device_data。
func TestMigrator_PhaseSwitchPersists(t *testing.T) {
	db := testutil.OpenTestDB(t)
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	src1 := seedMergeSource(t, db, "src1", 2, base)
	src2 := seedMergeSource(t, db, "src2", 1, base)
	targetID := startMerge(t, db, src1, src2)

	m := NewMigrator(db)
	m.SetBatchSize(10)
	m.SetBatchSleep(0)
	// device_data 第一批注入失败 → unified 完成, device_data 未开始。
	m.SetBatchHook(func(table string, watermark, windowEnd int64) error {
		if table == "device_data" {
			return errors.New("device_data failure")
		}
		return nil
	})
	if _, err := m.RunOnce(context.Background()); err != nil {
		t.Fatalf("run 1: %v", err)
	}
	var job1 models.MergeJob
	db.Where("source_logical_id = ?", src1.ID).First(&job1)
	if job1.WatermarkPhase != models.MergePhaseDeviceData {
		t.Errorf("phase = %s, want device_data (persisted switch)", job1.WatermarkPhase)
	}
	if got := countLogicalRows(t, db, "unified_data", targetID); got != 3 {
		t.Errorf("unified rows migrated = %d, want 3 (unified phase completed)", got)
	}
	if got := countLogicalRows(t, db, "device_data", targetID); got != 0 {
		t.Errorf("device_data rows migrated = %d, want 0 (failed phase)", got)
	}

	// 续跑: device_data 从头搬。
	m.SetBatchHook(nil)
	results, err := m.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	for _, r := range results {
		if !r.Done {
			t.Errorf("job %d not done after resume: %+v", r.JobID, r)
		}
	}
	if got := countLogicalRows(t, db, "device_data", targetID); got != 3 {
		t.Errorf("device_data rows after resume = %d, want 3", got)
	}
}
