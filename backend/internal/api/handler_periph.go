package api

import (
	"encoding/binary"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"ehome/backend/internal/models"
	"ehome/backend/internal/nodemgr"
	"ehome/backend/pkg/logger"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Peripheral control constants (v3.0)
//
// PeriphCmd field 2: periph_type
const (
	PeriphTypeGPIO uint8 = 1
	PeriphTypePWM  uint8 = 2
)

// GPIO action enum (PeriphCmd field 4)
const (
	GPIOActionSetLow   uint8 = 0
	GPIOActionSetHigh  uint8 = 1
	GPIOActionRead     uint8 = 2
	GPIOActionConfig   uint8 = 3
	GPIOActionDeconfig uint8 = 4
	GPIOActionToggle   uint8 = 5
)

// PWM action enum (PeriphCmd field 4)
const (
	PWMActionSetDuty       uint8 = 0
	PWMActionSetFreq       uint8 = 1
	PWMActionStart         uint8 = 2
	PWMActionStop          uint8 = 3
	PWMActionRead          uint8 = 4
	PWMActionSetResolution uint8 = 5
)

// GPIO direction encoding: 0=INPUT, 1=OUTPUT, 2=INPUT_PULLUP, 3=INPUT_PULLDOWN
const (
	GPIODirInput       uint8 = 0
	GPIODirOutput      uint8 = 1
	GPIODirInputPullUp uint8 = 2
	GPIODirInputPullDn uint8 = 3
)

// registerPeriphRoutes sets up GPIO + PWM peripheral control routes.
// Uses :id parameter name (consistent with existing /nodes/:id routes).
func registerPeriphRoutes(v1 *gin.RouterGroup, db *gorm.DB, nodeMgr *nodemgr.Manager) {
	eventBus := nodeMgr.EventBus()
	n := v1.Group("/nodes")

	// ================================================================
	// GPIO API
	// ================================================================

	// GET /api/v1/nodes/:id/gpio — list all GPIO configs for a node
	n.GET("/:id/gpio", func(c *gin.Context) {
		id := c.Param("id")
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		var configs []models.GPIOConfig
		db.Where("node_id = ?", node.NodeID).Order("pin ASC").Find(&configs)
		Success(c, configs)
	})

	// POST /api/v1/nodes/:id/gpio — configure a GPIO pin
	n.POST("/:id/gpio", func(c *gin.Context) {
		id := c.Param("id")
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		var req struct {
			Pin          int    `json:"pin"`
			Direction    uint8  `json:"direction"`
			InitialLevel uint8  `json:"initial_level"`
			Label        string `json:"label"`
			Enabled      *bool  `json:"enabled"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		if req.Pin < 0 {
			Error(c, http.StatusBadRequest, "pin is required")
			return
		}
		if req.Direction > GPIODirInputPullDn {
			Error(c, http.StatusBadRequest, "invalid direction (0-3)")
			return
		}
		cfg := models.GPIOConfig{
			NodeID:       node.NodeID,
			Pin:          req.Pin,
			Direction:    req.Direction,
			InitialLevel: req.InitialLevel,
			Label:        req.Label,
			Enabled:      true,
		}
		if req.Enabled != nil {
			cfg.Enabled = *req.Enabled
		}
		if err := db.Create(&cfg).Error; err != nil {
			if strings.Contains(err.Error(), "UNIQUE") || strings.Contains(err.Error(), "duplicate") {
				Error(c, http.StatusConflict, fmt.Sprintf("GPIO pin %d already configured for this node", req.Pin))
				return
			}
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeGPIO, nodemgr.CfgActionCreate, node.NodeID, fmt.Sprint(cfg.ID))

		// Send CONFIG command to device (best-effort)
		configBytes := []byte{req.Direction, req.InitialLevel}
		if err := nodeMgr.SendPeriphCmd(node.NodeID, PeriphTypeGPIO, uint8(req.Pin), GPIOActionConfig, 0, configBytes); err != nil {
			logger.Warnf("[%s] Failed to send GPIO CONFIG: %v", node.NodeID, err)
		}

		c.JSON(http.StatusCreated, cfg)
	})

	// PUT /api/v1/nodes/:id/gpio/:pin — update GPIO config
	n.PUT("/:id/gpio/:pin", func(c *gin.Context) {
		id := c.Param("id")
		pin, _ := strconv.Atoi(c.Param("pin"))
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		var cfg models.GPIOConfig
		if err := db.Where("node_id = ? AND pin = ?", node.NodeID, pin).First(&cfg).Error; err != nil {
			Error(c, http.StatusNotFound, "GPIO config not found")
			return
		}
		var req struct {
			Direction    *uint8 `json:"direction"`
			InitialLevel *uint8 `json:"initial_level"`
			Label        *string `json:"label"`
			Enabled      *bool   `json:"enabled"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		updates := map[string]interface{}{}
		if req.Direction != nil {
			if *req.Direction > GPIODirInputPullDn {
				Error(c, http.StatusBadRequest, "invalid direction (0-3)")
				return
			}
			updates["direction"] = *req.Direction
		}
		if req.InitialLevel != nil {
			updates["initial_level"] = *req.InitialLevel
		}
		if req.Label != nil {
			updates["label"] = *req.Label
		}
		if req.Enabled != nil {
			updates["enabled"] = *req.Enabled
		}
		if len(updates) > 0 {
			if err := db.Model(&cfg).Updates(updates).Error; err != nil {
				Error(c, http.StatusInternalServerError, err.Error())
				return
			}
		}
		db.First(&cfg, cfg.ID)
		nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeGPIO, nodemgr.CfgActionUpdate, node.NodeID, fmt.Sprint(cfg.ID))

		// Re-send CONFIG to device with updated values (best-effort)
		configBytes := []byte{cfg.Direction, cfg.InitialLevel}
		if err := nodeMgr.SendPeriphCmd(node.NodeID, PeriphTypeGPIO, uint8(cfg.Pin), GPIOActionConfig, 0, configBytes); err != nil {
			logger.Warnf("[%s] Failed to send GPIO CONFIG update: %v", node.NodeID, err)
		}

		c.JSON(http.StatusOK, cfg)
	})

	// DELETE /api/v1/nodes/:id/gpio/:pin — deconfigure GPIO pin
	n.DELETE("/:id/gpio/:pin", func(c *gin.Context) {
		id := c.Param("id")
		pin, _ := strconv.Atoi(c.Param("pin"))
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		var cfg models.GPIOConfig
		if err := db.Where("node_id = ? AND pin = ?", node.NodeID, pin).First(&cfg).Error; err != nil {
			Error(c, http.StatusNotFound, "GPIO config not found")
			return
		}
		if err := db.Delete(&cfg).Error; err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeGPIO, nodemgr.CfgActionDelete, node.NodeID, fmt.Sprint(cfg.ID))

		// Send DECONFIG to device (best-effort)
		if err := nodeMgr.SendPeriphCmd(node.NodeID, PeriphTypeGPIO, uint8(pin), GPIOActionDeconfig, 0, nil); err != nil {
			logger.Warnf("[%s] Failed to send GPIO DECONFIG: %v", node.NodeID, err)
		}

		c.JSON(http.StatusOK, gin.H{"message": "deleted"})
	})

	// POST /api/v1/nodes/:id/gpio/:pin/set — set output level {level: 0|1}
	n.POST("/:id/gpio/:pin/set", func(c *gin.Context) {
		id := c.Param("id")
		pin, _ := strconv.Atoi(c.Param("pin"))
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		var req struct {
			Level  uint8 `json:"level"`
			Toggle bool  `json:"toggle"`
		}
		c.ShouldBindJSON(&req)

		var action uint8
		var value uint32
		if req.Toggle {
			action = GPIOActionToggle
		} else if req.Level == 0 {
			action = GPIOActionSetLow
		} else {
			action = GPIOActionSetHigh
		}

		if err := nodeMgr.SendPeriphCmd(node.NodeID, PeriphTypeGPIO, uint8(pin), action, value, nil); err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "command sent", "action": action})
	})

	// POST /api/v1/nodes/:id/gpio/:pin/read — read pin level
	n.POST("/:id/gpio/:pin/read", func(c *gin.Context) {
		id := c.Param("id")
		pin, _ := strconv.Atoi(c.Param("pin"))
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		if err := nodeMgr.SendPeriphCmd(node.NodeID, PeriphTypeGPIO, uint8(pin), GPIOActionRead, 0, nil); err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "read command sent"})
	})

	// ================================================================
	// PWM API
	// ================================================================

	// GET /api/v1/nodes/:id/pwm — list all PWM configs for a node
	n.GET("/:id/pwm", func(c *gin.Context) {
		id := c.Param("id")
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		var configs []models.PWMConfig
		db.Where("node_id = ?", node.NodeID).Order("pin ASC").Find(&configs)
		Success(c, configs)
	})

	// POST /api/v1/nodes/:id/pwm — configure a PWM pin
	n.POST("/:id/pwm", func(c *gin.Context) {
		id := c.Param("id")
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		var req struct {
			Pin        int    `json:"pin"`
			Frequency  uint32 `json:"frequency"`
			Duty       uint16 `json:"duty"`
			Resolution uint8  `json:"resolution"`
			AutoStart  bool   `json:"auto_start"`
			Label      string `json:"label"`
			Enabled    *bool  `json:"enabled"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		if req.Pin < 0 {
			Error(c, http.StatusBadRequest, "pin is required")
			return
		}
		if req.Resolution == 0 {
			req.Resolution = 14 // default
		}
		if req.Resolution < 4 || req.Resolution > 20 {
			Error(c, http.StatusBadRequest, "resolution must be 4-20")
			return
		}
		if req.Duty > 10000 {
			Error(c, http.StatusBadRequest, "duty must be 0-10000")
			return
		}
		cfg := models.PWMConfig{
			NodeID:     node.NodeID,
			Pin:        req.Pin,
			Frequency:  req.Frequency,
			Duty:       req.Duty,
			Resolution: req.Resolution,
			AutoStart:  req.AutoStart,
			Label:      req.Label,
			Enabled:    true,
		}
		if req.Enabled != nil {
			cfg.Enabled = *req.Enabled
		}
		if err := db.Create(&cfg).Error; err != nil {
			if strings.Contains(err.Error(), "UNIQUE") || strings.Contains(err.Error(), "duplicate") {
				Error(c, http.StatusConflict, fmt.Sprintf("PWM pin %d already configured for this node", req.Pin))
				return
			}
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangePWM, nodemgr.CfgActionCreate, node.NodeID, fmt.Sprint(cfg.ID))
		c.JSON(http.StatusCreated, cfg)
	})

	// PUT /api/v1/nodes/:id/pwm/:pin — update PWM config
	n.PUT("/:id/pwm/:pin", func(c *gin.Context) {
		id := c.Param("id")
		pin, _ := strconv.Atoi(c.Param("pin"))
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		var cfg models.PWMConfig
		if err := db.Where("node_id = ? AND pin = ?", node.NodeID, pin).First(&cfg).Error; err != nil {
			Error(c, http.StatusNotFound, "PWM config not found")
			return
		}
		var req struct {
			Frequency  *uint32 `json:"frequency"`
			Duty       *uint16 `json:"duty"`
			Resolution *uint8  `json:"resolution"`
			AutoStart  *bool   `json:"auto_start"`
			Label      *string `json:"label"`
			Enabled    *bool   `json:"enabled"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		updates := map[string]interface{}{}
		if req.Frequency != nil {
			updates["frequency"] = *req.Frequency
		}
		if req.Duty != nil {
			if *req.Duty > 10000 {
				Error(c, http.StatusBadRequest, "duty must be 0-10000")
				return
			}
			updates["duty"] = *req.Duty
		}
		if req.Resolution != nil {
			if *req.Resolution < 4 || *req.Resolution > 20 {
				Error(c, http.StatusBadRequest, "resolution must be 4-20")
				return
			}
			updates["resolution"] = *req.Resolution
		}
		if req.AutoStart != nil {
			updates["auto_start"] = *req.AutoStart
		}
		if req.Label != nil {
			updates["label"] = *req.Label
		}
		if req.Enabled != nil {
			updates["enabled"] = *req.Enabled
		}
		if len(updates) > 0 {
			if err := db.Model(&cfg).Updates(updates).Error; err != nil {
				Error(c, http.StatusInternalServerError, err.Error())
				return
			}
		}
		db.First(&cfg, cfg.ID)
		nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangePWM, nodemgr.CfgActionUpdate, node.NodeID, fmt.Sprint(cfg.ID))
		c.JSON(http.StatusOK, cfg)
	})

	// DELETE /api/v1/nodes/:id/pwm/:pin — deconfigure PWM pin
	n.DELETE("/:id/pwm/:pin", func(c *gin.Context) {
		id := c.Param("id")
		pin, _ := strconv.Atoi(c.Param("pin"))
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		var cfg models.PWMConfig
		if err := db.Where("node_id = ? AND pin = ?", node.NodeID, pin).First(&cfg).Error; err != nil {
			Error(c, http.StatusNotFound, "PWM config not found")
			return
		}
		// Send STOP to device (best-effort) before deleting
		if err := nodeMgr.SendPeriphCmd(node.NodeID, PeriphTypePWM, uint8(pin), PWMActionStop, 0, nil); err != nil {
			logger.Warnf("[%s] Failed to send PWM STOP before delete: %v", node.NodeID, err)
		}
		if err := db.Delete(&cfg).Error; err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangePWM, nodemgr.CfgActionDelete, node.NodeID, fmt.Sprint(cfg.ID))
		c.JSON(http.StatusOK, gin.H{"message": "deleted"})
	})

	// POST /api/v1/nodes/:id/pwm/:pin/start — start PWM output
	n.POST("/:id/pwm/:pin/start", func(c *gin.Context) {
		id := c.Param("id")
		pin, _ := strconv.Atoi(c.Param("pin"))
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		var cfg models.PWMConfig
		if err := db.Where("node_id = ? AND pin = ?", node.NodeID, pin).First(&cfg).Error; err != nil {
			Error(c, http.StatusNotFound, "PWM config not found")
			return
		}
		// Build PWM START config: [freq:4B LE][duty:2B LE][resolution:1B]
		configBytes := make([]byte, 7)
		binary.LittleEndian.PutUint32(configBytes[0:4], cfg.Frequency)
		binary.LittleEndian.PutUint16(configBytes[4:6], cfg.Duty)
		configBytes[6] = cfg.Resolution

		if err := nodeMgr.SendPeriphCmd(node.NodeID, PeriphTypePWM, uint8(pin), PWMActionStart, 0, configBytes); err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "start command sent"})
	})

	// POST /api/v1/nodes/:id/pwm/:pin/stop — stop PWM output
	n.POST("/:id/pwm/:pin/stop", func(c *gin.Context) {
		id := c.Param("id")
		pin, _ := strconv.Atoi(c.Param("pin"))
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		if err := nodeMgr.SendPeriphCmd(node.NodeID, PeriphTypePWM, uint8(pin), PWMActionStop, 0, nil); err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "stop command sent"})
	})

	// POST /api/v1/nodes/:id/pwm/:pin/duty — set duty cycle {duty: 0-10000}
	n.POST("/:id/pwm/:pin/duty", func(c *gin.Context) {
		id := c.Param("id")
		pin, _ := strconv.Atoi(c.Param("pin"))
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		var req struct {
			Duty uint16 `json:"duty" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		if req.Duty > 10000 {
			Error(c, http.StatusBadRequest, "duty must be 0-10000")
			return
		}
		if err := nodeMgr.SendPeriphCmd(node.NodeID, PeriphTypePWM, uint8(pin), PWMActionSetDuty, uint32(req.Duty), nil); err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		// Update DB config to reflect latest duty
		db.Model(&models.PWMConfig{}).Where("node_id = ? AND pin = ?", node.NodeID, pin).Update("duty", req.Duty)
		c.JSON(http.StatusOK, gin.H{"message": "duty command sent", "duty": req.Duty})
	})

	// POST /api/v1/nodes/:id/pwm/:pin/freq — set frequency {frequency: Hz}
	n.POST("/:id/pwm/:pin/freq", func(c *gin.Context) {
		id := c.Param("id")
		pin, _ := strconv.Atoi(c.Param("pin"))
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		var req struct {
			Frequency uint32 `json:"frequency" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		// Get current config for resolution
		var cfg models.PWMConfig
		var resolution uint8 = 14
		if err := db.Where("node_id = ? AND pin = ?", node.NodeID, pin).First(&cfg).Error; err == nil {
			resolution = cfg.Resolution
		}
		// SET_FREQ config: [resolution:1B]
		configBytes := []byte{resolution}

		if err := nodeMgr.SendPeriphCmd(node.NodeID, PeriphTypePWM, uint8(pin), PWMActionSetFreq, req.Frequency, configBytes); err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		// Update DB config
		db.Model(&models.PWMConfig{}).Where("node_id = ? AND pin = ?", node.NodeID, pin).Update("frequency", req.Frequency)
		c.JSON(http.StatusOK, gin.H{"message": "frequency command sent", "frequency": req.Frequency})
	})

	// GET /api/v1/nodes/:id/pwm/:pin/state — read current PWM state (duty)
	n.GET("/:id/pwm/:pin/state", func(c *gin.Context) {
		id := c.Param("id")
		pin, _ := strconv.Atoi(c.Param("pin"))
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		var cfg models.PWMConfig
		if err := db.Where("node_id = ? AND pin = ?", node.NodeID, pin).First(&cfg).Error; err != nil {
			Error(c, http.StatusNotFound, "PWM config not found")
			return
		}
		// Send READ command to device (best-effort) — result comes back via PeriphRsp WebSocket event
		if err := nodeMgr.SendPeriphCmd(node.NodeID, PeriphTypePWM, uint8(pin), PWMActionRead, 0, nil); err != nil {
			logger.Warnf("[%s] Failed to send PWM READ: %v", node.NodeID, err)
		}
		c.JSON(http.StatusOK, gin.H{
			"pin":        cfg.Pin,
			"frequency":  cfg.Frequency,
			"duty":       cfg.Duty,
			"resolution": cfg.Resolution,
			"auto_start": cfg.AutoStart,
			"enabled":    cfg.Enabled,
		})
	})
}
