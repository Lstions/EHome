package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/nodemgr"
	"ehome/backend/pkg/logger"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// findNodeByID resolves a node by DB primary key (if numeric) or node_id string.
func findNodeByID(db *gorm.DB, id string) (*models.Node, error) {
	var node models.Node
	if intID, err := strconv.Atoi(id); err == nil {
		if db.First(&node, intID).Error == nil {
			return &node, nil
		}
	}
	if err := db.Where("node_id = ?", id).First(&node).Error; err != nil {
		return nil, err
	}
	return &node, nil
}

// getDefaultESP32C6Buses returns the default hardware resources for ESP32-C6.
// Used as fallback when DB capabilities field is empty.
func getDefaultESP32C6Buses() map[string]interface{} {
	return map[string]interface{}{
		"uart": []interface{}{
			map[string]interface{}{"id": "UART0", "port": 0, "default_tx": 16, "default_rx": 17, "max_baud": 5000000,
				"pins": []interface{}{
					map[string]interface{}{"pin": 16, "role": 1},
					map[string]interface{}{"pin": 17, "role": 2},
				}},
			map[string]interface{}{"id": "UART1", "port": 1, "default_tx": 21, "default_rx": 20, "max_baud": 5000000,
				"pins": []interface{}{
					map[string]interface{}{"pin": 21, "role": 1},
					map[string]interface{}{"pin": 20, "role": 2},
				}},
		},
		"i2c": []interface{}{
			map[string]interface{}{"id": "I2C0", "port": 0, "default_sda": 21, "default_scl": 22, "max_freq_hz": 1000000,
				"pins": []interface{}{
					map[string]interface{}{"pin": 21, "role": 3},
					map[string]interface{}{"pin": 22, "role": 4},
				}},
		},
		"spi": []interface{}{
			map[string]interface{}{"id": "SPI2", "port": 2, "default_mosi": 23, "default_miso": 19, "default_sclk": 18, "default_cs": 5, "max_freq_hz": 40000000,
				"pins": []interface{}{
					map[string]interface{}{"pin": 23, "role": 5},
					map[string]interface{}{"pin": 19, "role": 6},
					map[string]interface{}{"pin": 18, "role": 7},
					map[string]interface{}{"pin": 5, "role": 8},
				}},
		},
		"gpio": []interface{}{
			map[string]interface{}{"id": "GPIO0", "pin": 0, "pins": []interface{}{map[string]interface{}{"pin": 0, "role": 9}}},
			map[string]interface{}{"id": "GPIO1", "pin": 1, "pins": []interface{}{map[string]interface{}{"pin": 1, "role": 9}}},
			map[string]interface{}{"id": "GPIO2", "pin": 2, "pins": []interface{}{map[string]interface{}{"pin": 2, "role": 9}}},
			map[string]interface{}{"id": "GPIO3", "pin": 3, "pins": []interface{}{map[string]interface{}{"pin": 3, "role": 9}}},
			map[string]interface{}{"id": "GPIO4", "pin": 4, "pins": []interface{}{map[string]interface{}{"pin": 4, "role": 9}}},
			map[string]interface{}{"id": "GPIO5", "pin": 5, "pins": []interface{}{map[string]interface{}{"pin": 5, "role": 9}}},
			map[string]interface{}{"id": "GPIO6", "pin": 6, "pins": []interface{}{map[string]interface{}{"pin": 6, "role": 9}}},
			map[string]interface{}{"id": "GPIO7", "pin": 7, "pins": []interface{}{map[string]interface{}{"pin": 7, "role": 9}}},
		},
		"adc": []interface{}{
			map[string]interface{}{"id": "ADC1_CH0", "unit": 1, "channel": 0, "max_bits": 12, "pins": []interface{}{map[string]interface{}{"pin": 0, "role": 10}}},
			map[string]interface{}{"id": "ADC1_CH1", "unit": 1, "channel": 1, "max_bits": 12, "pins": []interface{}{map[string]interface{}{"pin": 1, "role": 10}}},
			map[string]interface{}{"id": "ADC1_CH2", "unit": 1, "channel": 2, "max_bits": 12, "pins": []interface{}{map[string]interface{}{"pin": 2, "role": 10}}},
		},
	}
}

// registerNodeRoutes sets up node CRUD routes
func registerNodeRoutes(v1 *gin.RouterGroup, db *gorm.DB, nodeMgr *nodemgr.Manager) {
	eventBus := nodeMgr.EventBus()

	// List nodes (v2.2 compat path)
	v1.GET("/nodes", func(c *gin.Context) {
		var nodes []models.Node
		if err := db.Find(&nodes).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		Success(c, nodes)
	})

	// Get node by DB id or node_id (v2.2 path)
	v1.GET("/nodes/:id", func(c *gin.Context) {
		id := c.Param("id")
		var node models.Node
		// Try by primary key first (if numeric)
		if intID, err := strconv.Atoi(id); err == nil {
			if db.First(&node, intID).Error == nil {
				Success(c, node)
				return
			}
		}
		// Fallback: lookup by node_id string
		if err := db.Where("node_id = ?", id).First(&node).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
			return
		}
		Success(c, node)
	})

	// Create node (v2.2 compat path)
	v1.POST("/nodes", func(c *gin.Context) {
		var node models.Node
		if err := c.ShouldBindJSON(&node); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := db.Create(&node).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeNode, nodemgr.CfgActionCreate, node.NodeID, fmt.Sprint(node.ID))
		c.JSON(http.StatusCreated, node)
	})

	// Update node (v2.2 path for PUT /collectors/:id)
	// M1 fix: use separate DTO to prevent field injection (ID, CreatedAt, etc.)
	v1.PUT("/nodes/:id", func(c *gin.Context) {
		id := c.Param("id")
		node, err := findNodeByID(db, id)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
			return
		}
		// M1 fix: bind to a separate DTO, then copy allowed fields only
		var dto struct {
			Name            *string `json:"name"`
			Model           *string `json:"model"`
			FirmwareVersion *string `json:"firmware_version"`
			ProtocolVersion *string `json:"protocol_version"`
			Platform        *string `json:"platform"`
			Status          *string `json:"status"`
			ConfigVersion   *string `json:"config_version"`
			ConfigStatus    *string `json:"config_status"`
			WiFiSSID        *string `json:"wifi_ssid"`
			WiFiRSSI        *int    `json:"wifi_rssi"`
			FreeHeapBytes   *int    `json:"free_heap_bytes"`
			Capabilities    *string `json:"capabilities"`
			HardwareInfo    *string `json:"hardware_info"`
			Config          *string `json:"config"`
		}
		if err := c.ShouldBindJSON(&dto); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		updates := map[string]interface{}{}
		if dto.Name != nil {
			updates["name"] = *dto.Name
		}
		if dto.Model != nil {
			updates["model"] = *dto.Model
		}
		if dto.FirmwareVersion != nil {
			updates["firmware_version"] = *dto.FirmwareVersion
		}
		if dto.ProtocolVersion != nil {
			updates["protocol_version"] = *dto.ProtocolVersion
		}
		if dto.Platform != nil {
			updates["platform"] = *dto.Platform
		}
		if dto.Status != nil {
			updates["status"] = *dto.Status
		}
		if dto.ConfigVersion != nil {
			updates["config_version"] = *dto.ConfigVersion
		}
		if dto.ConfigStatus != nil {
			updates["config_status"] = *dto.ConfigStatus
		}
		if dto.WiFiSSID != nil {
			updates["wifi_ssid"] = *dto.WiFiSSID
		}
		if dto.WiFiRSSI != nil {
			updates["wifi_rssi"] = *dto.WiFiRSSI
		}
		if dto.FreeHeapBytes != nil {
			updates["free_heap_bytes"] = *dto.FreeHeapBytes
		}
		if dto.Capabilities != nil {
			updates["capabilities"] = *dto.Capabilities
		}
		if dto.HardwareInfo != nil {
			updates["hardware_info"] = *dto.HardwareInfo
		}
		if dto.Config != nil {
			updates["config"] = *dto.Config
		}
		if len(updates) > 0 {
			db.Model(node).Updates(updates)
		}
		nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeNode, nodemgr.CfgActionUpdate, node.NodeID, fmt.Sprint(node.ID))
		// Reload node to get updated fields
		db.First(node, node.ID)
		Success(c, node)
	})

	// Delete node (v2.2 path for DELETE /collectors/:id)
	v1.DELETE("/nodes/:id", func(c *gin.Context) {
		id := c.Param("id")
		node, err := findNodeByID(db, id)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
			return
		}
		nodeIDStr := node.NodeID
		if err := db.Delete(node).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		nodemgr.InvalidateNodeIDCache(nodeIDStr)
		nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeNode, nodemgr.CfgActionDelete, nodeIDStr, nodeIDStr)
		c.JSON(http.StatusOK, gin.H{"message": "deleted"})
	})

	// Get channels for node
	v1.GET("/nodes/:id/channels", func(c *gin.Context) {
		id := c.Param("id")
		node, err := findNodeByID(db, id)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
			return
		}
		var channels []models.Channel
		db.Where("node_id = ?", node.NodeID).Find(&channels)
		Success(c, channels)
	})

	// Get node data
	v1.GET("/nodes/:id/data", func(c *gin.Context) {
		id := c.Param("id")
		limitStr := c.DefaultQuery("limit", "100")
		limit, _ := strconv.Atoi(limitStr)
		node, err := findNodeByID(db, id)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
			return
		}
		var data []models.DeviceData
		db.Where("node_id = ?", node.NodeID).Order("timestamp DESC").Limit(limit).Find(&data)
		Success(c, data)
	})

	// Ping node
	v1.POST("/nodes/:id/ping", func(c *gin.Context) {
		id := c.Param("id")
		node, err := findNodeByID(db, id)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
			return
		}
		if err := nodeMgr.SendPing(node.NodeID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "ping sent"})
	})

	// Get node config (v2.2 ConfigManifest)
	v1.GET("/nodes/:id/config", getNodeConfig(db, nodeMgr))

	// Update node config (v2.2 incremental update)
	v1.PUT("/nodes/:id/config", updateNodeConfig(db, nodeMgr))

	// BUG-08 fix: OTA history for a specific node
	v1.GET("/nodes/:id/ota/history", getNodeOTAHistory(db))

	// POST /api/v1/nodes/:id/bus/i2c/scan
	n := v1.Group("/nodes")
	n.POST("/:id/bus/i2c/scan", func(c *gin.Context) {
		id := c.Param("id")
		node, err := findNodeByID(db, id)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
			return
		}
		var req struct {
			HardwareID string `json:"hardware_id"`
		}
		c.ShouldBindJSON(&req)
		// NOTE: requires MQTT broadcast I2C_SCAN to node firmware
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": gin.H{
			"devices": []string{}, "request_id": fmt.Sprintf("i2c-%s-%d", node.NodeID, time.Now().Unix()),
		}})
	})

	// POST /api/v1/nodes/:id/config/sync
	n.POST("/:id/config/sync", func(c *gin.Context) {
		id := c.Param("id")
		node, err := findNodeByID(db, id)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "node not found"})
			return
		}
		// Force a full config push to this node (bypass hash match)
		decisions := nodeMgr.SyncGate().OnServerStartup()
		for _, d := range decisions {
			if d.DeviceID == node.NodeID {
				nodeMgr.SendConfigManifestWithDecision(d)
				break
			}
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": gin.H{"node_id": node.NodeID, "status": "syncing"}})
	})

	// GET /api/v1/nodes/:id/capabilities
	n.GET("/:id/capabilities", func(c *gin.Context) {
		id := c.Param("id")
		node, err := findNodeByID(db, id)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "node not found"})
			return
		}
		buses := getDefaultESP32C6Buses()
		var capList []string
		if node.Capabilities != "" && node.Capabilities != "{}" {
			var parsed map[string]interface{}
			if json.Unmarshal([]byte(node.Capabilities), &parsed) == nil {
				if b, ok := parsed["buses"]; ok {
					buses = b.(map[string]interface{})
				}
			}
		}
		for k := range buses {
			capList = append(capList, k)
		}
		sort.Strings(capList)
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": gin.H{
			"capabilities": capList,
			"buses":        buses,
		}})
	})

	// GET /api/v1/nodes/:id/hardware/config
	n.GET("/:id/hardware/config", func(c *gin.Context) {
		id := c.Param("id")
		node, err := findNodeByID(db, id)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "node not found"})
			return
		}
		hardware := gin.H{
			"wifi_ssid": node.WiFiSSID,
			"wifi_rssi": node.WiFiRSSI,
			"free_heap": node.FreeHeapBytes,
		}
		if node.HardwareInfo != "" && node.HardwareInfo != "{}" {
			var parsed map[string]interface{}
			if json.Unmarshal([]byte(node.HardwareInfo), &parsed) == nil {
				if b, ok := parsed["buses"]; ok {
					hardware["buses"] = b
				}
			}
		}
		if hardware["buses"] == nil {
			hardware["buses"] = gin.H{}
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": gin.H{
			"hardware": hardware,
		}})
	})

	// PUT /api/v1/nodes/:id/hardware/config
	n.PUT("/:id/hardware/config", func(c *gin.Context) {
		id := c.Param("id")
		node, err := findNodeByID(db, id)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "node not found"})
			return
		}
		var req struct {
			Hardware map[string]interface{} `json:"hardware"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "invalid request body"})
			return
		}
		// Store buses from request into hardware_info
		if buses, ok := req.Hardware["buses"]; ok {
			hwJSON, _ := json.Marshal(map[string]interface{}{"buses": buses})
			node.HardwareInfo = string(hwJSON)
			db.Model(node).Update("hardware_info", node.HardwareInfo)
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": gin.H{
			"node_id": node.NodeID,
			"status":  "updated",
		}})
	})

	// POST /api/v1/nodes/:id/query-resources
	n.POST("/:id/query-resources", func(c *gin.Context) {
		id := c.Param("id")
		node, err := findNodeByID(db, id)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "node not found"})
			return
		}
		deviceID := node.NodeID
		requestID, err := nodeMgr.SendQueryResources(deviceID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": gin.H{
			"node_id":    id,
			"request_id": requestID,
			"status":     "sent",
		}})
	})

	// GET /api/v1/nodes/:id/dma-channels — get DMA channel info for a node
	// Merges device-reported state with config intent (node.Config.dma_configs).
	// If config says enabled but device reports disabled, use config intent state.
	n.GET("/:id/dma-channels", func(c *gin.Context) {
		id := c.Param("id")
		node, err := findNodeByID(db, id)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "node not found"})
			return
		}

		var channels []models.DmaChannelInfo
		if node.DmaChannels != "" && node.DmaChannels != "[]" {
			if err := json.Unmarshal([]byte(node.DmaChannels), &channels); err != nil {
				logger.Warnf("[%s] Failed to parse dma_channels JSONB: %v", node.NodeID, err)
			}
		}

		// Merge config intent: if dma_configs says enabled=true but device state=2(disabled),
		// override state to 0(free) — config has been pushed but device hasn't reported back yet.
		var configDmas []models.DmaChannelConfig
		if node.Config != "" {
			var cfg map[string]interface{}
			if err := json.Unmarshal([]byte(node.Config), &cfg); err == nil {
				if dc, ok := cfg["dma_configs"]; ok {
					if dcJSON, err := json.Marshal(dc); err == nil {
						json.Unmarshal(dcJSON, &configDmas)
					}
				}
			}
		}
		if len(configDmas) > 0 {
			configMap := make(map[uint32]bool)
			for _, cd := range configDmas {
				configMap[cd.DmaID] = cd.Enabled
			}
			for i := range channels {
				if enabled, ok := configMap[channels[i].DmaID]; ok {
					if enabled && channels[i].State == 2 {
						channels[i].State = 0 // config says enabled, device hasn't caught up
					} else if !enabled && channels[i].State != 2 {
						channels[i].State = 2 // config says disabled
					}
				}
			}
		}

		c.JSON(http.StatusOK, gin.H{"code": 200, "data": gin.H{
			"dma_channels": channels,
		}})
	})

	// PUT /api/v1/nodes/:id/dma-config — update DMA configuration for a node
	// Merges new configs by dma_id (does not overwrite unrelated channels).
	// Uses SELECT FOR UPDATE to prevent read-modify-write race conditions.
	n.PUT("/:id/dma-config", func(c *gin.Context) {
		id := c.Param("id")

		var configs []models.DmaChannelConfig
		if err := c.ShouldBindJSON(&configs); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": err.Error()})
			return
		}

		// Input validation: dma_id, duplicate check, bind_to length
		seen := make(map[uint32]bool)
		for i, cfg := range configs {

			if seen[cfg.DmaID] {
				c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": fmt.Sprintf("duplicate dma_id %d", cfg.DmaID)})
				return
			}
			seen[cfg.DmaID] = true
			if len(cfg.BindTo) > 16 {
				c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": fmt.Sprintf("configs[%d].bind_to exceeds 16 characters", i)})
				return
			}
		}

		// Use transaction with SELECT FOR UPDATE to prevent concurrent modification
		tx := db.Begin()
		var node models.Node
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("node_id = ?", id).First(&node).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "node not found"})
			return
		}

		// Parse existing node.Config JSON
		var cfg map[string]interface{}
		if node.Config != "" && node.Config != "{}" {
			if err := json.Unmarshal([]byte(node.Config), &cfg); err != nil {
				logger.Warnf("[%s] Failed to parse node.Config JSON: %v", node.NodeID, err)
			}
		}
		if cfg == nil {
			cfg = map[string]interface{}{}
		}

		// Load existing DMA configs for merge
		var existingConfigs []models.DmaChannelConfig
		if dc, ok := cfg["dma_configs"]; ok {
			if dcJSON, err := json.Marshal(dc); err == nil {
				if err := json.Unmarshal(dcJSON, &existingConfigs); err != nil {
					logger.Warnf("[%s] Failed to parse existing dma_configs: %v", node.NodeID, err)
				}
			}
		}

		// Merge by dma_id: existing configs as base, overlay new ones
		configMap := make(map[uint32]models.DmaChannelConfig)
		for _, ec := range existingConfigs {
			configMap[ec.DmaID] = ec
		}
		for _, nc := range configs {
			configMap[nc.DmaID] = nc
		}
		merged := make([]models.DmaChannelConfig, 0, len(configMap))
		for _, v := range configMap {
			merged = append(merged, v)
		}
		// Sort by dma_id for deterministic output (avoids spurious config sync)
		sort.Slice(merged, func(i, j int) bool { return merged[i].DmaID < merged[j].DmaID })

		cfg["dma_configs"] = merged
		cfgJSON, err := json.Marshal(cfg)
		if err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "failed to marshal config"})
			return
		}
		if err := tx.Model(&node).Update("config", string(cfgJSON)).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}

		// v2.5: Immediately update DmaChannels JSONB so GET /dma-channels
		// returns the correct state without waiting for next ResourceReport.
		var devChannels []models.DmaChannelInfo
		if node.DmaChannels != "" && node.DmaChannels != "[]" {
			json.Unmarshal([]byte(node.DmaChannels), &devChannels)
		}
		for _, nc := range configs {
			found := false
			for j := range devChannels {
				if devChannels[j].DmaID == nc.DmaID {
					devChannels[j].BoundTo = nc.BindTo
					if nc.Enabled {
						devChannels[j].State = 1 // allocated
					} else {
						devChannels[j].State = 2 // disabled
					}
					found = true
					break
				}
			}
			if !found {
				state := uint8(2)
				if nc.Enabled {
					state = 1
				}
				devChannels = append(devChannels, models.DmaChannelInfo{
					DmaID:   nc.DmaID,
					State:   state,
					BoundTo: nc.BindTo,
				})
			}
		}
		dmaJSON, err := json.Marshal(devChannels)
		if err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "failed to marshal dma_channels"})
			return
		}
		if err := tx.Model(&node).Update("dma_channels", string(dmaJSON)).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}

		tx.Commit()

		// Trigger config sync to push updated manifest to device
		nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeNode, nodemgr.CfgActionUpdate, node.NodeID, node.NodeID)

		c.JSON(http.StatusOK, gin.H{"code": 200, "data": gin.H{
			"node_id": node.NodeID,
			"status":  "sent",
		}})
	})
}

// edgeDeviceConfigItem is a lightweight EdgeDevice representation for config API responses.
// It omits nested associations (Node, Channel, DeviceConfig) to keep the response clean.
type edgeDeviceConfigItem struct {
	Type           string     `json:"type"`
	ParserID       string     `json:"parser_id"`
	ID             uint       `json:"id"`
	Name           string     `json:"name"`
	NodeID         string     `json:"node_id"`
	ChannelID      uint       `json:"channel_id"`
	DeviceConfigID uint       `json:"device_config_id"`
	HardwareID     string     `json:"hardware_id"`
	IntervalMs     int        `json:"interval_ms"`
	Enabled        bool       `json:"enabled"`
	Status         string     `json:"status"`
	ErrorCode      int        `json:"error_code"`
	LastDataAt     *time.Time `json:"last_data_at"`
	LastError      string     `json:"last_error"`
	ConfigVersion  string     `json:"config_version"`
	InitState      string     `json:"init_state"`
	InitLastStep   int        `json:"init_last_step"`
	InitTotalSteps int        `json:"init_total_steps"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

func edgeDeviceToConfigItem(ed models.EdgeDevice) edgeDeviceConfigItem {
	return edgeDeviceConfigItem{
		Type:           ed.Type,
		ParserID:       ed.ParserID,
		ID:             ed.ID,
		Name:           ed.Name,
		NodeID:         ed.NodeID,
		ChannelID:      ed.ChannelID,
		DeviceConfigID: ed.DeviceConfigID,
		HardwareID:     ed.HardwareID,
		IntervalMs:     ed.IntervalMs,
		Enabled:        ed.Enabled,
		Status:         ed.Status,
		ErrorCode:      ed.ErrorCode,
		LastDataAt:     ed.LastDataAt,
		LastError:      ed.LastError,
		ConfigVersion:  ed.ConfigVersion,
		InitState:      ed.InitState,
		InitLastStep:   ed.InitLastStep,
		InitTotalSteps: ed.InitTotalSteps,
		CreatedAt:      ed.CreatedAt,
		UpdatedAt:      ed.UpdatedAt,
	}
}

// nodeConfigResponse is the response structure for GET /nodes/:id/config
type nodeConfigResponse struct {
	Node            models.Node            `json:"node"`
	Channels        []models.Channel       `json:"channels"`
	EdgeDevices     []edgeDeviceConfigItem `json:"edge_devices"`
	DeviceConfigs   []models.DeviceConfig  `json:"device_configs"`
	Epoch           uint64                 `json:"epoch"`
	ProtocolVersion string                 `json:"protocol_version"`
}

// getNodeConfig returns the full configuration manifest for a node.
func getNodeConfig(db *gorm.DB, nodeMgr *nodemgr.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")

		var node models.Node
		if err := db.First(&node, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "node not found"})
			return
		}

		// Load channels for this node
		var channels []models.Channel
		db.Where("node_id = ?", node.NodeID).Find(&channels)

		// Load edge devices for this node
		var edgeDevices []models.EdgeDevice
		db.Where("node_id = ?", node.NodeID).Find(&edgeDevices)

		// Collect device_config IDs from edge devices
		deviceConfigIDs := make([]uint, 0, len(edgeDevices))
		for _, ed := range edgeDevices {
			if ed.DeviceConfigID > 0 {
				deviceConfigIDs = append(deviceConfigIDs, ed.DeviceConfigID)
			}
		}

		// Load device configs
		var deviceConfigs []models.DeviceConfig
		if len(deviceConfigIDs) > 0 {
			db.Where("id IN ?", deviceConfigIDs).Find(&deviceConfigs)
		}

		// Convert edge devices to clean response items (no nested associations)
		edgeDeviceItems := make([]edgeDeviceConfigItem, 0, len(edgeDevices))
		for _, ed := range edgeDevices {
			edgeDeviceItems = append(edgeDeviceItems, edgeDeviceToConfigItem(ed))
		}

		// Get current epoch
		epoch := nodeMgr.EventBus().CurrentEpoch()

		protocolVersion := node.ProtocolVersion
		if protocolVersion == "" {
			protocolVersion = "2.2"
		}

		c.JSON(http.StatusOK, gin.H{
			"code": 200,
			"data": nodeConfigResponse{
				Node:            node,
				Channels:        channels,
				EdgeDevices:     edgeDeviceItems,
				DeviceConfigs:   deviceConfigs,
				Epoch:           epoch,
				ProtocolVersion: protocolVersion,
			},
			"message": "ok",
		})
	}
}

// nodeConfigUpdateRequest is the request body for PUT /nodes/:id/config
// Only non-nil fields will be updated (partial update).
type nodeConfigUpdateRequest struct {
	Channels    *[]channelUpdateItem    `json:"channels,omitempty"`
	EdgeDevices *[]edgeDeviceUpdateItem `json:"edge_devices,omitempty"`
}

type channelUpdateItem struct {
	ID          uint   `json:"id"`
	Address     string `json:"address,omitempty"` // maps to HardwareID hex string
	HardwareID  string `json:"hardware_id,omitempty"`
	IntervalMs  *int   `json:"interval_ms,omitempty"`
	BusType     string `json:"bus_type,omitempty"`
	BusConfig   string `json:"bus_config,omitempty"`
	Config      string `json:"config,omitempty"`
	TemplateIDs string `json:"template_ids,omitempty"`
	Enabled     *bool  `json:"enabled,omitempty"`
}

type edgeDeviceUpdateItem struct {
	ID             uint   `json:"id"`
	Name           string `json:"name,omitempty"`
	ChannelID      *uint  `json:"channel_id,omitempty"`
	DeviceConfigID *uint  `json:"device_config_id,omitempty"`
	HardwareID     string `json:"hardware_id,omitempty"`
	IntervalMs     *int   `json:"interval_ms,omitempty"`
	Enabled        *bool  `json:"enabled,omitempty"`
}

// updateNodeConfig handles incremental (partial) config updates for a node.
// BUG-04 fix: idempotent — if the effective config content is unchanged,
// skip epoch increment and MQTT push, return 200 immediately.
func updateNodeConfig(db *gorm.DB, nodeMgr *nodemgr.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")

		var node models.Node
		if err := db.First(&node, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "node not found"})
			return
		}

		var req nodeConfigUpdateRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": err.Error()})
			return
		}

		var updatedFields []string

		// Update channels if provided
		if req.Channels != nil {
			for i, ch := range *req.Channels {
				if ch.ID == 0 {
					continue
				}
				// Verify channel belongs to this node
				var existing models.Channel
				if err := db.Where("id = ? AND node_id = ?", ch.ID, node.NodeID).First(&existing).Error; err != nil {
					continue // skip channels not belonging to this node
				}

				updates := map[string]interface{}{}

				// Handle "address" field (hex string like "0x77" → HardwareID string)
				if ch.Address != "" {
					updates["hardware_id"] = ch.Address
					updatedFields = append(updatedFields, fmt.Sprintf("channels.%d.address", i))
				}
				if ch.HardwareID != "" {
					updates["hardware_id"] = ch.HardwareID
					updatedFields = append(updatedFields, fmt.Sprintf("channels.%d.hardware_id", i))
				}
				if ch.IntervalMs != nil {
					updates["interval_ms"] = *ch.IntervalMs
					updatedFields = append(updatedFields, fmt.Sprintf("channels.%d.interval_ms", i))
				}
				if ch.BusType != "" {
					updates["bus_type"] = ch.BusType
					updatedFields = append(updatedFields, fmt.Sprintf("channels.%d.bus_type", i))
				}
				if ch.BusConfig != "" {
					updates["bus_config"] = ch.BusConfig
					updatedFields = append(updatedFields, fmt.Sprintf("channels.%d.bus_config", i))
				}
				if ch.Config != "" {
					updates["config"] = ch.Config
					updatedFields = append(updatedFields, fmt.Sprintf("channels.%d.config", i))
				}
				if ch.TemplateIDs != "" {
					updates["template_ids"] = ch.TemplateIDs
					updatedFields = append(updatedFields, fmt.Sprintf("channels.%d.template_ids", i))
				}
				if ch.Enabled != nil {
					updates["enabled"] = *ch.Enabled
					updatedFields = append(updatedFields, fmt.Sprintf("channels.%d.enabled", i))
				}

				if len(updates) > 0 {
					result := db.Model(&models.Channel{}).Where("id = ?", ch.ID).Updates(updates)
					if result.Error != nil {
						logger.Warnf("Failed to update channel id=%d: %v", ch.ID, result.Error)
					}
				}
			}
		}

		// Update edge devices if provided
		if req.EdgeDevices != nil {
			for i, ed := range *req.EdgeDevices {
				if ed.ID == 0 {
					continue
				}
				// Verify edge device belongs to this node
				var existing models.EdgeDevice
				if err := db.Where("id = ? AND node_id = ?", ed.ID, node.NodeID).First(&existing).Error; err != nil {
					continue
				}

				updates := map[string]interface{}{}

				if ed.Name != "" {
					updates["name"] = ed.Name
					updatedFields = append(updatedFields, fmt.Sprintf("edge_devices.%d.name", i))
				}
				if ed.ChannelID != nil {
					updates["channel_id"] = *ed.ChannelID
					updatedFields = append(updatedFields, fmt.Sprintf("edge_devices.%d.channel_id", i))
				}
				if ed.DeviceConfigID != nil {
					updates["device_config_id"] = *ed.DeviceConfigID
					updatedFields = append(updatedFields, fmt.Sprintf("edge_devices.%d.device_config_id", i))
				}
				if ed.HardwareID != "" {
					updates["hardware_id"] = ed.HardwareID
					updatedFields = append(updatedFields, fmt.Sprintf("edge_devices.%d.hardware_id", i))
				}
				if ed.IntervalMs != nil {
					updates["interval_ms"] = *ed.IntervalMs
					updatedFields = append(updatedFields, fmt.Sprintf("edge_devices.%d.interval_ms", i))
				}
				if ed.Enabled != nil {
					updates["enabled"] = *ed.Enabled
					updatedFields = append(updatedFields, fmt.Sprintf("edge_devices.%d.enabled", i))
				}

				if len(updates) > 0 {
					result := db.Model(&models.EdgeDevice{}).Where("id = ?", ed.ID).Updates(updates)
					if result.Error != nil {
						logger.Warnf("Failed to update edge_device id=%d: %v", ed.ID, result.Error)
					}
				}
			}
		}

		// --- BUG-04: Idempotency check (M7: simplified) ---
		// Only check if updatedFields is non-empty; skip DB snapshot comparison
		// to avoid race conditions. Worst case: extra config push, which is safe.
		configChanged := len(updatedFields) > 0

		if configChanged {
			// Increment epoch via EventBus (Publish increments epoch)
			nodemgr.EmitConfigChange(
				c,
				nodeMgr.EventBus(),
				nodemgr.CfgChangeNode,
				nodemgr.CfgActionUpdate,
				node.NodeID,
				node.NodeID,
			)
		}

		// Get the new epoch after increment (or current if unchanged)
		newEpoch := nodeMgr.EventBus().CurrentEpoch()

		// If node is online, SyncGate will automatically push config
		// (EmitConfigChange publishes to the bus, SyncGate consumes and pushes)
		// No need to manually call PushConfig here.

		c.JSON(http.StatusOK, gin.H{
			"code": 200,
			"data": gin.H{
				"epoch":          newEpoch,
				"updated_fields": updatedFields,
			},
			"message": "config updated",
		})
	}
}

// --- BUG-04 helper functions for idempotency ---

// getNodeOTAHistory returns OTA task history for a specific node.
// BUG-08 fix: This route was missing, causing 404 on the node detail page.
func getNodeOTAHistory(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")

		var node models.Node
		if err := db.First(&node, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "node not found"})
			return
		}

		var tasks []models.OTATask
		db.Where("collector_id = ?", node.NodeID).Order("created_at DESC").Find(&tasks)

		c.JSON(http.StatusOK, gin.H{
			"code": 200,
			"data": tasks,
		})
	}
}
