package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/nodemgr"
	"ehome/backend/internal/ota"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// =====================================================================
// Envelope contract locking (last batch of the response-shape migration)
//
// The handlers covered here previously returned bare arrays / bare objects.
// These tests pin the new contract: {code, message, data} with an array data
// for list endpoints, an object data for create/update endpoints, and the
// {@code {node_id, values}} dialect nested inside data for /nodes/:id/latest.
// =====================================================================

// envelopeData extracts the envelope "data" field into T. It is used to
// update older tests that previously decoded the bare body directly.
func envelopeData[T any](t *testing.T, body []byte) T {
	t.Helper()
	var env struct {
		Data T `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode envelope data: %v (body: %s)", err, string(body))
	}
	return env.Data
}

// requireEnvelopeOK asserts the response status and that the body is a
// {code, message, data} envelope whose numeric code matches wantStatus.
// It returns the raw "data" JSON so callers can assert its JSON kind.
func requireEnvelopeOK(t *testing.T, w *httptest.ResponseRecorder, wantStatus int) json.RawMessage {
	t.Helper()
	if w.Code != wantStatus {
		t.Fatalf("expected HTTP %d, got %d: %s", wantStatus, w.Code, w.Body.String())
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("response is not a JSON envelope object: %v (%s)", err, w.Body.String())
	}
	codeRaw, ok := env["code"]
	if !ok {
		t.Fatalf("envelope missing key %q: %s", "code", w.Body.String())
	}
	if _, ok := env["message"]; !ok {
		t.Fatalf("envelope missing key %q: %s", "message", w.Body.String())
	}
	data, ok := env["data"]
	if !ok {
		t.Fatalf("envelope missing key %q: %s", "data", w.Body.String())
	}
	var code int
	if err := json.Unmarshal(codeRaw, &code); err != nil {
		t.Fatalf("envelope code is not a number: %s", w.Body.String())
	}
	if code != wantStatus {
		t.Fatalf("envelope code = %d, want %d: %s", code, wantStatus, w.Body.String())
	}
	return data
}

func newEnvelopeContractDB(t *testing.T) *gorm.DB {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// historical-batch fans out to concurrent goroutines; in-memory SQLite
	// gives each new connection its own empty database, so pin to one conn.
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.AutoMigrate(
		&models.Node{}, &models.Channel{}, &models.ConfigTemplate{},
		&models.EdgeDevice{}, &models.DeviceConfig{},
		&models.UnifiedData{}, &models.DataSource{}, &models.User{},
		&models.OTATask{}, &models.Firmware{}, &models.Vendor{},
		&models.Notification{}, &models.DeviceModel{},
		&models.NodeEvent{}, &models.CalibrationCache{}, &models.PendingWriteRecord{},
		&models.NodeLog{}, &models.GPIOConfig{}, &models.PWMConfig{},
	); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	db.Create(&models.User{Username: "admin", PasswordHash: "$2a$10$dummy", Role: "admin", Enabled: true})
	return db
}

// setupEnvelopeContractRouter wires data + node + OTA + peripheral routes.
// Use setupEnvelopeContractOTARouter when a test actually creates an OTA task.
func setupEnvelopeContractRouter(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	db := newEnvelopeContractDB(t)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	mgr := nodemgr.NewManager(db, &envelopeDownlinkStub{}, nil, nil, nil)
	otaMgr := ota.NewManager(db, &envelopeDownlinkStub{}, nil)
	registerDataRoutes(v1, db)
	registerNodeRoutes(v1, db, mgr)
	registerPeriphRoutes(v1, db, mgr)
	registerOTARoutes(v1, db, otaMgr, mgr)
	return r, db
}

// ==================== Downlink stub ====================
//
// ⚠ This replaced a full fake MQTT broker, which spoke enough MQTT 3.1.1 to
// drive mqtt.Client to Ready and accept a QoS-1 publish. MQTT is gone from the
// backend, so there is no longer a client to drive -- and no broker to fake.
//
// The stub below is the honest replacement: these are ENVELOPE CONTRACT tests
// (status code + JSON shape), not transport tests. They need the OTA publish to
// SUCCEED so POST /ota/tasks reaches its 201 branch, and nothing more.
// A test that needed to prove delivery would use the real 3.0 transport
// (see cmd/server/device_e2e_test.go) rather than a stub.
type envelopeDownlinkStub struct {
	mu       sync.Mutex
	nodeIDs  []string
	payloads [][]byte
	// err, when set, makes every Publish fail. That is how a test expresses
	// "the node has no usable downlink" now that the removal of MQTT took away
	// the zero-value client that used to fail for free.
	err error
}

// errStubNoDownlink stands in for "the node is not connected".
var errStubNoDownlink = errors.New("no downlink session for this node")

func (d *envelopeDownlinkStub) Publish(nodeID string, payload []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.nodeIDs = append(d.nodeIDs, nodeID)
	d.payloads = append(d.payloads, append([]byte(nil), payload...))
	return d.err
}

// setupEnvelopeContractOTARouter wires OTA routes with a recording downlink so
// POST /ota/tasks reaches its 201 success branch.
//
// ⚠ It used to stand up a live in-process MQTT broker and wait for the client
// to reach Ready. That wait is gone with MQTT: the stub below succeeds
// synchronously, so there is no readiness window in which this test could flake
// (nor a 5s timeout that could fail the whole package's setup).
func setupEnvelopeContractOTARouter(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	db := newEnvelopeContractDB(t)
	down := &envelopeDownlinkStub{}

	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	mgr := nodemgr.NewManager(db, down, nil, nil, nil)
	registerOTARoutes(v1, db, ota.NewManager(db, down, nil), mgr)
	return r, db
}

func authenticated(t *testing.T, r *gin.Engine, method, path string, body []byte, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Authorization", authHeader(t))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// ==================== A group: list endpoints → array data ====================

func TestEnvelopeContract_DataEndpointsReturnArrayData(t *testing.T) {
	r, _ := setupEnvelopeContractRouter(t)

	start := url.QueryEscape(time.Now().Add(-time.Hour).UTC().Format(time.RFC3339))
	end := url.QueryEscape(time.Now().UTC().Format(time.RFC3339))
	cases := []struct {
		name string
		path string
	}{
		{"sensor-data", "/api/v1/devices/1/sensor-data?limit=10"},
		{"categories", "/api/v1/unified-data/categories?device_pk=1"},
		{"historical", "/api/v1/unified-data/historical?device_pk=1&category=temp&start_time=" + start + "&end_time=" + end},
		{"historical-batch", "/api/v1/unified-data/historical-batch?device_pk=1&categories=temp&start_time=" + start + "&end_time=" + end},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := authenticated(t, r, http.MethodGet, tc.path, nil, "")
			data := requireEnvelopeOK(t, w, http.StatusOK)
			var arr []json.RawMessage
			if err := json.Unmarshal(data, &arr); err != nil {
				t.Fatalf("data must be a JSON array, got %s", data)
			}
			if arr == nil {
				t.Fatalf("empty data must serialize as [] (not null): %s", data)
			}
		})
	}
}

// ==================== B group: create/update → object data ====================

func TestEnvelopeContract_NodeCreateReturnsObjectData(t *testing.T) {
	r, _ := setupEnvelopeContractRouter(t)

	body, _ := json.Marshal(map[string]interface{}{"node_id": "CONTRACT-NODE-1", "name": "contract"})
	w := authenticated(t, r, http.MethodPost, "/api/v1/nodes", body, "application/json")
	data := requireEnvelopeOK(t, w, http.StatusCreated)

	var node struct {
		ID     uint   `json:"id"`
		NodeID string `json:"node_id"`
	}
	if err := json.Unmarshal(data, &node); err != nil {
		t.Fatalf("data must be an object with node fields: %v (%s)", err, data)
	}
	if node.ID == 0 || node.NodeID != "CONTRACT-NODE-1" {
		t.Fatalf("unexpected node data: %+v", node)
	}
}

func TestEnvelopeContract_OTACreateTaskReturnsObjectData(t *testing.T) {
	r, db := setupEnvelopeContractOTARouter(t)

	fw := models.Firmware{Version: "1.0.0", Checksum: "x", URL: "http://example/fw.bin", SizeBytes: 10}
	if err := db.Create(&fw).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Node{NodeID: "CONTRACT-OTA", Name: "ota", Status: "online"}).Error; err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]interface{}{"node_id": "CONTRACT-OTA", "firmware_id": fw.ID})
	w := authenticated(t, r, http.MethodPost, "/api/v1/ota/tasks", body, "application/json")
	data := requireEnvelopeOK(t, w, http.StatusCreated)

	var task struct {
		ID     uint   `json:"id"`
		OtaID  string `json:"ota_id"`
		NodeID string `json:"node_id"`
	}
	if err := json.Unmarshal(data, &task); err != nil {
		t.Fatalf("data must be an object with task fields: %v (%s)", err, data)
	}
	if task.ID == 0 || task.OtaID == "" || task.NodeID != "CONTRACT-OTA" {
		t.Fatalf("unexpected OTA task data: %+v", task)
	}
}

func TestEnvelopeContract_FirmwareUploadReturnsObjectData(t *testing.T) {
	r, _ := setupEnvelopeContractRouter(t)
	t.Setenv("EHOME_EXTERNAL_HOST", "127.0.0.1:8080")

	const fwName = "contract-upload.bin"
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("version", "9.9.9")
	part, err := mw.CreateFormFile("file", fwName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("firmware-bytes")); err != nil {
		t.Fatal(err)
	}
	_ = mw.Close()

	w := authenticated(t, r, http.MethodPost, "/api/v1/firmwares/upload", buf.Bytes(), mw.FormDataContentType())
	t.Cleanup(func() {
		_ = os.Remove(filepath.Join("firmwares", fwName))
		_ = os.Remove("firmwares")
	})
	data := requireEnvelopeOK(t, w, http.StatusCreated)

	var fw struct {
		ID       uint   `json:"id"`
		Version  string `json:"version"`
		Filename string `json:"filename"`
	}
	if err := json.Unmarshal(data, &fw); err != nil {
		t.Fatalf("data must be an object with firmware fields: %v (%s)", err, data)
	}
	if fw.ID == 0 || fw.Version != "9.9.9" || fw.Filename != fwName {
		t.Fatalf("unexpected firmware data: %+v", fw)
	}
}

func TestEnvelopeContract_PeripheralCreateUpdateReturnObjectData(t *testing.T) {
	r, db := setupEnvelopeContractRouter(t)
	createTestNodeWithPeriphResources(t, db, "contract-periph")

	// B8: POST /nodes/:id/gpio -> 201 + object data with cfg.id
	w := periphJSON(t, r, http.MethodPost, "/api/v1/nodes/contract-periph/gpio", map[string]interface{}{
		"pin": 6, "direction": 1, "label": "contract-gpio",
	})
	data := requireEnvelopeOK(t, w, http.StatusCreated)
	var gpio models.GPIOConfig
	if err := json.Unmarshal(data, &gpio); err != nil {
		t.Fatalf("GPIO create data must be an object: %v (%s)", err, data)
	}
	if gpio.ID == 0 || gpio.Pin != 6 || gpio.Label != "contract-gpio" {
		t.Fatalf("unexpected GPIO create data: %+v", gpio)
	}

	// B9: PUT /nodes/:id/gpio/:pin -> 200 + object data
	w = periphJSON(t, r, http.MethodPut, "/api/v1/nodes/contract-periph/gpio/6", map[string]interface{}{
		"label": "contract-gpio-updated",
	})
	data = requireEnvelopeOK(t, w, http.StatusOK)
	var gpioUpdated models.GPIOConfig
	if err := json.Unmarshal(data, &gpioUpdated); err != nil {
		t.Fatalf("GPIO update data must be an object: %v (%s)", err, data)
	}
	if gpioUpdated.ID != gpio.ID || gpioUpdated.Label != "contract-gpio-updated" {
		t.Fatalf("unexpected GPIO update data: %+v", gpioUpdated)
	}

	// B10: POST /nodes/:id/pwm -> 201 + object data with cfg.id
	w = periphJSON(t, r, http.MethodPost, "/api/v1/nodes/contract-periph/pwm", map[string]interface{}{
		"hardware_id": "PWM0", "pin": 7, "frequency": 1000,
	})
	data = requireEnvelopeOK(t, w, http.StatusCreated)
	var pwm models.PWMConfig
	if err := json.Unmarshal(data, &pwm); err != nil {
		t.Fatalf("PWM create data must be an object: %v (%s)", err, data)
	}
	if pwm.ID == 0 || pwm.HardwareID != "PWM0" || pwm.Pin != 7 {
		t.Fatalf("unexpected PWM create data: %+v", pwm)
	}

	// B11: PUT /nodes/:id/pwm/:hardware_id -> 200 + object data
	w = periphJSON(t, r, http.MethodPut, "/api/v1/nodes/contract-periph/pwm/PWM0", map[string]interface{}{
		"frequency": 2000,
	})
	data = requireEnvelopeOK(t, w, http.StatusOK)
	var pwmUpdated models.PWMConfig
	if err := json.Unmarshal(data, &pwmUpdated); err != nil {
		t.Fatalf("PWM update data must be an object: %v (%s)", err, data)
	}
	if pwmUpdated.ID != pwm.ID || pwmUpdated.Frequency != 2000 {
		t.Fatalf("unexpected PWM update data: %+v", pwmUpdated)
	}
}

// ==================== C group: {node_id, values} nested in data ====================

func TestEnvelopeContract_NodeLatestNestsDialectInData(t *testing.T) {
	r, db := setupEnvelopeContractRouter(t)

	if err := db.Create(&models.Node{NodeID: "contract-latest", Name: "latest", Status: "online"}).Error; err != nil {
		t.Fatal(err)
	}

	// C12: node without enabled channels -> data.values must be []
	w := authenticated(t, r, http.MethodGet, "/api/v1/nodes/contract-latest/latest", nil, "")
	data := requireEnvelopeOK(t, w, http.StatusOK)
	var empty struct {
		NodeID string            `json:"node_id"`
		Values []json.RawMessage `json:"values"`
	}
	if err := json.Unmarshal(data, &empty); err != nil {
		t.Fatalf("data must be an object with node_id/values: %v (%s)", err, data)
	}
	if empty.NodeID != "contract-latest" {
		t.Fatalf("expected data.node_id=contract-latest, got %q", empty.NodeID)
	}
	if empty.Values == nil {
		t.Fatalf("empty values must serialize as [] (not null): %s", data)
	}
	if len(empty.Values) != 0 {
		t.Fatalf("expected empty values array, got %s", data)
	}

	// C13: channel + device + data -> data.values carries the latest row.
	ch := models.Channel{NodeID: "contract-latest", HardwareType: "I2C", BusType: "I2C", Enabled: true}
	if err := db.Create(&ch).Error; err != nil {
		t.Fatal(err)
	}
	dev := models.EdgeDevice{Name: "latest-dev", Type: "sensor", NodeID: "contract-latest", ChannelID: ch.ID, HardwareID: "0x01"}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.UnifiedData{
		DeviceID: dev.ID, SensorName: "temperature", Value: 21.5, Unit: "C", Timestamp: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	// ⚠ 2026-11-08：该端点已**不再回落 unified_data**（停写后它会返回冻结的过期值）。
	//
	// 这里必须显式 SetLatestValue：**这是契约，不是测试辅助**。
	// 生产运行时缓存由 SensorParserConsumer 每帧更新，语义等价；
	// 但「缓存 miss 时返回空」本身是对外行为，已记录在
	// docs/规范/unified_data硬切换-读路径契约变更-2026-11-08.md。
	// 详见 handler_data.go 的「契约变更」块。
	// 所以「有数据时返回最新值」这条契约现在由**缓存**承载 —— 正常运行时缓存由
	// SensorParserConsumer 每帧更新，用例里必须显式模拟这一步。
	//
	// 这条改动本身就是那个行为变更的证明：不灌缓存，接口返回空。
	SetLatestValue(models.UnifiedData{
		DeviceID: dev.ID, SensorName: "temperature", Value: 21.5, Unit: "C", Timestamp: time.Now().UTC(),
	})
	t.Cleanup(resetLatestValueCacheForTest)

	w = authenticated(t, r, http.MethodGet, "/api/v1/nodes/contract-latest/latest", nil, "")
	data = requireEnvelopeOK(t, w, http.StatusOK)
	var populated struct {
		NodeID string                   `json:"node_id"`
		Values []map[string]interface{} `json:"values"`
	}
	if err := json.Unmarshal(data, &populated); err != nil {
		t.Fatalf("data must be an object with node_id/values: %v (%s)", err, data)
	}
	if populated.NodeID != "contract-latest" {
		t.Fatalf("expected data.node_id=contract-latest, got %q", populated.NodeID)
	}
	if len(populated.Values) != 1 || populated.Values[0]["sensor_name"] != "temperature" {
		t.Fatalf("unexpected latest values: %s", data)
	}
}

// TestEnvelopeContract_NodeLatestEmptyDataSerializesAsArray 补的是 Lead 指出的
// **覆盖范围缺口**（2026-11-08，演练后确认）。
//
// ==================== 这个缺口是什么形态 ====================
//
// 上面 TestEnvelopeContract_NodeLatestNestsDialectInData 的 C12 断言了
// 「empty values must serialize as [] (not null)」，但**它造的是无通道的节点** ——
// 走的是 `if len(channelIDs) == 0` 那条分支，那条分支显式写死了
// `"values": []map[string]interface{}{}`。
//
// 于是「有通道、但没有任何数据」这条**真正会返回 nil 的分支从未被测过**：
//
//	var results []LatestValue      // nil
//	db.Raw(...).Scan(&results)     // 0 行 ⇒ 仍是 nil
//	Success(..., "values": results) // encoding/json: nil slice → null
//
// 实测（演练现场 + 本地复现）：
//
//	无通道        → {"values": []}
//	有通道无数据  → {"values": null}     ← 不一致
//
// 这是「判据覆盖了，但覆盖范围不等于声称范围」—— 断言的名字说的是
// 「empty values」，实际只验证了**两种 empty 中的一种**。
//
// 影响：任何按 `data.values.length` 消费的前端，在第二种空值上会抛
// TypeError（Cannot read properties of null）。当前前端无消费方，
// 但这是接口契约，不该依赖「暂时没人用」。
func TestEnvelopeContract_NodeLatestEmptyDataSerializesAsArray(t *testing.T) {
	r, db := setupEnvelopeContractRouter(t)

	if err := db.Create(&models.Node{NodeID: "contract-empty", Name: "empty", Status: "online"}).Error; err != nil {
		t.Fatal(err)
	}
	// 有**启用**通道 ⇒ 跳过 `len(channelIDs) == 0` 那条分支，进入回落 SQL。
	ch := models.Channel{NodeID: "contract-empty", HardwareType: "UART", BusType: "UART", Enabled: true}
	if err := db.Create(&ch).Error; err != nil {
		t.Fatal(err)
	}

	// 用子测试而不是两段顺序断言：两条都是**独立的空值形态**，
	// 顺序写法下第一条 t.Fatalf 会遮住第二条 —— 那正是本轮要补的那种缺口
	// （「跑过了，但没跑到出问题那条」）。
	t.Run("无设备", func(t *testing.T) {
		w := authenticated(t, r, http.MethodGet, "/api/v1/nodes/contract-empty/latest", nil, "")
		assertValuesIsArray(t, requireEnvelopeOK(t, w, http.StatusOK), "无设备")
	})

	t.Run("有设备无数据", func(t *testing.T) {
		dev := models.EdgeDevice{Name: "empty-dev", Type: "sensor", NodeID: "contract-empty", ChannelID: ch.ID, HardwareID: "0x02"}
		if err := db.Create(&dev).Error; err != nil {
			t.Fatal(err)
		}
		w := authenticated(t, r, http.MethodGet, "/api/v1/nodes/contract-empty/latest", nil, "")
		assertValuesIsArray(t, requireEnvelopeOK(t, w, http.StatusOK), "有设备无数据")
	})
}

// assertValuesIsArray 断言 data.values 序列化成 `[]` 而不是 `null`。
//
// 为什么用 json.RawMessage 而不是 []T：后者无法区分 `null` 与 `[]`
// —— Unmarshal 到 nil slice 时两者都得到 len==0，断言会假绿。
// 必须先拿到**原始字节**再判前几个字符。
func assertValuesIsArray(t *testing.T, data []byte, scenario string) {
	t.Helper()
	var raw struct {
		Values json.RawMessage `json:"values"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("%s: data must be an object with values: %v (%s)", scenario, err, data)
	}
	if len(raw.Values) == 0 {
		t.Fatalf("%s: values missing entirely: %s", scenario, data)
	}
	if string(raw.Values) == "null" {
		t.Fatalf("%s: values serialized as null, want [] —— "+
			"前端按 data.values.length 消费会在 null 上抛 TypeError: %s", scenario, data)
	}
	if raw.Values[0] != '[' {
		t.Fatalf("%s: values must be an array, got %s: %s", scenario, string(raw.Values), data)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw.Values, &items); err != nil {
		t.Fatalf("%s: values is not a valid array: %v", scenario, err)
	}
	if len(items) != 0 {
		t.Fatalf("%s: expected empty array, got %s", scenario, data)
	}
}
