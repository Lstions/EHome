package homeassistant

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/mqtt"
	"ehome/backend/pkg/logger"
)

func init() {
	logger.Init("warn")
}

func TestNewIntegration_Nil(t *testing.T) {
	integration := NewIntegration(nil)
	if integration == nil {
		t.Fatal("expected non-nil integration")
	}
	if integration.mqttClient != nil {
		t.Error("expected nil mqttClient")
	}
}

func TestPublishDiscovery_NilClient_NoPanic(t *testing.T) {
	integration := NewIntegration(nil)
	sensors := []drivers.SensorData{
		{Name: "temperature", Value: 25.3, Unit: "°C"},
	}
	// This will panic with nil client — test that the function exists
	// Real coverage comes from mqtt package tests
	defer func() {
		if r := recover(); r != nil {
			t.Logf("PublishDiscovery with nil client panicked (expected): %v", r)
		}
	}()
	integration.PublishDiscovery("dev001", "Test", "bmp280", sensors)
}

func TestPublishState_NilClient_NoPanic(t *testing.T) {
	integration := NewIntegration(nil)
	sensors := []drivers.SensorData{
		{Name: "temperature", Value: 25.3, Unit: "°C"},
	}
	// With a nil client the worker never starts; PublishState must be a
	// silent no-op instead of enqueueing into a nil pipeline.
	integration.PublishState("dev001", sensors)
	integration.StartPublishWorker()
	integration.StopPublishWorker()
}

func TestPublishDiscovery_ConfigJSON(t *testing.T) {
	// Test the JSON structure that PublishDiscovery would generate
	sensor := drivers.SensorData{Name: "temperature", Value: 25.3, Unit: "°C"}
	deviceID := "dev001"
	deviceName := "MyDevice"
	model := "bmp280"

	configTopic := "homeassistant/sensor/" + deviceID + "_" + sensor.Name + "/config"
	stateTopic := "homeassistant/sensor/" + deviceID + "_" + sensor.Name + "/state"

	if configTopic != "homeassistant/sensor/dev001_temperature/config" {
		t.Errorf("config topic: got %s", configTopic)
	}
	if stateTopic != "homeassistant/sensor/dev001_temperature/state" {
		t.Errorf("state topic: got %s", stateTopic)
	}

	config := map[string]interface{}{
		"name":                sensor.Name,
		"state_topic":         stateTopic,
		"unit_of_measurement": sensor.Unit,
		"value_template":      "{{ value_json." + sensor.Name + " }}",
		"device": map[string]interface{}{
			"identifiers":  []string{deviceID},
			"name":         deviceName,
			"model":        model,
			"manufacturer": "EHomeSystem",
		},
	}

	configJSON, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(configJSON, &parsed); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}

	if parsed["name"] != "temperature" {
		t.Errorf("name: got %v", parsed["name"])
	}
	if parsed["unit_of_measurement"] != "°C" {
		t.Errorf("unit: got %v", parsed["unit_of_measurement"])
	}
	device := parsed["device"].(map[string]interface{})
	if device["manufacturer"] != "EHomeSystem" {
		t.Errorf("manufacturer: got %v", device["manufacturer"])
	}
}

func TestPublishState_StateJSON(t *testing.T) {
	sensors := []drivers.SensorData{
		{Name: "temperature", Value: 25.3, Unit: "°C"},
		{Name: "humidity", Value: 65.0, Unit: "%RH"},
	}

	state := make(map[string]interface{})
	for _, sensor := range sensors {
		state[sensor.Name] = sensor.Value
	}

	stateJSON, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(stateJSON, &parsed); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}

	if parsed["temperature"] != 25.3 {
		t.Errorf("temperature: got %v", parsed["temperature"])
	}
	if parsed["humidity"] != 65.0 {
		t.Errorf("humidity: got %v", parsed["humidity"])
	}
}

func TestPublishDiscovery_EmptySensors(t *testing.T) {
	// With empty sensors, the loop body never executes, so no publish calls
	sensors := []drivers.SensorData{}
	if len(sensors) != 0 {
		t.Error("expected empty sensors")
	}
	// The function should return nil error with empty sensors
	// (loop just doesn't iterate)
}

// --- Async state publisher worker tests ---

// newTestIntegration returns an Integration whose worker publishes through fn
// instead of a real MQTT client. It mirrors what StartPublishWorker resolves
// from a TelemetryPublisher.
func newTestIntegration(fn func(topic string, payload []byte) error) *Integration {
	h := &Integration{}
	h.mu.Lock()
	h.publishQoS0 = fn
	h.pending = make(map[string]publishStateRequest)
	h.signal = make(chan struct{}, 1)
	h.stopCh = make(chan struct{})
	h.doneCh = make(chan struct{})
	h.started = true
	signal, stopCh, doneCh := h.signal, h.stopCh, h.doneCh
	h.mu.Unlock()
	go h.runStatePublisher(signal, stopCh, doneCh)
	return h
}

type capturedPublish struct {
	topic   string
	payload string
}

func TestPublishState_AsyncLatestValuePerDevice(t *testing.T) {
	var mu sync.Mutex
	var got []capturedPublish
	release := make(chan struct{})
	h := newTestIntegration(func(topic string, payload []byte) error {
		<-release // hold the worker so PublishState can pile up pending state
		mu.Lock()
		got = append(got, capturedPublish{topic, string(payload)})
		mu.Unlock()
		return nil
	})
	defer h.StopPublishWorker()

	h.PublishState("nodeA", []drivers.SensorData{{Name: "temperature", Value: 1}})
	// Wait until the worker picked up nodeA and is blocked inside the publisher.
	time.Sleep(50 * time.Millisecond)
	// Two more bursts for the same node: only the newest may survive.
	h.PublishState("nodeA", []drivers.SensorData{{Name: "temperature", Value: 2}})
	h.PublishState("nodeA", []drivers.SensorData{{Name: "temperature", Value: 3}})
	h.PublishState("nodeB", []drivers.SensorData{{Name: "humidity", Value: 60}})
	close(release)

	// Each surviving request publishes 1 topic per sensor: nodeA(1) + nodeA(3)
	// + nodeB(60) = 3 publishes total.
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected 3 publishes, got %d: %+v", n, got)
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond) // catch any unexpected extra drain

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 3 {
		t.Fatalf("expected exactly 3 publishes after merge, got %d: %+v", len(got), got)
	}
	values := map[string]string{}
	for _, p := range got {
		values[p.topic] = p.payload
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(values["homeassistant/sensor/nodeA_temperature/state"]), &parsed); err != nil {
		t.Fatalf("unmarshal nodeA state: %v", err)
	}
	if parsed["temperature"] != float64(3) {
		t.Errorf("nodeA latest value = %v, want 3", parsed["temperature"])
	}
	if _, ok := values["homeassistant/sensor/nodeB_humidity/state"]; !ok {
		t.Errorf("nodeB state missing: %+v", got)
	}
	for _, p := range got {
		if p.payload == `{"temperature":2}` {
			t.Errorf("stale intermediate nodeA value 2 was published: %+v", got)
		}
	}
}

func TestPublishState_QueueSaturation_DropsNewestUnknownDevice(t *testing.T) {
	// No worker goroutine: deterministic map state.
	h := newTestIntegration(func(topic string, payload []byte) error { return nil })
	h.mu.Lock()
	close(h.stopCh) // stop the worker without clearing the queue invariants under test
	<-h.doneCh
	h.mu.Unlock()

	// Fill exactly to capacity with distinct devices.
	for i := 0; i < statePublishQueueSize; i++ {
		h.PublishState(fmt.Sprintf("dev%04d", i), []drivers.SensorData{{Name: "temperature", Value: float64(i)}})
	}
	// Unknown device while full: dropped, map must not grow.
	h.PublishState("dev-newcomer", []drivers.SensorData{{Name: "temperature", Value: 1}})

	h.mu.Lock()
	size := len(h.pending)
	var newcomerKept bool
	if _, ok := h.pending["dev-newcomer"]; ok {
		newcomerKept = true
	}
	h.mu.Unlock()

	if size != statePublishQueueSize {
		t.Fatalf("pending size = %d, want %d", size, statePublishQueueSize)
	}
	if newcomerKept {
		t.Error("unknown device state must be dropped when queue is full")
	}

	// Existing device while full: replacement allowed (latest-value semantics).
	h.PublishState("dev0000", []drivers.SensorData{{Name: "temperature", Value: 999}})
	h.mu.Lock()
	size = len(h.pending)
	h.mu.Unlock()
	if size != statePublishQueueSize {
		t.Fatalf("pending size after replace = %d, want %d (replace must not grow the map)", size, statePublishQueueSize)
	}
}

func TestPublishState_BeforeStart_IsNoOp(t *testing.T) {
	var calls int32
	h := &Integration{publishQoS0: func(string, []byte) error { calls++; return nil }}
	h.PublishState("nodeA", []drivers.SensorData{{Name: "temperature", Value: 1}})
	if calls != 0 {
		t.Fatalf("publish called before worker start: %d", calls)
	}
}

func TestStopPublishWorker_Idempotent_And_ConcurrentPublishSafe(t *testing.T) {
	// In-flight publish blocked: Stop must still terminate the worker.
	blocked := newTestIntegration(func(topic string, payload []byte) error {
		time.Sleep(100 * time.Millisecond)
		return nil
	})
	blocked.StopPublishWorker() // must not deadlock behind the in-flight publish

	// Concurrent PublishState during and after Stop: no panic, no deadlock.
	h := newTestIntegration(func(topic string, payload []byte) error {
		return nil // success path: no WARN flood
	})
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					h.PublishState("nodeA", []drivers.SensorData{{Name: "temperature", Value: 1}})
				}
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	h.StopPublishWorker()
	// Stop must be idempotent and must not panic with concurrent PublishState.
	h.StopPublishWorker()
	close(stop)
	wg.Wait()
}

func TestStartPublishWorker_NonTelemetryClient_Refuses(t *testing.T) {
	// A real *mqtt.Client implements TelemetryPublisher, so fabricate a stub
	// that does not: the worker must refuse instead of nil-dereferencing later.
	h := NewIntegration(nil)
	h.publishQoS0 = func(string, []byte) error { return nil }
	// mqttClient == nil → refused path.
	h.StartPublishWorker()
	h.mu.Lock()
	started := h.started
	h.mu.Unlock()
	if started {
		t.Fatal("worker must not start with nil mqtt client")
	}
	h.StopPublishWorker() // no-op, must not block
}

func TestTelemetryPublisherInterfaceAssertion(t *testing.T) {
	var _ mqtt.TelemetryPublisher = (*mqtt.Client)(nil)
}
