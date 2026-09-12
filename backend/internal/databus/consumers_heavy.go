package databus

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/deviceinit"
	"ehome/backend/internal/drivers"
	"ehome/backend/internal/events"
	"ehome/backend/internal/homeassistant"
	"ehome/backend/internal/models"
	"ehome/backend/internal/pendingwrite"
	"ehome/backend/internal/websocket"
	"ehome/backend/pkg/logger"
	"ehome/backend/pkg/metrics"
	"ehome/backend/pkg/parser"

	"gorm.io/gorm"
)

// Reassembler is the interface for frame reassembly (implemented by nodemgr.streamReassembler).
// Buffering is scoped per (deviceID, requestID): requestID alone is not
// unique across devices once consumers run concurrently.
type Reassembler interface {
	Append(deviceID string, requestID uint32, data []byte) []byte
	Consume(deviceID string, requestID uint32)
}

// PendingWriteConsumer routes command responses to the pendingWrite manager
// for WriteCmd acknowledgment and device initialization tracking.
type PendingWriteConsumer struct {
	pendingWrite *pendingwrite.Manager
	deviceInit   *deviceinit.Orchestrator
	db           *gorm.DB
}

func NewPendingWriteConsumer(pw *pendingwrite.Manager, di *deviceinit.Orchestrator, db *gorm.DB) *PendingWriteConsumer {
	return &PendingWriteConsumer{pendingWrite: pw, deviceInit: di, db: db}
}

func (c *PendingWriteConsumer) Name() string { return "pending_write" }
func (c *PendingWriteConsumer) ShouldHandle(evt DataEvent) bool {
	return evt.RequestID != 0
}
func (c *PendingWriteConsumer) Handle(evt DataEvent) {
	if c.pendingWrite != nil {
		c.pendingWrite.HandleDataReportResult(uint32(evt.RequestID), evt.RawData, evt.ErrorCode)
	}
	// Device-init correlation is EdgeDevice-scoped.  A BMP280 response must
	// never satisfy another BMP280's initialization on the same node.
	if c.deviceInit != nil && evt.EdgeDeviceID > 0 && evt.RequestID != 0 {
		c.deviceInit.HandleDataReportAck(evt.DeviceID, uint(evt.EdgeDeviceID), uint32(evt.RequestID), evt.ErrorCode, evt.RawData)
	}
}

// DBPersistConsumer writes raw data to device_data table for audit/history.
// It persists command responses and scheduler samples; uncorrelated terminal
// RX data remains memory/WS-only.
type DBPersistConsumer struct {
	db *gorm.DB
}

func NewDBPersistConsumer(db *gorm.DB) *DBPersistConsumer {
	return &DBPersistConsumer{db: db}
}

func (c *DBPersistConsumer) Name() string { return "db_persist" }
func (c *DBPersistConsumer) ShouldHandle(evt DataEvent) bool {
	return evt.ShouldPersist()
}
func (c *DBPersistConsumer) Handle(evt DataEvent) {
	if c.db == nil {
		return
	}
	dataJSON, err := json.Marshal(map[string]interface{}{
		"raw":            fmt.Sprintf("%x", evt.RawData),
		"channel":        evt.ChannelID,
		"sequence":       evt.Sequence,
		"error_code":     evt.ErrorCode,
		"request_id":     evt.RequestID,
		"edge_device_id": evt.EdgeDeviceID,
		"command_index":  evt.CommandIndex,
	})
	if err != nil {
		logger.Warn("databus: failed to marshal raw device data", "consumer", c.Name(), "node_id", evt.DeviceID, "error", err)
		return
	}
	if err := c.db.Session(&gorm.Session{}).Create(&models.DeviceData{
		NodeID:    evt.DeviceID,
		DataJSON:  string(dataJSON),
		Timestamp: evt.ReceivedAt,
	}).Error; err != nil {
		metrics.DataConsumerDBWriteFailures.WithLabelValues(c.Name(), "device_data").Inc()
		logger.Warn("databus: failed to persist raw device data", "consumer", c.Name(), "node_id", evt.DeviceID, "error", err)
	}
}

// SensorParserConsumer parses sensor data, stores to unified_data,
// updates edge_device status, publishes to HomeAssistant, and broadcasts
// data_update WebSocket events. This is the heaviest consumer.
// Only handles command responses with valid data (not passive, not error).
type SensorParserConsumer struct {
	db             *gorm.DB
	wsHub          *websocket.Hub
	ha             *homeassistant.Integration
	reassembler    Reassembler
	deviceActivity func(uint)
	driverRegistry *drivers.Registry
	// rollupSink 数据层时序化 (v3.4 §3.2.2): 解析成功并持久化后的聚合回调
	// (注入 RollupConsumer.Upsert)。nil 时跳过。
	rollupSink func([]models.UnifiedData)
	// latestSink 数据层时序化 (v3.4 §3.2.4): 最新值缓存更新回调 (api.SetLatestValue)。
	latestSink func(models.UnifiedData)
	// alertSink 阈值告警引擎 (方案 v0.4 §5 任务C): 解析后回调注入 alert.Evaluator。
	// 复用 rollupSink 回调先例, 只对解析成功的物理量求值, nil 时跳过。
	alertSink func(edgeDeviceID uint, fields []parser.Field, at time.Time)
	// automationSink 自动化策略引擎 (设计/自动化策略引擎方案.md v0.1): 解析后回调注入
	// automation.Evaluator, 与 alertSink 并列不合并。nil 时跳过。
	automationSink func(edgeDeviceID uint, fields []parser.Field, at time.Time)
	// sourceHealthSink 数据源健康 (设计/数据源主备与故障转移.md §4): 解析成功回调。
	// 参数一: 边缘设备 ID; 参数二: 本次解析出的敏感量名; 参数三: 时间。
	sourceHealthSink func(edgeDeviceID uint, sensorNames []string, at time.Time)
}

func NewSensorParserConsumer(db *gorm.DB, wsHub *websocket.Hub, ha *homeassistant.Integration, reassembler Reassembler, deviceActivity ...func(uint)) *SensorParserConsumer {
	driverRegistry := drivers.NewRegistry()
	drivers.RegisterBuiltInDrivers(driverRegistry)
	return NewSensorParserConsumerWithRegistry(db, wsHub, ha, reassembler, driverRegistry, deviceActivity...)
}

func NewSensorParserConsumerWithRegistry(db *gorm.DB, wsHub *websocket.Hub, ha *homeassistant.Integration, reassembler Reassembler, driverRegistry *drivers.Registry, deviceActivity ...func(uint)) *SensorParserConsumer {
	consumer := &SensorParserConsumer{db: db, wsHub: wsHub, ha: ha, reassembler: reassembler, driverRegistry: driverRegistry}
	if len(deviceActivity) > 0 {
		consumer.deviceActivity = deviceActivity[0]
	}
	return consumer
}

// SetRollupSink 注入 rollup 聚合回调 (数据层时序化 v3.4 §3.2.2)。
func (c *SensorParserConsumer) SetRollupSink(sink func([]models.UnifiedData)) {
	c.rollupSink = sink
}

// SetLatestSink 注入最新值缓存更新回调 (数据层时序化 v3.4 §3.2.4)。
func (c *SensorParserConsumer) SetLatestSink(sink func(models.UnifiedData)) {
	c.latestSink = sink
}

// SetAlertSink 注入阈值告警求值回调 (方案 v0.4 §5.1.2, main.go 接线)。
func (c *SensorParserConsumer) SetAlertSink(sink func(edgeDeviceID uint, fields []parser.Field, at time.Time)) {
	c.alertSink = sink
}

// SetAutomationSink 注入自动化策略求值回调 (设计/自动化策略引擎方案.md v0.1,
// main.go 接线), 与 alertSink 并列不合并。
func (c *SensorParserConsumer) SetAutomationSink(sink func(edgeDeviceID uint, fields []parser.Field, at time.Time)) {
	c.automationSink = sink
}

// SetSourceHealthSink 注入数据源健康成功回调 (设计/数据源主备与故障转移.md §4,
// main.go 经 nodemgr 二阶段接线), 与 alertSink/automationSink 同点并列。
func (c *SensorParserConsumer) SetSourceHealthSink(sink func(edgeDeviceID uint, sensorNames []string, at time.Time)) {
	c.sourceHealthSink = sink
}

// HasSourceHealthSink 报告解析成功健康回调是否已注入 (接线测试只读辅助;
// 项目历史上缺此类断言导致 alert/automation 引擎"从未触发"而单测全绿)。
func (c *SensorParserConsumer) HasSourceHealthSink() bool {
	return c.sourceHealthSink != nil
}

func (c *SensorParserConsumer) Name() string { return "sensor_parser" }
func (c *SensorParserConsumer) ShouldHandle(evt DataEvent) bool {
	return evt.ShouldParse()
}
func (c *SensorParserConsumer) Handle(evt DataEvent) {
	if c.db == nil || c.reassembler == nil {
		return
	}

	// Frame reassembly for multi-frame protocols
	merged := c.reassembler.Append(evt.DeviceID, uint32(evt.RequestID), evt.RawData)

	// Find edge device. Preserve both firmware encodings: explicit edge_device_id,
	// real channels.id, and legacy 0-based channel-list index.
	var device models.EdgeDevice
	if evt.EdgeDeviceID > 0 {
		if err := c.db.Preload("Node").Where("id = ?", evt.EdgeDeviceID).First(&device).Error; err != nil {
			return
		}
	} else {
		if err := c.db.Preload("Node").Where("channel_id = ? AND node_id = ?", evt.ChannelID, evt.DeviceID).First(&device).Error; err != nil {
			var channels []models.Channel
			if err := c.db.Where("node_id = ?", evt.DeviceID).Order("id ASC").Find(&channels).Error; err != nil {
				return
			}
			idx := int(evt.ChannelID)
			if idx < 0 || idx >= len(channels) {
				return
			}
			if err := c.db.Preload("Node").Where("channel_id = ? AND node_id = ?", channels[idx].ID, evt.DeviceID).First(&device).Error; err != nil {
				return
			}
		}
	}

	// ---- 摄入边界 fail-closed 门: 已注销节点不得再产生历史数据/WS 推送 ----
	// 节点注销是 nodes 行的软删除 (docs/设计/节点.md §注销), 注销时该节点的
	// channels/edge_devices 行按设计保留 (历史归属不能丢), 因此"edge_device 命中"
	// 绝不等于"节点仍存活": 旧通道上的上报仍会走到这里。
	// Preload("Node") 带软删范围, 对已注销节点返回零值 (Node.ID == 0, 已实测),
	// 对确实不存在的节点同样为零值 —— 两种情形统一按"节点不可用"拒收, 与
	// commandexec 的 fail-closed 门同风格。门内拦截范围: 解析 / unified_data /
	// device_data / rollup / 最新值缓存 / 告警 / 自动化 / 数据源健康 / HA / WS。
	// 注意: 这里不额外查库 (零值即拒收), 健康路径查询次数不变。
	if device.Node.ID == 0 {
		logger.Warn("databus: node retired; refusing sample at ingest boundary",
			"consumer", c.Name(),
			"node_id", device.NodeID,
			"reported_node_id", evt.DeviceID,
			"edge_device_id", device.ID,
			"channel_id", evt.ChannelID,
			"request_id", evt.RequestID,
			"reason", c.nodeIngestDropReason(device.NodeID),
		)
		// 帧已完整重组, 丢弃前释放重组缓冲, 避免已注销节点持续上报堆积内存。
		c.reassembler.Consume(evt.DeviceID, uint32(evt.RequestID))
		return
	}

	// Parse sensor data
	var sensorData []parser.Field
	var parseMethod string

	// Primary: calibration-aware drivers must never be bypassed by a generic
	// DeviceConfig parser: calibration is part of their decoding invariant.
	drv, _ := c.driverRegistry.Get(device.Type)
	_, calibrationAware := drv.(drivers.CalibrationAwareDriver)
	if !calibrationAware && device.DeviceConfigID > 0 {
		var dc models.DeviceConfig
		if err := c.db.First(&dc, device.DeviceConfigID).Error; err == nil {
			if len(dc.Parser) > 0 && string(dc.Parser) != "{}" && string(dc.Parser) != "null" {
				cp, err := parser.NewConfigParser(dc.Parser)
				if err == nil {
					fields, err := cp.Parse(merged)
					if err == nil && len(fields) > 0 {
						sensorData = fields
						parseMethod = fmt.Sprintf("ConfigParser(%s)", dc.Name)
					}
				}
			}
		}
	}

	// Fallback: Driver registry. CommandAwareDriver receives the originating
	// ConfigTemplate.WriteData so protocols with identical response layouts can
	// select the correct parser branch.
	if sensorData == nil {
		drv, err := c.driverRegistry.Get(device.Type)
		if err != nil {
			c.reassembler.Consume(evt.DeviceID, uint32(evt.RequestID))
			return
		}

		var drvData []drivers.SensorData
		if calibrationDriver, ok := drv.(drivers.CalibrationAwareDriver); ok {
			var calibration models.CalibrationCache
			if err := c.db.Where("edge_device_id = ? AND device_type = ?", device.ID, device.Type).
				First(&calibration).Error; err != nil {
				logger.Warn("databus: calibration missing; refusing to persist sample", "node_id", evt.DeviceID, "edge_device_id", device.ID, "device_type", device.Type, "error", err)
				c.reassembler.Consume(evt.DeviceID, uint32(evt.RequestID))
				return
			}
			calibrationBytes, err := hex.DecodeString(calibration.Data)
			if err != nil {
				logger.Warn("databus: calibration encoding invalid; refusing to persist sample", "edge_device_id", device.ID, "error", err)
				c.reassembler.Consume(evt.DeviceID, uint32(evt.RequestID))
				return
			}
			drvData, err = calibrationDriver.ParseDataWithCalibration(merged, calibrationBytes)
		} else if commandAware, ok := drv.(drivers.CommandAwareDriver); ok {
			if evt.CommandTemplateID == 0 {
				logger.Infof("[%s] Command-aware driver requires command template context", evt.DeviceID)
				c.reassembler.Consume(evt.DeviceID, uint32(evt.RequestID))
				return
			}
			var template models.ConfigTemplate
			if lookupErr := c.db.Where("id = ? AND node_id = ?", evt.CommandTemplateID, evt.DeviceID).First(&template).Error; lookupErr != nil {
				logger.Infof("[%s] Failed to resolve command template %d: %v", evt.DeviceID, evt.CommandTemplateID, lookupErr)
				c.reassembler.Consume(evt.DeviceID, uint32(evt.RequestID))
				return
			}
			if template.WriteData == "" {
				logger.Infof("[%s] Command template %d has no write data", evt.DeviceID, evt.CommandTemplateID)
				c.reassembler.Consume(evt.DeviceID, uint32(evt.RequestID))
				return
			}
			drvData, err = commandAware.ParseDataWithCommand(merged, template.WriteData)
		} else {
			drvData, err = drv.ParseData(merged)
		}
		if err != nil {
			logger.Infof("[%s] Failed to parse data: %v", evt.DeviceID, err)
			c.reassembler.Consume(evt.DeviceID, uint32(evt.RequestID))
			return
		}
		sensorData = make([]parser.Field, len(drvData))
		for i, sd := range drvData {
			sensorData[i] = parser.Field{Name: sd.Name, Value: sd.Value, Unit: sd.Unit, StringValue: sd.StringValue}
		}
		parseMethod = fmt.Sprintf("Driver(%s)", device.Type)
	}

	// Parse succeeded — consume reassembly buffer
	c.reassembler.Consume(evt.DeviceID, uint32(evt.RequestID))

	// 写入双写 (§八): logical_device_id = 实例逻辑身份经 followMergeChain
	// 解析到最终目标 (v3.2-F1: 写入与查询同链 — 合并进行中实例仍在写入时,
	// 数据直接落目标, 不产生"目标缺新数据"窗口)。无逻辑身份的实例
	// (backfill 前旧数据) 保持 NULL, 由 dataScopeCondition 的 OR 回退分支
	// 按 device_id 兜住。解析失败时同样落 NULL + warn: device_id 血缘仍在,
	// 查询/清理的 OR 分支照常覆盖, 不丢数据。
	var logicalTarget *uint
	if device.LogicalDeviceID != nil && *device.LogicalDeviceID > 0 {
		target, err := datalifecycle.ResolveMergeTarget(c.db, *device.LogicalDeviceID)
		if err != nil {
			logger.Warn("databus: resolve merge target failed; writing logical_device_id NULL",
				"consumer", c.Name(), "edge_device_id", device.ID,
				"logical_device_id", *device.LogicalDeviceID, "error", err)
		} else if target > 0 {
			logicalTarget = &target
		}
	}

	// Store parsed data
	now := time.Now()
	records := make([]models.UnifiedData, 0, len(sensorData))
	for _, sd := range sensorData {
		records = append(records, models.UnifiedData{
			DeviceID:        device.ID,
			LogicalDeviceID: logicalTarget,
			SensorName:      sd.Name,
			Value:           sd.Value,
			Unit:            sd.Unit,
			Timestamp:       now,
		})
	}
	if len(records) > 0 {
		if err := c.db.Session(&gorm.Session{}).Create(&records).Error; err != nil {
			metrics.DataConsumerDBWriteFailures.WithLabelValues(c.Name(), "unified_data").Inc()
			logger.Warn("databus: failed to persist parsed sensor data", "consumer", c.Name(), "node_id", evt.DeviceID, "edge_device_id", device.ID, "error", err)
		} else if c.rollupSink != nil {
			// 数据层时序化 (v3.4 §3.2.2/§3.2.4): 持久化成功后聚合进 rollup 表
			// (仅 PG 生效) + 更新最新值缓存 (回调注入, 保持 databus 不依赖 api 包)。
			c.rollupSink(records)
			if c.latestSink != nil {
				for i := range records {
					c.latestSink(records[i])
				}
			}
		}
	}

	// 阈值告警引擎 (方案 v0.4 §5.1.2): 解析成功后对物理量求值 (解析后回调,
	// 非独立 consumer — 避免每事件重复解析 RawData)。独立于持久化结果:
	// 解析成功即求值, 与 ShouldHandle 语义等价。
	if c.alertSink != nil && len(sensorData) > 0 {
		c.alertSink(device.ID, sensorData, now)
	}
	// 自动化策略引擎 (设计/自动化策略引擎方案.md v0.1): 与 alertSink 同点挂接,
	// 同一批解析后物理量 (裁决: 避免独立 consumer 的重复解析开销)。
	if c.automationSink != nil && len(sensorData) > 0 {
		c.automationSink(device.ID, sensorData, now)
	}
	// 数据源健康 (设计/数据源主备与故障转移.md §4): 解析成功回调, 与
	// alertSink/automationSink 同点挂接。空集合也调用——服务层按 category
	// 匹配, 不命中则不更新。sink 失败/panic 只 Warn, 绝不影响入库/推送。
	if c.sourceHealthSink != nil {
		sensorNames := make([]string, 0, len(sensorData))
		for i := range sensorData {
			sensorNames = append(sensorNames, sensorData[i].Name)
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					logger.Warn("databus: source health sink panicked", "consumer", c.Name(), "edge_device_id", device.ID, "panic", r)
				}
			}()
			c.sourceHealthSink(device.ID, sensorNames, now)
		}()
	}

	// Update edge device status. Keep last_data_at fresh for every successful
	// sample, and notify the offline detector even when no status transition
	// occurs so its active-device cache cannot age out a healthy device.
	result := c.db.Model(&device).Where("status = ?", "offline").Updates(map[string]interface{}{
		"last_data_at": now,
		"status":       "active",
	})
	if result.RowsAffected == 0 {
		c.db.Model(&device).Updates(map[string]interface{}{"last_data_at": now})
	}
	if c.deviceActivity != nil {
		c.deviceActivity(device.ID)
	}
	if result.RowsAffected > 0 && c.wsHub != nil {
		c.wsHub.BroadcastEvent(events.EdgeDeviceStatus, map[string]interface{}{
			"edge_device_id": device.ID,
			"device_id":      device.ID,
			"device_name":    device.Name,
			"node_id":        device.NodeID,
			"channel_id":     device.ChannelID,
			"status":         "active",
			"reason":         "data_received",
		})
	}

	// Store raw data for this edge device
	dataJSON, err := json.Marshal(map[string]interface{}{
		"raw_hex":    fmt.Sprintf("%x", merged),
		"sensors":    sensorData,
		"channel_id": evt.ChannelID,
		"timestamp":  now.UnixMilli(),
	})
	if err != nil {
		logger.Warn("databus: failed to marshal parsed device data", "consumer", c.Name(), "node_id", evt.DeviceID, "edge_device_id", device.ID, "error", err)
	} else if err := c.db.Session(&gorm.Session{}).Create(&models.DeviceData{
		DeviceID:        device.ID,
		LogicalDeviceID: logicalTarget,
		NodeID:          evt.DeviceID,
		DataJSON:        string(dataJSON),
		Timestamp:       now,
	}).Error; err != nil {
		metrics.DataConsumerDBWriteFailures.WithLabelValues(c.Name(), "device_data").Inc()
		logger.Warn("databus: failed to persist parsed device data", "consumer", c.Name(), "node_id", evt.DeviceID, "edge_device_id", device.ID, "error", err)
	}

	// HomeAssistant publish
	if c.ha != nil {
		haData := make([]drivers.SensorData, len(sensorData))
		for i, f := range sensorData {
			haData[i] = drivers.SensorData{Name: f.Name, Value: f.Value, Unit: f.Unit}
		}
		c.ha.PublishState(evt.DeviceID, haData)
	}

	// Broadcast the legacy-compatible parsed channel_data payload. Terminal clients
	// still receive raw uncorrelated RX reports through WSPushConsumer.
	dataMap := make(map[string]interface{}, len(sensorData))
	for _, sd := range sensorData {
		dataMap[sd.Name] = sd.Value
	}
	if c.wsHub != nil {
		channelEvent := map[string]interface{}{
			"device_id":        evt.DeviceID,
			"node_id":          evt.DeviceID,
			"channel_id":       evt.ChannelID,
			"raw_hex":          fmt.Sprintf("%x", evt.RawData),
			"timestamp":        now.Unix(),
			"error_code":       evt.ErrorCode,
			"request_id":       evt.RequestID,
			"edge_device_id":   device.ID,
			"edge_device_name": device.Name,
			"command_index":    evt.CommandIndex,
			"data":             dataMap,
		}
		c.wsHub.BroadcastEvent(events.ChannelData, channelEvent)
	}

	// Broadcast data_update with canonical terminology: node = node_id/node_name,
	// edge device = edge_device_id/edge_device_name. The legacy collector_* names
	// and the duplicate device_id alias are no longer emitted.
	//
	// node_id carries the STRING node serial (device.NodeID). It used to carry
	// device.Node.ID, the numeric primary key, so the same field name meant two
	// different things inside this one function: channel_data above (evt.DeviceID),
	// edge_device_status and the REST API (handler_data.go, handler_node.go) all
	// use the string serial. No consumer needs the primary key - the frontend only
	// resolves node_id against serials (NodeList/NodeDetail/NodeOverview/
	// ChannelPanel compare it to a serial; stores/websocket.ts types it as
	// number-or-string; the data_update subscribers Dashboard.vue and
	// useRealtimeData.ts ignore node_id entirely) - and row identity is already
	// addressable through edge_device_id. Hence the unification, without adding a
	// separate node_db_id field that nothing consumes.
	if c.wsHub != nil && len(sensorData) > 0 {
		c.wsHub.BroadcastEvent(events.DataUpdate, map[string]interface{}{
			"edge_device_id":   device.ID,
			"edge_device_name": device.Name,
			"node_id":          device.NodeID,
			"node_name":        device.Node.Name,
			"channel_id":       evt.ChannelID,
			"data":             dataMap,
			"collected_at":     now.Format(time.RFC3339),
		})
	}

	logger.Debugf("[%s] Parsed %d sensors using %s", evt.DeviceID, len(sensorData), parseMethod)
}

// nodeIngestDropReason 为摄入边界 fail-closed 门给出可排查的丢弃原因。
// 只在丢弃路径调用 (冷路径), 健康摄入路径不增加查询; 正因为默认范围恰好
// 隐藏了要找的那一行, 这里必须 Unscoped。
func (c *SensorParserConsumer) nodeIngestDropReason(nodeID string) string {
	if nodeID == "" {
		return "node_id_empty"
	}
	var node models.Node
	if err := c.db.Unscoped().Select("id", "deleted_at").
		Where("node_id = ?", nodeID).First(&node).Error; err != nil {
		return "node_missing"
	}
	if node.DeletedAt.Valid {
		return "node_soft_deleted"
	}
	// 节点行仍存活却走到丢弃路径: Preload 未回填 (理论上不可达)。仍然 fail-closed,
	// 由运维按该原因继续排查。
	return "node_unresolved"
}
