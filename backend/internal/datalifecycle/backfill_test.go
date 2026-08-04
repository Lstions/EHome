package datalifecycle

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

// seedBackfillScenario 造一个完整的回填场景:
//   - 1 个 logical_device + nLiving 个存活实例 + nDeleted 个软删实例
//     (全部已挂载 logical_device_id, 等价 T1 启动补建后的状态)
//   - 每个实例 rowsPer 行 unified_data + device_data, logical_device_id
//     全 NULL (回填前的存量行)
//
// 返回存活实例列表 (用于断言)。
func seedBackfillScenario(t *testing.T, db *gorm.DB, key string, nLiving, nDeleted, rowsPer int) []*models.EdgeDevice {
	t.Helper()
	ld := &models.LogicalDevice{IdentityKey: key, Name: key, DeviceType: "bms_jbd", RetentionDays: 365}
	if err := db.Create(ld).Error; err != nil {
		t.Fatalf("create logical device: %v", err)
	}
	var living []*models.EdgeDevice
	for i := 0; i < nLiving+nDeleted; i++ {
		dev := seedDevice(t, db, key+"-inst", "bms_jbd", key, false)
		if err := db.Model(dev).Update("logical_device_id", ld.ID).Error; err != nil {
			t.Fatalf("attach logical device: %v", err)
		}
		for r := 0; r < rowsPer; r++ {
			if err := db.Create(&models.UnifiedData{
				DeviceID:   dev.ID,
				SensorName: "voltage",
				Value:      float64(r),
				Timestamp:  time.Now(),
			}).Error; err != nil {
				t.Fatalf("seed unified_data: %v", err)
			}
			if err := db.Create(&models.DeviceData{
				DeviceID:  dev.ID,
				NodeID:    "NODE001",
				DataJSON:  `{"v":1}`,
				Timestamp: time.Now(),
			}).Error; err != nil {
				t.Fatalf("seed device_data: %v", err)
			}
		}
		if i >= nLiving {
			if err := db.Delete(dev).Error; err != nil {
				t.Fatalf("soft delete instance: %v", err)
			}
			// 软删后 GORM Update 不命中, 显式 Unscoped 回挂 (T1 行为)。
			if err := db.Unscoped().Model(&models.EdgeDevice{}).
				Where("id = ?", dev.ID).
				Update("logical_device_id", ld.ID).Error; err != nil {
				t.Fatalf("re-attach soft-deleted instance: %v", err)
			}
		} else {
			living = append(living, dev)
		}
	}
	return living
}

// newTestBackfiller creates a Backfiller with tiny batches and no sleep for
// deterministic, fast tests.
func newTestBackfiller(db *gorm.DB, batchSize int) *Backfiller {
	b := NewBackfiller(db)
	b.SetBatchSize(batchSize)
	b.SetBatchSleep(0)
	return b
}

func countNullLogicalRows(t *testing.T, db *gorm.DB, table string) int64 {
	t.Helper()
	var n int64
	if err := db.Raw("SELECT COUNT(*) FROM " + table + " WHERE logical_device_id IS NULL").Scan(&n).Error; err != nil {
		t.Fatalf("count null rows %s: %v", table, err)
	}
	return n
}

// ==================== 端到端回填 ====================

func TestBackfill_EndToEnd_FillsBothTablesIncludingSoftDeleted(t *testing.T) {
	db := testutil.OpenTestDB(t)
	living := seedBackfillScenario(t, db, "e2e:0x10", 2, 1, 3) // 3 实例 × 3 行 × 2 表
	_ = living

	if n := countNullLogicalRows(t, db, "unified_data"); n != 9 {
		t.Fatalf("precondition: unified_data NULL rows = %d, want 9", n)
	}

	results, err := newTestBackfiller(db, 4).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	for _, r := range results {
		if r.Err != "" {
			t.Fatalf("table %s: %s", r.Table, r.Err)
		}
		if !r.VerifyPassed {
			t.Errorf("table %s: verify failed, rows_missing=%d", r.Table, r.RowsMissing)
		}
		if r.RowsUpdated != 9 {
			t.Errorf("table %s: rows_updated = %d, want 9", r.Table, r.RowsUpdated)
		}
		if n := countNullLogicalRows(t, db, r.Table); n != 0 {
			t.Errorf("table %s: %d rows still NULL after backfill", r.Table, n)
		}
	}

	// job 状态: done + 水位到表尾 + finished_at 置位。
	for _, table := range []string{"unified_data", "device_data"} {
		var job models.BackfillJob
		if err := db.Where("job_type = ? AND table_name = ?",
			models.BackfillJobTypeLogicalID, table).First(&job).Error; err != nil {
			t.Fatalf("job %s: %v", table, err)
		}
		if job.Status != models.BackfillStatusDone {
			t.Errorf("job %s status = %s, want done", table, job.Status)
		}
		var maxID int64
		if err := db.Raw("SELECT MAX(id) FROM " + table).Scan(&maxID).Error; err != nil {
			t.Fatal(err)
		}
		if job.WatermarkID != maxID {
			t.Errorf("job %s watermark = %d, want %d", table, job.WatermarkID, maxID)
		}
		if job.FinishedAt == nil {
			t.Errorf("job %s finished_at is nil", table)
		}
	}

	// 软删实例的行也回填到同一逻辑身份 (scope 退化的前提)。
	var ld models.LogicalDevice
	if err := db.Where("identity_key = ?", "e2e:0x10").First(&ld).Error; err != nil {
		t.Fatal(err)
	}
	var foreign int64
	if err := db.Raw("SELECT COUNT(*) FROM unified_data WHERE logical_device_id <> ?", ld.ID).
		Scan(&foreign).Error; err != nil {
		t.Fatal(err)
	}
	if foreign != 0 {
		t.Errorf("%d unified_data rows point at wrong logical device", foreign)
	}
}

// ==================== 分批逻辑 ====================

func TestBackfill_Batching_WindowBoundaries(t *testing.T) {
	db := testutil.OpenTestDB(t)
	seedBackfillScenario(t, db, "batch:0x20", 1, 0, 7) // 7 行, 批大小 3

	b := newTestBackfiller(db, 3)
	results, err := b.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	for _, r := range results {
		if r.Err != "" {
			t.Fatalf("table %s: %s", r.Table, r.Err)
		}
		// 7 行 id 1..7, 窗口 3 → 批数 = ceil(7/3) = 3
		if r.Batches != 3 {
			t.Errorf("table %s: batches = %d, want 3", r.Table, r.Batches)
		}
		if r.RowsUpdated != 7 {
			t.Errorf("table %s: rows_updated = %d, want 7", r.Table, r.RowsUpdated)
		}
		if !r.VerifyPassed {
			t.Errorf("table %s: verify failed", r.Table)
		}
	}
}

// ==================== 断点续跑 ====================

// 预置水位模拟上一次运行崩溃后的持久化状态: 低 id 行已回填, 高 id 行
// 未回填, job 仍 running。续跑必须从水位继续, 不重复处理已回填行。
func TestBackfill_ResumeFromWatermark(t *testing.T) {
	db := testutil.OpenTestDB(t)
	living := seedBackfillScenario(t, db, "resume:0x30", 1, 0, 6)
	devID := living[0].ID
	var ld models.LogicalDevice
	if err := db.Where("identity_key = ?", "resume:0x30").First(&ld).Error; err != nil {
		t.Fatal(err)
	}

	// 模拟前 3 行已回填 (上一次运行完成了它们), 后 3 行仍 NULL。
	if err := db.Exec(
		"UPDATE unified_data SET logical_device_id = ? WHERE id <= 3 AND device_id = ?",
		ld.ID, devID).Error; err != nil {
		t.Fatal(err)
	}
	// 预置水位行: watermark=3, status=running。
	if err := db.Create(&models.BackfillJob{
		JobType: models.BackfillJobTypeLogicalID, Table: "unified_data",
		Status: models.BackfillStatusRunning, WatermarkID: 3,
	}).Error; err != nil {
		t.Fatal(err)
	}

	results, err := newTestBackfiller(db, 2).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	var unified BackfillResult
	for _, r := range results {
		if r.Table == "unified_data" {
			unified = r
		}
		if r.Err != "" {
			t.Fatalf("table %s: %s", r.Table, r.Err)
		}
	}
	if unified.ResumedFrom != 3 {
		t.Errorf("resumed_from = %d, want 3", unified.ResumedFrom)
	}
	// 只回填了水位以上的 3 行 (幂等条件也使 id<=3 的行不会被重复处理)。
	if unified.RowsUpdated != 3 {
		t.Errorf("rows_updated = %d, want 3", unified.RowsUpdated)
	}
	if !unified.VerifyPassed {
		t.Errorf("verify failed: rows_missing=%d", unified.RowsMissing)
	}
	if n := countNullLogicalRows(t, db, "unified_data"); n != 0 {
		t.Errorf("%d unified_data rows still NULL", n)
	}
}

// 中断路径: 已取消的 context → 立即返回, 不改 job 状态; 之后正常续跑。
func TestBackfill_CancelledContext_ThenResume(t *testing.T) {
	db := testutil.OpenTestDB(t)
	seedBackfillScenario(t, db, "cancel:0x40", 1, 0, 4)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消
	results, err := newTestBackfiller(db, 1).RunOnce(ctx)
	if err == nil {
		t.Fatal("RunOnce with cancelled ctx: want error, got nil")
	}
	for _, r := range results {
		if r.Err == "" {
			t.Errorf("table %s: want cancellation error", r.Table)
		}
	}
	if n := countNullLogicalRows(t, db, "unified_data"); n != 4 {
		t.Errorf("cancelled run touched data: %d NULL rows remain, want 4", n)
	}

	// 续跑 (新 context) 完成。
	results, err = newTestBackfiller(db, 1).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("resume RunOnce: %v", err)
	}
	for _, r := range results {
		if r.Err != "" || !r.VerifyPassed {
			t.Errorf("table %s: err=%q verify_passed=%v", r.Table, r.Err, r.VerifyPassed)
		}
	}
	if n := countNullLogicalRows(t, db, "unified_data"); n != 0 {
		t.Errorf("after resume: %d unified_data rows still NULL", n)
	}
}

// ==================== 幂等 ====================

func TestBackfill_Idempotent_SecondRunNoOp(t *testing.T) {
	db := testutil.OpenTestDB(t)
	seedBackfillScenario(t, db, "idem:0x50", 2, 0, 3)

	first, err := newTestBackfiller(db, 4).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("first RunOnce: %v", err)
	}
	for _, r := range first {
		if r.Err != "" || !r.VerifyPassed {
			t.Fatalf("first run table %s: err=%q verify=%v", r.Table, r.Err, r.VerifyPassed)
		}
	}

	second, err := newTestBackfiller(db, 4).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("second RunOnce: %v", err)
	}
	for _, r := range second {
		if r.Err != "" {
			t.Fatalf("second run table %s: %s", r.Table, r.Err)
		}
		if r.RowsUpdated != 0 {
			t.Errorf("second run table %s updated %d rows, want 0", r.Table, r.RowsUpdated)
		}
		if !r.VerifyPassed {
			t.Errorf("second run table %s verify failed", r.Table)
		}
		if r.ResumedFrom == 0 {
			t.Errorf("second run table %s resumed_from=0, want prior watermark", r.Table)
		}
	}

	// job 行唯一 (重复执行不产生重复水位行)。
	var count int64
	if err := db.Model(&models.BackfillJob{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("backfill_jobs rows = %d, want 2 (one per table)", count)
	}
}

// ==================== 校验自检两分支 ====================

func TestVerify_FailureBranch_EligibleRowStillNull(t *testing.T) {
	db := testutil.OpenTestDB(t)
	seedBackfillScenario(t, db, "verify:0x60", 1, 0, 2)

	results, err := newTestBackfiller(db, 4).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	for _, r := range results {
		if r.Err != "" || !r.VerifyPassed {
			t.Fatalf("initial backfill table %s: err=%q verify=%v", r.Table, r.Err, r.VerifyPassed)
		}
	}

	// 人为制造漏洞: 把校验范围内一行已回填的行重置为 NULL
	// (模拟回填遗漏/损坏), done 短路路径的校验必须检出。
	if err := db.Exec(
		"UPDATE unified_data SET logical_device_id = NULL WHERE id = (SELECT MIN(id) FROM unified_data)").Error; err != nil {
		t.Fatalf("reset row: %v", err)
	}

	// done 短路路径仍执行校验 → 检出缺失。
	results, err = newTestBackfiller(db, 4).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("re-run: %v", err)
	}
	var unified BackfillResult
	for _, r := range results {
		if r.Table == "unified_data" {
			unified = r
		}
	}
	if unified.VerifyPassed {
		t.Error("verify passed, want FAILED for leaked NULL row")
	}
	if unified.RowsMissing != 1 {
		t.Errorf("rows_missing = %d, want 1", unified.RowsMissing)
	}
}

func TestVerify_PassBranch_PostBackfillWritesIgnored(t *testing.T) {
	db := testutil.OpenTestDB(t)
	// 无软删实例: SQLite 自增 id 单调, 回填后新写入行的 id 必然大于
	// 完成时水位 maxID (与 PG sequence 行为一致)。
	living := seedBackfillScenario(t, db, "window:0x70", 1, 0, 2)
	devID := living[0].ID

	if _, err := newTestBackfiller(db, 4).RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	// M→P1 窗口期新写入: 双写未激活, logical_device_id 仍 NULL
	// (id 大于完成时水位) —— §六 OR 安全网覆盖, 校验不得误报。
	if err := db.Create(&models.UnifiedData{
		DeviceID: devID, SensorName: "voltage", Value: 9, Timestamp: time.Now(),
	}).Error; err != nil {
		t.Fatal(err)
	}

	results, err := newTestBackfiller(db, 4).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("re-run: %v", err)
	}
	for _, r := range results {
		if r.Err != "" {
			t.Fatalf("table %s: %s", r.Table, r.Err)
		}
		if !r.VerifyPassed {
			t.Errorf("table %s: verify failed for out-of-range NULL row (rows_missing=%d)",
				r.Table, r.RowsMissing)
		}
	}
}

// ==================== 资格边界 ====================

// 实例尚未挂载 logical_device_id (P0 补建未覆盖的异常态): 其行不得被
// 回填 (子查询无匹配), 校验也不得把它计为缺失。
func TestBackfill_InstanceWithoutLogicalDevice_Skipped(t *testing.T) {
	db := testutil.OpenTestDB(t)
	dev := seedDevice(t, db, "nolog:0x80", "bms_jbd", "0x80", false) // 不挂 logical
	for r := 0; r < 3; r++ {
		if err := db.Create(&models.UnifiedData{
			DeviceID: dev.ID, SensorName: "voltage", Value: float64(r), Timestamp: time.Now(),
		}).Error; err != nil {
			t.Fatal(err)
		}
	}

	results, err := newTestBackfiller(db, 4).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	for _, r := range results {
		if r.Err != "" {
			t.Fatalf("table %s: %s", r.Table, r.Err)
		}
		if !r.VerifyPassed {
			t.Errorf("table %s: verify failed (rows_missing=%d) for ineligible rows",
				r.Table, r.RowsMissing)
		}
		if r.RowsUpdated != 0 {
			t.Errorf("table %s: updated %d rows, want 0", r.Table, r.RowsUpdated)
		}
	}
	if n := countNullLogicalRows(t, db, "unified_data"); n != 3 {
		t.Errorf("ineligible rows were touched: %d NULL remain, want 3", n)
	}
}

// ==================== 水位乐观锁 ====================

func TestAdvanceWatermark_OptimisticLock_NoRollback(t *testing.T) {
	db := testutil.OpenTestDB(t)
	job := &models.BackfillJob{
		JobType: models.BackfillJobTypeLogicalID, Table: "unified_data",
		Status: models.BackfillStatusRunning,
	}
	if err := db.Create(job).Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// 副本 A 推进 0→100。
	stale := *job // 副本 B 的过期视图 (watermark=0)
	if err := advanceBackfillWatermark(ctx, db, job, 100); err != nil {
		t.Fatalf("advance A: %v", err)
	}
	var dbJob models.BackfillJob
	if err := db.First(&dbJob, job.ID).Error; err != nil {
		t.Fatal(err)
	}
	if dbJob.WatermarkID != 100 {
		t.Fatalf("watermark = %d, want 100", dbJob.WatermarkID)
	}

	// 副本 B 拿过期视图推进 0→50: 条件 UPDATE 不命中, 水位不回退。
	if err := advanceBackfillWatermark(ctx, db, &stale, 50); err != nil {
		t.Fatalf("advance B: %v", err)
	}
	if err := db.First(&dbJob, job.ID).Error; err != nil {
		t.Fatal(err)
	}
	if dbJob.WatermarkID != 100 {
		t.Errorf("watermark rolled back to %d, want 100", dbJob.WatermarkID)
	}
}

func TestEnsureBackfillJob_IdempotentCreate(t *testing.T) {
	db := testutil.OpenTestDB(t)
	ctx := context.Background()
	j1, err := ensureBackfillJob(ctx, db, models.BackfillJobTypeLogicalID, "unified_data")
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	j2, err := ensureBackfillJob(ctx, db, models.BackfillJobTypeLogicalID, "unified_data")
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if j1.ID != j2.ID {
		t.Errorf("duplicate job rows: %d vs %d", j1.ID, j2.ID)
	}
	var count int64
	if err := db.Model(&models.BackfillJob{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("backfill_jobs rows = %d, want 1", count)
	}
}

func TestTableMaxID_EmptyTable(t *testing.T) {
	db := testutil.OpenTestDB(t)
	maxID, err := tableMaxID(context.Background(), db, "unified_data")
	if err != nil {
		t.Fatalf("tableMaxID: %v", err)
	}
	if maxID != 0 {
		t.Errorf("maxID = %d, want 0", maxID)
	}
}
