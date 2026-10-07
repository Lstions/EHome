package api

import (
	"strconv"

	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func registerNotificationRoutes(v1 *gin.RouterGroup, db *gorm.DB) {
	// GET /notifications
	//
	// S4 修复 (2026-10-07): 与 /nodes/:id/data 同一缺陷族 —— 未校验的 limit
	// 在 -1 时让 GORM 丢弃 LIMIT 子句(返回全表)、在 0 或 "abc" 时写 LIMIT 0
	// (静默空页)。前端固定传 limit=10/20, 均落在 [1,200] 内, 不受影响。
	v1.GET("/notifications", func(c *gin.Context) {
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
		if limit <= 0 || limit > 200 {
			limit = 20
		}
		var notifs []models.Notification
		db.Order("created_at DESC").Limit(limit).Find(&notifs)
		Success(c, notifs)
	})

	// GET /notifications/unread-count
	v1.GET("/notifications/unread-count", func(c *gin.Context) {
		var count int64
		db.Model(&models.Notification{}).Where("read = ?", false).Count(&count)
		Success(c, gin.H{"count": count})
	})

	// PUT /notifications/:id/read
	v1.PUT("/notifications/:id/read", func(c *gin.Context) {
		id := c.Param("id")
		db.Model(&models.Notification{}).Where("id = ?", id).Update("read", true)
		Success(c, nil)
	})

	// POST /notifications/read-all
	v1.POST("/notifications/read-all", func(c *gin.Context) {
		db.Model(&models.Notification{}).Where("read = ?", false).Update("read", true)
		Success(c, nil)
	})
}
