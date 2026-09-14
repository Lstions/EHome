package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"ehome/backend/internal/models"
	"ehome/backend/internal/nodemgr"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupDriverCommandsTest creates a test router with DB and driver-command
// routes, backed by the fake_multi driver (read_a/read_b schedulable,
// one_shot non-schedulable). Skeleton mirrors setupEdgeDeviceTest
// (handler_edge_device_crud_test.go:26-51) but registers only the fake
// driver — see 演进方案 §5.5 (never use techfine as a fixture here).
func setupDriverCommandsTest(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.AutoMigrate(
		&models.Node{}, &models.Channel{}, &models.ConfigTemplate{},
		&models.EdgeDevice{}, &models.DeviceConfig{}, &models.DeviceData{},
		&models.UnifiedData{}, &models.User{}, &models.OTATask{},
		&models.Firmware{}, &models.Vendor{}, &models.Notification{},
		&models.DeviceModel{},
		&models.NodeEvent{}, &models.CalibrationCache{},
		&models.PendingWriteRecord{},
		&models.LogicalDevice{},
	)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registry := newFakeRegistry()
	mgr := nodemgr.NewManager(db, nil, nil, nil, nil, nil, registry)
	registerDriverCommandRoutes(v1, db, mgr, registry)
	return r, db
}

// createFakeMultiDevice inserts one edge device of type fake_multi with the
// given stored command_intervals (nil = unset).
func createFakeMultiDevice(t *testing.T, db *gorm.DB, stored map[string]int) *models.EdgeDevice {
	t.Helper()
	var raw json.RawMessage
	if stored != nil {
		b, err := json.Marshal(stored)
		if err != nil {
			t.Fatalf("marshal stored intervals: %v", err)
		}
		raw = b
	}
	dev := models.EdgeDevice{Name: "D", NodeID: "NODE001", ChannelID: 1, Type: "fake_multi", CommandIntervals: raw}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatalf("create edge device: %v", err)
	}
	return &dev
}

func putCommands(t *testing.T, r *gin.Engine, devID int, intervals map[string]int) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]interface{}{"intervals": intervals})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/edge-devices/"+itoa(devID)+"/commands", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	return w
}

func reloadIntervals(t *testing.T, db *gorm.DB, devID uint) map[string]int {
	t.Helper()
	var dev models.EdgeDevice
	if err := db.First(&dev, devID).Error; err != nil {
		t.Fatalf("reload device %d: %v", devID, err)
	}
	return parseCommandIntervals(dev.CommandIntervals)
}

func itoa(v int) string { return strconv.Itoa(v) }

// ==================== C2: PUT validation ====================

func TestPutCommands_RejectsNonSchedulableId(t *testing.T) {
	r, db := setupDriverCommandsTest(t)
	dev := createFakeMultiDevice(t, db, map[string]int{"read_a": 5000})

	w := putCommands(t, r, int(dev.ID), map[string]int{"one_shot": 1000})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	stored := reloadIntervals(t, db, dev.ID)
	if _, ok := stored["one_shot"]; ok {
		t.Fatalf("one_shot leaked into DB: %v", stored)
	}
	if stored["read_a"] != 5000 {
		t.Fatalf("DB changed on rejected PUT: %v", stored)
	}
}

func TestPutCommands_RejectsUnknownId(t *testing.T) {
	r, db := setupDriverCommandsTest(t)
	dev := createFakeMultiDevice(t, db, map[string]int{"read_a": 5000})

	w := putCommands(t, r, int(dev.ID), map[string]int{"nope": 1000})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	stored := reloadIntervals(t, db, dev.ID)
	if _, ok := stored["nope"]; ok {
		t.Fatalf("unknown id leaked into DB: %v", stored)
	}
	if len(stored) != 1 || stored["read_a"] != 5000 {
		t.Fatalf("DB changed on rejected PUT: %v", stored)
	}
}

func TestPutCommands_MergesPartialUpdate(t *testing.T) {
	r, db := setupDriverCommandsTest(t)
	dev := createFakeMultiDevice(t, db, map[string]int{"read_a": 5000})

	w := putCommands(t, r, int(dev.ID), map[string]int{"read_b": 3000})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	stored := reloadIntervals(t, db, dev.ID)
	if len(stored) != 2 || stored["read_a"] != 5000 || stored["read_b"] != 3000 {
		t.Fatalf("merged map mismatch: %v", stored)
	}
}

func TestPutCommands_NormalizesNegative(t *testing.T) {
	r, db := setupDriverCommandsTest(t)
	dev := createFakeMultiDevice(t, db, nil)

	w := putCommands(t, r, int(dev.ID), map[string]int{"read_a": -5})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	stored := reloadIntervals(t, db, dev.ID)
	if stored["read_a"] != 0 {
		t.Fatalf("negative not normalized to 0: %v", stored)
	}
}

func TestPutCommands_RejectsLegacyDirtyStoredKey(t *testing.T) {
	r, db := setupDriverCommandsTest(t)
	// Simulate pre-cleanup legacy data: a non-schedulable key already stored.
	dev := createFakeMultiDevice(t, db, map[string]int{"one_shot": 1000})

	// PUT is validated on the MERGED map, so the stored dirty key rejects an
	// otherwise-legal request — locking the "cleanup must precede validation"
	// deployment order (演进方案 C2 §部署顺序).
	w := putCommands(t, r, int(dev.ID), map[string]int{"read_a": 5000})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for merged dirty key, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPutCommands_DriverNotFound(t *testing.T) {
	r, db := setupDriverCommandsTest(t)
	dev := models.EdgeDevice{Name: "G", NodeID: "NODE001", ChannelID: 1, Type: "ghost_type"}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatalf("create edge device: %v", err)
	}

	w := putCommands(t, r, int(dev.ID), map[string]int{"x": 1})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// ==================== C3: GET schedulable filtering ====================

type commandIDView struct {
	ID                string `json:"id"`
	CurrentIntervalMs int    `json:"current_interval_ms"`
}

func getCommandJSON(t *testing.T, r *gin.Engine, path string) (int, []byte) {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	return w.Code, w.Body.Bytes()
}

func commandIDs(t *testing.T, body []byte, wantSchedulableFlag bool) map[string]struct{} {
	t.Helper()
	var resp struct {
		Code int `json:"code"`
		Data []struct {
			ID          string `json:"id"`
			Schedulable bool   `json:"schedulable"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode response: %v (%s)", err, body)
	}
	ids := map[string]struct{}{}
	for _, c := range resp.Data {
		if wantSchedulableFlag && !c.Schedulable {
			t.Fatalf("non-schedulable template %q leaked into GET response", c.ID)
		}
		ids[c.ID] = struct{}{}
	}
	return ids
}

func TestGetDriverCommands_FiltersNonSchedulable(t *testing.T) {
	r, _ := setupDriverCommandsTest(t)
	code, body := getCommandJSON(t, r, "/api/v1/drivers/fake_multi/commands")
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", code, body)
	}
	ids := commandIDs(t, body, true)
	assertStringSet(t, "GET /drivers/fake_multi/commands", ids, "read_a", "read_b")
	if _, ok := ids["one_shot"]; ok {
		t.Fatal("one_shot leaked into GET /drivers/:type/commands")
	}
}

func TestGetEdgeDeviceCommands_FiltersNonSchedulable(t *testing.T) {
	r, db := setupDriverCommandsTest(t)
	dev := createFakeMultiDevice(t, db, map[string]int{"read_a": 3000})
	code, body := getCommandJSON(t, r, "/api/v1/edge-devices/"+itoa(int(dev.ID))+"/commands")
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", code, body)
	}
	ids := commandIDs(t, body, true)
	assertStringSet(t, "GET /edge-devices/:id/commands", ids, "read_a", "read_b")
	if _, ok := ids["one_shot"]; ok {
		t.Fatal("one_shot leaked into GET /edge-devices/:id/commands")
	}
}

func TestGetEdgeDeviceCommands_OverlayStillWorks(t *testing.T) {
	r, db := setupDriverCommandsTest(t)
	dev := createFakeMultiDevice(t, db, map[string]int{"read_a": 3000})
	code, body := getCommandJSON(t, r, "/api/v1/edge-devices/"+itoa(int(dev.ID))+"/commands")
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", code, body)
	}
	var resp struct {
		Data []commandIDView `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	byID := map[string]commandIDView{}
	for _, v := range resp.Data {
		byID[v.ID] = v
	}
	if got := byID["read_a"].CurrentIntervalMs; got != 3000 {
		t.Fatalf("read_a overlay: got %d, want 3000", got)
	}
	// read_b has no stored interval; because the stored map is non-empty the
	// device-default branch is skipped → template default IntervalMs (0) wins.
	if got := byID["read_b"].CurrentIntervalMs; got != 0 {
		t.Fatalf("read_b overlay: got %d, want 0 (template default)", got)
	}
}

func TestGetDriverCommands_UnknownDriver404(t *testing.T) {
	r, _ := setupDriverCommandsTest(t)
	code, _ := getCommandJSON(t, r, "/api/v1/drivers/nope/commands")
	if code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown driver, got %d", code)
	}
}
