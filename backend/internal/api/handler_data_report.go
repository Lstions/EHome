package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func registerDataReportRoutes(v1 *gin.RouterGroup, db *gorm.DB) {
	// GET /api/v1/data-reports — placeholder returning empty list
	v1.GET("/data-reports", func(c *gin.Context) {
		c.JSON(http.StatusOK, []interface{}{})
	})
}
