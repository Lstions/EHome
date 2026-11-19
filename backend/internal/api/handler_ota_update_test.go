package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"ehome/backend/internal/models"

	"gorm.io/gorm"
)

// ==================== Firmware update: the surviving writable fields ====================
//
// 方案 §4.3（2026-11-18 / M0）：PUT 只允许 changelog / channel / min_from_version。
// version 由镜像决定（镜像自证），target_model / stable 两列已随启动迁移 DROP，
// 三者都**不再出现在请求体里**。
//
// 断言风格仍然是「写真的落库 + 响应回显同一值」：这条端点原先的缺陷正是
// 结构体 tag 与前端字段不一致导致 Updates 拿到空 map 却照样回 200。
// 静默丢写比报错更难发现，所以只做精确相等断言。

// firmwareUpdateRequest builds an authenticated PUT /api/v1/firmwares/:id with a
// raw body so tests can send both well-formed and deliberately malformed JSON.
func firmwareUpdateRequest(t *testing.T, id uint, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/firmwares/"+strconv.FormatUint(uint64(id), 10), bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	return req
}

func seedFirmware(t *testing.T, db *gorm.DB) models.Firmware {
	t.Helper()
	fw := models.Firmware{
		Version:   "2.8.0",
		Checksum:  "deadbeef",
		SizeBytes: 9,
		URL:       "http://firmware-update-test.local/fw.bin",
		Changelog: "keep-me",
		Channel:   models.FirmwareChannelStable,
	}
	if err := db.Create(&fw).Error; err != nil {
		t.Fatalf("seed firmware row: %v", err)
	}
	return fw
}

// TestFirmwareUpdate_WritableFieldsPersisted is the core contract of this endpoint:
// changelog / channel / min_from_version must actually land in the DB and be
// echoed by the response and by a follow-up list read.
func TestFirmwareUpdate_WritableFieldsPersisted(t *testing.T) {
	t.Chdir(t.TempDir())
	r, db, _ := setupOTATest(t)
	fw := seedFirmware(t, db)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, firmwareUpdateRequest(t, fw.ID, `{"changelog":"notes","channel":"beta","min_from_version":"2.0.0"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("update: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var stored models.Firmware
	if err := db.First(&stored, fw.ID).Error; err != nil {
		t.Fatalf("reload firmware %d: %v", fw.ID, err)
	}
	if stored.Changelog != "notes" {
		t.Fatalf("changelog = %q, want %q (write was silently dropped)", stored.Changelog, "notes")
	}
	if stored.Channel != models.FirmwareChannelBeta {
		t.Fatalf("channel = %q, want %q", stored.Channel, models.FirmwareChannelBeta)
	}
	if stored.MinFromVersion != "2.0.0" {
		t.Fatalf("min_from_version = %q, want %q", stored.MinFromVersion, "2.0.0")
	}

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
	if resp.Data.Changelog != "notes" || resp.Data.Channel != models.FirmwareChannelBeta {
		t.Fatalf("response data = %+v, want the persisted values", resp.Data)
	}

	// The list endpoint is the UI's real read path; it must agree.
	w = httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/firmwares", nil)
	listReq.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, listReq)
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
	if listResp.Data[0].Channel != models.FirmwareChannelBeta || listResp.Data[0].Changelog != "notes" {
		t.Fatalf("list row = %+v, want the persisted values", listResp.Data[0])
	}
}

// TestFirmwareUpdate_OmittedFieldsAreUntouched pins the pointer semantics: an
// omitted key must leave the stored value alone.
func TestFirmwareUpdate_OmittedFieldsAreUntouched(t *testing.T) {
	t.Chdir(t.TempDir())
	r, db, _ := setupOTATest(t)
	fw := seedFirmware(t, db)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, firmwareUpdateRequest(t, fw.ID, `{"changelog":"only-changelog"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("partial update: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var stored models.Firmware
	if err := db.First(&stored, fw.ID).Error; err != nil {
		t.Fatalf("reload firmware %d: %v", fw.ID, err)
	}
	if stored.Channel != models.FirmwareChannelStable || stored.MinFromVersion != "" || stored.Version != "2.8.0" {
		t.Fatalf("omitted keys changed the row: %+v", stored)
	}
	if stored.Changelog != "only-changelog" {
		t.Fatalf("changelog = %q, want %q", stored.Changelog, "only-changelog")
	}
}

// TestFirmwareUpdate_RejectsUnknownChannel：channel 只有 stable/beta/internal 三值
// （方案 §5.2：withdrawn 由 withdrawn_at 表达，不占渠道取值）。
func TestFirmwareUpdate_RejectsUnknownChannel(t *testing.T) {
	t.Chdir(t.TempDir())
	r, db, _ := setupOTATest(t)
	fw := seedFirmware(t, db)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, firmwareUpdateRequest(t, fw.ID, `{"channel":"withdrawn"}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unknown channel, got %d: %s", w.Code, w.Body.String())
	}
	var stored models.Firmware
	if err := db.First(&stored, fw.ID).Error; err != nil {
		t.Fatalf("reload firmware %d: %v", fw.ID, err)
	}
	if stored.Channel != models.FirmwareChannelStable {
		t.Fatalf("rejected request mutated channel to %q", stored.Channel)
	}
}

// TestFirmwareUpdate_RejectsMalformedJSON pins the ShouldBindJSON error handling:
// a body that is not valid JSON must be a 400, never a 200.
func TestFirmwareUpdate_RejectsMalformedJSON(t *testing.T) {
	t.Chdir(t.TempDir())
	r, db, _ := setupOTATest(t)
	fw := seedFirmware(t, db)

	cases := []struct {
		name string
		body string
	}{
		{"truncated json", `{"changelog":`},
		{"not json at all", `changelog=notes`},
		{"wrong value type", `{"channel":123}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, firmwareUpdateRequest(t, fw.ID, tc.body))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for %s, got %d: %s", tc.name, w.Code, w.Body.String())
			}
			var stored models.Firmware
			if err := db.First(&stored, fw.ID).Error; err != nil {
				t.Fatalf("reload firmware %d: %v", fw.ID, err)
			}
			if stored.Changelog != "keep-me" {
				t.Fatalf("rejected request mutated changelog to %q", stored.Changelog)
			}
		})
	}
}

// TestFirmwareUpdate_RemovedFieldsAreIgnored 记录 M0 的破坏性收敛：
// version / target_model / stable 已从请求契约里删除（列也删了）。
// encoding/json 仍会忽略未知键，所以请求返回 200 但**一个字节都不会改**——
// 这正是「不再有第二个真源」的可执行证据。
func TestFirmwareUpdate_RemovedFieldsAreIgnored(t *testing.T) {
	t.Chdir(t.TempDir())
	r, db, _ := setupOTATest(t)
	fw := seedFirmware(t, db)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, firmwareUpdateRequest(t, fw.ID, `{"version":"9.9.9","target_model":"MODEL-X","stable":true,"changelog":"kept"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for ignored unknown keys, got %d: %s", w.Code, w.Body.String())
	}
	var stored models.Firmware
	if err := db.First(&stored, fw.ID).Error; err != nil {
		t.Fatalf("reload firmware %d: %v", fw.ID, err)
	}
	if stored.Version != "2.8.0" {
		t.Fatalf("removed field 'version' still wrote the row: %q", stored.Version)
	}
	if stored.Changelog != "kept" {
		t.Fatalf("changelog = %q, want %q", stored.Changelog, "kept")
	}
}
