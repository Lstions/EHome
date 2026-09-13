package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"ehome/backend/internal/models"
)

// ==================== Firmware update: target_model contract ====================
//
// Regression cover for the silent-write-loss in PUT /api/v1/firmwares/:id.
// The request struct declared `json:"node_model"` while the UI, the JSON view of
// models.Firmware and the DB column all use `target_model`. Encoding/json ignored
// the mismatched key, so the field stayed nil, `Updates` ran with an empty map and
// the handler still answered 200. Combined with a discarded ShouldBindJSON error,
// every malformed request also looked successful.
//
// The assertions below are exact-equality on the value read back out of the DB and
// on the JSON body returned to the client: "the write actually landed" is the whole
// point of this contract, so a lenient containment check would not guard it.

// firmwareUpdateRequest builds an authenticated PUT /api/v1/firmwares/:id with a
// raw body so tests can send both well-formed and deliberately malformed JSON.
func firmwareUpdateRequest(t *testing.T, id uint, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/firmwares/"+strconv.FormatUint(uint64(id), 10), bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	return req
}

// TestFirmwareUpdate_TargetModelIsPersisted is the core contract of this endpoint:
// a PUT carrying "target_model" must change the stored value. Sending the canonical
// field name (the one the UI sends, see FirmwareManage.vue) must not be a silent
// no-op. Reverting the struct tag to "node_model" turns this test red.
func TestFirmwareUpdate_TargetModelIsPersisted(t *testing.T) {
	t.Chdir(t.TempDir())
	r, db, _ := setupOTATest(t)

	fw := models.Firmware{
		Version:     "1.0.0",
		Checksum:    "deadbeef",
		SizeBytes:   9,
		URL:         "http://firmware-update-test.local/fw.bin",
		TargetModel: "MODEL-ORIGINAL",
	}
	if err := db.Create(&fw).Error; err != nil {
		t.Fatalf("seed firmware row: %v", err)
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, firmwareUpdateRequest(t, fw.ID, `{"target_model":"MODEL-EDITED"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("update: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// 1) The database must hold the new value. Exact equality, not containment.
	var stored models.Firmware
	if err := db.First(&stored, fw.ID).Error; err != nil {
		t.Fatalf("reload firmware %d: %v", fw.ID, err)
	}
	if stored.TargetModel != "MODEL-EDITED" {
		t.Fatalf("target_model after PUT = %q, want exactly %q (write was silently dropped)", stored.TargetModel, "MODEL-EDITED")
	}

	// 2) The response must expose the same value under the canonical JSON key, so a
	// client can rely on the echoed row instead of issuing a second GET.
	var resp struct {
		Code int             `json:"code"`
		Data models.Firmware `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse update response: %v", err)
	}
	if resp.Code != http.StatusOK {
		t.Fatalf("envelope code = %d, want %d", resp.Code, http.StatusOK)
	}
	if resp.Data.TargetModel != "MODEL-EDITED" {
		t.Fatalf("response data.target_model = %q, want exactly %q", resp.Data.TargetModel, "MODEL-EDITED")
	}

	// 3) A follow-up GET must agree with the PUT — this is the UI's real read path.
	w = httptest.NewRecorder()
	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/firmwares", nil)
	getReq.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, getReq)
	if w.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var listResp struct {
		Data []models.Firmware `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("parse list response: %v", err)
	}
	if len(listResp.Data) != 1 {
		t.Fatalf("list returned %d firmwares, want exactly 1", len(listResp.Data))
	}
	if listResp.Data[0].TargetModel != "MODEL-EDITED" {
		t.Fatalf("list target_model = %q, want exactly %q", listResp.Data[0].TargetModel, "MODEL-EDITED")
	}
}

// TestFirmwareUpdate_TargetModelCanBeCleared pins the pointer semantics: an explicit
// empty string is a real edit (clear the model restriction) and must be written,
// while an omitted key must leave the stored value untouched.
func TestFirmwareUpdate_TargetModelCanBeCleared(t *testing.T) {
	t.Chdir(t.TempDir())
	r, db, _ := setupOTATest(t)

	fw := models.Firmware{
		Version: "1.0.0", Checksum: "c", URL: "http://firmware-update-test.local/fw.bin",
		TargetModel: "MODEL-ORIGINAL", Changelog: "keep-me",
	}
	if err := db.Create(&fw).Error; err != nil {
		t.Fatalf("seed firmware row: %v", err)
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, firmwareUpdateRequest(t, fw.ID, `{"target_model":""}`))
	if w.Code != http.StatusOK {
		t.Fatalf("clear: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var stored models.Firmware
	if err := db.First(&stored, fw.ID).Error; err != nil {
		t.Fatalf("reload firmware %d: %v", fw.ID, err)
	}
	if stored.TargetModel != "" {
		t.Fatalf("target_model after clearing = %q, want exactly %q", stored.TargetModel, "")
	}
	if stored.Changelog != "keep-me" {
		t.Fatalf("changelog = %q, want %q (an omitted key must not be touched)", stored.Changelog, "keep-me")
	}

	// Omitted key: TargetModel must survive a PUT that only changes the changelog.
	w = httptest.NewRecorder()
	r.ServeHTTP(w, firmwareUpdateRequest(t, fw.ID, `{"changelog":"only-changelog"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("partial update: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if err := db.First(&stored, fw.ID).Error; err != nil {
		t.Fatalf("reload firmware %d: %v", fw.ID, err)
	}
	if stored.TargetModel != "" {
		t.Fatalf("omitted target_model changed the value to %q, want it untouched at %q", stored.TargetModel, "")
	}
	if stored.Changelog != "only-changelog" {
		t.Fatalf("changelog = %q, want exactly %q", stored.Changelog, "only-changelog")
	}
}

// TestFirmwareUpdate_RejectsMalformedJSON pins the restored ShouldBindJSON error
// handling: a body that is not valid JSON must be a 400, never a 200. Before the fix
// the error was discarded and the handler answered 200 with an unchanged row.
func TestFirmwareUpdate_RejectsMalformedJSON(t *testing.T) {
	t.Chdir(t.TempDir())
	r, db, _ := setupOTATest(t)

	fw := models.Firmware{
		Version: "1.0.0", Checksum: "c", URL: "http://firmware-update-test.local/fw.bin",
		TargetModel: "MODEL-ORIGINAL",
	}
	if err := db.Create(&fw).Error; err != nil {
		t.Fatalf("seed firmware row: %v", err)
	}

	cases := []struct {
		name string
		body string
	}{
		{"truncated json", `{"target_model":`},
		{"not json at all", `target_model=MODEL-EDITED`},
		{"wrong value type", `{"target_model":123}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, firmwareUpdateRequest(t, fw.ID, tc.body))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for %s, got %d: %s", tc.name, w.Code, w.Body.String())
			}
			// The rejected request must not have mutated the row either.
			var stored models.Firmware
			if err := db.First(&stored, fw.ID).Error; err != nil {
				t.Fatalf("reload firmware %d: %v", fw.ID, err)
			}
			if stored.TargetModel != "MODEL-ORIGINAL" {
				t.Fatalf("rejected request mutated target_model to %q, want %q", stored.TargetModel, "MODEL-ORIGINAL")
			}
		})
	}
}

// TestFirmwareUpdate_UnknownFieldIsIgnored documents that encoding/json stays
// permissive: unknown keys are ignored rather than rejected. Only the canonical
// `target_model` key is wired to the column, which is exactly why the old
// `node_model` tag produced a silent no-op instead of an error.
func TestFirmwareUpdate_UnknownFieldIsIgnored(t *testing.T) {
	t.Chdir(t.TempDir())
	r, db, _ := setupOTATest(t)

	fw := models.Firmware{
		Version: "1.0.0", Checksum: "c", URL: "http://firmware-update-test.local/fw.bin",
		TargetModel: "MODEL-ORIGINAL",
	}
	if err := db.Create(&fw).Error; err != nil {
		t.Fatalf("seed firmware row: %v", err)
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, firmwareUpdateRequest(t, fw.ID, `{"node_model":"MODEL-VIA-NODE-MODEL"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for an ignored unknown field, got %d: %s", w.Code, w.Body.String())
	}
	var stored models.Firmware
	if err := db.First(&stored, fw.ID).Error; err != nil {
		t.Fatalf("reload firmware %d: %v", fw.ID, err)
	}
	if stored.TargetModel != "MODEL-ORIGINAL" {
		t.Fatalf("legacy node_model key wrote to the column (%q); only target_model may bind", stored.TargetModel)
	}
}
