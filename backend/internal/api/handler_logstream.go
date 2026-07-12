package api

import (
	"fmt"
	"net/http"
	"strconv"

	"ehome/backend/internal/models"
	"ehome/backend/internal/nodemgr"
	"ehome/backend/pkg/logger"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// registerLogStreamRoutes registers log stream API routes under /nodes/:id/
func registerLogStreamRoutes(n *gin.RouterGroup, db *gorm.DB, nodeMgr *nodemgr.Manager) {
	// GET /api/v1/nodes/:id/log-config — get current log stream config
	n.GET("/:id/log-config", getLogConfig(db))

	// PUT /api/v1/nodes/:id/log-config — update log stream config (stream_enabled, level)
	// Triggers config sync to ESP32
	n.PUT("/:id/log-config", updateLogConfig(db, nodeMgr))

	// PUT /api/v1/nodes/:id/log-persist — enable/disable DB persistence (pure backend)
	n.PUT("/:id/log-persist", updateLogPersist(db, nodeMgr))

	// GET /api/v1/nodes/:id/logs — query persisted logs with filters
	n.GET("/:id/logs", getNodeLogs(db))

	// DELETE /api/v1/nodes/:id/logs — delete logs (all or before timestamp)
	n.DELETE("/:id/logs", deleteNodeLogs(db))
}

func getLogConfig(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		node, err := findNodeByID(db, c.Param("id"))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"stream_enabled":   node.LogStreamEnabled,
			"level":            node.LogStreamLevel,
			"persist_enabled":  node.LogPersistEnabled,
		})
	}
}

func updateLogConfig(db *gorm.DB, nodeMgr *nodemgr.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		node, err := findNodeByID(db, c.Param("id"))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
			return
		}

		var req struct {
			StreamEnabled *bool `json:"stream_enabled"`
			Level         *int  `json:"level"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
			return
		}

		updates := map[string]interface{}{}
		if req.StreamEnabled != nil {
			updates["log_stream_enabled"] = *req.StreamEnabled
		}
		if req.Level != nil {
			if *req.Level < 0 || *req.Level > 4 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "level must be 0-4"})
				return
			}
			updates["log_stream_level"] = *req.Level
		}

		if len(updates) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "no fields to update"})
			return
		}

		if err := db.Model(&models.Node{}).Where("node_id = ?", node.NodeID).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update config"})
			return
		}

		// Trigger config sync to push new manifest (with log_stream field) to ESP32
		if err := nodeMgr.TriggerConfigSync(node.NodeID); err != nil {
			logger.Warnf("Failed to trigger config sync for %s: %v", node.NodeID, err)
		}

		c.JSON(http.StatusOK, gin.H{
			"message":         "log config updated, config sync triggered",
			"stream_enabled":  updates["log_stream_enabled"],
			"level":           updates["log_stream_level"],
		})
	}
}

func updateLogPersist(db *gorm.DB, nodeMgr *nodemgr.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		node, err := findNodeByID(db, c.Param("id"))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
			return
		}

		var req struct {
			Enabled *bool `json:"enabled"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
			return
		}

		if req.Enabled == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "enabled field required"})
			return
		}

		if err := db.Model(&models.Node{}).Where("node_id = ?", node.NodeID).Update("log_persist_enabled", *req.Enabled).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update persist config"})
			return
		}

		// Toggle DB consumer via node manager
		nodeMgr.SetLogPersist(node.NodeID, *req.Enabled)

		c.JSON(http.StatusOK, gin.H{
			"message": fmt.Sprintf("log persistence %s", map[bool]string{true: "enabled", false: "disabled"}[*req.Enabled]),
			"persist_enabled": *req.Enabled,
		})
	}
}

func getNodeLogs(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		node, err := findNodeByID(db, c.Param("id"))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
			return
		}

		// Parse query params
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		size, _ := strconv.Atoi(c.DefaultQuery("size", "100"))
		if page < 1 { page = 1 }
		if size < 1 || size > 1000 { size = 100 }

		query := db.Model(&models.NodeLog{}).Where("node_id = ?", node.NodeID)

		// Time range filter
		if fromStr := c.Query("from"); fromStr != "" {
			if from, err := strconv.ParseInt(fromStr, 10, 64); err == nil {
				query = query.Where("ts >= ?", from)
			}
		}
		if toStr := c.Query("to"); toStr != "" {
			if to, err := strconv.ParseInt(toStr, 10, 64); err == nil {
				query = query.Where("ts <= ?", to)
			}
		}

		// Level filter
		if levelStr := c.Query("level"); levelStr != "" {
			if level, err := strconv.Atoi(levelStr); err == nil {
				query = query.Where("level = ?", level)
			}
		}

		// Tag filter
		if tag := c.Query("tag"); tag != "" {
			query = query.Where("tag = ?", tag)
		}

		// Keyword search
		if q := c.Query("q"); q != "" {
			query = query.Where("message ILIKE ?", "%"+q+"%")
		}

		// Count total
		var total int64
		query.Count(&total)

		// Paginate (ORDER BY ts DESC)
		var logs []models.NodeLog
		query.Order("ts DESC").Offset((page - 1) * size).Limit(size).Find(&logs)

		c.JSON(http.StatusOK, gin.H{
			"total": total,
			"page":  page,
			"size":  size,
			"logs":  logs,
		})
	}
}

func deleteNodeLogs(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		node, err := findNodeByID(db, c.Param("id"))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
			return
		}

		query := db.Where("node_id = ?", node.NodeID)

		// Optional: delete before timestamp
		if beforeStr := c.Query("before"); beforeStr != "" {
			if before, err := strconv.ParseInt(beforeStr, 10, 64); err == nil {
				query = query.Where("ts < ?", before)
			}
		}

		result := query.Delete(&models.NodeLog{})
		c.JSON(http.StatusOK, gin.H{
			"deleted": result.RowsAffected,
		})
	}
}
