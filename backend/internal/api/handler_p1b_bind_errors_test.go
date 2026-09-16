package api

// P1-B: four handlers discarded the error returned by c.ShouldBindJSON.
//
// A malformed body left the local request struct at its zero value and the
// handler continued into its side effects, answering 200 as if the request had
// been understood. The assertions below pin the restored behaviour: a body that
// does not bind must be a 400 and must not reach the side effect.
//
// See docs/分析/后续工作计划与方案-2026-09-15.md §3 P1-B.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ehome/backend/internal/models"
	"ehome/backend/internal/nodemgr"
)

// p1bMalformedBodies are bodies that encoding/json must reject for an endpoint
// whose struct declares the named key. "wrong value type" deliberately names a
// key the struct DOES declare: encoding/json ignores unknown keys, so a body
// like {"raw_data":123} is not malformed for a handler that only reads
// "fields" — it would bind to the zero value and the test would be asserting
// the wrong thing.
func p1bMalformedBodies(key string) []struct {
	name string
	body string
} {
	return []struct {
		name string
		body string
	}{
		{"truncated json", `{"` + key + `":`},
		{"not json at all", key + `=value`},
		{"wrong value type", `{"` + key + `":123}`},
	}
}

// TestP1B_DeviceConfig_TestParser_RejectsMalformedJSON covers
// POST /api/v1/device-configs/:id/test-parser.
func TestP1B_DeviceConfig_TestParser_RejectsMalformedJSON(t *testing.T) {
	r, db := setupDeviceTest(t)
	if err := db.Create(&models.DeviceConfig{
		Name: "P1B Parser", DeviceType: "temperature", HardwareType: "uart",
	}).Error; err != nil {
		t.Fatalf("seed device config: %v", err)
	}

	for _, tc := range p1bMalformedBodies("raw_data") {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/api/v1/device-configs/1/test-parser", bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", authHeader(t))
			r.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("%s: expected 400, got %d: %s", tc.name, w.Code, w.Body.String())
			}
		})
	}
}

// TestP1B_Channel_Reconfigure_RejectsMalformedJSON covers
// POST /api/v1/channels/:channel_id/reconfigure.
func TestP1B_Channel_Reconfigure_RejectsMalformedJSON(t *testing.T) {
	r, _ := setupDeviceTest(t)

	for _, tc := range []struct {
		name string
		body string
	}{
		{"truncated json", `{"baudrate":`},
		{"not json at all", `baudrate=9600`},
		{"wrong value type", `{"baudrate":"9600"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/api/v1/channels/1/reconfigure", bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", authHeader(t))
			r.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("%s: expected 400, got %d: %s", tc.name, w.Code, w.Body.String())
			}
		})
	}
}

// TestP1B_Node_I2CScan_RejectsMalformedJSON covers
// POST /api/v1/nodes/:id/bus/i2c/scan.
func TestP1B_Node_I2CScan_RejectsMalformedJSON(t *testing.T) {
	db := setupTestDB(t)
	r := setupRouter()
	if err := db.Create(&models.Node{NodeID: "P1B-NODE", Name: "P1B", Status: "online"}).Error; err != nil {
		t.Fatalf("seed node: %v", err)
	}
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerNodeRoutes(v1, db, nodemgr.NewManager(db, nil, nil, nil, nil, nil))

	for _, tc := range []struct {
		name string
		body string
	}{
		{"truncated json", `{"hardware_id":`},
		{"not json at all", `hardware_id=i2c0`},
		{"wrong value type", `{"hardware_id":7}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/api/v1/nodes/1/bus/i2c/scan", bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", authHeader(t))
			r.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("%s: expected 400, got %d: %s", tc.name, w.Code, w.Body.String())
			}
		})
	}
}

// TestP1B_DeviceModel_FieldsUpdate_RejectsMalformedJSON covers
// PUT /api/v1/device-models/:id/fields.
//
// This is the dangerous one: the handler binds a struct whose zero value is the
// empty string and then writes it straight to the "fields" column, so a body
// that failed to bind used to silently wipe the column while answering 200.
// The test therefore asserts both the status code AND that the stored value is
// byte-for-byte what it was before the rejected request.
func TestP1B_DeviceModel_FieldsUpdate_RejectsMalformedJSON(t *testing.T) {
	const originalFields = `{"sensors":["temp","humidity"]}`

	r, db := setupVendorTest(t)
	if err := db.Create(&models.DeviceModel{Name: "P1B M1", Type: "temperature", Fields: originalFields}).Error; err != nil {
		t.Fatalf("seed device model: %v", err)
	}

	for _, tc := range p1bMalformedBodies("fields") {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest("PUT", "/api/v1/device-models/1/fields", bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", authHeader(t))
			r.ServeHTTP(w, req)

			// Errorf, not Fatalf: a rejected request that also wipes the column
			// must report both facts in one run.
			if w.Code != http.StatusBadRequest {
				t.Errorf("%s: expected 400, got %d: %s", tc.name, w.Code, w.Body.String())
			}

			var stored models.DeviceModel
			if err := db.First(&stored, 1).Error; err != nil {
				t.Fatalf("reload device model 1: %v", err)
			}
			if stored.Fields != originalFields {
				t.Fatalf("%s: rejected request changed fields to %q, want it untouched at %q",
					tc.name, stored.Fields, originalFields)
			}
		})
	}
}

// TestP1B_DeviceModel_FieldsUpdate_ValidBodyStillWrites is the positive control:
// the malformed-body guards must not have turned a well-formed update into a
// silent no-op.
func TestP1B_DeviceModel_FieldsUpdate_ValidBodyStillWrites(t *testing.T) {
	r, db := setupVendorTest(t)
	if err := db.Create(&models.DeviceModel{Name: "P1B M2", Type: "temperature", Fields: `{}`}).Error; err != nil {
		t.Fatalf("seed device model: %v", err)
	}

	const updated = `{"sensors":["pressure"]}`
	body, err := json.Marshal(map[string]string{"fields": updated})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/v1/device-models/1/fields", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("valid update: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var stored models.DeviceModel
	if err := db.First(&stored, 1).Error; err != nil {
		t.Fatalf("reload device model 1: %v", err)
	}
	if stored.Fields != updated {
		t.Fatalf("valid update stored %q, want %q", stored.Fields, updated)
	}
}

// TestP1B_MalformedJSONErrorShape pins the error envelope the four fixed
// handlers emit, so callers keep seeing the standard shape every other 400 in
// this package already uses (see envelope.go Error): code=400, data=null and a
// non-empty human-readable message.
func TestP1B_MalformedJSONErrorShape(t *testing.T) {
	r, db := setupVendorTest(t)
	if err := db.Create(&models.DeviceModel{Name: "P1B M3", Type: "temperature"}).Error; err != nil {
		t.Fatalf("seed device model: %v", err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/v1/device-models/1/fields", bytes.NewBufferString(`{"fields":`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["code"] != float64(http.StatusBadRequest) {
		t.Fatalf("envelope code = %v, want 400", resp["code"])
	}
	if resp["data"] != nil {
		t.Fatalf("envelope data = %v, want null", resp["data"])
	}
	if msg, _ := resp["message"].(string); msg == "" {
		t.Fatal("envelope message is empty; the error must be explicit, not silent")
	}
}

// TestP1B_Vendor_Update_RejectsMalformedJSON covers PUT /api/v1/vendors/:id.
//
// This site was invisible to the P1-B denominator as first written: the plan
// grepped for c.ShouldBindJSON(&req)$ while this handler binds into &dto, so
// the search returned 4 sites when the tree actually had 6. The regex was the
// bug, not the count.
func TestP1B_Vendor_Update_RejectsMalformedJSON(t *testing.T) {
	r, db := setupVendorTest(t)
	if err := db.Create(&models.Vendor{Name: "P1B Vendor"}).Error; err != nil {
		t.Fatalf("seed vendor: %v", err)
	}

	for _, tc := range p1bMalformedBodies("name") {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest("PUT", "/api/v1/vendors/1", bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", authHeader(t))
			r.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("%s: expected 400, got %d: %s", tc.name, w.Code, w.Body.String())
			}
		})
	}
}

// TestP1B_DeviceModel_Update_RejectsMalformedJSON covers PUT /api/v1/device-models/:id,
// the second site the &req-shaped grep could not see.
func TestP1B_DeviceModel_Update_RejectsMalformedJSON(t *testing.T) {
	r, db := setupVendorTest(t)
	if err := db.Create(&models.DeviceModel{Name: "P1B Model", Type: "temperature"}).Error; err != nil {
		t.Fatalf("seed device model: %v", err)
	}

	for _, tc := range p1bMalformedBodies("name") {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest("PUT", "/api/v1/device-models/1", bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", authHeader(t))
			r.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("%s: expected 400, got %d: %s", tc.name, w.Code, w.Body.String())
			}
		})
	}
}

// TestP1B_VendorAndDeviceModel_MalformedJSONMustNotMutate is the side-effect
// half of the assertion: a bind failure must not be answered 200 *and* it must
// not write anything. Asserting only the status code would still pass if a
// future edit bound the error but kept going into the update.
func TestP1B_VendorAndDeviceModel_MalformedJSONMustNotMutate(t *testing.T) {
	t.Run("vendor", func(t *testing.T) {
		r, db := setupVendorTest(t)
		if err := db.Create(&models.Vendor{Name: "Unchanged"}).Error; err != nil {
			t.Fatalf("seed vendor: %v", err)
		}

		w := httptest.NewRecorder()
		req := httptest.NewRequest("PUT", "/api/v1/vendors/1", bytes.NewBufferString("{"))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", authHeader(t))
		r.ServeHTTP(w, req)

		var stored models.Vendor
		if err := db.First(&stored, 1).Error; err != nil {
			t.Fatalf("reload vendor: %v", err)
		}
		if stored.Name != "Unchanged" {
			t.Fatalf("malformed body mutated vendor name to %q", stored.Name)
		}
	})

	t.Run("device model", func(t *testing.T) {
		r, db := setupVendorTest(t)
		if err := db.Create(&models.DeviceModel{Name: "Unchanged", Type: "temperature"}).Error; err != nil {
			t.Fatalf("seed device model: %v", err)
		}

		w := httptest.NewRecorder()
		req := httptest.NewRequest("PUT", "/api/v1/device-models/1", bytes.NewBufferString("{"))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", authHeader(t))
		r.ServeHTTP(w, req)

		var stored models.DeviceModel
		if err := db.First(&stored, 1).Error; err != nil {
			t.Fatalf("reload device model: %v", err)
		}
		if stored.Name != "Unchanged" {
			t.Fatalf("malformed body mutated device model name to %q", stored.Name)
		}
	})
}
