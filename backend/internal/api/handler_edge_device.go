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

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
	"ehome/backend/internal/nodemgr"
	"ehome/backend/pkg/logger"
	"ehome/backend/pkg/metrics"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// operationNameRe validates operation names to prevent injection or malformed input.
var operationNameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// executeLimiter limits concurrent /execute read operations to prevent resource exhaustion.
var executeLimiter = make(chan struct{}, 20)

// createTemplatesFromDriver creates ConfigTemplates from the device driver's
// CommandTemplates (single source of truth).  Devices without a registered
// driver get no templates — the legacy getTemplateParamsFromDeviceConfig
// fallback has been superseded by GenericModbusDriver / GenericI2CDriver.
func createTemplatesFromDriver(tx *gorm.DB, driverRegistry *drivers.Registry, ch *models.Channel, dev *models.EdgeDevice) error {
	drv, err := driverRegistry.Get(dev.Type)
	if err != nil {
		// No driver registered — no templates. The generic_modbus and
		// generic_i2c drivers cover the former DeviceConfig-derived cases;
		// other device types should register a dedicated driver.
		logger.Warnf("[edge-device-create] no driver registered for type=%s, skipping ConfigTemplate creation", dev.Type)
		return nil
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
	logger.Infof("[edge-device-create] ConfigTemplate id=%d tx_hex_chars=%d channel=%d", tmpl.ID, len(writeData), ch.ID)
	return nil
}

// getTemplateParamsFromDeviceConfig attempts to derive ConfigTemplate parameters
// from a DeviceConfig's Connection JSONB field. Returns ("", 0, 0) if no params can be derived.
//
// Deprecated: Superseded by GenericModbusDriver / GenericI2CDriver. createTemplatesFromDriver
// no longer calls this function — register a driver (generic or dedicated) instead.
// Retained only for the transitional test suite in handler_edge_device_crud_test.go.
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

// checkDeviceUniqueness guards against two devices of the same model sharing one
// slave address on a channel. Multi-drop buses (SPI with several CS lines, I2C
// with several addresses) stay valid because they map to distinct channels or
// distinct hardware_id values. When hardware_id is empty we fall back to
// (channel_id, type) so address-less devices are still de-duplicated.
// excludeID (non-zero) excludes the device being updated from the check.
func checkDeviceUniqueness(tx *gorm.DB, channelID uint, devType, hardwareID string, excludeID uint) error {
	addr := strings.TrimSpace(hardwareID)
	dupQuery := tx.Model(&models.EdgeDevice{}).
		Where("channel_id = ? AND type = ?", channelID, devType)
	if addr != "" {
		dupQuery = dupQuery.Where("hardware_id = ?", addr)
	}
	if excludeID != 0 {
		dupQuery = dupQuery.Where("id <> ?", excludeID)
	}
	var dupCount int64
	if err := dupQuery.Count(&dupCount).Error; err != nil {
		return err
	}
	if dupCount > 0 {
		if addr != "" {
			return fmt.Errorf("channel %d already hosts a %q device at address %q", channelID, devType, addr)
		}
		return fmt.Errorf("channel %d already hosts a %q device", channelID, devType)
	}
	return nil
}

func validateDeviceConfigForChannel(db *gorm.DB, deviceConfigID uint, channel *models.Channel) (models.DeviceConfig, error) {
	if deviceConfigID == 0 {
		// 0 = no template; caller resolves type/hardware_types from the driver registry.
		return models.DeviceConfig{}, nil
	}
	var config models.DeviceConfig
	if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND status = ?", deviceConfigID, "active").First(&config).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.DeviceConfig{}, fmt.Errorf("active device config not found")
		}
		return models.DeviceConfig{}, err
	}
	configHardwareType := strings.TrimSpace(config.HardwareType)
	if configHardwareType == "" {
		// v2.2: fall back to Connection JSONB bus_type when legacy hardware_type is absent.
		if len(config.Connection) > 0 {
			var conn map[string]any
			if err := json.Unmarshal(config.Connection, &conn); err == nil {
				if bt, ok := conn["bus_type"].(string); ok {
					configHardwareType = strings.TrimSpace(bt)
				}
			}
		}
	}
	if configHardwareType == "" {
		return models.DeviceConfig{}, fmt.Errorf("device config has no hardware type")
	}
	if !strings.EqualFold(configHardwareType, channel.HardwareType) && !strings.EqualFold(configHardwareType, channel.BusType) {
		return models.DeviceConfig{}, fmt.Errorf("device config hardware type %q is incompatible with channel type %q", configHardwareType, channel.HardwareType)
	}
	return config, nil
}

// registerEdgeDeviceRoutes sets up edge-device CRUD routes
func registerEdgeDeviceRoutes(v1 *gin.RouterGroup, db *gorm.DB, nodeMgr *nodemgr.Manager, driverRegistry *drivers.Registry, policies ...ControlPolicy) {
	driverRegistry = resolveDriverRegistry(driverRegistry)
	controlPolicy := resolveControlPolicy(policies...)
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
			// F: inline channel creation — when channel_id is 0/absent and
			// channel is provided, the channel is created inside the same
			// transaction, eliminating the two-phase-commit orphan risk.
			Channel *struct {
				HardwareType *string          `json:"hardware_type"`
				HardwareID   *string          `json:"hardware_id"`
				Address      *string          `json:"address"`
				Config       *json.RawMessage `json:"config"`
				// bus_config carries the hex-encoded pin-route payload (the
				// same shape as Channel.BusConfig). The device wizard's inline
				// path typically omits it (no pin route to validate); a
				// caller that supplies one gets the same
				// validateChannelPeripheralConflicts gate as the dedicated
				// channel-create path.
				BusConfig *string `json:"bus_config"`
			} `json:"channel"`
		}
		if err := c.ShouldBindJSON(&dto); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		if dto.Type != nil && dto.DeviceConfigID != nil && *dto.DeviceConfigID != 0 {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "type is derived from device_config_id"})
			return
		}
		// channel_id is required unless an inline channel object is provided
		hasInlineChannel := dto.Channel != nil && dto.Channel.HardwareType != nil
		if dto.ChannelID == nil && !hasInlineChannel {
			Error(c, http.StatusBadRequest, "channel_id or channel is required")
			return
		}
		if dto.Name == nil || dto.NodeID == nil {
			Error(c, http.StatusBadRequest, "name and node_id are required")
			return
		}
		// Resolve effective device_config_id (0 when absent or explicitly 0)
		var cfgID uint
		if dto.DeviceConfigID != nil {
			cfgID = *dto.DeviceConfigID
		}
		// When no DeviceConfig template is provided, type must be supplied by the caller
		// and validated against the driver registry.
		if cfgID == 0 && (dto.Type == nil || strings.TrimSpace(*dto.Type) == "") {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "type is required when device_config_id is not provided"})
			return
		}
		// Resolve the effective channel ID: use channel_id when provided,
		// otherwise the inline channel object will be created inside the transaction.
		var effectiveChannelID uint
		if dto.ChannelID != nil {
			effectiveChannelID = *dto.ChannelID
		}
		dev := models.EdgeDevice{Name: *dto.Name, NodeID: *dto.NodeID}
		if dto.Enabled != nil {
			dev.Enabled = *dto.Enabled
		}
		if dto.IntervalMs != nil {
			dev.IntervalMs = *dto.IntervalMs
		}
		if dto.HardwareID != nil {
			dev.HardwareID = *dto.HardwareID
		}

		if err := db.Transaction(func(tx *gorm.DB) error {
			var bindingChannel models.Channel
			if effectiveChannelID > 0 {
				// Existing channel path
				if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND node_id = ?", effectiveChannelID, *dto.NodeID).First(&bindingChannel).Error; err != nil {
					return fmt.Errorf("channel does not belong to node")
				}
				if err := validateTransportChannel(&bindingChannel); err != nil {
					return err
				}
			} else {
				// F: inline channel creation path
				chInput := dto.Channel
				hwType := strings.TrimSpace(*chInput.HardwareType)
				busType := strings.ToUpper(hwType) // hardware_type == bus_type for transport channels
				bindingChannel = models.Channel{
					NodeID:       *dto.NodeID,
					HardwareType: hwType,
					BusType:      busType,
					Enabled:      true,
				}
				if chInput.HardwareID != nil {
					bindingChannel.HardwareID = *chInput.HardwareID
				}
				if chInput.Address != nil && *chInput.Address != "" {
					bindingChannel.HardwareID = *chInput.Address
				}
				if chInput.Config != nil {
					bindingChannel.Config = string(*chInput.Config)
				}
				if chInput.BusConfig != nil {
					bindingChannel.BusConfig = *chInput.BusConfig
				}
				if err := validateTransportChannelType(&bindingChannel); err != nil {
					return err
				}
				// Lock the node row to prevent concurrent channel creation races
				var node models.Node
				if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("node_id = ?", bindingChannel.NodeID).First(&node).Error; err != nil {
					return err
				}
				// Peripheral pin-conflict check runs only when a bus_config (pin
				// route) is supplied — consistent with handler_node.go channel
				// updates. The inline wizard path omits bus_config (no route to
				// validate), so the check is intentionally skipped there; a
				// caller that supplies bus_config gets the same gate as the
				// dedicated channel-create path.
				if bindingChannel.BusConfig != "" {
					if err := validateChannelPeripheralConflicts(tx, bindingChannel); err != nil {
						return err
					}
				}
				if err := tx.Create(&bindingChannel).Error; err != nil {
					return err
				}
				effectiveChannelID = bindingChannel.ID
			}
			dev.ChannelID = effectiveChannelID
			config, err := validateDeviceConfigForChannel(tx, cfgID, &bindingChannel)
			if err != nil {
				return err
			}
			if cfgID > 0 {
				// DeviceConfig path: type derived from config
				dev.Type = config.DeviceType
				dev.DeviceConfigID = config.ID
			} else {
				// No template: validate type against the driver registry
				devType := strings.TrimSpace(*dto.Type)
				if _, derr := driverRegistry.Get(devType); derr != nil {
					return fmt.Errorf("device type %q is not registered: %w", devType, derr)
				}
				dev.Type = devType
				dev.DeviceConfigID = 0
			}
			// Uniqueness guard: the same slave address must not host two devices of the
			// same model on one channel. Multi-drop buses (e.g. SPI with several CS
			// lines, I2C with several addresses) stay valid because they map to
			// distinct channels or distinct hardware_id values. When hardware_id is
			// empty we fall back to (channel_id, type) so address-less devices are
			// still de-duplicated.
			if err := checkDeviceUniqueness(tx, dev.ChannelID, dev.Type, dev.HardwareID, 0); err != nil {
				return err
			}
			// Step 1: Create EdgeDevice (inside transaction)
			if err := tx.Create(&dev).Error; err != nil {
				return err
			}
			// An explicit zero interval means "do not schedule".  The model has a
			// historical database default of 5000, which GORM otherwise substitutes
			// for a zero value during INSERT.
			if dto.IntervalMs != nil && *dto.IntervalMs == 0 {
				if err := tx.Model(&dev).UpdateColumn("interval_ms", 0).Error; err != nil {
					return err
				}
				dev.IntervalMs = 0
			}

			// Step 2: Create ConfigTemplates from driver's CommandTemplates (single source of truth)
			var ch models.Channel
			if err := tx.First(&ch, dev.ChannelID).Error; err == nil {
				if err := createTemplatesFromDriver(tx, driverRegistry, &ch, &dev); err != nil {
					logger.Warnf("[edge-device-create] Failed to create ConfigTemplates: %v", err)
				}
			}

			return nil // commit transaction
		}); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}

		// EmitConfigChange (outside transaction — event emission should not block rollback)
		var chForEvent models.Channel
		if db.First(&chForEvent, dev.ChannelID).Error == nil {
			nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeEdgeDevice, nodemgr.CfgActionCreate, chForEvent.NodeID, fmt.Sprint(dev.ID))
		}

		// Reload with associations for response
		db.Preload("Channel").Preload("Node").Preload("DeviceConfig").First(&dev, dev.ID)
		SuccessWithCode(c, http.StatusCreated, dev)
	})

	// Update edge device (v2.2 path for PUT /devices/:id)
	v1.PUT("/edge-devices/:id", func(c *gin.Context) {
		id := c.Param("id")
		// Bind to a separate DTO, then validate and update the complete candidate state atomically.
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
		var d models.EdgeDevice
		if err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&d, id).Error; err != nil {
				return err
			}
			targetNodeID, targetChannelID, configID := d.NodeID, d.ChannelID, d.DeviceConfigID
			if dto.NodeID != nil {
				targetNodeID = *dto.NodeID
			}
			if dto.ChannelID != nil {
				targetChannelID = *dto.ChannelID
			}
			if dto.DeviceConfigID != nil {
				configID = *dto.DeviceConfigID
			}
			// G1: type may only be supplied by the caller on the driver-registry path (no DeviceConfig template).
			if dto.Type != nil && configID != 0 {
				return fmt.Errorf("type is derived from device_config_id")
			}
			// When there is no template, a caller-supplied type must be a registered driver.
			if configID == 0 {
				if dto.Type != nil {
					devType := strings.TrimSpace(*dto.Type)
					if devType == "" {
						return fmt.Errorf("type must not be empty")
					}
					if _, derr := driverRegistry.Get(devType); derr != nil {
						return fmt.Errorf("device type %q is not registered: %w", devType, derr)
					}
				} else if d.Type == "" {
					return fmt.Errorf("type is required when device_config_id is not provided")
				}
			}
			var targetChannel models.Channel
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND node_id = ?", targetChannelID, targetNodeID).First(&targetChannel).Error; err != nil {
				return fmt.Errorf("channel does not belong to node")
			}
			if err := validateTransportChannel(&targetChannel); err != nil {
				return err
			}
			config, err := validateDeviceConfigForChannel(tx, configID, &targetChannel)
			if err != nil {
				return err
			}
			updates := map[string]interface{}{}
			if configID > 0 {
				updates["device_config_id"] = config.ID
				updates["type"] = config.DeviceType
			} else {
				updates["device_config_id"] = uint(0)
				if dto.Type != nil {
					updates["type"] = strings.TrimSpace(*dto.Type)
				}
			}
			if dto.Name != nil {
				updates["name"] = *dto.Name
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
			if dto.NodeID != nil {
				updates["node_id"] = targetNodeID
			}
			if dto.ChannelID != nil {
				updates["channel_id"] = targetChannelID
			}
			if dto.Status != nil {
				updates["status"] = *dto.Status
			}
			// Uniqueness guard on update: compute the candidate (channel_id, type,
			// hardware_id) triple after applying the requested changes, then reject
			// if it would collide with another device. excludeID = d.ID so the device
			// does not collide with itself. This closes the bypass where a PUT could
			// move a device onto a channel/address/type already taken by another.
			candidateType := d.Type
			if v, ok := updates["type"]; ok {
				candidateType, _ = v.(string)
			}
			candidateHardwareID := d.HardwareID
			if dto.HardwareID != nil {
				candidateHardwareID = *dto.HardwareID
			}
			if err := checkDeviceUniqueness(tx, targetChannelID, candidateType, candidateHardwareID, d.ID); err != nil {
				return err
			}
			if err := tx.Model(&d).Updates(updates).Error; err != nil {
				return err
			}
			return tx.Preload("Channel").Preload("Node").Preload("DeviceConfig").First(&d, d.ID).Error
		}); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "edge device not found"})
			} else {
				c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": err.Error()})
			}
			return
		}
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

		// G3K: devices without an init sequence must not be marked "running"
		// with a 0/N progress bar. Short-circuit to "completed"/"not_required"
		// before touching the orchestrator's reservation cache.
		steps := orchestrator.GetInitSequence(dev.Type)
		if len(steps) == 0 {
			db.Model(&dev).Updates(map[string]interface{}{
				"init_state":       "completed",
				"init_last_step":   0,
				"init_total_steps": 0,
			})
			c.JSON(http.StatusOK, gin.H{"code": 200, "data": gin.H{
				"id": id, "status": "not_required", "message": "no init sequence for this device type",
				"device_type": dev.Type, "device_id": deviceID,
			}})
			return
		}

		// Reserve and trigger through the orchestrator's single entry point. This
		// closes the API path's race with automatic online initialization.
		if !orchestrator.InitIfNeeded(dev, deviceID) {
			c.JSON(http.StatusConflict, gin.H{"code": 409, "message": "device initialization already active or completed"})
			return
		}

		// Update init state on the device record
		db.Model(&dev).Updates(map[string]interface{}{
			"init_state":       "running",
			"init_last_step":   0,
			"init_total_steps": len(steps),
		})

		c.JSON(http.StatusOK, gin.H{"code": 200, "data": gin.H{
			"id": id, "status": "running", "message": "init triggered",
			"device_type": dev.Type, "device_id": deviceID,
		}})
	})

	// Delete edge device (v2.2 path for DELETE /devices/:id)
	// 方案 v3.3 §2.3/§九: +delete_data 查询参数 (默认 false)。事务内:
	// 缺逻辑身份则补建 (§2.3.1 路径 3) → GORM 软删实例 → delete_data=true
	// 时置 logical_devices.purge_requested 标记; 数据硬删由 purge 后台任务
	// 异步分批执行 (§4.3), 不在 API 事务里删数据。
	v1.DELETE("/edge-devices/:id", func(c *gin.Context) {
		id := c.Param("id")
		deleteData := c.Query("delete_data") == "true" || c.Query("delete_data") == "1"

		var d models.EdgeDevice
		if err := db.First(&d, id).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}
		// Find node before deletion for event emission
		var ch models.Channel
		hasNode := db.First(&ch, d.ChannelID).Error == nil

		var logicalID uint
		err := db.Transaction(func(tx *gorm.DB) error {
			var inst models.EdgeDevice
			if err := tx.First(&inst, d.ID).Error; err != nil {
				return err
			}
			if inst.LogicalDeviceID == nil {
				// §2.3.1 路径 3: 删除时补建——允许复用既有 key (复用前跟随
				// merged_into 链挂到最终目标; 目标 purge_requested=TRUE 时
				// 禁止复用, 退序号 key)。保证任何被删设备都有逻辑身份锚点。
				ld, err := datalifecycle.EnsureLogicalDevice(tx, &inst, datalifecycle.PathDelete, datalifecycle.SystemRetentionDays())
				if err != nil {
					return err
				}
				if err := tx.Model(&models.EdgeDevice{}).Where("id = ?", inst.ID).
					Update("logical_device_id", ld.ID).Error; err != nil {
					return err
				}
				logicalID = ld.ID
			} else {
				logicalID = *inst.LogicalDeviceID
			}
			if err := tx.Delete(&models.EdgeDevice{}, inst.ID).Error; err != nil {
				return err
			}
			if deleteData {
				// 只置标记; purge 后台任务 (v3.3-N1 守卫后) 分批硬删数据。
				if err := tx.Model(&models.LogicalDevice{}).Where("id = ?", logicalID).
					Update("purge_requested", true).Error; err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}
		// EmitConfigChange
		if hasNode {
			nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeEdgeDevice, nodemgr.CfgActionDelete, ch.NodeID, fmt.Sprint(d.ID))
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "deleted", "data": gin.H{
			"deleted":           id,
			"logical_device_id": logicalID,
			"delete_data":       deleteData,
			"purge_requested":   deleteData,
		}})
	})

	// Edge device routes group for :id sub-resources
	e := v1.Group("/edge-devices")

	// GET /api/v1/edge-devices/:id/logical-device-info — 删除弹窗信息区
	// (方案 v3.3 §2.1/§1.3): 异步加载逻辑设备信息。row_estimate 用估算
	// (PG EXPLAIN reltuples / SQLite 截断 COUNT), 3s 超时降级为不含数据量
	// (row_estimate 字段省略)。最终形态: GET 单资源, 实例缺逻辑身份时
	// 按实例自身数据范围估算 (logical_device_id 为 null, 其余字段置空)。
	e.GET("/:id/logical-device-info", func(c *gin.Context) {
		id := c.Param("id")
		var d models.EdgeDevice
		if err := db.First(&d, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "edge device not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}

		resp := gin.H{"code": 200, "data": gin.H{
			"edge_device_id":    d.ID,
			"name":              d.Name,
			"logical_device_id": nil,
			"retention_days":    nil,
			"instance_count":    int64(1), // 至少包含本实例自身
		}}

		if d.LogicalDeviceID != nil {
			var ld models.LogicalDevice
			if err := db.First(&ld, *d.LogicalDeviceID).Error; err == nil {
				resp = gin.H{"code": 200, "data": gin.H{
					"edge_device_id":    d.ID,
					"name":              ld.Name,
					"logical_device_id": ld.ID,
					"retention_days":    ld.RetentionDays,
				}}
				count, err := datalifecycle.CountInstances(db, ld.ID)
				if err == nil {
					resp["data"].(gin.H)["instance_count"] = count
				}
				if rows, ok := datalifecycle.EstimateRowCount(db, ld.ID); ok {
					resp["data"].(gin.H)["row_estimate"] = rows
				}
			} else {
				resp["data"].(gin.H)["instance_count"] = int64(1)
			}
		} else {
			// 尚未建立逻辑身份: 按实例自身范围估算 (NULL-logical 行)。
			scope := &datalifecycle.Scope{InstanceIDs: []uint{d.ID}}
			if rows, ok := datalifecycle.EstimateScopeRows(db, scope); ok {
				resp["data"].(gin.H)["row_estimate"] = rows
			}
		}
		c.JSON(http.StatusOK, resp)
	})

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
		if opType == "write" && !controlPolicy.legacyWritesEnabled() {
			Error(c, http.StatusGone, "legacy device writes are disabled; migrate this operation to a trusted device action")
			return
		}
		// Phase 2 has a durable ChannelCmdV2 read path. Keeping even the old
		// read branch reachable in production would let callers bypass
		// CommandExecution, Outbox/Inbox, capability freshness and the timeline.
		if !controlPolicy.legacyReadBridgeEnabled() {
			Error(c, http.StatusGone, "legacy execute is retired; use the trusted device operation API")
			return
		}

		// 8.1: Track concurrent executions
		metrics.ExecuteConcurrentActive.Inc()
		metrics.ExecuteRequestTotal.WithLabelValues(opType, "started").Inc()
		defer metrics.ExecuteConcurrentActive.Dec()

		// 3. Resolve current address from EdgeDevice
		addr := parseHardwareIDUint(edge.HardwareID)

		// DeviceConfig is shared by multiple EdgeDevice instances in the
		// multi-bus model, so its connection defaults must never supply an
		// instance address. Default to Modbus slave address 1 only when this
		// EdgeDevice has no instance address yet.
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

		logger.Infof("[execute] edge=%d op=%s type=%s addr=%d tx_bytes=%d", edge.ID, req.Operation, opConfig.Type, addr, len(writeData))

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

			// Trigger config sync against the entity that owns the changed value.
			// In multi-bus mode baud belongs to Channel; address belongs to the
			// individual EdgeDevice instance.
			changeType, entityID := legacyPostActionConfigChange(edge, opConfig.PostAction)
			nodemgr.EmitConfigChange(c, eventBus, changeType, nodemgr.CfgActionUpdate, edge.NodeID, entityID)

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

			logger.Infof("[execute] read op=%s deviceID=%s ch=%d readSize=%d timeout=%v tx_bytes=%d",
				req.Operation, deviceID, edge.ChannelID, readSize, timeout, len(writeData))

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
		if !controlPolicy.legacyWritesEnabled() {
			Error(c, http.StatusGone, "legacy address changes are disabled; migrate to a verified device action")
			return
		}

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
					logger.Infof("[change-address] Using DeviceConfig template for edge=%d, old=%d→new=%d, tx_bytes=%d",
						edge.ID, oldAddr, newAddr, len(writeData))
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
		// Address is instance-owned. Never write it into the shared DeviceConfig.
		newAddr, ok := params["new_addr"]
		if !ok {
			return fmt.Errorf("new_addr param required for update_connection_address")
		}
		newAddrValue, err := toUint64(newAddr)
		if err != nil || newAddrValue < 1 || newAddrValue > 254 {
			return fmt.Errorf("unsupported address %v", newAddr)
		}
		newAddrStr := strconv.FormatUint(newAddrValue, 10)

		err = db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&models.EdgeDevice{}).Where("id = ? AND node_id = ?", edge.ID, edge.NodeID).Update("hardware_id", newAddrStr).Error; err != nil {
				return fmt.Errorf("failed to update hardware_id: %w", err)
			}
			return nil
		})
		if err != nil {
			return err
		}

		logger.Infof("[execute] post_action update_connection_address: edge=%d new_addr=%v", edge.ID, newAddr)

	case "update_connection_baud":
		// Baud rate belongs to the physical Channel, not the shared device model.
		newBaud, ok := params["new_baud"]
		if !ok {
			return fmt.Errorf("new_baud param required for update_connection_baud")
		}

		baud := strings.TrimSpace(fmt.Sprint(newBaud))
		if baud != "2400" && baud != "4800" && baud != "9600" {
			return fmt.Errorf("unsupported baud rate %q", baud)
		}
		var channel models.Channel
		if err := db.Where("id = ? AND node_id = ?", edge.ChannelID, edge.NodeID).First(&channel).Error; err != nil {
			return fmt.Errorf("failed to load channel: %w", err)
		}
		text := strings.TrimSpace(channel.BusConfig)
		text = strings.TrimPrefix(strings.TrimPrefix(text, "\\x"), "0x")
		data, err := hex.DecodeString(text)
		if err != nil || len(data) < 6 {
			return fmt.Errorf("channel %d has malformed UART bus_config", channel.ID)
		}
		target, _ := strconv.ParseUint(baud, 10, 32)
		data[2], data[3], data[4], data[5] = byte(target>>24), byte(target>>16), byte(target>>8), byte(target)
		if err := db.Model(&models.Channel{}).Where("id = ? AND node_id = ?", channel.ID, channel.NodeID).Update("bus_config", strings.ToUpper(hex.EncodeToString(data))).Error; err != nil {
			return fmt.Errorf("failed to update channel bus_config: %w", err)
		}

		logger.Infof("[execute] post_action update_connection_baud: edge=%d new_baud=%v", edge.ID, newBaud)

	case "":
		// No post action

	default:
		logger.Warnf("[execute] unknown post_action: %s", postAction)
	}

	return nil
}

func legacyPostActionConfigChange(edge models.EdgeDevice, postAction string) (nodemgr.ConfigChangeType, string) {
	if postAction == "update_connection_baud" {
		return nodemgr.CfgChangeChannel, fmt.Sprint(edge.ChannelID)
	}
	return nodemgr.CfgChangeEdgeDevice, fmt.Sprint(edge.ID)
}
