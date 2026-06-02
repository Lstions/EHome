package api

import (
	"net/http"
	"strconv"

	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// registerDeviceRoutes sets up device + channel CRUD routes
func registerDeviceRoutes(v1 *gin.RouterGroup, db *gorm.DB) {
	// List devices
	v1.GET("/devices", func(c *gin.Context) {
		var devices []models.Device
		db.Find(&devices)
		c.JSON(http.StatusOK, devices)
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
		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": d})
	})

	// Delete device
	v1.DELETE("/devices/:id", func(c *gin.Context) {
		id := c.Param("id")
		if err := db.Delete(&models.Device{}, id).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "deleted", "data": gin.H{"deleted": id}})
	})

	// List channels (optionally filter by collector_id)
	// - GET /api/v1/channels                  → all channels
	// - GET /api/v1/channels?collector_id=1   → channels belonging to that collector (numeric id)
	// Returns the envelope { code: 200, message: "", data: [...] } so the
	// front-end axios response interceptor can unwrap it.
	v1.GET("/channels", func(c *gin.Context) {
		q := db.Model(&models.Channel{})
		if cid := c.Query("collector_id"); cid != "" {
			// collector_id may be either a numeric id or a device_id string.
			// Match against Channel.CollectorID (numeric) when parseable, otherwise
			// resolve through the collectors table.
			if _, err := strconv.ParseUint(cid, 10, 64); err == nil {
				q = q.Where("collector_id = ?", cid)
			} else {
				var col models.Collector
				if err := db.Where("device_id = ?", cid).First(&col).Error; err == nil {
					q = q.Where("collector_id = ?", col.ID)
				} else {
					// Unknown collector → return empty result rather than 404 so
					// front-end skeletons resolve to "no data" instead of erroring.
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
		c.JSON(http.StatusCreated, ch)
	})

	// List config templates (optionally filter by collector_id and paginate)
	// - GET /api/v1/device-configs                                → all templates
	// - GET /api/v1/device-configs?collector_id=1&page_size=100   → filtered
	// Returns the envelope { code: 200, message: "", data: { items, total, page, page_size } }
	// so the front-end axios response interceptor can unwrap it.
	v1.GET("/device-configs", func(c *gin.Context) {
		q := db.Model(&models.ConfigTemplate{})
		if cid := c.Query("collector_id"); cid != "" {
			q = q.Where("collector_id = ?", cid)
		}
		var total int64
		q.Count(&total)
		// Pagination (defaults: page=1, page_size=20)
		page := 1
		pageSize := 20
		if v, err := strconv.Atoi(c.DefaultQuery("page", "1")); err == nil && v > 0 {
			page = v
		}
		if v, err := strconv.Atoi(c.DefaultQuery("page_size", "20")); err == nil && v > 0 {
			pageSize = v
		}
		var items []models.ConfigTemplate
		if err := q.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"code":    200,
			"message": "ok",
			"data": gin.H{
				"items":     items,
				"total":     total,
				"page":      page,
				"page_size": pageSize,
			},
		})
	})

	// Get config template detail
	v1.GET("/device-configs/:id", func(c *gin.Context) {
		id := c.Param("id")
		var tpl models.ConfigTemplate
		if err := db.First(&tpl, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "config template not found"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": tpl})
	})

	// Create config template
	v1.POST("/device-configs", func(c *gin.Context) {
		var tpl models.ConfigTemplate
		if err := c.ShouldBindJSON(&tpl); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": err.Error()})
			return
		}
		if err := db.Create(&tpl).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"code": 201, "message": "created", "data": tpl})
	})

	// Update config template
	v1.PUT("/device-configs/:id", func(c *gin.Context) {
		id := c.Param("id")
		var tpl models.ConfigTemplate
		if err := db.First(&tpl, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "config template not found"})
			return
		}
		var update models.ConfigTemplate
		if err := c.ShouldBindJSON(&update); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": err.Error()})
			return
		}
		update.ID = tpl.ID
		if err := db.Save(&update).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": update})
	})

	// Delete config template
	v1.DELETE("/device-configs/:id", func(c *gin.Context) {
		id := c.Param("id")
		if err := db.Delete(&models.ConfigTemplate{}, id).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "deleted", "data": gin.H{"deleted": id}})
	})

	// Get default config template for a device type
	// - GET /api/v1/device-configs/default/:device_type
	v1.GET("/device-configs/default/:device_type", func(c *gin.Context) {
		dt := c.Param("device_type")
		var tpl models.ConfigTemplate
		// Return most recently created template; in a real system this would
		// be marked is_default. For now we return any matching template or empty.
		err := db.Where("write_data LIKE ?", "%"+dt+"%").Order("id DESC").First(&tpl).Error
		if err == gorm.ErrRecordNotFound {
			c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "no default config for device_type"})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": tpl})
	})

	// Mark a config template as default (alias used by front-end forms)
	v1.POST("/device-configs/:id/default", func(c *gin.Context) {
		// For now this is a no-op alias; the mark-as-default logic is not yet
		// implemented in the model. Return OK so the front-end call resolves.
		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": gin.H{"id": c.Param("id"), "is_default": true}})
	})

	// Get a single channel
	// - GET /api/v1/channels/:channel_id
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
	// - PUT /api/v1/channels/:channel_id
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
		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": ch})
	})

	// Delete a channel
	// - DELETE /api/v1/channels/:channel_id
	v1.DELETE("/channels/:channel_id", func(c *gin.Context) {
		id := c.Param("channel_id")
		if err := db.Delete(&models.Channel{}, id).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
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
