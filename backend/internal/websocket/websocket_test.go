package websocket

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	gorilla "github.com/gorilla/websocket"
)

func TestNewHub(t *testing.T) {
	hub := NewHub()
	if hub == nil {
		t.Fatal("expected non-nil hub")
	}
	if hub.clients == nil {
		t.Error("hub.clients should be initialized")
	}
	if hub.broadcast == nil {
		t.Error("hub.broadcast should be initialized")
	}
	if hub.register == nil {
		t.Error("hub.register should be initialized")
	}
	if hub.unregister == nil {
		t.Error("hub.unregister should be initialized")
	}
	if hub.subscribers == nil {
		t.Error("hub.subscribers should be initialized")
	}
}

func TestHubBroadcastEvent(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	// Subscribe to capture events
	ch := hub.Subscribe()

	// Broadcast an event
	hub.BroadcastEvent("test_event", map[string]interface{}{
		"key": "value",
	})

	// Wait for event delivery
	select {
	case evt := <-ch:
		if evt.Type != "test_event" {
			t.Errorf("expected event type test_event, got %s", evt.Type)
		}
		payload, ok := evt.Payload.(map[string]interface{})
		if !ok {
			t.Fatal("expected map payload")
		}
		if payload["key"] != "value" {
			t.Errorf("expected payload.key=value, got %v", payload["key"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
	}

	hub.Unsubscribe(ch)
}

func TestHubSubscribeUnsubscribe(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	ch := hub.Subscribe()
	if ch == nil {
		t.Fatal("expected non-nil subscription channel")
	}

	// Unsubscribe should not panic
	hub.Unsubscribe(ch)
}

func TestHubMultipleSubscribers(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	ch1 := hub.Subscribe()
	ch2 := hub.Subscribe()

	hub.BroadcastEvent("multi_test", "hello")

	// Both subscribers should receive the event
	for i, ch := range []chan Event{ch1, ch2} {
		select {
		case evt := <-ch:
			if evt.Type != "multi_test" {
				t.Errorf("subscriber %d: expected multi_test, got %s", i, evt.Type)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("subscriber %d: timed out", i)
		}
	}

	hub.Unsubscribe(ch1)
	hub.Unsubscribe(ch2)
}

func TestHubBroadcast(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	ch := hub.Subscribe()

	msg, _ := json.Marshal(Event{Type: "raw_test", Payload: 42})
	hub.Broadcast(msg)

	select {
	case evt := <-ch:
		if evt.Type != "raw_test" {
			t.Errorf("expected raw_test, got %s", evt.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for raw broadcast")
	}

	hub.Unsubscribe(ch)
}

func TestHubSetOnMessage(t *testing.T) {
	hub := NewHub()

	called := false
	hub.SetOnMessage(func(client *Client, evt Event) {
		called = true
	})

	hub.onMsgMu.RLock()
	handler := hub.onMessage
	hub.onMsgMu.RUnlock()

	if handler == nil {
		t.Error("expected onMessage handler to be set")
	}

	// Verify the handler can be called (through the field)
	if !called {
		// It should not be called yet since we only set it
		// Now invoke it
		handler(&Client{}, Event{Type: "test"})
		if !called {
			t.Error("expected handler to be called")
		}
	}
}

func TestEventJSON(t *testing.T) {
	evt := Event{
		Type:    "node_status",
		Payload: map[string]interface{}{"node_id": "ABC", "status": "online"},
	}

	data, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("failed to marshal event: %v", err)
	}

	var parsed Event
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal event: %v", err)
	}
	if parsed.Type != "node_status" {
		t.Errorf("expected type node_status, got %s", parsed.Type)
	}
}

func TestHubBroadcastNoSubscribers(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	// Should not panic with no subscribers
	hub.BroadcastEvent("no_sub", nil)

	// Give a small window for the broadcast to be processed
	time.Sleep(50 * time.Millisecond)
}

func TestHubBroadcastEventToRoleOnlySendsToMatchingExternalClients(t *testing.T) {
	hub := NewHub()
	clients := map[string]*Client{
		"admin":    {Role: "admin", send: make(chan []byte, 1)},
		"operator": {Role: "operator", send: make(chan []byte, 1)},
		"viewer":   {Role: "viewer", send: make(chan []byte, 1)},
		"empty":    {Role: "", send: make(chan []byte, 1)},
		"unknown":  {Role: "unknown", send: make(chan []byte, 1)},
	}
	for _, client := range clients {
		hub.clients[client] = true
	}
	subscriber := hub.Subscribe()
	defer hub.Unsubscribe(subscriber)
	go hub.Run()

	hub.BroadcastEventToRole("node_log", map[string]interface{}{"node_id": "node-1"}, "admin")

	select {
	case evt := <-subscriber:
		if evt.Type != "node_log" {
			t.Fatalf("subscriber event type = %q, want node_log", evt.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for internal subscriber")
	}

	assertClientEventType(t, clients["admin"], "node_log")
	for _, role := range []string{"operator", "viewer", "empty", "unknown"} {
		select {
		case message := <-clients[role].send:
			t.Fatalf("%s client unexpectedly received restricted event: %s", role, message)
		default:
		}
	}
}

func TestHubBroadcastEventToRoleEmptyTargetFailsClosed(t *testing.T) {
	tests := []struct {
		name       string
		targetRole string
	}{
		{name: "empty", targetRole: ""},
		{name: "whitespace", targetRole: " 	\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hub := NewHub()
			clients := []*Client{
				{Role: "admin", send: make(chan []byte, 1)},
				{Role: "operator", send: make(chan []byte, 1)},
				{Role: "", send: make(chan []byte, 1)},
			}
			for _, client := range clients {
				hub.clients[client] = true
			}
			subscriber := hub.Subscribe()
			defer hub.Unsubscribe(subscriber)
			go hub.Run()

			hub.BroadcastEventToRole("node_log", nil, tt.targetRole)

			select {
			case evt := <-subscriber:
				if evt.Type != "node_log" {
					t.Fatalf("subscriber event type = %q, want node_log", evt.Type)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for trusted internal subscriber")
			}
			for _, client := range clients {
				select {
				case message := <-client.send:
					t.Fatalf("client role %q unexpectedly received fail-closed event: %s", client.Role, message)
				default:
				}
			}
		})
	}
}

func TestHubBroadcastEventStillSendsToAllRoles(t *testing.T) {
	hub := NewHub()
	clients := []*Client{
		{Role: "admin", send: make(chan []byte, 1)},
		{Role: "operator", send: make(chan []byte, 1)},
		{Role: "viewer", send: make(chan []byte, 1)},
	}
	for _, client := range clients {
		hub.clients[client] = true
	}
	subscriber := hub.Subscribe()
	defer hub.Unsubscribe(subscriber)
	go hub.Run()

	hub.BroadcastEvent("data_update", map[string]interface{}{"value": 42})

	select {
	case <-subscriber: // subscriber delivery happens after external client delivery
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for broadcast processing")
	}
	for _, client := range clients {
		assertClientEventType(t, client, "data_update")
	}
}

func TestHubRestrictedBroadcastRemovesOnlySlowMatchingClients(t *testing.T) {
	hub := NewHub()
	slowAdmin := &Client{Role: "admin", send: make(chan []byte)}
	slowViewer := &Client{Role: "viewer", send: make(chan []byte)}
	hub.clients[slowAdmin] = true
	hub.clients[slowViewer] = true
	subscriber := hub.Subscribe()
	defer hub.Unsubscribe(subscriber)
	go hub.Run()

	hub.BroadcastEventToRole("node_log", nil, "admin")
	select {
	case <-subscriber:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for restricted broadcast processing")
	}

	hub.mu.RLock()
	_, hasAdmin := hub.clients[slowAdmin]
	_, hasViewer := hub.clients[slowViewer]
	hub.mu.RUnlock()
	if hasAdmin {
		t.Error("slow matching client should be removed")
	}
	if !hasViewer {
		t.Error("non-matching client should not be affected by restricted broadcast")
	}
}

func TestHandleWebSocketCopiesAuthenticatedRoleFromContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	hub := NewHub()
	received := make(chan string, 1)
	hub.SetOnMessage(func(client *Client, _ Event) {
		received <- client.Role
	})
	go hub.Run()

	router := gin.New()
	router.GET("/ws", func(c *gin.Context) {
		c.Set("role", "admin")
		hub.HandleWebSocket(c)
	})
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + server.URL[len("http"):] + "/ws"
	conn, _, err := gorilla.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()
	if err := conn.WriteJSON(Event{Type: "probe"}); err != nil {
		t.Fatalf("write websocket event: %v", err)
	}

	select {
	case role := <-received:
		if role != "admin" {
			t.Fatalf("client role = %q, want admin", role)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for websocket message handler")
	}
}

func assertClientEventType(t *testing.T, client *Client, want string) {
	t.Helper()
	select {
	case message := <-client.send:
		var evt Event
		if err := json.Unmarshal(message, &evt); err != nil {
			t.Fatalf("unmarshal client event: %v", err)
		}
		if evt.Type != want {
			t.Fatalf("client event type = %q, want %q", evt.Type, want)
		}
	default:
		t.Fatalf("client did not receive %q event", want)
	}
}
