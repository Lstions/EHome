package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/mqtt"
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
		&models.EdgeDevice{}, &models.DeviceConfig{}, &models.DeviceData{},
		&models.UnifiedData{}, &models.DataSource{}, &models.User{},
		&models.OTATask{}, &models.Firmware{}, &models.Vendor{},
		&models.Notification{}, &models.DeviceModel{},
		&models.NodeEvent{}, &models.CalibrationCache{}, &models.PendingWriteRecord{},
		&models.NodeLog{}, &models.GPIOConfig{}, &models.PWMConfig{},
		&models.LogicalDevice{}, &models.MergeJob{},
	); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	db.Create(&models.User{Username: "admin", PasswordHash: "$2a$10$dummy", Role: "admin", Enabled: true})
	return db
}

// setupEnvelopeContractRouter wires data + node + OTA + peripheral routes with
// a disconnected (non-panicking) MQTT client. Use setupEnvelopeContractOTARouter
// when a test actually creates an OTA task.
func setupEnvelopeContractRouter(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	db := newEnvelopeContractDB(t)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	mgr := nodemgr.NewManager(db, &mqtt.Client{}, nil, nil, nil, nil)
	otaMgr := ota.NewManager(db, &mqtt.Client{}, nil)
	registerDataRoutes(v1, db)
	registerNodeRoutes(v1, db, mgr)
	registerPeriphRoutes(v1, db, mgr)
	registerOTARoutes(v1, db, otaMgr, mgr)
	return r, db
}

// ==================== Fake MQTT broker ====================

// fakeMQTTBroker speaks just enough MQTT 3.1.1 to let the supervised
// mqtt.Client reach Ready and publish a QoS-1 message successfully.
type fakeMQTTBroker struct {
	ln net.Listener
}

func newFakeMQTTBroker(t *testing.T) *fakeMQTTBroker {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("fake mqtt broker listen: %v", err)
	}
	b := &fakeMQTTBroker{ln: ln}
	go b.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return b
}

func (b *fakeMQTTBroker) addr() string { return "tcp://" + b.ln.Addr().String() }

func (b *fakeMQTTBroker) serve() {
	for {
		conn, err := b.ln.Accept()
		if err != nil {
			return
		}
		go b.handle(conn)
	}
}

func readMQTTPacket(r io.Reader) (byte, []byte, error) {
	var first [1]byte
	if _, err := io.ReadFull(r, first[:]); err != nil {
		return 0, nil, err
	}
	length := 0
	multiplier := 1
	for {
		var b [1]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return 0, nil, err
		}
		length += int(b[0]&0x7f) * multiplier
		if b[0]&0x80 == 0 {
			break
		}
		multiplier *= 128
		if multiplier > 128*128*128 {
			return 0, nil, fmt.Errorf("malformed remaining length")
		}
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return first[0], payload, nil
}

func (b *fakeMQTTBroker) handle(conn net.Conn) {
	defer conn.Close()
	for {
		hdr, payload, err := readMQTTPacket(conn)
		if err != nil {
			return
		}
		switch hdr >> 4 {
		case 1: // CONNECT -> CONNACK (accepted)
			_, _ = conn.Write([]byte{0x20, 0x02, 0x00, 0x00})
		case 8: // SUBSCRIBE -> SUBACK (one granted QoS per filter)
			if len(payload) < 2 {
				continue
			}
			pid := payload[:2]
			count := 0
			idx := 2
			for idx+2 <= len(payload) {
				tl := int(payload[idx])<<8 | int(payload[idx+1])
				idx += 2 + tl + 1
				count++
			}
			if count == 0 {
				count = 1
			}
			resp := []byte{0x90, byte(2 + count), pid[0], pid[1]}
			for i := 0; i < count; i++ {
				resp = append(resp, 0x01)
			}
			_, _ = conn.Write(resp)
		case 3: // PUBLISH -> PUBACK for QoS 1/2
			qos := (hdr >> 1) & 0x03
			if qos == 0 || len(payload) < 2 {
				continue
			}
			tl := int(payload[0])<<8 | int(payload[1])
			off := 2 + tl
			if off+2 > len(payload) {
				continue
			}
			_, _ = conn.Write([]byte{0x40, 0x02, payload[off], payload[off+1]})
		case 12: // PINGREQ -> PINGRESP
			_, _ = conn.Write([]byte{0xD0, 0x00})
		case 14: // DISCONNECT
			return
		}
	}
}

// setupEnvelopeContractOTARouter wires OTA routes with a live in-process
// broker so POST /ota/tasks reaches its 201 success branch.
func setupEnvelopeContractOTARouter(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	db := newEnvelopeContractDB(t)
	broker := newFakeMQTTBroker(t)
	mc := mqtt.New(broker.addr(), "", "")
	mc.SetHandler(func(string, []byte) {})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = mc.Run(ctx) }()
	select {
	case <-mc.Ready():
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("fake MQTT broker: client did not become ready")
	}
	t.Cleanup(func() {
		cancel()
		mc.Close()
	})

	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	mgr := nodemgr.NewManager(db, &mqtt.Client{}, nil, nil, nil, nil)
	registerOTARoutes(v1, db, ota.NewManager(db, mc, nil), mgr)
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
