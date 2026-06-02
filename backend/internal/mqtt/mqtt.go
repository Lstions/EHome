package mqtt

import (
	"fmt"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// Client wraps the MQTT client
type Client struct {
	client  mqtt.Client
	handler func(topic string, payload []byte)
}

// Initialize connects to the MQTT broker
func Initialize(broker, user, password string) (*Client, error) {
	opts := mqtt.NewClientOptions().
		AddBroker(broker).
		SetClientID("ehome-server-v2").
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(5 * time.Second).
		SetKeepAlive(30 * time.Second).
		SetPingTimeout(10 * time.Second)

	if user != "" {
		opts = opts.SetUsername(user).SetPassword(password)
	}

	client := mqtt.NewClient(opts)
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		return nil, fmt.Errorf("connect to MQTT: %w", token.Error())
	}

	c := &Client{client: client}

	// Subscribe to all device topics
	token := client.Subscribe("devices/+/up", 1, c.onMessage)
	token.Wait()
	if token.Error() != nil {
		return nil, fmt.Errorf("subscribe: %w", token.Error())
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

// Close disconnects from the broker
func (c *Client) Close() {
	c.client.Disconnect(250)
}

// TopicForDevice returns the down topic for a device
func TopicForDevice(deviceID string) string {
	return fmt.Sprintf("devices/%s/down", deviceID)
}
