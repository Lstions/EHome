package api

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"ehome/backend/internal/models"
	"ehome/backend/internal/nodemgr"
	"ehome/backend/pkg/logger"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type reportedPWMResource struct {
	ID                string `json:"id"`
	Channel           uint8  `json:"channel"`
	MaxResolutionBits uint8  `json:"max_resolution_bits"`
}

type reportedGPIOResource struct {
	Pin int `json:"pin"`
}

type reportedUARTResource struct {
	DefaultTxPin int `json:"default_tx_pin"`
	DefaultRxPin int `json:"default_rx_pin"`
}

type reportedI2CResource struct {
	DefaultSdaPin int `json:"default_sda_pin"`
	DefaultSclPin int `json:"default_scl_pin"`
}

type reportedSPIResource struct {
	DefaultMosiPin int `json:"default_mosi_pin"`
	DefaultMisoPin int `json:"default_miso_pin"`
	DefaultSclkPin int `json:"default_sclk_pin"`
	DefaultCsPin   int `json:"default_cs_pin"`
}

type reportedPeripheralResources struct {
	Buses struct {
		PWM  []reportedPWMResource  `json:"pwm"`
		GPIO []reportedGPIOResource `json:"gpio"`
		UART []reportedUARTResource `json:"uart"`
		I2C  []reportedI2CResource  `json:"i2c"`
		SPI  []reportedSPIResource  `json:"spi"`
	} `json:"buses"`
}

func validateEnabledChannelPin(db *gorm.DB, nodeID string, pin int) error {
	var channels []models.Channel
	if err := db.Where("node_id = ? AND enabled = ?", nodeID, true).Find(&channels).Error; err != nil {
		return fmt.Errorf("load enabled channels: %w", err)
	}
	for _, ch := range channels {
		busType := strings.ToUpper(strings.TrimSpace(ch.BusType))
		if busType == "GPIO" || busType == "4" || busType == "PWM" || busType == "6" {
			return fmt.Errorf("legacy peripheral channel %d is still enabled", ch.ID)
		}
		if busType == "ADC" {
			continue
		}
		raw := strings.TrimPrefix(strings.TrimSpace(ch.BusConfig), `\x`)
		cfg, err := hex.DecodeString(raw)
		if err != nil {
			return fmt.Errorf("enabled channel %d has malformed bus_config", ch.ID)
		}
		uses := false
		switch busType {
		case "UART", "I2C":
			if len(cfg) < 2 {
				return fmt.Errorf("enabled channel %d has malformed bus_config", ch.ID)
			}
			uses = int(cfg[0]) == pin || int(cfg[1]) == pin
		case "SPI":
			if len(cfg) != 9 {
				return fmt.Errorf("enabled channel %d has malformed bus_config", ch.ID)
			}
			uses = int(cfg[0]) == pin
			if len(cfg) >= 9 {
				uses = uses || int(cfg[6]) == pin || int(cfg[7]) == pin || int(cfg[8]) == pin
			}
		default:
			return fmt.Errorf("enabled channel %d has unsupported bus type %q", ch.ID, ch.BusType)
		}
		if uses {
			return fmt.Errorf("GPIO pin %d conflicts with enabled channel %d", pin, ch.ID)
		}
	}
	return nil
}

func resolveReportedPWMResources(db *gorm.DB, node *models.Node, hardwareID string, pin int) (reportedPWMResource, error) {
	var resources reportedPeripheralResources
	if node.Capabilities == "" || json.Unmarshal([]byte(node.Capabilities), &resources) != nil {
		return reportedPWMResource{}, fmt.Errorf("node has not reported usable hardware resources")
	}
	var pwm *reportedPWMResource
	for i := range resources.Buses.PWM {
		if resources.Buses.PWM[i].ID == hardwareID {
			pwm = &resources.Buses.PWM[i]
			break
		}
	}
	if pwm == nil {
		return reportedPWMResource{}, fmt.Errorf("PWM resource %q was not reported by node", hardwareID)
	}
	for _, gpio := range resources.Buses.GPIO {
		if gpio.Pin == pin {
			if err := validateEnabledChannelPin(db, node.NodeID, pin); err != nil {
				return reportedPWMResource{}, err
			}
			return *pwm, nil
		}
	}
	return reportedPWMResource{}, fmt.Errorf("GPIO pin %d was not reported by node", pin)
}

func validateCurrentPWMConfig(db *gorm.DB, node *models.Node, cfg *models.PWMConfig) (reportedPWMResource, error) {
	resource, err := resolveReportedPWMResources(db, node, cfg.HardwareID, cfg.Pin)
	if err != nil {
		return reportedPWMResource{}, err
	}
	if resource.Channel != cfg.Channel {
		return reportedPWMResource{}, fmt.Errorf("PWM resource %q channel no longer matches current report", cfg.HardwareID)
	}
	return resource, nil
}

func reportedBusPinConflict(resources *reportedPeripheralResources, pin int) bool {
	for _, bus := range resources.Buses.UART {
		if bus.DefaultTxPin == pin || bus.DefaultRxPin == pin {
			return true
		}
	}
	for _, bus := range resources.Buses.I2C {
		if bus.DefaultSdaPin == pin || bus.DefaultSclPin == pin {
			return true
		}
	}
	for _, bus := range resources.Buses.SPI {
		if bus.DefaultMosiPin == pin || bus.DefaultMisoPin == pin || bus.DefaultSclkPin == pin || bus.DefaultCsPin == pin {
			return true
		}
	}
	return false
}

func validateReportedGPIO(db *gorm.DB, node *models.Node, pin int) error {
	var resources reportedPeripheralResources
	if node.Capabilities == "" || json.Unmarshal([]byte(node.Capabilities), &resources) != nil || len(resources.Buses.GPIO) == 0 {
		return fmt.Errorf("node has not reported usable GPIO resources")
	}
	for _, gpio := range resources.Buses.GPIO {
		if gpio.Pin == pin {
			return validateEnabledChannelPin(db, node.NodeID, pin)
		}
	}
	return fmt.Errorf("GPIO pin %d was not reported by node", pin)
}

func validatePWMFrequency(frequency uint32, resolution uint8) error {
	if frequency == 0 {
		return fmt.Errorf("frequency must be nonzero")
	}
	if resolution < 4 || resolution > 20 || uint64(frequency)*(uint64(1)<<resolution) > 40000000 {
		return fmt.Errorf("frequency and resolution exceed PWM controller capability")
	}
	return nil
}

func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique") || strings.Contains(message, "duplicate")
}

var errPeripheralPinConflict = errors.New("peripheral pin conflict")

func createGPIOConfigWithPinExclusion(db *gorm.DB, nodeID string, cfg *models.GPIOConfig) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var node models.Node
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("node_id = ?", nodeID).First(&node).Error; err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&models.PWMConfig{}).Where("node_id = ? AND pin = ?", nodeID, cfg.Pin).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return errPeripheralPinConflict
		}
		if err := validateEnabledChannelPin(tx, nodeID, cfg.Pin); err != nil {
			return errPeripheralPinConflict
		}
		desiredEnabled := cfg.Enabled
		if err := tx.Create(cfg).Error; err != nil {
			return err
		}
		if !desiredEnabled {
			if err := tx.Model(cfg).Update("enabled", false).Error; err != nil {
				return err
			}
			cfg.Enabled = false
		}
		return nil
	})
}

func createPWMConfigWithPinExclusion(db *gorm.DB, nodeID string, cfg *models.PWMConfig) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var node models.Node
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("node_id = ?", nodeID).First(&node).Error; err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&models.GPIOConfig{}).Where("node_id = ? AND pin = ?", nodeID, cfg.Pin).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return errPeripheralPinConflict
		}
		if err := validateEnabledChannelPin(tx, nodeID, cfg.Pin); err != nil {
			return errPeripheralPinConflict
		}
		desiredEnabled := cfg.Enabled
		if err := tx.Create(cfg).Error; err != nil {
			return err
		}
		if !desiredEnabled {
			if err := tx.Model(cfg).Update("enabled", false).Error; err != nil {
				return err
			}
			cfg.Enabled = false
		}
		return nil
	})
}

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
			Pin          *int   `json:"pin"`
			Direction    uint8  `json:"direction"`
			InitialLevel uint8  `json:"initial_level"`
			Label        string `json:"label"`
			Enabled      *bool  `json:"enabled"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		if req.Pin == nil || *req.Pin < 0 {
			Error(c, http.StatusBadRequest, "pin is required")
			return
		}
		pin := *req.Pin
		if req.Direction > GPIODirInputPullDn {
			Error(c, http.StatusBadRequest, "invalid direction (0-3)")
			return
		}
		if req.InitialLevel > 1 {
			Error(c, http.StatusBadRequest, "initial_level must be 0 or 1")
			return
		}
		if err := validateReportedGPIO(db, node, pin); err != nil {
			Error(c, http.StatusUnprocessableEntity, err.Error())
			return
		}
		var pwmConflict int64
		if err := db.Model(&models.PWMConfig{}).Where("node_id = ? AND pin = ?", node.NodeID, pin).Count(&pwmConflict).Error; err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		if pwmConflict > 0 {
			Error(c, http.StatusConflict, fmt.Sprintf("pin %d is already used by PWM", pin))
			return
		}
		cfg := models.GPIOConfig{
			NodeID:       node.NodeID,
			Pin:          pin,
			Direction:    req.Direction,
			InitialLevel: req.InitialLevel,
			Label:        req.Label,
			Enabled:      true,
		}
		if req.Enabled != nil {
			cfg.Enabled = *req.Enabled
		}
		if err := createGPIOConfigWithPinExclusion(db, node.NodeID, &cfg); err != nil {
			if errors.Is(err, errPeripheralPinConflict) {
				Error(c, http.StatusConflict, fmt.Sprintf("pin %d is already used by PWM", pin))
				return
			}
			if isUniqueConstraintError(err) {
				Error(c, http.StatusConflict, fmt.Sprintf("GPIO pin %d already configured for this node", pin))
				return
			}
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeGPIO, nodemgr.CfgActionCreate, node.NodeID, fmt.Sprint(cfg.ID))

		if cfg.Enabled {
			configBytes := []byte{req.Direction, req.InitialLevel}
			if err := nodeMgr.SendPeriphCmd(node.NodeID, PeriphTypeGPIO, uint8(pin), GPIOActionConfig, 0, configBytes); err != nil {
				logger.Warnf("[%s] Failed to send GPIO CONFIG: %v", node.NodeID, err)
			}
		}

		c.JSON(http.StatusCreated, cfg)
	})

	// PUT /api/v1/nodes/:id/gpio/:pin — update GPIO config
	n.PUT("/:id/gpio/:pin", func(c *gin.Context) {
		id := c.Param("id")
		pin, parseErr := strconv.Atoi(c.Param("pin"))
		if parseErr != nil || pin < 0 || pin > 255 {
			Error(c, http.StatusBadRequest, "invalid GPIO pin")
			return
		}
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
			Direction    *uint8  `json:"direction"`
			InitialLevel *uint8  `json:"initial_level"`
			Label        *string `json:"label"`
			Enabled      *bool   `json:"enabled"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		if err := validateReportedGPIO(db, node, pin); err != nil {
			Error(c, http.StatusUnprocessableEntity, err.Error())
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
			if *req.InitialLevel > 1 {
				Error(c, http.StatusBadRequest, "initial_level must be 0 or 1")
				return
			}
			updates["initial_level"] = *req.InitialLevel
		}
		if req.Label != nil {
			updates["label"] = *req.Label
		}
		if req.Enabled != nil {
			updates["enabled"] = *req.Enabled
		}
		if len(updates) > 0 {
			if err := db.Transaction(func(tx *gorm.DB) error {
				var lockedNode models.Node
				if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("node_id = ?", node.NodeID).First(&lockedNode).Error; err != nil {
					return err
				}
				var candidate models.GPIOConfig
				if err := tx.Where("id = ?", cfg.ID).First(&candidate).Error; err != nil {
					return err
				}
				if req.Enabled != nil {
					candidate.Enabled = *req.Enabled
				}
				if candidate.Enabled {
					if err := validateEnabledChannelPin(tx, node.NodeID, candidate.Pin); err != nil {
						return errPeripheralPinConflict
					}
					var pwmCount int64
					if err := tx.Model(&models.PWMConfig{}).Where("node_id = ? AND pin = ?", node.NodeID, candidate.Pin).Count(&pwmCount).Error; err != nil {
						return err
					}
					if pwmCount > 0 {
						return errPeripheralPinConflict
					}
				}
				return tx.Model(&cfg).Updates(updates).Error
			}); err != nil {
				Error(c, http.StatusInternalServerError, err.Error())
				return
			}
		}
		db.First(&cfg, cfg.ID)
		nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeGPIO, nodemgr.CfgActionUpdate, node.NodeID, fmt.Sprint(cfg.ID))

		action := GPIOActionDeconfig
		var configBytes []byte
		if cfg.Enabled {
			action = GPIOActionConfig
			configBytes = []byte{cfg.Direction, cfg.InitialLevel}
		}
		if err := nodeMgr.SendPeriphCmd(node.NodeID, PeriphTypeGPIO, uint8(cfg.Pin), action, 0, configBytes); err != nil {
			logger.Warnf("[%s] Failed to apply GPIO enabled state: %v", node.NodeID, err)
		}

		c.JSON(http.StatusOK, cfg)
	})

	// DELETE /api/v1/nodes/:id/gpio/:pin — deconfigure GPIO pin
	n.DELETE("/:id/gpio/:pin", func(c *gin.Context) {
		id := c.Param("id")
		pin, parseErr := strconv.Atoi(c.Param("pin"))
		if parseErr != nil || pin < 0 || pin > 255 {
			Error(c, http.StatusBadRequest, "invalid GPIO pin")
			return
		}
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

		// Only an enabled, currently reported resource may need runtime teardown.
		if cfg.Enabled && validateReportedGPIO(db, node, pin) == nil {
			if err := nodeMgr.SendPeriphCmd(node.NodeID, PeriphTypeGPIO, uint8(pin), GPIOActionDeconfig, 0, nil); err != nil {
				logger.Warnf("[%s] Failed to send GPIO DECONFIG: %v", node.NodeID, err)
			}
		}

		c.JSON(http.StatusOK, gin.H{"message": "deleted"})
	})

	// POST /api/v1/nodes/:id/gpio/:pin/set — set output level {level: 0|1}
	n.POST("/:id/gpio/:pin/set", func(c *gin.Context) {
		id := c.Param("id")
		pin, parseErr := strconv.Atoi(c.Param("pin"))
		if parseErr != nil || pin < 0 || pin > 255 {
			Error(c, http.StatusBadRequest, "invalid GPIO pin")
			return
		}
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		if err := validateReportedGPIO(db, node, pin); err != nil {
			Error(c, http.StatusUnprocessableEntity, err.Error())
			return
		}
		var cfg models.GPIOConfig
		if err := db.Where("node_id = ? AND pin = ?", node.NodeID, pin).First(&cfg).Error; err != nil {
			Error(c, http.StatusNotFound, "GPIO config not found")
			return
		}
		if !cfg.Enabled {
			Error(c, http.StatusConflict, "GPIO config is disabled")
			return
		}
		var req struct {
			Level  *uint8 `json:"level"`
			Toggle bool   `json:"toggle"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}

		var action uint8
		var value uint32
		if req.Toggle {
			action = GPIOActionToggle
		} else if req.Level == nil || *req.Level > 1 {
			Error(c, http.StatusBadRequest, "level must be 0 or 1")
			return
		} else if *req.Level == 0 {
			action = GPIOActionSetLow
		} else {
			action = GPIOActionSetHigh
		}

		requestID, err := nodeMgr.SendPeriphCmdWithID(node.NodeID, PeriphTypeGPIO, uint8(pin), action, value, nil)
		if err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "command sent", "action": action, "request_id": requestID})
	})

	// POST /api/v1/nodes/:id/gpio/:pin/read — read pin level
	n.POST("/:id/gpio/:pin/read", func(c *gin.Context) {
		id := c.Param("id")
		pin, parseErr := strconv.Atoi(c.Param("pin"))
		if parseErr != nil || pin < 0 || pin > 255 {
			Error(c, http.StatusBadRequest, "invalid GPIO pin")
			return
		}
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		if err := validateReportedGPIO(db, node, pin); err != nil {
			Error(c, http.StatusUnprocessableEntity, err.Error())
			return
		}
		var cfg models.GPIOConfig
		if err := db.Where("node_id = ? AND pin = ?", node.NodeID, pin).First(&cfg).Error; err != nil {
			Error(c, http.StatusNotFound, "GPIO config not found")
			return
		}
		if !cfg.Enabled {
			Error(c, http.StatusConflict, "GPIO config is disabled")
			return
		}
		requestID, err := nodeMgr.SendPeriphCmdWithID(node.NodeID, PeriphTypeGPIO, uint8(pin), GPIOActionRead, 0, nil)
		if err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "read command sent", "request_id": requestID})
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
			HardwareID string `json:"hardware_id"`
			Pin        *int   `json:"pin"`
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
		if strings.TrimSpace(req.HardwareID) == "" {
			Error(c, http.StatusBadRequest, "hardware_id is required")
			return
		}
		if req.Pin == nil || *req.Pin < 0 {
			Error(c, http.StatusBadRequest, "pin is required")
			return
		}
		resource, err := resolveReportedPWMResources(db, node, req.HardwareID, *req.Pin)
		if err != nil {
			Error(c, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if req.Resolution == 0 {
			req.Resolution = 14 // default
		}
		if resource.MaxResolutionBits == 0 || req.Resolution < 4 || req.Resolution > 20 || req.Resolution > resource.MaxResolutionBits {
			Error(c, http.StatusBadRequest, "resolution exceeds reported PWM resource capability")
			return
		}
		if err := validatePWMFrequency(req.Frequency, req.Resolution); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		if req.Duty > 10000 {
			Error(c, http.StatusBadRequest, "duty must be 0-10000")
			return
		}
		var gpioConflict int64
		if err := db.Model(&models.GPIOConfig{}).Where("node_id = ? AND pin = ?", node.NodeID, *req.Pin).Count(&gpioConflict).Error; err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		if gpioConflict > 0 {
			Error(c, http.StatusConflict, fmt.Sprintf("pin %d is already used by GPIO", *req.Pin))
			return
		}
		cfg := models.PWMConfig{
			NodeID:     node.NodeID,
			HardwareID: resource.ID,
			Channel:    resource.Channel,
			Pin:        *req.Pin,
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
		if err := createPWMConfigWithPinExclusion(db, node.NodeID, &cfg); err != nil {
			if errors.Is(err, errPeripheralPinConflict) {
				Error(c, http.StatusConflict, fmt.Sprintf("pin %d is already used by GPIO", *req.Pin))
				return
			}
			if isUniqueConstraintError(err) {
				Error(c, http.StatusConflict, "PWM hardware resource or output pin already configured for this node")
				return
			}
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangePWM, nodemgr.CfgActionCreate, node.NodeID, fmt.Sprint(cfg.ID))
		c.JSON(http.StatusCreated, cfg)
	})

	// PUT /api/v1/nodes/:id/pwm/:hardware_id — update PWM config
	n.PUT("/:id/pwm/:hardware_id", func(c *gin.Context) {
		id := c.Param("id")
		hardwareID := c.Param("hardware_id")
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		var req struct {
			Pin        *int    `json:"pin"`
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
		var cfg models.PWMConfig
		if err := db.Where("node_id = ? AND hardware_id = ?", node.NodeID, hardwareID).First(&cfg).Error; err != nil {
			Error(c, http.StatusNotFound, "PWM config not found")
			return
		}
		resource, err := validateCurrentPWMConfig(db, node, &cfg)
		if err != nil {
			Error(c, http.StatusUnprocessableEntity, err.Error())
			return
		}
		updates := map[string]interface{}{}
		if req.Pin != nil {
			if *req.Pin < 0 {
				Error(c, http.StatusBadRequest, "pin must be non-negative")
				return
			}
			resource, err := resolveReportedPWMResources(db, node, cfg.HardwareID, *req.Pin)
			if err != nil || resource.Channel != cfg.Channel {
				Error(c, http.StatusUnprocessableEntity, "PWM hardware resource or GPIO pin was not reported by node")
				return
			}
			var conflicts int64
			if err := db.Model(&models.GPIOConfig{}).Where("node_id = ? AND pin = ?", node.NodeID, *req.Pin).Count(&conflicts).Error; err != nil {
				Error(c, http.StatusInternalServerError, err.Error())
				return
			}
			if conflicts > 0 {
				Error(c, http.StatusConflict, fmt.Sprintf("pin %d is already used by GPIO", *req.Pin))
				return
			}
			updates["pin"] = *req.Pin
		}
		frequency := cfg.Frequency
		resolution := cfg.Resolution
		if req.Frequency != nil {
			frequency = *req.Frequency
		}
		if req.Resolution != nil {
			resolution = *req.Resolution
		}
		if err := validatePWMFrequency(frequency, resolution); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
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
			if resource.MaxResolutionBits == 0 || *req.Resolution < 4 || *req.Resolution > 20 || *req.Resolution > resource.MaxResolutionBits {
				Error(c, http.StatusBadRequest, "resolution exceeds reported PWM resource capability")
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
			err := db.Transaction(func(tx *gorm.DB) error {
				var lockedNode models.Node
				if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("node_id = ?", node.NodeID).First(&lockedNode).Error; err != nil {
					return err
				}
				if req.Pin != nil {
					var count int64
					if err := tx.Model(&models.GPIOConfig{}).Where("node_id = ? AND pin = ?", node.NodeID, *req.Pin).Count(&count).Error; err != nil {
						return err
					}
					if count > 0 {
						return errPeripheralPinConflict
					}
				}
				candidatePin := cfg.Pin
				if req.Pin != nil {
					candidatePin = *req.Pin
				}
				if err := validateEnabledChannelPin(tx, node.NodeID, candidatePin); err != nil {
					return errPeripheralPinConflict
				}
				return tx.Model(&cfg).Updates(updates).Error
			})
			if err != nil {
				if errors.Is(err, errPeripheralPinConflict) {
					Error(c, http.StatusConflict, "PWM pin conflicts with an existing GPIO/transport route")
					return
				}
				if isUniqueConstraintError(err) {
					Error(c, http.StatusConflict, "PWM output pin already configured for this node")
					return
				}
				Error(c, http.StatusInternalServerError, err.Error())
				return
			}
		}
		db.First(&cfg, cfg.ID)
		nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangePWM, nodemgr.CfgActionUpdate, node.NodeID, fmt.Sprint(cfg.ID))
		if req.Enabled != nil && !cfg.Enabled {
			if err := nodeMgr.SendPeriphCmd(node.NodeID, PeriphTypePWM, cfg.Channel, PWMActionStop, 0, nil); err != nil {
				if restoreErr := db.Model(&cfg).Update("enabled", true).Error; restoreErr != nil {
					Error(c, http.StatusInternalServerError, fmt.Sprintf("stop failed: %v; restore failed: %v", err, restoreErr))
					return
				}
				Error(c, http.StatusInternalServerError, err.Error())
				return
			}
		}
		c.JSON(http.StatusOK, cfg)
	})

	// DELETE /api/v1/nodes/:id/pwm/:hardware_id — deconfigure PWM hardware resource
	n.DELETE("/:id/pwm/:hardware_id", func(c *gin.Context) {
		id := c.Param("id")
		hardwareID := c.Param("hardware_id")
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		var cfg models.PWMConfig
		if err := db.Where("node_id = ? AND hardware_id = ?", node.NodeID, hardwareID).First(&cfg).Error; err != nil {
			Error(c, http.StatusNotFound, "PWM config not found")
			return
		}
		_, currentErr := resolveReportedPWMResources(db, node, cfg.HardwareID, cfg.Pin)
		if cfg.Enabled && currentErr == nil {
			if err := nodeMgr.SendPeriphCmd(node.NodeID, PeriphTypePWM, cfg.Channel, PWMActionStop, 0, nil); err != nil {
				logger.Warnf("[%s] PWM STOP before authoritative cleanup failed: %v", node.NodeID, err)
			}
		}
		if err := db.Delete(&cfg).Error; err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangePWM, nodemgr.CfgActionDelete, node.NodeID, fmt.Sprint(cfg.ID))
		c.JSON(http.StatusOK, gin.H{"message": "deleted"})
	})

	// POST /api/v1/nodes/:id/pwm/:hardware_id/start — start PWM output
	n.POST("/:id/pwm/:hardware_id/start", func(c *gin.Context) {
		id := c.Param("id")
		hardwareID := c.Param("hardware_id")
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		var cfg models.PWMConfig
		if err := db.Where("node_id = ? AND hardware_id = ?", node.NodeID, hardwareID).First(&cfg).Error; err != nil {
			Error(c, http.StatusNotFound, "PWM config not found")
			return
		}
		if !cfg.Enabled {
			Error(c, http.StatusConflict, "PWM config is disabled")
			return
		}
		if _, err := validateCurrentPWMConfig(db, node, &cfg); err != nil {
			Error(c, http.StatusUnprocessableEntity, err.Error())
			return
		}
		// Build PWM START config: [pin:1B][freq:4B LE][duty:2B LE][resolution:1B]
		configBytes := make([]byte, 8)
		configBytes[0] = uint8(cfg.Pin)
		binary.LittleEndian.PutUint32(configBytes[1:5], cfg.Frequency)
		binary.LittleEndian.PutUint16(configBytes[5:7], cfg.Duty)
		configBytes[7] = cfg.Resolution

		requestID, err := nodeMgr.SendPeriphCmdWithID(node.NodeID, PeriphTypePWM, cfg.Channel, PWMActionStart, 0, configBytes)
		if err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "start command sent", "request_id": requestID})
	})

	// POST /api/v1/nodes/:id/pwm/:hardware_id/stop — stop PWM output
	n.POST("/:id/pwm/:hardware_id/stop", func(c *gin.Context) {
		id := c.Param("id")
		hardwareID := c.Param("hardware_id")
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		var cfg models.PWMConfig
		if err := db.Where("node_id = ? AND hardware_id = ?", node.NodeID, hardwareID).First(&cfg).Error; err != nil {
			Error(c, http.StatusNotFound, "PWM config not found")
			return
		}
		if !cfg.Enabled {
			Error(c, http.StatusConflict, "PWM config is disabled")
			return
		}
		if _, err := validateCurrentPWMConfig(db, node, &cfg); err != nil {
			Error(c, http.StatusUnprocessableEntity, err.Error())
			return
		}
		requestID, err := nodeMgr.SendPeriphCmdWithID(node.NodeID, PeriphTypePWM, cfg.Channel, PWMActionStop, 0, nil)
		if err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "stop command sent", "request_id": requestID})
	})

	// POST /api/v1/nodes/:id/pwm/:hardware_id/duty — set duty cycle {duty: 0-10000}
	n.POST("/:id/pwm/:hardware_id/duty", func(c *gin.Context) {
		nodeMgr.LockPeriphIntent()
		defer nodeMgr.UnlockPeriphIntent()
		id := c.Param("id")
		hardwareID := c.Param("hardware_id")
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		var req struct {
			Duty *uint16 `json:"duty" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || req.Duty == nil {
			if err == nil {
				err = fmt.Errorf("duty is required")
			}
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		if *req.Duty > 10000 {
			Error(c, http.StatusBadRequest, "duty must be 0-10000")
			return
		}
		var cfg models.PWMConfig
		if err := db.Where("node_id = ? AND hardware_id = ?", node.NodeID, hardwareID).First(&cfg).Error; err != nil {
			Error(c, http.StatusNotFound, "PWM config not found")
			return
		}
		if !cfg.Enabled {
			Error(c, http.StatusConflict, "PWM config is disabled")
			return
		}
		if _, err := validateCurrentPWMConfig(db, node, &cfg); err != nil {
			Error(c, http.StatusUnprocessableEntity, err.Error())
			return
		}
		oldDuty := cfg.Duty
		if err := db.Model(&cfg).Update("duty", *req.Duty).Error; err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		requestID, err := nodeMgr.SendPeriphCmdWithPreviousValue(node.NodeID, PeriphTypePWM, cfg.Channel, PWMActionSetDuty, uint32(*req.Duty), nil, uint32(oldDuty))
		if err != nil {
			if restoreErr := db.Model(&cfg).Update("duty", oldDuty).Error; restoreErr != nil {
				Error(c, http.StatusInternalServerError, fmt.Sprintf("send failed: %v; restore failed: %v", err, restoreErr))
				return
			}
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "duty command sent", "duty": *req.Duty, "request_id": requestID})
	})

	// POST /api/v1/nodes/:id/pwm/:hardware_id/freq — set frequency {frequency: Hz}
	n.POST("/:id/pwm/:hardware_id/freq", func(c *gin.Context) {
		nodeMgr.LockPeriphIntent()
		defer nodeMgr.UnlockPeriphIntent()
		id := c.Param("id")
		hardwareID := c.Param("hardware_id")
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
		var cfg models.PWMConfig
		if err := db.Where("node_id = ? AND hardware_id = ?", node.NodeID, hardwareID).First(&cfg).Error; err != nil {
			Error(c, http.StatusNotFound, "PWM config not found")
			return
		}
		if !cfg.Enabled {
			Error(c, http.StatusConflict, "PWM config is disabled")
			return
		}
		if _, err := validateCurrentPWMConfig(db, node, &cfg); err != nil {
			Error(c, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if err := validatePWMFrequency(req.Frequency, cfg.Resolution); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		// SET_FREQ config: [resolution:1B]
		configBytes := []byte{cfg.Resolution}

		oldFrequency := cfg.Frequency
		if err := db.Model(&cfg).Update("frequency", req.Frequency).Error; err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		requestID, err := nodeMgr.SendPeriphCmdWithPreviousValue(node.NodeID, PeriphTypePWM, cfg.Channel, PWMActionSetFreq, req.Frequency, configBytes, oldFrequency)
		if err != nil {
			if restoreErr := db.Model(&cfg).Update("frequency", oldFrequency).Error; restoreErr != nil {
				Error(c, http.StatusInternalServerError, fmt.Sprintf("send failed: %v; restore failed: %v", err, restoreErr))
				return
			}
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "frequency command sent", "frequency": req.Frequency, "request_id": requestID})
	})

	// GET /api/v1/nodes/:id/pwm/:hardware_id/state — read current PWM state (duty)
	n.GET("/:id/pwm/:hardware_id/state", func(c *gin.Context) {
		id := c.Param("id")
		hardwareID := c.Param("hardware_id")
		node, err := findNodeByID(db, id)
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		var cfg models.PWMConfig
		if err := db.Where("node_id = ? AND hardware_id = ?", node.NodeID, hardwareID).First(&cfg).Error; err != nil {
			Error(c, http.StatusNotFound, "PWM config not found")
			return
		}
		if !cfg.Enabled {
			Error(c, http.StatusConflict, "PWM config is disabled")
			return
		}
		if _, err := validateCurrentPWMConfig(db, node, &cfg); err != nil {
			Error(c, http.StatusUnprocessableEntity, err.Error())
			return
		}
		// Send READ command to device (best-effort) — result comes back via PeriphRsp WebSocket event
		requestID, err := nodeMgr.SendPeriphCmdWithID(node.NodeID, PeriphTypePWM, cfg.Channel, PWMActionRead, 0, nil)
		if err != nil {
			logger.Warnf("[%s] Failed to send PWM READ: %v", node.NodeID, err)
			c.JSON(http.StatusServiceUnavailable, gin.H{"message": "runtime state unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"hardware_id": cfg.HardwareID,
			"channel":     cfg.Channel,
			"pin":         cfg.Pin,
			"frequency":   cfg.Frequency,
			"duty":        cfg.Duty,
			"resolution":  cfg.Resolution,
			"auto_start":  cfg.AutoStart,
			"enabled":     cfg.Enabled,
			"request_id":  requestID,
		})
	})
}
