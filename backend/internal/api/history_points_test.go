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
	"strings"
	"testing"
	"time"

	"ehome/backend/internal/datalifecycle"
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

// seedSQLiteHistoryDevice creates one logical device + instance + two
// unified_data rows for the legacy-shape regression checks below.
func seedSQLiteHistoryDevice(t *testing.T, db *gorm.DB) uint {
	t.Helper()
	ld := models.LogicalDevice{IdentityKey: "agg:0x01", Name: "agg", DeviceType: "bms"}
	if err := db.Create(&ld).Error; err != nil {
		t.Fatalf("create logical device: %v", err)
	}
	db.Create(&models.Node{NodeID: "N-AGG", Name: "agg", Status: "online"})
	db.Create(&models.Channel{NodeID: "N-AGG", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	dev := models.EdgeDevice{Name: "agg", Type: "bms", NodeID: "N-AGG", ChannelID: 1,
		HardwareID: "0x01", LogicalDeviceID: &ld.ID}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatalf("create edge device: %v", err)
	}
	base := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		row := models.UnifiedData{
			DeviceID: dev.ID, LogicalDeviceID: &ld.ID, SensorName: "cell_voltage_1",
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

// ==================== §1.6: scope 拆分支 / UNION ALL SQL 组装 (纯函数) ====================

func TestDataScopeBranches(t *testing.T) {
	if got := dataScopeBranches(nil); got != nil {
		t.Fatalf("nil scope branches = %#v, want nil", got)
	}

	// backfill 前: 无逻辑身份 → 单分支 device_id 回退 (与 dataScopeCond 一致)。
	fallback := dataScopeBranches(&datalifecycle.DataQueryScope{FallbackDeviceID: 7})
	if len(fallback) != 1 || fallback[0].cond != "device_id = ?" ||
		len(fallback[0].args) != 1 || fallback[0].args[0] != uint(7) {
		t.Fatalf("fallback branches = %#v, want single device_id = 7", fallback)
	}

	// 两种 scope 都有 → 两个互斥分支, A 在前 (logical), B 在后 (NULL-logical)。
	both := dataScopeBranches(&datalifecycle.DataQueryScope{
		LogicalID:        14,
		FallbackDeviceID: 14,
		Scope:            &datalifecycle.Scope{LogicalIDs: []uint{14}, InstanceIDs: []uint{14, 15}},
	})
	if len(both) != 2 {
		t.Fatalf("branches = %#v, want 2", both)
	}
	if both[0].cond != "logical_device_id IN ?" ||
		!strings.Contains(both[1].cond, "logical_device_id IS NULL AND device_id IN ?") {
		t.Fatalf("branch conditions = %q / %q", both[0].cond, both[1].cond)
	}

	// 单一 scope: 只生成对应分支 (契约: 单分支不拼 UNION)。
	onlyLogical := dataScopeBranches(&datalifecycle.DataQueryScope{
		LogicalID: 14, Scope: &datalifecycle.Scope{LogicalIDs: []uint{14}},
	})
	if len(onlyLogical) != 1 || onlyLogical[0].cond != "logical_device_id IN ?" {
		t.Fatalf("logical-only branches = %#v, want 1", onlyLogical)
	}
	onlyInstance := dataScopeBranches(&datalifecycle.DataQueryScope{
		LogicalID: 14, Scope: &datalifecycle.Scope{InstanceIDs: []uint{14}},
	})
	if len(onlyInstance) != 1 || !strings.Contains(onlyInstance[0].cond, "logical_device_id IS NULL") {
		t.Fatalf("instance-only branches = %#v, want 1 NULL-logical branch", onlyInstance)
	}
}

// bothScope is the two-branch scope used by the SQL-shape tests below.
func bothScope() *datalifecycle.DataQueryScope {
	return &datalifecycle.DataQueryScope{
		LogicalID:        14,
		FallbackDeviceID: 14,
		Scope:            &datalifecycle.Scope{LogicalIDs: []uint{14}, InstanceIDs: []uint{14, 15}},
	}
}

func TestHistoryUnionSQL(t *testing.T) {
	start := time.Date(2026, 10, 19, 14, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	extra := `sensor_name = ? AND "timestamp" BETWEEN ? AND ?`
	extraArgs := []interface{}{"cell_voltage_1", start, end}

	sqlText, args := historyUnionSQL(bothScope(), `"timestamp", "value"`, extra, extraArgs, "")
	if !strings.Contains(sqlText, "UNION ALL") {
		t.Fatalf("two-branch SQL must contain UNION ALL: %s", sqlText)
	}
	if !strings.Contains(sqlText, "logical_device_id IN ?") ||
		!strings.Contains(sqlText, "logical_device_id IS NULL AND device_id IN ?") {
		t.Fatalf("UNION SQL lost a scope branch: %s", sqlText)
	}
	// 参数顺序必须与 SQL 占位符一致: A(branch, extra) → B(branch, extra)。
	if len(args) != 8 {
		t.Fatalf("args = %d, want 8: %#v", len(args), args)
	}
	if got := args[0].([]uint); len(got) != 1 || got[0] != 14 {
		t.Errorf("args[0] = %#v, want logical IDs [14]", args[0])
	}
	if args[1] != "cell_voltage_1" {
		t.Errorf("args[1] = %#v, want sensor category", args[1])
	}
	if got := args[4].([]uint); len(got) != 2 || got[0] != 14 || got[1] != 15 {
		t.Errorf("args[4] = %#v, want instance IDs [14 15]", args[4])
	}
	if args[5] != "cell_voltage_1" {
		t.Errorf("args[5] = %#v, want sensor category", args[5])
	}

	// 单分支: 不拼 UNION。
	singleSQL, singleArgs := historyUnionSQL(&datalifecycle.DataQueryScope{
		LogicalID: 14, Scope: &datalifecycle.Scope{LogicalIDs: []uint{14}},
	}, `"timestamp", "value"`, extra, extraArgs, "")
	if strings.Contains(singleSQL, "UNION ALL") {
		t.Fatalf("single-branch SQL must not contain UNION ALL: %s", singleSQL)
	}
	if len(singleArgs) != 4 {
		t.Fatalf("single-branch args = %d, want 4: %#v", len(singleArgs), singleArgs)
	}

	// scope 匹配不到任何行 → 空 SQL (调用方直接返回空结果)。
	emptySQL, emptyArgs := historyUnionSQL(&datalifecycle.DataQueryScope{
		LogicalID: 14, Scope: &datalifecycle.Scope{},
	}, `"timestamp", "value"`, extra, extraArgs, "")
	if emptySQL != "" || emptyArgs != nil {
		t.Fatalf("empty scope SQL = %q / %#v, want empty", emptySQL, emptyArgs)
	}
}

func TestHistoryAggregateSQLShape(t *testing.T) {
	start := time.Date(2026, 10, 19, 14, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	width := bucketWidthSeconds(historyWindowSeconds(start, end), 500)
	base := historyPointQuery{Scope: bothScope(), Category: "cell_voltage_1", Start: start, End: end, MaxPoints: 500}

	// avg: 桶宽内联为整数 (173), 分桶聚合 + 序数分组/排序。
	avgSQL, _ := historyAggregateSQL(base, width)
	for _, want := range []string{
		"to_timestamp(floor(extract(epoch FROM \"timestamp\") / 173) * 173)",
		`avg("value")`,
		"GROUP BY 1 ORDER BY 1",
		"UNION ALL",
	} {
		if !strings.Contains(avgSQL, want) {
			t.Errorf("avg SQL missing %q: %s", want, avgSQL)
		}
	}
	if strings.Contains(avgSQL, "ROW_NUMBER()") {
		t.Errorf("avg SQL without DedupNeeded must not dedup: %s", avgSQL)
	}

	// change: lag + IS DISTINCT FROM, 且绝不允许"只取非零"的取巧。
	change := base
	change.Mode = AggModeChange
	changeSQL, _ := historyAggregateSQL(change, width)
	for _, want := range []string{
		`lag(d."value") OVER (ORDER BY d."timestamp")`,
		`s.prev IS DISTINCT FROM s."value"`,
	} {
		if !strings.Contains(changeSQL, want) {
			t.Errorf("change SQL missing %q: %s", want, changeSQL)
		}
	}
	if got := strings.Count(changeSQL, `ORDER BY "timestamp"`); got < 2 {
		t.Errorf("change SQL must ORDER BY in every UNION branch (Merge Append instead of sort), got %d: %s", got, changeSQL)
	}
	for _, banned := range []string{"value <> 0", "value != 0", "value > 0"} {
		if strings.Contains(changeSQL, banned) {
			t.Errorf("change SQL must not filter non-zero (breaks 1->0): %q / %s", banned, changeSQL)
		}
	}

	// dedup: ROW_NUMBER 必须在 avg 之前 (先保形去重再聚合)。
	dedup := base
	dedup.DedupNeeded = true
	dedupSQL, _ := historyAggregateSQL(dedup, width)
	for _, want := range []string{
		"ROW_NUMBER() OVER (PARTITION BY sensor_name, \"timestamp\" ORDER BY id DESC)",
		"WHERE dedup_rn = 1",
		`avg("value")`,
	} {
		if !strings.Contains(dedupSQL, want) {
			t.Errorf("dedup SQL missing %q: %s", want, dedupSQL)
		}
	}
	if !strings.Contains(dedupSQL, `"timestamp", "value", "id", "sensor_name"`) {
		t.Errorf("dedup union must select id+sensor_name for MAX(id) dedup: %s", dedupSQL)
	}
}

func TestLatestDeviceDataSQL(t *testing.T) {
	// 两分支: 每支各自 LIMIT 1, 外层按 created_at 取较新 —— 与旧 OR scope
	// 查询的 max(created_at) over (A ∪ B) 严格等价（评审 2026-10-21：
	// 「A 命中即止」在 backfill 未完成 / NULL 回退写入时会返回 stale）。
	sqlText, args := latestDeviceDataSQL(bothScope())
	if sqlText == "" {
		t.Fatal("both-branch latest SQL is empty")
	}
	if !strings.Contains(sqlText, "logical_device_id IN ?") {
		t.Errorf("latest SQL missing logical branch: %s", sqlText)
	}
	if !strings.Contains(sqlText, "logical_device_id IS NULL AND device_id IN ?") {
		t.Errorf("latest SQL missing NULL-logical fallback branch: %s", sqlText)
	}
	if !strings.Contains(sqlText, "UNION ALL") {
		t.Errorf("latest SQL must merge both branch rows: %s", sqlText)
	}
	// 两个分支各一条 + 外层排序一条 = 3（每支各自 LIMIT 1 后才能走各自索引）。
	if strings.Count(sqlText, "ORDER BY created_at DESC LIMIT 1") != 3 {
		t.Errorf("each branch and the outer query must order by created_at DESC LIMIT 1: %s", sqlText)
	}
	if !strings.HasSuffix(strings.TrimSpace(sqlText), "ORDER BY created_at DESC LIMIT 1") {
		t.Errorf("outer query must pick the newest across branches: %s", sqlText)
	}
	if len(args) != 2 {
		// 每支一个 IN 切片参数（GORM 负责把 IN ? 展开）；多出的说明条件拼错了。
		t.Errorf("latest args = %d, want 2 (one IN slice per branch)", len(args))
	}

	// 单分支: 不拼 UNION（语义优先 + 少一次 Gather）。
	singleSQL, _ := latestDeviceDataSQL(&datalifecycle.DataQueryScope{
		LogicalID: 14, Scope: &datalifecycle.Scope{LogicalIDs: []uint{14}},
	})
	if strings.Contains(singleSQL, "UNION ALL") {
		t.Errorf("single-branch latest SQL must not union: %s", singleSQL)
	}
	if !strings.Contains(singleSQL, "ORDER BY created_at DESC LIMIT 1") {
		t.Errorf("single-branch latest SQL missing LIMIT 1: %s", singleSQL)
	}

	// 空 scope: 不打扰数据库。
	if emptySQL, emptyArgs := latestDeviceDataSQL(nil); emptySQL != "" || emptyArgs != nil {
		t.Errorf("nil scope must render empty SQL, got %q args=%v", emptySQL, emptyArgs)
	}
}

func TestTrimAvgPointsBeforeStart(t *testing.T) {
	start := time.Date(2026, 10, 19, 14, 0, 0, 0, time.UTC)
	pts := []HistoryPoint{
		{Timestamp: start.Add(-116 * time.Second), Value: 1}, // floor 对齐推到窗口前的首桶
		{Timestamp: start, Value: 2},
		{Timestamp: start.Add(173 * time.Second), Value: 3},
	}
	got := trimAvgPointsBeforeStart(pts, start)
	if len(got) != 2 || got[0].Value != 2 || got[1].Value != 3 {
		t.Fatalf("trim = %#v, want the two in-window points", got)
	}
	// 全部在窗口内: 原样返回（不复制不裁剪）。
	inWindow := []HistoryPoint{{Timestamp: start, Value: 1}}
	if out := trimAvgPointsBeforeStart(inWindow, start); len(out) != 1 {
		t.Fatalf("in-window points must be kept, got %#v", out)
	}
	// 空切片安全。
	if out := trimAvgPointsBeforeStart(nil, start); len(out) != 0 {
		t.Fatalf("nil points = %#v", out)
	}
}

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
