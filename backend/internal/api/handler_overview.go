package api

import (
	"net/http"

	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func registerOverviewRoutes(v1 *gin.RouterGroup, db *gorm.DB) {
	v1.GET("/overview", func(c *gin.Context) {
		var nodeTotal int64
		var nodeOnline int64
		var edgeDeviceTotal int64
		var edgeDeviceOnline int64
		db.Model(&models.Node{}).Count(&nodeTotal)
		db.Model(&models.Node{}).Where("status = ?", "online").Count(&nodeOnline)
		db.Model(&models.EdgeDevice{}).Count(&edgeDeviceTotal)
		db.Model(&models.EdgeDevice{}).Where("status = ?", "online").Count(&edgeDeviceOnline)
		c.JSON(http.StatusOK, gin.H{
			"nodes":        gin.H{"total": nodeTotal, "online": nodeOnline, "offline": nodeTotal - nodeOnline},
			"edge_devices": gin.H{"total": edgeDeviceTotal, "online": edgeDeviceOnline, "offline": edgeDeviceTotal - edgeDeviceOnline},
		})
	})
}
