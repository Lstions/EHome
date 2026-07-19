package nodemgr

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"ehome/backend/internal/commandexec"
	"ehome/backend/internal/databus"
	"ehome/backend/internal/deviceinit"
	"ehome/backend/internal/drivers"
	"ehome/backend/internal/homeassistant"
	"ehome/backend/internal/logstream"
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
	mqtt            mqtt.Publisher
	wsHub           *websocket.Hub
	ha              *homeassistant.Integration
	otaMgr          *ota.Manager
	hashMgr         *ConfigHashManager
	pendingWrite    *pendingwrite.Manager
	deviceInit      *deviceinit.Orchestrator
	termMgr         *terminal.Manager
	offlineDetector *offlinedetector.Detector
	driverRegistry  *drivers.Registry
	stopCh          chan struct{}
	stopOnce        sync.Once
	wg              sync.WaitGroup     // for worker pool graceful shutdown
	dataCh          chan dataReportJob // worker pool job channel

	// S3: requestID-level frame reassembly (P1-8 safety net)
	reassembler *streamReassembler

	// F7.6: Ping tracking for retry/timeout
	pingTracker *PingTracker

	// v2.1: Sync mechanism
	eventBus *ConfigEventBus
	syncGate *SyncGate

	// v2.5: Log stream event bus + consumer manager
	logBus        *logstream.LogEventBus
	logDBConsumer *logstream.DBConsumer
	logCleanup    *logstream.LogCleanup

	// v2.5: Data event bus (replaces inline processDataReportJob heavy logic)
	dataBus        *databus.DataEventBus
	periphMu       sync.Mutex
	periphIntentMu sync.Mutex
	periphPending  map[uint32]periphRequestMeta
	periphLatest   map[string]uint32
	commandExec    *commandexec.Service
}

type periphRequestMeta struct {
	deviceID                       string
	periphType, resourceID, action uint8
	hardwareID                     string
	pin                            int
	created                        time.Time
	previousValue                  uint32
	provisionalValue               uint32
}

func (m *Manager) LockPeriphIntent()   { m.periphIntentMu.Lock() }
func (m *Manager) UnlockPeriphIntent() { m.periphIntentMu.Unlock() }

// NewManager creates a new node manager
func NewManager(db *gorm.DB, mqttClient *mqtt.Client, wsHub *websocket.Hub, ha *homeassistant.Integration, offlineDetector *offlinedetector.Detector, otaMgr *ota.Manager, registries ...*drivers.Registry) *Manager {
	driverRegistry := drivers.NewRegistry()
	if len(registries) > 0 && registries[0] != nil {
		driverRegistry = registries[0]
	} else {
		drivers.RegisterBuiltInDrivers(driverRegistry)
	}
	mgr := &Manager{
		db:              db,
		mqtt:            mqttClient,
		wsHub:           wsHub,
		ha:              ha,
		otaMgr:          otaMgr,
		hashMgr:         NewConfigHashManager(),
		pendingWrite:    pendingwrite.NewManager(mqttClient, db),
		deviceInit:      deviceinit.NewOrchestrator(db, mqttClient),
		termMgr:         terminal.NewManager(),
		offlineDetector: offlineDetector,
		driverRegistry:  driverRegistry,
		stopCh:          make(chan struct{}),
		reassembler:     newStreamReassembler(),
		periphPending:   make(map[uint32]periphRequestMeta),
		periphLatest:    make(map[string]uint32),
	}
	mgr.pingTracker = NewPingTracker()

	// v2.2: Initialize sync mechanism (no epoch generator)
	mgr.eventBus = NewConfigEventBus(1024)
	mgr.syncGate = NewSyncGate(mgr, mgr.eventBus)

	// v2.5: Initialize log stream event bus + consumers
	mgr.logBus = logstream.NewLogEventBus()
	mgr.logDBConsumer = logstream.NewDBConsumer(db)
	mgr.logBus.Register(mgr.logDBConsumer)
	mgr.logBus.Register(logstream.NewWSConsumer(wsHub))

	// NodeLog is migrated at application startup with the rest of the schema;
	// do not make manager construction perform DDL in request/unit-test paths.
	mgr.logCleanup = logstream.NewLogCleanup(db, 72*time.Hour, time.Hour)
	mgr.logCleanup.Start()

	// v2.5: Initialize data event bus + consumers
	mgr.dataBus = databus.NewDataEventBus()
	mgr.dataBus.Register(databus.NewTerminalConsumer(mgr.termMgr))
	mgr.dataBus.Register(databus.NewWSPushConsumer(wsHub))
	mgr.dataBus.Register(databus.NewDataMetricsConsumer())
	mgr.dataBus.Register(databus.NewPendingWriteConsumer(mgr.pendingWrite, mgr.deviceInit, db))
	mgr.dataBus.Register(databus.NewDBPersistConsumer(db))
	if offlineDetector != nil {
		mgr.dataBus.Register(databus.NewSensorParserConsumerWithRegistry(db, wsHub, ha, mgr.reassembler, driverRegistry, offlineDetector.OnEdgeDeviceData))
	} else {
		mgr.dataBus.Register(databus.NewSensorParserConsumerWithRegistry(db, wsHub, ha, mgr.reassembler, driverRegistry))
	}

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
	if m.dataBus != nil {
		m.dataBus.Stop()
	}
	if m.logBus != nil {
		m.logBus.Stop()
	}
	if m.logCleanup != nil {
		m.logCleanup.Stop()
	}
	logger.Infof("All workers drained")
}

// Stop begins graceful shutdown. It is safe to call more than once.
func (m *Manager) Stop() {
	m.stopOnce.Do(func() {
		close(m.stopCh)
	})
}

// PendingWrite returns the pending write manager for external access (e.g. API)
func (m *Manager) PendingWrite() *pendingwrite.Manager {
	return m.pendingWrite
}

// TerminalMgr returns the terminal manager for external access
func (m *Manager) TerminalMgr() *terminal.Manager {
	return m.termMgr
}

// SetCommandExecutionService connects the independently constructed control
// domain after composition root setup. Keeping this setter explicit avoids a
// nodemgr↔commandexec construction cycle.
func (m *Manager) SetCommandExecutionService(service *commandexec.Service) {
	m.commandExec = service
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
	case frame.MsgChannelCmdV2Ack, frame.MsgChannelCmdV2Final:
		m.handleChannelCmdV2Response(deviceID, payload)
	case frame.MsgPeriphRsp:
		m.handlePeriphResponse(deviceID, payload)
	case frame.MsgLogStream:
		m.handleLogStream(deviceID, payload)

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

// buildHashData serializes templates + channels + edge_devices + dmaConfigs into bytes for hash calculation.
// v2.4: added edge_devices — previously missing, causing hash to not change when edge_device
// config (hardware_id, interval_ms, enabled) was modified. Device would receive stale config after reboot.
func (m *Manager) buildHashData(
	templates []models.ConfigTemplate,
	channels []models.Channel,
	edgeDevices []models.EdgeDevice,
	deviceConfigs []models.DeviceConfig,
	dmaConfigs []models.DmaChannelConfig,
	gpioConfigs []models.GPIOConfig,
	pwmConfigs []models.PWMConfig,
) []byte {
	var buf []byte
	for _, t := range templates {
		buf = append(buf, []byte(fmt.Sprintf("t:%d:%s:%d:%d:",
			t.ID, t.WriteData, t.ReadLength, t.DelayMs))...)
	}
	for _, c := range channels {
		buf = append(buf, []byte(fmt.Sprintf("c:%d:%s:%s:%d:%v:%s:",
			c.ID, c.HardwareID, c.TemplateIDs, c.IntervalMs, c.Enabled, c.BusConfig))...)
	}
	for _, ed := range edgeDevices {
		buf = append(buf, []byte(fmt.Sprintf("e:%d:%d:%s:%d:%v:%s:",
			ed.ID, ed.DeviceConfigID, ed.HardwareID, ed.IntervalMs, ed.Enabled, string(ed.CommandIntervals)))...)
	}
	for _, dc := range deviceConfigs {
		buf = append(buf, []byte(fmt.Sprintf("dc:%d:%s:%s:%s:%s:%s:%s:%s:",
			dc.ID, dc.DeviceType, dc.DeviceModel, string(dc.Connection), string(dc.Parser), string(dc.InitFlow), string(dc.Operations), dc.Status))...)
	}
	for _, d := range dmaConfigs {
		buf = append(buf, []byte(fmt.Sprintf("d:%d:%v:%s:", d.DmaID, d.Enabled, d.BindTo))...)
	}
	for _, g := range gpioConfigs {
		buf = append(buf, []byte(fmt.Sprintf("g:%d:%d:%d:%v:", g.Pin, g.Direction, g.InitialLevel, g.Enabled))...)
	}
	for _, p := range pwmConfigs {
		buf = append(buf, []byte(fmt.Sprintf("p:%s:%d:%d:%d:%d:%d:%v:%v:",
			p.HardwareID, p.Channel, p.Pin, p.Frequency, p.Duty, p.Resolution, p.AutoStart, p.Enabled))...)
	}
	return buf
}

// triggerDeviceInit finds devices for a node and triggers initialization.
func (m *Manager) triggerDeviceInit(nodeID string, deviceID string) {
	devices, err := m.loadInitializableEdgeDevices(nodeID)
	if err != nil {
		logger.Warnf("[%s] Failed to load edge devices for init: %v", deviceID, err)
		return
	}
	for _, dev := range devices {
		if m.deviceInit.InitIfNeeded(dev, deviceID) {
			logger.Infof("[%s] Triggered device init: type=%s ch=%d", deviceID, dev.Type, dev.ChannelID)
		}
	}
}

// loadInitializableEdgeDevices applies the same fail-closed transport contract as API init.
func (m *Manager) loadInitializableEdgeDevices(nodeID string) ([]models.EdgeDevice, error) {
	var devices []models.EdgeDevice
	err := m.db.Joins("JOIN channels ON channels.id = edge_devices.channel_id").
		Where("edge_devices.node_id = ?", nodeID).
		Where("edge_devices.enabled = ?", true).
		Where("channels.enabled = ?", true).
		Where("channels.node_id = edge_devices.node_id").
		Where("UPPER(TRIM(channels.hardware_type)) NOT IN ?", []string{"GPIO", "PWM", "4", "6"}).
		Where("UPPER(TRIM(channels.bus_type)) NOT IN ?", []string{"GPIO", "PWM", "4", "6"}).
		Find(&devices).Error
	return devices, err
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

// ConfigHashResult holds the result of a config hash calculation.
type ConfigHashResult struct {
	Hash         string
	ManifestID   string
	ChannelCount int // number of server-side channels for the device
}

// CalcConfigHashForDevice calculates the config hash and manifest ID for a device.
// P2-1: Uses a transaction with REPEATABLE READ isolation so all four queries see a
// consistent snapshot (no partial view of a concurrent write).
// P2-6: All Find queries use ORDER BY id ASC so the hash is deterministic regardless
// of database row-ordering differences.
func (m *Manager) CalcConfigHashForDevice(deviceID string) ConfigHashResult {
	var result ConfigHashResult

	err := m.db.Transaction(func(tx *gorm.DB) error {
		// P2-1: REPEATABLE READ for consistent snapshot across queries.
		// Silently ignored by SQLite — the transaction itself still provides consistency.
		// Use helper instead of raw SQL so SQLite tests don't hit syntax errors.
		SetTransactionIsolation(tx)

		var node models.Node
		if err := tx.Where("node_id = ?", deviceID).First(&node).Error; err != nil {
			return err
		}

		// P2-6: ORDER BY id ASC for deterministic hash
		var templates []models.ConfigTemplate
		tx.Order("id ASC").Where("node_id = ?", node.NodeID).Find(&templates)
		var channels []models.Channel
		tx.Order("id ASC").Where("node_id = ?", node.NodeID).Find(&channels)

		// 从 node.Config JSON 解析 DMA configs
		var dmaConfigs []models.DmaChannelConfig
		if node.Config != "" {
			var cfg map[string]interface{}
			if err := json.Unmarshal([]byte(node.Config), &cfg); err == nil {
				if dc, ok := cfg["dma_configs"]; ok {
					if dcJSON, err := json.Marshal(dc); err == nil {
						json.Unmarshal(dcJSON, &dmaConfigs)
					}
				}
			}
		}

		// v2.4: query edge_devices for hash calculation
		// P2-6: ORDER BY id ASC for deterministic hash
		var edgeDevices []models.EdgeDevice
		tx.Order("id ASC").Where("node_id = ? AND enabled = true", node.NodeID).Find(&edgeDevices)

		deviceConfigIDs := make([]uint, 0, len(edgeDevices))
		seenDeviceConfigIDs := make(map[uint]struct{}, len(edgeDevices))
		for _, ed := range edgeDevices {
			if ed.DeviceConfigID > 0 {
				if _, ok := seenDeviceConfigIDs[ed.DeviceConfigID]; !ok {
					seenDeviceConfigIDs[ed.DeviceConfigID] = struct{}{}
					deviceConfigIDs = append(deviceConfigIDs, ed.DeviceConfigID)
				}
			}
		}
		var deviceConfigs []models.DeviceConfig
		if len(deviceConfigIDs) > 0 {
			tx.Order("id ASC").Where("id IN ?", deviceConfigIDs).Find(&deviceConfigs)
		}

		// v3.0: query GPIO/PWM configs for hash calculation
		var gpioConfigs []models.GPIOConfig
		tx.Order("pin ASC").Where("node_id = ?", node.NodeID).Find(&gpioConfigs)
		var pwmConfigs []models.PWMConfig
		tx.Order("pin ASC").Where("node_id = ?", node.NodeID).Find(&pwmConfigs)

		hashData := m.buildHashData(templates, channels, edgeDevices, deviceConfigs, dmaConfigs, gpioConfigs, pwmConfigs)
		// v2.5: include log_stream config in hash so changes trigger manifest push
		hashData = append(hashData, []byte(fmt.Sprintf("ls:%v:%d:", node.LogStreamEnabled, node.LogStreamLevel))...)
		hash := m.hashMgr.CalcConfigHash(hashData)
		manifestID := fmt.Sprintf("v2-%s", hash)

		result = ConfigHashResult{
			Hash:         hash,
			ManifestID:   manifestID,
			ChannelCount: len(channels),
		}
		return nil
	})

	if err != nil {
		return ConfigHashResult{}
	}
	return result
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
		driver, err := m.driverRegistry.Get(dev.Type)
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

// SetTransactionIsolation sets the transaction isolation level to REPEATABLE READ
// on PostgreSQL. On SQLite it is a no-op because SQLite transactions are
// SERIALIZABLE by default (strictly stronger than REPEATABLE READ), and the
// SET TRANSACTION syntax is not supported.
func SetTransactionIsolation(tx *gorm.DB) {
	dialect := tx.Dialector.Name()
	if dialect == "postgres" {
		tx.Exec("SET TRANSACTION ISOLATION LEVEL REPEATABLE READ")
	}
}
