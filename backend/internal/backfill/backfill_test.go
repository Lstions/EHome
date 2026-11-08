package backfill

// backfill_test.go — 回填引擎的 SQLite 行为验收（契约 §6.2/§6.3）。
//
// ============ 这组用例守什么 ============
//
// 回填引擎的失败模式**全是静默的**：
//
//	· 逐行而不是逐帧编码 ⇒ 帧数放大 29 倍、压缩率归零，但**不报错**；
//	· watermark 落在 ts 中间 ⇒ 恢复时跳过一批帧，但**不报错**；
//	· 超量程值被 clamp 且没写 raw ⇒ 历史值永久丢失，**不报错**。
//
// 三种都只能靠**行为断言**抓（帧数、行数、raw 内容），不能靠"跑起来没崩"。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/models"
	"ehome/backend/internal/telemetry"
)

// openBackfillTestDB 建一个含 unified_data / telemetry_frame / telemetry_slot /
// 状态表的内存 SQLite 库。
//
// 内存库每条新连接是独立库 ⇒ 钉死单连接（与 telemetry 包的测试同一教训：
// 不钉的话回填的读与写在两条连接上互相看不见）。
func openBackfillTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if sqlDB, derr := db.DB(); derr == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.AutoMigrate(
		&models.UnifiedData{},
		&models.TelemetryFrame{},
		&models.TelemetrySlot{},
		&datalifecycle.BackfillProgressRow{},
	); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

// seedFrameRows 写一帧的字段行（一帧 = 多个字段行）。
func seedFrameRows(t *testing.T, db *gorm.DB, deviceID uint, ts time.Time, fields map[string]float64) {
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

// bmsFrame 造一帧 BMS 典型字段（挑几个有精确 spec 的）。
func bmsFrame(base float64) map[string]float64 {
	return map[string]float64{
		"cell_voltage_1": 3.254 + base,
		"cell_voltage_2": 3.239 + base,
		"temperature_1":  24.45 + base,
		"current":        -12.5 + base,
		"total_voltage":  51.2 + base,
		"rsoc":           float64(int(87 + base*10)),
	}
}

// newTestRunner 造一个指向 db 的 Runner（内存 slot 字典即可）。
func newTestRunner(t *testing.T, db *gorm.DB, cfg Config) *Runner {
	t.Helper()
	dict := telemetry.NewMemorySlots()
	if err := datalifecycle.EnsureBackfillProgressTable(db); err != nil {
		t.Fatalf("ensure progress table: %v", err)
	}
	return NewRunner(db, dict, cfg, false, nil)
}

// TestBackfillGroupsRowsIntoFrames 是**最核心**的一条：一行一个字段，一帧一行。
//
// 断言：N 帧（每帧 6 字段）⇒ telemetry_frame 恰好 N 行，不是 6N 行。
// 这条如果退化成逐行编码，帧数会立刻变成 6 倍。
func TestBackfillGroupsRowsIntoFrames(t *testing.T) {
	db := openBackfillTestDB(t)
	base := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	const frames = 20
	for i := 0; i < frames; i++ {
		seedFrameRows(t, db, 14, base.Add(time.Duration(i)*time.Second), bmsFrame(float64(i)*0.001))
	}

	r := newTestRunner(t, db, Config{BatchFrames: 7}) // 故意用非整除的批大小
	prog, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if prog.State != StateDone {
		t.Errorf("state = %q, want %q（errors=%v）", prog.State, StateDone, prog.Errors)
	}

	var got int64
	db.Model(&models.TelemetryFrame{}).Count(&got)
	if got != frames {
		t.Fatalf("telemetry_frame 行数 = %d, want %d（一行一个字段 ≠ 一帧一行）", got, frames)
	}
	var rows int64
	db.Model(&models.UnifiedData{}).Count(&rows)
	if rows != int64(frames*6) {
		t.Fatalf("前置条件不符：unified_data 行数 = %d, want %d", rows, frames*6)
	}
	if prog.RowsDone != rows {
		t.Errorf("rows_done = %d, want %d", prog.RowsDone, rows)
	}
}

// TestBackfillKeyFramePolicy 守关键帧策略（契约 §4.5）：
// 首帧必为关键帧；之后每 KeyFrameInterval 强制一个。
//
// 用 1s 间隔的数据 + 3s 的 KeyFrameInterval ⇒ 期望关键帧落在 0s/3s/6s/... 附近。
func TestBackfillKeyFramePolicy(t *testing.T) {
	db := openBackfillTestDB(t)
	base := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	const frames = 10
	for i := 0; i < frames; i++ {
		seedFrameRows(t, db, 14, base.Add(time.Duration(i)*time.Second), bmsFrame(0))
	}

	r := newTestRunner(t, db, Config{BatchFrames: 100, KeyFrameInterval: 3 * time.Second})
	if _, err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	var keyFrames int64
	db.Model(&models.TelemetryFrame{}).
		Where("frame_kind = ?", models.TelemetryFrameKindKey).Count(&keyFrames)
	// 0s,3s,6s,9s ⇒ 4 个关键帧。
	if keyFrames != 4 {
		t.Errorf("关键帧数 = %d, want 4（0/3/6/9 秒各一个）", keyFrames)
	}
	// 首帧必须是关键帧（决定解码器能否从分区起点独立解码）。
	var first models.TelemetryFrame
	db.Order("ts ASC").First(&first)
	if first.FrameKind != models.TelemetryFrameKindKey {
		t.Errorf("首帧 frame_kind = %d, want %d（关键帧）", first.FrameKind, models.TelemetryFrameKindKey)
	}
}

// TestBackfillPreservesOutOfRangeValuesInRaw 守契约 §2.2/§7 裁决 3：
// 超量程值被 clamp，但**原文必须写进 raw 列**。
//
// 这是「所有数据长期保留」承诺的唯一兑现方式 —— 丢弃或篡改都不行。
func TestBackfillPreservesOutOfRangeValuesInRaw(t *testing.T) {
	db := openBackfillTestDB(t)
	base := time.Date(2026, 10, 19, 8, 53, 0, 0, time.UTC)
	// battery_voltage 的 spec 量程 0~204.7V；1e12 必被 clamp。
	fields := map[string]float64{
		"battery_voltage": 1e12,
		"total_voltage":   51.2,
	}
	seedFrameRows(t, db, 15, base, fields)
	// 第二帧正常值：确认只有超量程的帧才有 raw。
	seedFrameRows(t, db, 15, base.Add(time.Second), map[string]float64{
		"battery_voltage": 51.2,
		"total_voltage":   51.3,
	})

	r := newTestRunner(t, db, Config{BatchFrames: 10})
	if _, err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	var withRaw, withoutRaw int64
	db.Model(&models.TelemetryFrame{}).Where("raw IS NOT NULL").Count(&withRaw)
	db.Model(&models.TelemetryFrame{}).Where("raw IS NULL").Count(&withoutRaw)
	if withRaw != 1 {
		t.Fatalf("有 raw 的帧数 = %d, want 1（只有超量程那一帧）", withRaw)
	}
	if withoutRaw != 1 {
		t.Errorf("无 raw 的帧数 = %d, want 1（常规帧必须留 NULL，否则体积爆炸）", withoutRaw)
	}

	var rawFrame models.TelemetryFrame
	db.Where("raw IS NOT NULL").First(&rawFrame)
	var rp rawPayload
	if err := json.Unmarshal(rawFrame.Raw, &rp); err != nil {
		t.Fatalf("raw 不是合法 JSON: %v（内容=%s）", err, string(rawFrame.Raw))
	}
	if len(rp.Clamped) != 1 {
		t.Fatalf("clamped 条数 = %d, want 1", len(rp.Clamped))
	}
	cv := rp.Clamped[0]
	if cv.Field != "battery_voltage" {
		t.Errorf("clamped 字段 = %q, want battery_voltage", cv.Field)
	}
	if cv.Value != 1e12 {
		t.Errorf("原始值 = %v, want 1e12（必须保原值，不能记 clamp 后的值）", cv.Value)
	}
	if cv.SpecMax < 200 || cv.SpecMax > 210 {
		t.Errorf("spec_max = %v, want ≈204.7（排查时需要知道当时的量程）", cv.SpecMax)
	}
}

// TestBackfillResumesFromWatermark 守断点续跑：中途打断后重跑，不重复不遗漏。
//
// 这是契约 §6.3 的硬要求。
//
// ============ 为什么用 testHookAfterBatch 而不是「起个 goroutine 延时取消」============
//
// 第一版用的是 go func(){ time.Sleep(60ms); cancel() }()。它是**假绿**：
// 内存 SQLite 上 40 帧的回填只要几毫秒，取消到达时早就跑完了 —— 用例变成
// 「完整跑一遍」，续跑一行都没验证，却还是绿的。
//
// 真实缺陷是「打断与速度的竞态」，不是「取消机制不好用」。用确定性钩子在
// 第 3 批之后返回错误，才能保证「确实中途停了、确实有 watermark、续跑确实接上」。
func TestBackfillResumesFromWatermark(t *testing.T) {
	db := openBackfillTestDB(t)
	base := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	const frames = 40
	for i := 0; i < frames; i++ {
		seedFrameRows(t, db, 14, base.Add(time.Duration(i)*time.Second), bmsFrame(float64(i)*0.001))
	}

	r := newTestRunner(t, db, Config{BatchFrames: 5})
	// 第 3 批之后（batchIdx==2 已提交）模拟进程被中断。
	interrupted := errors.New("simulated interruption")
	r.testHookAfterBatch = func(batch int) error {
		if batch == 2 {
			return interrupted
		}
		return nil
	}
	if _, err := r.RunOnce(context.Background()); !errors.Is(err, interrupted) {
		t.Fatalf("第一轮必须被中断，得到 err=%v", err)
	}

	// 中断后必须**部分完成**：3 批 × 5 帧 = 15 帧。
	var afterFirst int64
	db.Model(&models.TelemetryFrame{}).Count(&afterFirst)
	if afterFirst != 15 {
		t.Fatalf("中断后帧数 = %d, want 15（3 批 × 5 帧）—— 若不是 15，说明批边界或中断点不符预期", afterFirst)
	}
	// 状态行必须是 failed 且 watermark 停在第 15 帧（0-based index 14）。
	var row datalifecycle.BackfillProgressRow
	if err := db.Where("part_name = ?", "unified_data_all").First(&row).Error; err != nil {
		t.Fatalf("读中断后的状态行: %v", err)
	}
	if row.State != datalifecycle.BackfillFailed {
		t.Errorf("中断后 state = %q, want failed", row.State)
	}
	wantWM := base.Add(14 * time.Second)
	if row.Watermark == nil || !row.Watermark.Equal(wantWM) {
		t.Fatalf("中断后 watermark = %v, want %v", row.Watermark, wantWM)
	}
	if row.FramesDone != 15 {
		t.Errorf("中断后 frames_done = %d, want 15", row.FramesDone)
	}

	// 第二轮：新 Runner（模拟进程重启），正常续跑。
	r2 := newTestRunner(t, db, Config{BatchFrames: 5})
	prog, err := r2.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("续跑失败: %v", err)
	}
	if prog.State != StateDone {
		t.Errorf("续跑后状态 = %q, want %q（errors=%v）", prog.State, StateDone, prog.Errors)
	}

	var total int64
	db.Model(&models.TelemetryFrame{}).Count(&total)
	if total != frames {
		t.Fatalf("续跑后帧数 = %d, want %d（多于 = 重复编码，少于 = 跳过了帧）", total, frames)
	}

	if err := db.Where("part_name = ?", "unified_data_all").First(&row).Error; err != nil {
		t.Fatalf("读最终状态行: %v", err)
	}
	if row.State != datalifecycle.BackfillDone {
		t.Errorf("最终 state = %q, want done", row.State)
	}
	if row.Watermark == nil || !row.Watermark.Equal(base.Add(time.Duration(frames-1)*time.Second)) {
		t.Errorf("最终 watermark = %v, want %v", row.Watermark, base.Add(time.Duration(frames-1)*time.Second))
	}
	if row.FramesDone != frames {
		t.Errorf("最终 frames_done = %d, want %d（必须是真实帧数，不能累加两次）", row.FramesDone, frames)
	}
}

// TestBackfillIdempotentRerun 守「整体重跑不产生重复帧」。
func TestBackfillIdempotentRerun(t *testing.T) {
	db := openBackfillTestDB(t)
	base := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	const frames = 12
	for i := 0; i < frames; i++ {
		seedFrameRows(t, db, 14, base.Add(time.Duration(i)*time.Second), bmsFrame(0))
	}
	r := newTestRunner(t, db, Config{BatchFrames: 4})
	if _, err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("首跑: %v", err)
	}

	// 删掉 done 状态强制重跑（模拟"状态表丢失但帧表已有数据"）。
	if err := db.Where("part_name = ?", "unified_data_all").Delete(&datalifecycle.BackfillProgressRow{}).Error; err != nil {
		t.Fatalf("reset progress: %v", err)
	}
	r2 := newTestRunner(t, db, Config{BatchFrames: 4})
	if _, err := r2.RunOnce(context.Background()); err != nil {
		t.Fatalf("重跑: %v", err)
	}
	var total int64
	db.Model(&models.TelemetryFrame{}).Count(&total)
	if total != frames {
		t.Errorf("重跑后帧数 = %d, want %d（ON CONFLICT DO NOTHING 保证幂等）", total, frames)
	}
}

// TestBackfillDisabledIsNoop 守应急阀：disabled 时不碰数据库。
func TestBackfillDisabledIsNoop(t *testing.T) {
	db := openBackfillTestDB(t)
	seedFrameRows(t, db, 14, time.Now().UTC(), bmsFrame(0))
	r := NewRunner(db, telemetry.NewMemorySlots(), Config{}, true, nil)
	if r.Enabled() {
		t.Fatal("disabled 的 Runner 必须报告 Enabled()==false")
	}
	prog, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("disabled RunOnce 不该报错: %v", err)
	}
	if prog.State != StateDisabled {
		t.Errorf("state = %q, want %q", prog.State, StateDisabled)
	}
	var n int64
	db.Model(&models.TelemetryFrame{}).Count(&n)
	if n != 0 {
		t.Errorf("disabled 时写了 %d 帧，want 0", n)
	}
}

// TestConfigFromEnvParsing 守配置解析（含非法值回落 + 告警）。
func TestConfigFromEnvParsing(t *testing.T) {
	t.Setenv(EnvEnabled, "false")
	t.Setenv(EnvBatchFrames, "500")
	cfg, enabled, warns := ConfigFromEnv()
	if enabled {
		t.Error("EHOME_BACKFILL_ENABLED=false 必须关闭回填")
	}
	if cfg.BatchFrames != 500 {
		t.Errorf("BatchFrames = %d, want 500", cfg.BatchFrames)
	}
	if len(warns) != 0 {
		t.Errorf("合法配置不该有告警: %v", warns)
	}

	t.Setenv(EnvBatchFrames, "abc")
	cfg2, _, warns2 := ConfigFromEnv()
	if cfg2.BatchFrames != DefaultBatchFrames {
		t.Errorf("非法值必须回落到默认 %d，得到 %d", DefaultBatchFrames, cfg2.BatchFrames)
	}
	if len(warns2) == 0 {
		t.Error("非法值必须留一条告警（否则运维设错了都不知道）")
	}
}

// placeholderRemoved 保证 fmt 等 import 被用到（本文件有多条 t.Fatalf 用 %v）。
var _ = fmt.Sprintf

// TestBackfillNeverDeletesSourceData 是**源码级门禁**：回填引擎绝不能删老数据。
//
// ============ 为什么需要这条 ============
//
// 契约 §7 裁决 2 明确：「先迁移 → 验证 1 周 → 再 DROP」，且保留期现在是
// 永久保留（task-13 的症结）⇒ **删除动作必须永远由人触发**。
//
// 最危险的失败形态是：某人为了「省空间」在回填完成后顺手加一句
//
//	DROP TABLE unified_data_202610   // 或 DELETE FROM unified_data WHERE ...
//
// 那一刻代码还是绿的（删的是「已经迁移过的」数据），但它直接违背用户
// 「所有数据长期保留」的要求，而且**不可恢复**。
//
// 源码扫描而不是行为测试：行为测试只能覆盖跑到的路径，而这类语句可能加在
// 某个分支里（比如「只在 PG 上」「只在某月之后」），行为用例容易漏。
//
// 扫描范围：本包全部非测试 .go 文件。
func TestBackfillNeverDeletesSourceData(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	// 允许的 SQL 动词：本包只该有 SELECT / INSERT / UPDATE（推进 watermark）。
	//
	// ⚠ 用**词边界正则**而不是 strings.Contains：本包有个函数叫 truncateAtCleanBoundary
	// （截断批边界，与删数据毫无关系），朴素 Contains("TRUNCATE") 会把它判成违规 ——
	// 那是假红，而假红比没有门禁更糟（维护者会去改名迎合门禁，或者直接注释掉门禁）。
	forbidden := []*regexp.Regexp{
		regexp.MustCompile(`\bDROP\s+TABLE\b`),
		regexp.MustCompile(`\bDROP\s+PARTITION\b`),
		regexp.MustCompile(`\bDROP\s+INDEX\b`),
		regexp.MustCompile(`\bDELETE\s+FROM\b`),
		regexp.MustCompile(`\bTRUNCATE\s+(TABLE\s+)?\w+`),
	}
	var seen int
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, rerr := os.ReadFile(f)
		if rerr != nil {
			t.Fatalf("read %s: %v", f, rerr)
		}
		seen++
		upper := strings.ToUpper(string(raw))
		for _, re := range forbidden {
			if m := re.FindString(upper); m != "" {
				t.Errorf("%s 含有 %q —— 回填引擎绝不能删老数据（契约 §7 裁决 2：DROP 必须由人触发）。"+
					"永久保留模式下删数据不可恢复，且直接违背用户需求。", f, m)
			}
		}
	}
	if seen == 0 {
		t.Fatal("没有扫到任何非测试源文件 —— 门禁失效（路径变了？）")
	}
}

// TestProgressExposesRetentionPermanent 守回填进度里的永久保留标记。
//
// task-13 把「保留期 0 = 永久保留」落地后，回填完成后是否能删老分区完全取决于
// 这个配置。运维必须能在进度里看到它，否则会在永久保留模式下误删数据。
func TestProgressExposesRetentionPermanent(t *testing.T) {
	db := openBackfillTestDB(t)
	r := newTestRunner(t, db, Config{})
	defer datalifecycle.SetSystemRetentionDays(datalifecycle.SystemRetentionDays())

	datalifecycle.SetSystemRetentionDays(0) // 永久保留
	if got := r.Progress().RetentionPermanent; !got {
		t.Errorf("保留期=0 时 RetentionPermanent = false, want true（必须让运维看到「现在是永久保留」）")
	}

	datalifecycle.SetSystemRetentionDays(45)
	if got := r.Progress().RetentionPermanent; got {
		t.Errorf("保留期=45 时 RetentionPermanent = true, want false")
	}
}
