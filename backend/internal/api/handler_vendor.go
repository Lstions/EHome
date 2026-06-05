package api

import (
	"net/http"
	"strconv"

	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func registerVendorRoutes(v1 *gin.RouterGroup, db *gorm.DB) {
	// === Vendors ===
	v1.GET("/vendors", func(c *gin.Context) {
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
		var vendors []models.Vendor
		var total int64
		db.Model(&models.Vendor{}).Count(&total)
		db.Offset((page - 1) * pageSize).Limit(pageSize).Find(&vendors)
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": gin.H{"items": vendors, "total": total, "page": page, "page_size": pageSize}})
	})
	v1.GET("/vendors/:id", func(c *gin.Context) {
		var vendor models.Vendor
		if err := db.First(&vendor, c.Param("id")).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"code": 404})
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": vendor})
	})
	v1.POST("/vendors", func(c *gin.Context) {
		var vendor models.Vendor
		if err := c.ShouldBindJSON(&vendor); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": err.Error()})
			return
		}
		db.Create(&vendor)
		c.JSON(http.StatusCreated, gin.H{"code": 201, "data": vendor})
	})
	v1.PUT("/vendors/:id", func(c *gin.Context) {
		var vendor models.Vendor
		if err := db.First(&vendor, c.Param("id")).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"code": 404})
			return
		}
		var req map[string]interface{}
		c.ShouldBindJSON(&req)
		db.Model(&vendor).Updates(req)
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": vendor})
	})
	v1.DELETE("/vendors/:id", func(c *gin.Context) {
		db.Delete(&models.Vendor{}, c.Param("id"))
		c.JSON(http.StatusOK, gin.H{"code": 200})
	})

	// === Device Models ===
	v1.GET("/device-models", func(c *gin.Context) {
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
		vendorID := c.Query("vendor_id")
		var deviceModels []models.DeviceModel
		var total int64
		q := db.Model(&models.DeviceModel{})
		if vendorID != "" {
			q = q.Where("vendor_id = ?", vendorID)
		}
		q.Count(&total)
		q.Offset((page - 1) * pageSize).Limit(pageSize).Find(&deviceModels)
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": gin.H{"items": deviceModels, "total": total, "page": page, "page_size": pageSize}})
	})
	v1.GET("/device-models/:id", func(c *gin.Context) {
		var dm models.DeviceModel
		if err := db.First(&dm, c.Param("id")).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"code": 404})
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": dm})
	})
	v1.POST("/device-models", func(c *gin.Context) {
		var dm models.DeviceModel
		if err := c.ShouldBindJSON(&dm); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": err.Error()})
			return
		}
		db.Create(&dm)
		c.JSON(http.StatusCreated, gin.H{"code": 201, "data": dm})
	})
	v1.PUT("/device-models/:id", func(c *gin.Context) {
		var dm models.DeviceModel
		if err := db.First(&dm, c.Param("id")).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"code": 404})
			return
		}
		var req map[string]interface{}
		c.ShouldBindJSON(&req)
		db.Model(&dm).Updates(req)
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": dm})
	})
	v1.DELETE("/device-models/:id", func(c *gin.Context) {
		db.Delete(&models.DeviceModel{}, c.Param("id"))
		c.JSON(http.StatusOK, gin.H{"code": 200})
	})

	// Device model fields (definitions)
	v1.GET("/device-models/:id/fields", func(c *gin.Context) {
		var dm models.DeviceModel
		if err := db.First(&dm, c.Param("id")).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"code": 404})
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": dm.Fields})
	})
	v1.PUT("/device-models/:id/fields", func(c *gin.Context) {
		var dm models.DeviceModel
		if err := db.First(&dm, c.Param("id")).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"code": 404})
			return
		}
		var req struct {
			Fields string `json:"fields"`
		}
		c.ShouldBindJSON(&req)
		db.Model(&dm).Update("fields", req.Fields)
		c.JSON(http.StatusOK, gin.H{"code": 200})
	})

	// === Device Categories (distinct type values) ===
	v1.GET("/device-categories", func(c *gin.Context) {
		var categories []string
		db.Model(&models.DeviceModel{}).Distinct("type").Pluck("type", &categories)
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": categories})
	})
}
