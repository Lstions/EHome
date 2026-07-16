package mqtt

import (
	"fmt"
	"sync"
	"time"

	"ehome/backend/pkg/logger"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// Publisher is the interface for MQTT publishing operations.
// *Client implements this interface. Tests can provide mock implementations.
type Publisher interface {
	Publish(topic string, payload []byte) error
	PublishQoS2(topic string, payload []byte) error
	PublishRetained(topic string, payload []byte) error
}

// Client wraps the MQTT client
type Client struct {
	client  mqtt.Client
	handler func(topic string, payload []byte)
	mu      sync.RWMutex
	topics  map[string]byte // subscribed topics with QoS for resubscription
}

// Initialize connects to the MQTT broker
func Initialize(broker, user, password string) (*Client, error) {
	c := &Client{
		topics: make(map[string]byte),
	}

	opts := mqtt.NewClientOptions().
		AddBroker(broker).
		SetClientID("ehome-server-v2").
		SetCleanSession(false). // persistent session: broker keeps subscriptions & queued messages
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(5 * time.Second).
		SetKeepAlive(30 * time.Second).
		SetPingTimeout(10 * time.Second).
		SetOnConnectHandler(func(_ mqtt.Client) {
			// On (re)connect: resubscribe all topics so we never miss messages
			// after a reconnect. Even with persistent session, the broker
			// only restores subscriptions that were made with clean=false;
			// resubscribing is idempotent and guarantees coverage.
			c.mu.RLock()
			for topic, qos := range c.topics {
				if token := c.client.Subscribe(topic, qos, c.onMessage); token.Wait() && token.Error() != nil {
					logger.Warnf("[MQTT] resubscribe %s failed: %v", topic, token.Error())
				} else {
					logger.Infof("[MQTT] resubscribed %s (QoS %d)", topic, qos)
				}
			}
			c.mu.RUnlock()
		})

	if user != "" {
		opts = opts.SetUsername(user).SetPassword(password)
	}

	client := mqtt.NewClient(opts)
	c.client = client

	if token := client.Connect(); token.Wait() && token.Error() != nil {
		return nil, fmt.Errorf("connect to MQTT: %w", token.Error())
	}

	// Subscribe to all node upstream topics
	if err := c.Subscribe("nodes/+/up", 1); err != nil {
		return nil, fmt.Errorf("subscribe: %w", err)
	}

	return c, nil
}

// SetHandler sets the message handler callback
func (c *Client) SetHandler(handler func(topic string, payload []byte)) {
	c.handler = handler
}

func (c *Client) onMessage(client mqtt.Client, msg mqtt.Message) {
	if c.handler != nil {
		c.handler(msg.Topic(), msg.Payload())
	}
}

// Publish sends a message to a topic with QoS 1 and a 5-second timeout.
// Returns error if the broker does not acknowledge within the deadline.
func (c *Client) Publish(topic string, payload []byte) error {
	if c == nil || c.client == nil {
		return fmt.Errorf("MQTT client not connected")
	}
	token := c.client.Publish(topic, 1, false, payload)
	if !token.WaitTimeout(5 * time.Second) {
		return fmt.Errorf("mqtt publish timeout")
	}
	return token.Error()
}

// P3-5: PublishQoS2 sends a message with QoS 2 (exactly-once delivery) for critical operations.
// QoS 2 uses a 4-step handshake which adds latency — only use for write commands
// where delivery guarantee matters more than speed.
func (c *Client) PublishQoS2(topic string, payload []byte) error {
	if c.client == nil {
		return fmt.Errorf("MQTT client not connected")
	}
	token := c.client.Publish(topic, 2, false, payload)
	if !token.WaitTimeout(10 * time.Second) {
		return fmt.Errorf("QoS 2 publish timeout")
	}
	return token.Error()
}

// PublishRetained sends a retained message to a topic with a 5-second timeout.
func (c *Client) PublishRetained(topic string, payload []byte) error {
	token := c.client.Publish(topic, 1, true, payload)
	if !token.WaitTimeout(5 * time.Second) {
		return fmt.Errorf("mqtt publish timeout")
	}
	return token.Error()
}

// Subscribe adds a topic subscription (also tracked for auto-resubscribe on reconnect)
func (c *Client) Subscribe(topic string, qos byte) error {
	c.mu.Lock()
	c.topics[topic] = qos
	c.mu.Unlock()
	token := c.client.Subscribe(topic, qos, c.onMessage)
	token.Wait()
	return token.Error()
}

// Close disconnects from the broker
func (c *Client) Close() {
	c.client.Disconnect(250)
}

// TopicForNode returns the down topic for a node
func TopicForNode(nodeID string) string {
	return fmt.Sprintf("nodes/%s/down", nodeID)
}

// ControlTopicForNode is the reliable QoS-1 subscription used for manifests
// and control commands. Telemetry-compatible legacy down traffic remains on /down.
func ControlTopicForNode(nodeID string) string {
	return fmt.Sprintf("nodes/%s/control", nodeID)
}
