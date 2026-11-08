package api

// history_source_pg_test.go — 帧表读路径在真 PG 上的辅助件。
//
// ============ 原验收内容已迁走（2026-11-08）============
//
// 本文件原来装的是 task-18 的核心验收：同一批数据同时存在于 unified_data 与
// telemetry_frame，逐窗口逐模式**逐值对拍**（TestFrameSourceEqualsUnifiedData）。
//
// 硬切换后 unified_data 读路径删除，那条对拍的两个前提同时消失：
//   · 基准（queryHistoryPoints 的 SQL）被删
//   · 数据（unified_data）即将被 DROP
//
// 替代物：testdata/oracle_20261105.json（**冻结的生产 SQL 输出**）+
// history_oracle_fixture_test.go 的 TestFrameSourceMatchesOracleFixture_*。
//
// ⚠ 为什么固化成数据而不是留在本文件里：活对拍依赖 unified_data 存在，那张表
// DROP 之后用例只能 skip —— 而 skip 在 CI 里是绿的。一个会静默变成「总是通过」
// 的证明，比没有证明更危险。
//
// 本文件保留的只有一个 helper：seedFrameFromUnifiedData（用生产编码器把行编成帧），
// 它仍被 rollup_tail_parity_pg_test.go 使用。

import (
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
	"ehome/backend/internal/telemetry"
	"ehome/backend/testutil"
)

// seedFrameFromUnifiedData 用**生产同一个编码器**把 unified_data 的行编码成帧。
//
// 为什么不用 backfill 包：任务明确「不要动 backfill 包」（生产回填在跑）。这里
// 直接调 telemetry.NewEncoder + Store.PersistFrames —— 与回填用的是同一条编码
// 链，因此帧表内容与线上回填产物同形。
func seedFrameFromUnifiedData(t *testing.T, db *gorm.DB, deviceID uint, dict telemetry.SlotDictionary) {
	t.Helper()
	var rows []struct {
		TS         time.Time
		SensorName string
		Value      float64
	}
	if err := db.Model(&models.UnifiedData{}).
		Select("timestamp AS ts, sensor_name, value").
		Where("device_id = ?", deviceID).
		Order("timestamp ASC, sensor_name ASC").Scan(&rows).Error; err != nil {
		t.Fatalf("load unified rows: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("没有 unified_data 行可编码")
	}

	// 按 (ts) 分组 = 一帧（与回填的分帧口径一致）。
	enc := telemetry.NewEncoder(telemetry.EncoderConfig{KeyFrameInterval: 60 * time.Second}, dict)
	frames := make([]models.TelemetryFrame, 0, len(rows)/8+1)
	i := 0
	for i < len(rows) {
		ts := rows[i].TS
		var readings []telemetry.Reading
		for i < len(rows) && rows[i].TS.Equal(ts) {
			readings = append(readings, telemetry.Reading{Name: rows[i].SensorName, Value: rows[i].Value})
			i++
		}
		frame, err := enc.EncodeFrame(deviceID, ts.UTC(), 0, readings)
		if err != nil {
			t.Fatalf("encode frame %s: %v", ts.Format(time.RFC3339Nano), err)
		}
		frames = append(frames, models.TelemetryFrame{
			DeviceID:  frame.DeviceID,
			TS:        frame.TS,
			Seq:       frame.Seq,
			FrameKind: frame.FrameKind,
			SlotID:    frame.SlotID,
			Payload:   frame.Payload,
		})
	}
	if err := db.Session(&gorm.Session{}).CreateInBatches(frames, 500).Error; err != nil {
		t.Fatalf("persist frames: %v", err)
	}
	t.Logf("编码帧数 = %d（来自 %d 行）", len(frames), len(rows))
}

// requirePostgresAPI 跳过非 PG 环境。
func requirePostgresAPI(t *testing.T) {
	t.Helper()
	if !testutil.IsPostgres() {
		t.Skip("需要 PostgreSQL（EHOME_TEST_DB=postgres）；帧表读路径的分区/时间精度行为只在 PG 上可信")
	}
}

// TestFrameSourceEqualsUnifiedData / assertSourcesEqual / head3 已删除（2026-11-08）。
//
// 它们做的是**活对拍**：帧表解码结果 vs queryHistoryPoints（生产 SQL 路径）逐点相等。
// 那个基准（SQL 路径）随 unified_data 一起退场，所以活对拍不可能继续存在。
//
// 替代物：testdata/oracle_20261105.json（冻结的生产 SQL 输出）+
// history_oracle_fixture_test.go 的 TestFrameSourceMatchesOracleFixture_*。
//
// ⚠ 为什么固化成数据而不是「搬代码到 _test.go」：活对拍依赖 unified_data 存在，
//   那张表 DROP 之后用例只能 skip —— 而 skip 在 CI 里是绿的。
//   一个会静默变成「总是通过」的证明，比没有证明更危险。
