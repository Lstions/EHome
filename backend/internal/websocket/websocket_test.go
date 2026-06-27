package websocket

import (
	"encoding/json"
	"testing"
	"time"
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
