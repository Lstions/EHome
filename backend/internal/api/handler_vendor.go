package api

import (
	"net/http"
	"strconv"

	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// registerVendorRoutes 注册「厂商 / 设备型号 / 设备类别」这一组遗留端点。
//
// 保留原因（而不是直接删除）：
//  1. 这组端点有完整的路由级测试（handler_ota_vendor_user_test.go 的 TestVendor_* /
//     TestDeviceModel_* / TestDeviceCategories_List，以及 handler_p0_error_semantics_test.go
//     的 P0-4 状态码回归护栏），删除会让这些护栏一起消失；端点本身无副作用、不参与采集链路。
//  2. 型号库将来仍可能被复用（例如做设备型号选择器）。前提是它重新变得需要，
//     而不是"既然有就先留着"。
//
// 为什么现在没有前端消费：该体系已被 v2.2 的 DeviceConfig 取代——设备连接/解析/初始化/操作
// 改由 models.DeviceConfig 的 Connection / Parser / InitFlow / Operations 四个 JSONB 字段承载
// （见 models/models.go:216-220），models.Vendor / models.DeviceModel 自身也被标注为「(保留)」
// （models/models.go:423/431）。Device 上的 DeviceModelID *uint（models/models.go:223）全仓零读写，
// 同样是这套遗留关联的残骸。
//
// 配套的前端客户端 frontend-shared/src/api/vendor.ts（vendorApi / deviceModelApi /
// deviceCategoryApi）已于清理中删除：它零消费，留着只会造成"有 api 却无页面"的误导性半成品状态。
//
// 若确认不再需要型号库：请连同 models.Vendor / models.DeviceModel 以及上述测试一并删除，
// 不要只删端点而留下无人引用的表结构。
func registerVendorRoutes(v1 *gin.RouterGroup, db *gorm.DB) {
	// === Vendors ===
	v1.GET("/vendors", func(c *gin.Context) {
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
		var vendors []models.Vendor
		var total int64
		db.Model(&models.Vendor{}).Count(&total)
		db.Offset((page - 1) * pageSize).Limit(pageSize).Find(&vendors)
		Success(c, gin.H{"items": vendors, "total": total, "page": page, "page_size": pageSize})
	})
	v1.GET("/vendors/:id", func(c *gin.Context) {
		var vendor models.Vendor
		if err := db.First(&vendor, c.Param("id")).Error; err != nil {
			Error(c, http.StatusNotFound, "")
			return
		}
		Success(c, vendor)
	})
	v1.POST("/vendors", func(c *gin.Context) {
		var dto struct {
			Name string `json:"name" binding:"required"`
		}
		if err := c.ShouldBindJSON(&dto); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		vendor := models.Vendor{Name: dto.Name}
		if err := db.Create(&vendor).Error; err != nil {
			Error(c, http.StatusInternalServerError, "failed to create vendor")
			return
		}
		SuccessWithCode(c, http.StatusCreated, vendor)
	})
	v1.PUT("/vendors/:id", func(c *gin.Context) {
		var vendor models.Vendor
		if err := db.First(&vendor, c.Param("id")).Error; err != nil {
			Error(c, http.StatusNotFound, "")
			return
		}
		var dto struct {
			Name *string `json:"name"`
		}
		c.ShouldBindJSON(&dto)
		updates := map[string]interface{}{}
		if dto.Name != nil {
			updates["name"] = *dto.Name
		}
		if len(updates) > 0 {
			db.Model(&vendor).Updates(updates)
		}
		Success(c, vendor)
	})
	v1.DELETE("/vendors/:id", func(c *gin.Context) {
		db.Delete(&models.Vendor{}, c.Param("id"))
		Success(c, nil)
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
		Success(c, gin.H{"items": deviceModels, "total": total, "page": page, "page_size": pageSize})
	})
	v1.GET("/device-models/:id", func(c *gin.Context) {
		var dm models.DeviceModel
		if err := db.First(&dm, c.Param("id")).Error; err != nil {
			Error(c, http.StatusNotFound, "")
			return
		}
		Success(c, dm)
	})
	v1.POST("/device-models", func(c *gin.Context) {
		var dto struct {
			Name     string `json:"name" binding:"required"`
			Type     string `json:"type" binding:"required"`
			VendorID uint   `json:"vendor_id" binding:"required"`
			Fields   string `json:"fields"`
		}
		if err := c.ShouldBindJSON(&dto); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		dm := models.DeviceModel{Name: dto.Name, Type: dto.Type, VendorID: dto.VendorID, Fields: dto.Fields}
		db.Create(&dm)
		SuccessWithCode(c, http.StatusCreated, dm)
	})
	v1.PUT("/device-models/:id", func(c *gin.Context) {
		var dm models.DeviceModel
		if err := db.First(&dm, c.Param("id")).Error; err != nil {
			Error(c, http.StatusNotFound, "")
			return
		}
		var dto struct {
			Name     *string `json:"name"`
			Type     *string `json:"type"`
			VendorID *uint   `json:"vendor_id"`
			Fields   *string `json:"fields"`
		}
		c.ShouldBindJSON(&dto)
		updates := map[string]interface{}{}
		if dto.Name != nil {
			updates["name"] = *dto.Name
		}
		if dto.Type != nil {
			updates["type"] = *dto.Type
		}
		if dto.VendorID != nil {
			updates["vendor_id"] = *dto.VendorID
		}
		if dto.Fields != nil {
			updates["fields"] = *dto.Fields
		}
		if len(updates) > 0 {
			db.Model(&dm).Updates(updates)
		}
		Success(c, dm)
	})
	v1.DELETE("/device-models/:id", func(c *gin.Context) {
		db.Delete(&models.DeviceModel{}, c.Param("id"))
		Success(c, nil)
	})

	// Device model fields (definitions)
	v1.GET("/device-models/:id/fields", func(c *gin.Context) {
		var dm models.DeviceModel
		if err := db.First(&dm, c.Param("id")).Error; err != nil {
			Error(c, http.StatusNotFound, "")
			return
		}
		Success(c, dm.Fields)
	})
	v1.PUT("/device-models/:id/fields", func(c *gin.Context) {
		var dm models.DeviceModel
		if err := db.First(&dm, c.Param("id")).Error; err != nil {
			Error(c, http.StatusNotFound, "")
			return
		}
		var req struct {
			Fields string `json:"fields"`
		}
		c.ShouldBindJSON(&req)
		db.Model(&dm).Update("fields", req.Fields)
		Success(c, nil)
	})

	// === Device Categories (distinct type values) ===
	v1.GET("/device-categories", func(c *gin.Context) {
		var categories []string
		db.Model(&models.DeviceModel{}).Distinct("type").Pluck("type", &categories)
		Success(c, categories)
	})
}
