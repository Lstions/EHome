package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
)

// ==================== POST /edge-devices command_intervals tests ====================
//
// These cover the create-time per-command polling interval support:
//   - valid schedulable command ids persist into edge_devices.command_intervals
//   - unknown command ids → 400, no device created
//   - non-schedulable (one-shot) command ids → 400
//   - negative intervals are normalized to 0 (0 = disabled), matching PUT
//   - omitting the field keeps legacy behavior (device still created)
//   - validation runs against the *final* dev.Type (both the explicit-type
//     driver path and the device_config_id-derived path)

func postEdgeDevice(t *testing.T, r *gin.Engine, body map[string]interface{}) *httptest.ResponseRecorder {
	t.Helper()
	payload, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/edge-devices", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	return w
}

// createJiabaidaBMS posts a jiabaida_bms create request (explicit-type driver
// path) with optional extra body fields.
func createJiabaidaBMS(t *testing.T, r *gin.Engine, extra map[string]interface{}) *httptest.ResponseRecorder {
	t.Helper()
	body := map[string]interface{}{
		"name":        "Jiabaida BMS",
		"node_id":     "NODE001",
		"channel_id":  1,
		"type":        "jiabaida_bms",
		"hardware_id": "1",
	}
	for k, v := range extra {
		body[k] = v
	}
	return postEdgeDevice(t, r, body)
}

func TestEdgeDevice_CommandIntervals_NoFieldStillCreates(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART", Enabled: true})

	w := createJiabaidaBMS(t, r, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 without command_intervals, got %d: %s", w.Code, w.Body.String())
	}
	var dev models.EdgeDevice
	if err := db.First(&dev, "name = ?", "Jiabaida BMS").Error; err != nil {
		t.Fatalf("device must be created without command_intervals: %v", err)
	}
	// Legacy default: empty map, not nil garbage.
	got := parseCommandIntervals(dev.CommandIntervals)
	if len(got) != 0 {
		t.Fatalf("expected empty command_intervals, got %v", got)
	}
}

func TestEdgeDevice_CommandIntervals_PersistsValidSchedulableIds(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART", Enabled: true})

	w := createJiabaidaBMS(t, r, map[string]interface{}{
		"command_intervals": map[string]interface{}{
			"read_basic_info":        3000,
			"read_cell_voltage":      0, // 0 = disabled, explicit
			"read_hardware_version":  7000,
		},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var dev models.EdgeDevice
	if err := db.First(&dev, "name = ?", "Jiabaida BMS").Error; err != nil {
		t.Fatalf("load created device: %v", err)
	}
	if dev.Type != "jiabaida_bms" {
		t.Fatalf("expected type jiabaida_bms, got %q", dev.Type)
	}
	stored := parseCommandIntervals(dev.CommandIntervals)
	if len(stored) != 3 {
		t.Fatalf("expected 3 persisted intervals, got %v", stored)
	}
	if stored["read_basic_info"] != 3000 {
		t.Errorf("read_basic_info = %d, want 3000", stored["read_basic_info"])
	}
	if stored["read_cell_voltage"] != 0 {
		t.Errorf("read_cell_voltage = %d, want 0 (explicit disable)", stored["read_cell_voltage"])
	}
	if stored["read_hardware_version"] != 7000 {
		t.Errorf("read_hardware_version = %d, want 7000", stored["read_hardware_version"])
	}
}

func TestEdgeDevice_CommandIntervals_RejectsUnknownId(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART", Enabled: true})

	w := createJiabaidaBMS(t, r, map[string]interface{}{
		"command_intervals": map[string]interface{}{"no_such_command": 5000},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown command id, got %d: %s", w.Code, w.Body.String())
	}
	var count int64
	db.Model(&models.EdgeDevice{}).Where("name = ?", "Jiabaida BMS").Count(&count)
	if count != 0 {
		t.Fatalf("device with invalid command_intervals must not be created, found %d rows", count)
	}
}

func TestEdgeDevice_CommandIntervals_RejectsNonSchedulableId(t *testing.T) {
	// techfine_inverter exposes only non-schedulable (one-shot) templates —
	// a good fixture for the "known but not schedulable" rejection.
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART", Enabled: true})

	body := map[string]interface{}{
		"name": "Inverter", "node_id": "NODE001", "channel_id": 1,
		"type": "techfine_inverter",
		"command_intervals": map[string]interface{}{"query_status": 5000},
	}
	w := postEdgeDevice(t, r, body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-schedulable command id, got %d: %s", w.Code, w.Body.String())
	}
	var count int64
	db.Model(&models.EdgeDevice{}).Where("name = ?", "Inverter").Count(&count)
	if count != 0 {
		t.Fatalf("device with non-schedulable command must not be created, found %d rows", count)
	}
}

func TestEdgeDevice_CommandIntervals_NormalizesNegativeToZero(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART", Enabled: true})

	w := createJiabaidaBMS(t, r, map[string]interface{}{
		"command_intervals": map[string]interface{}{"read_basic_info": -100},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 with negative interval normalized, got %d: %s", w.Code, w.Body.String())
	}
	var dev models.EdgeDevice
	if err := db.First(&dev, "name = ?", "Jiabaida BMS").Error; err != nil {
		t.Fatalf("load created device: %v", err)
	}
	stored := parseCommandIntervals(dev.CommandIntervals)
	if got := stored["read_basic_info"]; got != 0 {
		t.Fatalf("negative interval must be normalized to 0, got %d", got)
	}
}

// Requirement 5: DeviceConfig path — type is derived from device_config_id;
// command_intervals must be validated and persisted against the *derived*
// dev.Type (jiabaida_bms here), not an explicit type field.
func TestEdgeDevice_CommandIntervals_DeviceConfigPathUsesDerivedType(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART", Enabled: true})
	db.Create(&models.DeviceConfig{Name: "JBD", DeviceType: "jiabaida_bms", HardwareType: "uart", Status: "active"})

	body := map[string]interface{}{
		"name": "DerivedBMS", "node_id": "NODE001", "channel_id": 1,
		"device_config_id":  1,
		"command_intervals": map[string]interface{}{"read_basic_info": 2500},
	}
	w := postEdgeDevice(t, r, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 on DeviceConfig path, got %d: %s", w.Code, w.Body.String())
	}
	var dev models.EdgeDevice
	if err := db.First(&dev, "name = ?", "DerivedBMS").Error; err != nil {
		t.Fatalf("load created device: %v", err)
	}
	if dev.Type != "jiabaida_bms" {
		t.Fatalf("expected type derived from config, got %q", dev.Type)
	}
	stored := parseCommandIntervals(dev.CommandIntervals)
	if stored["read_basic_info"] != 2500 {
		t.Fatalf("expected read_basic_info=2500 persisted on derived-type path, got %v", stored)
	}
}

func TestEdgeDevice_CommandIntervals_DeviceConfigPathRejectsUnknownId(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART", Enabled: true})
	db.Create(&models.DeviceConfig{Name: "JBD", DeviceType: "jiabaida_bms", HardwareType: "uart", Status: "active"})

	body := map[string]interface{}{
		"name": "DerivedBMSBad", "node_id": "NODE001", "channel_id": 1,
		"device_config_id":  1,
		"command_intervals": map[string]interface{}{"not_a_command": 1000},
	}
	w := postEdgeDevice(t, r, body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown id on DeviceConfig path, got %d: %s", w.Code, w.Body.String())
	}
	var count int64
	db.Model(&models.EdgeDevice{}).Where("name = ?", "DerivedBMSBad").Count(&count)
	if count != 0 {
		t.Fatalf("device with invalid command_intervals must not be created, found %d rows", count)
	}
}

// Mixed payload: valid + unknown ids must fail atomically (nothing persisted).
func TestEdgeDevice_CommandIntervals_RejectsMixedValidUnknown(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART", Enabled: true})

	w := createJiabaidaBMS(t, r, map[string]interface{}{
		"command_intervals": map[string]interface{}{
			"read_basic_info": 3000,
			"bogus":           100,
		},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when any id is invalid, got %d: %s", w.Code, w.Body.String())
	}
	var count int64
	db.Model(&models.EdgeDevice{}).Where("name = ?", "Jiabaida BMS").Count(&count)
	if count != 0 {
		t.Fatalf("mixed valid/invalid payload must not create a device, found %d rows", count)
	}
}
