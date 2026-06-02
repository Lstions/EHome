package homeassistant

import (
	"encoding/json"
	"fmt"
	"ehome/backend/pkg/logger"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/mqtt"
)

// Integration handles HomeAssistant MQTT Discovery
type Integration struct {
	mqttClient *mqtt.Client
}

// NewIntegration creates a new HA integration
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

		if err := h.mqttClient.Publish(configTopic, configJSON); err != nil {
			logger.Infof("HA Discovery: failed to publish config: %v", err)
		}
	}
	return nil
}

// PublishState publishes sensor state to HomeAssistant
func (h *Integration) PublishState(deviceID string, sensors []drivers.SensorData) error {
	state := make(map[string]interface{})
	for _, sensor := range sensors {
		state[sensor.Name] = sensor.Value
	}

	stateJSON, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	for _, sensor := range sensors {
		stateTopic := fmt.Sprintf("homeassistant/sensor/%s_%s/state", deviceID, sensor.Name)
		if err := h.mqttClient.Publish(stateTopic, stateJSON); err != nil {
			logger.Infof("HA State: failed to publish: %v", err)
		}
	}
	return nil
}
