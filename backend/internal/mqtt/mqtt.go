package mqtt

import (
	"fmt"
	"sync"
	"time"

	"ehome/backend/pkg/logger"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

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

// Publish sends a message to a topic
func (c *Client) Publish(topic string, payload []byte) error {
	token := c.client.Publish(topic, 1, false, payload)
	token.Wait()
	return token.Error()
}

// PublishRetained sends a retained message to a topic
func (c *Client) PublishRetained(topic string, payload []byte) error {
	token := c.client.Publish(topic, 1, true, payload)
	token.Wait()
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
