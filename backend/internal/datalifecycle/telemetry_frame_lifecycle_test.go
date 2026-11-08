package datalifecycle

// telemetry_frame_lifecycle_test.go — L2 帧表加入生命周期的验收（评审 B-3）。
//
// 这组用例存在的理由：帧表成为 L2 主路径后，「设备删了但帧数据还在」与
// 「帧数据永不清理」是两个**不可逆**的事故（前者破坏"彻底删除"语义，后者
// 是无界增长的第二张表）。它们在语法上不会报错，只会在数据库里静默累积，
// 因此必须有行为级用例钉住。

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

// openFrameLifecycleDB 打开测试库并**额外**建出 telemetry_frame/telemetry_slot。
//
// testutil.allModels 里没有这两个模型（TelemetryFrame 刻意不进生产 AutoMigrate，
// 见 models/telemetry_frame.go 的范式说明），所以这里显式补建。
func openFrameLifecycleDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testutil.OpenTestDB(t)
	if err := db.AutoMigrate(&models.TelemetryFrame{}, &models.TelemetrySlot{}); err != nil {
		t.Fatalf("automigrate telemetry tables: %v", err)
	}
	return db
}

// seedFrame 写一行帧（时间戳可以跨越保留期）。
func seedFrame(t *testing.T, db *gorm.DB, deviceID uint, ts time.Time, seq int16, kind int16) {
	t.Helper()
	f := models.TelemetryFrame{
		DeviceID:  deviceID,
		TS:        ts,
		Seq:       seq,
		FrameKind: kind,
		SlotID:    1,
		Payload:   []byte{0x00, 0x01, 0xAA},
	}
	if err := db.Create(&f).Error; err != nil {
		t.Fatalf("seed frame device=%d ts=%s: %v", deviceID, ts.Format(time.RFC3339), err)
	}
}

// TestTimeseriesTablesIncludesFrame 守清单本身：三张表、列名正确。
//
// 这是**清单门禁**：任何一张表从清单里消失，对应的生命周期路径都会静默失效。
// 列名断言同样重要 —— telemetry_frame 的时间列是 ts 不是 timestamp，写错就是
// SQL 报「列不存在」，而这类错误只在真有数据时才会触发。
func TestTimeseriesTablesIncludesFrame(t *testing.T) {
	tables := TimeseriesTables()
	if len(tables) != 3 {
		t.Fatalf("TimeseriesTables 有 %d 张表，want 3（unified_data/device_data/telemetry_frame）", len(tables))
	}
	want := map[string]string{
		"unified_data":    "timestamp",
		"device_data":     "timestamp",
		"telemetry_frame": "ts",
	}
	hasID := map[string]bool{"unified_data": true, "device_data": true, "telemetry_frame": false}
	for _, tbl := range tables {
		tsCol, ok := want[tbl.Name]
		if !ok {
			t.Fatalf("清单里出现了未预期的表 %q", tbl.Name)
		}
		if tbl.TSColumn != tsCol {
			t.Errorf("%s.TSColumn = %q, want %q", tbl.Name, tbl.TSColumn, tsCol)
		}
		if tbl.HasID != hasID[tbl.Name] {
			t.Errorf("%s.HasID = %v, want %v（帧表没有 id 列，批量删必须用 (device_id,ts,seq)）",
				tbl.Name, tbl.HasID, hasID[tbl.Name])
		}
	}
	// 返回副本：调用方修改不得影响下一次调用（否则某个调用方删一行就全局失效）。
	tables[0].Name = "mutated"
	if TimeseriesTables()[0].Name == "mutated" {
		t.Error("TimeseriesTables 必须每次返回新切片")
	}
}

// TestRetentionDeletesExpiredFrames 守「帧数据会真的按保留期被清理」。
func TestRetentionDeletesExpiredFrames(t *testing.T) {
	db := openFrameLifecycleDB(t)
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	SetSystemRetentionDays(90)
	defer SetSystemRetentionDays(90)

	dev := seedDevice(t, db, "frame-ret", "bms_jbd", "frame-ret-hw", false)
	// 过期帧（400 天前）与新鲜帧（10 天前）；同一设备。
	seedFrame(t, db, dev.ID, now.AddDate(0, 0, -400), 0, models.TelemetryFrameKindKey)
	seedFrame(t, db, dev.ID, now.AddDate(0, 0, -400), 1, models.TelemetryFrameKindDelta)
	seedFrame(t, db, dev.ID, now.AddDate(0, 0, -10), 0, models.TelemetryFrameKindKey)
	// 另一张表也放一条过期行，确认多表都被清理（不是只清帧表）。
	if err := db.Create(&models.UnifiedData{
		DeviceID: dev.ID, SensorName: "voltage", Value: 1,
		Timestamp: now.AddDate(0, 0, -400),
	}).Error; err != nil {
		t.Fatalf("seed unified row: %v", err)
	}

	r := NewRetentionTask(db)
	r.now = func() time.Time { return now }
	r.SetBatchSize(1) // 强制分批：帧表每批只能删 1 行
	r.SetBatchSleep(0)
	if _, err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("retention run: %v", err)
	}

	var frames int64
	db.Model(&models.TelemetryFrame{}).Where("device_id = ?", dev.ID).Count(&frames)
	if frames != 1 {
		t.Errorf("保留期后帧数 = %d, want 1（过期 2 行必须被删、新鲜 1 行必须留）", frames)
	}
	var unified int64
	db.Model(&models.UnifiedData{}).Where("device_id = ?", dev.ID).Count(&unified)
	if unified != 0 {
		t.Errorf("unified_data 过期行未清理: 剩 %d 行", unified)
	}
	// 新鲜帧必须还在原时间点（不能被误删）
	var fresh models.TelemetryFrame
	if err := db.Where("device_id = ?", dev.ID).First(&fresh).Error; err != nil {
		t.Fatalf("read surviving frame: %v", err)
	}
	if !fresh.TS.Equal(now.AddDate(0, 0, -10)) {
		t.Errorf("存活的帧 ts = %s, want %s（删错了时间窗）", fresh.TS, now.AddDate(0, 0, -10))
	}
}

// TestPurgeDeletesAllFramesOfDevice 守「彻底删除设备会连帧数据一起删」。
func TestPurgeDeletesAllFramesOfDevice(t *testing.T) {
	db := openFrameLifecycleDB(t)
	now := time.Now().UTC()

	// 与 DELETE /edge-devices/:id?delete_data=true 同形状：软删 + 置删除数据标记。
	dev := seedDevice(t, db, "frame-purge", "bms_jbd", "frame-purge-hw", false)
	if err := db.Delete(dev).Error; err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	if err := db.Unscoped().Model(&models.EdgeDevice{}).Where("id = ?", dev.ID).
		Update("data_purge_requested", true).Error; err != nil {
		t.Fatalf("mark purge requested: %v", err)
	}
	// 该设备 3 帧（跨不同时间/seq），另有一台**存活设备**的 1 帧必须不受影响。
	for i := 0; i < 3; i++ {
		seedFrame(t, db, dev.ID, now.Add(-time.Duration(i)*time.Minute), int16(i), models.TelemetryFrameKindKey)
	}
	other := seedDevice(t, db, "frame-keep", "bms_jbd", "frame-keep-hw", false)
	seedFrame(t, db, other.ID, now, 0, models.TelemetryFrameKindKey)

	p := NewDevicePurger(db)
	p.SetBatchSize(1) // 强制分批：验证批量锚点 (device_id,ts,seq) 正确
	p.SetSchedule(time.Hour, time.Hour, 0)
	results, err := p.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("purger run: %v", err)
	}
	if len(results) != 1 || results[0].Outcome != Purged {
		t.Fatalf("purge 结果 = %+v, want 单台 purged", results)
	}

	var left int64
	db.Model(&models.TelemetryFrame{}).Where("device_id = ?", dev.ID).Count(&left)
	if left != 0 {
		t.Errorf("设备已彻底删除但仍有 %d 帧残留 —— 「彻底删除」语义被打破", left)
	}
	var kept int64
	db.Model(&models.TelemetryFrame{}).Where("device_id = ?", other.ID).Count(&kept)
	if kept != 1 {
		t.Errorf("误删了其它设备的帧：剩 %d 行, want 1", kept)
	}
}

// TestMigrateFramesToDevice 守合并历史的帧迁移（复制+幂等+精确删源）。
func TestMigrateFramesToDevice(t *testing.T) {
	db := openFrameLifecycleDB(t)
	now := time.Date(2026, 11, 7, 3, 0, 0, 0, time.UTC)

	target := seedDevice(t, db, "frame-merge-target", "bms_jbd", "t-hw", false)
	src1 := seedDevice(t, db, "frame-merge-src1", "bms_jbd", "s1-hw", true)
	src2 := seedDevice(t, db, "frame-merge-src2", "bms_jbd", "s2-hw", true)

	// 源设备各 2 帧（不重叠时间）。
	seedFrame(t, db, src1.ID, now.Add(-2*time.Minute), 0, models.TelemetryFrameKindKey)
	seedFrame(t, db, src1.ID, now.Add(-1*time.Minute), 0, models.TelemetryFrameKindDelta)
	seedFrame(t, db, src2.ID, now.Add(-4*time.Minute), 0, models.TelemetryFrameKindKey)
	seedFrame(t, db, src2.ID, now.Add(-3*time.Minute), 0, models.TelemetryFrameKindDelta)
	// 目标设备在**其中一帧的同一 (ts, seq)** 上已有行：迁移时必须被丢弃而不是撞主键失败。
	seedFrame(t, db, target.ID, now.Add(-1*time.Minute), 0, models.TelemetryFrameKindKey)

	sources := []uint{src1.ID, src2.ID}
	moved, skipped, err := MigrateTelemetryFramesToDevice(context.Background(), db, target.ID, sources, 2)
	if err != nil {
		t.Fatalf("migrate frames: %v", err)
	}
	if moved != 3 {
		t.Errorf("moved = %d, want 3（4 帧里 1 帧与目标撞主键）", moved)
	}
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1（同 (device_id,ts,seq) 冲突）", skipped)
	}
	// 源设备的帧必须被删干净。
	var srcLeft int64
	db.Model(&models.TelemetryFrame{}).Where("device_id IN ?", sources).Count(&srcLeft)
	if srcLeft != 0 {
		t.Errorf("源设备仍有 %d 帧未删除（复制成功却删源失败 = 数据重复）", srcLeft)
	}
	// 目标设备应有 4 帧（原 1 + 迁入 3）。
	var targetFrames int64
	db.Model(&models.TelemetryFrame{}).Where("device_id = ?", target.ID).Count(&targetFrames)
	if targetFrames != 4 {
		t.Errorf("目标设备帧数 = %d, want 4", targetFrames)
	}

	// 幂等：重跑不应再搬任何东西，也不应报错。
	moved2, skipped2, err := MigrateTelemetryFramesToDevice(context.Background(), db, target.ID, sources, 2)
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if moved2 != 0 || skipped2 != 0 {
		t.Errorf("重跑 moved=%d skipped=%d, want 0/0（源已空）", moved2, skipped2)
	}
}

// TestMigrateFramesToDeviceSkipsWhenTableMissing 守「帧表不存在时合并不失败」。
//
// 精简测试库 / 迁移未跑的库没有 telemetry_frame。此时合并历史必须仍然是
// 可用的（只是不搬帧），而不是 500。
func TestMigrateFramesToDeviceSkipsWhenTableMissing(t *testing.T) {
	db := testutil.OpenTestDB(t) // 不建 telemetry_frame
	moved, skipped, err := MigrateTelemetryFramesToDevice(context.Background(), db, 1, []uint{2, 3}, 100)
	if err != nil {
		t.Fatalf("帧表不存在时不该报错: %v", err)
	}
	if moved != 0 || skipped != 0 {
		t.Errorf("moved=%d skipped=%d, want 0/0", moved, skipped)
	}
}

// TestDropTelemetryFramePartitionsNoopOnSQLite 守保留期的分区 DROP 在非 PG 下是 no-op。
func TestDropTelemetryFramePartitionsNoopOnSQLite(t *testing.T) {
	db := openFrameLifecycleDB(t)
	dropped, err := DropTelemetryFramePartitionsBefore(db, time.Now().AddDate(0, 0, -90))
	if err != nil {
		t.Fatalf("sqlite 下分区 DROP 必须 no-op: %v", err)
	}
	if len(dropped) != 0 {
		t.Errorf("dropped = %v, want none", dropped)
	}
}

// TestMigrateTelemetryFrameToPartitionedOnSQLite 守建表入口在 SQLite 下可用。
//
// 生产上这个函数还会建分区母表（PG）；SQLite 下 MigrateTableToPartitioned 是
// no-op，但 telemetry_slot 的 AutoMigrate **必须**发生 —— 否则测试库与精简部署
// 里 slot 字典表根本不存在，双写写第一帧就会失败。
func TestMigrateTelemetryFrameToPartitionedOnSQLite(t *testing.T) {
	db := testutil.OpenTestDB(t)
	if err := MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Fatalf("MigrateTelemetryFrameToPartitioned: %v", err)
	}
	if !db.Migrator().HasTable("telemetry_slot") {
		t.Error("迁移后 telemetry_slot 必须存在（slot 字典是解码的前提）")
	}
	// 幂等：重跑不报错。
	if err := MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Fatalf("二次迁移必须幂等: %v", err)
	}
}
