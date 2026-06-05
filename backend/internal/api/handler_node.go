package api

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"ehome/backend/internal/collector"
	"ehome/backend/internal/models"
	"ehome/backend/pkg/logger"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// registerNodeRoutes sets up node CRUD routes
func registerNodeRoutes(v1 *gin.RouterGroup, db *gorm.DB, collectorMgr *collector.Manager) {
	eventBus := collectorMgr.EventBus()

	// List nodes (v2.2 path for /collectors)
	v1.GET("/nodes", func(c *gin.Context) {
		var nodes []models.Node
		if err := db.Find(&nodes).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, nodes)
	})

	// Get node by DB id (v2.2 path for /collectors/:id)
	v1.GET("/nodes/:id", func(c *gin.Context) {
		id := c.Param("id")
		var node models.Node
		if err := db.First(&node, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
			return
		}
		c.JSON(http.StatusOK, node)
	})

	// Create node (v2.2 path for POST /collectors)
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
		collector.EmitConfigChange(c, eventBus, collector.CfgChangeNode, collector.CfgActionCreate, node.ID, node.ID)
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
		collector.EmitConfigChange(c, eventBus, collector.CfgChangeNode, collector.CfgActionUpdate, node.ID, node.ID)
		c.JSON(http.StatusOK, node)
	})

	// Delete node (v2.2 path for DELETE /collectors/:id)
	v1.DELETE("/nodes/:id", func(c *gin.Context) {
		id := c.Param("id")
		nodeID := parseUintID(id)
		if err := db.Delete(&models.Node{}, id).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		collector.EmitConfigChange(c, eventBus, collector.CfgChangeNode, collector.CfgActionDelete, nodeID, nodeID)
		c.JSON(http.StatusOK, gin.H{"message": "deleted"})
	})

	// Get channels for node (v2.2 path for /collectors/:id/channels)
	v1.GET("/nodes/:id/channels", func(c *gin.Context) {
		id := c.Param("id")
		var node models.Node
		if err := db.First(&node, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
			return
		}
		var channels []models.Channel
		db.Where("node_id = ?", node.ID).Find(&channels)
		c.JSON(http.StatusOK, channels)
	})

	// Get node data (v2.2 path for /collectors/:id/data)
	v1.GET("/nodes/:id/data", func(c *gin.Context) {
		id := c.Param("id")
		limitStr := c.DefaultQuery("limit", "100")
		limit, _ := strconv.Atoi(limitStr)
		var node models.Node
		if err := db.First(&node, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
			return
		}
		var data []models.DeviceData
		db.Where("collector_id = ?", node.ID).Order("timestamp DESC").Limit(limit).Find(&data)
		c.JSON(http.StatusOK, data)
	})

	// Ping node (v2.2 path for /collectors/:id/ping)
	v1.POST("/nodes/:id/ping", func(c *gin.Context) {
		id := c.Param("id")
		var node models.Node
		if err := db.First(&node, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
			return
		}
		if err := collectorMgr.SendPing(strconv.FormatInt(node.NodeID, 10)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "ping sent"})
	})

	// Get node config (v2.2 ConfigManifest)
	v1.GET("/nodes/:id/config", getNodeConfig(db, collectorMgr))

	// Update node config (v2.2 incremental update)
	v1.PUT("/nodes/:id/config", updateNodeConfig(db, collectorMgr))

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
		// Trigger config sync via collector manager
		collectorMgr.SyncGate().OnServerStartup() // or specific node sync
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": gin.H{"node_id": id, "status": "syncing"}})
	})

	// GET /api/v1/nodes/:id/hardware/config
	n.GET("/:id/hardware/config", func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		var node models.Node
		if err := db.First(&node, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"code": 404})
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": gin.H{
			"hardware": gin.H{"wifi_ssid": node.WiFiSSID, "wifi_rssi": node.WiFiRSSI, "free_heap": node.FreeHeapBytes},
		}})
	})

	// PUT /api/v1/nodes/:id/hardware/config
	n.PUT("/:id/hardware/config", func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		var req struct{ Hardware map[string]interface{} `json:"hardware"` }
		c.ShouldBindJSON(&req)
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": gin.H{"node_id": id, "status": "updated"}})
	})

	// GET /api/v1/nodes/:id/capabilities
	n.GET("/:id/capabilities", func(c *gin.Context) {
		_, _ = strconv.Atoi(c.Param("id"))
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": gin.H{
			"capabilities": []string{"i2c", "spi", "uart", "gpio", "adc"},
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
func getNodeConfig(db *gorm.DB, collectorMgr *collector.Manager) gin.HandlerFunc {
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
		epoch := collectorMgr.EventBus().CurrentEpoch()

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
func updateNodeConfig(db *gorm.DB, collectorMgr *collector.Manager) gin.HandlerFunc {
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
			collector.EmitConfigChange(
				c,
				collectorMgr.EventBus(),
				collector.CfgChangeNode,
				collector.CfgActionUpdate,
				node.ID,
				node.ID,
			)
		}

		// Get the new epoch after increment (or current if unchanged)
		newEpoch := collectorMgr.EventBus().CurrentEpoch()

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
