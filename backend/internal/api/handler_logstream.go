package api

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/nodemgr"
	"ehome/backend/pkg/logger"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// registerLogStreamRoutes registers log stream API routes under /nodes/:id/
func registerLogStreamRoutes(n *gin.RouterGroup, db *gorm.DB, nodeMgr *nodemgr.Manager) {
	// Every operational log endpoint is an administrator action: logs can expose
	// topology, configuration, and error detail, and configuration/deletion mutate state.
	admin := n.Group("", RequireRole("admin"))
	admin.GET("/:id/log-config", getLogConfig(db))
	admin.PUT("/:id/log-config", updateLogConfig(db, nodeMgr))
	admin.PUT("/:id/log-persist", updateLogPersist(db, nodeMgr))
	admin.GET("/:id/logs", getNodeLogs(db))
	admin.DELETE("/:id/logs", deleteNodeLogs(db))
}

func getLogConfig(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		node, err := findNodeByID(db, c.Param("id"))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"stream_enabled":  node.LogStreamEnabled,
			"level":           node.LogStreamLevel,
			"persist_enabled": node.LogPersistEnabled,
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

		// Trigger config sync to push new manifest (with log_stream field) to ESP32.
		// A nil manager is valid in API-only tests and offline maintenance tools.
		if nodeMgr != nil {
			if err := nodeMgr.TriggerConfigSync(node.NodeID); err != nil {
				logger.Warnf("Failed to trigger config sync for %s: %v", node.NodeID, err)
			}
		}

		c.JSON(http.StatusOK, gin.H{
			"message":        "log config updated, config sync triggered",
			"stream_enabled": updates["log_stream_enabled"],
			"level":          updates["log_stream_level"],
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
			"message":         fmt.Sprintf("log persistence %s", map[bool]string{true: "enabled", false: "disabled"}[*req.Enabled]),
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
		if page < 1 {
			page = 1
		}
		if size < 1 || size > 1000 {
			size = 100
		}

		query := db.Model(&models.NodeLog{}).Where("node_id = ?", node.NodeID)

		// created_at is server receipt time. ESP Ts is monotonic uptime, not wall
		// time, so it cannot be used to filter or order historical records.
		if fromStr := c.Query("from"); fromStr != "" {
			if from, err := parseLogTimeMillis(fromStr); err == nil {
				query = query.Where("created_at >= ?", from)
			} else {
				c.JSON(http.StatusBadRequest, gin.H{"error": "from must be Unix milliseconds or RFC3339"})
				return
			}
		}
		if toStr := c.Query("to"); toStr != "" {
			if to, err := parseLogTimeMillis(toStr); err == nil {
				query = query.Where("created_at <= ?", to)
			} else {
				c.JSON(http.StatusBadRequest, gin.H{"error": "to must be Unix milliseconds or RFC3339"})
				return
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
			// LOWER + LIKE works on PostgreSQL and SQLite test databases; ILIKE is
			// PostgreSQL-only and made this query path untestable in SQLite.
			query = query.Where("LOWER(message) LIKE LOWER(?)", "%"+q+"%")
		}

		// Count total
		var total int64
		if err := query.Count(&total).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to count logs"})
			return
		}

		// Paginate by server receipt time. Ts is retained only as ESP uptime metadata.
		var logs []models.NodeLog
		if err := query.Order("created_at DESC").Order("id DESC").Offset((page - 1) * size).Limit(size).Find(&logs).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to query logs"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"total": total,
			"page":  page,
			"size":  size,
			"logs":  logs,
		})
	}
}

func parseLogTimeMillis(value string) (time.Time, error) {
	if millis, err := strconv.ParseInt(value, 10, 64); err == nil {
		return time.UnixMilli(millis).UTC(), nil
	}
	return time.Parse(time.RFC3339, value)
}

func deleteNodeLogs(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		node, err := findNodeByID(db, c.Param("id"))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
			return
		}

		query := db.Where("node_id = ?", node.NodeID)

		// Optional: delete entries received before a server wall-clock timestamp.
		if beforeStr := c.Query("before"); beforeStr != "" {
			before, err := parseLogTimeMillis(beforeStr)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "before must be Unix milliseconds or RFC3339"})
				return
			}
			query = query.Where("created_at < ?", before)
		}

		result := query.Delete(&models.NodeLog{})
		c.JSON(http.StatusOK, gin.H{
			"deleted": result.RowsAffected,
		})
	}
}
