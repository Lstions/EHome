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

func TestHubBroadcastAuthenticatedEventSendsOnlyToValidSessions(t *testing.T) {
	hub := NewHub()
	hub.SetSessionValidator(func(subjectID uint, version int64) bool { return subjectID == 1 && version == 1 })
	valid := &Client{hub: hub, SubjectID: 1, SessionVersion: 1, ExpiresAt: time.Now().Add(time.Minute), send: make(chan []byte, 1)}
	invalid := &Client{hub: hub, SubjectID: 2, SessionVersion: 1, ExpiresAt: time.Now().Add(time.Minute), send: make(chan []byte, 1)}
	hub.clients[valid] = true
	hub.clients[invalid] = true
	subscriber := hub.Subscribe()
	defer hub.Unsubscribe(subscriber)
	go hub.Run()
	hub.BroadcastAuthenticatedEvent("node_log", nil)
	select {
	case <-subscriber:
	case <-time.After(2 * time.Second):
		t.Fatal("subscriber timeout")
	}
	assertClientEventType(t, valid, "node_log")
	select {
	case message := <-invalid.send:
		t.Fatalf("invalid session received %s", message)
	default:
	}
}

func TestHandleWebSocketCopiesAuthenticatedSubjectFromContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	hub := NewHub()
	hub.SetSessionValidator(func(subjectID uint, version int64) bool { return subjectID == 7 && version == 3 })
	received := make(chan uint, 1)
	hub.SetOnMessage(func(client *Client, _ Event) { received <- client.SubjectID })
	go hub.Run()
	router := gin.New()
	router.GET("/ws", func(c *gin.Context) {
		c.Set("subject_id", uint(7))
		c.Set("session_version", int64(3))
		c.Set("token_expires_at", time.Now().Add(time.Minute))
		c.Set("token_jti", "jti")
		hub.HandleWebSocket(c)
	})
	server := httptest.NewServer(router)
	defer server.Close()
	wsURL := "ws" + server.URL[len("http"):] + "/ws"
	conn, _, err := gorilla.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteJSON(Event{Type: "probe"}); err != nil {
		t.Fatal(err)
	}
	select {
	case subjectID := <-received:
		if subjectID != 7 {
			t.Fatalf("subject=%d", subjectID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
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
