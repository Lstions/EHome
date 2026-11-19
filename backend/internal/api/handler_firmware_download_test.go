package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"

	"github.com/gin-gonic/gin"
)

// 这张票的验收判据只有一条：**公开下载路由的校验函数必须认它**。
// 只断言「返回了 url」会漏掉签名/文件名/ttl 任一处的漂移 —— 而那正是原缺陷的形态
// （前端拼 JWT，路由只认 HMAC，两边各自「有值」却永远 401）。
func TestFirmwareDownloadTicketHandlerSignsURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.OpenTestDB(t)

	fw := models.Firmware{
		Version:       "1.0.0",
		Checksum:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SizeBytes:     1024,
		URL:           "placeholder",
		Filename:      "fw-1.0.0.bin",
		StoragePath:   "firmwares/abc123.bin",
		IdentityState: "verified",
	}
	if err := db.Create(&fw).Error; err != nil {
		t.Fatalf("create firmware: %v", err)
	}

	r := gin.New()
	r.POST("/api/v1/firmwares/:id/download-ticket", firmwareDownloadTicketHandler(db))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/firmwares/1/download-ticket", nil)
	req.Host = "ehome.example:8443"
	req.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var envelope struct {
		Data FirmwareDownloadTicket `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v body=%s", err, w.Body.String())
	}
	if envelope.Data.URL == "" || envelope.Data.ExpiresAt == "" {
		t.Fatalf("empty ticket: %+v", envelope.Data)
	}

	u, err := url.Parse(envelope.Data.URL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	if u.Scheme != "https" || u.Host != "ehome.example:8443" {
		t.Fatalf("base url must follow request origin, got %s", envelope.Data.URL)
	}
	// 下载名必须取 StoragePath 的 basename（上传按 elf_sha256 重命名落盘）。
	if u.Path != "/api/v1/firmwares/abc123.bin/download" {
		t.Fatalf("download path = %s", u.Path)
	}

	expires := u.Query().Get("expires")
	signature := u.Query().Get("signature")
	if !validateFirmwareDownload("abc123.bin", expires, signature, jwtSecret, time.Now().UTC()) {
		t.Fatal("minted url must pass the public download validator")
	}

	expiresAt, err := time.Parse(time.RFC3339, envelope.Data.ExpiresAt)
	if err != nil {
		t.Fatalf("expires_at not RFC3339: %v", err)
	}
	if expiresAt.Before(time.Now().Add(29 * time.Minute)) {
		t.Fatalf("ticket ttl too short: %s", envelope.Data.ExpiresAt)
	}
}

func TestFirmwareDownloadTicketHandlerNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.OpenTestDB(t)

	r := gin.New()
	r.POST("/api/v1/firmwares/:id/download-ticket", firmwareDownloadTicketHandler(db))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/firmwares/999/download-ticket", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestFirmwareDownloadTicketHandlerRejectsBadID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.OpenTestDB(t)

	r := gin.New()
	r.POST("/api/v1/firmwares/:id/download-ticket", firmwareDownloadTicketHandler(db))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/firmwares/not-a-number/download-ticket", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}