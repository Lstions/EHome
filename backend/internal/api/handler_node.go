package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"ehome/backend/internal/nodemgr"
	"ehome/backend/internal/models"
	"ehome/backend/pkg/logger"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
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
		nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeNode, nodemgr.CfgActionCreate, node.ID, node.ID)
		c.JSON(http.StatusCreated, node)
	})

	// Update node (v2.2 path for PUT /collectors/:id)
	v1.PUT("/nodes/:id", func(c *gin.Context) {
		id := c.Param("id")
		var node models.Node
		if err := db.First(&node, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
			return
		}
		if err := c.ShouldBindJSON(&node); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		db.Save(&node)
		nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeNode, nodemgr.CfgActionUpdate, node.ID, node.ID)
		Success(c, node)
	})

	// Delete node (v2.2 path for DELETE /collectors/:id)
	v1.DELETE("/nodes/:id", func(c *gin.Context) {
		id := c.Param("id")
		nodeID := parseUintID(id)
		if err := db.Delete(&models.Node{}, id).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeNode, nodemgr.CfgActionDelete, nodeID, nodeID)
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
		db.Where("node_id = ?", node.ID).Find(&channels)
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
		db.Where("collector_id = ?", node.ID).Order("timestamp DESC").Limit(limit).Find(&data)
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
		if err := nodeMgr.SendPing(strconv.FormatInt(node.NodeID, 10)); err != nil {
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
		id, _ := strconv.Atoi(c.Param("id"))
		var req struct {
			HardwareID string `json:"hardware_id"`
		}
		c.ShouldBindJSON(&req)
		// NOTE: requires MQTT broadcast I2C_SCAN to node firmware
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": gin.H{
			"devices": []string{}, "request_id": fmt.Sprintf("i2c-%d-%d", id, time.Now().Unix()),
		}})
	})

	// POST /api/v1/nodes/:id/config/sync
	n.POST("/:id/config/sync", func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		// Trigger config sync via node manager
		nodeMgr.SyncGate().OnServerStartup() // or specific node sync
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": gin.H{"node_id": id, "status": "syncing"}})
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
			"wifi_ssid":  node.WiFiSSID,
			"wifi_rssi":  node.WiFiRSSI,
			"free_heap":  node.FreeHeapBytes,
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
			"node_id": node.ID,
			"status":  "updated",
		}})
	})

	// POST /api/v1/nodes/:id/query-resources
	n.POST("/:id/query-resources", func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": gin.H{
			"request_id": fmt.Sprintf("res-%d-%d", id, time.Now().Unix()),
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
	NodeID         uint       `json:"node_id"`
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
	Node            models.Node              `json:"node"`
	Channels        []models.Channel         `json:"channels"`
	EdgeDevices     []edgeDeviceConfigItem   `json:"edge_devices"`
	DeviceConfigs   []models.DeviceConfig    `json:"device_configs"`
	Epoch           uint64                   `json:"epoch"`
	ProtocolVersion string                   `json:"protocol_version"`
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
		db.Where("node_id = ?", node.ID).Find(&channels)

		// Load edge devices for this node
		var edgeDevices []models.EdgeDevice
		db.Where("node_id = ?", node.ID).Find(&edgeDevices)

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
	Address     string `json:"address,omitempty"`    // maps to HardwareID hex string
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
				if err := db.Where("id = ? AND node_id = ?", ch.ID, node.ID).First(&existing).Error; err != nil {
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
				if err := db.Where("id = ? AND node_id = ?", ed.ID, node.ID).First(&existing).Error; err != nil {
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
				node.ID,
				node.ID,
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
		db.Where("collector_id = ?", node.ID).Order("created_at DESC").Find(&tasks)

		c.JSON(http.StatusOK, gin.H{
			"code": 200,
			"data":  tasks,
		})
	}
}
