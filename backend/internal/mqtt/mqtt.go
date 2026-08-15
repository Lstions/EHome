package mqtt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// Publisher is the interface for MQTT publishing operations.
// *Client implements this interface. Tests can provide mock implementations.
type Publisher interface {
	Publish(topic string, payload []byte) error
	PublishQoS2(topic string, payload []byte) error
	PublishRetained(topic string, payload []byte) error
}

// TelemetryPublisher is the optional best-effort publishing capability used for
// high-rate telemetry. It deliberately extends Publisher rather than widening
// the control-plane interface and breaking command-path implementations.
type TelemetryPublisher interface {
	PublishQoS0(topic string, payload []byte) error
}

type transportAttempt struct {
	client     mqtt.Client
	generation uint64
	connected  bool
	// lostCh broadcasts invalidation to every public operation using this
	// attempt. lostWake below remains the single-consumer supervisor wake-up.
	lostCh chan struct{}
}

// Client owns one supervised MQTT transport lifecycle. Run is the only method
// allowed to create, connect, subscribe, rebuild, disconnect, or destroy the
// underlying Paho client.
type Client struct {
	broker string
	user   string
	pass   string
	// clientID is unique per Client instance and stable across all supervised
	// transport attempts owned by that instance.
	clientID string

	mu             sync.RWMutex
	handler        func(topic string, payload []byte)
	requiredTopics map[string]byte
	current        *transportAttempt
	nextGeneration uint64
	lastErr        error
	runStarted     bool
	closeRequested bool

	// operationMu is the barrier between bounded public operations and Run's
	// teardown. Callbacks deliberately never acquire it.
	operationMu sync.Mutex

	initialReady     chan struct{}
	initialReadyOnce sync.Once
	lostWake         chan struct{} // cap=1, consumed only by Run
	closeCh          chan struct{}
	closeOnce        sync.Once
	runDone          chan struct{}
	runDoneOnce      sync.Once

	backoff func(int) time.Duration
}

var newPahoClient = mqtt.NewClient

var (
	clientIDSequence atomic.Uint64
	clientIDSeed     = uint64(time.Now().UnixNano()) ^ uint64(os.Getpid())<<32
)

var (
	errRunAlreadyStarted = errors.New("MQTT supervisor Run already started")
	errClientClosed      = errors.New("MQTT client closed")
	errAttemptLost       = errors.New("MQTT transport attempt lost")
)

const (
	connectTimeout   = 5 * time.Second
	subscribeTimeout = 5 * time.Second
	publishTimeout   = 5 * time.Second
	// publishQoS0Timeout bounds best-effort telemetry only. Control-plane QoS 1/2
	// keeps the longer timeout; a stuck broker must not let high-rate telemetry
	// holds starve command/config publishes on the shared operation mutex.
	publishQoS0Timeout = 1 * time.Second
	publishQoS2Time    = 10 * time.Second
)

func supervisorBackoff(attempt int) time.Duration {
	if attempt > 5 {
		attempt = 5
	}
	return time.Second << attempt
}

// New creates a disconnected supervised client. The underlying Paho client is
// not created until Run starts an attempt.
func New(broker, user, password string) *Client {
	return &Client{
		broker:         broker,
		user:           user,
		pass:           password,
		clientID:       nextClientID(),
		requiredTopics: map[string]byte{"nodes/+/up": 1},
		initialReady:   make(chan struct{}),
		lostWake:       make(chan struct{}, 1),
		closeCh:        make(chan struct{}),
		runDone:        make(chan struct{}),
		backoff:        supervisorBackoff,
	}
}

func nextClientID() string {
	prefix := strings.TrimSpace(os.Getenv("EHOME_MQTT_CLIENT_ID"))
	if prefix == "" {
		prefix = "ehome-server-v2"
	}
	sequence := clientIDSequence.Add(1)
	return fmt.Sprintf("%s-%016x-%x", prefix, clientIDSeed, sequence)
}

// Ready is a one-shot startup latch. It closes after this process has completed
// CONNECT and all required SUBACKs at least once. A later disconnect does not
// replace or reopen the channel.
func (c *Client) Ready() <-chan struct{} {
	if c == nil {
		return nil
	}
	return c.initialReady
}

// LastError returns the most recent transport failure. It is cleared when a
// fresh attempt completes all required subscriptions.
func (c *Client) LastError() error {
	if c == nil {
		return fmt.Errorf("MQTT client not created")
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastErr
}

// SetHandler sets the inbound message handler. It must be set before Run.
func (c *Client) SetHandler(handler func(topic string, payload []byte)) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.handler = handler
	c.mu.Unlock()
}

// Run owns the complete MQTT lifecycle and may be called only once. It keeps
// creating fresh clients until the context is cancelled or Close is requested.
func (c *Client) Run(ctx context.Context) error {
	if c == nil {
		return fmt.Errorf("MQTT client not created")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	c.mu.Lock()
	if c.runStarted {
		c.mu.Unlock()
		return errRunAlreadyStarted
	}
	c.runStarted = true
	if c.closeRequested {
		c.mu.Unlock()
		c.finishRun()
		return errClientClosed
	}
	if c.handler == nil {
		err := fmt.Errorf("MQTT message handler must be set before Run")
		c.lastErr = err
		c.mu.Unlock()
		c.finishRun()
		return err
	}
	c.mu.Unlock()
	defer c.finishRun()

	retry := 0
	for {
		if c.stopping(ctx) {
			return nil
		}

		attempt, err := c.installAttempt()
		if err != nil {
			return nil
		}

		err = c.runAttempt(ctx, attempt)
		c.teardownAttempt(attempt)
		if errors.Is(err, errClientClosed) || errors.Is(err, context.Canceled) {
			return nil
		}
		if err == nil {
			retry = 0
			continue
		}
		// The callback records the broker's concrete loss error before waking
		// Run; do not replace it with the internal attempt-lost sentinel.
		if !errors.Is(err, errAttemptLost) {
			c.recordError(err)
		}
		if !c.waitBackoff(ctx, c.backoff(retry)) {
			return nil
		}
		retry++
	}
}

func (c *Client) finishRun() {
	c.runDoneOnce.Do(func() { close(c.runDone) })
}

func (c *Client) stopping(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	case <-c.closeCh:
		return true
	default:
		return false
	}
}

func (c *Client) installAttempt() (*transportAttempt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closeRequested {
		return nil, errClientClosed
	}
	c.nextGeneration++
	attempt := &transportAttempt{generation: c.nextGeneration, lostCh: make(chan struct{})}
	attempt.client = newPahoClient(c.buildOptions(attempt))
	c.current = attempt
	return attempt, nil
}

func (c *Client) buildOptions(attempt *transportAttempt) *mqtt.ClientOptions {
	opts := mqtt.NewClientOptions().
		AddBroker(c.broker).
		SetClientID(c.clientID).
		SetCleanSession(true).
		SetOrderMatters(false).
		SetAutoReconnect(false).
		SetConnectRetry(false).
		SetKeepAlive(30 * time.Second).
		SetPingTimeout(10 * time.Second)
	if c.user != "" {
		opts.SetUsername(c.user).SetPassword(c.pass)
	}
	opts.SetConnectionLostHandler(func(client mqtt.Client, err error) {
		c.connectionLost(attempt, client, err)
	})
	return opts
}

// connectionLost is intentionally bounded: it only invalidates the matching
// attempt, records the error, and performs a best-effort wake-up.
func (c *Client) connectionLost(attempt *transportAttempt, client mqtt.Client, err error) {
	c.mu.Lock()
	if c.current != attempt || attempt.client != client {
		c.mu.Unlock()
		return
	}
	attempt.connected = false
	c.current = nil
	if err == nil {
		err = errAttemptLost
	}
	if !c.closeRequested {
		c.lastErr = fmt.Errorf("MQTT connection lost: %w", err)
	}
	close(attempt.lostCh)
	c.mu.Unlock()
	select {
	case c.lostWake <- struct{}{}:
	default:
	}
}

func (c *Client) runAttempt(ctx context.Context, attempt *transportAttempt) error {
	if err := c.establishAttempt(ctx, attempt); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return context.Canceled
		case <-c.closeCh:
			return errClientClosed
		case <-c.lostWake:
			c.mu.RLock()
			active := c.current == attempt && attempt.connected
			c.mu.RUnlock()
			if !active {
				return errAttemptLost
			}
		}
	}
}

func (c *Client) establishAttempt(ctx context.Context, attempt *transportAttempt) error {
	c.operationMu.Lock()
	defer c.operationMu.Unlock()

	connect := attempt.client.Connect()
	if err := c.waitToken(ctx, attempt, connect, connectTimeout); err != nil {
		return fmt.Errorf("connect to MQTT: %w", err)
	}
	if err := connect.Error(); err != nil {
		return fmt.Errorf("connect to MQTT: %w", err)
	}

	c.mu.Lock()
	if c.current != attempt || c.closeRequested {
		c.mu.Unlock()
		return errAttemptLost
	}
	attempt.connected = true
	c.mu.Unlock()

	for topic, qos := range c.requiredTopics {
		token := attempt.client.Subscribe(topic, qos, func(client mqtt.Client, msg mqtt.Message) {
			c.onMessage(attempt, client, msg)
		})
		if err := c.waitToken(ctx, attempt, token, subscribeTimeout); err != nil {
			return fmt.Errorf("subscribe %s: %w", topic, err)
		}
		if err := token.Error(); err != nil {
			return fmt.Errorf("subscribe %s: %w", topic, err)
		}
	}

	// Validation and the usable-state commit are one transaction. A stale or
	// invalidated attempt can never close the startup latch.
	c.mu.Lock()
	if c.current != attempt || !attempt.connected || c.closeRequested {
		c.mu.Unlock()
		return errAttemptLost
	}
	c.lastErr = nil
	c.initialReadyOnce.Do(func() { close(c.initialReady) })
	c.mu.Unlock()
	return nil
}

// waitToken waits for a bounded Paho operation without preventing Close or a
// matching connection-lost callback from interrupting the attempt.
func (c *Client) waitToken(ctx context.Context, attempt *transportAttempt, token mqtt.Token, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case <-token.Done():
			return nil
		case <-ctx.Done():
			return context.Canceled
		case <-c.closeCh:
			return errClientClosed
		case <-c.lostWake:
			c.mu.RLock()
			active := c.current == attempt
			c.mu.RUnlock()
			if !active {
				return errAttemptLost
			}
		case <-timer.C:
			return fmt.Errorf("timeout")
		}
	}
}

func (c *Client) teardownAttempt(attempt *transportAttempt) {
	c.operationMu.Lock()
	c.mu.Lock()
	if c.current == attempt {
		attempt.connected = false
		c.current = nil
	}
	c.mu.Unlock()
	if attempt != nil && attempt.client != nil {
		attempt.client.Disconnect(250)
	}
	c.operationMu.Unlock()
}

func (c *Client) waitBackoff(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return !c.stopping(ctx)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-c.closeCh:
		return false
	case <-timer.C:
		return true
	}
}

func (c *Client) recordError(err error) {
	c.mu.Lock()
	if !c.closeRequested {
		c.lastErr = err
	}
	c.mu.Unlock()
}

func (c *Client) onMessage(attempt *transportAttempt, client mqtt.Client, msg mqtt.Message) {
	if c == nil || msg == nil {
		return
	}
	c.mu.RLock()
	if c.current != attempt || attempt.client != client || !attempt.connected || c.closeRequested {
		c.mu.RUnlock()
		return
	}
	handler := c.handler
	c.mu.RUnlock()
	if handler != nil {
		handler(msg.Topic(), msg.Payload())
	}
}

func (c *Client) currentClientForOperation() (mqtt.Client, *transportAttempt, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closeRequested || c.current == nil || !c.current.connected || c.current.client == nil {
		return nil, nil, fmt.Errorf("MQTT client not connected")
	}
	return c.current.client, c.current, nil
}

func (c *Client) publish(topic string, qos byte, retained bool, payload []byte, timeout time.Duration, timeoutMessage string) error {
	if c == nil {
		return fmt.Errorf("MQTT client not connected")
	}
	c.operationMu.Lock()
	defer c.operationMu.Unlock()
	client, attempt, err := c.currentClientForOperation()
	if err != nil {
		return err
	}
	token := client.Publish(topic, qos, retained, payload)
	if err := c.waitPublicToken(attempt, token, timeout); err != nil {
		if errors.Is(err, errClientClosed) || errors.Is(err, errAttemptLost) {
			return fmt.Errorf("MQTT client not connected")
		}
		return fmt.Errorf("%s", timeoutMessage)
	}
	c.mu.RLock()
	stillCurrent := c.current == attempt && attempt.connected && !c.closeRequested
	c.mu.RUnlock()
	if !stillCurrent {
		return fmt.Errorf("MQTT client not connected")
	}
	return token.Error()
}

func (c *Client) waitPublicToken(attempt *transportAttempt, token mqtt.Token, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-token.Done():
		return nil
	case <-attempt.lostCh:
		return errAttemptLost
	case <-c.closeCh:
		return errClientClosed
	case <-timer.C:
		return fmt.Errorf("timeout")
	}
}

// Publish sends a message with QoS 1.
func (c *Client) Publish(topic string, payload []byte) error {
	return c.publish(topic, 1, false, payload, publishTimeout, "mqtt publish timeout")
}

// PublishQoS0 sends best-effort telemetry without waiting for a broker acknowledgement.
// It must not be used for device commands, configuration, OTA, or discovery messages.
func (c *Client) PublishQoS0(topic string, payload []byte) error {
	return c.publish(topic, 0, false, payload, publishQoS0Timeout, "mqtt QoS 0 publish timeout")
}

// PublishQoS2 sends a message with QoS 2.
func (c *Client) PublishQoS2(topic string, payload []byte) error {
	return c.publish(topic, 2, false, payload, publishQoS2Time, "QoS 2 publish timeout")
}

// PublishRetained sends a retained message with QoS 1.
func (c *Client) PublishRetained(topic string, payload []byte) error {
	return c.publish(topic, 1, true, payload, publishTimeout, "mqtt publish timeout")
}

// Close requests shutdown. If Run has started, Close waits until Run has
// crossed the operation barrier and disconnected its current client.
func (c *Client) Close() {
	if c == nil {
		return
	}
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closeRequested = true
		c.lastErr = errClientClosed
		c.mu.Unlock()
		close(c.closeCh)
	})
	c.mu.RLock()
	started := c.runStarted
	c.mu.RUnlock()
	if started {
		<-c.runDone
	}
}

// TopicForNode returns the down topic for a node.
func TopicForNode(nodeID string) string {
	return fmt.Sprintf("nodes/%s/down", nodeID)
}

// ControlTopicForNode is the reliable QoS-1 topic used for manifests and
// control commands.
func ControlTopicForNode(nodeID string) string {
	return fmt.Sprintf("nodes/%s/control", nodeID)
}
