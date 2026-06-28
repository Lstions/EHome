package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
	"ehome/backend/internal/nodemgr"
	"ehome/backend/pkg/logger"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// registerDriverCommandRoutes adds endpoints for per-command frequency management.
func registerDriverCommandRoutes(v1 *gin.RouterGroup, db *gorm.DB, nodeMgr *nodemgr.Manager) {
	eventBus := nodeMgr.EventBus()

	// GET /api/v1/drivers/:type/commands — list commands for a device type
	v1.GET("/drivers/:type/commands", func(c *gin.Context) {
		driverType := c.Param("type")

		drv, err := drivers.Get(driverType)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "driver not found: " + driverType})
			return
		}

		cmds := getCommandTemplates(drv)
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": cmds})
	})

	// GET /api/v1/edge-devices/:id/commands — get current command intervals for an edge device
	v1.GET("/edge-devices/:id/commands", func(c *gin.Context) {
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "invalid id"})
			return
		}

		var dev models.EdgeDevice
		if err := db.First(&dev, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "edge device not found"})
			return
		}

		drv, err := drivers.Get(dev.Type)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "driver not found for device type: " + dev.Type})
			return
		}

		templates := getCommandTemplates(drv)

		// Overlay stored intervals from edge device
		storedIntervals := parseCommandIntervals(dev.CommandIntervals)
		type commandView struct {
			drivers.CommandTemplate
			CurrentIntervalMs int `json:"current_interval_ms"`
		}
		result := make([]commandView, len(templates))
		for i, t := range templates {
			interval := t.IntervalMs
			// Priority: stored CommandIntervals > edge_device.IntervalMs > template default
			if v, ok := storedIntervals[t.ID]; ok {
				interval = v
			} else if len(storedIntervals) == 0 && dev.IntervalMs > 0 {
				// No per-command intervals stored → use edge device's effective interval
				interval = dev.IntervalMs
			}
			result[i] = commandView{CommandTemplate: t, CurrentIntervalMs: interval}
		}

		c.JSON(http.StatusOK, gin.H{"code": 200, "data": result})
	})

	// PUT /api/v1/edge-devices/:id/commands — update command intervals and trigger config sync
	v1.PUT("/edge-devices/:id/commands", RequireRole("admin"), func(c *gin.Context) {
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "invalid id"})
			return
		}

		var req struct {
			Intervals map[string]int `json:"intervals"` // command_id → interval_ms (0=disabled)
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": err.Error()})
			return
		}

		if len(req.Intervals) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "intervals is required"})
			return
		}

		var dev models.EdgeDevice
		if err := db.First(&dev, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "edge device not found"})
			return
		}

		// Merge with existing intervals
		existing := parseCommandIntervals(dev.CommandIntervals)
		for cmdID, interval := range req.Intervals {
			if interval < 0 {
				interval = 0
			}
			existing[cmdID] = interval
		}

		intervalsJSON, err := json.Marshal(existing)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "failed to marshal intervals"})
			return
		}

		db.Model(&dev).Update("command_intervals", intervalsJSON)

		// Trigger config sync to push new intervals to ESP32
		var ch models.Channel
		if db.First(&ch, dev.ChannelID).Error == nil {
			nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeEdgeDevice,
				nodemgr.CfgActionUpdate, ch.NodeID, strconv.Itoa(id))
		}

		logger.Infof("[commands] Updated intervals for edge_device=%d type=%s: %v", dev.ID, dev.Type, existing)
		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": gin.H{"command_intervals": existing}})
	})
}

// getCommandTemplates returns templates from a driver, or empty slice.
func getCommandTemplates(drv drivers.Driver) []drivers.CommandTemplate {
	if provider, ok := drv.(drivers.CommandTemplateProvider); ok {
		return provider.GetCommandTemplates()
	}
	return nil
}

// parseCommandIntervals parses the JSONB map of command_id → interval_ms.
func parseCommandIntervals(raw json.RawMessage) map[string]int {
	if len(raw) == 0 {
		return make(map[string]int)
	}
	var m map[string]int
	if err := json.Unmarshal(raw, &m); err != nil {
		return make(map[string]int)
	}
	return m
}
