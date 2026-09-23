package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ehome/backend/internal/models"
)

// =====================================================================
// USB transport channel (2026-09-21)
//
// The ESP32-C6 native USB port (previously the console, /dev/ttyACM0) becomes a
// data bus for the BMS simulator. Firmware side is BUS_TYPE_USB.
//
// bus_config contract for USB
// ---------------------------
// USB CDC is a virtual serial port: no tx/rx pins, no baudrate. The minimal
// legal value is therefore the EMPTY STRING, and that is what the backend
// writes for a USB channel. Any length is accepted (including a length that
// would be "too short" for UART/I2C) because nothing is decoded out of it —
// but the value must still be valid hex, because bus_config is a hex byte
// string for every bus type on this path (the collector forwards the bytes to
// the firmware verbatim). "", "00" and "050000000000020304" are all legal;
// "zz" is still rejected (500 via the generic error path — only the
// GPIO/PWM pin-conflict sentinel is mapped to a 4xx on POST /channels; that
// pre-existing error semantics is deliberately preserved).
//
// "USB" is accepted by NAME only. The numeric alias "4" is already GPIO on the
// channel-type path (see peripheralExcludedTypes), so a numeric USB alias would
// collide with the legacy GPIO meaning. The manifest's bus_type wire field uses
// a different numbering where USB = 4; that mapping is the collector protocol's,
// not this API's.
// =====================================================================

func TestChannel_CreateUSBTransport(t *testing.T) {
	for _, busConfig := range []string{"", "00", "050000000000020304"} {
		t.Run("bus_config="+busConfig, func(t *testing.T) {
			r, db := setupDeviceTest(t)
			db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})

			body, _ := json.Marshal(map[string]interface{}{
				"node_id":       "NODE001",
				"hardware_type": "USB",
				"bus_type":      "USB",
				"bus_config":    busConfig,
				"enabled":       true,
				"interval_ms":   5000,
			})
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/channels", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", authHeader(t))
			r.ServeHTTP(w, req)

			if w.Code != http.StatusCreated {
				t.Fatalf("USB channel bus_config=%q rejected: %d %s", busConfig, w.Code, w.Body.String())
			}
			var ch models.Channel
			if err := db.First(&ch).Error; err != nil {
				t.Fatalf("USB channel was not persisted: %v", err)
			}
			if ch.BusType != "USB" || ch.HardwareType != "USB" {
				t.Fatalf("stored bus_type=%q hardware_type=%q, want USB/USB", ch.BusType, ch.HardwareType)
			}
		})
	}
}

// A non-hex bus_config is still rejected for USB — the pins-less exception does
// not turn into "no validation at all". The status is the generic 500 the
// existing createChannel handler returns for any non-conflict transaction
// error (see the 422/500 mapping at the end of the POST /channels transaction);
// this change does not alter that mapping.
func TestChannel_CreateUSBRejectsNonHexBusConfig(t *testing.T) {
	r, db := setupDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	body, _ := json.Marshal(map[string]interface{}{
		"node_id": "NODE001", "hardware_type": "USB", "bus_type": "USB", "bus_config": "zz", "enabled": true,
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/channels", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code < 400 {
		t.Fatalf("non-hex USB bus_config accepted: %d %s", w.Code, w.Body.String())
	}
	var count int64
	db.Model(&models.Channel{}).Count(&count)
	if count != 0 {
		t.Fatalf("rejected USB channel was persisted")
	}
}

// An enabled USB channel occupies no GPIO, so a reported pin stays usable
// (validateEnabledChannelPin must skip USB the same way it skips ADC).
// TestGPIO_USBChannelDoesNotBlockPin below is the control that proves the
// check is still live for a pin-owning bus.
func TestChannel_USBDoesNotBlockGPIOPin(t *testing.T) {
	r, db, _ := setupPeriphTest(t)
	createTestNodeWithPeriphResources(t, db, "node-1")
	if err := db.Create(&models.Channel{
		NodeID: "node-1", HardwareType: "USB", BusType: "USB", BusConfig: "", Enabled: true,
	}).Error; err != nil {
		t.Fatalf("seed USB channel: %v", err)
	}

	w := periphJSON(t, r, "POST", "/api/v1/nodes/node-1/gpio", map[string]interface{}{
		"pin": 6, "direction": 1,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("GPIO pin 6 blocked by an enabled USB channel: %d %s", w.Code, w.Body.String())
	}
}

// Control for the test above: an enabled I2C channel routed on pins 6/7 must
// still block pin 6. Without this, the USB test would also pass if the pin
// conflict check had been silently disabled.
func TestChannel_I2CStillBlocksGPIOPin(t *testing.T) {
	r, db, _ := setupPeriphTest(t)
	createTestNodeWithPeriphResources(t, db, "node-1")
	if err := db.Create(&models.Channel{
		NodeID: "node-1", HardwareType: "I2C", BusType: "I2C", BusConfig: "0607", Enabled: true,
	}).Error; err != nil {
		t.Fatalf("seed I2C channel: %v", err)
	}

	w := periphJSON(t, r, "POST", "/api/v1/nodes/node-1/gpio", map[string]interface{}{
		"pin": 6, "direction": 1,
	})
	// 422 comes from validateReportedGPIO -> validateEnabledChannelPin; the
	// point of this control is that the pin-conflict gate is still live.
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for a pin owned by an enabled I2C channel, got %d: %s", w.Code, w.Body.String())
	}
}

// The legacy numeric alias must keep its GPIO meaning: isPeripheralChannelType
// must not start treating "USB" as a peripheral, and "4" must stay GPIO.
func TestChannel_USBBusTypeClassification(t *testing.T) {
	if isPeripheralChannelType("USB") || isPeripheralChannelType("usb") {
		t.Fatal("USB must not be classified as a GPIO/PWM peripheral resource")
	}
	if !isPeripheralChannelType("4") {
		t.Fatal("4 must stay a peripheral (GPIO) alias")
	}
}
