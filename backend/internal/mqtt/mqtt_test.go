package mqtt

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	pahomqtt "github.com/eclipse/paho.mqtt.golang"
)

type testToken struct {
	done chan struct{}
	err  error
}

func completedToken(err error) *testToken {
	t := &testToken{done: make(chan struct{}), err: err}
	close(t.done)
	return t
}

func pendingToken() *testToken { return &testToken{done: make(chan struct{})} }

func (t *testToken) Wait() bool {
	<-t.done
	return true
}

func (t *testToken) WaitTimeout(timeout time.Duration) bool {
	select {
	case <-t.done:
		return true
	case <-time.After(timeout):
		return false
	}
}

func (t *testToken) Done() <-chan struct{} { return t.done }
func (t *testToken) Error() error          { return t.err }

type fakePublish struct {
	topic    string
	qos      byte
	retained bool
	payload  interface{}
}

type fakeClient struct {
	mu sync.Mutex

	connectToken   pahomqtt.Token
	subscribeToken pahomqtt.Token
	publishToken   pahomqtt.Token
	onLost         pahomqtt.ConnectionLostHandler

	connectCalled    chan struct{}
	subscribeCalled  chan struct{}
	publishCalled    chan struct{}
	disconnectCalled chan struct{}
	connectOnce      sync.Once
	subscribeOnce    sync.Once
	publishOnce      sync.Once
	disconnectOnce   sync.Once

	publishEntered chan struct{}
	releasePublish chan struct{}
	publishGate    sync.Once

	disconnected  bool
	subscriptions []string
	publishes     []fakePublish
	message       pahomqtt.MessageHandler
}

func newFakeClient() *fakeClient {
	return &fakeClient{
		connectToken:     completedToken(nil),
		subscribeToken:   completedToken(nil),
		publishToken:     completedToken(nil),
		connectCalled:    make(chan struct{}),
		subscribeCalled:  make(chan struct{}),
		publishCalled:    make(chan struct{}),
		disconnectCalled: make(chan struct{}),
	}
}

func (f *fakeClient) IsConnected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.disconnected
}

func (f *fakeClient) IsConnectionOpen() bool { return f.IsConnected() }

func (f *fakeClient) Connect() pahomqtt.Token {
	f.connectOnce.Do(func() { close(f.connectCalled) })
	return f.connectToken
}

func (f *fakeClient) Disconnect(uint) {
	f.mu.Lock()
	f.disconnected = true
	f.mu.Unlock()
	f.disconnectOnce.Do(func() { close(f.disconnectCalled) })
}

func (f *fakeClient) Publish(topic string, qos byte, retained bool, payload interface{}) pahomqtt.Token {
	f.mu.Lock()
	f.publishes = append(f.publishes, fakePublish{topic: topic, qos: qos, retained: retained, payload: payload})
	f.mu.Unlock()
	f.publishOnce.Do(func() { close(f.publishCalled) })
	if f.publishEntered != nil {
		f.publishGate.Do(func() { close(f.publishEntered) })
		<-f.releasePublish
	}
	return f.publishToken
}

func (f *fakeClient) Subscribe(topic string, _ byte, callback pahomqtt.MessageHandler) pahomqtt.Token {
	f.mu.Lock()
	f.subscriptions = append(f.subscriptions, topic)
	f.message = callback
	f.mu.Unlock()
	f.subscribeOnce.Do(func() { close(f.subscribeCalled) })
	return f.subscribeToken
}

func (f *fakeClient) SubscribeMultiple(map[string]byte, pahomqtt.MessageHandler) pahomqtt.Token {
	return completedToken(nil)
}

func (f *fakeClient) Unsubscribe(...string) pahomqtt.Token     { return completedToken(nil) }
func (f *fakeClient) AddRoute(string, pahomqtt.MessageHandler) {}
func (f *fakeClient) OptionsReader() pahomqtt.ClientOptionsReader {
	return pahomqtt.ClientOptionsReader{}
}

func (f *fakeClient) lose(err error) {
	f.mu.Lock()
	callback := f.onLost
	f.mu.Unlock()
	if callback != nil {
		callback(f, err)
	}
}

type fakeFactory struct {
	t       *testing.T
	mu      sync.Mutex
	clients []*fakeClient
	next    int
	created chan *fakeClient
	options []*pahomqtt.ClientOptions
}

func installFactory(t *testing.T, clients ...*fakeClient) *fakeFactory {
	t.Helper()
	factory := &fakeFactory{t: t, clients: clients, created: make(chan *fakeClient, len(clients))}
	previous := newPahoClient
	newPahoClient = func(options *pahomqtt.ClientOptions) pahomqtt.Client {
		factory.mu.Lock()
		defer factory.mu.Unlock()
		if factory.next >= len(factory.clients) {
			t.Fatalf("unexpected MQTT client creation %d", factory.next+1)
		}
		client := factory.clients[factory.next]
		factory.next++
		factory.options = append(factory.options, options)
		client.mu.Lock()
		client.onLost = options.OnConnectionLost
		client.mu.Unlock()
		factory.created <- client
		return client
	}
	t.Cleanup(func() { newPahoClient = previous })
	return factory
}

func newConfiguredClient() *Client {
	c := New("tcp://test:1883", "", "")
	c.SetHandler(func(string, []byte) {})
	c.backoff = func(int) time.Duration { return 0 }
	return c
}

func runClient(c *Client) <-chan error {
	done := make(chan error, 1)
	go func() { done <- c.Run(context.Background()) }()
	return done
}

func mustReceive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for synchronization event")
		var zero T
		return zero
	}
}

func mustNotReceive[T any](t *testing.T, ch <-chan T) {
	t.Helper()
	select {
	case <-ch:
		t.Fatal("unexpected synchronization event")
	default:
	}
}

func TestTopicHelpers(t *testing.T) {
	if got := TopicForNode("ABC"); got != "nodes/ABC/down" {
		t.Fatalf("TopicForNode = %q", got)
	}
	if got := ControlTopicForNode("ABC"); got != "nodes/ABC/control" {
		t.Fatalf("ControlTopicForNode = %q", got)
	}
}

func TestNewDoesNotCreateOrConnectUntilRun(t *testing.T) {
	fake := newFakeClient()
	factory := installFactory(t, fake)
	c := newConfiguredClient()
	mustNotReceive(t, factory.created)
	mustNotReceive(t, fake.connectCalled)

	runDone := runClient(c)
	mustReceive(t, fake.subscribeCalled)
	mustReceive(t, c.Ready())
	if err := c.Run(context.Background()); !errors.Is(err, errRunAlreadyStarted) {
		t.Fatalf("concurrent second Run = %v, want %v", err, errRunAlreadyStarted)
	}

	factory.mu.Lock()
	options := factory.options[0]
	factory.mu.Unlock()
	if options.AutoReconnect || options.ConnectRetry {
		t.Fatalf("Paho recovery must be disabled: AutoReconnect=%v ConnectRetry=%v", options.AutoReconnect, options.ConnectRetry)
	}
	if !options.CleanSession {
		t.Fatal("backend MQTT transport must use a clean session")
	}
	if options.Order {
		t.Fatal("Paho message routing must not serialize blocking business handlers")
	}
	c.Close()
	if err := mustReceive(t, runDone); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

func TestClientIDIsUniquePerClientAndStableAcrossAttempts(t *testing.T) {
	t.Setenv("EHOME_MQTT_CLIENT_ID", "test-backend")
	first := New("tcp://test:1883", "", "")
	second := New("tcp://test:1883", "", "")

	if !strings.HasPrefix(first.clientID, "test-backend-") {
		t.Fatalf("first client ID = %q, want configured prefix", first.clientID)
	}
	if first.clientID == second.clientID {
		t.Fatalf("different Client instances reused ID %q", first.clientID)
	}
	firstAttemptID := first.buildOptions(&transportAttempt{generation: 1}).ClientID
	secondAttemptID := first.buildOptions(&transportAttempt{generation: 2}).ClientID
	if firstAttemptID != first.clientID || secondAttemptID != first.clientID {
		t.Fatalf("client ID changed across attempts: client=%q attempt1=%q attempt2=%q", first.clientID, firstAttemptID, secondAttemptID)
	}
}

func TestRunRequiresHandlerAndCanOnlyRunOnce(t *testing.T) {
	c := New("tcp://test:1883", "", "")
	err := c.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "handler") {
		t.Fatalf("Run without handler = %v", err)
	}
	if err := c.Run(context.Background()); !errors.Is(err, errRunAlreadyStarted) {
		t.Fatalf("second Run = %v, want %v", err, errRunAlreadyStarted)
	}
	c.Close()
}

func TestInitialFailureRecoversInSameRunWithFreshClient(t *testing.T) {
	first := newFakeClient()
	first.connectToken = completedToken(errors.New("broker unavailable"))
	second := newFakeClient()
	installFactory(t, first, second)
	c := newConfiguredClient()
	runDone := runClient(c)

	mustReceive(t, first.disconnectCalled)
	mustReceive(t, second.subscribeCalled)
	mustReceive(t, c.Ready())
	if err := c.LastError(); err != nil {
		t.Fatalf("LastError after recovery = %v", err)
	}
	c.Close()
	if err := mustReceive(t, runDone); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

func TestSubscribeFailureRecoversWithFreshClient(t *testing.T) {
	first := newFakeClient()
	first.subscribeToken = completedToken(errors.New("SUBACK rejected"))
	second := newFakeClient()
	installFactory(t, first, second)
	c := newConfiguredClient()
	runDone := runClient(c)

	mustReceive(t, first.disconnectCalled)
	mustReceive(t, second.subscribeCalled)
	mustReceive(t, c.Ready())
	c.Close()
	if err := mustReceive(t, runDone); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

func TestLostDuringSubscribeCannotCommitReady(t *testing.T) {
	first := newFakeClient()
	first.subscribeToken = pendingToken()
	second := newFakeClient()
	second.connectToken = pendingToken()
	installFactory(t, first, second)
	c := newConfiguredClient()
	runDone := runClient(c)

	mustReceive(t, first.subscribeCalled)
	first.lose(errors.New("lost during SUBACK"))
	mustReceive(t, first.disconnectCalled)
	mustReceive(t, second.connectCalled)
	mustNotReceive(t, c.Ready())
	if err := c.LastError(); err == nil || !strings.Contains(err.Error(), "lost during SUBACK") {
		t.Fatalf("LastError lost broker detail: %v", err)
	}

	c.Close()
	if err := mustReceive(t, runDone); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

func TestStaleAttemptCallbackDoesNotInvalidateCurrent(t *testing.T) {
	first := newFakeClient()
	second := newFakeClient()
	installFactory(t, first, second)
	c := newConfiguredClient()
	runDone := runClient(c)
	mustReceive(t, c.Ready())

	first.lose(errors.New("first lost"))
	mustReceive(t, second.subscribeCalled)
	if err := c.Publish("test/current", []byte("one")); err != nil {
		t.Fatalf("Publish on second attempt: %v", err)
	}
	first.lose(errors.New("stale callback"))

	c.mu.RLock()
	current := c.current
	connected := current != nil && current.connected && current.client == second
	c.mu.RUnlock()
	if !connected {
		t.Fatal("stale callback invalidated current attempt")
	}
	if err := c.PublishRetained("test/current", []byte("two")); err != nil {
		t.Fatalf("Publish after stale callback: %v", err)
	}

	second.mu.Lock()
	count := len(second.publishes)
	second.mu.Unlock()
	if count != 2 {
		t.Fatalf("current client publish count = %d, want 2", count)
	}
	c.Close()
	mustReceive(t, runDone)
}

func TestConnectionLostLatestStateWinsWhenWakeChannelFull(t *testing.T) {
	c := newConfiguredClient()
	fake := newFakeClient()
	attempt := &transportAttempt{client: fake, generation: 1, connected: true, lostCh: make(chan struct{})}
	options := c.buildOptions(attempt)
	fake.onLost = options.OnConnectionLost
	c.mu.Lock()
	c.current = attempt
	c.mu.Unlock()
	c.lostWake <- struct{}{}

	returned := make(chan struct{})
	go func() {
		fake.lose(errors.New("full mailbox"))
		close(returned)
	}()
	mustReceive(t, returned)

	c.mu.RLock()
	current := c.current
	lastErr := c.lastErr
	c.mu.RUnlock()
	if current != nil || lastErr == nil {
		t.Fatalf("callback final state current=%v lastErr=%v", current, lastErr)
	}
	c.Close()
}

func TestCallbackDoesNotWaitForBlockedPublicOperationAndTeardownDoes(t *testing.T) {
	first := newFakeClient()
	first.publishEntered = make(chan struct{})
	first.releasePublish = make(chan struct{})
	second := newFakeClient()
	second.connectToken = pendingToken()
	installFactory(t, first, second)
	c := newConfiguredClient()
	runDone := runClient(c)
	mustReceive(t, c.Ready())

	publishDone := make(chan error, 1)
	go func() { publishDone <- c.Publish("test/barrier", []byte("payload")) }()
	mustReceive(t, first.publishEntered)

	callbackDone := make(chan struct{})
	go func() {
		first.lose(errors.New("lost during publish"))
		close(callbackDone)
	}()
	mustReceive(t, callbackDone)
	mustNotReceive(t, first.disconnectCalled)

	close(first.releasePublish)
	if err := mustReceive(t, publishDone); err == nil {
		t.Fatal("stale publish must fail closed")
	}
	mustReceive(t, first.disconnectCalled)
	mustReceive(t, second.connectCalled)
	c.Close()
	mustReceive(t, runDone)
}

func TestCloseInterruptsBlockedConnectAndWaitsForTeardown(t *testing.T) {
	fake := newFakeClient()
	fake.connectToken = pendingToken()
	installFactory(t, fake)
	c := newConfiguredClient()
	runDone := runClient(c)
	mustReceive(t, fake.connectCalled)

	closeDone := make(chan struct{}, 4)
	for i := 0; i < cap(closeDone); i++ {
		go func() {
			c.Close()
			closeDone <- struct{}{}
		}()
	}
	mustReceive(t, fake.disconnectCalled)
	for i := 0; i < cap(closeDone); i++ {
		mustReceive(t, closeDone)
	}
	if err := mustReceive(t, runDone); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	c.Close()
}

func TestCloseInterruptsBlockedSubscribeWithoutReady(t *testing.T) {
	fake := newFakeClient()
	fake.subscribeToken = pendingToken()
	installFactory(t, fake)
	c := newConfiguredClient()
	runDone := runClient(c)
	mustReceive(t, fake.subscribeCalled)

	c.Close()
	mustReceive(t, fake.disconnectCalled)
	mustNotReceive(t, c.Ready())
	if err := mustReceive(t, runDone); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

func TestCloseBeforeRunMakesRunFailClosed(t *testing.T) {
	c := newConfiguredClient()
	c.Close()
	if err := c.Run(context.Background()); !errors.Is(err, errClientClosed) {
		t.Fatalf("Run after Close = %v, want %v", err, errClientClosed)
	}
	if err := c.Publish("test", nil); err == nil {
		t.Fatal("Publish after Close must fail")
	}
}

func TestPublishVariantsAndTokenErrors(t *testing.T) {
	fake := newFakeClient()
	installFactory(t, fake)
	c := newConfiguredClient()
	runDone := runClient(c)
	mustReceive(t, c.Ready())

	if err := c.Publish("qos1", []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := c.PublishQoS2("qos2", []byte("two")); err != nil {
		t.Fatal(err)
	}
	if err := c.PublishRetained("retained", []byte("three")); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	got := append([]fakePublish(nil), fake.publishes...)
	fake.mu.Unlock()
	if len(got) != 3 || got[0].qos != 1 || got[0].retained || got[1].qos != 2 || !got[2].retained {
		t.Fatalf("unexpected publishes: %#v", got)
	}

	fake.mu.Lock()
	fake.publishToken = completedToken(errors.New("publish rejected"))
	fake.mu.Unlock()
	if err := c.Publish("error", nil); err == nil || !strings.Contains(err.Error(), "publish rejected") {
		t.Fatalf("Publish token error = %v", err)
	}
	c.Close()
	mustReceive(t, runDone)
}

func TestReadyIsOneShotAcrossReconnect(t *testing.T) {
	first := newFakeClient()
	second := newFakeClient()
	installFactory(t, first, second)
	c := newConfiguredClient()
	ready := c.Ready()
	runDone := runClient(c)
	mustReceive(t, ready)

	first.lose(errors.New("reconnect"))
	mustReceive(t, second.subscribeCalled)
	if c.Ready() != ready {
		t.Fatal("Ready channel was replaced after disconnect")
	}
	mustReceive(t, ready)
	c.Close()
	mustReceive(t, runDone)
}

type fakeMessage struct {
	topic   string
	payload []byte
}

func (m *fakeMessage) Duplicate() bool   { return false }
func (m *fakeMessage) Qos() byte         { return 1 }
func (m *fakeMessage) Retained() bool    { return false }
func (m *fakeMessage) Topic() string     { return m.topic }
func (m *fakeMessage) MessageID() uint16 { return 1 }
func (m *fakeMessage) Payload() []byte   { return m.payload }
func (m *fakeMessage) Ack()              {}

func TestStaleAttemptMessageIsIgnored(t *testing.T) {
	first := newFakeClient()
	second := newFakeClient()
	installFactory(t, first, second)
	got := make(chan string, 2)
	c := New("tcp://test:1883", "", "")
	c.backoff = func(int) time.Duration { return 0 }
	c.SetHandler(func(topic string, _ []byte) { got <- topic })
	runDone := runClient(c)
	mustReceive(t, c.Ready())
	first.mu.Lock()
	firstHandler := first.message
	first.mu.Unlock()

	first.lose(errors.New("reconnect"))
	mustReceive(t, second.subscribeCalled)
	if err := c.Publish("sync", nil); err != nil {
		t.Fatal(err)
	}
	firstHandler(first, &fakeMessage{topic: "stale"})
	mustNotReceive(t, got)

	second.mu.Lock()
	secondHandler := second.message
	second.mu.Unlock()
	secondHandler(second, &fakeMessage{topic: "current"})
	if topic := mustReceive(t, got); topic != "current" {
		t.Fatalf("handler topic = %q", topic)
	}
	c.Close()
	mustReceive(t, runDone)
}
