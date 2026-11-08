//go:build integration

package api

// PG 集成测试 (契约 §1.5): 在真实 PostgreSQL 上执行 history_points.go 的
// 聚合 SQL —— 这是唯一能证明 to_timestamp/floor/lag/IS DISTINCT FROM 在 PG 上
// 真的可执行、且语义正确的证据 (SQLite 单测只能覆盖纯函数与方言分支, 不能执行
// PG 方言 SQL)。
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
//
// 2026-10-23 (逻辑设备概念层移除, .contract/logical-device-removal.md §5):
// 本文件原有一组「scope 双分支 UNION ALL / 保形去重 / latest-data 取较新」用例,
// 它们全部以 `datalifecycle.ResolveDataQueryScope` 与 `logical_device_id` 为
// 前提。这两个东西随概念层删除后, 那组用例没有对象 —— 已按「设备维度直查」这个
// 新形态重写:
//
//   - 聚合 SQL 的语义正确性 (avg 分桶 / change 变更点) 保持原样, 只把数据来源
//     从 scope 换成 EdgeDeviceID;
//   - 原「保形去重先于聚合」→ 改为 **设备隔离**: 一次查询只能看到目标设备自己的
//     行, 别的设备同 timestamp 的行一律不得混入 (这正是去重当初想掩盖的问题);
//   - 原「UNION 分支让复合索引生效」→ 改为 **设备列索引存在且被选中**;
//   - 原「latest-data A/B 分支取较新」→ 随 latestDeviceDataSQL 一起删除
//     (latest-data 端点改由 handler_edge_device.go 直查 device_id)。

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

// seedPostgresHistoryDevice creates one edge device and returns
// (deviceID, startTime). 查询维度就是这台设备本身 —— 逻辑设备层移除后,
// "设备的历史"不再需要任何解析步骤。
func seedPostgresHistoryDevice(t *testing.T, db *gorm.DB) (uint, time.Time) {
	t.Helper()
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	if err := db.Create(&models.Node{NodeID: "N-PGH", Name: "pgh", Status: "online"}).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := db.Create(&models.Channel{NodeID: "N-PGH", HardwareType: "I2C", BusType: "I2C", Enabled: true}).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	dev := models.EdgeDevice{Name: "pg-hist", Type: "bms", NodeID: "N-PGH", ChannelID: 1, HardwareID: "0x76"}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatalf("create edge device: %v", err)
	}
	return dev.ID, start
}

// historyPointRouter wires exactly the production data routes onto a PG test DB.
func historyPointRouter(t *testing.T, db *gorm.DB) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerDataRoutes(r.Group("/api/v1"), db)
	return r
}

// ==================== avg 模式: 点数上限 + 桶均值 ====================

func TestHistoryPointsPostgres_AvgBuckets(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	if db.Dialector.Name() != "postgres" {
		t.Fatalf("dialector = %q, want postgres", db.Dialector.Name())
	}
	devID, start := seedPostgresHistoryDevice(t, db)

	// 契约 §1.5: 1Hz × 16 类 × 2h。
	const categories = 16
	const pointsPerCategory = 7200 // 2h at 1 Hz
	rows := make([]models.UnifiedData, 0, categories*pointsPerCategory)
	for c := 0; c < categories; c++ {
		name := fmt.Sprintf("metric_%02d", c)
		for i := 0; i < pointsPerCategory; i++ {
			rows = append(rows, models.UnifiedData{
				DeviceID:   devID,
				SensorName: name,
				Value:      float64(c)*1e6 + float64(i),
				Timestamp:  start.Add(time.Duration(i) * time.Second),
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

	end := start.Add(2 * time.Hour)
	points, err := queryHistoryPoints(context.Background(), db, historyPointQuery{
		EdgeDeviceID: devID,
		Category:     "metric_00", Start: start, End: end,
		MaxPoints: 500, Mode: AggModeAvg,
	})
	if err != nil {
		t.Fatalf("queryHistoryPoints(avg): %v", err)
	}

	// (1) 点数上限: <= maxPoints+1。
	if len(points) > 501 {
		t.Fatalf("avg returned %d points, want <= maxPoints+1 (501)", len(points))
	}
	// (2) 桶宽 = ceil(7200/500) = 15s; 桶内【最后值】= 该桶内 i 的最大值。
	// D1 (2026-11-07): 旧断言是桶均值 sums/counts；L1 启用后行数不再代表时间密度，
	// 必须改成「桶内最后一行」。这里播种是 1 Hz 连续序列，所以桶内最后值 = 桶内最大 i。
	width := bucketWidthSeconds(pointsPerCategory, 500)
	if width != 15 {
		t.Fatalf("bucketWidthSeconds(7200, 500) = %d, want 15", width)
	}
	lastByBucket := make(map[int64]float64)
	for i := 0; i < pointsPerCategory; i++ {
		ts := start.Add(time.Duration(i) * time.Second).Unix()
		bucket := ts / width * width
		lastByBucket[bucket] = float64(i) // 按 i 递增插入 ⇒ 后写覆盖先写 = 桶内最后
	}
	if len(points) != len(lastByBucket) {
		t.Fatalf("avg points = %d, want one per non-empty bucket (%d)", len(points), len(lastByBucket))
	}
	for _, p := range points {
		bucket := p.Timestamp.Unix()
		wantLast, ok := lastByBucket[bucket]
		if !ok {
			t.Fatalf("point timestamp %s is not an expected bucket start", p.Timestamp)
		}
		if p.Timestamp.Unix()%width != 0 {
			t.Errorf("bucket timestamp %s is not aligned to width %ds", p.Timestamp, width)
		}
		if p.Value != wantLast {
			t.Errorf("bucket %d last-value = %v, want %v (D1: bucket-last-value, not bucket mean)",
				bucket, p.Value, wantLast)
		}
	}
	t.Logf("avg(bucket-last-value): seeded=%d categories=%d points=%d width=%ds", seeded, categories, len(points), width)
}

// ==================== change 模式: 变更点序列 (首点 + 双向跳变) ====================

func TestHistoryPointsPostgres_ChangeSequence(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	devID, start := seedPostgresHistoryDevice(t, db)

	// 0 0 1 1 0 0 0 1 -> 变更点: t0(首点), t2(0->1), t4(1->0), t7(0->1)。
	seq := []float64{0, 0, 1, 1, 0, 0, 0, 1}
	for i, v := range seq {
		row := models.UnifiedData{
			DeviceID: devID, SensorName: "protection_status",
			Value: v, Timestamp: start.Add(time.Duration(i) * time.Second),
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("seed protection row %d: %v", i, err)
		}
	}

	points, err := queryHistoryPoints(context.Background(), db, historyPointQuery{
		EdgeDeviceID: devID,
		Category:     "protection_status", Start: start, End: start.Add(time.Duration(len(seq)) * time.Second),
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
	devID, start := seedPostgresHistoryDevice(t, db)

	// 30min @ 1Hz 两个连续量 + 每 10s 一个 protection_status。
	const n = 1800
	for _, cat := range []string{"cell_voltage_1", "cell_voltage_2"} {
		rows := make([]models.UnifiedData, 0, n)
		for i := 0; i < n; i++ {
			rows = append(rows, models.UnifiedData{
				DeviceID: devID, SensorName: cat,
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
			DeviceID: devID, SensorName: "protection_status",
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

// ==================== 设备隔离 (原「保形去重先于聚合」的替代) ====================

// TestHistoryPointsPostgres_DeviceIsolation 取代原
// TestHistoryPointsPostgres_DedupBeforeAggregation。
//
// 原用例的前提是「两台物理设备在同一 (sensor_name, timestamp) 各写一行, 必须先按
// MAX(id) 去重再聚合」—— 那是逻辑身份把多台设备的行**合并进同一查询**的产物。
// 逻辑层移除后, 一次查询只落在一台设备 (device_id = ? 等值) 上, 因此需要钉住的
// 性质从"去重"变成 **隔离**: 别的设备的行绝不能混进来。
//
// 造两台设备, 在**完全相同的 timestamp** 上写同一个 sensor 的不同值:
//   - 若查询条件写错 (漏了 device_id / 用 IN 兜底 / 又回到 UNION), 桶均值会被
//     另一台设备的值污染, 值可直接观察到不同 (10/20 vs 110/120);
//   - 隔离正确时每桶只有目标设备那一个值。
func TestHistoryPointsPostgres_DeviceIsolation(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)

	if err := db.Create(&models.Node{NodeID: "N-PGI", Name: "pgi", Status: "online"}).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := db.Create(&models.Channel{NodeID: "N-PGI", HardwareType: "I2C", BusType: "I2C", Enabled: true}).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	dev1 := models.EdgeDevice{Name: "iso-target", Type: "bms", NodeID: "N-PGI", ChannelID: 1}
	dev2 := models.EdgeDevice{Name: "iso-noise", Type: "bms", NodeID: "N-PGI", ChannelID: 1}
	if err := db.Create(&dev1).Error; err != nil {
		t.Fatalf("create dev1: %v", err)
	}
	if err := db.Create(&dev2).Error; err != nil {
		t.Fatalf("create dev2: %v", err)
	}

	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	// 同一 (sensor, timestamp) 上两台设备各有读数: 目标 10/20, 噪声 110/120。
	rows := []models.UnifiedData{
		{DeviceID: dev1.ID, SensorName: "soc", Value: 10, Timestamp: start},
		{DeviceID: dev2.ID, SensorName: "soc", Value: 110, Timestamp: start},
		{DeviceID: dev1.ID, SensorName: "soc", Value: 20, Timestamp: start.Add(time.Second)},
		{DeviceID: dev2.ID, SensorName: "soc", Value: 120, Timestamp: start.Add(time.Second)},
	}
	for i := range rows {
		if err := db.Create(&rows[i]).Error; err != nil {
			t.Fatalf("seed row %d: %v", i, err)
		}
	}

	// 窗口 2s / maxPoints=500 -> 桶宽 1s => t0、t0+1s 两个桶。
	points, err := queryHistoryPoints(context.Background(), db, historyPointQuery{
		EdgeDeviceID: dev1.ID,
		Category:     "soc", Start: start, End: start.Add(2 * time.Second),
		MaxPoints: 500, Mode: AggModeAvg,
	})
	if err != nil {
		t.Fatalf("queryHistoryPoints(avg, isolated): %v", err)
	}
	wantValues := []float64{10, 20}
	if len(points) != len(wantValues) {
		t.Fatalf("avg points = %d, want %d: %+v", len(points), len(wantValues), points)
	}
	for i, p := range points {
		if p.Value != wantValues[i] {
			t.Errorf("bucket %d value = %v, want %v (another device leaked into the query)", i, p.Value, wantValues[i])
		}
	}

	// 端点同样必须隔离 (证明 handler 把 device_pk 传成了 device_id 等值条件)。
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
		t.Fatalf("endpoint points = %+v, want %d points", batch, len(wantValues))
	}
	for i, p := range batch[0].Data {
		if p.Value != wantValues[i] {
			t.Errorf("endpoint bucket %d value = %v, want %v", i, p.Value, wantValues[i])
		}
	}
}

// ==================== 设备维度查询确实走设备列索引 ====================

// explainPlan runs EXPLAIN on sql and returns the plan text lines.
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

// requirePlanOmits fails if the plan text contains any of the banned substrings.
func requirePlanOmits(t *testing.T, plan []string, banned ...string) {
	t.Helper()
	joined := strings.Join(plan, "\n")
	for _, b := range banned {
		if strings.Contains(joined, b) {
			t.Fatalf("query plan must not mention %q:\n  %s", b, joined)
		}
	}
}

// alignUnifiedDataSchemaToProduction 把测试 schema 的 unified_data 索引形状
// **对齐到生产 DDL**（datalifecycle/partition_mgr.go 的 partitionParentIndexes）。
//
// ============ 为什么必须显式对齐（2026-11-07 实测，非推测）============
//
// 测试库走 testutil.OpenTestDB → AutoMigrate(allModels)，而 models.UnifiedData 的
// SensorName 字段带 `gorm:"index"` ⇒ AutoMigrate 会建出单列索引
// idx_unified_data_sensor_name。
//
// 生产走 partition_mgr.go 的裸 DDL，**刻意不建**这个索引 —— 第 911 行注释：
//
//	//   · sensor_name_idx —— 全仓没有以 sensor_name 为**前导**列的查询。
//
// 于是同一条 `device_id = ? AND sensor_name = ? AND timestamp BETWEEN ? AND ?` 查询，
// 在两个形状上 planner 给出**不同答案**（同数据、同统计信息、同 PG18.6 实测）：
//
//	有 sensor_name 索引（AutoMigrate 形状）→ Index Scan using idx_unified_data_sensor_name (cost=0.29..84.54)
//	删掉后（= 生产形状）              → Index Scan using idx_unified_data_device_id  (cost=0.29..1215.29)
//
// 本包断言的是「查询在生产上走 device_id 前导索引」，那就必须先让 schema 是生产形状，
// 否则断言的是一条生产上根本不存在的计划 —— 用例会一直红，而红的原因与产品代码无关。
//
// ⚠ 刻意**只**改测试 schema，两条更省事的路都是错的：
//
//	· 改 production DDL 加 sensor_name 索引 —— 生产会多一个没人用的索引（写放大 + 空间）；
//	· 改 model 的 gorm tag 去掉 index  —— 所有 AutoMigrate 出来的库（含测试库以外）
//	  都会失去它，是**另一个方向**的漂移，且掩盖真实差异而不消除它。
func alignUnifiedDataSchemaToProduction(t *testing.T, db *gorm.DB) {
	t.Helper()

	// (1) 去掉 AutoMigrate 多出来的单列 sensor_name 索引（生产刻意不建，见上）。
	if err := db.Exec("DROP INDEX IF EXISTS idx_unified_data_sensor_name").Error; err != nil {
		t.Fatalf("drop idx_unified_data_sensor_name: %v", err)
	}

	// (2) 补上 AutoMigrate 不会建、但生产 DDL 一定建的两条**设备前导复合索引**
	//     （partition_mgr.go 的 unified_data 清单）。
	//
	// 为什么这一步不能省（我第一版只做了 (1)：avg 模式绿了，change 模式仍红）：
	//
	//	change 模式带窗口函数 `ORDER BY timestamp`，在「只有单列 device_id /
	//	timestamp 索引」的形状下 planner 会选 timestamp 索引来省一次 Sort，把
	//	device_id 降级成 Filter —— 那是**合理的**代价选择，不是缺陷。生产同时有
	//	(device_id, timestamp)，它既提供设备过滤又提供时间序，planner 必然选它。
	//	缺了这条，用例就在一个生产不存在的形状上做断言。
	//
	// 只有「多出来的删掉 + 缺的补上」两半都做到，测试 schema 才真是生产形状。
	for _, ddl := range []string{
		"CREATE INDEX IF NOT EXISTS unified_data_device_sensor_ts_idx ON unified_data (device_id, sensor_name, timestamp)",
		"CREATE INDEX IF NOT EXISTS unified_data_device_ts_idx ON unified_data (device_id, timestamp)",
	} {
		if err := db.Exec(ddl).Error; err != nil {
			t.Fatalf("align index DDL failed: %v\nDDL: %s", err, ddl)
		}
	}

	// (3) 自检：对齐后的形状必须「没有 sensor_name 前导索引」且「有 3 条 device_id
	//     前导索引」。把将来的漂移变成一条可读的失败，而不是留给读者一条莫名其妙
	//     的「计划里没有 device_id 索引」。
	indexes, err := db.Migrator().GetIndexes(&models.UnifiedData{})
	if err != nil {
		t.Fatalf("list indexes on unified_data: %v", err)
	}
	deviceLeading := 0
	for _, idx := range indexes {
		cols := idx.Columns()
		if len(cols) == 0 {
			continue
		}
		if cols[0] == "sensor_name" {
			t.Fatalf("对齐后仍有以 sensor_name 为前导列的索引 %q，生产 DDL 没有它"+
				"（partition_mgr.go:911）；请更新 alignUnifiedDataSchemaToProduction", idx.Name())
		}
		if cols[0] == "device_id" {
			deviceLeading++
		}
	}
	if deviceLeading < 3 {
		t.Fatalf("对齐后 device_id 前导索引只有 %d 条，生产 DDL 有 3 条"+
			"（device_id / device_sensor_ts / device_ts）；对齐 DDL 可能未生效", deviceLeading)
	}
}

// deviceLeadingIndexNames 返回某表上**所有**以 device_id 为首列的索引名。
//
// 为什么返回列表而不是一个名字（原 deviceIndexName 的修订）：生产在同一张表上有
// 三条 device_id 前导索引（单列 / +sensor_name+timestamp / +timestamp），planner
// 按查询形态各选其一。断言「必须选中某一条具体索引」会把用例绑死在 planner 的
// 代价模型上 —— 正确的断言是「选中了**任意一条** device_id 前导索引」，
// 即：设备维度过滤走索引，而不是退化成全表扫 + Filter。
func deviceLeadingIndexNames(t *testing.T, db *gorm.DB, model interface{}, table string) []string {
	t.Helper()
	indexes, err := db.Migrator().GetIndexes(model)
	if err != nil {
		t.Fatalf("list indexes on %s: %v", table, err)
	}
	var names []string
	for _, idx := range indexes {
		cols := idx.Columns()
		if len(cols) > 0 && cols[0] == "device_id" {
			names = append(names, idx.Name())
		}
	}
	if len(names) == 0 {
		t.Fatalf("%s has no index whose leading column is device_id (逻辑层移除后 device_id 是唯一查询维度, 没有它只能全表扫)", table)
	}
	return names
}

// requirePlanMentionsAny 断言计划里出现 want 中**任意一个**子串。
func requirePlanMentionsAny(t *testing.T, plan []string, want []string) {
	t.Helper()
	joined := strings.Join(plan, "\n")
	for _, w := range want {
		if strings.Contains(joined, w) {
			return
		}
	}
	t.Fatalf("query plan mentions none of the device_id-leading indexes %v:\n  %s",
		want, strings.Join(plan, "\n  "))
}

// TestPostgresDeviceScopedPlan 取代原 TestPostgresUnionPlanUsesCompositeIndexes。
//
// 原用例锁的是「UNION ALL 两个分支各自带干净前导列, 避免 OR 条件把
// logical_device_id 降级成 Filter」。列没了, 分支也没了 —— 现在要锁的是**更简单
// 也更强的一条**: 单表单条件查询必须能走 device_id 前导索引。
//
// 做法: 关掉 enable_seqscan 后 EXPLAIN, 断言计划里出现那个以 device_id 为首列的
// 索引, 且计划里**不出现** logical_device_id。关 seqscan 是为了消除"数据量小所以
// planner 选顺序扫"的噪声 —— 这里断言的是"索引可用且被选中", 不是"planner 在
// 任意代价下都选它" (后者在小表上不稳定, 原用例注释里也承认过)。
//
// ⚠ 前置动作 alignUnifiedDataSchemaToProduction 不可删：AutoMigrate 建出的 schema
// 比生产多一个 sensor_name 前导索引，会让 planner 走错索引 ⇒ 本用例稳定红
// （2026-11-07 实测 3/3）。详见该函数的注释。
func TestPostgresDeviceScopedPlan(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	alignUnifiedDataSchemaToProduction(t, db)
	devID, start := seedPostgresHistoryDevice(t, db)

	// 造足够的行, 让索引选择有意义。
	start = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	const sensors = 16
	const points = 2000
	rows := make([]models.UnifiedData, 0, sensors*points)
	for i := 0; i < points; i++ {
		ts := start.Add(time.Duration(i) * time.Second)
		for s := 0; s < sensors; s++ {
			rows = append(rows, models.UnifiedData{
				DeviceID: devID, SensorName: fmt.Sprintf("metric_%02d", s),
				Value: float64(i), Timestamp: ts,
			})
		}
	}
	if err := db.CreateInBatches(rows, 1000).Error; err != nil {
		t.Fatalf("seed unified_data: %v", err)
	}
	if err := db.Exec("ANALYZE unified_data").Error; err != nil {
		t.Fatalf("analyze unified_data: %v", err)
	}

	// 断言口径：两条模式各自选中**任意一条** device_id 前导索引即可。
	//
	// 为什么不钉死某一条（原实现用 deviceIndexName 取"第一条"）：生产同时有
	// (device_id) / (device_id, sensor_name, timestamp) / (device_id, timestamp)
	// 三条，planner 按查询形态各选其一。钉死单条会把用例绑死在 PG 的代价模型
	// 细节上 —— 实测 avg 选 device_sensor_ts、change 选 device_ts，两者都是正确
	// 答案。这里要锁的性质只有一条：**设备维度过滤走索引，不退化成全表扫 + Filter**。
	deviceIdxs := deviceLeadingIndexNames(t, db, &models.UnifiedData{}, "unified_data")

	// 在一个事务里关掉 seqscan, 让 planner 只能走索引 (设置不跨连接泄漏)。
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SET LOCAL enable_seqscan = off").Error; err != nil {
			return err
		}
		end := start.Add(2 * time.Hour)
		for _, mode := range []AggMode{AggModeAvg, AggModeChange} {
			sqlText, args := historyAggregateSQL(db, historyPointQuery{
				EdgeDeviceID: devID, Category: "metric_00",
				Start: start, End: end, MaxPoints: 500, Mode: mode,
			}, bucketWidthSeconds(historyWindowSeconds(start, end), 500))
			if sqlText == "" {
				t.Fatalf("%s: SQL is empty", mode)
			}
			plan := explainPlan(t, tx, sqlText, args...)
			requirePlanMentionsAny(t, plan, deviceIdxs)
			requirePlanOmits(t, plan, "logical_device_id", "UNION")
			t.Logf("%s plan on unified_data:\n  %s", mode, strings.Join(plan, "\n  "))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("plan check: %v", err)
	}
}

// ==================== change 模式: 跨传感器乱序写入下的全局有序 ====================

// TestHistoryPointsPostgres_ChangeOrderedAcrossSensors 取代原
// TestHistoryPointsPostgres_ChangeAcrossBranchesOrdered。
//
// 原用例验证「A/B 两分支的行交错时, change 序列仍按 timestamp 全局有序」——
// 分支没了, 但**乱序写入**这个真实条件还在: 不同命令/不同传感器的心跳会让行在
// 堆里不按时间先后落盘。change 模式依赖 lag() 的 ORDER BY, 必须自己保证全局有序,
// 不能指望物理行序。
func TestHistoryPointsPostgres_ChangeOrderedAcrossSensors(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	devID, start := seedPostgresHistoryDevice(t, db)

	start = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	// 值序列按时间应为 0,1,0,0,1 => 变更点: t0=0(首点), t1=1(0->1), t2=0(1->0), t4=1(0->1)。
	// 写入顺序刻意打乱 (4,1,3,0,2), 模拟多命令交错的落盘顺序。
	type point struct {
		offset int
		value  float64
	}
	points := []point{{4, 1}, {1, 1}, {3, 0}, {0, 0}, {2, 0}}
	for _, pt := range points {
		row := models.UnifiedData{
			DeviceID: devID, SensorName: "protection_status",
			Value: pt.value, Timestamp: start.Add(time.Duration(pt.offset) * time.Second),
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("seed unified row t=%d: %v", pt.offset, err)
		}
	}

	got, err := queryHistoryPoints(context.Background(), db, historyPointQuery{
		EdgeDeviceID: devID, Category: "protection_status",
		Start: start, End: start.Add(time.Hour), MaxPoints: 500, Mode: AggModeChange,
	})
	if err != nil {
		t.Fatalf("change query: %v", err)
	}
	wantOffsets := []int{0, 1, 2, 4}
	wantValues := []float64{0, 1, 0, 1}
	if len(got) != len(wantOffsets) {
		t.Fatalf("change points = %d, want %d: %#v", len(got), len(wantOffsets), got)
	}
	for i := range wantOffsets {
		wantTs := start.Add(time.Duration(wantOffsets[i]) * time.Second)
		if !got[i].Timestamp.Equal(wantTs) {
			t.Errorf("point[%d].timestamp = %s, want %s (global order regardless of insert order)",
				i, got[i].Timestamp.Format(time.RFC3339), wantTs.Format(time.RFC3339))
		}
		if got[i].Value != wantValues[i] {
			t.Errorf("point[%d].value = %v, want %v", i, got[i].Value, wantValues[i])
		}
	}
}
