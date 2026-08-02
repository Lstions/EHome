package deviceinit

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
	"ehome/backend/internal/mqtt"
	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/logger"
	"gorm.io/gorm"
)

type Step struct {
	Name     string
	Data     []byte
	ReadSize uint32
	Timeout  time.Duration
	// Role is a stable semantic tag (e.g. "calib") used to dispatch step
	// side-effects without matching on the driver-specific step.Name.
	Role string
}

type publisher interface {
	Publish(topic string, payload []byte) error
}

type Orchestrator struct {
	db   *gorm.DB
	mqtt publisher
	mu   sync.RWMutex
	// cache is keyed by the concrete EdgeDevice, never by device type.
	cache          map[uint]*InitState
	pendingMu      sync.Mutex
	pendingResp    map[uint32]pendingResponse
	driverRegistry *drivers.Registry
}

type responseKind uint8

const (
	responseWrite responseKind = iota + 1
	responseData
)

type pendingResult struct {
	raw []byte
	err error
}

// pendingResponse is the complete correlation key for an init command.
// Retaining source, edge, step, response kind, and expected length prevents a
// malformed report from completing another device's wait.
type pendingResponse struct {
	nodeID           string
	edgeDeviceID     uint
	stepName         string
	responseKind     responseKind
	expectedReadSize uint32
	response         chan pendingResult
}

type InitState struct {
	EdgeDeviceID uint
	NodeID       string
	DeviceType   string
	Steps        []Step
	CurrentIdx   int
	// CurrentRequestID is the only DataReport request allowed to mutate this
	// active init step. EdgeDeviceID scopes the flow; request ID scopes the step.
	CurrentRequestID uint32
	Completed        bool
	InProgress       bool
	CalibData        []byte
}

var nextInitRequestID uint32

func NewOrchestrator(db *gorm.DB, mqttClient publisher, driverRegistry *drivers.Registry) *Orchestrator {
	return &Orchestrator{db: db, mqtt: mqttClient, cache: make(map[uint]*InitState), pendingResp: make(map[uint32]pendingResponse), driverRegistry: driverRegistry}
}

// GetInitSequence resolves the init sequence for a device type with a three-tier
// priority chain:
//  1. Driver implementing drivers.InitSequenceProvider (looked up via the
//     driver registry, when set).
//  2. DeviceConfig.InitFlow JSONB for the default config of this device type
//     (looked up via the DB, when set).
//  3. Hardcoded switch (legacy, backward-compat fallback).
//
// Returns nil when no sequence is defined at any tier.
func (o *Orchestrator) GetInitSequence(deviceType string) []Step {
	// Tier 1: driver-provided init sequence.
	if o.driverRegistry != nil {
		if drv, err := o.driverRegistry.Get(deviceType); err == nil {
			if provider, ok := drv.(drivers.InitSequenceProvider); ok {
				if steps := provider.GetInitSequence(); len(steps) > 0 {
					return fromDriverInitSteps(steps)
				}
			}
		}
	}
	// Tier 2: DeviceConfig.InitFlow JSONB.
	if o.db != nil {
		if steps := o.loadInitFlowFromDB(deviceType); len(steps) > 0 {
			return steps
		}
	}
	// Tier 3: hardcoded fallback.
	return hardcodedInitSequence(deviceType)
}

// fromDriverInitSteps converts drivers.InitStep slice to deviceinit.Step.
func fromDriverInitSteps(in []drivers.InitStep) []Step {
	out := make([]Step, len(in))
	for i, s := range in {
		out[i] = Step{Name: s.Name, Data: s.Data, ReadSize: s.ReadSize, Timeout: s.Timeout, Role: s.Role}
	}
	return out
}

// initFlowStep is the JSON shape of a single entry in DeviceConfig.InitFlow.
type initFlowStep struct {
	Name      string `json:"name"`
	Data      string `json:"data"`       // hex-encoded command bytes
	ReadSize  uint32 `json:"read_size"`  // expected response length (0 = write-only)
	TimeoutMs int    `json:"timeout_ms"` // per-step timeout in milliseconds
	Role      string `json:"role"`       // semantic tag (e.g. "calib")
}

// loadInitFlowFromDB resolves the default DeviceConfig for deviceType and
// parses its InitFlow JSONB into Steps. Returns nil on any error or empty
// flow so callers fall through to the next tier.
func (o *Orchestrator) loadInitFlowFromDB(deviceType string) []Step {
	var cfg models.DeviceConfig
	if err := o.db.Where("device_type = ? AND is_default = ?", deviceType, true).First(&cfg).Error; err != nil {
		return nil
	}
	if len(cfg.InitFlow) == 0 || string(cfg.InitFlow) == "[]" {
		return nil
	}
	var raw []initFlowStep
	if err := json.Unmarshal(cfg.InitFlow, &raw); err != nil {
		logger.Warnf("[Init] device_type=%s init_flow parse failed: %v", deviceType, err)
		return nil
	}
	if len(raw) == 0 {
		return nil
	}
	steps := make([]Step, 0, len(raw))
	for _, r := range raw {
		data, err := hex.DecodeString(r.Data)
		if err != nil {
			logger.Warnf("[Init] device_type=%s step %s hex decode failed: %v", deviceType, r.Name, err)
			return nil
		}
		timeout := time.Duration(r.TimeoutMs) * time.Millisecond
		if r.TimeoutMs <= 0 {
			timeout = 100 * time.Millisecond
		}
		steps = append(steps, Step{Name: r.Name, Data: data, ReadSize: r.ReadSize, Timeout: timeout, Role: r.Role})
	}
	return steps
}

// hardcodedInitSequence is the legacy switch retained for backward
// compatibility when neither a driver nor a DeviceConfig.InitFlow defines a
// sequence for the device type.
func hardcodedInitSequence(deviceType string) []Step {
	switch deviceType {
	case "bmp280":
		return []Step{
			{Name: "reset", Data: []byte{0xE0, 0xB6}, Timeout: 100 * time.Millisecond},
			{Name: "read_chip_id", Data: []byte{0xD0}, ReadSize: 1, Timeout: 100 * time.Millisecond},
			{Name: "read_calib", Data: []byte{0x88}, ReadSize: 24, Timeout: 100 * time.Millisecond, Role: "calib"},
			{Name: "set_ctrl", Data: []byte{0xF4, 0x27}, Timeout: 100 * time.Millisecond},
			{Name: "set_config", Data: []byte{0xF5, 0xA0}, Timeout: 100 * time.Millisecond},
		}
	case "lk_th01":
		return []Step{
			{Name: "reset", Data: []byte{0xFE}, Timeout: 20 * time.Millisecond},
			{Name: "read_temp", Data: []byte{0xF3}, ReadSize: 3, Timeout: 100 * time.Millisecond},
		}
	default:
		return nil
	}
}

func (o *Orchestrator) sendAndWait(nodeID string, edgeDeviceID uint, stepName string, channelID, requestID uint32, data []byte, readSize uint32, timeout time.Duration) ([]byte, error) {
	if o.mqtt == nil {
		return nil, fmt.Errorf("mqtt required")
	}
	respCh := make(chan pendingResult, 1)
	o.pendingMu.Lock()
	kind := responseWrite
	if readSize > 0 {
		kind = responseData
	}
	o.pendingResp[requestID] = pendingResponse{nodeID: nodeID, edgeDeviceID: edgeDeviceID, stepName: stepName, responseKind: kind, expectedReadSize: readSize, response: respCh}
	o.pendingMu.Unlock()
	defer func() { o.pendingMu.Lock(); delete(o.pendingResp, requestID); o.pendingMu.Unlock() }()
	enc := frame.NewEncoder(frame.MsgWriteCmd)
	enc.EncodeVarint(1, uint64(requestID))
	enc.EncodeVarint(2, uint64(channelID))
	enc.EncodeBytes(3, data)
	if readSize > 0 {
		enc.EncodeVarint(4, uint64(readSize))
	}
	// Field 5 is echoed in DataReport to correlate initialization reads to the
	// concrete edge device.
	enc.EncodeVarint(5, uint64(edgeDeviceID))
	if readSize > 0 {
		rxTimeoutMs := timeout.Milliseconds()
		if rxTimeoutMs < 1 {
			rxTimeoutMs = 1
		}
		if rxTimeoutMs > 30000 {
			rxTimeoutMs = 30000
		}
		enc.EncodeVarint(6, uint64(rxTimeoutMs))
	}
	if err := o.mqtt.Publish(mqtt.TopicForNode(nodeID), enc.Bytes()); err != nil {
		return nil, fmt.Errorf("send failed: %w", err)
	}
	select {
	case result := <-respCh:
		return result.raw, result.err
	case <-time.After(timeout):
		return nil, fmt.Errorf("timeout after %v", timeout)
	}
}

// HandleWriteResponse completes only write-only init steps. Read commands are
// completed by HandleDataReportAck, which carries EdgeDeviceID.
func (o *Orchestrator) HandleWriteResponse(nodeID string, requestID uint32, success bool, errorCode uint32, errorMsg string) {
	o.pendingMu.Lock()
	pending, ok := o.pendingResp[requestID]
	o.pendingMu.Unlock()
	if !ok || pending.nodeID != nodeID {
		return
	}
	if !success || errorCode != 0 {
		o.deliver(pending, pendingResult{err: fmt.Errorf("write response error: code=%d msg=%s", errorCode, errorMsg)})
		return
	}
	if pending.responseKind == responseWrite {
		select {
		case pending.response <- pendingResult{}:
		default:
		}
	}
}

func (o *Orchestrator) deliver(pending pendingResponse, result pendingResult) {
	select {
	case pending.response <- result:
	default:
	}
}

func allocateInitRequestID() uint32 {
	id := atomic.AddUint32(&nextInitRequestID, 1)
	if id == 0 {
		id = atomic.AddUint32(&nextInitRequestID, 1)
	}
	return id
}

func (o *Orchestrator) reserve(device models.EdgeDevice, nodeID string) (*InitState, error) {
	if device.ID == 0 {
		return nil, fmt.Errorf("edge device id is required")
	}
	steps := o.GetInitSequence(device.Type)
	if steps == nil {
		return nil, fmt.Errorf("no init sequence for device type: %s", device.Type)
	}
	o.mu.Lock()
	if existing, ok := o.cache[device.ID]; ok && (existing.Completed || existing.InProgress) {
		o.mu.Unlock()
		return nil, fmt.Errorf("edge device %d initialization already active or completed", device.ID)
	}
	state := &InitState{EdgeDeviceID: device.ID, NodeID: nodeID, DeviceType: device.Type, Steps: steps, InProgress: true}
	o.cache[device.ID] = state
	o.mu.Unlock()
	return state, nil
}

func (o *Orchestrator) InitEdgeDevice(device models.EdgeDevice, nodeID string) error {
	state, err := o.reserve(device, nodeID)
	if err != nil {
		return err
	}
	return o.runReserved(device, nodeID, state)
}

func (o *Orchestrator) runReserved(device models.EdgeDevice, nodeID string, state *InitState) (firstErr error) {
	defer func() {
		o.mu.Lock()
		state.InProgress = false
		state.Completed = firstErr == nil
		o.mu.Unlock()
	}()
	for _, step := range state.Steps {
		requestID := allocateInitRequestID()
		raw, err := o.sendAndWait(nodeID, device.ID, step.Name, uint32(device.ChannelID), requestID, step.Data, step.ReadSize, step.Timeout)
		if err != nil {
			logger.Warnf("[Init] %s step %s failed: %v", nodeID, step.Name, err)
			return fmt.Errorf("%s: %w", step.Name, err)
		}
		if step.Role == "calib" || (step.Role == "" && step.Name == "read_calib") {
			if err := o.saveCalibData(device, raw); err != nil {
				return fmt.Errorf("%s calibration: %w", step.Name, err)
			}
			o.mu.Lock()
			state.CalibData = append([]byte(nil), raw...)
			o.mu.Unlock()
		}
	}
	return firstErr
}

func (o *Orchestrator) HandleDataReportAck(nodeID string, edgeDeviceID uint, requestID uint32, errorCode uint64, raw []byte) {
	if nodeID == "" || edgeDeviceID == 0 || requestID == 0 {
		return
	}
	o.pendingMu.Lock()
	pending, ok := o.pendingResp[requestID]
	if !ok || pending.nodeID != nodeID || pending.edgeDeviceID != edgeDeviceID || pending.responseKind != responseData {
		o.pendingMu.Unlock()
		return
	}
	o.pendingMu.Unlock()
	if errorCode != 0 {
		o.deliver(pending, pendingResult{err: fmt.Errorf("data report error: code=%d", errorCode)})
		return
	}
	if uint32(len(raw)) != pending.expectedReadSize {
		o.deliver(pending, pendingResult{err: fmt.Errorf("data report length %d, expected %d", len(raw), pending.expectedReadSize)})
		return
	}
	o.deliver(pending, pendingResult{raw: append([]byte(nil), raw...)})
}

func (o *Orchestrator) saveCalibData(device models.EdgeDevice, data []byte) error {
	if o.db == nil {
		return fmt.Errorf("database required")
	}
	if device.ID == 0 || len(data) != 24 {
		return fmt.Errorf("invalid calibration length %d", len(data))
	}
	allZero := true
	allFF := true
	for _, b := range data {
		if b != 0 {
			allZero = false
		}
		if b != 0xff {
			allFF = false
		}
	}
	if allZero || allFF {
		return fmt.Errorf("invalid uniform calibration")
	}
	value := models.CalibrationCache{NodeID: device.NodeID, EdgeDeviceID: device.ID, DeviceType: device.Type, Data: fmt.Sprintf("%x", data)}
	if err := o.db.Where("edge_device_id = ? AND device_type = ?", device.ID, device.Type).
		Assign(value).FirstOrCreate(&value).Error; err != nil {
		return fmt.Errorf("persist calibration: %w", err)
	}
	return nil
}

func (o *Orchestrator) IsInitialized(edgeDeviceID uint) bool {
	o.mu.RLock()
	defer o.mu.RUnlock()
	s, ok := o.cache[edgeDeviceID]
	return ok && s.Completed
}
func (o *Orchestrator) ClearCache(edgeDeviceID uint) {
	o.mu.Lock()
	delete(o.cache, edgeDeviceID)
	o.mu.Unlock()
}
func (o *Orchestrator) InitIfNeeded(device models.EdgeDevice, nodeID string) bool {
	state, err := o.reserve(device, nodeID)
	if err != nil {
		return false
	}
	go func() {
		if err := o.runReserved(device, nodeID, state); err != nil {
			logger.Warnf("[Init] edge device %d: %v", device.ID, err)
		}
	}()
	return true
}
func (o *Orchestrator) HasActiveInit(edgeDeviceID uint) bool {
	o.mu.RLock()
	defer o.mu.RUnlock()
	s, ok := o.cache[edgeDeviceID]
	return ok && s.InProgress
}
