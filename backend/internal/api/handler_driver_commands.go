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
func registerDriverCommandRoutes(v1 *gin.RouterGroup, db *gorm.DB, nodeMgr *nodemgr.Manager, registries ...*drivers.Registry) {
	driverRegistry := resolveDriverRegistry(registries...)
	eventBus := nodeMgr.EventBus()

	// GET /api/v1/drivers/:type/commands — list commands for a device type
	v1.GET("/drivers/:type/commands", func(c *gin.Context) {
		driverType := c.Param("type")

		drv, err := driverRegistry.Get(driverType)
		if err != nil {
			Error(c, http.StatusNotFound, "driver not found: "+driverType)
			return
		}

		cmds := getCommandTemplates(drv)
		// I-2: only schedulable polling templates are returned; one-shot
		// commands belong to the Action Catalog (DeviceControlPanel).
		Success(c, filterSchedulableTemplates(cmds))
	})

	// GET /api/v1/edge-devices/:id/commands — get current command intervals for an edge device
	v1.GET("/edge-devices/:id/commands", func(c *gin.Context) {
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil {
			Error(c, http.StatusBadRequest, "invalid id")
			return
		}

		var dev models.EdgeDevice
		if err := db.First(&dev, id).Error; err != nil {
			Error(c, http.StatusNotFound, "edge device not found")
			return
		}

		drv, err := driverRegistry.Get(dev.Type)
		if err != nil {
			Error(c, http.StatusNotFound, "driver not found for device type: "+dev.Type)
			return
		}

		templates := filterSchedulableTemplates(getCommandTemplates(drv))

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

		Success(c, result)
	})

	// PUT /api/v1/edge-devices/:id/commands — update command intervals and trigger config sync
	v1.PUT("/edge-devices/:id/commands", func(c *gin.Context) {
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil {
			Error(c, http.StatusBadRequest, "invalid id")
			return
		}

		var req struct {
			Intervals map[string]int `json:"intervals"` // command_id → interval_ms (0=disabled)
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}

		if len(req.Intervals) == 0 {
			Error(c, http.StatusBadRequest, "intervals is required")
			return
		}

		var dev models.EdgeDevice
		if err := db.First(&dev, id).Error; err != nil {
			Error(c, http.StatusNotFound, "edge device not found")
			return
		}

		// Merge with existing intervals (partial update: request keys overlay
		// the stored map).
		existing := parseCommandIntervals(dev.CommandIntervals)
		for cmdID, interval := range req.Intervals {
			existing[cmdID] = interval
		}

		// I-3 (演进方案 §3.1): validate the *merged* map against the driver's
		// schedulable set — the same helper as the create path. Unknown ids
		// and non-schedulable ids are rejected with 400 before any DB write.
		// Validation runs on the merged map (not the request subset) because
		// PUT is a partial update; legacy dirty keys are removed by the C2
		// cleanup script before this gate goes live.
		if err := ValidateCommandIntervals(driverRegistry, dev.Type, existing); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		existing = NormalizeCommandIntervals(existing)

		intervalsJSON, err := json.Marshal(existing)
		if err != nil {
			Error(c, http.StatusInternalServerError, "failed to marshal intervals")
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
		Success(c, gin.H{"command_intervals": existing})
	})
}

// getCommandTemplates returns templates from a driver, or empty slice.
func getCommandTemplates(drv drivers.Driver) []drivers.CommandTemplate {
	if provider, ok := drv.(drivers.CommandTemplateProvider); ok {
		return provider.GetCommandTemplates()
	}
	return nil
}

// filterSchedulableTemplates returns only Schedulable templates, preserving
// order. I-2: GET endpoints return schedulable templates only; the frontend's
// client-side filter (CommandList.vue:131, CreateWizardCommandIntervals.vue:109)
// stays as defense in depth.
func filterSchedulableTemplates(templates []drivers.CommandTemplate) []drivers.CommandTemplate {
	filtered := make([]drivers.CommandTemplate, 0, len(templates))
	for _, t := range templates {
		if t.Schedulable {
			filtered = append(filtered, t)
		}
	}
	return filtered
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
