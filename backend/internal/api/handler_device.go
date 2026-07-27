package api

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
	"ehome/backend/internal/nodemgr"
	"ehome/backend/pkg/logger"
	"ehome/backend/pkg/parser"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errInactiveDeviceConfigDefault = errors.New("only an active device config can be default")

func isPeripheralChannelType(value string) bool {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "GPIO", "4", "PWM", "6":
		return true
	default:
		return false
	}
}

func validateTransportChannel(ch *models.Channel) error {
	if ch == nil {
		return fmt.Errorf("channel is required")
	}
	if !ch.Enabled {
		return fmt.Errorf("channel is disabled")
	}
	return validateTransportChannelType(ch)
}

func validateTransportChannelType(ch *models.Channel) error {
	if ch == nil {
		return fmt.Errorf("channel is required")
	}
	if isPeripheralChannelType(ch.HardwareType) || isPeripheralChannelType(ch.BusType) {
		return fmt.Errorf("GPIO and PWM are peripheral resources, not channels")
	}
	hardwareType := strings.TrimSpace(ch.HardwareType)
	busType := strings.TrimSpace(ch.BusType)
	if hardwareType == "" || busType == "" {
		return fmt.Errorf("channel hardware_type and bus_type are required")
	}
	if !strings.EqualFold(hardwareType, busType) {
		return fmt.Errorf("channel hardware_type %q does not match bus_type %q", ch.HardwareType, ch.BusType)
	}
	return nil
}

func channelRoutePins(ch models.Channel) ([]int, error) {
	busType := strings.ToUpper(strings.TrimSpace(ch.BusType))
	raw := strings.TrimPrefix(strings.TrimSpace(ch.BusConfig), `\x`)
	bytes, err := hex.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("malformed bus_config: %w", err)
	}
	toInts := func(values ...byte) []int {
		out := make([]int, 0, len(values))
		for _, value := range values {
			out = append(out, int(value))
		}
		return out
	}
	switch busType {
	case "UART", "I2C":
		if len(bytes) < 2 {
			return nil, fmt.Errorf("%s bus_config requires at least 2 bytes", busType)
		}
		return toInts(bytes[0], bytes[1]), nil
	case "SPI":
		if len(bytes) != 9 {
			return nil, fmt.Errorf("SPI bus_config requires 9 bytes")
		}
		return toInts(bytes[0], bytes[6], bytes[7], bytes[8]), nil
	case "ADC":
		return nil, nil
	default:
		return nil, fmt.Errorf("unsupported transport bus type %q", ch.BusType)
	}
}

func validateChannelPeripheralConflicts(tx *gorm.DB, ch models.Channel) error {
	if !ch.Enabled {
		return nil
	}
	pins, err := channelRoutePins(ch)
	if err != nil {
		return err
	}
	for _, pin := range pins {
		var gpioCount, pwmCount int64
		if err := tx.Model(&models.GPIOConfig{}).Where("node_id = ? AND pin = ?", ch.NodeID, pin).Count(&gpioCount).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.PWMConfig{}).Where("node_id = ? AND pin = ?", ch.NodeID, pin).Count(&pwmCount).Error; err != nil {
			return err
		}
		if gpioCount+pwmCount > 0 {
			return errPeripheralPinConflict
		}
	}
	return nil
}

// validateChannelBindings prevents channel mutation from invalidating an
// existing EdgeDevice -> DeviceConfig -> Channel binding. The candidate channel
// and its referencing devices must be locked by the caller's transaction.
func validateChannelBindings(tx *gorm.DB, candidate *models.Channel) error {
	if candidate == nil {
		return fmt.Errorf("channel is required")
	}
	var devices []models.EdgeDevice
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("channel_id = ? AND node_id = ?", candidate.ID, candidate.NodeID).
		Find(&devices).Error; err != nil {
		return err
	}
	if len(devices) == 0 {
		return nil
	}
	if !candidate.Enabled {
		return fmt.Errorf("cannot disable channel referenced by edge devices")
	}
	for _, device := range devices {
		if _, err := validateDeviceConfigForChannel(tx, device.DeviceConfigID, candidate); err != nil {
			return fmt.Errorf("channel update invalidates edge device %d: %w", device.ID, err)
		}
	}
	return nil
}

func loadTransportChannel(db *gorm.DB, channelID uint, nodeID string) (*models.Channel, error) {
	if db == nil {
		return nil, fmt.Errorf("channel database is unavailable")
	}
	if channelID == 0 || strings.TrimSpace(nodeID) == "" {
		return nil, fmt.Errorf("channel_id and device_id are required")
	}
	var ch models.Channel
	if err := db.Where("id = ? AND node_id = ?", channelID, nodeID).First(&ch).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("channel not found for device")
		}
		return nil, fmt.Errorf("failed to load channel: %w", err)
	}
	if err := validateTransportChannel(&ch); err != nil {
		return nil, err
	}
	return &ch, nil
}

func affectedDeviceConfigNodes(tx *gorm.DB, configID uint) ([]string, error) {
	var nodeIDs []string
	if err := tx.Model(&models.EdgeDevice{}).Distinct("node_id").
		Where("device_config_id = ? AND deleted_at IS NULL AND node_id <> '' AND node_id <> '0'", configID).
		Pluck("node_id", &nodeIDs).Error; err != nil {
		return nil, err
	}
	return nodeIDs, nil
}

func emitDeviceConfigChanges(c *gin.Context, bus *nodemgr.ConfigEventBus, action nodemgr.ConfigChangeAction, nodeIDs []string, configID uint) {
	for _, nodeID := range nodeIDs {
		nodemgr.EmitConfigChange(c, bus, nodemgr.CfgChangeDeviceConfig, action, nodeID, fmt.Sprint(configID))
	}
}

// registerDeviceRoutes sets up channel + device-config CRUD routes
func registerDeviceRoutes(v1 *gin.RouterGroup, db *gorm.DB, nodeMgr *nodemgr.Manager, driverRegistry *drivers.Registry, policies ...ControlPolicy) {
	controlPolicy := resolveControlPolicy(policies...)
	// Get the EventBus for emitting config change events
	eventBus := nodeMgr.EventBus()

	// ============================================================
	// DeviceConfig 设备级配置模板 (前端 src/api/deviceConfig.ts)
	// ============================================================
	//
	// 与采集器级 ConfigTemplate (hex 读寄存器) 不同, 本组端点管理
	// 设备级元数据模板: 名称/描述/协议/硬件类型/参数/默认标志, 用于
	// 创建设备时一键套用。
	//
	// 端点清单:
	//   GET    /api/v1/device-configs                      → 列表 (分页 + 过滤)
	//   GET    /api/v1/device-configs/:id                  → 详情
	//   POST   /api/v1/device-configs                      → 创建
	//   PUT    /api/v1/device-configs/:id                  → 更新
	//   DELETE /api/v1/device-configs/:id                  → 删除
	//   GET    /api/v1/device-configs/default/:device_type → 取该类型默认模板
	//   POST   /api/v1/device-configs/:id/default          → 标记为默认

	// List device-config templates (paginated + filterable)
	v1.GET("/device-configs", func(c *gin.Context) {
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
		if page < 1 {
			page = 1
		}
		if pageSize < 1 || pageSize > 200 {
			pageSize = 20
		}
		q := db.Model(&models.DeviceConfig{})
		if dt := c.Query("device_type"); dt != "" {
			q = q.Where("device_type = ?", dt)
		}
		if ht := c.Query("hardware_type"); ht != "" {
			q = q.Where("hardware_type = ?", ht)
		}
		if st := c.Query("status"); st != "" {
			q = q.Where("status = ?", st)
		}
		var total int64
		q.Count(&total)
		var items []models.DeviceConfig
		if err := q.Order("is_default DESC, id DESC").
			Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"code": 200, "message": "ok",
			"data": gin.H{"list": items, "total": total, "page": page, "page_size": pageSize},
		})
	})

	// Get device-config detail
	v1.GET("/device-configs/:id", func(c *gin.Context) {
		id := c.Param("id")
		var tpl models.DeviceConfig
		if err := db.First(&tpl, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "device config not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": tpl})
	})

	// Create device-config template
	v1.POST("/device-configs", func(c *gin.Context) {
		var tpl models.DeviceConfig
		if err := c.ShouldBindJSON(&tpl); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": err.Error()})
			return
		}
		// 服务端兜底: 必填字段
		if tpl.Name == "" {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "name is required"})
			return
		}
		if tpl.DeviceType == "" {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "device_type is required"})
			return
		}
		if tpl.HardwareType == "" {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "hardware_type is required"})
			return
		}
		if tpl.Status == "" {
			tpl.Status = "active"
		} else {
			tpl.Status = strings.ToLower(strings.TrimSpace(tpl.Status))
			if tpl.Status != "active" && tpl.Status != "inactive" {
				c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "status must be active or inactive"})
				return
			}
		}
		if tpl.IsDefault && tpl.Status != "active" {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": errInactiveDeviceConfigDefault.Error()})
			return
		}
		if len(tpl.Config) == 0 {
			tpl.Config = json.RawMessage([]byte("{}"))
		}
		if len(tpl.Connection) == 0 {
			tpl.Connection = json.RawMessage([]byte("{}"))
		}
		if len(tpl.Parser) == 0 {
			tpl.Parser = json.RawMessage([]byte("{}"))
		}
		if len(tpl.InitFlow) == 0 {
			tpl.InitFlow = json.RawMessage([]byte("[]"))
		}
		if len(tpl.Operations) == 0 {
			tpl.Operations = json.RawMessage([]byte("{}"))
		}
		// Default replacement and creation are one transaction; no event is emitted
		// until the transaction commits (a newly-created config has no references).
		if err := db.Transaction(func(tx *gorm.DB) error {
			if tpl.IsDefault {
				if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Model(&models.DeviceConfig{}).
					Where("device_type = ? AND is_default = ?", tpl.DeviceType, true).
					Update("is_default", false).Error; err != nil {
					return err
				}
			}
			return tx.Create(&tpl).Error
		}); err != nil {
			c.JSON(http.StatusConflict, gin.H{"code": 409, "message": err.Error()})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"code": 201, "message": "created", "data": tpl})
	})

	// Update device-config template
	v1.PUT("/device-configs/:id", func(c *gin.Context) {
		id := c.Param("id")
		var raw map[string]json.RawMessage
		if err := c.ShouldBindJSON(&raw); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": err.Error()})
			return
		}
		if _, ok := raw["name"]; !ok {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "name is required"})
			return
		}
		var update models.DeviceConfig
		var affectedNodes []string
		if err := db.Transaction(func(tx *gorm.DB) error {
			var current models.DeviceConfig
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, id).Error; err != nil {
				return err
			}
			update = current
			body, err := json.Marshal(raw)
			if err != nil {
				return err
			}
			if err := json.Unmarshal(body, &update); err != nil {
				return err
			}
			update.ID = current.ID
			if strings.TrimSpace(update.Name) == "" {
				return fmt.Errorf("name is required")
			}
			if strings.TrimSpace(update.Status) == "" {
				update.Status = current.Status
			} else {
				update.Status = strings.ToLower(strings.TrimSpace(update.Status))
				if update.Status != "active" && update.Status != "inactive" {
					return fmt.Errorf("status must be active or inactive")
				}
			}
			if update.IsDefault && update.Status != "active" {
				return errInactiveDeviceConfigDefault
			}
			var references int64
			if err := tx.Model(&models.EdgeDevice{}).Where("device_config_id = ?", current.ID).Count(&references).Error; err != nil {
				return err
			}
			if references > 0 && (update.DeviceType != current.DeviceType || !strings.EqualFold(update.HardwareType, current.HardwareType) || !strings.EqualFold(update.Status, "active")) {
				return fmt.Errorf("cannot change type, hardware_type, or active status of a device config referenced by edge devices")
			}
			if update.IsDefault {
				if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Model(&models.DeviceConfig{}).Where("device_type = ? AND id <> ? AND is_default = ?", update.DeviceType, current.ID, true).Update("is_default", false).Error; err != nil {
					return err
				}
			}
			nodes, nodeErr := affectedDeviceConfigNodes(tx, current.ID)
			if nodeErr != nil {
				return nodeErr
			}
			affectedNodes = nodes
			return tx.Save(&update).Error
		}); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "device config not found"})
			} else {
				c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": err.Error()})
			}
			return
		}
		// Transaction committed: fan out once per real referenced node.
		emitDeviceConfigChanges(c, eventBus, nodemgr.CfgActionUpdate, affectedNodes, update.ID)
		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": update})
	})

	// Delete device-config template
	v1.DELETE("/device-configs/:id", func(c *gin.Context) {
		id := c.Param("id")
		if err := db.Transaction(func(tx *gorm.DB) error {
			var config models.DeviceConfig
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&config, id).Error; err != nil {
				return err
			}
			var references int64
			if err := tx.Model(&models.EdgeDevice{}).Where("device_config_id = ?", config.ID).Count(&references).Error; err != nil {
				return err
			}
			if references > 0 {
				return fmt.Errorf("cannot delete device config referenced by edge devices")
			}
			return tx.Delete(&config).Error
		}); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "device config not found"})
			} else {
				c.JSON(http.StatusConflict, gin.H{"code": 409, "message": err.Error()})
			}
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "deleted", "data": gin.H{"deleted": id}})
	})

	// Get default device-config for a device_type
	// - 优先返回 is_default=true 的
	// - 没有默认则返回最近创建的一条 active 模板
	// - 都没有则 404 + data:null (前端兼容)
	v1.GET("/device-configs/default/:device_type", func(c *gin.Context) {
		dt := c.Param("device_type")
		var tpl models.DeviceConfig
		// 1. 找 is_default
		err := db.Where("device_type = ? AND is_default = ? AND status = ?", dt, true, "active").
			Order("id DESC").First(&tpl).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}
		// 2. 兜底: 任意 active
		if tpl.ID == 0 {
			err = db.Where("device_type = ? AND status = ?", dt, "active").
				Order("id DESC").First(&tpl).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "no default config for device_type", "data": nil})
				return
			}
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
				return
			}
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": tpl})
	})

	// Mark a device-config as default (取消同 device_type 的其他默认)
	v1.POST("/device-configs/:id/default", func(c *gin.Context) {
		id := c.Param("id")
		var tpl models.DeviceConfig
		err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&tpl, id).Error; err != nil {
				return err
			}
			if tpl.Status != "active" {
				return errInactiveDeviceConfigDefault
			}
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Model(&models.DeviceConfig{}).
				Where("device_type = ? AND id <> ? AND is_default = ?", tpl.DeviceType, tpl.ID, true).
				Update("is_default", false).Error; err != nil {
				return err
			}
			return tx.Model(&tpl).Update("is_default", true).Error
		})
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "device config not found"})
			} else if errors.Is(err, errInactiveDeviceConfigDefault) {
				c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": err.Error()})
			} else {
				c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			}
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": tpl})
	})

	// Get init-flow for a device-config
	v1.GET("/device-configs/:id/init-flow", func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		var cfg models.DeviceConfig
		if err := db.First(&cfg, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"code": 404})
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": cfg.InitFlow})
	})

	// Get operations for a device-config
	v1.GET("/device-configs/:id/operations", func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		var cfg models.DeviceConfig
		if err := db.First(&cfg, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"code": 404})
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": cfg.Operations})
	})

	// Test parser for a device-config
	v1.POST("/device-configs/:id/test-parser", func(c *gin.Context) {
		var req struct {
			RawData string `json:"raw_data"`
		}
		c.ShouldBindJSON(&req)
		id, _ := strconv.Atoi(c.Param("id"))
		var cfg models.DeviceConfig
		if err := db.First(&cfg, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"code": 404})
			return
		}

		rawBytes := []byte(req.RawData)
		// If raw_data looks like hex, decode it first
		if decoded, hexErr := decodeHexString(req.RawData); hexErr == nil && len(decoded) > 0 {
			rawBytes = decoded
		}

		// Primary path: DeviceConfig.Parser JSONB. This mirrors runtime parsing in nodemgr.
		if len(cfg.Parser) > 0 && string(cfg.Parser) != "{}" && string(cfg.Parser) != "null" {
			if cp, err := parser.NewConfigParser(cfg.Parser); err == nil {
				fields, parseErr := cp.Parse(rawBytes)
				if parseErr == nil {
					c.JSON(http.StatusOK, gin.H{"code": 200, "data": gin.H{
						"device_type": cfg.DeviceType, "parser_id": cfg.ParserID,
						"raw_data": req.RawData, "parsed": fields, "parser": "device_config",
					}})
					return
				}
			}
		}

		// Fallback: legacy driver parser.
		if driverRegistry != nil {
			if driver, err := driverRegistry.Get(cfg.DeviceType); err == nil {
				sensorData, parseErr := driver.ParseData(rawBytes)
				if parseErr != nil {
					c.JSON(http.StatusOK, gin.H{"code": 200, "data": gin.H{
						"device_type": cfg.DeviceType, "parser_id": cfg.ParserID,
						"raw_data": req.RawData, "parsed": gin.H{}, "error": parseErr.Error(), "parser": "driver",
					}})
					return
				}
				c.JSON(http.StatusOK, gin.H{"code": 200, "data": gin.H{
					"device_type": cfg.DeviceType, "parser_id": cfg.ParserID,
					"raw_data": req.RawData, "parsed": sensorData, "parser": "driver",
				}})
				return
			}
		}

		// Fallback: basic JSON parse if raw_data is valid JSON
		parsed := gin.H{}
		if req.RawData != "" {
			var jsonObj interface{}
			if json.Unmarshal([]byte(req.RawData), &jsonObj) == nil {
				parsed = gin.H{"json": jsonObj}
			}
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": gin.H{
			"device_type": cfg.DeviceType, "parser_id": cfg.ParserID,
			"raw_data": req.RawData, "parsed": parsed,
		}})
	})

	// ============================================================
	// Channel 通道 CRUD
	// ============================================================

	// List channels (optionally filter by node_id)
	v1.GET("/channels", func(c *gin.Context) {
		q := db.Model(&models.Channel{})
		if nid := c.Query("node_id"); nid != "" {
			if _, err := strconv.ParseUint(nid, 10, 64); err == nil {
				q = q.Where("node_id = ?", nid)
			} else {
				var node models.Node
				if err := db.Where("node_id = ?", nid).First(&node).Error; err == nil {
					q = q.Where("node_id = ?", node.NodeID)
				} else {
					c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": []models.Channel{}})
					return
				}
			}
		}
		var chs []models.Channel
		if err := q.Find(&chs).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": chs})
	})

	// Create channel
	v1.POST("/channels", func(c *gin.Context) {
		var raw map[string]json.RawMessage
		if err := c.ShouldBindJSON(&raw); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		// GORM applies the model's default value when a zero-valued field is
		// inserted.  Keep an explicit interval_ms: 0 distinct from an omitted
		// interval so callers can intentionally disable scheduling.
		var requestedInterval *int
		if value, ok := raw["interval_ms"]; ok {
			var interval int
			if err := json.Unmarshal(value, &interval); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "interval_ms must be an integer"})
				return
			}
			requestedInterval = &interval
		}
		body, err := json.Marshal(raw)
		var ch models.Channel
		if err != nil || json.Unmarshal(body, &ch) != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid channel payload"})
			return
		}
		desiredEnabled := true
		if value, ok := raw["enabled"]; ok {
			if err := json.Unmarshal(value, &desiredEnabled); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "enabled must be boolean"})
				return
			}
		}
		ch.Enabled = desiredEnabled
		if err := validateTransportChannelType(&ch); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := db.Transaction(func(tx *gorm.DB) error {
			var node models.Node
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("node_id = ?", ch.NodeID).First(&node).Error; err != nil {
				return err
			}
			if err := validateChannelPeripheralConflicts(tx, ch); err != nil {
				return err
			}
			if err := tx.Create(&ch).Error; err != nil {
				return err
			}
			if requestedInterval != nil && *requestedInterval == 0 {
				if err := tx.Model(&ch).UpdateColumn("interval_ms", 0).Error; err != nil {
					return err
				}
				ch.IntervalMs = 0
			}
			if !desiredEnabled {
				if err := tx.Model(&ch).Update("enabled", false).Error; err != nil {
					return err
				}
				ch.Enabled = false
			}
			return nil
		}); err != nil {
			if errors.Is(err, errPeripheralPinConflict) {
				c.JSON(http.StatusConflict, gin.H{"error": "channel route conflicts with GPIO/PWM configuration"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		// Emit config change for the node
		nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeChannel, nodemgr.CfgActionCreate, ch.NodeID, fmt.Sprint(ch.ID))
		c.JSON(http.StatusCreated, ch)
	})

	v1.GET("/channels/:channel_id", func(c *gin.Context) {
		id := c.Param("channel_id")
		var ch models.Channel
		if err := db.First(&ch, id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "channel not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": ch})
	})

	// Update a channel (DTO pattern to prevent mass assignment)
	v1.PUT("/channels/:channel_id", func(c *gin.Context) {
		id := c.Param("channel_id")
		var ch models.Channel
		if err := db.First(&ch, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "channel not found"})
			return
		}
		var dto struct {
			HardwareType *string `json:"hardware_type"`
			HardwareID   *string `json:"hardware_id"`
			IntervalMs   *int    `json:"interval_ms"`
			BusType      *string `json:"bus_type"`
			BusConfig    *string `json:"bus_config"`
			TemplateIDs  *string `json:"template_ids"`
			Config       *string `json:"config"`
			Enabled      *bool   `json:"enabled"`
			DmaEnabled   *bool   `json:"dma_enabled"`
		}
		if err := c.ShouldBindJSON(&dto); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": err.Error()})
			return
		}
		candidate := ch
		if dto.HardwareType != nil {
			candidate.HardwareType = *dto.HardwareType
		}
		if dto.BusType != nil {
			candidate.BusType = *dto.BusType
		}
		if dto.Enabled != nil {
			candidate.Enabled = *dto.Enabled
		}
		if dto.BusConfig != nil {
			candidate.BusConfig = *dto.BusConfig
		}
		/* Validate the requested final type. Re-enabling a currently disabled
		 * transport channel must not fail because its persisted state is false. */
		if err := validateTransportChannelType(&candidate); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": err.Error()})
			return
		}
		updates := map[string]interface{}{}
		if dto.HardwareType != nil {
			updates["hardware_type"] = *dto.HardwareType
		}
		if dto.HardwareID != nil {
			updates["hardware_id"] = *dto.HardwareID
		}
		if dto.IntervalMs != nil {
			updates["interval_ms"] = *dto.IntervalMs
		}
		if dto.BusType != nil {
			updates["bus_type"] = *dto.BusType
		}
		if dto.BusConfig != nil {
			updates["bus_config"] = *dto.BusConfig
		}
		if dto.TemplateIDs != nil {
			updates["template_ids"] = *dto.TemplateIDs
		}
		if dto.Config != nil {
			updates["config"] = *dto.Config
		}
		if dto.Enabled != nil {
			updates["enabled"] = *dto.Enabled
		}
		if dto.DmaEnabled != nil {
			updates["dma_enabled"] = *dto.DmaEnabled
		}
		if len(updates) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "no fields to update"})
			return
		}
		if err := db.Transaction(func(tx *gorm.DB) error {
			var node models.Node
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("node_id = ?", ch.NodeID).First(&node).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			var locked models.Channel
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, id).Error; err != nil {
				return err
			}
			finalCandidate := locked
			if dto.HardwareType != nil {
				finalCandidate.HardwareType = *dto.HardwareType
			}
			if dto.BusType != nil {
				finalCandidate.BusType = *dto.BusType
			}
			if dto.Enabled != nil {
				finalCandidate.Enabled = *dto.Enabled
			}
			if dto.BusConfig != nil {
				finalCandidate.BusConfig = *dto.BusConfig
			}
			if err := validateTransportChannelType(&finalCandidate); err != nil {
				return err
			}
			if err := validateChannelPeripheralConflicts(tx, finalCandidate); err != nil {
				return err
			}
			if err := validateChannelBindings(tx, &finalCandidate); err != nil {
				return err
			}
			return tx.Model(&locked).Updates(updates).Error
		}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}
		// Reload to get updated record with new timestamps
		if err := db.First(&ch, id).Error; err != nil {
			logger.Warnf("Failed to reload channel after update: %v", err)
		}
		// Emit config change for the node
		nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeChannel, nodemgr.CfgActionUpdate, ch.NodeID, fmt.Sprint(ch.ID))
		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": ch})
	})

	// Delete a channel
	v1.DELETE("/channels/:channel_id", func(c *gin.Context) {
		id := c.Param("channel_id")
		var ch models.Channel
		if err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&ch, id).Error; err != nil {
				return err
			}
			var devices []models.EdgeDevice
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("channel_id = ?", ch.ID).Find(&devices).Error; err != nil {
				return err
			}
			if len(devices) > 0 {
				return fmt.Errorf("cannot delete channel referenced by edge devices")
			}
			return tx.Delete(&ch).Error
		}); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "channel not found"})
			} else {
				c.JSON(http.StatusConflict, gin.H{"code": 409, "message": err.Error()})
			}
			return
		}
		// Emit config change
		nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeChannel, nodemgr.CfgActionDelete, ch.NodeID, fmt.Sprint(ch.ID))
		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "deleted", "data": gin.H{"deleted": id}})
	})

	// Channel write (send raw data)
	v1.POST("/channels/:channel_id/write", func(c *gin.Context) {
		if !controlPolicy.rawWritesEnabled() {
			c.JSON(http.StatusGone, gin.H{"code": 410, "message": "raw channel writes are disabled; use an audited device action"})
			return
		}

		id := c.Param("channel_id")
		channelID := parseUintID(id)
		var req struct {
			Data    string `json:"data" binding:"required"`
			HexMode bool   `json:"hex_mode"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": err.Error()})
			return
		}
		// Decode data (hex or raw)
		var data []byte
		if req.HexMode {
			var err error
			data, err = hex.DecodeString(req.Data)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "invalid hex data"})
				return
			}
		} else {
			data = []byte(req.Data)
		}
		// Look up channel to get node_id
		var ch models.Channel
		if err := db.First(&ch, channelID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "channel not found"})
			return
		}
		if err := validateTransportChannel(&ch); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": err.Error()})
			return
		}
		// Send WriteCommand via pending write (with 10s timeout)
		deviceID := ch.NodeID
		ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
		defer cancel()
		resp, err := nodeMgr.PendingWrite().SendWriteCommand(ctx, deviceID, uint32(channelID), data, 0, 10*time.Second)
		if err != nil {
			c.JSON(http.StatusGatewayTimeout, gin.H{"code": 504, "message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"code": 200,
			"data": gin.H{
				"channel_id": channelID,
				"success":    resp.Success,
				"error_code": resp.ErrorCode,
				"error_msg":  resp.ErrorMsg,
			},
		})
	})

	// Channel scan (discover devices on bus)
	// Supports both I2C and Modbus scan types
	v1.POST("/channels/:channel_id/scan", func(c *gin.Context) {
		id := c.Param("channel_id")
		channelID := parseUintID(id)

		var req struct {
			ScanType  string `json:"scan_type"`            // "i2c" or "modbus"
			StartAddr int    `json:"start_addr,omitempty"` // Modbus start address
			EndAddr   int    `json:"end_addr,omitempty"`   // Modbus end address
			TimeoutMs int    `json:"timeout_ms,omitempty"` // per-address timeout
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			req.ScanType = "i2c" // default to I2C
		}

		// Look up channel
		var ch models.Channel
		if err := db.First(&ch, channelID).Error; err != nil {
			Error(c, http.StatusNotFound, "channel not found")
			return
		}
		if err := validateTransportChannel(&ch); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}

		// Look up node to get device ID for MQTT
		var node models.Node
		if err := db.Where("node_id = ?", ch.NodeID).First(&node).Error; err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}

		deviceID := node.NodeID

		if req.ScanType == "modbus" {
			// Modbus scan: send ScanRequest with Modbus parameters
			requestID, err := nodeMgr.SendModbusScanRequest(deviceID, req.StartAddr, req.EndAddr, req.TimeoutMs)
			if err != nil {
				Error(c, http.StatusInternalServerError, "failed to trigger Modbus scan: "+err.Error())
				return
			}
			c.JSON(http.StatusOK, gin.H{
				"code":    200,
				"message": "ok",
				"data": gin.H{
					"channel_id": channelID,
					"scan_type":  "modbus",
					"request_id": requestID,
					"message":    "Modbus 扫描已触发，等待结果",
				},
			})
		} else {
			// I2C scan: use existing SendScanRequest
			hwID := uint32(ch.ID) // use channel ID as hardware_id for I2C
			if err := nodeMgr.SendScanRequest(deviceID, hwID); err != nil {
				Error(c, http.StatusInternalServerError, "failed to trigger I2C scan: "+err.Error())
				return
			}
			c.JSON(http.StatusOK, gin.H{
				"code":    200,
				"message": "ok",
				"data": gin.H{
					"channel_id": channelID,
					"scan_type":  "i2c",
					"request_id": fmt.Sprintf("scan-%d", time.Now().Unix()),
					"message":    "I2C 扫描已触发，等待结果",
				},
			})
		}
	})

	// Channel reconfigure (change bus params)
	v1.POST("/channels/:channel_id/reconfigure", func(c *gin.Context) {
		id := c.Param("channel_id")
		var req struct {
			Baudrate int `json:"baudrate"`
			ClockHz  int `json:"clock_hz"`
		}
		c.ShouldBindJSON(&req)
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": gin.H{"status": "reconfigured", "request_id": fmt.Sprintf("reconf-%s-%d", id, time.Now().Unix())}})
	})

	// Device config tree (driver hierarchy: OEM → Category → Driver)
	v1.GET("/device-configs/tree", func(c *gin.Context) {
		type DriverLeaf struct {
			Type          string   `json:"type"`
			Model         string   `json:"model"`
			DisplayName   string   `json:"display_name"`
			HardwareTypes []string `json:"hardware_types"`
			Description   string   `json:"description"`
		}
		type TreeNode struct {
			ID       string       `json:"id"`
			Name     string       `json:"name"`
			Children []TreeNode   `json:"children,omitempty"`
			Drivers  []DriverLeaf `json:"drivers,omitempty"`
		}

		// Build tree from registered drivers + DB device-configs
		// Structure: OEM → Category → Driver
		type catKey struct{ oem, cat string }
		driverMap := map[catKey][]DriverLeaf{}

		// 1. Registered drivers (priority)
		if driverRegistry != nil {
			for _, dt := range driverRegistry.List() {
				d, _ := driverRegistry.Get(dt)
				hwt := d.HardwareTypes()
				if hwt == nil {
					hwt = []string{}
				}
				key := catKey{d.OEM(), d.Category()}
				driverMap[key] = append(driverMap[key], DriverLeaf{
					Type: dt, Model: dt, DisplayName: d.DeviceName(),
					HardwareTypes: hwt, Description: "",
				})
			}
		}

		// 2. DB device-configs (supplement, dedup by type)
		var configs []models.DeviceConfig
		db.Find(&configs)
		for _, cfg := range configs {
			oem := "通用"
			if cfg.VendorID != nil {
				var vendor models.Vendor
				if db.First(&vendor, *cfg.VendorID).Error == nil {
					oem = vendor.Name
				}
			}
			key := catKey{oem, cfg.DeviceType}
			// Dedup: skip if type already exists
			dup := false
			for _, d := range driverMap[key] {
				if d.Type == cfg.DeviceType {
					dup = true
					break
				}
			}
			if !dup {
				driverMap[key] = append(driverMap[key], DriverLeaf{
					Type: cfg.DeviceType, Model: cfg.DeviceModel,
					DisplayName:   cfg.Name,
					HardwareTypes: []string{cfg.HardwareType},
					Description:   cfg.Description,
				})
			}
		}

		// Group by OEM
		oemMap := map[string]map[string][]DriverLeaf{}
		for k, drivers := range driverMap {
			if oemMap[k.oem] == nil {
				oemMap[k.oem] = map[string][]DriverLeaf{}
			}
			oemMap[k.oem][k.cat] = drivers
		}

		tree := []TreeNode{}
		for oemName, cats := range oemMap {
			oemNode := TreeNode{ID: oemName, Name: oemName, Children: []TreeNode{}}
			for catName, drivers := range cats {
				oemNode.Children = append(oemNode.Children, TreeNode{
					ID: catName, Name: catName, Drivers: drivers,
				})
			}
			tree = append(tree, oemNode)
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": tree})
	})
}

// parseUintID parses a uint from a string, returning 0 on error
// decodeHexString attempts to decode a hex string (with or without 0x prefix)
func decodeHexString(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		s = s[2:]
	}
	if len(s)%2 != 0 {
		return nil, fmt.Errorf("odd length hex string")
	}
	return hex.DecodeString(s)
}

func parseUintID(s string) uint {
	id, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0
	}
	return uint(id)
}
