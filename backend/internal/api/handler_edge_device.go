package api

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
	"ehome/backend/internal/nodemgr"
	"ehome/backend/pkg/logger"
	"ehome/backend/pkg/metrics"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// operationNameRe validates operation names to prevent injection or malformed input.
var operationNameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// executeLimiter limits concurrent /execute read operations to prevent resource exhaustion.
var executeLimiter = make(chan struct{}, 20)

// driver creates ConfigTemplates from the device driver's CommandTemplates (single source of truth).
// Falls back to getTemplateParamsFromDeviceConfig for legacy devices without a driver.
func createTemplatesFromDriver(tx *gorm.DB, ch *models.Channel, dev *models.EdgeDevice) error {
	drv, err := drivers.Get(dev.Type)
	if err != nil {
		// No driver registered — try legacy DeviceConfig-based fallback
		if dev.DeviceConfigID > 0 {
			var dc models.DeviceConfig
			if err2 := tx.First(&dc, dev.DeviceConfigID).Error; err2 == nil {
				writeData, readLength, delayMs := getTemplateParamsFromDeviceConfig(dc, dev.HardwareID)
				if writeData != "" {
					return createSingleTemplate(tx, ch, writeData, readLength, delayMs)
				}
			}
		}
		return nil // no templates to create — that's OK for unregistered device types
	}

	provider, ok := drv.(drivers.CommandTemplateProvider)
	if !ok {
		return nil // driver exists but doesn't provide templates
	}

	created := 0
	for _, cmd := range provider.GetCommandTemplates() {
		if !cmd.Schedulable {
			continue // one-shot triggers don't need ConfigTemplates
		}
		if err := createSingleTemplate(tx, ch, cmd.WriteData, cmd.ReadLength, cmd.DelayMs); err != nil {
			return fmt.Errorf("failed to create template for command %s: %w", cmd.ID, err)
		}
		created++
	}
	logger.Infof("[edge-device-create] Created %d ConfigTemplates for type=%s via driver", created, dev.Type)
	return nil
}

// createSingleTemplate inserts one ConfigTemplate and appends its ID to the channel's template_ids.
func createSingleTemplate(tx *gorm.DB, ch *models.Channel, writeData string, readLength uint32, delayMs uint32) error {
	if writeData == "" {
		return nil
	}
	tmpl := models.ConfigTemplate{
		NodeID:     ch.NodeID,
		WriteData:  writeData,
		ReadLength: readLength,
		DelayMs:    delayMs,
	}
	if err := tx.Create(&tmpl).Error; err != nil {
		return err
	}
	newTmplID := strconv.FormatUint(uint64(tmpl.ID), 10)
	if err := tx.Model(ch).Update("template_ids",
		gorm.Expr("CASE WHEN template_ids = '' OR template_ids IS NULL THEN ? ELSE template_ids || ',' || ? END", newTmplID, newTmplID),
	).Error; err != nil {
		return err
	}
	logger.Infof("[edge-device-create] ConfigTemplate id=%d write_data=%s channel=%d", tmpl.ID, writeData, ch.ID)
	return nil
}

// getTemplateParamsFromDeviceConfig attempts to derive ConfigTemplate parameters
// from a DeviceConfig's Connection JSONB field. Returns ("", 0, 0) if no params can be derived.
func getTemplateParamsFromDeviceConfig(dc models.DeviceConfig, hardwareID string) (string, uint32, uint32) {
	if dc.Connection == nil {
		return "", 0, 0
	}

	var conn map[string]interface{}
	if err := json.Unmarshal(dc.Connection, &conn); err != nil {
		return "", 0, 0
	}

	protocol, _ := conn["protocol"].(string)
	switch strings.ToLower(protocol) {
	case "modbus", "modbus-rtu", "uart":
		// Modbus RTU: build read holding registers command
		slaveAddr := uint8(1)
		if v := parseHardwareIDUint(hardwareID); v > 0 && v <= 247 {
			slaveAddr = uint8(v)
		}
		// Check for custom params in connection.default_params
		startReg := uint16(0)
		regCount := uint16(2)
		if dp, ok := conn["default_params"].(map[string]interface{}); ok {
			if sr, ok := dp["start_register"].(float64); ok {
				startReg = uint16(sr)
			}
			if rc, ok := dp["register_count"].(float64); ok {
				regCount = uint16(rc)
			}
		}
		writeData := fmt.Sprintf("%02X03%04X%04X", slaveAddr, startReg, regCount)
		readLength := uint32(3 + regCount*2 + 2) // addr + func + byte_count + data + CRC
		return writeData, readLength, 100

	case "i2c":
		// I2C: use register address from connection.default_params
		if dp, ok := conn["default_params"].(map[string]interface{}); ok {
			if addr, ok := dp["read_register"].(string); ok {
				return addr, 6, 100
			}
			// Default I2C read for common sensors
			if strings.Contains(strings.ToLower(dc.DeviceType), "bmp") {
				return "F7", 6, 100
			}
		}
		return "", 0, 0

	default:
		return "", 0, 0
	}
}

// parseHardwareIDUint converts a hardware ID string to uint64.
// "0x76" → 118, "5" → 5, "" → 0
// (mirror of nodemgr.parseHardwareID for use in API layer)
func parseHardwareIDUint(s string) uint64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if strings.HasPrefix(strings.ToLower(s), "0x") {
		v, err := strconv.ParseUint(s[2:], 16, 64)
		if err == nil {
			return v
		}
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err == nil {
		return v
	}
	return 0
}

// registerEdgeDeviceRoutes sets up edge-device CRUD routes
func registerEdgeDeviceRoutes(v1 *gin.RouterGroup, db *gorm.DB, nodeMgr *nodemgr.Manager) {
	eventBus := nodeMgr.EventBus()

	// List edge devices (v2.2 path for /devices)
	v1.GET("/edge-devices", func(c *gin.Context) {
		var devices []models.EdgeDevice
		query := db.Preload("Channel").Preload("Node").Preload("DeviceConfig")

		// Apply optional node_id filter (frontend sends as collector_id)
		nodeID := c.Query("node_id")
		if nodeID == "" {
			nodeID = c.Query("collector_id")
		}
		if nodeID != "" {
			query = query.Where("node_id = ?", nodeID)
		}

		// Apply optional device_type & status filters
		if dt := c.Query("device_type"); dt != "" {
			query = query.Where("type = ?", dt)
		}
		if st := c.Query("status"); st != "" {
			query = query.Where("status = ?", st)
		}

		query.Find(&devices)

		// Enrich each device with latest sensor data from unified_data (C1 fix: batch query)
		type lastDataEntry struct {
			DeviceID   uint    `json:"device_id"`
			SensorName string  `json:"sensor_name"`
			Value      float64 `json:"value"`
			Unit       string  `json:"unit"`
		}
		// Collect all device IDs
		deviceIDs := make([]uint, len(devices))
		for i, d := range devices {
			deviceIDs[i] = d.ID
		}
		// Single query: get latest 10 rows per device using DISTINCT ON (PostgreSQL)
		var allEntries []lastDataEntry
		if len(deviceIDs) > 0 {
			db.Table("unified_data ud").
				Select("ud.device_id, ud.sensor_name, ud.value, ud.unit").
				Joins("INNER JOIN (SELECT DISTINCT ON (device_id) device_id, created_at FROM unified_data WHERE device_id IN ? ORDER BY device_id, created_at DESC) latest ON ud.device_id = latest.device_id AND ud.created_at = latest.created_at", deviceIDs).
				Where("ud.device_id IN ?", deviceIDs).
				Find(&allEntries)
		}
		// Group by device ID
		dataByDevice := make(map[uint]map[string]float64, len(devices))
		for _, e := range allEntries {
			if dataByDevice[e.DeviceID] == nil {
				dataByDevice[e.DeviceID] = make(map[string]float64)
			}
			dataByDevice[e.DeviceID][e.SensorName] = e.Value
		}
		for i := range devices {
			if dm, ok := dataByDevice[devices[i].ID]; ok && len(dm) > 0 {
				devices[i].LastData = dm
			}
		}

		Success(c, devices)
	})

	// Get single edge device by id (v2.2 path for /devices/:id)
	v1.GET("/edge-devices/:id", func(c *gin.Context) {
		id := c.Param("id")
		var d models.EdgeDevice
		if err := db.Preload("Channel").Preload("Node").Preload("DeviceConfig").First(&d, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "edge device not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": d})
	})

	// Create edge device (v2.2 path for POST /devices)
	v1.POST("/edge-devices", func(c *gin.Context) {
		// B1 fix: bind to a separate DTO, then construct from allowed fields only
		var dto struct {
			Name           *string `json:"name"`
			Type           *string `json:"type"`
			NodeID         *string `json:"node_id"`
			ChannelID      *uint   `json:"channel_id"`
			Enabled        *bool   `json:"enabled"`
			IntervalMs     *int    `json:"interval_ms"`
			HardwareID     *string `json:"hardware_id"`
			DeviceConfigID *uint   `json:"device_config_id"`
		}
		if err := c.ShouldBindJSON(&dto); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if dto.Name == nil || dto.Type == nil || dto.NodeID == nil || dto.ChannelID == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "name, type, node_id, and channel_id are required"})
			return
		}
		var bindingChannel models.Channel
		if err := db.Where("id = ? AND node_id = ?", *dto.ChannelID, *dto.NodeID).First(&bindingChannel).Error; err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "channel does not belong to node"})
			return
		}
		if err := validateTransportChannel(&bindingChannel); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		dev := models.EdgeDevice{Name: *dto.Name, Type: *dto.Type, NodeID: *dto.NodeID, ChannelID: *dto.ChannelID}
		if dto.Enabled != nil {
			dev.Enabled = *dto.Enabled
		}
		if dto.IntervalMs != nil {
			dev.IntervalMs = *dto.IntervalMs
		}
		if dto.HardwareID != nil {
			dev.HardwareID = *dto.HardwareID
		}
		if dto.DeviceConfigID != nil {
			dev.DeviceConfigID = *dto.DeviceConfigID
			var dc models.DeviceConfig
			if err := db.First(&dc, *dto.DeviceConfigID).Error; err == nil && dc.DeviceType != "" {
				dev.Type = dc.DeviceType
			}
		}
		if err := db.Transaction(func(tx *gorm.DB) error {
			// Step 1: Create EdgeDevice (inside transaction)
			if err := tx.Create(&dev).Error; err != nil {
				return err
			}

			// Step 2: Create ConfigTemplates from driver's CommandTemplates (single source of truth)
			var ch models.Channel
			if err := tx.First(&ch, dev.ChannelID).Error; err == nil {
				if err := createTemplatesFromDriver(tx, &ch, &dev); err != nil {
					logger.Warnf("[edge-device-create] Failed to create ConfigTemplates: %v", err)
					return err
				}
			}

			return nil // commit transaction
		}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		// EmitConfigChange (outside transaction — event emission should not block rollback)
		var chForEvent models.Channel
		if db.First(&chForEvent, dev.ChannelID).Error == nil {
			nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeEdgeDevice, nodemgr.CfgActionCreate, chForEvent.NodeID, fmt.Sprint(dev.ID))
		}

		// Reload with associations for response
		db.Preload("Channel").Preload("Node").Preload("DeviceConfig").First(&dev, dev.ID)
		c.JSON(http.StatusCreated, dev)
	})

	// Update edge device (v2.2 path for PUT /devices/:id)
	v1.PUT("/edge-devices/:id", func(c *gin.Context) {
		id := c.Param("id")
		var d models.EdgeDevice
		if err := db.First(&d, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "edge device not found"})
			return
		}
		// B1 fix: bind to a separate DTO, then apply only allowed fields via Updates
		var dto struct {
			Name           *string `json:"name"`
			Type           *string `json:"type"`
			Enabled        *bool   `json:"enabled"`
			IntervalMs     *int    `json:"interval_ms"`
			HardwareID     *string `json:"hardware_id"`
			DeviceConfigID *uint   `json:"device_config_id"`
			ChannelID      *uint   `json:"channel_id"`
			NodeID         *string `json:"node_id"`
			Status         *string `json:"status"`
		}
		if err := c.ShouldBindJSON(&dto); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": err.Error()})
			return
		}
		updates := map[string]interface{}{}
		if dto.Name != nil {
			updates["name"] = *dto.Name
		}
		if dto.Type != nil {
			updates["type"] = *dto.Type
		}
		if dto.Enabled != nil {
			updates["enabled"] = *dto.Enabled
		}
		if dto.IntervalMs != nil {
			updates["interval_ms"] = *dto.IntervalMs
		}
		if dto.HardwareID != nil {
			updates["hardware_id"] = *dto.HardwareID
		}
		if dto.DeviceConfigID != nil {
			updates["device_config_id"] = *dto.DeviceConfigID
			// P2-7: Sync Type from DeviceConfig when DeviceConfigID changes
			var dc models.DeviceConfig
			if err := db.First(&dc, *dto.DeviceConfigID).Error; err == nil && dc.DeviceType != "" {
				updates["type"] = dc.DeviceType
			}
		}
		if dto.ChannelID != nil {
			targetNodeID := d.NodeID
			if dto.NodeID != nil {
				targetNodeID = *dto.NodeID
			}
			var bindingChannel models.Channel
			if err := db.Where("id = ? AND node_id = ?", *dto.ChannelID, targetNodeID).First(&bindingChannel).Error; err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "channel does not belong to node"})
				return
			}
			if err := validateTransportChannel(&bindingChannel); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": err.Error()})
				return
			}
			updates["channel_id"] = *dto.ChannelID
		}
		if dto.NodeID != nil {
			targetChannelID := d.ChannelID
			if dto.ChannelID != nil {
				targetChannelID = *dto.ChannelID
			}
			if _, err := loadTransportChannel(db, targetChannelID, *dto.NodeID); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": err.Error()})
				return
			}
			updates["node_id"] = *dto.NodeID
		}
		if dto.Status != nil {
			updates["status"] = *dto.Status
		}
		if len(updates) > 0 {
			db.Model(&d).Updates(updates)
		}
		// Reload to get updated fields (P1 fix: Preload associations so PUT response includes Channel/Node/DeviceConfig)
		db.Preload("Channel").Preload("Node").Preload("DeviceConfig").First(&d, id)
		// EmitConfigChange: find the node via channel
		var ch models.Channel
		if db.First(&ch, d.ChannelID).Error == nil {
			nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeEdgeDevice, nodemgr.CfgActionUpdate, ch.NodeID, fmt.Sprint(d.ID))
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": d})
	})

	// Init edge device (trigger InitDevice via deviceinit.Orchestrator)
	v1.POST("/edge-devices/:id/init", func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))

		// Load the edge device to get device type, channel, and node info
		var dev models.EdgeDevice
		if err := db.First(&dev, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "edge device not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}
		if !dev.Enabled {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "edge device is disabled"})
			return
		}
		if _, err := loadTransportChannel(db, dev.ChannelID, dev.NodeID); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": err.Error()})
			return
		}

		// Resolve the MQTT device ID from the associated Node
		node, err := findNodeByID(db, dev.NodeID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "associated node not found"})
			return
		}
		deviceID := node.NodeID

		// Use the deviceinit Orchestrator to trigger init
		orchestrator := nodeMgr.DeviceInit()
		if orchestrator == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "device init orchestrator not available"})
			return
		}

		// Trigger async initialization
		go orchestrator.InitDevice(deviceID, uint32(dev.ChannelID), dev.Type)

		// Update init state on the device record
		db.Model(&dev).Updates(map[string]interface{}{
			"init_state":       "running",
			"init_last_step":   0,
			"init_total_steps": len(orchestrator.GetInitSequence(dev.Type)),
		})

		c.JSON(http.StatusOK, gin.H{"code": 200, "data": gin.H{
			"id": id, "status": "running", "message": "init triggered",
			"device_type": dev.Type, "device_id": deviceID,
		}})
	})

	// Delete edge device (v2.2 path for DELETE /devices/:id)
	v1.DELETE("/edge-devices/:id", func(c *gin.Context) {
		id := c.Param("id")
		var d models.EdgeDevice
		if err := db.First(&d, id).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}
		// Find node before deletion for event emission
		var ch models.Channel
		hasNode := db.First(&ch, d.ChannelID).Error == nil
		if err := db.Delete(&models.EdgeDevice{}, id).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}
		// EmitConfigChange
		if hasNode {
			nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeEdgeDevice, nodemgr.CfgActionDelete, ch.NodeID, fmt.Sprint(d.ID))
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "deleted", "data": gin.H{"deleted": id}})
	})

	// Edge device routes group for :id sub-resources
	e := v1.Group("/edge-devices")

	// GET /api/v1/edge-devices/:id/latest-data
	e.GET("/:id/latest-data", func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		// 从 device_data 表查最新一条
		var data models.DeviceData
		db.Where("device_id = ?", id).Order("created_at DESC").First(&data)
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": data})
	})

	// GET /api/v1/edge-devices/:id/data
	e.GET("/:id/data", func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		from := c.Query("start_time")
		to := c.Query("end_time")
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))
		var data []models.DeviceData
		q := db.Where("device_id = ?", id)
		if from != "" {
			q = q.Where("created_at >= ?", from)
		}
		if to != "" {
			q = q.Where("created_at <= ?", to)
		}
		var total int64
		q.Model(&models.DeviceData{}).Count(&total)
		q.Order("created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&data)
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": gin.H{"items": data, "total": total}})
	})

	// POST /api/v1/edge-devices/:id/operations
	e.POST("/:id/operations", func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		var req struct {
			Operation string                 `json:"operation"`
			Params    map[string]interface{} `json:"params"`
		}
		c.ShouldBindJSON(&req)
		// NOTE: requires MQTT device gateway integration
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": gin.H{
			"device_id": id, "operation": req.Operation, "status": "pending",
		}})
	})

	// GET /api/v1/edge-devices/:id/operations/history
	e.GET("/:id/operations/history", func(c *gin.Context) {
		_, _ = strconv.Atoi(c.Param("id"))
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
		_ = limit // reserved for future MQTT event pipeline integration
		// NOTE: operation_logs table populated by MQTT event pipeline
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": []interface{}{}})
	})

	// POST /api/v1/edge-devices/:id/execute — execute a device operation
	// Uses DeviceConfig.Operations JSONB for operation definitions with template engine + CRC
	e.POST("/:id/execute", func(c *gin.Context) {
		id := c.Param("id")

		var req struct {
			Operation string                 `json:"operation" binding:"required"`
			Params    map[string]interface{} `json:"params"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, "operation is required")
			return
		}

		// Validate operation name format
		if !operationNameRe.MatchString(req.Operation) {
			Error(c, http.StatusBadRequest, "invalid operation name format")
			return
		}

		// 1. Look up EdgeDevice
		var edge models.EdgeDevice
		if err := db.Preload("DeviceConfig").First(&edge, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				Error(c, http.StatusNotFound, "edge device not found")
				return
			}
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		if !edge.Enabled {
			Error(c, http.StatusBadRequest, "edge device is disabled")
			return
		}
		if _, err := loadTransportChannel(db, edge.ChannelID, edge.NodeID); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}

		// 2. Look up DeviceConfig.Operations
		if edge.DeviceConfig.ID == 0 {
			Error(c, http.StatusBadRequest, "device config not found for this edge device")
			return
		}

		var operations map[string]OperationConfig
		if edge.DeviceConfig.Operations != nil {
			if err := json.Unmarshal(edge.DeviceConfig.Operations, &operations); err != nil {
				Error(c, http.StatusInternalServerError, "failed to parse device config operations")
				return
			}
		}

		opConfig, ok := operations[req.Operation]
		if !ok {
			Error(c, http.StatusBadRequest, fmt.Sprintf("operation %q not found in device config", req.Operation))
			return
		}

		// Validate operation type
		if opConfig.Type != "read" && opConfig.Type != "write" {
			Error(c, http.StatusBadRequest, fmt.Sprintf("invalid operation type %q, must be 'read' or 'write'", opConfig.Type))
			return
		}

		opType := opConfig.Type

		// 8.1: Track concurrent executions
		metrics.ExecuteConcurrentActive.Inc()
		metrics.ExecuteRequestTotal.WithLabelValues(opType, "started").Inc()
		defer metrics.ExecuteConcurrentActive.Dec()

		// 3. Resolve current address from EdgeDevice
		addr := parseHardwareIDUint(edge.HardwareID)

		// Also try to get address from DeviceConfig.Connection
		if addr == 0 && edge.DeviceConfig.Connection != nil {
			var conn map[string]interface{}
			if err := json.Unmarshal(edge.DeviceConfig.Connection, &conn); err == nil {
				if dp, ok := conn["default_params"].(map[string]interface{}); ok {
					if a, ok := dp["address"]; ok {
						if v, err := toUint64(a); err == nil {
							addr = v
						}
					}
				}
			}
		}

		// Default to Modbus slave address 1 if not resolved from hardware_id or connection
		if addr == 0 {
			addr = 1
		}

		// 4. Render command template
		vars := TemplateVars{
			Addr:   addr,
			Params: req.Params,
		}
		writeData, err := RenderCommandTemplate(opConfig.CommandTemplate, vars)
		if err != nil {
			Error(c, http.StatusBadRequest, "failed to render command template: "+err.Error())
			return
		}

		// Resolve node for MQTT routing
		var node models.Node
		if err := db.Where("node_id = ?", edge.NodeID).First(&node).Error; err != nil {
			Error(c, http.StatusNotFound, "associated node not found")
			return
		}
		deviceID := node.NodeID

		logger.Infof("[execute] edge=%d op=%s type=%s addr=%d data_hex=%x", edge.ID, req.Operation, opConfig.Type, addr, writeData)

		// 5. Execute based on type
		switch opConfig.Type {
		case "write":
			// Fire-and-forget: send via nodeMgr
			if err := nodeMgr.SendWriteCommand(deviceID, uint32(edge.ChannelID), writeData, 0); err != nil {
				metrics.ExecuteRequestTotal.WithLabelValues(opType, "error").Inc()
				Error(c, http.StatusInternalServerError, "failed to send command: "+err.Error())
				return
			}

			// Execute post_action
			if err := executePostAction(db, edge, opConfig.PostAction, req.Params); err != nil {
				logger.Warnf("[execute] post_action failed for edge=%d op=%s: %v", edge.ID, req.Operation, err)
			}

			// Trigger config sync
			nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeEdgeDevice, nodemgr.CfgActionUpdate, edge.NodeID, fmt.Sprint(edge.ID))

			// P2-4: Verify write by executing a read operation
			var verifyResult interface{}
			if opConfig.VerifyOperation != "" {
				verifyOpConfig, ok := operations[opConfig.VerifyOperation]
				if !ok {
					logger.Warnf("[execute] verify_operation %q not found in device config", opConfig.VerifyOperation)
				} else if verifyOpConfig.Type != "read" {
					logger.Warnf("[execute] verify_operation %q is not a read operation", opConfig.VerifyOperation)
				} else {
					// Render verify command
					verifyVars := TemplateVars{Addr: addr, Params: req.Params}
					verifyData, err := RenderCommandTemplate(verifyOpConfig.CommandTemplate, verifyVars)
					if err != nil {
						logger.Warnf("[execute] verify command render failed: %v", err)
					} else {
						verifyReadSize := verifyOpConfig.ReadSize
						if verifyReadSize == 0 {
							verifyReadSize = 9
						}
						verifyTimeout := 10 * time.Second
						if verifyOpConfig.TimeoutMs > 0 {
							configured := time.Duration(verifyOpConfig.TimeoutMs) * time.Millisecond
							if configured <= 30*time.Second {
								verifyTimeout = configured
							}
						}

						ctx2, cancel2 := context.WithTimeout(c.Request.Context(), verifyTimeout)
						defer cancel2()

						resp2, err := nodeMgr.PendingWrite().SendWriteCommand(ctx2, deviceID, uint32(edge.ChannelID), verifyData, verifyReadSize, verifyTimeout)
						if err != nil {
							logger.Warnf("[execute] verify operation failed: %v", err)
						} else if !resp2.Success {
							logger.Warnf("[execute] verify operation returned error: code=%d msg=%s", resp2.ErrorCode, resp2.ErrorMsg)
						} else if verifyOpConfig.ResponseParser != "" && len(resp2.RawData) > 0 {
							value, err := ParseModbusResponse(resp2.RawData, verifyOpConfig.ResponseParser)
							if err != nil {
								logger.Warnf("[execute] verify parse failed: %v", err)
							} else {
								verifyResult = value
							}
						}
					}
				}
			}

			result := gin.H{
				"status":    "sent",
				"operation": req.Operation,
				"data_hex":  fmt.Sprintf("%x", writeData),
			}
			if verifyResult != nil {
				result["verify_value"] = verifyResult
				result["verify_operation"] = opConfig.VerifyOperation
			}
			metrics.ExecuteRequestTotal.WithLabelValues(opType, "success").Inc()
			Success(c, result)

		case "read":
			// Concurrency limit for read operations
			select {
			case executeLimiter <- struct{}{}:
				defer func() { <-executeLimiter }()
			default:
				// 8.1: Track rate limit rejections
				metrics.ExecuteRateLimitRejected.Inc()
				metrics.ExecuteRequestTotal.WithLabelValues(opType, "error").Inc()
				Error(c, http.StatusServiceUnavailable, "too many concurrent operations")
				return
			}

			// P1-3: Device-level mutex to prevent concurrent /execute on same device+channel
			deviceKey := fmt.Sprintf("%s:%d", deviceID, edge.ChannelID)
			deviceLocks.lock(deviceKey)
			defer deviceLocks.unlock(deviceKey)

			// Read operation: use pendingwrite to wait for response
			// Timeout with 30s upper bound to prevent runaway waits
			timeout := 10 * time.Second
			if opConfig.TimeoutMs > 0 {
				configured := time.Duration(opConfig.TimeoutMs) * time.Millisecond
				if configured <= 30*time.Second {
					timeout = configured
				}
			}
			readSize := opConfig.ReadSize
			if readSize == 0 {
				readSize = 9 // default Modbus response size
			}

			logger.Infof("[execute] read op=%s deviceID=%s ch=%d readSize=%d timeout=%v dataHex=%x",
				req.Operation, deviceID, edge.ChannelID, readSize, timeout, writeData)

			// 8.1: Track read operation latency
			readStart := time.Now()

			ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
			defer cancel()
			resp, err := nodeMgr.PendingWrite().SendWriteCommand(ctx, deviceID, uint32(edge.ChannelID), writeData, readSize, timeout)
			if err != nil {
				metrics.ExecuteRequestTotal.WithLabelValues(opType, "error").Inc()
				if errors.Is(err, context.DeadlineExceeded) {
					Error(c, http.StatusGatewayTimeout, "device did not respond")
				} else if errors.Is(err, context.Canceled) {
					Error(c, 499, "client disconnected")
				} else {
					Error(c, http.StatusInternalServerError, "command failed: "+err.Error())
				}
				return
			}
			if !resp.Success {
				metrics.ExecuteRequestTotal.WithLabelValues(opType, "error").Inc()
				Error(c, http.StatusInternalServerError, fmt.Sprintf("device returned error: code=%d msg=%s", resp.ErrorCode, resp.ErrorMsg))
				return
			}

			// 8.1: Observe read duration
			metrics.ExecuteReadDuration.Observe(time.Since(readStart).Seconds())

			// Parse response if parser is defined
			result := gin.H{
				"status":    "ok",
				"operation": req.Operation,
				"data_hex":  fmt.Sprintf("%x", writeData),
			}

			if opConfig.ResponseParser != "" && len(resp.RawData) > 0 {
				value, err := ParseModbusResponse(resp.RawData, opConfig.ResponseParser)
				if err != nil {
					logger.Warnf("[execute] response parse failed for edge=%d op=%s: %v", edge.ID, req.Operation, err)
					result["raw_hex"] = fmt.Sprintf("%x", resp.RawData)
					result["parse_error"] = err.Error()
				} else {
					result["value"] = value
					// Use ResponseUnit if Unit is empty (DB configs use "response_unit" key)
					unit := opConfig.Unit
					if unit == "" {
						unit = opConfig.ResponseUnit
					}
					if unit != "" {
						result["unit"] = unit
					}
				}
			} else if len(resp.RawData) > 0 {
				result["raw_hex"] = fmt.Sprintf("%x", resp.RawData)
			}

			// Execute post_action
			if err := executePostAction(db, edge, opConfig.PostAction, req.Params); err != nil {
				logger.Warnf("[execute] post_action failed for edge=%d op=%s: %v", edge.ID, req.Operation, err)
			}

			metrics.ExecuteRequestTotal.WithLabelValues(opType, "success").Inc()
			Success(c, result)

		default:
			metrics.ExecuteRequestTotal.WithLabelValues(opType, "error").Inc()
			Error(c, http.StatusBadRequest, fmt.Sprintf("unsupported operation type: %s", opConfig.Type))
		}
	})

	// DEPRECATED: Use POST /:id/execute with operation "change_address" instead.
	// This endpoint is kept for backward compatibility and will be removed in a future version.
	// POST /api/v1/edge-devices/:id/change-address — modify edge device address
	e.POST("/:id/change-address", func(c *gin.Context) {
		id := c.Param("id")

		var req struct {
			NewAddress int    `json:"new_address" binding:"required"`
			Command    string `json:"command,omitempty"` // optional custom Modbus command (hex)
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, "new_address is required")
			return
		}
		if req.NewAddress < 1 || req.NewAddress > 247 {
			Error(c, http.StatusBadRequest, "new_address must be between 1 and 247")
			return
		}

		// Look up edge device
		var edge models.EdgeDevice
		if err := db.First(&edge, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				Error(c, http.StatusNotFound, "edge device not found")
				return
			}
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}

		// Look up channel and node for MQTT routing
		var ch models.Channel
		if err := db.First(&ch, edge.ChannelID).Error; err != nil {
			Error(c, http.StatusNotFound, "associated channel not found")
			return
		}
		if _, err := loadTransportChannel(db, edge.ChannelID, edge.NodeID); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		var node models.Node
		if err := db.Where("node_id = ?", edge.NodeID).First(&node).Error; err != nil {
			Error(c, http.StatusNotFound, "associated node not found")
			return
		}

		// Build WriteCommand data
		var writeData []byte
		newAddr := uint8(req.NewAddress)

		if req.Command != "" {
			// User-provided custom command (hex) — escape hatch for one-off cases
			var err error
			writeData, err = hex.DecodeString(req.Command)
			if err != nil {
				Error(c, http.StatusBadRequest, "invalid hex command")
				return
			}

			// P1-2: Modbus command security validation

			// 1. Length check
			if len(writeData) < 4 || len(writeData) > 64 {
				Error(c, http.StatusBadRequest, "command must be 4-64 bytes")
				return
			}

			// 2. Slave address check — prevent broadcast attack
			slaveAddr := writeData[0]
			if slaveAddr == 0x00 {
				Error(c, http.StatusBadRequest, "broadcast address 0x00 not allowed")
				return
			}
			if slaveAddr > 247 {
				Error(c, http.StatusBadRequest, "Modbus address must be 1-247")
				return
			}

			// 3. Target address must match current device (prevent attacking other devices)
			oldAddr := uint8(0)
			if v := parseHardwareIDUint(edge.HardwareID); v > 0 && v <= 247 {
				oldAddr = uint8(v)
			}
			if oldAddr > 0 && slaveAddr != oldAddr {
				Error(c, http.StatusBadRequest,
					fmt.Sprintf("command must target device address 0x%02X", oldAddr))
				return
			}

			// 4. CRC check
			if len(writeData) >= 4 {
				expectedCRC := ModbusCRC16(writeData[:len(writeData)-2])
				actualCRC := uint16(writeData[len(writeData)-2]) | uint16(writeData[len(writeData)-1])<<8
				if expectedCRC != actualCRC {
					Error(c, http.StatusBadRequest, "invalid Modbus CRC")
					return
				}
			}

			// 5. Function code whitelist (replace funcCode >= 0x05 ban)
			// Allow: FC01-04 (read) + FC05/06 (write single register/coil, needed for change-address)
			// Deny: FC0F/10 (write multiple) + FC2x/3x/4x/5x/6x/7x (diagnostics/file/etc)
			funcCode := writeData[1] & 0x7F
			switch funcCode {
			case 0x01, 0x02, 0x03, 0x04:
				// Read operations — allowed
			case 0x05, 0x06:
				// Write single register/coil — allowed (change-address uses FC06)
				// Additional check: write target register should be in allowed range
				if len(writeData) >= 6 {
					regAddr := uint16(writeData[2])<<8 | uint16(writeData[3])
					if regAddr > 0xFF {
						Error(c, http.StatusForbidden,
							fmt.Sprintf("write to register 0x%04X not allowed (max 0x00FF)", regAddr))
						return
					}
				}
			default:
				Error(c, http.StatusForbidden,
					fmt.Sprintf("function code 0x%02X not allowed (allowed: 01-06)", funcCode))
				return
			}
		} else {
			// Look up DeviceConfig for change_address_command template
			var dc models.DeviceConfig
			if err := db.First(&dc, edge.DeviceConfigID).Error; err != nil {
				Error(c, http.StatusNotFound, "device config not found")
				return
			}

			// Parse init_flow or a dedicated field for change_address_command
			// v2.3: change_address_command is stored in DeviceConfig.Config JSON
			// or a dedicated JSONB field. For now, check Config for the template.
			type AddrCmdCfg struct {
				ChangeAddressCommand *struct {
					TemplateHex string `json:"template_hex"`
				} `json:"change_address_command"`
			}
			var acfg AddrCmdCfg
			if dc.Config != nil {
				if err := json.Unmarshal(dc.Config, &acfg); err == nil && acfg.ChangeAddressCommand != nil && acfg.ChangeAddressCommand.TemplateHex != "" {
					// Use DeviceConfig template with placeholder replacement
					tmpl := acfg.ChangeAddressCommand.TemplateHex
					oldAddr := uint8(0)
					if v := parseHardwareIDUint(edge.HardwareID); v > 0 && v <= 247 {
						oldAddr = uint8(v)
					}
					// Replace placeholders
					replacer := strings.NewReplacer(
						"{addr}", fmt.Sprintf("%02X", newAddr),
						"{addr_hi}", fmt.Sprintf("%02X", uint16(newAddr)>>8),
						"{addr_lo}", fmt.Sprintf("%02X", newAddr&0xFF),
						"{old_addr}", fmt.Sprintf("%02X", oldAddr),
					)
					tmpl = replacer.Replace(tmpl)
					// Remove spaces and parse hex
					hexStr := strings.ReplaceAll(tmpl, " ", "")
					var err error
					writeData, err = hex.DecodeString(hexStr)
					if err != nil {
						Error(c, http.StatusBadRequest, "invalid change_address_command template: "+err.Error())
						return
					}
					// Prepend old slave address if not already in template
					if oldAddr > 0 && (len(writeData) == 0 || writeData[0] == 0 || writeData[0] == newAddr) {
						writeData = append([]byte{oldAddr}, writeData...)
					}
					logger.Infof("[change-address] Using DeviceConfig template for edge=%d, old=%d→new=%d, hex=%x",
						edge.ID, oldAddr, newAddr, writeData)
				} else {
					Error(c, http.StatusBadRequest, "该设备型号不支持地址修改（DeviceConfig 未定义 change_address_command 模板）")
					return
				}
			} else {
				Error(c, http.StatusBadRequest, "该设备型号不支持地址修改（DeviceConfig 未定义 change_address_command 模板）")
				return
			}
		}

		// Send WriteCommand via node manager
		deviceID := node.NodeID
		if err := nodeMgr.SendWriteCommand(deviceID, uint32(edge.ChannelID), writeData, 0); err != nil {
			Error(c, http.StatusInternalServerError, "failed to send address change command: "+err.Error())
			return
		}

		// Update DB immediately (simplified: don't wait for ACK)
		edge.HardwareID = strconv.Itoa(req.NewAddress)
		db.Model(&edge).Update("hardware_id", edge.HardwareID)

		// Trigger config sync so device gets the updated address
		nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeEdgeDevice, nodemgr.CfgActionUpdate, ch.NodeID, fmt.Sprint(edge.ID))

		c.JSON(http.StatusOK, gin.H{
			"code":    200,
			"message": "ok",
			"data": gin.H{
				"id":          edge.ID,
				"new_address": req.NewAddress,
				"message":     "地址修改命令已发送",
			},
		})
	})
}

// executePostAction handles post-operation side effects like updating device address or baud rate.
func executePostAction(db *gorm.DB, edge models.EdgeDevice, postAction string, params map[string]interface{}) error {
	switch postAction {
	case "update_connection_address":
		// Update EdgeDevice.HardwareID and DeviceConfig.Connection.address atomically
		newAddr, ok := params["new_addr"]
		if !ok {
			return fmt.Errorf("new_addr param required for update_connection_address")
		}
		newAddrStr := fmt.Sprintf("%v", newAddr)

		err := db.Transaction(func(tx *gorm.DB) error {
			// Update EdgeDevice.HardwareID
			if err := tx.Model(&models.EdgeDevice{}).Where("id = ?", edge.ID).Update("hardware_id", newAddrStr).Error; err != nil {
				return fmt.Errorf("failed to update hardware_id: %w", err)
			}

			// Update DeviceConfig.Connection.address
			var dc models.DeviceConfig
			if err := tx.First(&dc, edge.DeviceConfigID).Error; err != nil {
				return fmt.Errorf("failed to load device config: %w", err)
			}
			var conn map[string]interface{}
			if dc.Connection != nil {
				if err := json.Unmarshal(dc.Connection, &conn); err != nil {
					return fmt.Errorf("failed to parse connection JSON: %w", err)
				}
			} else {
				conn = make(map[string]interface{})
			}
			// Update address in default_params
			dp, ok := conn["default_params"].(map[string]interface{})
			if !ok {
				dp = make(map[string]interface{})
			}
			dp["address"] = newAddr
			conn["default_params"] = dp
			connJSON, _ := json.Marshal(conn)
			if err := tx.Model(&models.DeviceConfig{}).Where("id = ?", dc.ID).Update("connection", connJSON).Error; err != nil {
				return fmt.Errorf("failed to update connection: %w", err)
			}
			return nil
		})
		if err != nil {
			return err
		}

		logger.Infof("[execute] post_action update_connection_address: edge=%d new_addr=%v", edge.ID, newAddr)

	case "update_connection_baud":
		// Update DeviceConfig.Connection.baud_rate
		newBaud, ok := params["new_baud"]
		if !ok {
			return fmt.Errorf("new_baud param required for update_connection_baud")
		}

		var dc models.DeviceConfig
		if err := db.First(&dc, edge.DeviceConfigID).Error; err != nil {
			return fmt.Errorf("failed to load device config: %w", err)
		}
		var conn map[string]interface{}
		if dc.Connection != nil {
			if err := json.Unmarshal(dc.Connection, &conn); err != nil {
				return fmt.Errorf("failed to parse connection JSON: %w", err)
			}
		} else {
			conn = make(map[string]interface{})
		}
		dp, ok := conn["default_params"].(map[string]interface{})
		if !ok {
			dp = make(map[string]interface{})
		}
		dp["baud_rate"] = newBaud
		conn["default_params"] = dp
		connJSON, _ := json.Marshal(conn)
		if err := db.Model(&dc).Update("connection", connJSON).Error; err != nil {
			return fmt.Errorf("failed to update connection: %w", err)
		}

		logger.Infof("[execute] post_action update_connection_baud: edge=%d new_baud=%v", edge.ID, newBaud)

	case "":
		// No post action

	default:
		logger.Warnf("[execute] unknown post_action: %s", postAction)
	}

	return nil
}
