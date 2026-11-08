package backfill

// pg_cursor_test.go — 复合游标的**真 PostgreSQL** 端到端验收（2026-11-07 P0）。
//
// ============ 为什么这条用例必须存在 ============
//
// 第一版游标用 `watermark + 1ns` 推进，全部 SQLite 用例绿、真实数据 142,467 帧
// 对拍也全绿 —— 但**生产 PG 上会无限重复处理同一批**：
//
//	SELECT (timestamptz '2026-10-05 00:00:00+00' + interval '0.000000001 second')
//	     = timestamptz '2026-10-05 00:00:00+00';   -- t （加 1ns 等于没加）
//
// PG timestamptz 精度是**微秒**，SQLite 的 INT 时间是**纳秒**。两条路径各自自洽，
// 交叉点上没有用例 —— 这是本月第二次同形状的假绿（上一次是 L3 rollup 的
// updated_at 列漂移：SQLite 走 AutoMigrate、PG 走裸 DDL）。
//
// 症状（生产会是这个形状）：回填永远 running、percent 不动、一个核 100%、
// 磁盘 IO 持续、frames_done 涨到源数据的上百倍，**且不报错**。
//
// ============ 这条用例守什么 ============
//
// 1. 复合游标在 PG 上真的前进（不会取回同一批）；
// 2. frames_done == count(DISTINCT device_id, timestamp)（完整性）；
// 3. 同一 ts 多设备的批边界不丢帧（复合游标的第二分量）；
// 4. 恰好等于分区上界的最后一行取得到（闭区间上界，不靠 +1ns）。
//
// 变异验证（写进验收报告）：把游标改回 `Add(time.Nanosecond)` 后本用例必须红。

import (
	"context"
	"fmt"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/models"
	"ehome/backend/internal/telemetry"
	"ehome/backend/testutil"
)

// requirePostgres 跳过非 PG 环境。
func requirePostgres(t *testing.T) {
	t.Helper()
	if !testutil.IsPostgres() {
		t.Skip("需要 PostgreSQL（EHOME_TEST_DB=postgres）；本用例验证 PG timestamptz 精度下的游标正确性")
	}
}

// seedPGProjectedRows 往 unified_data 写一帧（多字段 = 多行，同 ts 同 device）。
func seedPGProjectedRows(t *testing.T, db *gorm.DB, deviceID uint, ts time.Time, fields map[string]float64) {
	t.Helper()
	for name, v := range fields {
		if err := db.Create(&models.UnifiedData{
			DeviceID:   deviceID,
			SensorName: name,
			Value:      v,
			Timestamp:  ts,
		}).Error; err != nil {
			t.Fatalf("seed %s@%s: %v", name, ts.Format(time.RFC3339Nano), err)
		}
	}
}

// newPGRunner 造 Runner（落 DB 的 slot 字典，与生产同一条 slot 派生路径）。
func newPGRunner(t *testing.T, db *gorm.DB, cfg Config) *Runner {
	t.Helper()
	dict := telemetry.NewDBSlots(db)
	if err := dict.WarmUp(); err != nil {
		t.Fatalf("warm up slots: %v", err)
	}
	if err := datalifecycle.EnsureBackfillProgressTable(db); err != nil {
		t.Fatalf("ensure progress table: %v", err)
	}
	return NewRunner(db, dict, cfg, false, nil)
}

// newPGDict 造落 DB 的 slot 字典。
func newPGDict(t *testing.T, db *gorm.DB) telemetry.SlotDictionary {
	t.Helper()
	dict := telemetry.NewDBSlots(db)
	if err := dict.WarmUp(); err != nil {
		t.Fatalf("warm up slots: %v", err)
	}
	return dict
}

// ensurePGTelemetryTables 建 telemetry_slot / telemetry_frame。
//
// testutil.allModels **不含**这两个模型（生产上分区母表由 partition_mgr 的裸 DDL 建，
// AutoMigrate 会把分区母表降级改写）。所以 PG 用例要自己建：
//
//	· telemetry_slot：普通表，AutoMigrate 即可（与 MigrateTelemetryFrameToPartitioned 同款）；
//	· telemetry_frame：走**生产同一条**建表路径（裸 DDL + 分区），不 AutoMigrate。
//
// 为什么必须用生产路径而不是 AutoMigrate：这样用例同时验证了「回填写入的表形状
// 与生产一致」；用 AutoMigrate 建普通表会掩盖掉分区相关的写入问题。
func ensurePGTelemetryTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := datalifecycle.MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Fatalf("建 telemetry_frame/slot: %v", err)
	}
}

// resetBackfillState 建表并清掉上一次用例残留的帧与状态行。
func resetBackfillState(t *testing.T, db *gorm.DB) {
	t.Helper()
	ensurePGTelemetryTables(t, db)
	db.Exec("DELETE FROM " + datalifecycle.BackfillProgressTable)
	db.Exec("DELETE FROM telemetry_frame")
}

// TestPGCursorTerminates 是核心回归：回填必须**跑完**，且 frames_done 精确。
//
// 数据规模刻意小（120 帧 × 6 字段 = 720 行）但批大小极小（batch=7）⇒ 至少 17 批。
// 只要游标不前进，第二批就会重复第一批，frames_done 立刻超过 120，用例必然红。
func TestPGCursorTerminates(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	resetBackfillState(t, db)

	base := time.Date(2026, 10, 5, 0, 0, 0, 123456000, time.UTC) // 微秒精度（PG 真实精度）
	const frames = 120
	for i := 0; i < frames; i++ {
		seedPGProjectedRows(t, db, 14, base.Add(time.Duration(i)*time.Second), bmsFrame(float64(i)*0.001))
	}

	r := newPGRunner(t, db, Config{BatchFrames: 7})
	// 用带超时的 ctx：游标不前进时 RunOnce 会无限循环，超时让它失败而不是挂死。
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	prog, err := r.RunOnce(ctx)
	if err != nil {
		t.Fatalf("回填失败（若为 context deadline exceeded，说明游标没前进、在重复处理同一批）: %v\n"+
			"这正是 2026-11-07 P0 的形状：PG timestamptz 精度是微秒，`watermark + 1ns` 等于没加。\n"+
			"进度快照: %+v", err, prog)
	}
	if prog.State != StateDone {
		t.Fatalf("回填未完成 state=%q（errors=%v）", prog.State, prog.Errors)
	}
	t.Logf("prog=%+v", prog)

	var written int64
	db.Model(&models.TelemetryFrame{}).Count(&written)
	if written != frames {
		t.Fatalf("telemetry_frame 帧数 = %d, want %d", written, frames)
	}
	// ★ 硬要求 1：frames_done 必须等于 distinct 帧数（进度对账口径）。
	var distinct int64
	db.Raw("SELECT count(*) FROM (SELECT DISTINCT device_id, timestamp FROM unified_data) t").
		Scan(&distinct)
	if prog.FramesDone != distinct {
		t.Errorf("frames_done = %d, want %d（count(DISTINCT device_id,timestamp)）—— 不等说明游标在重复或跳过",
			prog.FramesDone, distinct)
	}
	if distinct != frames {
		t.Fatalf("前置条件不符：distinct 帧数 = %d, want %d", distinct, frames)
	}
}

// TestPGCursorSameTimestampMultipleDevices 守复合游标第二分量。
//
// 场景：**多台设备在同一 ts**（线上真实发生：不同设备解析同一批总线帧）。
// 若游标只记 ts（`timestamp > ts`），批在中间截断时会**跳过同 ts 内 device_id
// 更大的帧** —— 静默丢数据，且完整性校验要跑完整个分区才会发现。
func TestPGCursorSameTimestampMultipleDevices(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	resetBackfillState(t, db)

	// 每台设备 10 帧，三台设备共用同一批时间戳。
	base := time.Date(2026, 10, 5, 1, 0, 0, 500000000, time.UTC)
	devices := []uint{14, 15, 2}
	const perDevice = 10
	for i := 0; i < perDevice; i++ {
		ts := base.Add(time.Duration(i) * time.Second)
		for _, dev := range devices {
			seedPGProjectedRows(t, db, dev, ts, bmsFrame(float64(i)*0.001))
		}
	}
	const totalFrames = perDevice * 3

	// batch=4 ⇒ 强制在「同 ts 多设备」中间截断。
	r := newPGRunner(t, db, Config{BatchFrames: 4})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	prog, err := r.RunOnce(ctx)
	if err != nil {
		t.Fatalf("回填失败: %v（进度=%+v）", err, prog)
	}

	var written int64
	db.Model(&models.TelemetryFrame{}).Count(&written)
	if written != totalFrames {
		t.Fatalf("帧数 = %d, want %d（少 = 复合游标第二分量丢了同 ts 的其它设备）", written, totalFrames)
	}
	for _, dev := range devices {
		var n int64
		db.Model(&models.TelemetryFrame{}).Where("device_id = ?", dev).Count(&n)
		if n != perDevice {
			t.Errorf("device %d 帧数 = %d, want %d", dev, n, perDevice)
		}
	}
}

// TestPGCursorResumeAfterInterrupt 守 PG 上的断点续跑（复合游标落库/读回）。
func TestPGCursorResumeAfterInterrupt(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	resetBackfillState(t, db)

	base := time.Date(2026, 10, 5, 2, 0, 0, 0, time.UTC)
	const frames = 40
	for i := 0; i < frames; i++ {
		seedPGProjectedRows(t, db, 14, base.Add(time.Duration(i)*time.Second), bmsFrame(float64(i)*0.001))
	}

	r := newPGRunner(t, db, Config{BatchFrames: 5})
	interrupted := fmt.Errorf("simulated interruption")
	r.testHookAfterBatch = func(batch int) error {
		if batch == 2 {
			return interrupted
		}
		return nil
	}
	if _, err := r.RunOnce(context.Background()); err == nil {
		t.Fatal("第一轮必须被中断")
	}

	var afterFirst int64
	db.Model(&models.TelemetryFrame{}).Count(&afterFirst)
	if afterFirst != 15 {
		t.Fatalf("中断后帧数 = %d, want 15（3 批 × 5 帧）", afterFirst)
	}
	// 断点必须两列都有值。
	var row datalifecycle.BackfillProgressRow
	if err := db.Where("part_name = ?", "unified_data_all").First(&row).Error; err != nil {
		t.Fatalf("读状态行: %v", err)
	}
	if row.Watermark == nil {
		t.Fatal("watermark 为 NULL，断点没落库")
	}
	if row.WatermarkDeviceID == 0 {
		t.Error("watermark_device_id = 0（未写入），续跑会按「跳过整段同 ts」恢复")
	}

	// 续跑。
	r2 := newPGRunner(t, db, Config{BatchFrames: 5})
	prog, err := r2.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("续跑失败: %v", err)
	}
	var total int64
	db.Model(&models.TelemetryFrame{}).Count(&total)
	if total != frames {
		t.Fatalf("续跑后帧数 = %d, want %d（多 = 重复，少 = 遗漏）", total, frames)
	}
	if prog.FramesDone != frames {
		t.Errorf("frames_done = %d, want %d（不能把续跑前的累加两次）", prog.FramesDone, frames)
	}
}
