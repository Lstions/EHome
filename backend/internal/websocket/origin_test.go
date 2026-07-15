package websocket

import (
	"net/http/httptest"
	"testing"
)

func TestCheckOriginAllowsSameOriginAndRejectsCrossOrigin(t *testing.T) {
	req := httptest.NewRequest("GET", "http://example.test/api/v1/ws", nil)
	req.Host = "example.test"
	req.Header.Set("Origin", "http://example.test")
	if !checkOrigin(req) {
		t.Fatal("same origin rejected")
	}
	req.Header.Set("Origin", "https://evil.test")
	if checkOrigin(req) {
		t.Fatal("cross origin accepted")
	}
}

func TestCheckOriginUsesExplicitAllowlist(t *testing.T) {
	t.Setenv("EHOME_ALLOWED_ORIGINS", "https://ui.example.test")
	req := httptest.NewRequest("GET", "http://api.example.test/api/v1/ws", nil)
	req.Host = "api.example.test"
	req.Header.Set("Origin", "https://ui.example.test")
	if !checkOrigin(req) {
		t.Fatal("configured origin rejected")
	}
}
