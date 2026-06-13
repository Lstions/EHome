package api

import (
	"github.com/gin-gonic/gin"
)

func registerHARoutes(v1 *gin.RouterGroup) {
	ha := v1.Group("/ha")

	// POST /api/v1/ha/sync/:deviceId
	ha.POST("/sync/:deviceId", func(c *gin.Context) {
		Success(c, gin.H{"status": "synced"})
	})

	// POST /api/v1/ha/sync/node/:nodeId
	ha.POST("/sync/node/:nodeId", func(c *gin.Context) {
		Success(c, gin.H{"status": "synced"})
	})

	// POST /api/v1/ha/sync/all
	ha.POST("/sync/all", func(c *gin.Context) {
		Success(c, gin.H{"status": "syncing_all"})
	})

	// DELETE /api/v1/ha/device/:deviceId
	ha.DELETE("/device/:deviceId", func(c *gin.Context) {
		Success(c, nil)
	})
}
