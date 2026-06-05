package api

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ehome/backend/internal/nodemgr"
	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// registerDeviceRoutes sets up channel + device-config CRUD routes
func registerDeviceRoutes(v1 *gin.RouterGroup, db *gorm.DB, nodeMgr *nodemgr.Manager, driverRegistry *drivers.Registry) {
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
		}
		// 若标记为默认, 取消同 device_type 的其他默认
		if tpl.IsDefault {
			db.Model(&models.DeviceConfig{}).
				Where("device_type = ? AND is_default = ?", tpl.DeviceType, true).
				Update("is_default", false)
		}
		// Normalize v2.2 JSONB fields: 空值 -> 合法 JSON (GORM 不会自动转)
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
		if err := db.Create(&tpl).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}
		// Emit config change (device_config affects all nodes using this config)
		nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeDeviceConfig, nodemgr.CfgActionCreate, 0, tpl.ID)
		c.JSON(http.StatusCreated, gin.H{"code": 201, "message": "created", "data": tpl})
	})

	// Update device-config template
	v1.PUT("/device-configs/:id", func(c *gin.Context) {
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
		var update models.DeviceConfig
		if err := c.ShouldBindJSON(&update); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": err.Error()})
			return
		}
		update.ID = tpl.ID
		if update.Name == "" {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "name is required"})
			return
		}
		if update.DeviceType == "" {
			update.DeviceType = tpl.DeviceType
		}
		if update.HardwareType == "" {
			update.HardwareType = tpl.HardwareType
		}
		// 若标记为默认, 取消同 device_type 的其他默认
		if update.IsDefault {
			db.Model(&models.DeviceConfig{}).
				Where("device_type = ? AND id <> ? AND is_default = ?", update.DeviceType, tpl.ID, true).
				Update("is_default", false)
		}
		// Normalize v2.2 JSONB fields: 空值 -> 合法 JSON (GORM 不会自动转)
		if len(update.Config) == 0 {
			update.Config = json.RawMessage([]byte("{}"))
		}
		if len(update.Connection) == 0 {
			update.Connection = json.RawMessage([]byte("{}"))
		}
		if len(update.Parser) == 0 {
			update.Parser = json.RawMessage([]byte("{}"))
		}
		if len(update.InitFlow) == 0 {
			update.InitFlow = json.RawMessage([]byte("[]"))
		}
		if err := db.Save(&update).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}
		// Emit config change (device_config affects all nodes using this config)
		nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeDeviceConfig, nodemgr.CfgActionUpdate, 0, update.ID)
		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": update})
	})

	// Delete device-config template
	v1.DELETE("/device-configs/:id", func(c *gin.Context) {
		id := c.Param("id")
		tplID := parseUintID(id)
		if err := db.Delete(&models.DeviceConfig{}, id).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}
		// Emit config change
		nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeDeviceConfig, nodemgr.CfgActionDelete, 0, tplID)
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
		if err := db.First(&tpl, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "device config not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}
		// 事务: 取消同 device_type 的其他默认, 设当前为默认
		err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&models.DeviceConfig{}).
				Where("device_type = ? AND id <> ? AND is_default = ?", tpl.DeviceType, tpl.ID, true).
				Update("is_default", false).Error; err != nil {
				return err
			}
			return tx.Model(&tpl).Update("is_default", true).Error
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}
		// Emit config change (default flag change affects nodes using this config)
		nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeDeviceConfig, nodemgr.CfgActionUpdate, 0, tpl.ID)
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

		// Try the drivers.Registry first
		if driverRegistry != nil {
			if driver, err := driverRegistry.Get(cfg.DeviceType); err == nil {
				rawBytes := []byte(req.RawData)
				// If raw_data looks like hex, decode it first
				if decoded, hexErr := decodeHexString(req.RawData); hexErr == nil && len(decoded) > 0 {
					rawBytes = decoded
				}
				sensorData, parseErr := driver.ParseData(rawBytes)
				if parseErr != nil {
					c.JSON(http.StatusOK, gin.H{"code": 200, "data": gin.H{
						"device_type": cfg.DeviceType, "parser_id": cfg.ParserID,
						"raw_data": req.RawData, "parsed": gin.H{}, "error": parseErr.Error(),
					}})
					return
				}
				c.JSON(http.StatusOK, gin.H{"code": 200, "data": gin.H{
					"device_type": cfg.DeviceType, "parser_id": cfg.ParserID,
					"raw_data": req.RawData, "parsed": sensorData,
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
					q = q.Where("node_id = ?", node.ID)
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
		var ch models.Channel
		if err := c.ShouldBindJSON(&ch); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := db.Create(&ch).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		// Emit config change for the node
		nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeChannel, nodemgr.CfgActionCreate, ch.NodeID, ch.ID)
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

	// Update a channel
	v1.PUT("/channels/:channel_id", func(c *gin.Context) {
		id := c.Param("channel_id")
		var ch models.Channel
		if err := db.First(&ch, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "channel not found"})
			return
		}
		if err := c.ShouldBindJSON(&ch); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": err.Error()})
			return
		}
		ch.ID = parseUintID(id)
		if err := db.Save(&ch).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}
		// Emit config change for the node
		nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeChannel, nodemgr.CfgActionUpdate, ch.NodeID, ch.ID)
		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": ch})
	})

	// Delete a channel
	v1.DELETE("/channels/:channel_id", func(c *gin.Context) {
		id := c.Param("channel_id")
		var ch models.Channel
		hasNode := db.First(&ch, id).Error == nil
		nodeID := ch.NodeID
		channelID := ch.ID
		if err := db.Delete(&models.Channel{}, id).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}
		// Emit config change
		if hasNode {
			nodemgr.EmitConfigChange(c, eventBus, nodemgr.CfgChangeChannel, nodemgr.CfgActionDelete, nodeID, channelID)
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "deleted", "data": gin.H{"deleted": id}})
	})

	// Channel write (send raw data)
	v1.POST("/channels/:channel_id/write", func(c *gin.Context) {
		id := c.Param("channel_id")
		var req struct {
			Data string `json:"data" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": gin.H{"channel_id": parseUintID(id), "data": req.Data, "status": "sent"}})
	})

	// Channel scan (discover devices on bus)
	v1.POST("/channels/:channel_id/scan", func(c *gin.Context) {
		id := c.Param("channel_id")
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": gin.H{"channel_id": parseUintID(id), "devices": []string{}}})
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

	// Device config tree (driver hierarchy)
	v1.GET("/device-configs/tree", func(c *gin.Context) {
		var configs []models.DeviceConfig
		db.Find(&configs)
		// Group by device_type
		groups := map[string][]models.DeviceConfig{}
		for _, cfg := range configs {
			groups[cfg.DeviceType] = append(groups[cfg.DeviceType], cfg)
		}
		tree := []gin.H{}
		for dtype, cfgs := range groups {
			drivers := []gin.H{}
			for _, cfg := range cfgs {
				drivers = append(drivers, gin.H{
					"type": cfg.DeviceType, "model": cfg.DeviceModel,
					"display_name": cfg.Name, "hardware_types": []string{},
					"description": cfg.Description,
				})
			}
			tree = append(tree, gin.H{"id": dtype, "name": dtype, "children": []gin.H{}, "drivers": drivers})
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
