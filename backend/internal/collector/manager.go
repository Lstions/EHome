package collector

import (
	"fmt"
	"strings"
	"sync"

	"ehome/backend/internal/deviceinit"
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

// Manager handles collector lifecycle and message processing
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
	wg              sync.WaitGroup  // for worker pool graceful shutdown
	dataCh          chan dataReportJob // worker pool job channel
}

// NewManager creates a new collector manager
func NewManager(db *gorm.DB, mqttClient *mqtt.Client, wsHub *websocket.Hub, ha *homeassistant.Integration, offlineDetector *offlinedetector.Detector) *Manager {
	mgr := &Manager{
		db:              db,
		mqtt:            mqttClient,
		wsHub:           wsHub,
		ha:              ha,
		hashMgr:         NewConfigHashManager(),
		pendingWrite:    pendingwrite.NewManager(mqttClient),
		deviceInit:      deviceinit.NewOrchestrator(db, mqttClient),
		termMgr:         terminal.NewManager(),
		offlineDetector: offlineDetector,
		stopCh:          make(chan struct{}),
	}
	mgr.otaMgr = ota.NewManager(db, mqttClient, wsHub)
	return mgr
}

func (m *Manager) Start() {
	m.startWorkerPool()
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
	case frame.MsgOtaProg:
		m.otaMgr.HandleOtaProgress(deviceID, payload)
	case frame.MsgScanRpt:
		m.handleScanReport(deviceID, payload)
	case frame.MsgQueryRsp:
		m.handleQueryResponse(deviceID, payload)
	case frame.MsgConfigReport:
		m.handleConfigReport(deviceID, payload)
	default:
		logger.Infof("[%s] Unknown msg type: 0x%02X", deviceID, msgType)
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
		buf = append(buf, []byte(fmt.Sprintf("c:%d:%d:%s:%d:%v:%s:", c.ID, c.HardwareID, c.TemplateIDs, c.IntervalMs, c.Enabled, c.BusConfig))...)
	}
	return buf
}

// triggerDeviceInit finds devices for a collector and triggers initialization
func (m *Manager) triggerDeviceInit(collectorID uint, deviceID string) {
	var devices []models.Device
	m.db.Joins("JOIN channels ON channels.id = devices.channel_id").
		Where("channels.collector_id = ?", collectorID).
		Find(&devices)

	for _, dev := range devices {
		if m.deviceInit.InitIfNeeded(deviceID, uint32(dev.ChannelID), dev.Type) {
			logger.Infof("[%s] Triggered device init: type=%s ch=%d", deviceID, dev.Type, dev.ChannelID)
		}
	}
}
