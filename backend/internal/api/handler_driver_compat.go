package api

import (
	"errors"
	"net/http"
	"strconv"

	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// registerDriverCompatRoutes adds /drivers/* routes that reuse device-configs logic
// for frontend compatibility (v2.2 replaces "drivers" with "device_configs").
func registerDriverCompatRoutes(v1 *gin.RouterGroup, db *gorm.DB) {
	// GET /drivers — list device-configs (same as /device-configs but with driver-compatible response)
	v1.GET("/drivers", func(c *gin.Context) {
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

	// GET /drivers/:type — single device-config by device_type
	v1.GET("/drivers/:type", func(c *gin.Context) {
		dt := c.Param("type")
		// Try ID first if numeric
		if id, err := strconv.ParseUint(dt, 10, 64); err == nil {
			var tpl models.DeviceConfig
			if err := db.First(&tpl, id).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "driver not found"})
					return
				}
				c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": tpl})
			return
		}
		// Otherwise look up by device_type
		var tpl models.DeviceConfig
		if err := db.Where("device_type = ?", dt).Order("is_default DESC, id DESC").First(&tpl).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "driver not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": tpl})
	})

	// GET /drivers/tree — driver tree grouped by vendor/hardware_type
	v1.GET("/drivers/tree", func(c *gin.Context) {
		var configs []models.DeviceConfig
		if err := db.Where("status = ?", "active").Order("hardware_type, device_type").Find(&configs).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}
		// Group by hardware_type (as proxy for vendor category)
		type TreeNode struct {
			Key      string             `json:"key"`
			Label    string             `json:"label"`
			Children []models.DeviceConfig `json:"children"`
		}
		groupMap := make(map[string]*TreeNode)
		var tree []TreeNode
		// Use device_type as tree key (more meaningful than HardwareType which is just uart/i2c/spi)
		for i := range configs {
			key := configs[i].DeviceType
			if key == "" {
				key = "other"
			}
			if _, ok := groupMap[key]; !ok {
				label := key
				groupMap[key] = &TreeNode{Key: key, Label: label}
				tree = append(tree, *groupMap[key])
				// Keep pointer valid after append
				groupMap[key] = &tree[len(tree)-1]
			}
			groupMap[key].Children = append(groupMap[key].Children, configs[i])
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": tree})
	})
}
