package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
	"ehome/backend/internal/nodemgr"
	"ehome/backend/pkg/logger"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// createTemplatesFromDriver creates ConfigTemplates from the device driver's
// CommandTemplates (single source of truth).  Devices without a registered
// driver get no templates — that legacy DeviceConfig-derived fallback has been
// superseded by GenericModbusDriver / GenericI2CDriver.
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
		if err := createSingleTemplate(tx, ch, dev.ID, cmd.WriteData, cmd.ReadLength, cmd.DelayMs); err != nil {
			return fmt.Errorf("failed to create template for command %s: %w", cmd.ID, err)
		}
		created++
	}
	logger.Infof("[edge-device-create] Created %d ConfigTemplates for type=%s via driver", created, dev.Type)
	return nil
}

// createSingleTemplate inserts one ConfigTemplate and appends its ID to the channel's template_ids.
// edgeDeviceID records the owning edge device (方案 v3.3 §2.4); 0 leaves the
// ownership column NULL (self-healing / callers without a device instance).
func createSingleTemplate(tx *gorm.DB, ch *models.Channel, edgeDeviceID uint, writeData string, readLength uint32, delayMs uint32) error {
	if writeData == "" {
		return nil
	}
	tmpl := models.ConfigTemplate{
		NodeID:     ch.NodeID,
		WriteData:  writeData,
		ReadLength: readLength,
		DelayMs:    delayMs,
	}
	if edgeDeviceID > 0 {
		tmpl.EdgeDeviceID = &edgeDeviceID
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
func registerEdgeDeviceRoutes(v1 *gin.RouterGroup, db *gorm.DB, nodeMgr *nodemgr.Manager, driverRegistry *drivers.Registry) {
	driverRegistry = resolveDriverRegistry(driverRegistry)
	eventBus := nodeMgr.EventBus()

	// List edge devices (v2.2 path for /devices)
	v1.GET("/edge-devices", func(c *gin.Context) {
		var devices []models.EdgeDevice
		// P2.2 取消传播: 全量列表及其 Preload 链绑定请求上下文。
		query := db.WithContext(c.Request.Context()).Preload("Channel").Preload("Node").Preload("DeviceConfig")

		// Apply optional node_id filter
		nodeID := c.Query("node_id")
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

		if err := query.Find(&devices).Error; err != nil {
			logger.Warnf("[edge-devices-list] query failed: %v", err)
			Error(c, http.StatusInternalServerError, "failed to query edge devices")
			return
		}

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
			// 数据层时序化 (v3.4 §3.2.4): 缓存优先, miss 的设备回落原 DISTINCT ON SQL。
			var missed []uint
			cacheByDevice := make(map[uint][]lastDataEntry)
			for _, did := range deviceIDs {
				if rec, ok := LatestValue(did); ok {
					cacheByDevice[did] = append(cacheByDevice[did], lastDataEntry{DeviceID: rec.DeviceID, SensorName: rec.SensorName, Value: rec.Value, Unit: rec.Unit})
				} else {
					missed = append(missed, did)
				}
			}
			if len(missed) > 0 {
				var fallback []lastDataEntry
				db.Table("unified_data ud").
					Select("ud.device_id, ud.sensor_name, ud.value, ud.unit").
					Joins("INNER JOIN (SELECT DISTINCT ON (device_id) device_id, created_at FROM unified_data WHERE device_id IN ? ORDER BY device_id, created_at DESC) latest ON ud.device_id = latest.device_id AND ud.created_at = latest.created_at", missed).
					Where("ud.device_id IN ?", missed).
					Find(&fallback)
				for _, e := range fallback {
					cacheByDevice[e.DeviceID] = append(cacheByDevice[e.DeviceID], e)
				}
			}
			for _, entries := range cacheByDevice {
				allEntries = append(allEntries, entries...)
			}
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
	// 方案 v3.3 T5 P2: candidates 静态路由 (§1.3 §九) — gin 静态段优先于
	// :id 通配符, 必须先注册以显式声明共存关系。
	registerEdgeDeviceCandidateRoutes(v1, db)

	v1.GET("/edge-devices/:id", func(c *gin.Context) {
		id := c.Param("id")
		var d models.EdgeDevice
		if err := db.Preload("Channel").Preload("Node").Preload("DeviceConfig").First(&d, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				Error(c, http.StatusNotFound, "edge device not found")
				return
			}
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		Success(c, d)
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
			// CommandIntervals (optional): per-command polling interval map
			// (command_id → interval_ms, 0 = disabled). Validated against the
			// device type's schedulable command templates once dev.Type is
			// resolved (after DeviceConfig derivation, if any).
			CommandIntervals map[string]int `json:"command_intervals"`
			// 方案 v3.3 §3.3/§九: 继承目标 (可选)。指定时校验目标存在 +
			// device_type 匹配 + merged_into IS NULL + purge_requested=FALSE,
			// 并做存活实例唯一性校验; 未指定则新建 logical_device
			// (永不复用既有 key)。
			LogicalDeviceID *uint `json:"logical_device_id"`
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
			Error(c, http.StatusBadRequest, "type is derived from device_config_id")
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
			Error(c, http.StatusBadRequest, "type is required when device_config_id is not provided")
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
			// 方案 v3.3 §3.3: 创建继承 (可选)。指定 logical_device_id:
			// 事务内校验目标存在 + device_type 匹配 + merged_into IS NULL +
			// purge_requested=FALSE (v3.3-N1), 再做存活实例唯一性校验
			// (§3.3-3, 双存活实例混合数据无物理意义)。未指定: 创建后新建
			// logical_device, 永不复用既有 key (§2.3.1 路径表)。
			if dto.LogicalDeviceID != nil {
				target, err := validateInheritanceTarget(tx, *dto.LogicalDeviceID, dev.Type)
				if err != nil {
					return err
				}
				if err := checkLivingInstanceUniqueness(tx, target); err != nil {
					return err
				}
				dev.LogicalDeviceID = &target.ID
			}
			// Step 1: Create EdgeDevice (inside transaction)
			// command_intervals validation must run after dev.Type is resolved
			// (DeviceConfig-derived or explicit type). Unknown ids and
			// non-schedulable ids are rejected; negatives are normalized to 0.
			if len(dto.CommandIntervals) > 0 {
				intervalsJSON, vErr := validateAndNormalizeCommandIntervals(driverRegistry, dev.Type, dto.CommandIntervals)
				if vErr != nil {
					return vErr
				}
				dev.CommandIntervals = intervalsJSON
			}
			if err := tx.Create(&dev).Error; err != nil {
				return err
			}
			if dto.LogicalDeviceID == nil {
				// §3.3-4: "作为新设备创建"路径——任何设备都有逻辑身份,
				// 与删除流程 §2.3-2 对称。dev.ID 已就绪 (空 hardware_id 的
				// 确定性派生需要稳定基准)。
				if err := attachNewLogicalDevice(tx, &dev); err != nil {
					return err
				}
			}
			// §2.5: 仅"原位置重建"场景 (权重 100 档) 复制校准行。
			if err := copyCalibrationIfInPlace(tx, &dev); err != nil {
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
			var conflict conflictError
			if errors.As(err, &conflict) {
				// §3.3-2/§3.3-3: 继承校验失败与存活实例冲突为 409 语义。
				Error(c, http.StatusConflict, err.Error())
				return
			}
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
			Error(c, http.StatusBadRequest, err.Error())
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
				Error(c, http.StatusNotFound, "edge device not found")
			} else {
				Error(c, http.StatusBadRequest, err.Error())
			}
			return
		}
		// EmitConfigChange: find the node via channel
		var ch models.Channel
		if db.First(&ch, d.ChannelID).Error == nil {
			nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeEdgeDevice, nodemgr.CfgActionUpdate, ch.NodeID, fmt.Sprint(d.ID))
		}
		Success(c, d)
	})

	// Init edge device (trigger InitDevice via deviceinit.Orchestrator)
	v1.POST("/edge-devices/:id/init", func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))

		// Load the edge device to get device type, channel, and node info
		var dev models.EdgeDevice
		if err := db.First(&dev, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				Error(c, http.StatusNotFound, "edge device not found")
				return
			}
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		if !dev.Enabled {
			Error(c, http.StatusBadRequest, "edge device is disabled")
			return
		}
		if _, err := loadTransportChannel(db, dev.ChannelID, dev.NodeID); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}

		// Resolve the MQTT device ID from the associated Node
		node, err := findNodeByID(db, dev.NodeID)
		if err != nil {
			Error(c, http.StatusBadRequest, "associated node not found")
			return
		}
		deviceID := node.NodeID

		// Use the deviceinit Orchestrator to trigger init
		orchestrator := nodeMgr.DeviceInit()
		if orchestrator == nil {
			Error(c, http.StatusInternalServerError, "device init orchestrator not available")
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
			SuccessMsg(c, gin.H{
				"id": id, "status": "not_required", "message": "no init sequence for this device type",
				"device_type": dev.Type, "device_id": deviceID,
			}, "no init sequence for this device type")
			return
		}

		// Reserve and trigger through the orchestrator's single entry point. This
		// closes the API path's race with automatic online initialization.
		if !orchestrator.InitIfNeeded(dev, deviceID) {
			Error(c, http.StatusConflict, "device initialization already active or completed")
			return
		}

		// Update init state on the device record
		db.Model(&dev).Updates(map[string]interface{}{
			"init_state":       "running",
			"init_last_step":   0,
			"init_total_steps": len(steps),
		})

		SuccessMsg(c, gin.H{
			"id": id, "status": "running", "message": "init triggered",
			"device_type": dev.Type, "device_id": deviceID,
		}, "init triggered")
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
			if errors.Is(err, gorm.ErrRecordNotFound) {
				Error(c, http.StatusNotFound, "edge device not found")
				return
			}
			Error(c, http.StatusInternalServerError, err.Error())
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
			if err := handleConfigTemplateOnDelete(tx, &inst); err != nil {
				return err
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
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		// EmitConfigChange
		if hasNode {
			nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeEdgeDevice, nodemgr.CfgActionDelete, ch.NodeID, fmt.Sprint(d.ID))
		}
		SuccessMsg(c, gin.H{
			"deleted":           id,
			"logical_device_id": logicalID,
			"delete_data":       deleteData,
			"purge_requested":   deleteData,
		}, "deleted")
	})

	// Edge device routes group for :id sub-resources
	e := v1.Group("/edge-devices")

	// POST /api/v1/edge-devices/batch-delete — 批量删除边缘设备
	// 方案 v3.3 §2.2: 批量删除复用单删逻辑 (事务内逐条处理),
	// 返回每条结果汇总。delete_data 语义与单删一致。
	e.POST("/batch-delete", func(c *gin.Context) {
		var req struct {
			IDs        []uint `json:"ids" binding:"required,min=1,max=100"`
			DeleteData bool   `json:"delete_data"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}

		type result struct {
			ID      uint   `json:"id"`
			Success bool   `json:"success"`
			Error   string `json:"error,omitempty"`
		}
		results := make([]result, 0, len(req.IDs))

		for _, id := range req.IDs {
			r := result{ID: id}
			var d models.EdgeDevice
			if err := db.First(&d, id).Error; err != nil {
				r.Error = err.Error()
				results = append(results, r)
				continue
			}

			var logicalID uint
			err := db.Transaction(func(tx *gorm.DB) error {
				var inst models.EdgeDevice
				if err := tx.First(&inst, d.ID).Error; err != nil {
					return err
				}
				if inst.LogicalDeviceID == nil {
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
				if err := handleConfigTemplateOnDelete(tx, &inst); err != nil {
					return err
				}
				if err := tx.Delete(&models.EdgeDevice{}, inst.ID).Error; err != nil {
					return err
				}
				if req.DeleteData {
					if err := tx.Model(&models.LogicalDevice{}).Where("id = ?", logicalID).
						Update("purge_requested", true).Error; err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil {
				r.Error = err.Error()
			} else {
				r.Success = true
			}
			results = append(results, r)
		}

		succeeded := 0
		for _, r := range results {
			if r.Success {
				succeeded++
			}
		}
		Success(c, gin.H{
			"total":     len(results),
			"succeeded": succeeded,
			"failed":    len(results) - succeeded,
			"results":   results,
		})
	})

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
				Error(c, http.StatusNotFound, "edge device not found")
				return
			}
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}

		data := gin.H{
			"edge_device_id":    d.ID,
			"name":              d.Name,
			"logical_device_id": nil,
			"retention_days":    nil,
			"instance_count":    int64(1), // 至少包含本实例自身
		}

		if d.LogicalDeviceID != nil {
			var ld models.LogicalDevice
			if err := db.First(&ld, *d.LogicalDeviceID).Error; err == nil {
				data = gin.H{
					"edge_device_id":    d.ID,
					"name":              ld.Name,
					"logical_device_id": ld.ID,
					"retention_days":    ld.RetentionDays,
				}
				count, err := datalifecycle.CountInstances(db, ld.ID)
				if err == nil {
					data["instance_count"] = count
				}
				// T1.1: 估算段挂端点级超时兜底 — 超时/失败走降级路径
				// (省略 row_estimate), 保证端点不阻塞 (方案 §1.3)。
				estCtx, cancel := context.WithTimeout(c.Request.Context(), datalifecycle.EstimateTimeout)
				if rows, ok := datalifecycle.EstimateRowCount(estCtx, db, ld.ID); ok {
					data["row_estimate"] = rows
				}
				cancel()
			} else {
				data["instance_count"] = int64(1)
			}
		} else {
			// 尚未建立逻辑身份: 按实例自身范围估算 (NULL-logical 行)。
			estCtx, cancel := context.WithTimeout(c.Request.Context(), datalifecycle.EstimateTimeout)
			scope := &datalifecycle.Scope{InstanceIDs: []uint{d.ID}}
			if rows, ok := datalifecycle.EstimateScopeRows(estCtx, db, scope); ok {
				data["row_estimate"] = rows
			}
			cancel()
		}
		Success(c, data)
	})

	// GET /api/v1/edge-devices/:id/latest-data
	// 查询协议 (§六/§十二): 实例已删 → 404 (实例语义); 存活 → resolve
	// 逻辑身份后查 device_data 最新一条。
	e.GET("/:id/latest-data", func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		qs, err := datalifecycle.ResolveDataQueryScope(db, uint(id))
		if err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		if qs.InstanceDeleted {
			Error(c, http.StatusNotFound, "edge device not found")
			return
		}
		// 从 device_data 表查最新一条
		var data models.DeviceData
		cond, args := dataScopeCond(qs)
		db.Where(cond, args...).Order("created_at DESC").First(&data)
		Success(c, data)
	})

	// GET /api/v1/edge-devices/:id/data
	// 查询协议 (§六/§十二): 实例已删 → 404; 存活 → resolve 后查全量历史
	// (含继承/合并前数据, device_id 与 logical_device_id 列同名共用条件)。
	e.GET("/:id/data", func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		qs, err := datalifecycle.ResolveDataQueryScope(db, uint(id))
		if err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		if qs.InstanceDeleted {
			Error(c, http.StatusNotFound, "edge device not found")
			return
		}
		from := c.Query("start_time")
		to := c.Query("end_time")
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))
		cond, args := dataScopeCond(qs)
		var data []models.DeviceData
		// P2.2 取消传播: device_data 分页查询绑定请求上下文。
		q := db.WithContext(c.Request.Context()).Where(cond, args...)
		if from != "" {
			q = q.Where("created_at >= ?", from)
		}
		if to != "" {
			q = q.Where("created_at <= ?", to)
		}
		var total int64
		if err := q.Model(&models.DeviceData{}).Count(&total).Error; err != nil {
			logger.Warnf("[edge-device-data] count failed id=%d: %v", id, err)
			Error(c, http.StatusInternalServerError, "failed to count device data")
			return
		}
		if err := q.Order("created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&data).Error; err != nil {
			logger.Warnf("[edge-device-data] query failed id=%d: %v", id, err)
			Error(c, http.StatusInternalServerError, "failed to query device data")
			return
		}
		Success(c, gin.H{"items": data, "total": total})
	})
}
