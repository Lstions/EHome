package terminal

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ehome/backend/internal/websocket"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	wslib "github.com/gorilla/websocket"
)

// mockHistoryFetcher returns fixed history entries
func mockHistoryFetcher(channelID uint) ([]Entry, error) {
	return []Entry{
		{Direction: "tx", DataHex: "0102", Timestamp: time.Now()},
		{Direction: "rx", DataHex: "0304", Timestamp: time.Now()},
	}, nil
}

// mockWriteSender records write commands
var lastWriteCommand struct {
	deviceID  string
	channelID uint32
	data      []byte
	readSize  uint32
}

func mockWriteSender(deviceID string, channelID uint32, data []byte, readSize uint32) error {
	lastWriteCommand.deviceID = deviceID
	lastWriteCommand.channelID = channelID
	lastWriteCommand.data = data
	lastWriteCommand.readSize = readSize
	return nil
}

// generateTestToken creates a JWT token for testing
func generateTestToken(role string) string {
	claims := jwt.MapClaims{
		"sub":  "test-user",
		"role": role,
		"exp":  time.Now().Add(1 * time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, _ := token.SignedString([]byte(jwtSecret))
	return tokenStr
}

func TestNewWSHandler(t *testing.T) {
	hub := websocket.NewHub()
	go hub.Run()

	h := NewWSHandler(hub, mockHistoryFetcher, mockWriteSender)
	if h == nil {
		t.Fatal("expected non-nil handler")
	}
	if h.historyFetch == nil {
		t.Error("expected historyFetch to be set")
	}
	if h.writeSend == nil {
		t.Error("expected writeSend to be set")
	}
}

func TestWSHandler_SubscribeAndHistory(t *testing.T) {
	gin.SetMode(gin.TestMode)

	hub := websocket.NewHub()
	go hub.Run()

	h := NewWSHandler(hub, mockHistoryFetcher, mockWriteSender)

	r := gin.New()
	r.GET("/ws/terminal", h.HandleTerminalWS)

	server := httptest.NewServer(r)
	defer server.Close()

	// Connect as WebSocket client
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/terminal?token=" + generateTestToken("admin")
	conn, _, err := wslib.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer conn.Close()

	// Send subscribe message
	subscribeMsg := map[string]interface{}{
		"type": "subscribe",
		"payload": map[string]interface{}{
			"channel_id": 1,
		},
	}
	if err := conn.WriteJSON(subscribeMsg); err != nil {
		t.Fatalf("failed to send subscribe: %v", err)
	}

	// Read response (should get history + ack)
	var resp map[string]interface{}
	if err := conn.ReadJSON(&resp); err != nil {
		t.Fatalf("failed to read response: %v", err)
	}

	// First response should be data with history
	if resp["type"] == "data" {
		payload, ok := resp["payload"].(map[string]interface{})
		if !ok {
			t.Fatal("expected payload to be a map")
		}
		if payload["channel_id"] != float64(1) {
			t.Errorf("expected channel_id=1, got %v", payload["channel_id"])
		}
		history, ok := payload["history"].([]interface{})
		if !ok || len(history) != 2 {
			t.Errorf("expected 2 history entries, got %v", history)
		}
	} else if resp["type"] == "ack" {
		// History might be empty, just check ack
	} else {
		t.Errorf("unexpected response type: %v", resp["type"])
	}
}

func TestWSHandler_SendCommand(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Reset lastWriteCommand
	lastWriteCommand.deviceID = ""
	lastWriteCommand.channelID = 0
	lastWriteCommand.data = nil
	lastWriteCommand.readSize = 0

	hub := websocket.NewHub()
	go hub.Run()

	h := NewWSHandler(hub, mockHistoryFetcher, mockWriteSender)

	r := gin.New()
	r.GET("/ws/terminal", h.HandleTerminalWS)

	server := httptest.NewServer(r)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/terminal?token=" + generateTestToken("admin")
	conn, _, err := wslib.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer conn.Close()

	// Subscribe first
	subscribeMsg := map[string]interface{}{
		"type": "subscribe",
		"payload": map[string]interface{}{
			"channel_id": 5,
		},
	}
	conn.WriteJSON(subscribeMsg)

	// Drain the subscribe responses (data + ack)
	for i := 0; i < 2; i++ {
		var resp map[string]interface{}
		conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		if err := conn.ReadJSON(&resp); err != nil {
			break
		}
		t.Logf("drained: type=%v", resp["type"])
	}

	// Send a write command
	sendMsg := map[string]interface{}{
		"type": "send",
		"payload": map[string]interface{}{
			"device_id":  "device123",
			"channel_id": 5,
			"data_hex":   "01020304",
			"read_size":  10,
		},
	}
	if err := conn.WriteJSON(sendMsg); err != nil {
		t.Fatalf("failed to send command: %v", err)
	}

	// Read ack or error
	var resp map[string]interface{}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := conn.ReadJSON(&resp); err != nil {
		t.Fatalf("failed to read ack: %v", err)
	}

	if resp["type"] == "error" {
		t.Fatalf("got error response: %v", resp["payload"])
	}

	if resp["type"] != "ack" {
		t.Errorf("expected ack, got %v", resp["type"])
	}

	// Give a moment for the write command to be processed
	time.Sleep(100 * time.Millisecond)

	// Verify write command was recorded
	if lastWriteCommand.deviceID != "device123" {
		t.Errorf("expected deviceID=device123, got %s", lastWriteCommand.deviceID)
	}
	if lastWriteCommand.channelID != 5 {
		t.Errorf("expected channelID=5, got %d", lastWriteCommand.channelID)
	}
	if len(lastWriteCommand.data) != 4 || lastWriteCommand.data[0] != 0x01 || lastWriteCommand.data[3] != 0x04 {
		t.Errorf("expected data=[1,2,3,4], got %v", lastWriteCommand.data)
	}
	if lastWriteCommand.readSize != 10 {
		t.Errorf("expected readSize=10, got %d", lastWriteCommand.readSize)
	}
}

func TestWSHandler_PingPong(t *testing.T) {
	gin.SetMode(gin.TestMode)

	hub := websocket.NewHub()
	go hub.Run()

	h := NewWSHandler(hub, mockHistoryFetcher, mockWriteSender)

	r := gin.New()
	r.GET("/ws/terminal", h.HandleTerminalWS)

	server := httptest.NewServer(r)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/terminal?token=" + generateTestToken("admin")
	conn, _, err := wslib.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer conn.Close()

	// Send ping
	pingMsg := map[string]interface{}{
		"type": "ping",
	}
	if err := conn.WriteJSON(pingMsg); err != nil {
		t.Fatalf("failed to send ping: %v", err)
	}

	// Read pong
	var resp map[string]interface{}
	if err := conn.ReadJSON(&resp); err != nil {
		t.Fatalf("failed to read pong: %v", err)
	}

	if resp["type"] != "pong" {
		t.Errorf("expected pong, got %v", resp["type"])
	}
}

func TestWSHandler_InvalidJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)

	hub := websocket.NewHub()
	go hub.Run()

	h := NewWSHandler(hub, mockHistoryFetcher, mockWriteSender)

	r := gin.New()
	r.GET("/ws/terminal", h.HandleTerminalWS)

	server := httptest.NewServer(r)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/terminal?token=" + generateTestToken("admin")
	conn, _, err := wslib.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer conn.Close()

	// Send invalid JSON
	if err := conn.WriteMessage(wslib.TextMessage, []byte("not json")); err != nil {
		t.Fatalf("failed to send: %v", err)
	}

	// Read error response
	var resp map[string]interface{}
	if err := conn.ReadJSON(&resp); err != nil {
		t.Fatalf("failed to read response: %v", err)
	}

	if resp["type"] != "error" {
		t.Errorf("expected error, got %v", resp["type"])
	}
}

func TestWSHandler_UnknownMessageType(t *testing.T) {
	gin.SetMode(gin.TestMode)

	hub := websocket.NewHub()
	go hub.Run()

	h := NewWSHandler(hub, mockHistoryFetcher, mockWriteSender)

	r := gin.New()
	r.GET("/ws/terminal", h.HandleTerminalWS)

	server := httptest.NewServer(r)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/terminal?token=" + generateTestToken("admin")
	conn, _, err := wslib.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer conn.Close()

	// Send unknown message type
	msg := map[string]interface{}{
		"type": "unknown_type",
	}
	if err := conn.WriteJSON(msg); err != nil {
		t.Fatalf("failed to send: %v", err)
	}

	// Read error response
	var resp map[string]interface{}
	if err := conn.ReadJSON(&resp); err != nil {
		t.Fatalf("failed to read response: %v", err)
	}

	if resp["type"] != "error" {
		t.Errorf("expected error, got %v", resp["type"])
	}
}

func TestWSHandler_Unsubscribe(t *testing.T) {
	gin.SetMode(gin.TestMode)

	hub := websocket.NewHub()
	go hub.Run()

	h := NewWSHandler(hub, mockHistoryFetcher, mockWriteSender)

	r := gin.New()
	r.GET("/ws/terminal", h.HandleTerminalWS)

	server := httptest.NewServer(r)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/terminal?token=" + generateTestToken("admin")
	conn, _, err := wslib.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer conn.Close()

	// Subscribe
	subscribeMsg := map[string]interface{}{
		"type": "subscribe",
		"payload": map[string]interface{}{
			"channel_id": 1,
		},
	}
	conn.WriteJSON(subscribeMsg)
	conn.ReadJSON(new(map[string]interface{})) // drain history
	conn.ReadJSON(new(map[string]interface{})) // drain ack

	// Unsubscribe
	unsubscribeMsg := map[string]interface{}{
		"type": "unsubscribe",
		"payload": map[string]interface{}{
			"channel_id": 1,
		},
	}
	if err := conn.WriteJSON(unsubscribeMsg); err != nil {
		t.Fatalf("failed to send unsubscribe: %v", err)
	}

	// Read ack
	var resp map[string]interface{}
	if err := conn.ReadJSON(&resp); err != nil {
		t.Fatalf("failed to read response: %v", err)
	}

	if resp["type"] != "ack" {
		t.Errorf("expected ack, got %v", resp["type"])
	}
}

func TestParseHex(t *testing.T) {
	tests := []struct {
		input    string
		expected []byte
		hasError bool
	}{
		{"0102", []byte{0x01, 0x02}, false},
		{"AABBCC", []byte{0xAA, 0xBB, 0xCC}, false},
		{"aabbcc", []byte{0xAA, 0xBB, 0xCC}, false},
		{"", []byte{}, false},
		{"0", nil, true},       // odd length
		{"GG", nil, true},      // invalid hex
		{"0x0102", nil, true},  // 0x prefix not supported
	}

	for _, tt := range tests {
		result, err := parseHex(tt.input)
		if tt.hasError {
			if err == nil {
				t.Errorf("parseHex(%q): expected error, got nil", tt.input)
			}
		} else {
			if err != nil {
				t.Errorf("parseHex(%q): unexpected error: %v", tt.input, err)
				continue
			}
			if string(result) != string(tt.expected) {
				t.Errorf("parseHex(%q): expected %v, got %v", tt.input, tt.expected, result)
			}
		}
	}
}

func TestWSHandler_DataBroadcast(t *testing.T) {
	gin.SetMode(gin.TestMode)

	hub := websocket.NewHub()
	go hub.Run()

	h := NewWSHandler(hub, mockHistoryFetcher, mockWriteSender)

	r := gin.New()
	r.GET("/ws/terminal", h.HandleTerminalWS)

	server := httptest.NewServer(r)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/terminal?token=" + generateTestToken("admin")
	conn, _, err := wslib.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer conn.Close()

	// Subscribe to channel 1
	subscribeMsg := map[string]interface{}{
		"type": "subscribe",
		"payload": map[string]interface{}{
			"channel_id": 1,
		},
	}
	conn.WriteJSON(subscribeMsg)

	// Drain responses (history + ack)
	conn.ReadJSON(new(map[string]interface{}))
	conn.ReadJSON(new(map[string]interface{}))

	// Broadcast a channel_data event
	hub.BroadcastEvent("channel_data", map[string]interface{}{
		"device_id":  "dev1",
		"channel_id": 1,
		"raw_hex":    "aabbcc",
		"timestamp":  12345,
	})

	// Read the broadcast data
	var resp map[string]interface{}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := conn.ReadJSON(&resp); err != nil {
		t.Fatalf("failed to read broadcast: %v", err)
	}

	if resp["type"] != "data" {
		t.Errorf("expected data, got %v", resp["type"])
	}
	payload, _ := resp["payload"].(map[string]interface{})
	if payload["channel_id"] != float64(1) {
		t.Errorf("expected channel_id=1, got %v", payload["channel_id"])
	}
	if payload["raw_hex"] != "aabbcc" {
		t.Errorf("expected raw_hex=aabbcc, got %v", payload["raw_hex"])
	}
}

func TestWSHandler_DataBroadcastFilteredByChannel(t *testing.T) {
	gin.SetMode(gin.TestMode)

	hub := websocket.NewHub()
	go hub.Run()

	h := NewWSHandler(hub, mockHistoryFetcher, mockWriteSender)

	r := gin.New()
	r.GET("/ws/terminal", h.HandleTerminalWS)

	server := httptest.NewServer(r)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/terminal?token=" + generateTestToken("admin")
	conn, _, err := wslib.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer conn.Close()

	// Subscribe to channel 1
	subscribeMsg := map[string]interface{}{
		"type": "subscribe",
		"payload": map[string]interface{}{
			"channel_id": 1,
		},
	}
	conn.WriteJSON(subscribeMsg)
	conn.ReadJSON(new(map[string]interface{})) // drain
	conn.ReadJSON(new(map[string]interface{})) // drain

	// Broadcast for channel 2 (should NOT be received)
	hub.BroadcastEvent("channel_data", map[string]interface{}{
		"device_id":  "dev1",
		"channel_id": 2,
		"raw_hex":    "ddeeff",
		"timestamp":  12345,
	})

	// Broadcast for channel 1 (should be received)
	hub.BroadcastEvent("channel_data", map[string]interface{}{
		"device_id":  "dev1",
		"channel_id": 1,
		"raw_hex":    "aabbcc",
		"timestamp":  12345,
	})

	// Read only the channel 1 broadcast
	var resp map[string]interface{}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := conn.ReadJSON(&resp); err != nil {
		t.Fatalf("failed to read broadcast: %v", err)
	}

	payload, _ := resp["payload"].(map[string]interface{})
	if payload["channel_id"] != float64(1) {
		t.Errorf("expected channel_id=1, got %v", payload["channel_id"])
	}
	if payload["raw_hex"] != "aabbcc" {
		t.Errorf("expected raw_hex=aabbcc, got %v", payload["raw_hex"])
	}
}

func TestWSHandler_SendCommand_ForbiddenForViewer(t *testing.T) {
	gin.SetMode(gin.TestMode)

	hub := websocket.NewHub()
	go hub.Run()

	h := NewWSHandler(hub, mockHistoryFetcher, mockWriteSender)

	r := gin.New()
	r.GET("/ws/terminal", h.HandleTerminalWS)

	server := httptest.NewServer(r)
	defer server.Close()

	// Connect as viewer (non-admin)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/terminal?token=" + generateTestToken("viewer")
	conn, _, err := wslib.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer conn.Close()

	// Try to send a command (should be forbidden)
	sendMsg := map[string]interface{}{
		"type": "send",
		"payload": map[string]interface{}{
			"device_id":  "test-device",
			"channel_id": 1,
			"data_hex":   "0102",
			"read_size":  2,
		},
	}
	msgBytes, _ := json.Marshal(sendMsg)
	if err := conn.WriteMessage(wslib.TextMessage, msgBytes); err != nil {
		t.Fatalf("failed to send: %v", err)
	}

	// Read response
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("failed to read: %v", err)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(msg, &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if resp["type"] != "error" {
		t.Errorf("expected error response, got %v", resp["type"])
	}

	payload, ok := resp["payload"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected payload to be map, got %T", resp["payload"])
	}

	if !strings.Contains(payload["message"].(string), "forbidden") {
		t.Errorf("expected forbidden message, got %v", payload["message"])
	}
}
