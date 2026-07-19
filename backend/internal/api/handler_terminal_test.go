package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"ehome/backend/internal/models"
	"ehome/backend/internal/nodemgr"
	"ehome/backend/internal/terminal"
	wsinternal "ehome/backend/internal/websocket"

	"github.com/gin-gonic/gin"
	wslib "github.com/gorilla/websocket"
)

func setupTerminalRouteTest(t *testing.T) (*gin.Engine, *nodemgr.Manager, func(models.Channel)) {
	t.Helper()
	db := setupTestDB(t)
	mgr := nodemgr.NewManager(db, nil, nil, nil, nil, nil)
	r := gin.New()
	v1 := r.Group("/api/v1")
	registerTerminalRoutes(v1, db, mgr, ControlPolicy{allowUnsafeRawForTests: true})
	return r, mgr, func(ch models.Channel) {
		t.Helper()
		if err := db.Create(&ch).Error; err != nil {
			t.Fatalf("create channel: %v", err)
		}
		if !ch.Enabled || ch.HardwareID == "force-disabled" {
			if err := db.Model(&models.Channel{}).Where("id = ?", ch.ID).UpdateColumn("enabled", false).Error; err != nil {
				t.Fatalf("disable channel: %v", err)
			}
		}
	}
}

func terminalWriteRequest(t *testing.T, r http.Handler, channelID uint, deviceID string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]interface{}{
		"device_id": deviceID,
		"data_hex":  "0102",
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/channels/"+strconv.FormatUint(uint64(channelID), 10)+"/terminal/write", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

func TestTerminalWriteRejectsLegacyPeripheralChannels(t *testing.T) {
	for _, peripheralType := range []string{"GPIO", "PWM"} {
		t.Run(peripheralType, func(t *testing.T) {
			r, _, createChannel := setupTerminalRouteTest(t)
			createChannel(models.Channel{ID: 1, NodeID: "NODE001", HardwareType: peripheralType, BusType: peripheralType, Enabled: true})

			w := terminalWriteRequest(t, r, 1, "NODE001")
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestTerminalWriteRejectsDisabledOrMismatchedChannel(t *testing.T) {
	tests := []struct {
		name     string
		channel  models.Channel
		deviceID string
	}{
		{name: "disabled", channel: models.Channel{ID: 1, NodeID: "NODE001", HardwareType: "UART", BusType: "UART", HardwareID: "force-disabled", Enabled: false}, deviceID: "NODE001"},
		{name: "different node", channel: models.Channel{ID: 1, NodeID: "NODE001", HardwareType: "UART", BusType: "UART", Enabled: true}, deviceID: "NODE999"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _, createChannel := setupTerminalRouteTest(t)
			createChannel(tt.channel)

			w := terminalWriteRequest(t, r, 1, tt.deviceID)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestTerminalWebSocketRejectsLegacyPeripheralAndAllowsUART(t *testing.T) {
	db := setupTestDB(t)
	db.Create(&models.Channel{ID: 1, NodeID: "NODE001", HardwareType: "GPIO", BusType: "GPIO", Enabled: true})
	db.Create(&models.Channel{ID: 2, NodeID: "NODE001", HardwareType: "PWM", BusType: "PWM", Enabled: true})
	db.Create(&models.Channel{ID: 3, NodeID: "NODE001", HardwareType: "UART", BusType: "UART", Enabled: true})

	var sent int
	writeSender := validatedTerminalWriteSender(db, func(string, uint32, []byte, uint32) error {
		sent++
		return nil
	})
	hub := wsinternal.NewHub()
	go hub.Run()
	h := terminal.NewWSHandler(hub, nil, writeSender)
	r := gin.New()
	r.GET("/ws/terminal", func(c *gin.Context) {
		c.Set("subject_id", uint(1))
	}, h.HandleTerminalWS)
	server := httptest.NewServer(r)
	defer server.Close()

	conn, _, err := wslib.DefaultDialer.Dial("ws"+server.URL[len("http"):]+"/ws/terminal", nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	for _, channelID := range []uint{1, 2} {
		if err := conn.WriteJSON(map[string]interface{}{"type": "send", "payload": map[string]interface{}{
			"device_id": "NODE001", "channel_id": channelID, "data_hex": "01",
		}}); err != nil {
			t.Fatalf("send websocket payload: %v", err)
		}
		var response map[string]interface{}
		if err := conn.ReadJSON(&response); err != nil {
			t.Fatalf("read websocket response: %v", err)
		}
		if response["type"] != "error" {
			t.Fatalf("channel %d: expected error, got %#v", channelID, response)
		}
	}
	if sent != 0 {
		t.Fatalf("legacy peripheral reached write sender %d times", sent)
	}

	if err := conn.WriteJSON(map[string]interface{}{"type": "send", "payload": map[string]interface{}{
		"device_id": "NODE001", "channel_id": 3, "data_hex": "01",
	}}); err != nil {
		t.Fatalf("send UART payload: %v", err)
	}
	var response map[string]interface{}
	if err := conn.ReadJSON(&response); err != nil {
		t.Fatalf("read UART response: %v", err)
	}
	if response["type"] != "ack" || sent != 1 {
		t.Fatalf("UART: expected ack and one send, got response=%#v sends=%d", response, sent)
	}
}
