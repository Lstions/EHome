package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
)

// seedMergedScenario builds the §六 acceptance scenario on SQLite:
//
//   - target logical device (id T) with a living instance dev1 (edge id D1)
//   - source logical device merged into target with merge_status=done, whose
//     instance dev2 (D2) has been soft-deleted (the device that was replaced)
//   - unified_data rows covering all four shapes:
//     (a) post-merge writes: logical_device_id = T
//     (b) migrated source rows: logical_device_id = S (merge 搬迁产物)
//     (c) pre-backfill rows: logical_device_id NULL under device D1 (OR 兜底)
//     (d) duplicate (sensor_name, timestamp) across branches (保形去重 input)
//
// It returns the seeded router and dev1's id — queries against dev1 must see
// everything in scope and dedup must keep the newest row per
// (sensor_name, timestamp).
func seedMergedScenario(t *testing.T) (*gin.Engine, uint) {
	t.Helper()
	r, db := setupTestRouter(t)

	target := models.LogicalDevice{IdentityKey: "bms:0x76", Name: "BMS", DeviceType: "bms"}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	source := models.LogicalDevice{IdentityKey: "bms:0x77", Name: "BMS-old", DeviceType: "bms",
		MergedInto: &target.ID, MergeStatus: strPtrAPI(models.MergeStatusDone)}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}

	db.Create(&models.Node{NodeID: "N-E2E", Name: "e2e", Status: "online"})
	db.Create(&models.Channel{NodeID: "N-E2E", HardwareType: "I2C", BusType: "I2C", Enabled: true})

	dev1 := models.EdgeDevice{Name: "BMS-1", Type: "bms", NodeID: "N-E2E", ChannelID: 1,
		HardwareID: "0x76", LogicalDeviceID: &target.ID}
	if err := db.Create(&dev1).Error; err != nil {
		t.Fatal(err)
	}
	dev2 := models.EdgeDevice{Name: "BMS-old", Type: "bms", NodeID: "N-E2E", ChannelID: 1,
		HardwareID: "0x77", LogicalDeviceID: &source.ID}
	if err := db.Create(&dev2).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&dev2).Error; err != nil { // 实例已删 — 数据保留
		t.Fatal(err)
	}

	t0 := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)   // pre-merge history (source)
	t1 := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)   // post-merge (target)
	tOut := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC) // outside query windows

	// merge_status=done ⇒ the §4.3 任务 3 搬迁 has already moved every source
	// row to logical_device_id = target; device_id lineage still points at
	// the soft-deleted source instance.
	rows := []models.UnifiedData{
		// (b) migrated source rows — now under the target logical id
		{DeviceID: dev2.ID, LogicalDeviceID: &target.ID, SensorName: "voltage", Unit: "V", Value: 47.1, Timestamp: t0},
		// (a) post-merge rows — logical_device_id = target
		{DeviceID: dev1.ID, LogicalDeviceID: &target.ID, SensorName: "voltage", Unit: "V", Value: 48.2, Timestamp: t1},
		// (c) pre-backfill NULL rows under dev1 (OR fallback must surface them)
		{DeviceID: dev1.ID, SensorName: "current", Unit: "A", Value: 3.3, Timestamp: t1},
		// (d) duplicate (sensor_name, timestamp) across branches — the
		// migrated source row (lower id) vs the post-merge row (higher id);
		// dedup must keep the newer one.
		{DeviceID: dev2.ID, LogicalDeviceID: &target.ID, SensorName: "soc", Unit: "%", Value: 55.0, Timestamp: t1},
		{DeviceID: dev1.ID, LogicalDeviceID: &target.ID, SensorName: "soc", Unit: "%", Value: 56.0, Timestamp: t1},
		// outside-window rows on both branches — time filters must exclude them
		{DeviceID: dev2.ID, LogicalDeviceID: &target.ID, SensorName: "voltage", Unit: "V", Value: 1.0, Timestamp: tOut},
		{DeviceID: dev1.ID, SensorName: "current", Unit: "A", Value: 2.0, Timestamp: tOut},
		// noise from an unrelated device — must never leak into the scope
		{DeviceID: 999, SensorName: "voltage", Unit: "V", Value: -1, Timestamp: t1},
	}
	for i := range rows {
		if err := db.Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	return r, dev1.ID
}

func strPtrAPI(s string) *string { return &s }

// TestQueryProtocol_E2E_MergedSourcesVisibleAndDeduped is the §六 SQLite
// end-to-end acceptance: 造两源数据合并后查询 — 全量历史可见 (migrated +
// post-merge + NULL-logical pre-backfill rows), 保形去重 keeps the newest
// row per (sensor_name, timestamp), and unrelated devices never leak in.
func TestQueryProtocol_E2E_MergedSourcesVisibleAndDeduped(t *testing.T) {
	r, dev1ID := seedMergedScenario(t)

	// since=2026-07-01 scopes to July rows on every branch.
	req := httptest.NewRequest("GET", fmt.Sprintf(
		"/api/v1/devices/%d/sensor-data?limit=100&since=%s",
		dev1ID, url.QueryEscape("2026-07-01T00:00:00Z")), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var data []models.UnifiedData
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}

	// Expected survivors: voltage@t0 (source/migrated), voltage@t1 (target),
	// current@t1 (NULL-logical fallback), soc@t1 deduped to the newest. The
	// duplicate soc@55 (lower id), June rows and device-999 noise are out.
	if len(data) != 4 {
		t.Fatalf("sensor-data returned %d rows, want 4: %#v", len(data), data)
	}
	for _, row := range data {
		if row.SensorName == "soc" && row.Value != 56.0 {
			t.Errorf("dedup kept wrong soc row: value=%v, want newest 56.0", row.Value)
		}
		if row.DeviceID == 999 {
			t.Error("unrelated device row leaked into scope")
		}
	}
}

func TestQueryProtocol_E2E_HistoryAppliesTimeFilterToWholeScope(t *testing.T) {
	r, dev1ID := seedMergedScenario(t)

	// Window [2026-07-01, 2026-07-03): excludes the June rows on BOTH
	// branches (logical AND NULL-logical) — the precedence regression guard.
	req := httptest.NewRequest("GET", fmt.Sprintf(
		"/api/v1/devices/%d/history?start_time=%s&end_time=%s",
		dev1ID, url.QueryEscape("2026-07-01T00:00:00Z"), url.QueryEscape("2026-07-03T00:00:00Z")), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Data []models.UnifiedData `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 4 {
		t.Fatalf("history returned %d rows, want 4 (time filter must apply to the whole OR scope): %#v", len(body.Data), body.Data)
	}
}

func TestQueryProtocol_E2E_CategoriesUnionAcrossMergedSources(t *testing.T) {
	r, dev1ID := seedMergedScenario(t)

	req := httptest.NewRequest("GET", fmt.Sprintf("/api/v1/unified-data/categories?device_pk=%d", dev1ID), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var cats []struct {
		Code string `json:"code"`
		Unit string `json:"unit"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &cats); err != nil {
		t.Fatal(err)
	}
	// voltage+soc come (partly) from the merged source; current from the
	// NULL-logical fallback branch — all three must be visible via dev1.
	if len(cats) != 3 {
		t.Fatalf("categories = %#v, want current+soc+voltage", cats)
	}
}

func TestQueryProtocol_E2E_HistoricalAndBatch(t *testing.T) {
	r, dev1ID := seedMergedScenario(t)

	// historical: dedup applies — soc at 2026-07-02 exists in both branches;
	// only the newest (56.0) survives.
	req := httptest.NewRequest("GET", fmt.Sprintf(
		"/api/v1/unified-data/historical?device_pk=%d&category=soc&start_time=%s&end_time=%s",
		dev1ID, url.QueryEscape("2026-07-01T00:00:00Z"), url.QueryEscape("2026-07-03T00:00:00Z")), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("historical: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var hist []models.UnifiedData
	if err := json.Unmarshal(w.Body.Bytes(), &hist); err != nil {
		t.Fatal(err)
	}
	if len(hist) != 1 || hist[0].Value != 56.0 {
		t.Fatalf("historical soc rows = %#v, want exactly the deduped newest row 56.0", hist)
	}

	// historical-batch: the concurrent goroutine path must return the same
	// result per category (Session isolation + shared resolved scope).
	req = httptest.NewRequest("GET", fmt.Sprintf(
		"/api/v1/unified-data/historical-batch?device_pk=%d&categories=soc,voltage&start_time=%s&end_time=%s",
		dev1ID, url.QueryEscape("2026-07-01T00:00:00Z"), url.QueryEscape("2026-07-03T00:00:00Z")), nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("historical-batch: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var batch []struct {
		Category string               `json:"category"`
		Data     []models.UnifiedData `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &batch); err != nil {
		t.Fatal(err)
	}
	if len(batch) != 2 {
		t.Fatalf("historical-batch returned %d categories, want 2", len(batch))
	}
	for _, cat := range batch {
		switch cat.Category {
		case "soc":
			if len(cat.Data) != 1 || cat.Data[0].Value != 56.0 {
				t.Errorf("batch soc = %#v, want deduped newest 56.0", cat.Data)
			}
		case "voltage":
			if len(cat.Data) != 2 {
				t.Errorf("batch voltage rows = %d, want 2 (t0 source + t1 target)", len(cat.Data))
			}
		}
	}
}

func TestQueryProtocol_E2E_InstanceScopedEndpoints(t *testing.T) {
	r, dev1ID := seedMergedScenario(t)

	// Living instance: latest-data 200 with the newest device_data row.
	// (device_data table is empty in this scenario — 200 with zero value.)
	req := httptest.NewRequest("GET", fmt.Sprintf("/api/v1/edge-devices/%d/latest-data", dev1ID), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("latest-data living instance: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest("GET", fmt.Sprintf("/api/v1/edge-devices/%d/data", dev1ID), nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("/:id/data living instance: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Soft-deleted instance (dev2): both endpoints return 404 (§十二 实例语义).
	req = httptest.NewRequest("GET", "/api/v1/edge-devices/2/latest-data", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("latest-data deleted instance: expected 404, got %d: %s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest("GET", "/api/v1/edge-devices/2/data", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("/:id/data deleted instance: expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// TestQueryProtocol_E2E_NoMergeHistoryKeepsRawShape pins the 前端零改动
// guarantee through the endpoint: an instance whose logical device has NO
// done incoming merge returns rows unmodified (dedup off).
func TestQueryProtocol_E2E_NoMergeHistoryKeepsRawShape(t *testing.T) {
	r, db := setupTestRouter(t)

	ld := models.LogicalDevice{IdentityKey: "sensor:0x01", Name: "solo", DeviceType: "t"}
	if err := db.Create(&ld).Error; err != nil {
		t.Fatal(err)
	}
	db.Create(&models.Node{NodeID: "N-SOLO", Name: "solo", Status: "online"})
	db.Create(&models.Channel{NodeID: "N-SOLO", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	dev := models.EdgeDevice{Name: "solo", Type: "t", NodeID: "N-SOLO", ChannelID: 1,
		HardwareID: "0x01", LogicalDeviceID: &ld.ID}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatal(err)
	}

	ts := time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC)
	// Two rows with the same (sensor, timestamp) — with dedup wrongly enabled
	// one would disappear.
	db.Create(&models.UnifiedData{DeviceID: dev.ID, LogicalDeviceID: &ld.ID, SensorName: "v", Value: 1, Timestamp: ts})
	db.Create(&models.UnifiedData{DeviceID: dev.ID, LogicalDeviceID: &ld.ID, SensorName: "v", Value: 2, Timestamp: ts})

	req := httptest.NewRequest("GET", fmt.Sprintf("/api/v1/devices/%d/sensor-data?limit=100", dev.ID), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var data []models.UnifiedData
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if len(data) != 2 {
		t.Fatalf("no-merge-history device returned %d rows, want both kept (dedup must stay off)", len(data))
	}
}
