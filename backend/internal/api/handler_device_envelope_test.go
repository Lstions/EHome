package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ehome/backend/internal/models"
)

// TestChannelCreate_ReturnsEnvelopeWithData proves the POST /api/v1/channels
// contract after the envelope convergence batch 1: the response is the unified
// {code,message,data} envelope (not a bare Channel struct), so the frontend's
// `response.data` unpack in frontend-shared/src/stores/channel.ts receives the
// created channel object.
func TestChannelCreate_ReturnsEnvelopeWithData(t *testing.T) {
	r, db := setupDeviceTest(t)

	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})

	body, _ := json.Marshal(map[string]interface{}{
		"node_id":       "NODE001",
		"hardware_type": "I2C",
		"bus_type":      "I2C",
		"bus_config":    "0102",
		"enabled":       true,
		"interval_ms":   5000,
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/channels", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not valid JSON: %v (%s)", err, w.Body.String())
	}
	if code, _ := resp["code"].(float64); int(code) != 201 {
		t.Errorf("expected envelope code 201, got %v (%s)", resp["code"], w.Body.String())
	}
	if msg, _ := resp["message"].(string); msg != "ok" {
		t.Errorf("expected envelope message %q, got %v", "ok", resp["message"])
	}
	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected data to be a JSON object, got %T (%s)", resp["data"], w.Body.String())
	}
	if id, _ := data["id"].(float64); id == 0 {
		t.Errorf("expected data.id != 0, got %v (%s)", data["id"], w.Body.String())
	}
}

// TestChannelList_SuccessEnvelopeHasOKMessage proves GET /api/v1/channels
// returns the unified success envelope with message "ok" after batch 1.
func TestChannelList_SuccessEnvelopeHasOKMessage(t *testing.T) {
	r, _ := setupDeviceTest(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/channels", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not valid JSON: %v (%s)", err, w.Body.String())
	}
	if code, _ := resp["code"].(float64); int(code) != 200 {
		t.Errorf("expected envelope code 200, got %v (%s)", resp["code"], w.Body.String())
	}
	if msg, _ := resp["message"].(string); msg != "ok" {
		t.Errorf("expected envelope message %q, got %v", "ok", resp["message"])
	}
	if resp["data"] == nil {
		t.Errorf("expected data non-null, got nil (%s)", w.Body.String())
	}
}
