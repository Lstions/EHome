package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ==================== 设备地址门禁 (2026-09-20 生产故障回归) ====================
//
// 故障: 创建向导把 Channel.hardware_id ("UART1" = 总线名) 原样写成
// EdgeDevice.hardware_id (设备地址)。写入时无人校验, 于是每次派发都在
// deviceaction.ParseHardwareAddress 被拒, 操作静默停在 QUEUED 约 120s 后
// 才变 FAILED ("deadline expired before dispatch")。
//
// 本组测试钉死创建/更新两道门禁: 非法地址在**写入时**就 400, 而不是等到派发。

// postEdgeDevice is declared in handler_edge_device_command_intervals_test.go.
// putEdgeDevice is the update-path analogue.
func putEdgeDevice(t *testing.T, r *gin.Engine, path string, body map[string]interface{}) *httptest.ResponseRecorder {
	t.Helper()
	payload, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	return w
}

// seedUartNodeWithBusNameChannel reproduces the production shape: the node's
// only channel carries the BUS NAME "UART1" in channels.hardware_id.
func seedUartNodeWithBusNameChannel(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART", HardwareID: "UART1", Enabled: true})
	return r, db
}

// THE incident: creating an SN-3001 (an addressed Modbus driver) with the bus
// name as its address must be refused at creation time.
func TestEdgeDevice_Create_RejectsBusNameAsAddress(t *testing.T) {
	r, db := seedUartNodeWithBusNameChannel(t)

	w := postEdgeDevice(t, r, map[string]interface{}{
		"name": "光学雨量计", "node_id": "NODE001", "channel_id": 1,
		"type": "sn3001_rain", "hardware_id": "UART1",
	})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for bus name as device address, got %d: %s", w.Code, w.Body.String())
	}
	// The rejection reason must be readable and must carry the legal domain.
	body := w.Body.String()
	if !strings.Contains(body, "1-254") {
		t.Fatalf("rejection must state the legal address domain, got: %s", body)
	}
	var count int64
	db.Model(&models.EdgeDevice{}).Where("name = ?", "光学雨量计").Count(&count)
	if count != 0 {
		t.Fatalf("rejected device must not be persisted, found %d rows", count)
	}
}

// The same value on a driver whose actions never embed an address must stay
// accepted: the gate must not block address-less devices.
func TestEdgeDevice_Create_AllowsBusNameForNonAddressedDriver(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", HardwareID: "I2C0", Enabled: true})

	w := postEdgeDevice(t, r, map[string]interface{}{
		"name": "PureI2C", "node_id": "NODE001", "channel_id": 1,
		"type": "bmp280", "hardware_id": "I2C0",
	})

	if w.Code != http.StatusCreated {
		t.Fatalf("non-addressed driver must keep accepting any identifier, got %d: %s", w.Code, w.Body.String())
	}
	var dev models.EdgeDevice
	if err := db.First(&dev, "name = ?", "PureI2C").Error; err != nil {
		t.Fatalf("device must be created: %v", err)
	}
	if dev.HardwareID != "I2C0" {
		t.Fatalf("hardware_id must be stored verbatim, got %q", dev.HardwareID)
	}
}

func TestEdgeDevice_Create_AddressGateBoundaries(t *testing.T) {
	tests := []struct {
		name       string
		hardwareID interface{} // nil = omit the field entirely
		wantCode   int
		wantStored string
	}{
		{"legal_decimal", "1", http.StatusCreated, "1"},
		{"legal_upper_bound", "254", http.StatusCreated, "254"},
		{"legal_hex", "0x01", http.StatusCreated, "0x01"},
		{"legal_hex_upper", "0xFE", http.StatusCreated, "0xFE"},
		{"legal_padded", " 1 ", http.StatusCreated, " 1 "},
		{"legacy_omitted", nil, http.StatusCreated, ""},
		{"legacy_empty", "", http.StatusCreated, ""},
		{"legacy_zero", "0", http.StatusCreated, "0"},
		{"legacy_whitespace", "   ", http.StatusCreated, "   "},
		{"above_upper_bound", "255", http.StatusBadRequest, ""},
		{"hex_above_upper_bound", "0xFF", http.StatusBadRequest, ""},
		{"hex_zero", "0x00", http.StatusBadRequest, ""},
		{"hex_empty_digits", "0x", http.StatusBadRequest, ""},
		{"alphabetical", "abc", http.StatusBadRequest, ""},
		{"negative", "-1", http.StatusBadRequest, ""},
		{"float", "1.5", http.StatusBadRequest, ""},
		{"lowercase_bus_name", "uart1", http.StatusBadRequest, ""},
		{"other_bus_name", "UART0", http.StatusBadRequest, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, db := seedUartNodeWithBusNameChannel(t)
			body := map[string]interface{}{
				"name": "Boundary", "node_id": "NODE001", "channel_id": 1,
				"type": "sn3001_rain",
			}
			if tc.hardwareID != nil {
				body["hardware_id"] = tc.hardwareID
			}
			w := postEdgeDevice(t, r, body)
			if w.Code != tc.wantCode {
				t.Fatalf("hardware_id=%v: got %d want %d: %s", tc.hardwareID, w.Code, tc.wantCode, w.Body.String())
			}
			var count int64
			db.Model(&models.EdgeDevice{}).Where("name = ?", "Boundary").Count(&count)
			if tc.wantCode == http.StatusCreated {
				if count != 1 {
					t.Fatalf("expected the device to be created, found %d rows", count)
				}
				var dev models.EdgeDevice
				db.First(&dev, "name = ?", "Boundary")
				if dev.HardwareID != tc.wantStored {
					t.Fatalf("hardware_id stored as %q, want %q (verbatim, trimmed only for validation)", dev.HardwareID, tc.wantStored)
				}
			} else if count != 0 {
				t.Fatalf("rejected device must not be persisted, found %d rows", count)
			}
		})
	}
}

// UPDATE path: a PUT must not be able to smuggle a bus name into hardware_id,
// and the rejected write must leave the row untouched.
func TestEdgeDevice_Update_RejectsBusNameAsAddress(t *testing.T) {
	r, db := seedUartNodeWithBusNameChannel(t)
	db.Create(&models.EdgeDevice{Name: "Rain", Type: "sn3001_rain", NodeID: "NODE001", ChannelID: 1, HardwareID: "3", Enabled: true})

	w := putEdgeDevice(t, r, "/api/v1/edge-devices/1", map[string]interface{}{"hardware_id": "UART1"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for bus name on update, got %d: %s", w.Code, w.Body.String())
	}
	var dev models.EdgeDevice
	db.First(&dev, 1)
	if dev.HardwareID != "3" {
		t.Fatalf("rejected update must leave hardware_id untouched, got %q", dev.HardwareID)
	}
}

// A PUT that only renames an existing row must not be blocked by the gate, even
// when the stored address is a legacy value the gate would reject on write.
func TestEdgeDevice_Update_AllowsUnrelatedFieldWhenStoredAddressIsLegacy(t *testing.T) {
	r, db := seedUartNodeWithBusNameChannel(t)
	// Historical row written before the gate existed (the production shape).
	db.Create(&models.EdgeDevice{Name: "LegacyRain", Type: "sn3001_rain", NodeID: "NODE001", ChannelID: 1, HardwareID: "UART1", Enabled: true})

	w := putEdgeDevice(t, r, "/api/v1/edge-devices/1", map[string]interface{}{"name": "LegacyRainRenamed"})
	if w.Code != http.StatusOK {
		t.Fatalf("renaming a row with a legacy address must stay allowed, got %d: %s", w.Code, w.Body.String())
	}
	var dev models.EdgeDevice
	db.First(&dev, 1)
	if dev.Name != "LegacyRainRenamed" {
		t.Fatalf("rename must be applied, got %q", dev.Name)
	}
	if dev.HardwareID != "UART1" {
		t.Fatalf("untouched legacy address must be preserved, got %q", dev.HardwareID)
	}
}

// Fixing a legacy row through the API must work: the wizard's remediation path.
func TestEdgeDevice_Update_AcceptsLegalAddressFixingLegacyRow(t *testing.T) {
	r, db := seedUartNodeWithBusNameChannel(t)
	db.Create(&models.EdgeDevice{Name: "LegacyRain", Type: "sn3001_rain", NodeID: "NODE001", ChannelID: 1, HardwareID: "UART1", Enabled: true})

	w := putEdgeDevice(t, r, "/api/v1/edge-devices/1", map[string]interface{}{"hardware_id": "0x01"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 when repairing the address, got %d: %s", w.Code, w.Body.String())
	}
	var dev models.EdgeDevice
	db.First(&dev, 1)
	if dev.HardwareID != "0x01" {
		t.Fatalf("repaired address must be stored, got %q", dev.HardwareID)
	}
}

// Update gate must key off the CANDIDATE type, not the stored one: switching a
// row onto an addressed driver while keeping a bus name must be refused.
func TestEdgeDevice_Update_RejectsWhenCandidateTypeNeedsAddress(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", HardwareID: "I2C0", Enabled: true})
	db.Create(&models.EdgeDevice{Name: "Dev", Type: "bmp280", NodeID: "NODE001", ChannelID: 1, HardwareID: "I2C0", Enabled: true})

	w := putEdgeDevice(t, r, "/api/v1/edge-devices/1", map[string]interface{}{
		"device_config_id": 0, "type": "sn3001_rain",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when the candidate type needs an address, got %d: %s", w.Code, w.Body.String())
	}
	var dev models.EdgeDevice
	db.First(&dev, 1)
	if dev.Type != "bmp280" {
		t.Fatalf("rejected update must leave type untouched, got %q", dev.Type)
	}
}
