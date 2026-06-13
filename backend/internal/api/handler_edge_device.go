package api

import (
	"errors"
	"net/http"
	"strconv"

	"ehome/backend/internal/nodemgr"
	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// registerEdgeDeviceRoutes sets up edge-device CRUD routes
func registerEdgeDeviceRoutes(v1 *gin.RouterGroup, db *gorm.DB, nodeMgr *nodemgr.Manager) {
	eventBus := nodeMgr.EventBus()

	// List edge devices (v2.2 path for /devices)
	v1.GET("/edge-devices", func(c *gin.Context) {
		var devices []models.EdgeDevice
		db.Find(&devices)
		Success(c, devices)
	})

	// Get single edge device by id (v2.2 path for /devices/:id)
	v1.GET("/edge-devices/:id", func(c *gin.Context) {
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
		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": d})
	})

	// Create edge device (v2.2 path for POST /devices)
	v1.POST("/edge-devices", func(c *gin.Context) {
		var dev models.EdgeDevice
		if err := c.ShouldBindJSON(&dev); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := db.Create(&dev).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		// EmitConfigChange: find the node via channel
		var ch models.Channel
		if db.First(&ch, dev.ChannelID).Error == nil {
			nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeEdgeDevice, nodemgr.CfgActionCreate, ch.NodeID, dev.ID)
		}
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
		if err := c.ShouldBindJSON(&d); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": err.Error()})
			return
		}
		d.ID = parseUintID(id)
		if err := db.Save(&d).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}
		// EmitConfigChange: find the node via channel
		var ch models.Channel
		if db.First(&ch, d.ChannelID).Error == nil {
			nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeEdgeDevice, nodemgr.CfgActionUpdate, ch.NodeID, d.ID)
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

		// Resolve the MQTT device ID from the associated Node
		var node models.Node
		if err := db.First(&node, dev.NodeID).Error; err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "associated node not found"})
			return
		}
		deviceID := strconv.FormatInt(node.NodeID, 10)

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
			nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeEdgeDevice, nodemgr.CfgActionDelete, ch.NodeID, d.ID)
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
}
