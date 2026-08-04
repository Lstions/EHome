package datalifecycle

import (
	"errors"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

// seedMergeSource 建一个可合并源: 逻辑设备 + 一个软删实例 + 数据行
// (unified_data/device_data 各 rows 条, timestamp 自 base 起逐行 +1h)。
// 注意: logical_device_id 必须在软删前挂载 (软删后 Model(dev).Update
// 被 GORM 默认 scope 静默命中 0 行)。
func seedMergeSource(t *testing.T, db *gorm.DB, key string, rows int, base time.Time) *models.LogicalDevice {
	t.Helper()
	ld := &models.LogicalDevice{IdentityKey: key, Name: key, DeviceType: "bms_jbd", RetentionDays: 365}
	if err := db.Create(ld).Error; err != nil {
		t.Fatalf("create logical device: %v", err)
	}
	dev := seedDevice(t, db, key+"-inst", "bms_jbd", key, false)
	db.Model(dev).Update("logical_device_id", ld.ID)
	for i := 0; i < rows; i++ {
		ts := base.Add(time.Duration(i) * time.Hour)
		if err := db.Create(&models.UnifiedData{
			DeviceID:        dev.ID,
			SensorName:      "voltage",
			Value:           float64(i),
			Timestamp:       ts,
			LogicalDeviceID: &ld.ID,
		}).Error; err != nil {
			t.Fatalf("seed unified_data: %v", err)
		}
		if err := db.Create(&models.DeviceData{
			DeviceID:        dev.ID,
			NodeID:          "NODE001",
			DataJSON:        `{"v":1}`,
			Timestamp:       ts,
			LogicalDeviceID: &ld.ID,
		}).Error; err != nil {
			t.Fatalf("seed device_data: %v", err)
		}
	}
	db.Delete(dev) // 软删实例 — 数据保留
	return ld
}

func TestMergeDevices_Success(t *testing.T) {
	db := testutil.OpenTestDB(t)
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	src1 := seedMergeSource(t, db, "src1", 3, base)
	src2 := seedMergeSource(t, db, "src2", 2, base.Add(24*time.Hour))

	SetSystemRetentionDays(200)
	defer SetSystemRetentionDays(365)

	result, err := MergeDevices(db, &MergeRequest{TargetName: "合并BMS", SourceIDs: []uint{src1.ID, src2.ID}})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if result.TargetID == 0 || len(result.JobIDs) != 2 {
		t.Fatalf("unexpected result: %+v", result)
	}

	// 目标: identity_key=merge: 前缀, retention_days 快照, 无实例。
	var target models.LogicalDevice
	if err := db.First(&target, result.TargetID).Error; err != nil {
		t.Fatalf("load target: %v", err)
	}
	if len(target.IdentityKey) < 6 || target.IdentityKey[:6] != "merge:" {
		t.Errorf("target identity_key = %q, want merge: prefix", target.IdentityKey)
	}
	if target.Name != "合并BMS" || target.DeviceType != "bms_jbd" {
		t.Errorf("target = name %q type %q", target.Name, target.DeviceType)
	}
	if target.RetentionDays != 200 {
		t.Errorf("target retention_days = %d, want snapshot 200", target.RetentionDays)
	}

	// 源: merged_into + merge_status=pending。
	for _, src := range []uint{src1.ID, src2.ID} {
		var ld models.LogicalDevice
		db.First(&ld, src)
		if ld.MergedInto == nil || *ld.MergedInto != result.TargetID {
			t.Errorf("source %d merged_into = %v, want %d", src, ld.MergedInto, result.TargetID)
		}
		if ld.MergeStatus == nil || *ld.MergeStatus != models.MergeStatusPending {
			t.Errorf("source %d merge_status = %v, want pending", src, ld.MergeStatus)
		}
	}

	// merge_jobs: 每源一行 pending。
	var jobs []models.MergeJob
	db.Where("target_logical_id = ?", result.TargetID).Find(&jobs)
	if len(jobs) != 2 {
		t.Fatalf("merge_jobs = %d rows, want 2", len(jobs))
	}
	for _, j := range jobs {
		if j.Status != models.MergeJobPending || j.WatermarkPhase != models.MergePhaseUnifiedData {
			t.Errorf("job %d status=%s phase=%s", j.ID, j.Status, j.WatermarkPhase)
		}
	}
}

func TestMergeDevices_RejectsLivingInstance(t *testing.T) {
	db := testutil.OpenTestDB(t)
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	src1 := seedMergeSource(t, db, "src1", 1, base)
	src2 := seedMergeSource(t, db, "src2", 1, base)

	// src2 重建存活实例 → 合并必须 409 且整体回滚。
	living := seedDevice(t, db, "src2-live", "bms_jbd", "src2", false)
	db.Model(living).Update("logical_device_id", src2.ID)

	_, err := MergeDevices(db, &MergeRequest{TargetName: "X", SourceIDs: []uint{src1.ID, src2.ID}})
	var verr *MergeValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("expected MergeValidationError, got %v", err)
	}
	found := false
	for _, c := range verr.Conflicts {
		if c.LogicalDeviceID == src2.ID && c.Reason == MergeConflictAliveInstance {
			found = true
			if c.InstanceID != living.ID {
				t.Errorf("conflict instance_id = %d, want %d", c.InstanceID, living.ID)
			}
			if c.NodeName != "n" {
				t.Errorf("conflict node_name = %q, want n", c.NodeName)
			}
		}
	}
	if !found {
		t.Fatalf("conflicts missing alive_instance for src2: %+v", verr.Conflicts)
	}

	// 整体回滚: 无目标、无 jobs、src1 未被占位。
	var targets int64
	db.Model(&models.LogicalDevice{}).Where("identity_key LIKE ?", "merge:%").Count(&targets)
	if targets != 0 {
		t.Errorf("rollback failed: %d merge targets exist", targets)
	}
	var jobs int64
	db.Model(&models.MergeJob{}).Count(&jobs)
	if jobs != 0 {
		t.Errorf("rollback failed: %d merge jobs exist", jobs)
	}
	var reloaded models.LogicalDevice
	db.First(&reloaded, src1.ID)
	if reloaded.MergedInto != nil || reloaded.MergeStatus != nil {
		t.Errorf("src1 partially occupied: merged_into=%v status=%v", reloaded.MergedInto, reloaded.MergeStatus)
	}
}

func TestMergeDevices_RejectsPurgeRequested(t *testing.T) {
	db := testutil.OpenTestDB(t)
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	src1 := seedMergeSource(t, db, "src1", 1, base)
	src2 := seedMergeSource(t, db, "src2", 1, base)
	db.Model(&models.LogicalDevice{}).Where("id = ?", src2.ID).Update("purge_requested", true)

	_, err := MergeDevices(db, &MergeRequest{TargetName: "X", SourceIDs: []uint{src1.ID, src2.ID}})
	var verr *MergeValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("expected MergeValidationError, got %v", err)
	}
	for _, c := range verr.Conflicts {
		if c.LogicalDeviceID == src2.ID && c.Reason == MergeConflictPurgeRequested {
			return
		}
	}
	t.Fatalf("conflicts missing purge_requested for src2: %+v", verr.Conflicts)
}

// TestMergeDevices_ConcurrentOccupation — 两个合并并发引用同一源:
// 乐观占位 (RowsAffected=0) 保证只有一个成功, 另一个整体回滚。
// SQLite 单连接下无法真并发, 用串行模拟: 手工预占位等价于另一事务先到。
func TestMergeDevices_ConcurrentOccupation(t *testing.T) {
	db := testutil.OpenTestDB(t)
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	src1 := seedMergeSource(t, db, "src1", 1, base)
	src2 := seedMergeSource(t, db, "src2", 1, base)
	src3 := seedMergeSource(t, db, "src3", 1, base)

	// 模拟并发事务 A 先占位 src1。
	otherTarget := models.LogicalDevice{IdentityKey: "merge:other", Name: "other", DeviceType: "bms_jbd"}
	db.Create(&otherTarget)
	res := db.Model(&models.LogicalDevice{}).
		Where("id = ? AND merged_into IS NULL AND (merge_status IS NULL OR merge_status = '')", src1.ID).
		Updates(map[string]interface{}{"merged_into": otherTarget.ID, "merge_status": models.MergeStatusPending})
	if res.RowsAffected != 1 {
		t.Fatalf("pre-occupation failed: %d rows", res.RowsAffected)
	}

	// 事务 B: 合并 src1+src2 → src1 占位 RowsAffected=0 → 409 + src2 回滚。
	_, err := MergeDevices(db, &MergeRequest{TargetName: "X", SourceIDs: []uint{src1.ID, src2.ID}})
	var verr *MergeValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("expected MergeValidationError, got %v", err)
	}
	found := false
	for _, c := range verr.Conflicts {
		if c.LogicalDeviceID == src1.ID && c.Reason == MergeConflictAlreadyMerging {
			found = true
		}
	}
	if !found {
		t.Fatalf("conflicts missing already_merging for src1: %+v", verr.Conflicts)
	}
	var s2 models.LogicalDevice
	db.First(&s2, src2.ID)
	if s2.MergedInto != nil || s2.MergeStatus != nil {
		t.Errorf("src2 not rolled back: merged_into=%v status=%v", s2.MergedInto, s2.MergeStatus)
	}

	// src3 + src2 仍可正常合并 (未被污染)。
	result, err := MergeDevices(db, &MergeRequest{TargetName: "Y", SourceIDs: []uint{src2.ID, src3.ID}})
	if err != nil {
		t.Fatalf("second merge should succeed: %v", err)
	}
	if result.TargetID == 0 {
		t.Fatal("second merge returned no target")
	}
}

// TestMergeDevices_ConcurrentRace — 真并发 (goroutine) 下同一源只允许
// 一个合并成功。PG 下 UPDATE 串行; SQLite 单连接测试跳过。
func TestMergeDevices_ConcurrentRace(t *testing.T) {
	if !testutil.IsPostgres() {
		t.Skip("true concurrency requires PostgreSQL")
	}
	db := testutil.OpenTestDB(t)
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	src1 := seedMergeSource(t, db, "src1", 2, base)
	src2 := seedMergeSource(t, db, "src2", 2, base)

	const racers = 4
	var wg sync.WaitGroup
	successes := make(chan *MergeResult, racers)
	failures := make(chan error, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			// 每个 goroutine 自己的连接 (PG schema 隔离由 testutil 保证)。
			res, err := MergeDevices(db, &MergeRequest{
				TargetName: "race-" + string(rune('A'+n)),
				SourceIDs:  []uint{src1.ID, src2.ID},
			})
			if err != nil {
				failures <- err
				return
			}
			successes <- res
		}(i)
	}
	wg.Wait()
	close(successes)
	close(failures)

	ok := 0
	for range successes {
		ok++
	}
	if ok != 1 {
		t.Fatalf("concurrent races: %d successes, want exactly 1", ok)
	}
	// 失败者必须是 MergeValidationError (占位冲突), 不是 DB 错误。
	for err := range failures {
		var verr *MergeValidationError
		if !errors.As(err, &verr) {
			t.Errorf("race failure is not a validation conflict: %v", err)
		}
	}
}

func TestMergeDevices_RejectsMixedDeviceTypes(t *testing.T) {
	db := testutil.OpenTestDB(t)
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	src1 := seedMergeSource(t, db, "src1", 1, base)
	other := &models.LogicalDevice{IdentityKey: "sensor:0x01", Name: "sensor", DeviceType: "sn3001_rain", RetentionDays: 365}
	db.Create(other)

	_, err := MergeDevices(db, &MergeRequest{TargetName: "X", SourceIDs: []uint{src1.ID, other.ID}})
	if err == nil {
		t.Fatal("mixed device types should be rejected")
	}
}

func TestMergeDevices_Validation(t *testing.T) {
	db := testutil.OpenTestDB(t)
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	src1 := seedMergeSource(t, db, "src1", 1, base)

	if _, err := MergeDevices(db, &MergeRequest{TargetName: "", SourceIDs: []uint{src1.ID}}); err == nil {
		t.Error("empty target_name should fail")
	}
	if _, err := MergeDevices(db, &MergeRequest{TargetName: "X", SourceIDs: []uint{src1.ID}}); err == nil {
		t.Error("single source should fail")
	}
	if _, err := MergeDevices(db, &MergeRequest{TargetName: "X", SourceIDs: []uint{src1.ID, src1.ID}}); err == nil {
		t.Error("duplicate sources (deduped to 1) should fail")
	}
	if _, err := MergeDevices(db, &MergeRequest{TargetName: "X", SourceIDs: []uint{999, 998}}); err == nil {
		t.Error("nonexistent sources should fail")
	}
}
