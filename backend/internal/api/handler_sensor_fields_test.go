package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/sensorfield"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ==================== 数据字段解析层 API 契约测试 (方案 §8.1–§8.4) ====================

// newSensorFieldsAPI 用真实 SQLite 库 + 真实 Registry 构造四条 /sensor-fields 路由。
// setupTestDB/setupRouter/authHeader/JWTAuth 来自 handler_test.go（本包既有夹具）。
func newSensorFieldsAPI(t *testing.T, enabled bool) (*gin.Engine, *gorm.DB, *sensorfield.Registry) {
	t.Helper()
	db := setupTestDB(t)
	// api 包的 setupTestDB 维护一份自己的迁移清单（历史原因，非 testutil.allModels）；
	// 本域两张新表在此就地补齐，避免改共享夹具（写入范围纪律）。
	if err := db.AutoMigrate(&models.SensorFieldObs{}, &models.SensorFieldMeta{}); err != nil {
		t.Fatalf("migrate sensor field tables: %v", err)
	}
	reg := sensorfield.New(sensorfield.Config{Enabled: enabled})
	r := setupRouter()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerSensorFieldRoutes(v1, db, reg)
	return r, db, reg
}

// seedSensorFieldDevice 建一个真实 device_config + edge_device（device_id 语义 = EdgeDevice.ID）。
func seedSensorFieldDevice(t *testing.T, db *gorm.DB, deviceType string) models.EdgeDevice {
	t.Helper()
	cfg := models.DeviceConfig{Name: "测试型号", DeviceType: deviceType}
	if err := db.Create(&cfg).Error; err != nil {
		t.Fatalf("seed device config: %v", err)
	}
	dev := models.EdgeDevice{
		Name:           "字段测试设备",
		NodeID:         "TESTNODE",
		ChannelID:      1,
		DeviceConfigID: cfg.ID,
		HardwareID:     "0x01",
		IntervalMs:     5000,
		Enabled:        true,
		Status:         "active",
	}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatalf("seed edge device: %v", err)
	}
	return dev
}

// sfDo 发起带鉴权的请求（body=nil 时不带请求体）。
func sfDo(t *testing.T, r *gin.Engine, method, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", authHeader(t))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// sfDecode 断言 HTTP 状态码并解出 envelope.data。
func sfDecode(t *testing.T, w *httptest.ResponseRecorder, want int) interface{} {
	t.Helper()
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, w.Body.String())
	}
	if w.Code != want {
		t.Fatalf("http status = %d want %d (body=%s)", w.Code, want, w.Body.String())
	}
	code, _ := resp["code"].(float64)
	if int(code) != want {
		t.Fatalf("envelope code = %v want %d (body=%s)", resp["code"], want, w.Body.String())
	}
	return resp["data"]
}

// sfDataMap 把 data 断言成对象。
func sfDataMap(t *testing.T, data interface{}) map[string]interface{} {
	t.Helper()
	m, ok := data.(map[string]interface{})
	if !ok {
		t.Fatalf("data is not object: %T", data)
	}
	return m
}

// sfFields 取出 fields 数组。
func sfFields(t *testing.T, data interface{}) []map[string]interface{} {
	t.Helper()
	m := sfDataMap(t, data)
	raw, ok := m["fields"].([]interface{})
	if !ok {
		t.Fatalf("fields is not array: %T (%v)", m["fields"], m["fields"])
	}
	out := make([]map[string]interface{}, 0, len(raw))
	for _, item := range raw {
		obj, ok := item.(map[string]interface{})
		if !ok {
			t.Fatalf("field item is not object: %T", item)
		}
		out = append(out, obj)
	}
	return out
}

// sfFindField 按 name 找字段项。
func sfFindField(t *testing.T, fields []map[string]interface{}, name string) (map[string]interface{}, bool) {
	t.Helper()
	for _, f := range fields {
		if f["name"] == name {
			return f, true
		}
	}
	return nil, false
}

// sfStrings 把 JSON 数组转成 []string。
func sfStrings(t *testing.T, raw interface{}) []string {
	t.Helper()
	arr, ok := raw.([]interface{})
	if !ok {
		t.Fatalf("not array: %T", raw)
	}
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		s, _ := v.(string)
		out = append(out, s)
	}
	return out
}

// seedDeclaredAndObserved 用真实 Registry 注入声明 + 别名，并落观测/元数据/时序行。
func seedDeclaredAndObserved(t *testing.T, db *gorm.DB, reg *sensorfield.Registry, dev models.EdgeDevice) {
	t.Helper()
	reg.SetDeviceType(dev.ID, "jiabaida_bms")
	reg.RegisterDeclarations("jiabaida_bms", []sensorfield.Field{
		{Name: "rsoc", Unit: "%"},
		{Name: "current", Unit: "A"},
		{Name: "temperature_1", Unit: "°C"},
		{Name: "temperature_2", Unit: "°C"},
	})
	if err := reg.SetAliases([]sensorfield.AliasRule{
		{Semantic: "soc", Candidates: []string{"rsoc"}, ExpectedUnit: "%"},
	}); err != nil {
		t.Fatalf("set aliases: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	obs := []models.SensorFieldObs{
		{DeviceID: dev.ID, SensorName: "rsoc", Unit: "%", FirstSeen: now.Add(-2 * time.Hour), LastSeen: now, Samples: 100},
		{DeviceID: dev.ID, SensorName: "current", Unit: "A", FirstSeen: now.Add(-time.Hour), LastSeen: now, Samples: 50},
	}
	if err := db.Create(&obs).Error; err != nil {
		t.Fatalf("seed obs: %v", err)
	}
	precision := 1
	metas := []models.SensorFieldMeta{
		{SensorName: "rsoc", LabelZh: "SOC", Unit: "%", ValueType: "number", Precision: &precision, Status: "active"},
		{SensorName: "current", LabelZh: "电流", Unit: "A", ValueType: "number", Status: "deprecated"},
	}
	if err := db.Create(&metas).Error; err != nil {
		t.Fatalf("seed meta: %v", err)
	}
	for i := 0; i < 3; i++ {
		ts := now.Add(time.Duration(i) * 10 * time.Second)
		rows := []models.UnifiedData{
			{DeviceID: dev.ID, SensorName: "temperature_1", Value: float64(21 + i), Unit: "°C", Timestamp: ts},
			{DeviceID: dev.ID, SensorName: "temperature_2", Value: float64(22 + i), Unit: "°C", Timestamp: ts},
		}
		if err := db.Create(&rows).Error; err != nil {
			t.Fatalf("seed unified_data: %v", err)
		}
	}
}

// TestSensorFieldsList_Contract §8.1：声明∪观测、三源合并、semantics 数组、deprecated 过滤。
func TestSensorFieldsList_Contract(t *testing.T) {
	r, db, reg := newSensorFieldsAPI(t, true)
	dev := seedSensorFieldDevice(t, db, "jiabaida_bms")
	seedDeclaredAndObserved(t, db, reg, dev)

	data := sfDataMap(t, sfDecode(t, sfDo(t, r, http.MethodGet, "/api/v1/sensor-fields?edge_device_id="+uintStr(dev.ID), nil), http.StatusOK))
	if got := data["edge_device_id"].(float64); uint(got) != dev.ID {
		t.Fatalf("edge_device_id = %v want %d", got, dev.ID)
	}
	if data["device_type"] != "jiabaida_bms" {
		t.Fatalf("device_type = %v", data["device_type"])
	}
	if data["truncated"] != false {
		t.Fatalf("truncated = %v want false", data["truncated"])
	}
	fields := sfFields(t, data)

	// rsoc: 声明+观测 + meta（label/precision/value_type）+ 语义别名。
	rsoc, ok := sfFindField(t, fields, "rsoc")
	if !ok {
		t.Fatalf("rsoc missing: %v", fields)
	}
	if rsoc["label_zh"] != "SOC" {
		t.Fatalf("rsoc label_zh = %v", rsoc["label_zh"])
	}
	if rsoc["value_type"] != "number" {
		t.Fatalf("rsoc value_type = %v", rsoc["value_type"])
	}
	if rsoc["precision"].(float64) != 1 {
		t.Fatalf("rsoc precision = %v", rsoc["precision"])
	}
	if rsoc["first_seen"] == nil || rsoc["last_seen"] == nil {
		t.Fatalf("observed field must have first/last_seen: %v", rsoc)
	}
	if rsoc["samples"].(float64) != 100 {
		t.Fatalf("rsoc samples = %v want 100", rsoc["samples"])
	}
	if got := sfStrings(t, rsoc["sources"]); len(got) != 2 || got[0] != "decl" || got[1] != "obs" {
		t.Fatalf("rsoc sources = %v want [decl obs]", got)
	}
	if got := sfStrings(t, rsoc["semantics"]); len(got) != 1 || got[0] != "soc" {
		t.Fatalf("rsoc semantics = %v want [soc]", got)
	}

	// temperature_2: 仅声明（无观测）⇒ first_seen/last_seen 为 null、samples 0。
	t2, ok := sfFindField(t, fields, "temperature_2")
	if !ok {
		t.Fatalf("temperature_2 (decl-only) missing: %v", fields)
	}
	if t2["first_seen"] != nil || t2["last_seen"] != nil {
		t.Fatalf("decl-only field must have null first/last_seen: %v", t2)
	}
	if t2["samples"].(float64) != 0 {
		t.Fatalf("decl-only samples = %v want 0", t2["samples"])
	}
	if got := sfStrings(t, t2["sources"]); len(got) != 1 || got[0] != "decl" {
		t.Fatalf("temperature_2 sources = %v want [decl]", got)
	}

	// deprecated 默认过滤；include_deprecated=true 才返回且 deprecated:true。
	if _, found := sfFindField(t, fields, "current"); found {
		t.Fatalf("deprecated field current must be filtered by default")
	}
	withDeprecated := sfDataMap(t, sfDecode(t, sfDo(t, r, http.MethodGet,
		"/api/v1/sensor-fields?edge_device_id="+uintStr(dev.ID)+"&include_deprecated=true", nil), http.StatusOK))
	current, ok := sfFindField(t, sfFields(t, withDeprecated), "current")
	if !ok || current["deprecated"] != true {
		t.Fatalf("include_deprecated=true must return current with deprecated=true: %v", current)
	}

	// 参数校验：缺参 400、未知设备 404。
	sfDecode(t, sfDo(t, r, http.MethodGet, "/api/v1/sensor-fields", nil), http.StatusBadRequest)
	sfDecode(t, sfDo(t, r, http.MethodGet, "/api/v1/sensor-fields?edge_device_id=999999", nil), http.StatusNotFound)
}

// TestSensorFieldsResolve_Contract §8.2：resolved/unbound 200；语法错误 400+error_code。
func TestSensorFieldsResolve_Contract(t *testing.T) {
	r, db, reg := newSensorFieldsAPI(t, true)
	dev := seedSensorFieldDevice(t, db, "jiabaida_bms")
	seedDeclaredAndObserved(t, db, reg, dev)

	// family：matched = 已知集合里满足族文法（严格数字后缀）的成员。
	data := sfDataMap(t, sfDecode(t, sfDo(t, r, http.MethodGet,
		"/api/v1/sensor-fields/resolve?edge_device_id="+uintStr(dev.ID)+"&selector=temperature_%2A%3Amax", nil), http.StatusOK))
	if data["status"] != "resolved" || data["kind"] != "family" {
		t.Fatalf("family resolve = %v", data)
	}
	if data["aggregate"] != "max" {
		t.Fatalf("aggregate = %v want max", data["aggregate"])
	}
	matched := sfStrings(t, data["matched"])
	if len(matched) != 2 || matched[0] != "temperature_1" || matched[1] != "temperature_2" {
		t.Fatalf("matched = %v", matched)
	}

	// semantic：@soc → rsoc。
	sem := sfDataMap(t, sfDecode(t, sfDo(t, r, http.MethodGet,
		"/api/v1/sensor-fields/resolve?edge_device_id="+uintStr(dev.ID)+"&selector=%40soc", nil), http.StatusOK))
	if sem["status"] != "resolved" || sem["kind"] != "semantic" {
		t.Fatalf("semantic resolve = %v", sem)
	}

	// literal 未观测/未声明 ⇒ unbound 且不是错误。
	unbound := sfDataMap(t, sfDecode(t, sfDo(t, r, http.MethodGet,
		"/api/v1/sensor-fields/resolve?edge_device_id="+uintStr(dev.ID)+"&selector=rainfall", nil), http.StatusOK))
	if unbound["status"] != "unbound" {
		t.Fatalf("literal unknown status = %v want unbound", unbound["status"])
	}

	// 非法语法 ⇒ 400 + error_code（不允许 200 里混 status=error）。
	w := sfDo(t, r, http.MethodGet, "/api/v1/sensor-fields/resolve?edge_device_id="+uintStr(dev.ID)+"&selector=%40", nil)
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid syntax status = %d want 400 (body=%s)", w.Code, w.Body.String())
	}
	if resp["error_code"] != "invalid_selector_syntax" {
		t.Fatalf("error_code = %v want invalid_selector_syntax", resp["error_code"])
	}
}

// TestSensorFieldsPreview_Contract §8.3：分帧求值、trace/reduced/missing_known、samples 口径。
func TestSensorFieldsPreview_Contract(t *testing.T) {
	r, db, reg := newSensorFieldsAPI(t, true)
	dev := seedSensorFieldDevice(t, db, "jiabaida_bms")
	seedDeclaredAndObserved(t, db, reg, dev)

	data := sfDataMap(t, sfDecode(t, sfDo(t, r, http.MethodPost, "/api/v1/sensor-fields/preview", gin.H{
		"edge_device_id": dev.ID,
		"selector":       "temperature_*:max",
		"comparator":     "gt",
		"threshold":      20,
		"window_hours":   24,
		"duration_sec":   5,
	}), http.StatusOK))
	if data["status"] != "resolved" {
		t.Fatalf("preview status = %v", data["status"])
	}
	if data["samples_considered"].(float64) != 6 {
		t.Fatalf("samples_considered = %v want 6", data["samples_considered"])
	}
	frames, ok := data["frames"].([]interface{})
	if !ok || len(frames) != 3 {
		t.Fatalf("frames = %v want 3", data["frames"])
	}
	trace, ok := data["aggregation_trace"].([]interface{})
	if !ok || len(trace) != 2 {
		t.Fatalf("aggregation_trace = %v want 2 items (recent frame)", data["aggregation_trace"])
	}
	first := trace[0].(map[string]interface{})
	if first["field"] != "temperature_1" || first["valid"] != true || first["cmp"] != true {
		t.Fatalf("trace[0] = %v want temperature_1 valid+cmp", first)
	}
	if data["reduced"] == nil || data["reduced"].(float64) != 24 {
		t.Fatalf("reduced = %v want 24 (max of last frame: temperature_1=23, temperature_2=24)", data["reduced"])
	}
	if missing := sfStrings(t, data["missing_known"]); len(missing) != 0 {
		t.Fatalf("missing_known = %v want []", missing)
	}
	if data["duration_would_fire"] != true {
		t.Fatalf("duration_would_fire = %v want true (3 frames x10s, duration 5s)", data["duration_would_fire"])
	}
	if data["approx"] != true {
		t.Fatalf("approx = %v want true when duration_sec>0", data["approx"])
	}

	// 非法 comparator ⇒ 400；unbound selector ⇒ 200 + 空 trace（不是错误）。
	sfDecode(t, sfDo(t, r, http.MethodPost, "/api/v1/sensor-fields/preview", gin.H{
		"edge_device_id": dev.ID, "selector": "rsoc", "comparator": "bogus", "threshold": 1,
	}), http.StatusBadRequest)
	unbound := sfDataMap(t, sfDecode(t, sfDo(t, r, http.MethodPost, "/api/v1/sensor-fields/preview", gin.H{
		"edge_device_id": dev.ID, "selector": "rainfall", "comparator": "gt", "threshold": 1,
	}), http.StatusOK))
	if unbound["status"] != "unbound" {
		t.Fatalf("unbound preview status = %v", unbound["status"])
	}
}

// TestSensorFieldsSchema_Contract §8.4：词表唯一来源、形状稳定。
func TestSensorFieldsSchema_Contract(t *testing.T) {
	r, db, reg := newSensorFieldsAPI(t, true)
	dev := seedSensorFieldDevice(t, db, "jiabaida_bms")
	seedDeclaredAndObserved(t, db, reg, dev)

	data := sfDataMap(t, sfDecode(t, sfDo(t, r, http.MethodGet, "/api/v1/sensor-fields/schema", nil), http.StatusOK))
	if data["selector_version"].(float64) != 1 {
		t.Fatalf("selector_version = %v", data["selector_version"])
	}
	if data["aggregate_default"] != "any" {
		t.Fatalf("aggregate_default = %v want any", data["aggregate_default"])
	}
	aggregates := sfStrings(t, data["aggregates"])
	if len(aggregates) != 6 {
		t.Fatalf("aggregates = %v want 6", aggregates)
	}
	fns, ok := data["virtual_fns"].([]interface{})
	if !ok || len(fns) == 0 {
		t.Fatalf("virtual_fns empty: %v", data["virtual_fns"])
	}
	aliases, ok := data["aliases"].([]interface{})
	if !ok || len(aliases) != 1 {
		t.Fatalf("aliases = %v want 1", data["aliases"])
	}
	alias := aliases[0].(map[string]interface{})
	if alias["semantic"] != "soc" || alias["expected_unit"] != "%" {
		t.Fatalf("alias = %v", alias)
	}
	limits := data["limits"].(map[string]interface{})
	if limits["max_selector_chars"].(float64) != 64 || limits["max_family_size"].(float64) != 64 || limits["max_virtual_depth"].(float64) != 4 {
		t.Fatalf("limits = %v", limits)
	}
	codes, ok := data["error_codes"].([]interface{})
	if !ok || len(codes) == 0 {
		t.Fatalf("error_codes empty: %v", data["error_codes"])
	}
}

// TestSensorFieldsKillSwitch 方案 §11：关闭态下特殊 selector 400 field_resolver_disabled，
// 纯字面量行为不变；/sensor-fields 与 /schema 仍可用。
func TestSensorFieldsKillSwitch(t *testing.T) {
	r, db, reg := newSensorFieldsAPI(t, false)
	dev := seedSensorFieldDevice(t, db, "jiabaida_bms")
	seedDeclaredAndObserved(t, db, reg, dev)

	// @soc 在关闭态 ⇒ 400 + field_resolver_disabled。
	w := sfDo(t, r, http.MethodGet, "/api/v1/sensor-fields/resolve?edge_device_id="+uintStr(dev.ID)+"&selector=%40soc", nil)
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if w.Code != http.StatusBadRequest || resp["error_code"] != "field_resolver_disabled" {
		t.Fatalf("disabled special selector = %d %v want 400 field_resolver_disabled", w.Code, resp["error_code"])
	}
	// 字面量 rsoc 不受影响（已声明/观测 ⇒ resolved）。
	lit := sfDataMap(t, sfDecode(t, sfDo(t, r, http.MethodGet,
		"/api/v1/sensor-fields/resolve?edge_device_id="+uintStr(dev.ID)+"&selector=rsoc", nil), http.StatusOK))
	if lit["status"] != "resolved" {
		t.Fatalf("literal in disabled mode status = %v want resolved", lit["status"])
	}
	// 关闭态 /schema 与 /sensor-fields 仍可用（方案 §11 明确要求）。
	sfDecode(t, sfDo(t, r, http.MethodGet, "/api/v1/sensor-fields/schema", nil), http.StatusOK)
	sfDecode(t, sfDo(t, r, http.MethodGet, "/api/v1/sensor-fields?edge_device_id="+uintStr(dev.ID), nil), http.StatusOK)
}

// TestSensorFieldsRoutesUnavailable resolver 未注入 ⇒ 四条路由显式 503（不 panic 不 404）。
func TestSensorFieldsRoutesUnavailable(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerSensorFieldRoutes(v1, db, nil)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/sensor-fields"},
		{http.MethodGet, "/api/v1/sensor-fields/resolve"},
		{http.MethodPost, "/api/v1/sensor-fields/preview"},
		{http.MethodGet, "/api/v1/sensor-fields/schema"},
	} {
		sfDecode(t, sfDo(t, r, tc.method, tc.path, nil), http.StatusServiceUnavailable)
	}
}

// uintStr 把设备 ID 转路径参数（避免每处 fmt.Sprintf 引入 fmt 依赖噪声）。
func uintStr(v uint) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
