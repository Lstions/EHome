package deviceinit

import (
	"ehome/backend/pkg/logger"
	"fmt"
	"sync"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/mqtt"
	"ehome/backend/pkg/frame"

	"gorm.io/gorm"
)

// Step represents a single initialization step
type Step struct {
	Name     string
	Data     []byte
	ReadSize uint32
	Timeout  time.Duration
}

// Orchestrator manages device initialization sequences
type Orchestrator struct {
	db    *gorm.DB
	mqtt  *mqtt.Client
	mu    sync.RWMutex
	cache map[string]*InitState // device_type -> init state

	// v2.1: response tracking for sendAndWait
	pendingMu   sync.Mutex
	pendingResp map[uint32]chan []byte // request_id -> response channel
}

// InitState tracks initialization progress for a device
type InitState struct {
	DeviceType string
	Steps      []Step
	CurrentIdx int
	Completed  bool
	CalibData  []byte
}

// NewOrchestrator creates a new device init orchestrator
func NewOrchestrator(db *gorm.DB, mqttClient *mqtt.Client) *Orchestrator {
	return &Orchestrator{
		db:          db,
		mqtt:        mqttClient,
		cache:       make(map[string]*InitState),
		pendingResp: make(map[uint32]chan []byte),
	}
}

// GetInitSequence returns the initialization sequence for a device type
func (o *Orchestrator) GetInitSequence(deviceType string) []Step {
	switch deviceType {
	case "bmp280":
		return []Step{
			{Name: "reset", Data: []byte{0xE0, 0xB6}, ReadSize: 0, Timeout: 100 * time.Millisecond},
			{Name: "read_chip_id", Data: []byte{0xD0}, ReadSize: 1, Timeout: 100 * time.Millisecond},
			{Name: "read_calib", Data: []byte{0x88}, ReadSize: 24, Timeout: 100 * time.Millisecond},
			{Name: "set_ctrl", Data: []byte{0xF4, 0x27}, ReadSize: 0, Timeout: 100 * time.Millisecond},
			{Name: "set_config", Data: []byte{0xF5, 0xA0}, ReadSize: 0, Timeout: 100 * time.Millisecond},
		}
	case "lk_th01":
		return []Step{
			{Name: "reset", Data: []byte{0xFE}, ReadSize: 0, Timeout: 20 * time.Millisecond},
			{Name: "read_temp", Data: []byte{0xF3}, ReadSize: 3, Timeout: 100 * time.Millisecond},
		}
	default:
		return nil
	}
}

// sendAndWait sends a WriteCommand and waits for the response with timeout.
// v2.1: replaces time.Sleep with proper async wait (fixes G6).
func (o *Orchestrator) sendAndWait(deviceID string, channelID uint32, requestID uint32, data []byte, readSize uint32, timeout time.Duration) ([]byte, error) {
	// Register pending response channel
	respCh := make(chan []byte, 1)
	o.pendingMu.Lock()
	o.pendingResp[requestID] = respCh
	o.pendingMu.Unlock()

	defer func() {
		o.pendingMu.Lock()
		delete(o.pendingResp, requestID)
		o.pendingMu.Unlock()
	}()

	// Send WriteCommand
	enc := frame.NewEncoder(frame.MsgWriteCmd)
	enc.EncodeVarint(1, uint64(requestID))
	enc.EncodeVarint(2, uint64(channelID))
	enc.EncodeBytes(3, data)
	if readSize > 0 {
		enc.EncodeVarint(4, uint64(readSize))
	}

	topic := mqtt.TopicForDevice(deviceID)
	if err := o.mqtt.Publish(topic, enc.Bytes()); err != nil {
		return nil, fmt.Errorf("send failed: %w", err)
	}

	// Wait for response or timeout
	select {
	case resp := <-respCh:
		return resp, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("timeout after %v", timeout)
	}
}

// HandleWriteResponse routes a WriteResponse to the waiting sender.
// Called by collector manager when a WriteResponse (0x07) is received.
func (o *Orchestrator) HandleWriteResponse(requestID uint32, rawData []byte) {
	o.pendingMu.Lock()
	ch, ok := o.pendingResp[requestID]
	o.pendingMu.Unlock()

	if ok {
		select {
		case ch <- rawData:
		default: // channel full, response already received or timed out
		}
	}
}

// InitDevice performs initialization for a device
func (o *Orchestrator) InitDevice(deviceID string, channelID uint32, deviceType string) error {
	steps := o.GetInitSequence(deviceType)
	if steps == nil {
		return fmt.Errorf("no init sequence for device type: %s", deviceType)
	}

	state := &InitState{
		DeviceType: deviceType,
		Steps:      steps,
		CurrentIdx: 0,
	}

	o.mu.Lock()
	o.cache[deviceType] = state
	o.mu.Unlock()

	// Execute each step using sendAndWait (v2.1: no more time.Sleep)
	for i, step := range steps {
		state.CurrentIdx = i
		logger.Infof("[Init] %s: executing step %d/%d: %s", deviceID, i+1, len(steps), step.Name)

		requestID := uint32(i + 1)
		rawData, err := o.sendAndWait(deviceID, channelID, requestID, step.Data, step.ReadSize, step.Timeout)
		if err != nil {
			logger.Warnf("[Init] %s: step %s failed: %v", deviceID, step.Name, err)
			// Continue with next step on failure (best-effort init)
			continue
		}

		// Store calibration data if applicable
		if step.Name == "read_calib" && len(rawData) > 0 {
			state.CalibData = rawData
			o.saveCalibData(deviceID, deviceType, rawData)
		}

		logger.Infof("[Init] %s: step %s completed, data=%x", deviceID, step.Name, rawData)
	}

	state.Completed = true
	logger.Infof("[Init] %s: initialization complete", deviceID)
	return nil
}

// HandleDataReportAck processes DataReport that may be an init response
func (o *Orchestrator) HandleDataReportAck(deviceID string, requestID uint32, rawData []byte) {
	o.mu.RLock()
	defer o.mu.RUnlock()

	for deviceType, state := range o.cache {
		if state.Completed {
			continue
		}
		// Check if this response matches current step
		if int(requestID) == state.CurrentIdx+1 {
			logger.Infof("[Init] %s: received response for step %d (%s), data=%x",
				deviceID, state.CurrentIdx, state.Steps[state.CurrentIdx].Name, rawData)
			// Store calibration data if applicable
			if state.Steps[state.CurrentIdx].Name == "read_calib" {
				state.CalibData = rawData
				// Save to DB
				o.saveCalibData(deviceID, deviceType, rawData)
			}
		}
	}
}

// saveCalibData saves calibration data to database
func (o *Orchestrator) saveCalibData(deviceID, deviceType string, data []byte) {
	var node models.Node
	if err := o.db.Where("node_id = ?", deviceID).First(&node).Error; err != nil {
		logger.Infof("[Init] Node not found: %s", deviceID)
		return
	}

	// Save to calibration cache
	o.db.Create(&models.CalibrationCache{
		CollectorID: node.ID,
		DeviceType:  deviceType,
		Data:        fmt.Sprintf("%x", data),
	})
	logger.Infof("[Init] Calibration data saved for %s/%s", deviceID, deviceType)
}

// IsInitialized checks if a device type has been initialized
func (o *Orchestrator) IsInitialized(deviceType string) bool {
	o.mu.RLock()
	defer o.mu.RUnlock()
	state, ok := o.cache[deviceType]
	return ok && state.Completed
}

// ClearCache removes init state for a device type
func (o *Orchestrator) ClearCache(deviceType string) {
	o.mu.Lock()
	delete(o.cache, deviceType)
	o.mu.Unlock()
}

// InitIfNeeded checks if a device needs initialization and triggers it.
// This is called when a collector transitions from offline to online.
// deviceID: collector device_id
// channelID: the channel where the device is connected
// deviceType: the type of device (e.g., "bmp280")
// Returns true if init was triggered, false if already initialized or no init sequence.
func (o *Orchestrator) InitIfNeeded(deviceID string, channelID uint32, deviceType string) bool {
	// Check if already initialized
	o.mu.RLock()
	state, ok := o.cache[deviceType]
	if ok && state.Completed {
		o.mu.RUnlock()
		return false
	}
	o.mu.RUnlock()

	// Get init sequence
	steps := o.GetInitSequence(deviceType)
	if steps == nil {
		return false // No init sequence for this device type
	}

	// Trigger async initialization
	go o.InitDevice(deviceID, channelID, deviceType)
	return true
}

// HasActiveInit checks if there's an active init flow for a device type
func (o *Orchestrator) HasActiveInit(deviceType string) bool {
	o.mu.RLock()
	defer o.mu.RUnlock()
	state, ok := o.cache[deviceType]
	return ok && !state.Completed
}
