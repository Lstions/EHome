//go:build integration

package api

// PG 集成测试 (契约 §1.5): 在真实 PostgreSQL 上执行 history_points.go 的
// 聚合 SQL —— 这是唯一能证明 to_timestamp/floor/lag/IS DISTINCT FROM 与
// 保形去重子查询在 PG 上真的可执行、且语义正确的证据 (SQLite 单测只能覆盖
// 纯函数与方言分支, 不能执行 PG 方言 SQL)。
//
// 运行:
//
//	EHOME_TEST_DB=postgres EHOME_DB_HOST=127.0.0.1 EHOME_DB_PORT=5432 \
//	EHOME_DB_USER=ehome EHOME_DB_PASSWORD=*** EHOME_DB_NAME=ehome_test \
//	go test -tags=integration -count=1 ./internal/api/ -run Postgres -v
//
// 未设 EHOME_TEST_DB=postgres 时 requirePostgresAPI(t) 显式 skip (不伪装成
// "跑过了")。库隔离: testutil.OpenTestDB 在目标库内建随机 schema
// test_<pid>_<nano>, 结束时 DROP SCHEMA CASCADE —— 对落脚库零残留写入。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/models"
	"ehome/backend/testutil"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// requirePostgresAPI skips unless the test DB is PostgreSQL.
func requirePostgresAPI(t *testing.T) {
	t.Helper()
	if !testutil.IsPostgres() {
		t.Skip("requires PostgreSQL: EHOME_TEST_DB=postgres + -tags=integration (make test-integration)")
	}
}

// seedPostgresHistoryDevice creates one logical device plus its single
// instance and returns (instanceID, logicalID, startTime). Query semantics:
// instance → logical scope (§六), exactly like the endpoint does.
func seedPostgresHistoryDevice(t *testing.T, db *gorm.DB) (uint, uint, time.Time) {
	t.Helper()
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	ld := models.LogicalDevice{IdentityKey: "bms-pg-hist:0x76", Name: "pg-hist", DeviceType: "bms"}
	if err := db.Create(&ld).Error; err != nil {
		t.Fatalf("create logical device: %v", err)
	}
	if err := db.Create(&models.Node{NodeID: "N-PGH", Name: "pgh", Status: "online"}).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := db.Create(&models.Channel{NodeID: "N-PGH", HardwareType: "I2C", BusType: "I2C", Enabled: true}).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	dev := models.EdgeDevice{Name: "pg-hist", Type: "bms", NodeID: "N-PGH", ChannelID: 1,
		HardwareID: "0x76", LogicalDeviceID: &ld.ID}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatalf("create edge device: %v", err)
	}
	return dev.ID, ld.ID, start
}

// historyPointRouter wires exactly the production data routes onto a PG test DB.
func historyPointRouter(t *testing.T, db *gorm.DB) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerDataRoutes(r.Group("/api/v1"), db)
	return r
}

// historyQueryFor resolves the §六 data scope of devID, mirroring what the
// endpoint passes into queryHistoryPoints (rendered as UNION ALL branches).
func historyQueryFor(t *testing.T, db *gorm.DB, devID uint) *datalifecycle.DataQueryScope {
	t.Helper()
	qs, err := datalifecycle.ResolveDataQueryScope(db, devID)
	if err != nil {
		t.Fatalf("resolve query scope: %v", err)
	}
	return qs
}

// ==================== avg 模式: 点数上限 + 桶均值 ====================

func TestHistoryPointsPostgres_AvgBuckets(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	if db.Dialector.Name() != "postgres" {
		t.Fatalf("dialector = %q, want postgres", db.Dialector.Name())
	}
	devID, ldID, start := seedPostgresHistoryDevice(t, db)

	// 契约 §1.5: 1Hz × 16 类 × 2h。
	const categories = 16
	const pointsPerCategory = 7200 // 2h at 1 Hz
	rows := make([]models.UnifiedData, 0, categories*pointsPerCategory)
	for c := 0; c < categories; c++ {
		name := fmt.Sprintf("metric_%02d", c)
		for i := 0; i < pointsPerCategory; i++ {
			rows = append(rows, models.UnifiedData{
				DeviceID:        devID,
				LogicalDeviceID: &ldID,
				SensorName:      name,
				Value:           float64(c)*1e6 + float64(i),
				Timestamp:       start.Add(time.Duration(i) * time.Second),
			})
		}
	}
	if err := db.CreateInBatches(rows, 2000).Error; err != nil {
		t.Fatalf("seed rows: %v", err)
	}
	var seeded int64
	if err := db.Model(&models.UnifiedData{}).Where("sensor_name LIKE ?", "metric_%").Count(&seeded).Error; err != nil {
		t.Fatalf("count seeded: %v", err)
	}
	if want := int64(categories * pointsPerCategory); seeded != want {
		t.Fatalf("seeded rows = %d, want %d", seeded, want)
	}

	qs := historyQueryFor(t, db, devID)
	end := start.Add(2 * time.Hour)
	points, err := queryHistoryPoints(context.Background(), db, historyPointQuery{
		Scope: qs, DedupNeeded: qs.DedupNeeded,
		Category: "metric_00", Start: start, End: end,
		MaxPoints: 500, Mode: AggModeAvg,
	})
	if err != nil {
		t.Fatalf("queryHistoryPoints(avg): %v", err)
	}

	// (1) 点数上限: <= maxPoints+1。
	if len(points) > 501 {
		t.Fatalf("avg returned %d points, want <= maxPoints+1 (501)", len(points))
	}
	// (2) 桶宽 = ceil(7200/500) = 15s; 桶均值 = 桶内 i 的平均值 (值即 i)。
	width := bucketWidthSeconds(pointsPerCategory, 500)
	if width != 15 {
		t.Fatalf("bucketWidthSeconds(7200, 500) = %d, want 15", width)
	}
	sums := make(map[int64]float64)
	counts := make(map[int64]int)
	for i := 0; i < pointsPerCategory; i++ {
		ts := start.Add(time.Duration(i) * time.Second).Unix()
		bucket := ts / width * width
		sums[bucket] += float64(i)
		counts[bucket]++
	}
	if len(points) != len(sums) {
		t.Fatalf("avg points = %d, want one per non-empty bucket (%d)", len(points), len(sums))
	}
	for _, p := range points {
		bucket := p.Timestamp.Unix()
		wantMean, ok := sums[bucket]
		if !ok {
			t.Fatalf("point timestamp %s is not an expected bucket start", p.Timestamp)
		}
		if p.Timestamp.Unix()%width != 0 {
			t.Errorf("bucket timestamp %s is not aligned to width %ds", p.Timestamp, width)
		}
		want := wantMean / float64(counts[bucket])
		if p.Value != want {
			t.Errorf("bucket %d avg = %v, want %v", bucket, p.Value, want)
		}
	}
	t.Logf("avg: seeded=%d categories=%d points=%d width=%ds", seeded, categories, len(points), width)
}

// ==================== change 模式: 变更点序列 (首点 + 双向跳变) ====================

func TestHistoryPointsPostgres_ChangeSequence(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	devID, ldID, start := seedPostgresHistoryDevice(t, db)

	// 0 0 1 1 0 0 0 1 -> 变更点: t0(首点), t2(0->1), t4(1->0), t7(0->1)。
	seq := []float64{0, 0, 1, 1, 0, 0, 0, 1}
	for i, v := range seq {
		row := models.UnifiedData{
			DeviceID: devID, LogicalDeviceID: &ldID, SensorName: "protection_status",
			Value: v, Timestamp: start.Add(time.Duration(i) * time.Second),
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("seed protection row %d: %v", i, err)
		}
	}

	qs := historyQueryFor(t, db, devID)
	points, err := queryHistoryPoints(context.Background(), db, historyPointQuery{
		Scope: qs, DedupNeeded: qs.DedupNeeded,
		Category: "protection_status", Start: start, End: start.Add(time.Duration(len(seq)) * time.Second),
		MaxPoints: 500, Mode: AggModeChange,
	})
	if err != nil {
		t.Fatalf("queryHistoryPoints(change): %v", err)
	}

	wantOffsets := []int{0, 2, 4, 7}
	wantValues := []float64{0, 1, 0, 1}
	if len(points) != len(wantOffsets) {
		t.Fatalf("change points = %d, want %d (first point + every transition incl 1->0): %+v",
			len(points), len(wantOffsets), points)
	}
	for i, p := range points {
		wantTS := start.Add(time.Duration(wantOffsets[i]) * time.Second)
		if !p.Timestamp.Equal(wantTS) {
			t.Errorf("change point %d timestamp = %s, want %s", i, p.Timestamp, wantTS)
		}
		if p.Value != wantValues[i] {
			t.Errorf("change point %d value = %v, want %v", i, p.Value, wantValues[i])
		}
	}
	t.Logf("change: seq=%v changes=%d", seq, len(points))
}

// ==================== 端点: agg=auto/raw/非法值 + 轻量点形状 ====================

func TestHistoricalEndpointsPostgres_Aggregation(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	devID, ldID, start := seedPostgresHistoryDevice(t, db)

	// 30min @ 1Hz 两个连续量 + 每 10s 一个 protection_status。
	const n = 1800
	for _, cat := range []string{"cell_voltage_1", "cell_voltage_2"} {
		rows := make([]models.UnifiedData, 0, n)
		for i := 0; i < n; i++ {
			rows = append(rows, models.UnifiedData{
				DeviceID: devID, LogicalDeviceID: &ldID, SensorName: cat,
				Value: 3.0 + float64(i)*0.001, Timestamp: start.Add(time.Duration(i) * time.Second),
			})
		}
		if err := db.CreateInBatches(rows, 1000).Error; err != nil {
			t.Fatalf("seed %s: %v", cat, err)
		}
	}
	// protection 期望的变更点序列: 首点 + 每次值变化 (含 1->0)。
	protectionValues := make([]float64, 0, n/10+1)
	protectionRows := make([]models.UnifiedData, 0, n/10+1)
	wantChanges := make([]HistoryPoint, 0, n/10+1)
	for i := 0; i <= n/10; i++ {
		v := 0.0
		if i%4 == 1 || i%4 == 2 {
			v = 1.0
		}
		ts := start.Add(time.Duration(i*10) * time.Second)
		protectionValues = append(protectionValues, v)
		protectionRows = append(protectionRows, models.UnifiedData{
			DeviceID: devID, LogicalDeviceID: &ldID, SensorName: "protection_status",
			Value: v, Timestamp: ts,
		})
		if len(protectionValues) == 1 || v != protectionValues[len(protectionValues)-2] {
			wantChanges = append(wantChanges, HistoryPoint{Timestamp: ts, Value: v})
		}
	}
	if err := db.CreateInBatches(protectionRows, 500).Error; err != nil {
		t.Fatalf("seed protection: %v", err)
	}

	r := historyPointRouter(t, db)
	end := start.Add(n * time.Second)
	get := func(t *testing.T, path string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s -> %d: %s", path, w.Code, w.Body.String())
		}
		return w
	}
	batchURL := func(extra string) string {
		return fmt.Sprintf("/api/v1/unified-data/historical-batch?device_pk=%d&categories=cell_voltage_1,protection_status&start_time=%s&end_time=%s&max_points=100%s",
			devID, url.QueryEscape(start.Format(time.RFC3339)), url.QueryEscape(end.Format(time.RFC3339)), extra)
	}

	// (1) agg=auto: 连续量 -> avg (点数 <= 101, 轻量形状), protection -> change。
	w := get(t, batchURL(""))
	type batchEntry struct {
		Category string          `json:"category"`
		Data     json.RawMessage `json:"data"`
	}
	batch := envelopeData[[]batchEntry](t, w.Body.Bytes())
	if len(batch) != 2 {
		t.Fatalf("batch returned %d categories, want 2: %s", len(batch), w.Body.String())
	}
	for _, entry := range batch {
		switch entry.Category {
		case "cell_voltage_1":
			var pts []HistoryPoint
			if err := json.Unmarshal(entry.Data, &pts); err != nil {
				t.Fatalf("decode avg points: %v (%s)", err, entry.Data)
			}
			if len(pts) == 0 || len(pts) > 101 {
				t.Errorf("avg points = %d, want 1..101 (maxPoints+1)", len(pts))
			}
			var raw []map[string]json.RawMessage
			if err := json.Unmarshal(entry.Data, &raw); err != nil {
				t.Fatalf("decode avg raw: %v", err)
			}
			if len(raw) > 0 {
				if _, ok := raw[0]["created_at"]; ok {
					t.Errorf("aggregated points must be lean ({timestamp,value}), got created_at")
				}
				if _, ok := raw[0]["timestamp"]; !ok {
					t.Errorf("aggregated point missing timestamp: %s", entry.Data)
				}
			}
		case "protection_status":
			var pts []HistoryPoint
			if err := json.Unmarshal(entry.Data, &pts); err != nil {
				t.Fatalf("decode change points: %v (%s)", err, entry.Data)
			}
			if len(pts) != len(wantChanges) {
				t.Fatalf("protection change points = %d, want %d", len(pts), len(wantChanges))
			}
			for i := range pts {
				if !pts[i].Timestamp.Equal(wantChanges[i].Timestamp) || pts[i].Value != wantChanges[i].Value {
					t.Fatalf("change point %d = %+v, want %+v", i, pts[i], wantChanges[i])
				}
			}
		default:
			t.Errorf("unexpected category %q", entry.Category)
		}
	}

	// (2) agg=raw: 旧全量拉取 + Go 侧抽样, 保形仍是 models.UnifiedData 全字段。
	w = get(t, batchURL("&agg=raw"))
	batch = envelopeData[[]batchEntry](t, w.Body.Bytes())
	for _, entry := range batch {
		if entry.Category != "cell_voltage_1" {
			continue
		}
		var raw []map[string]json.RawMessage
		if err := json.Unmarshal(entry.Data, &raw); err != nil {
			t.Fatalf("decode raw rows: %v", err)
		}
		if len(raw) == 0 {
			t.Fatal("agg=raw returned no rows")
		}
		for _, key := range []string{"id", "sensor_name", "created_at"} {
			if _, ok := raw[0][key]; !ok {
				t.Errorf("agg=raw must keep the legacy full-row shape, missing %q", key)
			}
		}
		if len(raw) != 101 { // 1800 rows, maxPoints=100 -> 首尾 + 每 18 行 1 点
			t.Errorf("agg=raw downsampled rows = %d, want 101", len(raw))
		}
	}

	// (3) 非法 agg 回落 auto (不 400), 仍有分桶结果。
	w = get(t, batchURL("&agg=not-a-mode"))
	batch = envelopeData[[]batchEntry](t, w.Body.Bytes())
	for _, entry := range batch {
		if entry.Category != "cell_voltage_1" {
			continue
		}
		var pts []HistoryPoint
		if err := json.Unmarshal(entry.Data, &pts); err != nil {
			t.Fatalf("decode invalid-agg points: %v", err)
		}
		if len(pts) == 0 || len(pts) > 101 {
			t.Errorf("invalid agg must fall back to auto/avg (<=101 points), got %d", len(pts))
		}
	}

	// (4) 单 category 端点 agg=avg / agg=change。
	histURL := func(extra string) string {
		return fmt.Sprintf("/api/v1/unified-data/historical?device_pk=%d&category=cell_voltage_1&start_time=%s&end_time=%s&max_points=100%s",
			devID, url.QueryEscape(start.Format(time.RFC3339)), url.QueryEscape(end.Format(time.RFC3339)), extra)
	}
	w = get(t, histURL("&agg=avg"))
	avgPts := envelopeData[[]HistoryPoint](t, w.Body.Bytes())
	if len(avgPts) == 0 || len(avgPts) > 101 {
		t.Errorf("historical agg=avg points = %d, want 1..101", len(avgPts))
	}
	changeURL := fmt.Sprintf("/api/v1/unified-data/historical?device_pk=%d&category=protection_status&start_time=%s&end_time=%s&max_points=100&agg=change",
		devID, url.QueryEscape(start.Format(time.RFC3339)), url.QueryEscape(end.Format(time.RFC3339)))
	w = get(t, changeURL)
	changePts := envelopeData[[]HistoryPoint](t, w.Body.Bytes())
	if len(changePts) != len(wantChanges) {
		t.Fatalf("historical agg=change points = %d, want %d", len(changePts), len(wantChanges))
	}
}

// ==================== 保形去重必须先于聚合 ====================

// TestHistoryPointsPostgres_DedupBeforeAggregation pins 契约 §1.3: 两台物理
// 设备同一 (sensor_name, timestamp) 的两行必须先按 MAX(id) 去重再 avg ——
// 直接聚合会把两台设备的读数平均掉 (无物理意义) 且值可被观察到不同。
func TestHistoryPointsPostgres_DedupBeforeAggregation(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)

	done := models.MergeStatusDone
	target := models.LogicalDevice{IdentityKey: "bms-pg-dedup:0x76", Name: "dedup-target", DeviceType: "bms"}
	if err := db.Create(&target).Error; err != nil {
		t.Fatalf("create target logical device: %v", err)
	}
	source := models.LogicalDevice{IdentityKey: "bms-pg-dedup:0x77", Name: "dedup-source", DeviceType: "bms",
		MergedInto: &target.ID, MergeStatus: &done}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create source logical device: %v", err)
	}
	if err := db.Create(&models.Node{NodeID: "N-PGD", Name: "pgd", Status: "online"}).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := db.Create(&models.Channel{NodeID: "N-PGD", HardwareType: "I2C", BusType: "I2C", Enabled: true}).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	dev1 := models.EdgeDevice{Name: "dedup-living", Type: "bms", NodeID: "N-PGD", ChannelID: 1, LogicalDeviceID: &target.ID}
	dev2 := models.EdgeDevice{Name: "dedup-replaced", Type: "bms", NodeID: "N-PGD", ChannelID: 1, LogicalDeviceID: &source.ID}
	if err := db.Create(&dev1).Error; err != nil {
		t.Fatalf("create dev1: %v", err)
	}
	if err := db.Create(&dev2).Error; err != nil {
		t.Fatalf("create dev2: %v", err)
	}
	if err := db.Delete(&dev2).Error; err != nil { // 实例已删, 数据保留 (merge done 场景)
		t.Fatalf("soft delete dev2: %v", err)
	}

	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	// 每个 timestamp 两行: 迁移来的旧源行 (id 较小, 值 10/20) + 新写入行
	// (id 较大, 值 30/40)。保形去重保留 MAX(id) => 30/40。
	rows := []models.UnifiedData{
		{DeviceID: dev2.ID, LogicalDeviceID: &target.ID, SensorName: "soc", Value: 10, Timestamp: start},
		{DeviceID: dev1.ID, LogicalDeviceID: &target.ID, SensorName: "soc", Value: 30, Timestamp: start},
		{DeviceID: dev2.ID, LogicalDeviceID: &target.ID, SensorName: "soc", Value: 20, Timestamp: start.Add(time.Second)},
		{DeviceID: dev1.ID, LogicalDeviceID: &target.ID, SensorName: "soc", Value: 40, Timestamp: start.Add(time.Second)},
	}
	for i := range rows {
		if err := db.Create(&rows[i]).Error; err != nil {
			t.Fatalf("seed row %d: %v", i, err)
		}
	}

	qs, err := datalifecycle.ResolveDataQueryScope(db, dev1.ID)
	if err != nil {
		t.Fatalf("resolve query scope: %v", err)
	}
	if !qs.DedupNeeded {
		t.Fatal("precondition: qs.DedupNeeded must be true (target has a done incoming merge)")
	}
	// 窗口 2s / maxPoints=500 -> 桶宽 1s => t0、t0+1s 两个桶。
	points, err := queryHistoryPoints(context.Background(), db, historyPointQuery{
		Scope: qs, DedupNeeded: qs.DedupNeeded,
		Category: "soc", Start: start, End: start.Add(2 * time.Second),
		MaxPoints: 500, Mode: AggModeAvg,
	})
	if err != nil {
		t.Fatalf("queryHistoryPoints(avg, dedup): %v", err)
	}
	wantValues := []float64{30, 40} // 去重保留 MAX(id); 未去重会是 20 / 30
	if len(points) != len(wantValues) {
		t.Fatalf("deduped avg points = %d, want %d: %+v", len(points), len(wantValues), points)
	}
	for i, p := range points {
		if p.Value != wantValues[i] {
			t.Errorf("deduped bucket %d value = %v, want %v (de-dup must precede avg)", i, p.Value, wantValues[i])
		}
	}

	// 端点同样走 dedup 感知的 scope (证明 handler 把 qs.DedupNeeded 传下去了)。
	r := historyPointRouter(t, db)
	path := fmt.Sprintf("/api/v1/unified-data/historical-batch?device_pk=%d&categories=soc&start_time=%s&end_time=%s&max_points=500&agg=avg",
		dev1.ID, url.QueryEscape(start.Format(time.RFC3339)), url.QueryEscape(start.Add(2*time.Second).Format(time.RFC3339)))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET batch -> %d: %s", w.Code, w.Body.String())
	}
	type batchEntry struct {
		Category string         `json:"category"`
		Data     []HistoryPoint `json:"data"`
	}
	batch := envelopeData[[]batchEntry](t, w.Body.Bytes())
	if len(batch) != 1 || len(batch[0].Data) != len(wantValues) {
		t.Fatalf("endpoint deduped points = %+v, want %d points", batch, len(wantValues))
	}
	for i, p := range batch[0].Data {
		if p.Value != wantValues[i] {
			t.Errorf("endpoint deduped bucket %d value = %v, want %v", i, p.Value, wantValues[i])
		}
	}
}

// ==================== §1.6 回归门禁: UNION ALL 让索引真正被使用 ====================

// explainPlan runs EXPLAIN (COSTS OFF) on sql and returns the plan text lines.
func explainPlan(t *testing.T, db *gorm.DB, sqlText string, args ...interface{}) []string {
	t.Helper()
	var lines []string
	if err := db.Raw("EXPLAIN (COSTS OFF) "+sqlText, args...).Scan(&lines).Error; err != nil {
		t.Fatalf("EXPLAIN failed: %v\nSQL: %s", err, sqlText)
	}
	if len(lines) == 0 {
		t.Fatalf("EXPLAIN returned no plan lines\nSQL: %s", sqlText)
	}
	return lines
}

// requirePlanMentions fails unless the plan text mentions want.
func requirePlanMentions(t *testing.T, plan []string, want string) {
	t.Helper()
	for _, line := range plan {
		if strings.Contains(line, want) {
			return
		}
	}
	t.Fatalf("query plan does not use %q:\n  %s", want, strings.Join(plan, "\n  "))
}

// TestPostgresUnionPlanUsesCompositeIndexes 是本修复的关键回归门禁
// (契约 §1.6, Lead 2026-10-20 23:09 补充第 3 点):
//
//   - A 分支 (logical_device_id IN (...) + sensor_name + timestamp) 必须走
//     idx_unified_logical_sensor_ts (avg 与 change 两种模式);
//   - B 分支 (logical_device_id IS NULL AND device_id IN (...), 造了 NULL 行)
//     必须走部分索引 idx_unified_legacy_null_ts;
//   - latest-data A 分支走 idx_devdata_logical_created, B 分支走
//     idx_devdata_legacy_null_created。
//
// 回退成旧 OR scope 条件时本条必红 (verifier 在 22.4M 行 bench 上实测:
// 24h 单测点仍 Index Scan timestamp_idx 扫 2.5M 行, latest-data 仍 Seq Scan)。
func TestPostgresUnionPlanUsesCompositeIndexes(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	ctx := context.Background()
	if err := datalifecycle.EnsureLogicalDataIndexes(ctx, db); err != nil {
		t.Fatalf("ensure indexes: %v", err)
	}

	// 1 逻辑设备 + 1 实例 (scope 因此有 A/B 两个分支)。
	ld := models.LogicalDevice{IdentityKey: "bms-union-plan:0x76", Name: "union-plan", DeviceType: "bms"}
	if err := db.Create(&ld).Error; err != nil {
		t.Fatalf("create logical device: %v", err)
	}
	if err := db.Create(&models.Node{NodeID: "N-UP", Name: "up", Status: "online"}).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := db.Create(&models.Channel{NodeID: "N-UP", HardwareType: "I2C", BusType: "I2C", Enabled: true}).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	dev := models.EdgeDevice{Name: "union-plan", Type: "bms", NodeID: "N-UP", ChannelID: 1,
		HardwareID: "0x76", LogicalDeviceID: &ld.ID}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatalf("create edge device: %v", err)
	}

	// 帧序插入 16 类 × 2000s = 32000 行 (与生产 1Hz 采集同构: 同测点在堆内分散),
	// 另造 400 行 backfill 前 NULL-logical 旧行, 让 B 分支部分索引可被选中。
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	const sensors = 16
	const points = 2000
	rows := make([]models.UnifiedData, 0, sensors*points+400)
	for i := 0; i < points; i++ {
		ts := start.Add(time.Duration(i) * time.Second)
		for s := 0; s < sensors; s++ {
			rows = append(rows, models.UnifiedData{
				DeviceID: dev.ID, LogicalDeviceID: &ld.ID,
				SensorName: fmt.Sprintf("metric_%02d", s), Value: float64(i), Timestamp: ts,
			})
		}
		if i%5 == 0 { // 每 5s 一条 NULL-logical 旧行 (同 sensor/时间轴)
			rows = append(rows, models.UnifiedData{
				DeviceID: dev.ID, SensorName: "metric_00", Value: float64(i), Timestamp: ts,
			})
		}
	}
	if err := db.CreateInBatches(rows, 1000).Error; err != nil {
		t.Fatalf("seed unified_data: %v", err)
	}
	devRows := make([]models.DeviceData, 0, 2000)
	for i := 0; i < 2000; i++ {
		ts := start.Add(time.Duration(i) * time.Second)
		devRows = append(devRows, models.DeviceData{
			DeviceID: dev.ID, NodeID: "N-UP", DataJSON: `{"v":1}`,
			Timestamp: ts, CreatedAt: ts, LogicalDeviceID: &ld.ID,
		})
	}
	for i := 0; i < 200; i++ { // NULL-logical 旧行, 供 latest-data B 分支部分索引
		ts := start.Add(time.Duration(i*10) * time.Second)
		devRows = append(devRows, models.DeviceData{
			DeviceID: dev.ID, NodeID: "N-UP", DataJSON: `{"v":0}`,
			Timestamp: ts, CreatedAt: ts,
		})
	}
	if err := db.CreateInBatches(devRows, 500).Error; err != nil {
		t.Fatalf("seed device_data: %v", err)
	}
	for _, table := range []string{"unified_data", "device_data"} {
		if err := db.Exec("ANALYZE " + table).Error; err != nil {
			t.Fatalf("analyze %s: %v", table, err)
		}
	}

	qs, err := datalifecycle.ResolveDataQueryScope(db, dev.ID)
	if err != nil {
		t.Fatalf("resolve query scope: %v", err)
	}
	if len(qs.Scope.LogicalIDs) == 0 || len(qs.Scope.InstanceIDs) == 0 {
		t.Fatalf("precondition: scope must have both branches (logical=%v instance=%v)",
			qs.Scope.LogicalIDs, qs.Scope.InstanceIDs)
	}
	// 注: 本测试 schema 只有 ~3 万行且单一逻辑设备, PG 代价模型对 A 分支
	// (logical_device_id, sensor_name, timestamp) 与单列索引的取舍不稳定;
	// A 分支"走复合索引"的真实验收证据在 22.4M 行 ehome_bench 的 EXPLAIN
	// (.bench/after-v2.md, 单测点 24h 扫描 ≈86,400 行)。本测试锁定的是
	// 结构上可复现的两点: union 分支顺序/形态, 以及 B 分支必须走 legacy
	// 部分索引 (没有它 B 分支会退化为全窗口扫描)。
	end := start.Add(2 * time.Hour)

	// (1) avg 历史查询: B 分支 (NULL-logical) 必须走部分索引。
	avgSQL, avgArgs := historyAggregateSQL(historyPointQuery{
		Scope: qs, Category: "metric_00", Start: start, End: end,
		MaxPoints: 500, Mode: AggModeAvg,
	}, bucketWidthSeconds(historyWindowSeconds(start, end), 500))
	t.Logf("avg SQL:\n%s", avgSQL)
	avgPlan := explainPlan(t, db, avgSQL, avgArgs...)
	requirePlanMentions(t, avgPlan, datalifecycle.IndexUnifiedLegacyNullTS)
	// covering 索引 (INCLUDE value): avg 必须是 Index Only Scan (无 heap fetch),
	// 否则 planner 在 86,400 行量级仍会退回去扫 timestamp 索引 (Lead bench 实测)。
	requirePlanMentions(t, avgPlan, "Index Only Scan")

	// (2) change 历史查询: B 分支同样必须走部分索引 (不许全窗口扫描)。
	changeSQL, changeArgs := historyAggregateSQL(historyPointQuery{
		Scope: qs, Category: "metric_00", Start: start, End: end,
		MaxPoints: 500, Mode: AggModeChange,
	}, bucketWidthSeconds(historyWindowSeconds(start, end), 500))
	changePlan := explainPlan(t, db, changeSQL, changeArgs...)
	requirePlanMentions(t, changePlan, datalifecycle.IndexUnifiedLegacyNullTS)
	// change: 分支 ORDER BY -> Merge Append + Index Only Scan (无 external sort)。
	requirePlanMentions(t, changePlan, "Index Only Scan")

	// (3) latest-data 两段式: A 分支走 (logical_device_id, created_at DESC),
	//     B 分支走 legacy 部分索引。
	latestQueries := latestDeviceDataQueries(qs)
	if len(latestQueries) != 2 {
		t.Fatalf("latest-data queries = %d, want 2 (logical + NULL-logical fallback)", len(latestQueries))
	}
	latestAPlan := explainPlan(t, db, latestQueries[0].SQL, latestQueries[0].Args...)
	requirePlanMentions(t, latestAPlan, datalifecycle.IndexDevDataLogicalCreated)
	latestBPlan := explainPlan(t, db, latestQueries[1].SQL, latestQueries[1].Args...)
	requirePlanMentions(t, latestBPlan, datalifecycle.IndexDevDataLegacyNullCreat)
}
