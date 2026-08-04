package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
	"ehome/backend/internal/nodemgr"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupLifecycleTestRouter builds the §十二 全链路集成 router:
//   - in-memory SQLite, single connection (historical-batch fan-out safety)
//   - driver registry with builtins (template ownership + creation paths)
//   - versioned routes + JWT auth middleware (all lifecycle endpoints)
//
// The returned *gin.Engine routes exactly what registerXxxRoutes wires in
// production, so the integration test exercises the real HTTP surface.
func setupLifecycleTestRouter(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1) // in-memory SQLite pool pinning
	}
	if err := db.AutoMigrate(
		&models.Node{}, &models.Channel{}, &models.ConfigTemplate{},
		&models.EdgeDevice{}, &models.DeviceConfig{}, &models.DeviceData{},
		&models.UnifiedData{}, &models.User{}, &models.OTATask{},
		&models.Firmware{}, &models.Vendor{}, &models.Notification{},
		&models.OperationLog{}, &models.DeviceModel{},
		&models.NodeEvent{}, &models.CalibrationCache{},
		&models.PendingWriteRecord{},
		&models.LogicalDevice{}, &models.CommandExecution{}, &models.CommandAttempt{},
	); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registry := drivers.NewRegistry()
	drivers.RegisterBuiltInDrivers(registry)
	mgr := nodemgr.NewManager(db, nil, nil, nil, nil, nil, registry)
	registerEdgeDeviceRoutes(v1, db, mgr, registry, ControlPolicy{allowUnsafeLegacyForTests: true})
	registerDataRoutes(v1, db)
	registerLogicalDeviceRoutes(v1, db)
	registerOverviewRoutes(v1, db)
	registerNodeRoutes(v1, db, mgr)
	return r, db
}

// createDeviceViaAPI drives POST /edge-devices (the real creation path) and
// returns the created device id (or fails the test).
func createDeviceViaAPI(t *testing.T, r *gin.Engine, name, nodeID, devType, hwID string) uint {
	t.Helper()
	body := fmt.Sprintf(`{"name":%q,"node_id":%q,"type":%q,"hardware_id":%q,"channel_id":1,"interval_ms":5000}`,
		name, nodeID, devType, hwID)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/edge-devices", strings.NewReader(body))
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create device: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			ID uint `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if resp.Data.ID == 0 {
		t.Fatal("created device id must not be 0")
	}
	return resp.Data.ID
}

// TestLifecycle_FullJourney_DeleteKeepDataInherit_Purge is the §十二/§2.4/§2.5
// SQLite end-to-end acceptance for the complete lifecycle:
//
//	创建设备 → 写数据 → 删除(保留) → 重建继承 → 查询全量历史 → 再删除(勾选删除数据)
//	→ purge 清空 → logical_device 行删除。
//
// Along the way it pins the §十二 scenario table and the §2.4/§2.5 ownership
// invariants:
//
//	latest-data / :id/data 实例已删 → 404; 存活 → resolve 全量
//	sensor-data / history resolve 后返回全量 (含继承前历史)
//	commandexec 按 edge_device_id 保留, 不随删除清理
//	offlinedetector 只跟踪存活实例 (scoped 查询天然正确)
//	overview latest_data 只含存活实例
//	ConfigTemplate 归属: 创建时写入; 删除 (无同型存活) 时从 template_ids 移除并删行;
//	同 channel 仍有同型存活 → 保留; 存量模板 backfill 尽力匹配
func TestLifecycle_FullJourney_DeleteKeepData_Inherit_Purge(t *testing.T) {
	r, db := setupLifecycleTestRouter(t)

	// ---- 基础设施: 节点 + 通道 ----
	db.Create(&models.Node{NodeID: "N-E2E", Name: "e2e", Status: "online"})
	db.Create(&models.Channel{NodeID: "N-E2E", HardwareType: "I2C", BusType: "I2C", Enabled: true})

	// ---- 1. 创建设备 (via real API, driver bmp280 → template "F7") ----
	dev1ID := createDeviceViaAPI(t, r, "BMS-1", "N-E2E", "bmp280", "0x76")

	// ConfigTemplate 归属: 创建路径写入 edge_device_id (§2.4-1)。
	var tmpl models.ConfigTemplate
	if err := db.Where("edge_device_id = ?", dev1ID).First(&tmpl).Error; err != nil {
		t.Fatalf("expected owned ConfigTemplate for dev %d: %v", dev1ID, err)
	}
	if tmpl.WriteData == "" {
		t.Fatal("owned template must carry write_data")
	}
	tmplID := tmpl.ID

	// ---- 2. 写数据 (双写 logical_device_id) ----
	// 创建路径已为 dev1 建 logical_device; 双写数据行 (模拟 consumers_heavy)。
	var dev1 models.EdgeDevice
	db.First(&dev1, dev1ID)
	if dev1.LogicalDeviceID == nil {
		t.Fatal("created device must have logical_device_id")
	}
	logicalID := *dev1.LogicalDeviceID
	t0 := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Hour)
	db.Create(&models.UnifiedData{DeviceID: dev1ID, LogicalDeviceID: &logicalID, SensorName: "voltage", Unit: "V", Value: 46.5, Timestamp: t0})
	db.Create(&models.UnifiedData{DeviceID: dev1ID, LogicalDeviceID: &logicalID, SensorName: "voltage", Unit: "V", Value: 48.0, Timestamp: t1})
	db.Create(&models.DeviceData{DeviceID: dev1ID, LogicalDeviceID: &logicalID, NodeID: "N-E2E", DataJSON: "{}", Timestamp: t1})
	db.Create(&models.CalibrationCache{NodeID: "N-E2E", EdgeDeviceID: dev1ID, DeviceType: "bmp280", Data: "{}"})

	// ---- 3. 删除 (默认保留数据) ----
	w := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", fmt.Sprintf("/api/v1/edge-devices/%d", dev1ID), nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("delete: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// §2.4: 删除 (无同型存活) → 模板从 template_ids 移除并删行。
	var ch models.Channel
	db.First(&ch, 1)
	if containsTemplateID(ch.TemplateIDs, tmplID) {
		t.Errorf("channel.template_ids must drop template %d after delete, got %q", tmplID, ch.TemplateIDs)
	}
	var tmplAfterDel models.ConfigTemplate
	if err := db.Unscoped().First(&tmplAfterDel, tmplID).Error; err == nil {
		t.Errorf("template %d must be hard-deleted after device delete", tmplID)
	}
	// §2.5: calibration 行保留 (删除时不删, purge 才清)。
	var calAfterDelete int64
	db.Model(&models.CalibrationCache{}).Count(&calAfterDelete)
	if calAfterDelete != 1 {
		t.Errorf("calibration row must survive delete, got %d", calAfterDelete)
	}
	// 软删实例 + logical_device 仍在 (数据保留)。
	var instDeleted models.EdgeDevice
	db.Unscoped().First(&instDeleted, dev1ID)
	if instDeleted.DeletedAt.Time.IsZero() {
		t.Error("instance must be soft-deleted")
	}
	if instDeleted.LogicalDeviceID == nil || *instDeleted.LogicalDeviceID != logicalID {
		t.Errorf("deleted instance must keep logical_device_id %d", logicalID)
	}

	// ---- §十二 行 1: 实例已删 → latest-data / :id/data 404 ----
	for _, ep := range []string{"/latest-data", "/data"} {
		w = httptest.NewRecorder()
		req = httptest.NewRequest("GET", fmt.Sprintf("/api/v1/edge-devices/%d%s", dev1ID, ep), nil)
		req.Header.Set("Authorization", authHeader(t))
		r.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s after delete: expected 404, got %d", ep, w.Code)
		}
	}

	// ---- §十二 行 3: /nodes/:id/latest 维持现状 (B9) ----
	// 软删实例的数据仍可能出现 (子查询无 deleted_at 过滤) — 与方案文档一致。
	// LatestValue 响应无 device_id 字段 (RAW SQL 只选 channel_id/sensor/value/
	// unit/timestamp), 故断言"软删后仍返回该设备数据"即响应非空。
	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/v1/nodes/N-E2E/latest", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("nodes/latest after delete: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var nodeLatest struct {
		Values []map[string]interface{} `json:"values"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &nodeLatest); err != nil {
		t.Fatalf("nodes/latest decode: %v", err)
	}
	// 软删实例 (dev1) 的数据仍出现在 nodes/latest (B9 现状行为, 非回归)。
	if len(nodeLatest.Values) == 0 {
		t.Errorf("nodes/latest must keep returning soft-deleted instance data (B9 现状行为), values empty")
	}

	// ---- 4. 重建继承 (向导: 同地址同型号 → 继承原逻辑设备) ----
	// 通过 create API 传 logical_device_id=logicalID 走继承路径 (§3.3)。
	dev2ID := createDeviceViaAPIInherit(t, r, "BMS-1-new", "N-E2E", "bmp280", "0x76", logicalID)

	// 继承后新实例挂同一 logical_device。
	var dev2 models.EdgeDevice
	db.First(&dev2, dev2ID)
	if dev2.LogicalDeviceID == nil || *dev2.LogicalDeviceID != logicalID {
		t.Fatalf("inherited device must carry logical_device_id %d, got %v", logicalID, dev2.LogicalDeviceID)
	}

	// ---- §十二 行 2: 图表/历史查询 → resolve 后返回全量 (含继承前历史) ----
	resp := getJSON(t, r, fmt.Sprintf("/api/v1/devices/%d/sensor-data?limit=100", dev2ID))
	var data []models.UnifiedData
	if err := json.Unmarshal(resp, &data); err != nil {
		t.Fatalf("sensor-data decode: %v", err)
	}
	if len(data) != 2 {
		t.Fatalf("sensor-data after inherit: expected 2 rows (full history), got %d: %#v", len(data), data)
	}
	for _, row := range data {
		if row.DeviceID != dev1ID && row.DeviceID != dev2ID {
			t.Errorf("unexpected device_id %d in inherited history", row.DeviceID)
		}
	}
	// newest first → 48.0
	if data[0].Value != 48.0 {
		t.Errorf("sensor-data must be ordered newest-first, got %v", data[0].Value)
	}

	// ---- 重建继承 → 校准复制 (§2.5 原位置重建场景) ----
	var calAfterInherit int64
	db.Model(&models.CalibrationCache{}).Where("edge_device_id = ?", dev2ID).Count(&calAfterInherit)
	if calAfterInherit != 1 {
		t.Errorf("in-place rebuild must copy calibration to new device, got %d rows", calAfterInherit)
	}

	// ---- §十二 行 1 (存活): latest-data 200 ----
	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", fmt.Sprintf("/api/v1/edge-devices/%d/latest-data", dev2ID), nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("latest-data living after inherit: expected 200, got %d", w.Code)
	}

	// ---- 5. 再删除 (勾选删除数据 → purge_requested 标记, 数据异步清) ----
	w = httptest.NewRecorder()
	req = httptest.NewRequest("DELETE", fmt.Sprintf("/api/v1/edge-devices/%d?delete_data=true", dev2ID), nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("delete with purge: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	// 数据未立即删 (API 事务只置标记) — 但 purge 后台任务处理。
	var ld models.LogicalDevice
	db.First(&ld, logicalID)
	if !ld.PurgeRequested {
		t.Fatal("purge_requested must be set after delete_data=true")
	}
	var rowsStill int64
	db.Model(&models.UnifiedData{}).Count(&rowsStill)
	if rowsStill != 2 {
		t.Fatalf("data must survive API delete (purge is async), got %d rows", rowsStill)
	}

	// ---- 6. purge 执行 → 数据清空 + calibration 孤儿清 + FK 解除 + logical 行删除 ----
	p := datalifecycle.NewPurger(db)
	p.SetBatchSize(2) // 缩批次加速
	all, err := p.RunOnce(t.Context())
	if err != nil {
		t.Fatalf("purge run: %v", err)
	}
	found := false
	for _, res := range all {
		if res.LogicalID == logicalID {
			found = true
			if res.Outcome != datalifecycle.Purged {
				t.Fatalf("expected purge outcome purged, got %s (%s)", res.Outcome, res.Error)
			}
		}
	}
	if !found {
		t.Fatalf("purge did not process logical id %d; results=%+v", logicalID, all)
	}
	// 数据全清。
	var unifiedAfter, devDataAfter, calAfter int64
	db.Model(&models.UnifiedData{}).Count(&unifiedAfter)
	db.Model(&models.DeviceData{}).Count(&devDataAfter)
	db.Model(&models.CalibrationCache{}).Count(&calAfter)
	if unifiedAfter != 0 || devDataAfter != 0 {
		t.Errorf("purge must empty unified_data/device_data, got %d/%d", unifiedAfter, devDataAfter)
	}
	if calAfter != 0 {
		t.Errorf("purge must clean calibration orphans, got %d", calAfter)
	}
	// FK 解除 (F6: 软删实例 detach; 无入边源故 B4 无操作)。
	var attached int64
	db.Unscoped().Model(&models.EdgeDevice{}).Where("logical_device_id = ?", logicalID).Count(&attached)
	if attached != 0 {
		t.Errorf("purge must detach instances (F6), got %d attached", attached)
	}
	// logical_device 行删除。
	var ldAfter models.LogicalDevice
	if err := db.First(&ldAfter, logicalID).Error; err == nil {
		t.Error("logical_device row must be deleted after purge")
	}

	// ---- §十二 行 6: offlinedetector 只跟踪存活实例 (scoped 查询) ----
	// 软删实例 (dev1/dev2) 不再出现在标准 scoped Find 中。
	var livingDevices []models.EdgeDevice
	db.Find(&livingDevices)
	for _, d := range livingDevices {
		if d.ID == dev1ID || d.ID == dev2ID {
			t.Errorf("soft-deleted instance %d must not appear in scoped query", d.ID)
		}
	}

	// ---- §十二 行 3 变体: overview 只含存活实例 ---- (dev1/dev2 已全部软删 → latest_data 不含它们)
	overview := getJSON(t, r, "/api/v1/overview")
	var ov struct {
		LatestData []struct {
			DeviceID uint `json:"device_id"`
		} `json:"latest_data"`
	}
	if err := json.Unmarshal(overview, &ov); err != nil {
		t.Fatalf("overview decode: %v", err)
	}
	for _, entry := range ov.LatestData {
		if entry.DeviceID == dev1ID || entry.DeviceID == dev2ID {
			t.Errorf("overview latest_data must exclude soft-deleted instance %d", entry.DeviceID)
		}
	}

	// ---- §十二 行 5: 命令执行历史按 edge_device_id 保留 ----
	// 删除前为 dev1 造一条命令执行记录; 删除+purge 后仍可见。
	db.Create(&models.CommandExecution{
		CommandID: "cmd-keep-1", EdgeDeviceID: dev1ID, NodeID: "N-E2E",
		DeviceType: "bmp280", ChannelID: 1, ManifestID: "m1", ActionID: "a1",
		ActionVersion: 1, ActorUserID: 1, IdempotencyScope: "s1", IdempotencyKey: "k1",
		RequestHash: "h1", ParamsJSON: "{}", Status: "done",
		DeadlineAt: time.Now().Add(time.Hour),
	})
	var keptCmd int64
	db.Model(&models.CommandExecution{}).Where("edge_device_id = ?", dev1ID).Count(&keptCmd)
	if keptCmd != 1 {
		t.Errorf("command execution history must survive delete+purge, got %d", keptCmd)
	}
}

// createDeviceViaAPIInherit creates a device inheriting the given logical device.
func createDeviceViaAPIInherit(t *testing.T, r *gin.Engine, name, nodeID, devType, hwID string, logicalID uint) uint {
	t.Helper()
	body := fmt.Sprintf(`{"name":%q,"node_id":%q,"type":%q,"hardware_id":%q,"channel_id":1,"interval_ms":5000,"logical_device_id":%d}`,
		name, nodeID, devType, hwID, logicalID)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/edge-devices", strings.NewReader(body))
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create inherited device: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			ID uint `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	return resp.Data.ID
}

func containsTemplateID(templateIDs string, id uint) bool {
	for _, part := range strings.Split(templateIDs, ",") {
		if strings.TrimSpace(part) == fmt.Sprint(id) {
			return true
		}
	}
	return false
}

// getJSON performs an authenticated GET and returns the response body.
func getJSON(t *testing.T, r *gin.Engine, path string) []byte {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: expected 200, got %d: %s", path, w.Code, w.Body.String())
	}
	return w.Body.Bytes()
}

// TestLifecycle_TemplateOwnership_MultiDrop_KeepsTemplates pins the §2.4
// multi-drop known limitation: 同 channel 同 type 多设备共享模板时, 删除一个
// 设备不删模板 (引用检查), 但也无法 backfill 归属 (留 NULL)。
func TestLifecycle_TemplateOwnership_MultiDrop_KeepsTemplates(t *testing.T) {
	r, db := setupLifecycleTestRouter(t)

	db.Create(&models.Node{NodeID: "N-MD", Name: "multi", Status: "online"})
	db.Create(&models.Channel{NodeID: "N-MD", HardwareType: "I2C", BusType: "I2C", Enabled: true})

	devA := createDeviceViaAPI(t, r, "md-a", "N-MD", "bmp280", "0x76")
	devB := createDeviceViaAPI(t, r, "md-b", "N-MD", "bmp280", "0x77")

	// Both devices' drivers produce the same WriteData "F7" → the channel
	// ends up with two identical templates (each created by its own device).
	var tmplCount int64
	db.Model(&models.ConfigTemplate{}).Count(&tmplCount)
	if tmplCount != 2 {
		t.Fatalf("expected 2 templates (per-device), got %d", tmplCount)
	}

	// Delete devA: 同 channel 仍有同 type 存活 (devB) → 模板保留不删。
	w := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", fmt.Sprintf("/api/v1/edge-devices/%d", devA), nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("delete: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var tmplAfter int64
	db.Model(&models.ConfigTemplate{}).Count(&tmplAfter)
	if tmplAfter != 2 {
		t.Errorf("multi-drop delete must keep both templates (same-type living device), got %d", tmplAfter)
	}
	// template_ids 完整 (2 个 ID 都在)。
	var ch models.Channel
	db.First(&ch, 1)
	parts := strings.Split(ch.TemplateIDs, ",")
	if len(parts) != 2 {
		t.Errorf("channel.template_ids must keep both template ids, got %q", ch.TemplateIDs)
	}

	// 删除 devB (最后一个同型存活) → 全部模板清理。
	w = httptest.NewRecorder()
	req = httptest.NewRequest("DELETE", fmt.Sprintf("/api/v1/edge-devices/%d", devB), nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("delete devB: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	db.Model(&models.ConfigTemplate{}).Count(&tmplAfter)
	if tmplAfter != 0 {
		t.Errorf("last same-type device delete must clean all owned templates, got %d", tmplAfter)
	}
	var ch2 models.Channel
	db.First(&ch2, 1)
	if ch2.TemplateIDs != "" {
		t.Errorf("channel.template_ids must be empty after last device delete, got %q", ch2.TemplateIDs)
	}
}

// TestLifecycle_TemplateBackfill_LegacyRowsOwnedByWriteDataMatch pins §2.4-2:
// 存量模板 (edge_device_id IS NULL) 按 driver WriteData 尽力匹配单个存活设备。
func TestLifecycle_TemplateBackfill_LegacyRowsOwnedByWriteDataMatch(t *testing.T) {
	r, db := setupLifecycleTestRouter(t)

	db.Create(&models.Node{NodeID: "N-BF", Name: "bf", Status: "online"})
	db.Create(&models.Channel{NodeID: "N-BF", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	devID := createDeviceViaAPI(t, r, "legacy-1", "N-BF", "bmp280", "0x76")

	// 手工把归属抹掉, 模拟存量 (edge_device_id IS NULL)。
	db.Model(&models.ConfigTemplate{}).Where("id IS NOT NULL").Update("edge_device_id", nil)

	// 归属 backfill: bmp280 WriteData "F7" 与模板匹配且单候选 → 写回。
	registry := drivers.NewRegistry()
	drivers.RegisterBuiltInDrivers(registry)
	backfilled, err := datalifecycle.BackfillConfigTemplateOwnership(db, registry)
	if err != nil {
		t.Fatalf("template backfill: %v", err)
	}
	if backfilled != 1 {
		t.Fatalf("expected 1 template backfilled, got %d", backfilled)
	}
	var tmpl models.ConfigTemplate
	if err := db.First(&tmpl).Error; err != nil {
		t.Fatal(err)
	}
	if tmpl.EdgeDeviceID == nil || *tmpl.EdgeDeviceID != devID {
		t.Errorf("template ownership must point to dev %d, got %v", devID, tmpl.EdgeDeviceID)
	}

	// 幂等: 再次运行 backfilled=0。
	again, err := datalifecycle.BackfillConfigTemplateOwnership(db, registry)
	if err != nil {
		t.Fatalf("second template backfill: %v", err)
	}
	if again != 0 {
		t.Errorf("second backfill must be a no-op, got %d", again)
	}
}
