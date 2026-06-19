package nodemgr

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"ehome/backend/internal/deviceinit"
	"ehome/backend/internal/drivers"
	"ehome/backend/internal/homeassistant"
	"ehome/backend/internal/models"
	"ehome/backend/internal/mqtt"
	"ehome/backend/internal/offlinedetector"
	"ehome/backend/internal/ota"
	"ehome/backend/internal/pendingwrite"
	"ehome/backend/internal/terminal"
	"ehome/backend/internal/websocket"
	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/logger"
	"ehome/backend/pkg/metrics"

	"gorm.io/gorm"
)

// Manager handles node lifecycle and message processing
type Manager struct {
	db              *gorm.DB
	mqtt            *mqtt.Client
	wsHub           *websocket.Hub
	ha              *homeassistant.Integration
	otaMgr          *ota.Manager
	hashMgr         *ConfigHashManager
	pendingWrite    *pendingwrite.Manager
	deviceInit      *deviceinit.Orchestrator
	termMgr         *terminal.Manager
	offlineDetector *offlinedetector.Detector
	stopCh          chan struct{}
	wg              sync.WaitGroup     // for worker pool graceful shutdown
	dataCh          chan dataReportJob // worker pool job channel

	// F7.6: Ping tracking for retry/timeout
	pingTracker *PingTracker

	// v2.1: Sync mechanism
	eventBus *ConfigEventBus
	syncGate *SyncGate
}

// NewManager creates a new node manager
func NewManager(db *gorm.DB, mqttClient *mqtt.Client, wsHub *websocket.Hub, ha *homeassistant.Integration, offlineDetector *offlinedetector.Detector, otaMgr *ota.Manager) *Manager {
	mgr := &Manager{
		db:              db,
		mqtt:            mqttClient,
		wsHub:           wsHub,
		ha:              ha,
		otaMgr:          otaMgr,
		hashMgr:         NewConfigHashManager(),
		pendingWrite:    pendingwrite.NewManager(mqttClient),
		deviceInit:      deviceinit.NewOrchestrator(db, mqttClient),
		termMgr:         terminal.NewManager(),
		offlineDetector: offlineDetector,
		stopCh:          make(chan struct{}),
	}
	mgr.pingTracker = NewPingTracker()

	// v2.1: Initialize sync mechanism
	epochGen := NewEpochGenerator(db)
	if err := epochGen.Restore(); err != nil {
		logger.Warnf("EpochGenerator restore failed: %v (will use time-based seed)", err)
	}
	mgr.eventBus = NewConfigEventBus(1024, epochGen)
	mgr.syncGate = NewSyncGate(mgr, mgr.eventBus)

	// G10: Record initial node online count
	var onlineCount int64
	mgr.db.Model(&models.Node{}).Where("status = ?", "online").Count(&onlineCount)
	metrics.NodesOnline.Set(float64(onlineCount))

	return mgr
}

func (m *Manager) Start() {
	m.startWorkerPool()

	// v2.1: Start SyncGate event consumer
	m.syncGate.Start()

	logger.Infof("Collector manager started")
	<-m.stopCh
	// Drain workers: close channel then wait
	close(m.dataCh)
	m.wg.Wait()
	logger.Infof("All workers drained")
}

func (m *Manager) Stop() {
	close(m.stopCh)
}

// PendingWrite returns the pending write manager for external access (e.g. API)
func (m *Manager) PendingWrite() *pendingwrite.Manager {
	return m.pendingWrite
}

// TerminalMgr returns the terminal manager for external access
func (m *Manager) TerminalMgr() *terminal.Manager {
	return m.termMgr
}

// HandleMessage processes incoming MQTT messages from devices
func (m *Manager) HandleMessage(topic string, payload []byte) {
	parts := strings.Split(topic, "/")
	if len(parts) < 3 {
		logger.Infof("Invalid topic: %s", topic)
		return
	}
	deviceID := parts[1]

	// Update heartbeat on every message (Redis TTL refresh)
	if m.offlineDetector != nil {
		m.offlineDetector.UpdateHeartbeat(deviceID)
	}

	if len(payload) < 1 {
		logger.Infof("Empty payload from %s", deviceID)
		return
	}

	msgType := payload[0]
	logger.Infof("[%s] Received msg type 0x%02X (%d bytes)", deviceID, msgType, len(payload))

	// Record message type as metric
	typeName := frame.MsgTypeName(msgType)
	metrics.MessagesReceived.WithLabelValues(typeName).Inc()

	switch msgType {
	case frame.MsgHello:
		m.handleHello(deviceID, payload)
	case frame.MsgStatusRpt:
		m.handleStatusReport(deviceID, payload)
	case frame.MsgDataRpt:
		m.handleDataReport(deviceID, payload)
	case frame.MsgConfigRslt:
		m.handleConfigResult(deviceID, payload)
	case frame.MsgWriteRsp:
		m.handleWriteResponse(deviceID, payload)
	case frame.MsgPong:
		m.handlePong(deviceID, payload)
	case frame.MsgPing:
		m.handlePing(deviceID, payload) // BUG-12: handle device→server Ping
	case frame.MsgOtaProg:
		m.otaMgr.HandleOtaProgress(deviceID, payload)
	case frame.MsgScanRpt:
		m.handleScanReport(deviceID, payload)
	case frame.MsgQueryRsp:
		m.handleQueryResponse(deviceID, payload)
	case frame.MsgConfigReport:
		m.handleConfigReport(deviceID, payload)
	case frame.MsgConfigSyncReq:
		m.handleConfigSyncRequest(deviceID, payload)
	case frame.MsgResourceReport:
		m.handleResourceReport(deviceID, payload)

	// Unimplemented message types — log warning, no panic
	case frame.MsgConfigMfst:
		logger.Warnf("[%s] Unimplemented msg type: ConfigManifest (0x%02X) — no handler", deviceID, msgType)
	case frame.MsgOtaCmd:
		logger.Warnf("[%s] Unimplemented msg type: OtaCommand (0x%02X) — no handler", deviceID, msgType)
	case frame.MsgScanReq:
		logger.Warnf("[%s] Unimplemented msg type: ScanRequest (0x%02X) — no handler", deviceID, msgType)
	case frame.MsgConfigQuery:
		logger.Warnf("[%s] Unimplemented msg type: ConfigQuery (0x%02X) — no handler", deviceID, msgType)
	case frame.MsgConfigSyncRsp:
		logger.Warnf("[%s] Unimplemented msg type: ConfigSyncResponse (0x%02X) — no handler", deviceID, msgType)

	default:
		logger.Warnf("[%s] Unknown msg type: 0x%02X", deviceID, msgType)
	}
}

// === Helpers ===

// buildHashData serializes templates + channels into bytes for hash calculation
func (m *Manager) buildHashData(templates []models.ConfigTemplate, channels []models.Channel) []byte {
	var buf []byte
	for _, t := range templates {
		buf = append(buf, []byte(fmt.Sprintf("t:%d:%s:%d:%d:", t.ID, t.WriteData, t.ReadLength, t.DelayMs))...)
	}
	for _, c := range channels {
		buf = append(buf, []byte(fmt.Sprintf("c:%d:%s:%s:%d:%v:%s:", c.ID, c.HardwareID, c.TemplateIDs, c.IntervalMs, c.Enabled, c.BusConfig))...)
	}
	return buf
}

// triggerDeviceInit finds devices for a node and triggers initialization
func (m *Manager) triggerDeviceInit(nodeID string, deviceID string) {
	var devices []models.EdgeDevice
	m.db.Joins("JOIN channels ON channels.id = edge_devices.channel_id").
		Where("channels.node_id = ?", nodeID).
		Find(&devices)

	for _, dev := range devices {
		if m.deviceInit.InitIfNeeded(deviceID, uint32(dev.ChannelID), dev.Type) {
			logger.Infof("[%s] Triggered device init: type=%s ch=%d", deviceID, dev.Type, dev.ChannelID)
		}
	}
}

// DeviceInit returns the device init orchestrator for external access (e.g. API handlers)
func (m *Manager) DeviceInit() *deviceinit.Orchestrator {
	return m.deviceInit
}

// EventBus returns the ConfigEventBus for external access (e.g. API handlers)
func (m *Manager) EventBus() *ConfigEventBus {
	return m.eventBus
}

// SyncGate returns the SyncGate for external access (e.g. main.go startup)
func (m *Manager) SyncGate() *SyncGate {
	return m.syncGate
}

// BuildManifestID generates a new manifest ID for config sync.
func (m *Manager) BuildManifestID() string {
	return fmt.Sprintf("v2-%d", time.Now().UnixMilli())
}

// ConfigHashResult holds the result of a config hash calculation.
type ConfigHashResult struct {
	Hash         string
	ManifestID   string
	ChannelCount int // number of server-side channels for the device
}

// CalcConfigHashForDevice calculates the config hash and manifest ID for a device.
func (m *Manager) CalcConfigHashForDevice(deviceID string) ConfigHashResult {
	var node models.Node
	if err := m.db.Where("node_id = ?", deviceID).First(&node).Error; err != nil {
		return ConfigHashResult{}
	}

	var templates []models.ConfigTemplate
	m.db.Where("node_id = ?", node.NodeID).Find(&templates)
	var channels []models.Channel
	m.db.Where("node_id = ?", node.NodeID).Find(&channels)

	hashData := m.buildHashData(templates, channels)
	hash := m.hashMgr.CalcConfigHash(hashData, nil)
	manifestID := node.ConfigVersion
	if manifestID == "" {
		manifestID = "none"
	}

	return ConfigHashResult{Hash: hash, ManifestID: manifestID, ChannelCount: len(channels)}
}

// GetDeviceIDByNodeID resolves a node DB ID to its node_id string.
func (m *Manager) GetDeviceIDByNodeID(nodeDBID uint) string {
	var node models.Node
	if err := m.db.First(&node, nodeDBID).Error; err != nil {
		return ""
	}
	return node.NodeID
}

// GetOnlineDeviceIDs returns device IDs of all online nodes.
func (m *Manager) GetOnlineDeviceIDs() []string {
	var collectors []models.Node
	m.db.Where("status = ?", "online").Find(&collectors)
	ids := make([]string, 0, len(collectors))
	for _, c := range collectors {
		ids = append(ids, c.NodeID)
	}
	return ids
}

// publishHADiscovery publishes HomeAssistant MQTT Discovery for all devices of a node
func (m *Manager) publishHADiscovery(collectorID string, deviceID string) {
	if m.ha == nil {
		return
	}

	var devices []models.EdgeDevice
	m.db.Joins("JOIN channels ON channels.id = edge_devices.channel_id").
		Where("channels.node_id = ?", collectorID).
		Find(&devices)

	for _, dev := range devices {
		driver, err := drivers.Get(dev.Type)
		if err != nil {
			continue
		}
		sensors := driver.GetSensorDefinitions()
		if len(sensors) == 0 {
			continue
		}
		if err := m.ha.PublishDiscovery(deviceID, dev.Name, dev.Type, sensors); err != nil {
			logger.Infof("[%s] HA Discovery failed for device %s: %v", deviceID, dev.Name, err)
		} else {
			logger.Infof("[%s] HA Discovery published for device %s (%s)", deviceID, dev.Name, dev.Type)
		}
	}
}
