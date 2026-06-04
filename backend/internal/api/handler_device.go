package api

import (
	"errors"
	"net/http"
	"strconv"

	"ehome/backend/internal/collector"
	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// registerDeviceRoutes sets up device + channel CRUD routes
func registerDeviceRoutes(v1 *gin.RouterGroup, db *gorm.DB, collectorMgr *collector.Manager) {
	// Get the EventBus for emitting config change events (v2.1)
	eventBus := collectorMgr.EventBus()

	// List devices
	v1.GET("/devices", func(c *gin.Context) {
		var devices []models.Device
		db.Find(&devices)
		c.JSON(http.StatusOK, devices)
	})

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
		if err := db.Create(&tpl).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}
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
		if err := db.Save(&update).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": update})
	})

	// Delete device-config template
	v1.DELETE("/device-configs/:id", func(c *gin.Context) {
		id := c.Param("id")
		if err := db.Delete(&models.DeviceConfig{}, id).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
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
		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": tpl})
	})

	// Create device
	v1.POST("/devices", func(c *gin.Context) {
		var dev models.Device
		if err := c.ShouldBindJSON(&dev); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := db.Create(&dev).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusCreated, dev)
	})

	// Get single device by id
	v1.GET("/devices/:id", func(c *gin.Context) {
		id := c.Param("id")
		var d models.Device
		if err := db.First(&d, id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "device not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": d})
	})

	// Update device
	v1.PUT("/devices/:id", func(c *gin.Context) {
		id := c.Param("id")
		var d models.Device
		if err := db.First(&d, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "device not found"})
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
		// v2.1: Emit config change for the channel's collector
		var ch models.Channel
		if db.First(&ch, d.ChannelID).Error == nil {
			collector.EmitConfigChange(eventBus, collector.CfgChangeDevice, collector.CfgActionUpdate, ch.CollectorID, d.ID)
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": d})
	})

	// Delete device
	v1.DELETE("/devices/:id", func(c *gin.Context) {
		id := c.Param("id")
		var d models.Device
		if err := db.First(&d, id).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}
		// Find collector before deletion for event emission
		var ch models.Channel
		hasCollector := db.First(&ch, d.ChannelID).Error == nil
		if err := db.Delete(&models.Device{}, id).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}
		// v2.1: Emit config change
		if hasCollector {
			collector.EmitConfigChange(eventBus, collector.CfgChangeDevice, collector.CfgActionDelete, ch.CollectorID, d.ID)
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "deleted", "data": gin.H{"deleted": id}})
	})

	// List channels (optionally filter by collector_id)
	v1.GET("/channels", func(c *gin.Context) {
		q := db.Model(&models.Channel{})
		if cid := c.Query("collector_id"); cid != "" {
			if _, err := strconv.ParseUint(cid, 10, 64); err == nil {
				q = q.Where("collector_id = ?", cid)
			} else {
				var col models.Collector
				if err := db.Where("device_id = ?", cid).First(&col).Error; err == nil {
					q = q.Where("collector_id = ?", col.ID)
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
		// v2.1: Emit config change for the collector
		collector.EmitConfigChange(eventBus, collector.CfgChangeChannel, collector.CfgActionCreate, ch.CollectorID, ch.ID)
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
		// v2.1: Emit config change for the collector
		collector.EmitConfigChange(eventBus, collector.CfgChangeChannel, collector.CfgActionUpdate, ch.CollectorID, ch.ID)
		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": ch})
	})

	// Delete a channel
	v1.DELETE("/channels/:channel_id", func(c *gin.Context) {
		id := c.Param("channel_id")
		var ch models.Channel
		hasCollector := db.First(&ch, id).Error == nil
		collectorID := ch.CollectorID
		channelID := ch.ID
		if err := db.Delete(&models.Channel{}, id).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}
		// v2.1: Emit config change
		if hasCollector {
			collector.EmitConfigChange(eventBus, collector.CfgChangeChannel, collector.CfgActionDelete, collectorID, channelID)
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "deleted", "data": gin.H{"deleted": id}})
	})
}

// parseUintID parses a uint from a string, returning 0 on error
func parseUintID(s string) uint {
	id, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0
	}
	return uint(id)
}
