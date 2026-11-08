package api

// SQLite 单测 (契约 §1.5): 聚合纯函数 + 方言分支选择 + "SQLite 行为零回归"。
// 真正的 PG SQL 由 history_points_pg_test.go (-tags=integration) 在真实
// PostgreSQL 上执行验证。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"ehome/backend/internal/models"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// ==================== 桶宽 (契约 §1.2: W = max(1, ceil(window/maxPoints))) ====================

func TestBucketWidthSeconds(t *testing.T) {
	tests := []struct {
		name          string
		windowSeconds int64
		maxPoints     int
		want          int64
	}{
		{name: "24h/500", windowSeconds: 24 * 3600, maxPoints: 500, want: 173},
		{name: "1h/500", windowSeconds: 3600, maxPoints: 500, want: 8},
		{name: "30d/500", windowSeconds: 30 * 24 * 3600, maxPoints: 500, want: 5184},
		{name: "window smaller than maxPoints -> 1s", windowSeconds: 100, maxPoints: 500, want: 1},
		{name: "window equals maxPoints -> 1s", windowSeconds: 500, maxPoints: 500, want: 1},
		{name: "maxPoints zero -> raw (0)", windowSeconds: 3600, maxPoints: 0, want: 0},
		{name: "maxPoints negative -> raw (0)", windowSeconds: 3600, maxPoints: -1, want: 0},
		{name: "empty window -> raw (0)", windowSeconds: 0, maxPoints: 500, want: 0},
		{name: "negative window -> raw (0)", windowSeconds: -10, maxPoints: 500, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bucketWidthSeconds(tt.windowSeconds, tt.maxPoints); got != tt.want {
				t.Errorf("bucketWidthSeconds(%d, %d) = %d, want %d",
					tt.windowSeconds, tt.maxPoints, got, tt.want)
			}
		})
	}
}

func TestHistoryWindowSeconds(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if got := historyWindowSeconds(start, start.Add(24*time.Hour)); got != 86400 {
		t.Errorf("24h window = %d, want 86400", got)
	}
	if got := historyWindowSeconds(start, start); got != 0 {
		t.Errorf("empty window = %d, want 0", got)
	}
	if got := historyWindowSeconds(start, start.Add(-time.Hour)); got != 0 {
		t.Errorf("reversed window = %d, want 0", got)
	}
	if got := historyWindowSeconds(start, start.Add(500*time.Millisecond)); got != 0 {
		t.Errorf("sub-second window = %d, want 0 (raw path)", got)
	}
}

// ==================== agg 解析 / auto 分类 / 非法回落 ====================

func TestParseAggMode(t *testing.T) {
	tests := []struct {
		raw  string
		want AggMode
	}{
		{"", AggModeAuto},
		{"auto", AggModeAuto},
		{"AUTO", AggModeAuto},
		{"avg", AggModeAvg},
		{" AVG ", AggModeAvg},
		{"raw", AggModeRaw},
		{"change", AggModeChange},
		{"Change", AggModeChange},
		// 非法值回落 auto —— 不返回 400, 保持旧前端兼容。
		{"bogus", AggModeAuto},
		{"protection_status", AggModeAuto},
		{"1", AggModeAuto},
	}
	for _, tt := range tests {
		if got := parseAggMode(tt.raw); got != tt.want {
			t.Errorf("parseAggMode(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}
}

func TestResolveAggMode(t *testing.T) {
	tests := []struct {
		name     string
		mode     AggMode
		category string
		want     AggMode
	}{
		{name: "auto protection_status -> change", mode: AggModeAuto, category: "protection_status", want: AggModeChange},
		{name: "auto fet_status -> change", mode: AggModeAuto, category: "fet_status", want: AggModeChange},
		{name: "auto cell_voltage -> avg", mode: AggModeAuto, category: "cell_voltage_1", want: AggModeAvg},
		{name: "auto similar-but-not-change -> avg", mode: AggModeAuto, category: "protection_status_2", want: AggModeAvg},
		{name: "explicit avg overrides change category", mode: AggModeAvg, category: "protection_status", want: AggModeAvg},
		{name: "explicit change on continuous", mode: AggModeChange, category: "rsoc", want: AggModeChange},
		{name: "explicit raw kept", mode: AggModeRaw, category: "protection_status", want: AggModeRaw},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveAggMode(tt.mode, tt.category); got != tt.want {
				t.Errorf("resolveAggMode(%q, %q) = %q, want %q", tt.mode, tt.category, got, tt.want)
			}
		})
	}
}

func TestIsChangeCategory(t *testing.T) {
	for _, cat := range []string{"protection_status", "fet_status"} {
		if !isChangeCategory(cat) {
			t.Errorf("isChangeCategory(%q) = false, want true", cat)
		}
	}
	for _, cat := range []string{"", "protection", "fet", "cell_voltage_1", "status"} {
		if isChangeCategory(cat) {
			t.Errorf("isChangeCategory(%q) = true, want false", cat)
		}
	}
}

// ==================== 聚合启用条件 (方言分支选择) ====================

func TestHistoryAggregationEligible(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	if !historyAggregationEligible(500, start, end) {
		t.Error("24h/500 must be eligible for aggregation")
	}
	if historyAggregationEligible(0, start, end) {
		t.Error("maxPoints=0 must keep the raw path")
	}
	if historyAggregationEligible(-1, start, end) {
		t.Error("negative maxPoints must keep the raw path")
	}
	if historyAggregationEligible(500, start, start) {
		t.Error("empty window must keep the raw path")
	}
	if historyAggregationEligible(500, start, start.Add(-time.Hour)) {
		t.Error("reversed window must keep the raw path")
	}
	if historyAggregationEligible(500, start, start.Add(500*time.Millisecond)) {
		t.Error("sub-second window must keep the raw path")
	}
	if !historyAggregationEligible(1, start, start.Add(time.Second)) {
		t.Error("1s window / maxPoints=1 must be eligible (bucket width 1s)")
	}
}

func TestShouldAggregateHistory_SQLiteNeverAggregates(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	if shouldAggregateHistory(db, 500, start, end) {
		t.Fatal("SQLite must never take the PostgreSQL aggregation path (spoken SQL is PG-only)")
	}
	if shouldAggregateHistory(nil, 500, start, end) {
		t.Fatal("nil db must not be treated as PostgreSQL")
	}
}

// ==================== SQLite 端点零回归 (既有行为是契约) ====================

// seedSQLiteHistoryDevice creates one edge device + two unified_data rows for
// the legacy-shape regression checks below.
//
// 2026-10-23: 造数据不再需要逻辑设备 —— 数据维度就是边缘设备 (device_id)。
func seedSQLiteHistoryDevice(t *testing.T, db *gorm.DB) uint {
	t.Helper()
	db.Create(&models.Node{NodeID: "N-AGG", Name: "agg", Status: "online"})
	db.Create(&models.Channel{NodeID: "N-AGG", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	dev := models.EdgeDevice{Name: "agg", Type: "bms", NodeID: "N-AGG", ChannelID: 1,
		HardwareID: "0x01"}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatalf("create edge device: %v", err)
	}
	base := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		row := models.UnifiedData{
			DeviceID: dev.ID, SensorName: "cell_voltage_1",
			Unit: "V", Value: 3.1 + float64(i)/10, Timestamp: base.Add(time.Duration(i) * time.Second),
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("create unified row: %v", err)
		}
	}
	return dev.ID
}

// TestHistoricalEndpoints_SQLiteKeepsLegacyShapeAndPath pins the contract's
// hardest requirement: on SQLite (and for agg=raw) the endpoints must keep the
// legacy full-row models.UnifiedData shape and the legacy query path, no matter
// what agg/ max_points say. If someone ever lets the PG branch leak into
// SQLite, this test turns red on the shape (missing created_at/sensor_name/id).
func TestHistoricalEndpoints_SQLiteKeepsLegacyShapeAndPath(t *testing.T) {
	r, db := setupTestRouter(t)
	devID := seedSQLiteHistoryDevice(t, db)

	start := url.QueryEscape("2026-07-01T00:00:00Z")
	end := url.QueryEscape("2026-07-03T00:00:00Z")
	aggs := []string{"auto", "avg", "change", "raw", "bogus"}

	for _, agg := range aggs {
		t.Run("historical/agg="+agg, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, fmt.Sprintf(
				"/api/v1/unified-data/historical?device_pk=%d&category=cell_voltage_1&start_time=%s&end_time=%s&max_points=500&agg=%s",
				devID, start, end, agg), nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
			}
			rows := envelopeData[[]map[string]json.RawMessage](t, w.Body.Bytes())
			if len(rows) != 2 {
				t.Fatalf("SQLite must return all rows (Go-side downsample only): got %d, want 2", len(rows))
			}
			for _, row := range rows {
				for _, key := range []string{"id", "device_id", "sensor_name", "unit", "created_at", "timestamp", "value"} {
					if _, ok := row[key]; !ok {
						t.Fatalf("SQLite legacy row shape lost key %q (agg=%s): %s", key, agg, w.Body.String())
					}
				}
			}
		})

		t.Run("historical-batch/agg="+agg, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, fmt.Sprintf(
				"/api/v1/unified-data/historical-batch?device_pk=%d&categories=cell_voltage_1&start_time=%s&end_time=%s&max_points=500&agg=%s",
				devID, start, end, agg), nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
			}
			type batchCategory struct {
				Category string                       `json:"category"`
				Data     []map[string]json.RawMessage `json:"data"`
			}
			batch := envelopeData[[]batchCategory](t, w.Body.Bytes())
			if len(batch) != 1 {
				t.Fatalf("batch returned %d categories, want 1: %s", len(batch), w.Body.String())
			}
			if len(batch[0].Data) != 2 {
				t.Fatalf("batch SQLite rows = %d, want 2: %s", len(batch[0].Data), w.Body.String())
			}
			if _, ok := batch[0].Data[0]["created_at"]; !ok {
				t.Fatalf("batch SQLite legacy row shape lost created_at (agg=%s): %s", agg, w.Body.String())
			}
		})
	}
}

// ==================== 设备维度 SQL 组装 (纯函数) ====================
//
// 2026-10-23: 原 §1.6 的「scope 拆分支 / UNION ALL」用例整组删除。那组断言锁的是
// 「logical 分支 + NULL-logical 回退分支各自带干净索引前导列」—— 两个分支和它们
// 依赖的列都不存在了。取而代之的是下面的单表单条件断言: SQL 里必须只有
// `device_id = ?` 一个维度条件, 且**不得**再出现 UNION / logical_device_id / 去重。

func TestExceedsChangePointCap(t *testing.T) {
	// change 模式的点数由翻转次数决定, 旧路径有 max_points 封顶; 超过硬上限
	// 必须报错而不是静默截断（截断会让 0→1 跃变计数变小 = 错数据）。
	if !exceedsChangePointCap(AggModeChange, maxChangePoints+1) {
		t.Error("change mode above cap must be rejected")
	}
	if exceedsChangePointCap(AggModeChange, maxChangePoints) {
		t.Error("change mode exactly at cap must pass")
	}
	if exceedsChangePointCap(AggModeAvg, maxChangePoints+1) {
		t.Error("avg mode is bucket-capped and must never be rejected by the change cap")
	}
	if exceedsChangePointCap(AggModeRaw, maxChangePoints+1) {
		t.Error("raw mode is legacy and must not be affected")
	}
}
