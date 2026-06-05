package api

import (
	"net/http"
	"strconv"

	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func registerDataReportRoutes(v1 *gin.RouterGroup, db *gorm.DB) {
	// GET /api/v1/data-reports — recent UnifiedData records
	v1.GET("/data-reports", func(c *gin.Context) {
		limit := 20
		if l := c.Query("limit"); l != "" {
			if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 100 {
				limit = n
			}
		}
		var data []models.UnifiedData
		db.Order("created_at DESC").Limit(limit).Find(&data)
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": data})
	})
}
