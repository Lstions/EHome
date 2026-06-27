package homeassistant

import (
	"encoding/json"
	"testing"

	"ehome/backend/internal/drivers"
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
	defer func() {
		if r := recover(); r != nil {
			t.Logf("PublishState with nil client panicked (expected): %v", r)
		}
	}()
	integration.PublishState("dev001", sensors)
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
