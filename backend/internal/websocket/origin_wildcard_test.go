package websocket

import (
	"net/http/httptest"
	"testing"
)

func TestCheckOriginWildcardAllowsAnyOrigin(t *testing.T) {
	t.Setenv("EHOME_ALLOWED_ORIGINS", "*")
	req := httptest.NewRequest("GET", "http://api.example.test/api/v1/ws", nil)
	req.Host = "api.example.test"
	for _, origin := range []string{
		"http://192.168.20.3:5174",
		"https://ui.example.test",
		"http://localhost:5174",
	} {
		req.Header.Set("Origin", origin)
		if !checkOrigin(req) {
			t.Fatalf("wildcard should allow origin %s", origin)
		}
	}
}

func TestCheckOriginWildcardListedAmongOthers(t *testing.T) {
	t.Setenv("EHOME_ALLOWED_ORIGINS", "https://ui.example.test,*")
	req := httptest.NewRequest("GET", "http://api.example.test/api/v1/ws", nil)
	req.Host = "api.example.test"
	req.Header.Set("Origin", "https://random.test")
	if !checkOrigin(req) {
		t.Fatal("wildcard in comma list should allow arbitrary origin")
	}
}
