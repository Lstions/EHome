package mqtt

import (
	"errors"
	"sync"
	"testing"
	"time"

	"ehome/backend/pkg/logger"

	pahomqtt "github.com/eclipse/paho.mqtt.golang"
)

func init() {
	logger.Init("warn")
}

// mockToken implements pahomqtt.Token
type mockToken struct {
	err   error
	wait  bool
	done  chan struct{}
}

func (t *mockToken) Wait() bool { return t.wait }
func (t *mockToken) WaitTimeout(d time.Duration) bool { return t.wait }
func (t *mockToken) Done() <-chan struct{} {
	if t.done == nil {
		t.done = make(chan struct{})
		close(t.done)
	}
	return t.done
}
func (t *mockToken) Error() error { return t.err }

// mockPahoClient implements pahomqtt.Client for testing
type mockPahoClient struct {
	mu         sync.Mutex
	published  []mockPublish
	subscribed []mockSubscribe
	disconnected bool
}

type mockPublish struct {
	topic    string
	qos      byte
	retained bool
	payload  interface{}
}

type mockSubscribe struct {
	topic    string
	qos      byte
	callback pahomqtt.MessageHandler
}

func (m *mockPahoClient) IsConnected() bool { return !m.disconnected }
func (m *mockPahoClient) IsConnectionOpen() bool { return !m.disconnected }
func (m *mockPahoClient) Connect() pahomqtt.Token { return &mockToken{wait: true} }
func (m *mockPahoClient) Disconnect(quiesce uint) { m.disconnected = true }
func (m *mockPahoClient) Publish(topic string, qos byte, retained bool, payload interface{}) pahomqtt.Token {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.published = append(m.published, mockPublish{topic, qos, retained, payload})
	return &mockToken{wait: true}
}
func (m *mockPahoClient) Subscribe(topic string, qos byte, callback pahomqtt.MessageHandler) pahomqtt.Token {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.subscribed = append(m.subscribed, mockSubscribe{topic, qos, callback})
	return &mockToken{wait: true}
}
func (m *mockPahoClient) SubscribeMultiple(filters map[string]byte, callback pahomqtt.MessageHandler) pahomqtt.Token {
	return &mockToken{wait: true}
}
func (m *mockPahoClient) Unsubscribe(topics ...string) pahomqtt.Token { return &mockToken{wait: true} }
func (m *mockPahoClient) AddRoute(topic string, callback pahomqtt.MessageHandler) {}
func (m *mockPahoClient) OptionsReader() pahomqtt.ClientOptionsReader {
	return pahomqtt.ClientOptionsReader{}
}

// timeoutPahoClient returns tokens that timeout
type timeoutPahoClient struct{}

func (t *timeoutPahoClient) IsConnected() bool                                                { return true }
func (t *timeoutPahoClient) IsConnectionOpen() bool                                           { return true }
func (t *timeoutPahoClient) Connect() pahomqtt.Token                                          { return &mockToken{wait: true} }
func (t *timeoutPahoClient) Disconnect(quiesce uint)                                          {}
func (t *timeoutPahoClient) Publish(topic string, qos byte, retained bool, payload interface{}) pahomqtt.Token {
	return &mockToken{wait: false} // timeout
}
func (t *timeoutPahoClient) Subscribe(topic string, qos byte, callback pahomqtt.MessageHandler) pahomqtt.Token {
	return &mockToken{wait: true}
}
func (t *timeoutPahoClient) SubscribeMultiple(filters map[string]byte, callback pahomqtt.MessageHandler) pahomqtt.Token {
	return &mockToken{wait: true}
}
func (t *timeoutPahoClient) Unsubscribe(topics ...string) pahomqtt.Token { return &mockToken{wait: true} }
func (t *timeoutPahoClient) AddRoute(topic string, callback pahomqtt.MessageHandler)                      {}
func (t *timeoutPahoClient) OptionsReader() pahomqtt.ClientOptionsReader {
	return pahomqtt.ClientOptionsReader{}
}

// errorPahoClient returns tokens with errors
type errorPahoClient struct{}

func (e *errorPahoClient) IsConnected() bool                                                { return true }
func (e *errorPahoClient) IsConnectionOpen() bool                                           { return true }
func (e *errorPahoClient) Connect() pahomqtt.Token                                          { return &mockToken{wait: true, err: errors.New("connect failed")} }
func (e *errorPahoClient) Disconnect(quiesce uint)                                          {}
func (e *errorPahoClient) Publish(topic string, qos byte, retained bool, payload interface{}) pahomqtt.Token {
	return &mockToken{wait: true, err: errors.New("publish failed")}
}
func (e *errorPahoClient) Subscribe(topic string, qos byte, callback pahomqtt.MessageHandler) pahomqtt.Token {
	return &mockToken{wait: true, err: errors.New("subscribe failed")}
}
func (e *errorPahoClient) SubscribeMultiple(filters map[string]byte, callback pahomqtt.MessageHandler) pahomqtt.Token {
	return &mockToken{wait: true}
}
func (e *errorPahoClient) Unsubscribe(topics ...string) pahomqtt.Token { return &mockToken{wait: true} }
func (e *errorPahoClient) AddRoute(topic string, callback pahomqtt.MessageHandler)                      {}
func (e *errorPahoClient) OptionsReader() pahomqtt.ClientOptionsReader {
	return pahomqtt.ClientOptionsReader{}
}

func newTestClient(paho pahomqtt.Client) *Client {
	return &Client{
		client: paho,
		topics: make(map[string]byte),
	}
}

// === Tests ===

func TestTopicForNode(t *testing.T) {
	tests := []struct {
		nodeID   string
		expected string
	}{
		{"ABC123", "nodes/ABC123/down"},
		{"F0F5BD02F35C", "nodes/F0F5BD02F35C/down"},
		{"", "nodes//down"},
	}

	for _, tt := range tests {
		got := TopicForNode(tt.nodeID)
		if got != tt.expected {
			t.Errorf("TopicForNode(%q) = %q, want %q", tt.nodeID, got, tt.expected)
		}
	}
}

func TestClientStructure(t *testing.T) {
	c := &Client{topics: make(map[string]byte)}
	if c.topics == nil {
		t.Error("topics map should be initialized")
	}
}

func TestClientSetHandler(t *testing.T) {
	c := &Client{topics: make(map[string]byte)}

	called := false
	c.SetHandler(func(topic string, payload []byte) {
		called = true
	})

	if c.handler == nil {
		t.Error("handler should be set")
	}

	// Call the handler directly
	c.handler("test/topic", []byte{1, 2, 3})
	if !called {
		t.Error("handler should have been called")
	}
}

func TestClientTopicTracking(t *testing.T) {
	c := &Client{topics: make(map[string]byte)}

	c.topics["nodes/+/up"] = 1
	c.topics["nodes/ABC/down"] = 1

	if len(c.topics) != 2 {
		t.Errorf("expected 2 tracked topics, got %d", len(c.topics))
	}
	if c.topics["nodes/+/up"] != 1 {
		t.Error("expected QoS 1 for nodes/+/up")
	}
}

func TestClient_Publish(t *testing.T) {
	paho := &mockPahoClient{}
	c := newTestClient(paho)

	err := c.Publish("test/topic", []byte("hello"))
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	paho.mu.Lock()
	defer paho.mu.Unlock()
	if len(paho.published) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(paho.published))
	}
	if paho.published[0].topic != "test/topic" {
		t.Errorf("topic: got %s, want test/topic", paho.published[0].topic)
	}
	if paho.published[0].qos != 1 {
		t.Errorf("qos: got %d, want 1", paho.published[0].qos)
	}
	if paho.published[0].retained {
		t.Error("expected retained=false")
	}
}

func TestClient_Publish_Error(t *testing.T) {
	paho := &errorPahoClient{}
	c := newTestClient(paho)

	err := c.Publish("test/topic", []byte("hello"))
	if err == nil {
		t.Error("expected error from publish")
	}
}

func TestClient_Publish_Timeout(t *testing.T) {
	paho := &timeoutPahoClient{}
	c := newTestClient(paho)

	err := c.Publish("test/topic", []byte("hello"))
	if err == nil {
		t.Error("expected timeout error from publish")
	}
	if err.Error() != "mqtt publish timeout" {
		t.Errorf("error message: got %s, want mqtt publish timeout", err.Error())
	}
}

func TestClient_PublishRetained(t *testing.T) {
	paho := &mockPahoClient{}
	c := newTestClient(paho)

	err := c.PublishRetained("test/topic", []byte("hello"))
	if err != nil {
		t.Fatalf("PublishRetained: %v", err)
	}

	paho.mu.Lock()
	defer paho.mu.Unlock()
	if len(paho.published) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(paho.published))
	}
	if !paho.published[0].retained {
		t.Error("expected retained=true")
	}
}

func TestClient_PublishRetained_Error(t *testing.T) {
	paho := &errorPahoClient{}
	c := newTestClient(paho)

	err := c.PublishRetained("test/topic", []byte("hello"))
	if err == nil {
		t.Error("expected error from PublishRetained")
	}
}

func TestClient_PublishRetained_Timeout(t *testing.T) {
	paho := &timeoutPahoClient{}
	c := newTestClient(paho)

	err := c.PublishRetained("test/topic", []byte("hello"))
	if err == nil {
		t.Error("expected timeout error")
	}
	if err.Error() != "mqtt publish timeout" {
		t.Errorf("error: got %s, want mqtt publish timeout", err.Error())
	}
}

func TestClient_PublishQoS2(t *testing.T) {
	paho := &mockPahoClient{}
	c := newTestClient(paho)

	err := c.PublishQoS2("test/topic", []byte("hello"))
	if err != nil {
		t.Fatalf("PublishQoS2: %v", err)
	}

	paho.mu.Lock()
	defer paho.mu.Unlock()
	if len(paho.published) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(paho.published))
	}
	if paho.published[0].qos != 2 {
		t.Errorf("qos: got %d, want 2", paho.published[0].qos)
	}
}

func TestClient_PublishQoS2_NilClient(t *testing.T) {
	c := &Client{topics: make(map[string]byte)} // client field is nil

	err := c.PublishQoS2("test/topic", []byte("hello"))
	if err == nil {
		t.Error("expected error for nil client")
	}
	if err.Error() != "MQTT client not connected" {
		t.Errorf("error: got %s, want MQTT client not connected", err.Error())
	}
}

func TestClient_PublishQoS2_Error(t *testing.T) {
	paho := &errorPahoClient{}
	c := newTestClient(paho)

	err := c.PublishQoS2("test/topic", []byte("hello"))
	if err == nil {
		t.Error("expected error from PublishQoS2")
	}
}

func TestClient_PublishQoS2_Timeout(t *testing.T) {
	paho := &timeoutPahoClient{}
	c := newTestClient(paho)

	err := c.PublishQoS2("test/topic", []byte("hello"))
	if err == nil {
		t.Error("expected timeout error")
	}
	if err.Error() != "QoS 2 publish timeout" {
		t.Errorf("error: got %s, want QoS 2 publish timeout", err.Error())
	}
}

func TestClient_Subscribe(t *testing.T) {
	paho := &mockPahoClient{}
	c := newTestClient(paho)

	err := c.Subscribe("nodes/+/up", 1)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// Check topic tracking
	c.mu.RLock()
	if c.topics["nodes/+/up"] != 1 {
		t.Errorf("topic tracking: got QoS %d, want 1", c.topics["nodes/+/up"])
	}
	c.mu.RUnlock()

	paho.mu.Lock()
	if len(paho.subscribed) != 1 {
		t.Fatalf("expected 1 subscribe, got %d", len(paho.subscribed))
	}
	if paho.subscribed[0].topic != "nodes/+/up" {
		t.Errorf("topic: got %s, want nodes/+/up", paho.subscribed[0].topic)
	}
	paho.mu.Unlock()
}

func TestClient_Subscribe_Error(t *testing.T) {
	paho := &errorPahoClient{}
	c := newTestClient(paho)

	err := c.Subscribe("nodes/+/up", 1)
	if err == nil {
		t.Error("expected error from Subscribe")
	}
}

func TestClient_Close(t *testing.T) {
	paho := &mockPahoClient{}
	c := newTestClient(paho)

	c.Close()

	if !paho.disconnected {
		t.Error("expected client to be disconnected")
	}
}

func TestClient_onMessage(t *testing.T) {
	c := &Client{topics: make(map[string]byte)}

	called := false
	var receivedTopic string
	var receivedPayload []byte
	c.SetHandler(func(topic string, payload []byte) {
		called = true
		receivedTopic = topic
		receivedPayload = payload
	})

	// Simulate onMessage callback
	c.onMessage(nil, &mockMessage{topic: "test/topic", payload: []byte("hello")})

	if !called {
		t.Error("handler should have been called")
	}
	if receivedTopic != "test/topic" {
		t.Errorf("topic: got %s, want test/topic", receivedTopic)
	}
	if string(receivedPayload) != "hello" {
		t.Errorf("payload: got %s, want hello", string(receivedPayload))
	}
}

func TestClient_onMessage_NilHandler(t *testing.T) {
	c := &Client{topics: make(map[string]byte)}

	// Should not panic with nil handler
	c.onMessage(nil, &mockMessage{topic: "test/topic", payload: []byte("hello")})
}

// mockMessage implements pahomqtt.Message
type mockMessage struct {
	topic   string
	payload []byte
}

func (m *mockMessage) Duplicate() bool                  { return false }
func (m *mockMessage) Qos() byte                        { return 1 }
func (m *mockMessage) Retained() bool                   { return false }
func (m *mockMessage) Topic() string                    { return m.topic }
func (m *mockMessage) MessageID() uint16                { return 1 }
func (m *mockMessage) Payload() []byte                  { return m.payload }
func (m *mockMessage) Ack()                             {}
