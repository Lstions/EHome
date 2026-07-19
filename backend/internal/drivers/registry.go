package drivers

import (
	"ehome/backend/pkg/logger"
	"fmt"
)

// SensorData represents parsed sensor data
type SensorData struct {
	Name        string  `json:"name"`
	Value       float64 `json:"value"`
	Unit        string  `json:"unit,omitempty"`
	StringValue string  `json:"string_value,omitempty"` // optional string data (hardware version, serial number, etc.)
}

// Driver is the interface for device drivers
type Driver interface {
	// ParseData parses raw bytes into sensor data
	ParseData(raw []byte) ([]SensorData, error)
	// DeviceType returns the device type identifier
	DeviceType() string
	// DeviceName returns human-readable name
	DeviceName() string
	// OEM returns the manufacturer name (e.g. "Bosch", "LK")
	OEM() string
	// Category returns the sensor category (e.g. "温度气压传感器")
	Category() string
	// HardwareTypes returns supported bus types
	HardwareTypes() []string
	// GetSensorDefinitions returns the sensor definitions for HA Discovery
	GetSensorDefinitions() []SensorData
}

type Registry struct {
	drivers map[string]Driver
}

// NewRegistry creates a new driver registry
func NewRegistry() *Registry {
	return &Registry{
		drivers: make(map[string]Driver),
	}
}

// Register registers a driver
func (r *Registry) Register(driver Driver) {
	typeID := driver.DeviceType()
	r.drivers[typeID] = driver
	logger.Infof("Driver registered: %s (%s)", typeID, driver.DeviceName())
}

// Get returns a driver by type
func (r *Registry) Get(deviceType string) (Driver, error) {
	driver, ok := r.drivers[deviceType]
	if !ok {
		return nil, fmt.Errorf("driver not found: %s", deviceType)
	}
	return driver, nil
}

// List returns all registered driver types
func (r *Registry) List() []string {
	var types []string
	for t := range r.drivers {
		types = append(types, t)
	}
	return types
}

// Global registry instance
var globalRegistry = NewRegistry()

// GlobalRegistry returns the process-wide driver registry.
func GlobalRegistry() *Registry {
	return globalRegistry
}

// Register registers a driver globally
func Register(driver Driver) {
	globalRegistry.Register(driver)
}

// Get gets a driver from global registry
func Get(deviceType string) (Driver, error) {
	return globalRegistry.Get(deviceType)
}

// List lists all drivers in global registry
func List() []string {
	return globalRegistry.List()
}

// CalibrationAwareDriver is implemented by drivers whose raw samples are
// uninterpretable without per-device calibration data.
type CalibrationAwareDriver interface {
	Driver
	ParseDataWithCalibration(raw, calibration []byte) ([]SensorData, error)
}

// CommandAwareDriver is an optional interface for drivers that need command
// context to disambiguate responses with identical formats (e.g. HPV vs HPVB
// on the Techfine GB3024, which both return "(AAA.A BB.B CCCCC").
//
// commandWriteData is the hex-encoded WriteData of the ConfigTemplate that
// was sent to the device (same format as CommandTemplate.WriteData /
// ConfigTemplate.WriteData — hex-encoded ASCII).  Drivers that implement this
// interface should decode it the same way the sender does. Callers must not
// fall back to Driver.ParseData when command context is missing or this method
// returns an error; doing so can silently select the wrong response branch.
type CommandAwareDriver interface {
	Driver
	// ParseDataWithCommand parses raw data with command context.
	// commandWriteData is the hex-encoded write data of the template that
	// was sent (e.g. "4850560d" for "HPV\r").
	ParseDataWithCommand(raw []byte, commandWriteData string) ([]SensorData, error)
}
