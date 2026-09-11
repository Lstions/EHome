package api

import (
	"net/http"
	"strconv"

	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func registerDataSourceRoutes(ds *gin.RouterGroup, db *gorm.DB) {
	// GET /api/v1/data-sources
	ds.GET("", func(c *gin.Context) {
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
		var items []models.DataSource
		var total int64
		q := db.Model(&models.DataSource{})
		if name := c.Query("name"); name != "" {
			q = q.Where("name LIKE ?", "%"+name+"%")
		}
		if typ := c.Query("type"); typ != "" {
			q = q.Where("type = ?", typ)
		}
		if status := c.Query("status"); status != "" {
			q = q.Where("status = ?", status)
		}
		q.Count(&total)
		q.Order("priority DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items)
		Success(c, gin.H{"items": items, "total": total})
	})

	// GET /api/v1/data-sources/:id
	ds.GET("/:id", func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		var item models.DataSource
		if err := db.First(&item, id).Error; err != nil {
			Error(c, http.StatusNotFound, "not found")
			return
		}
		Success(c, item)
	})

	// POST /api/v1/data-sources
	ds.POST("", func(c *gin.Context) {
		var item models.DataSource
		if err := c.ShouldBindJSON(&item); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		if err := db.Create(&item).Error; err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		Success(c, item)
	})

	// PUT /api/v1/data-sources/:id
	ds.PUT("/:id", func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		var item models.DataSource
		if err := db.First(&item, id).Error; err != nil {
			Error(c, http.StatusNotFound, "")
			return
		}
		if err := c.ShouldBindJSON(&item); err != nil {
			Error(c, http.StatusBadRequest, "")
			return
		}
		db.Model(&item).Where("id = ?", id).Updates(item)
		Success(c, item)
	})

	// DELETE /api/v1/data-sources/:id
	ds.DELETE("/:id", func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		if err := db.Delete(&models.DataSource{}, id).Error; err != nil {
			Error(c, http.StatusInternalServerError, "")
			return
		}
		SuccessMsg(c, nil, "deleted")
	})

	// POST /api/v1/data-sources/:id/activate
	ds.POST("/:id/activate", func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		db.Model(&models.DataSource{}).Where("id = ?", id).Update("status", "active")
		Success(c, nil)
	})

	// POST /api/v1/data-sources/:id/deactivate
	ds.POST("/:id/deactivate", func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		db.Model(&models.DataSource{}).Where("id = ?", id).Update("status", "disabled")
		Success(c, nil)
	})

	// POST /api/v1/data-sources/:id/reset
	ds.POST("/:id/reset", func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		db.Model(&models.DataSource{}).Where("id = ?", id).Updates(map[string]interface{}{
			"status": "active",
		})
		Success(c, nil)
	})

	// GET /api/v1/data-sources/:id/health
	ds.GET("/:id/health", func(c *gin.Context) {
		Success(c, []interface{}{})
	})
}
