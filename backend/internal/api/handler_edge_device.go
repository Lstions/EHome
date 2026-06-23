package api

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ehome/backend/internal/nodemgr"
	"ehome/backend/internal/models"
	"ehome/backend/pkg/logger"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// getDefaultTemplateParams returns default ConfigTemplate parameters (write_data, read_length, delay_ms)
// for a given device type. The hardwareID is used to build Modbus slave address in the command.
// Returns ("", 0, 0) if no defaults are known for the device type.
func getDefaultTemplateParams(deviceType string, hardwareID string) (string, uint32, uint32) {
	// Normalize device type for matching
	dt := strings.ToLower(strings.TrimSpace(deviceType))

	// Resolve Modbus slave address from hardware_id (default 1 if not set)
	slaveAddr := uint8(1)
	if v := parseHardwareIDUint(hardwareID); v > 0 && v <= 247 {
		slaveAddr = uint8(v)
	}

	switch dt {
	// Modbus RTU devices — SN-3000 wind direction sensor
	case "sn3000", "wind_direction", "wind":
		// Modbus Read Holding Registers: addr=slaveAddr, func=0x03, start=0x0000, count=0x0002
		// Command: [slaveAddr][03][00][00][00][02][CRC16]
		writeData := fmt.Sprintf("%02X0300000002", slaveAddr)
		// Response: [addr][func][byte_count=4][reg0_hi][reg0_lo][reg1_hi][reg1_lo][crc_lo][crc_hi] = 9 bytes
		return writeData, 9, 100

	// Modbus RTU devices — LK-TH01 temperature/humidity sensor
	case "lk_th01", "lk-th01", "temperature_humidity", "temp_humidity":
		// Modbus Read Holding Registers: addr=slaveAddr, func=0x03, start=0x0000, count=0x0002
		writeData := fmt.Sprintf("%02X0300000002", slaveAddr)
		// Response: [addr][func][byte_count=4][temp_hi][temp_lo][hum_hi][hum_lo][crc_lo][crc_hi] = 9 bytes
		return writeData, 9, 100

	// I2C devices — BMP280 temperature/pressure sensor
	case "bmp280", "bme280":
		// I2C read: write register address 0xF7 (pressure_msb), read 6 bytes of data
		// For I2C, write_data is just the register address byte(s)
		return "F7", 6, 100

	default:
		// For unknown device types, try to generate a generic Modbus read command
		// if the hardware_id looks like a Modbus address
		if slaveAddr > 1 || hardwareID != "" {
			// Generic Modbus Read Holding Registers: start=0x0000, count=0x0001
			writeData := fmt.Sprintf("%02X0300000001", slaveAddr)
			return writeData, 7, 100
		}
		return "", 0, 0
	}
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
		db.Preload("Channel").Preload("Node").Preload("DeviceConfig").Find(&devices)

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
	v1.POST("/edge-devices", RequireRole("admin"), func(c *gin.Context) {
		// B1 fix: bind to a separate DTO, then construct from allowed fields only
		var dto struct {
			Name          *string `json:"name"`
			Type          *string `json:"type"`
			NodeID        *string `json:"node_id"`
			ChannelID     *uint   `json:"channel_id"`
			Enabled       *bool   `json:"enabled"`
			IntervalMs    *int    `json:"interval_ms"`
			HardwareID    *string `json:"hardware_id"`
			DeviceConfigID *uint  `json:"device_config_id"`
		}
		if err := c.ShouldBindJSON(&dto); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if dto.Name == nil || dto.Type == nil || dto.NodeID == nil || dto.ChannelID == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "name, type, node_id, and channel_id are required"})
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
		}
		if err := db.Transaction(func(tx *gorm.DB) error {
			// Step 1: Create EdgeDevice (inside transaction)
			if err := tx.Create(&dev).Error; err != nil {
				return err
			}

			// Step 2: Cascade — create ConfigTemplate and link to channel's template_ids
			var ch models.Channel
			if err := tx.First(&ch, dev.ChannelID).Error; err == nil {
				// Determine default Modbus/I2C command parameters based on device type
				writeData, readLength, delayMs := getDefaultTemplateParams(dev.Type, dev.HardwareID)

				// If DeviceConfigID is set, try to derive params from DeviceConfig.Connection
				if writeData == "" && dev.DeviceConfigID > 0 {
					var dc models.DeviceConfig
					if err := tx.First(&dc, dev.DeviceConfigID).Error; err == nil {
						writeData, readLength, delayMs = getTemplateParamsFromDeviceConfig(dc, dev.HardwareID)
					}
				}

				// Only create ConfigTemplate if we have a valid write_data
				if writeData != "" {
					tmpl := models.ConfigTemplate{
						NodeID:     ch.NodeID,
						WriteData:  writeData,
						ReadLength: readLength,
						DelayMs:    delayMs,
					}
					if err := tx.Create(&tmpl).Error; err != nil {
						logger.Warnf("[edge-device-create] Failed to create ConfigTemplate for edge_device id=%d: %v", dev.ID, err)
						return err // rollback entire transaction
					}
					// Step 3: Append new template ID to channel's template_ids atomically (SQL-level)
					newTmplID := strconv.FormatUint(uint64(tmpl.ID), 10)
					if err := tx.Model(&ch).Update("template_ids",
						gorm.Expr("CASE WHEN template_ids = '' OR template_ids IS NULL THEN ? ELSE template_ids || ',' || ? END", newTmplID, newTmplID),
					).Error; err != nil {
						logger.Warnf("[edge-device-create] Failed to update channel template_ids for edge_device id=%d: %v", dev.ID, err)
						return err // rollback entire transaction
					}
					logger.Infof("[edge-device-create] Created ConfigTemplate id=%d for edge_device id=%d type=%s, channel %d (appended template_id=%s)",
						tmpl.ID, dev.ID, dev.Type, ch.ID, newTmplID)
				} else {
					logger.Infof("[edge-device-create] No default template params for type=%s, skipping ConfigTemplate creation", dev.Type)
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
	v1.PUT("/edge-devices/:id", RequireRole("admin"), func(c *gin.Context) {
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
		}
		if dto.ChannelID != nil {
			updates["channel_id"] = *dto.ChannelID
		}
		if dto.NodeID != nil {
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
	v1.POST("/edge-devices/:id/init", RequireRole("admin"), func(c *gin.Context) {
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
	e.POST("/:id/execute", RequireRole("admin"), func(c *gin.Context) {
		id := c.Param("id")

		var req struct {
			Operation string                 `json:"operation" binding:"required"`
			Params    map[string]interface{} `json:"params"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, "operation is required")
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
				Error(c, http.StatusInternalServerError, "failed to send command: "+err.Error())
				return
			}

			// Execute post_action
			if err := executePostAction(db, edge, opConfig.PostAction, req.Params); err != nil {
				logger.Warnf("[execute] post_action failed for edge=%d op=%s: %v", edge.ID, req.Operation, err)
			}

			// Trigger config sync
			nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeEdgeDevice, nodemgr.CfgActionUpdate, edge.NodeID, fmt.Sprint(edge.ID))

			Success(c, gin.H{
				"status":    "sent",
				"operation": req.Operation,
				"data_hex":  fmt.Sprintf("%x", writeData),
			})

		case "read":
			// Read operation: use pendingwrite to wait for response
			timeout := time.Duration(10000) * time.Millisecond
			if opConfig.TimeoutMs > 0 {
				timeout = time.Duration(opConfig.TimeoutMs) * time.Millisecond
			}
			readSize := opConfig.ReadSize
			if readSize == 0 {
				readSize = 9 // default Modbus response size
			}

			logger.Infof("[execute] read op=%s deviceID=%s ch=%d readSize=%d timeout=%v dataHex=%x",
				req.Operation, deviceID, edge.ChannelID, readSize, timeout, writeData)

			resp, err := nodeMgr.PendingWrite().SendWriteCommand(deviceID, uint32(edge.ChannelID), writeData, readSize, timeout)
			if err != nil {
				Error(c, http.StatusInternalServerError, "command failed: "+err.Error())
				return
			}
			if !resp.Success {
				Error(c, http.StatusInternalServerError, fmt.Sprintf("device returned error: code=%d msg=%s", resp.ErrorCode, resp.ErrorMsg))
				return
			}

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
					if opConfig.Unit != "" {
						result["unit"] = opConfig.Unit
					}
				}
			} else if len(resp.RawData) > 0 {
				result["raw_hex"] = fmt.Sprintf("%x", resp.RawData)
			}

			// Execute post_action
			if err := executePostAction(db, edge, opConfig.PostAction, req.Params); err != nil {
				logger.Warnf("[execute] post_action failed for edge=%d op=%s: %v", edge.ID, req.Operation, err)
			}

			Success(c, result)

		default:
			Error(c, http.StatusBadRequest, fmt.Sprintf("unsupported operation type: %s", opConfig.Type))
		}
	})

	// POST /api/v1/edge-devices/:id/change-address — modify edge device address
	e.POST("/:id/change-address", RequireRole("admin"), func(c *gin.Context) {
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
						"{addr_hi}", fmt.Sprintf("%02X", newAddr>>8),
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
		// Update EdgeDevice.HardwareID and DeviceConfig.Connection.address
		newAddr, ok := params["new_addr"]
		if !ok {
			return fmt.Errorf("new_addr param required for update_connection_address")
		}
		newAddrStr := fmt.Sprintf("%v", newAddr)

		// Update EdgeDevice.HardwareID
		if err := db.Model(&edge).Update("hardware_id", newAddrStr).Error; err != nil {
			return fmt.Errorf("failed to update hardware_id: %w", err)
		}

		// Update DeviceConfig.Connection.address
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
		// Update address in default_params
		dp, ok := conn["default_params"].(map[string]interface{})
		if !ok {
			dp = make(map[string]interface{})
		}
		dp["address"] = newAddr
		conn["default_params"] = dp
		connJSON, _ := json.Marshal(conn)
		if err := db.Model(&dc).Update("connection", connJSON).Error; err != nil {
			return fmt.Errorf("failed to update connection: %w", err)
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
