package api

// L3 读路径分派层的单测（SQLite，不需要 PG）。
//
// 核心承诺（方案 §5.3 + 本仓「关掉时行为一分不改」的硬约束）：
//   1. 开关关闭 ⇒ shouldUseRollup 恒 false（旧路径完全不变）；
//   2. 打开后也只有 **avg + 长窗 + 有效设备** 才走 rollup；change 模式绝不走
//      （rollup 表达不了翻转序列，用 avg/min/max/last 重建 0→1 跃变会算错）；
//   3. rollup 表空/坏/不存在 ⇒ **回落**旧路径（fail-open），绝不返回空点集
//      （空图比慢更糟）；
//   4. 命中时点集二次降采样到 <= maxPoints（否则 30 天 43,200 点会把
//      "更快"变成"100 倍数据"）。

import (
	"context"
	"os"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"ehome/backend/internal/datalifecycle"
)

// newRollupReadDB 建一个只含 telemetry_rollup_1m 的 SQLite 库。
func newRollupReadDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.Exec(`CREATE TABLE telemetry_rollup_1m (
		device_id integer NOT NULL,
		bucket    TIMESTAMP NOT NULL,
		payload   BLOB NOT NULL,
		PRIMARY KEY (device_id, bucket)
	)`).Error; err != nil {
		t.Fatalf("create telemetry_rollup_1m: %v", err)
	}
	// task-22: 覆盖元数据表。读路径现在**必须**先验证覆盖才用 rollup ——
	// 没有这张表时 rollupHistoryPoints 会因为"查不到元数据"而回落。
	if err := datalifecycle.EnsureRollupCoverageTable(db); err != nil {
		t.Fatalf("create telemetry_rollup_coverage: %v", err)
	}
	return db
}

// seedRollupMinutes 往 rollup 表写 n 分钟的 rsoc 统计量。
func seedRollupMinutes(t *testing.T, db *gorm.DB, deviceID uint, start time.Time, n int) {
	t.Helper()
	// task-22: 同时写覆盖元数据（否则读路径会判定"未回填"而回落）。
	// 按**实际写出的分钟数**逐天统计桶数 —— 与生产回填同口径，不是按 1440 硬算。
	perDay := map[string]int{}
	for i := 0; i < n; i++ {
		d := datalifecycle.RollupCoverageDay(start.Add(time.Duration(i) * time.Minute))
		perDay[d]++
	}
	for day, cnt := range perDay {
		if err := datalifecycle.MarkRollupCoverage(db, deviceID, day, cnt, datalifecycle.RollupCoverageComplete); err != nil {
			t.Fatalf("mark coverage %s: %v", day, err)
		}
	}
	for i := 0; i < n; i++ {
		payload, err := datalifecycle.EncodeRollupPayloadV1(map[string]*datalifecycle.RollupFieldStat{
			"rsoc": {Avg: float64(50 + i), Min: float64(50 + i), Max: float64(50 + i), Last: float64(50 + i), Count: 60},
		})
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if err := db.Exec("INSERT INTO telemetry_rollup_1m (device_id, bucket, payload) VALUES (?, ?, ?)",
			deviceID, start.Add(time.Duration(i)*time.Minute), payload).Error; err != nil {
			t.Fatalf("seed rollup row %d: %v", i, err)
		}
	}
}

func TestShouldUseRollup(t *testing.T) {
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	short := start.Add(1 * time.Hour)
	long := start.Add(24 * time.Hour)

	cases := []struct {
		name    string
		enabled bool
		mode    AggMode
		start   time.Time
		end     time.Time
		device  uint
		want    bool
	}{
		{"开关关闭 ⇒ 恒 false（旧路径不变）", false, AggModeAvg, start, long, 14, false},
		{"长窗 avg ⇒ 走 rollup", true, AggModeAvg, start, long, 14, true},
		{"短窗 avg ⇒ 不走（会把 1h 图从 3600 点变 60 点）", true, AggModeAvg, start, short, 14, false},
		{"change 模式 ⇒ 绝不走（翻转序列无法从 avg/min/max/last 重建）", true, AggModeChange, start, long, 14, false},
		{"raw 模式 ⇒ 不走", true, AggModeRaw, start, long, 14, false},
		{"设备维度为 0 ⇒ 不走", true, AggModeAvg, start, long, 0, false},
		{"空/倒置窗口 ⇒ 不走", true, AggModeAvg, long, start, 14, false},
		{"窗口正好等于阈值 ⇒ 不走（判据是严格大于）", true, AggModeAvg, start, start.Add(RollupShortWindow()), 14, false},
	}
	for _, c := range cases {
		if got := shouldUseRollup(c.enabled, c.mode, c.start, c.end, c.device); got != c.want {
			t.Errorf("%s: shouldUseRollup = %v, want %v", c.name, got, c.want)
		}
	}
}

// 默认值契约：环境变量未设置时，包级开关必须是 false。
//
// 为什么用 readBoolEnvDefault 而不是直接断言包级变量: 包级变量在 import 时就已
// 按当时的环境算好，若本次 test binary 带 EHOME_TELEMETRY_ROLLUP_READ=1 跑
// （例如灰度验证），断言包级变量会把环境问题当成代码缺陷。这里直接验"未设置 ⇒ 关"。
func TestRollupReadDefaultIsOff(t *testing.T) {
	if _, ok := os.LookupEnv(rollupReadEnv); ok {
		t.Skipf("本次运行显式设置了 %s，默认值契约改由 readBoolEnvDefault 的单测覆盖", rollupReadEnv)
	}
	if readBoolEnvDefault(rollupReadEnv, false) {
		t.Fatal("环境变量未设置时 rollup 读路径必须默认关闭")
	}
	if RollupReadEnabled() {
		t.Fatal("环境变量未设置时包级开关必须为 false")
	}
}

func TestReadBoolEnvDefaultParsing(t *testing.T) {
	t.Setenv(rollupReadEnv, "true")
	if !readBoolEnvDefault(rollupReadEnv, false) {
		t.Error(`"true" 必须解析为 true`)
	}
	t.Setenv(rollupReadEnv, "0")
	if readBoolEnvDefault(rollupReadEnv, true) {
		t.Error(`"0" 必须解析为 false（覆盖默认 true）`)
	}
	t.Setenv(rollupReadEnv, "garbage")
	if !readBoolEnvDefault(rollupReadEnv, true) {
		t.Error("非法值必须回落默认，而不是猜")
	}
}

func TestRollupHistoryPointsFallsBackWhenEmpty(t *testing.T) {
	db := newRollupReadDB(t)
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	points, handled, err := rollupHistoryPoints(context.Background(), db, historyPointQuery{
		EdgeDeviceID: 14,
		Category:     "rsoc",
		Start:        start,
		End:          start.Add(24 * time.Hour),
		MaxPoints:    500,
		Mode:         AggModeAvg,
	})
	if err != nil {
		t.Fatalf("空表不应返回错误（应回落）: %v", err)
	}
	if handled || points != nil {
		t.Fatalf("空表必须 handled=false（回落原路径），got handled=%v points=%v", handled, points)
	}
}

func TestRollupHistoryPointsServesLongWindow(t *testing.T) {
	db := newRollupReadDB(t)
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	seedRollupMinutes(t, db, 14, start, 90)

	points, handled, err := rollupHistoryPoints(context.Background(), db, historyPointQuery{
		EdgeDeviceID: 14,
		Category:     "rsoc",
		Start:        start,
		End:          start.Add(2 * time.Hour),
		MaxPoints:    500,
		Mode:         AggModeAvg,
	})
	if err != nil {
		t.Fatalf("rollupHistoryPoints: %v", err)
	}
	if !handled {
		t.Fatal("有数据时必须 handled=true")
	}
	if len(points) != 90 {
		t.Fatalf("点集 = %d, want 90（每分钟一个点，未超 maxPoints 不降采样）", len(points))
	}
	if points[0].Value != 50 || points[89].Value != 139 {
		t.Errorf("首尾值 = %v/%v, want 50/139", points[0].Value, points[89].Value)
	}
	if !points[0].Timestamp.Equal(start) {
		t.Errorf("首点时间 = %v, want %v", points[0].Timestamp, start)
	}
}

// TestRollupHistoryPointsDownsamplesToMaxPoints 守「点数被夹在 maxPoints 附近」。
//
// task-22 变更：点的时间戳不再是**分钟起点**，而是与 D1 完全一致的**桶起点**
// （epoch 绝对对齐 + 裁掉早于 Start 的桶）。所以本用例不能再断言"首点等于 start"
// —— start 未必落在桶边界上。改为断言**区间与单调性**，那才是真实契约：
//
//	· 点数与 maxPoints 同量级（不把"更快"变成"100 倍数据"）；
//	· 所有点都在 [start, end) 内（不返回窗口外的数据）；
//	· 时间严格递增（前端画折线的前提）。
//
// 「首点等于 start」这条在 D1 路径上同样不成立（trimAvgPointsBeforeStart 会裁掉
// 首桶），所以旧断言是错的 —— 它把「分钟点」的实现细节当成了契约。
func TestRollupHistoryPointsDownsamplesToMaxPoints(t *testing.T) {
	db := newRollupReadDB(t)
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	seedRollupMinutes(t, db, 14, start, 600) // 600 分钟
	end := start.Add(11 * time.Hour)

	points, handled, err := rollupHistoryPoints(context.Background(), db, historyPointQuery{
		EdgeDeviceID: 14,
		Category:     "rsoc",
		Start:        start,
		End:          end,
		MaxPoints:    100,
		Mode:         AggModeAvg,
	})
	if err != nil || !handled {
		t.Fatalf("rollupHistoryPoints handled=%v err=%v", handled, err)
	}
	if len(points) == 0 {
		t.Fatal("覆盖完整且有数据时必须返回点集")
	}
	// 桶宽 W = ceil(39600/100) = 396s，分桶后约 100 个点；再经 downsample 兜底，
	// 上限给到 maxPoints+2（与 downsampleUnifiedData 同口径）。
	if len(points) > 102 {
		t.Fatalf("降采样后 %d 点, want <= 102", len(points))
	}
	if points[0].Timestamp.Before(start) {
		t.Errorf("首点 %v 早于窗口起点 %v —— 桶起点早于 Start 的必须裁掉", points[0].Timestamp, start)
	}
	if !points[len(points)-1].Timestamp.Before(end) {
		t.Errorf("末点 %v 不早于窗口终点 %v —— 不该返回窗口外的数据", points[len(points)-1].Timestamp, end)
	}
	for i := 1; i < len(points); i++ {
		if !points[i].Timestamp.After(points[i-1].Timestamp) {
			t.Fatalf("第 %d 点时间 %v 未严格递增（前一个 %v）", i, points[i].Timestamp, points[i-1].Timestamp)
		}
	}
}

func TestRollupHistoryPointsIgnoresCorruptRows(t *testing.T) {
	db := newRollupReadDB(t)
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	seedRollupMinutes(t, db, 14, start, 5)
	// 一条坏行（未知版本）夹在中间。
	if err := db.Exec("INSERT INTO telemetry_rollup_1m (device_id, bucket, payload) VALUES (?, ?, ?)",
		14, start.Add(10*time.Minute), []byte{0x7f, 0, 1}).Error; err != nil {
		t.Fatalf("seed corrupt row: %v", err)
	}
	points, handled, err := rollupHistoryPoints(context.Background(), db, historyPointQuery{
		EdgeDeviceID: 14,
		Category:     "rsoc",
		Start:        start,
		End:          start.Add(time.Hour),
		MaxPoints:    500,
		Mode:         AggModeAvg,
	})
	if err != nil || !handled {
		t.Fatalf("坏行不该让整条查询失败: handled=%v err=%v", handled, err)
	}
	if len(points) != 5 {
		t.Errorf("坏行应被跳过, 好行 5 个全返回; got %d", len(points))
	}
}

// 表不存在（未接线/未迁移）时必须回落，而不是 500 或空图。
func TestRollupHistoryPointsFallsBackWhenTableMissing(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	points, handled, rerr := rollupHistoryPoints(context.Background(), db, historyPointQuery{
		EdgeDeviceID: 14,
		Category:     "rsoc",
		Start:        start,
		End:          start.Add(24 * time.Hour),
		MaxPoints:    500,
		Mode:         AggModeAvg,
	})
	if rerr != nil {
		t.Fatalf("表不存在必须回落而不是报错: %v", rerr)
	}
	if handled || points != nil {
		t.Fatalf("表不存在必须 handled=false, got %v/%v", handled, points)
	}
}
