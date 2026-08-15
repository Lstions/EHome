package homeassistant

import (
	"ehome/backend/pkg/logger"
	"encoding/json"
	"fmt"
	"sync"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/mqtt"
)

const statePublishQueueSize = 1024

// Integration handles HomeAssistant MQTT Discovery.
// Discovery/config traffic remains reliable on the primary MQTT client; high-rate
// Home Assistant state telemetry is explicitly best-effort and isolated in a
// bounded latest-value queue.
type Integration struct {
	mqttClient *mqtt.Client

	mu          sync.Mutex
	pending     map[string]publishStateRequest // newest state per node, keyed by deviceID
	signal      chan struct{}                  // coalesced wake-up; capacity is one
	stopCh      chan struct{}
	doneCh      chan struct{}
	started     bool
	publishQoS0 func(topic string, payload []byte) error // resolved from mqttClient in StartPublishWorker; overridable in-package tests
}

// publishStateRequest represents a HomeAssistant state publish request.
type publishStateRequest struct {
	deviceID string
	sensors  []drivers.SensorData
}

// NewIntegration creates a new HA integration.
func NewIntegration(client *mqtt.Client) *Integration {
	return &Integration{mqttClient: client}
}

// PublishDiscovery publishes MQTT Discovery config for a device
func (h *Integration) PublishDiscovery(deviceID, deviceName, model string, sensors []drivers.SensorData) error {
	for _, sensor := range sensors {
		configTopic := fmt.Sprintf("homeassistant/sensor/%s_%s/config", deviceID, sensor.Name)
		stateTopic := fmt.Sprintf("homeassistant/sensor/%s_%s/state", deviceID, sensor.Name)

		config := map[string]interface{}{
			"name":                sensor.Name,
			"state_topic":         stateTopic,
			"unit_of_measurement": sensor.Unit,
			"value_template":      fmt.Sprintf("{{ value_json.%s }}", sensor.Name),
			"device": map[string]interface{}{
				"identifiers":  []string{deviceID},
				"name":         deviceName,
				"model":        model,
				"manufacturer": "EHomeSystem",
			},
		}

		configJSON, err := json.Marshal(config)
		if err != nil {
			return fmt.Errorf("marshal discovery config: %w", err)
		}

		if err := h.mqttClient.PublishRetained(configTopic, configJSON); err != nil {
			logger.Infof("HA Discovery: failed to publish config: %v", err)
		}
	}
	return nil
}

// PublishState records the newest state for one node and returns without waiting
// for MQTT. State topics have latest-value semantics: replacing stale pending data
// is intentional and prevents a slow broker from consuming unbounded memory.
func (h *Integration) PublishState(deviceID string, sensors []drivers.SensorData) {
	if h == nil || len(sensors) == 0 {
		return
	}

	h.mu.Lock()
	if !h.started {
		h.mu.Unlock()
		return
	}
	if _, exists := h.pending[deviceID]; !exists && len(h.pending) >= statePublishQueueSize {
		h.mu.Unlock()
		logger.Warnf("HA state queue full, dropping latest state for %s", deviceID)
		return
	}
	// Copy because parser-owned slices must not be retained across goroutines;
	// done after the capacity check so the drop path allocates nothing.
	h.pending[deviceID] = publishStateRequest{deviceID: deviceID, sensors: append([]drivers.SensorData(nil), sensors...)}
	// signal is read outside the lock. This is race-free: reaching this line
	// requires observing started==true inside the same critical section, which
	// is ordered after StartPublishWorker's assignment of signal by h.mu.
	signal := h.signal
	h.mu.Unlock()

	select {
	case signal <- struct{}{}:
	default:
	}
}

// StartPublishWorker starts the asynchronous best-effort state publisher. It is
// idempotent and safe with a nil MQTT client (useful for unit-test composition).
func (h *Integration) StartPublishWorker() {
	if h == nil || h.mqttClient == nil {
		return
	}
	h.mu.Lock()
	if h.started {
		h.mu.Unlock()
		return
	}
	if _, ok := interface{}(h.mqttClient).(mqtt.TelemetryPublisher); !ok {
		h.mu.Unlock()
		logger.Warnf("HA state publisher does not support QoS 0; state telemetry disabled")
		return
	}
	h.publishQoS0 = interface{}(h.mqttClient).(mqtt.TelemetryPublisher).PublishQoS0
	h.pending = make(map[string]publishStateRequest)
	h.signal = make(chan struct{}, 1)
	h.stopCh = make(chan struct{})
	h.doneCh = make(chan struct{})
	h.started = true
	signal := h.signal
	stopCh := h.stopCh
	doneCh := h.doneCh
	h.mu.Unlock()

	go h.runStatePublisher(signal, stopCh, doneCh)
}

func (h *Integration) runStatePublisher(signal <-chan struct{}, stopCh <-chan struct{}, doneCh chan<- struct{}) {
	defer close(doneCh)
	for {
		select {
		case <-stopCh:
			return
		case <-signal:
			for {
				req, ok := h.takePendingState()
				if !ok {
					break
				}
				h.publishState(req)
			}
		}
	}
}

func (h *Integration) takePendingState() (publishStateRequest, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for deviceID, req := range h.pending {
		delete(h.pending, deviceID)
		return req, true
	}
	return publishStateRequest{}, false
}

func (h *Integration) publishState(req publishStateRequest) {
	state := make(map[string]interface{}, len(req.sensors))
	for _, sensor := range req.sensors {
		state[sensor.Name] = sensor.Value
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		logger.Warnf("HA: failed to marshal state for %s: %v", req.deviceID, err)
		return
	}
	telemetry := h.publishQoS0 // resolved once in StartPublishWorker (never nil there)
	for _, sensor := range req.sensors {
		stateTopic := fmt.Sprintf("homeassistant/sensor/%s_%s/state", req.deviceID, sensor.Name)
		if err := telemetry(stateTopic, stateJSON); err != nil {
			logger.Warnf("HA state publish failed for %s: %v", req.deviceID, err)
		}
	}
}

// StopPublishWorker stops the state publisher without closing a channel that
// concurrent parser workers could still send to. Pending telemetry is deliberately
// discarded at shutdown; it is non-durable latest-value data.
func (h *Integration) StopPublishWorker() {
	if h == nil {
		return
	}
	h.mu.Lock()
	if !h.started {
		h.mu.Unlock()
		return
	}
	stopCh := h.stopCh
	doneCh := h.doneCh
	h.started = false
	h.pending = nil
	h.mu.Unlock()
	close(stopCh)
	<-doneCh
}
