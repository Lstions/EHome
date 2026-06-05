package api

import (
	"encoding/hex"
	"net/http"
	"strconv"

	"ehome/backend/internal/nodemgr"

	"github.com/gin-gonic/gin"
)

// registerTerminalRoutes sets up channel terminal history + write routes
func registerTerminalRoutes(v1 *gin.RouterGroup, nodeMgr *nodemgr.Manager) {
	// Get terminal history
	v1.GET("/channels/:channel_id/terminal", func(c *gin.Context) {
		channelID, _ := strconv.Atoi(c.Param("channel_id"))
		count, _ := strconv.Atoi(c.DefaultQuery("count", "50"))
		entries := nodeMgr.TerminalMgr().GetHistory(uint(channelID), count)
		c.JSON(http.StatusOK, gin.H{
			"channel_id": channelID,
			"count":      len(entries),
			"entries":    entries,
		})
	})

	// Send write command via terminal
	v1.POST("/channels/:channel_id/terminal/write", func(c *gin.Context) {
		channelID, _ := strconv.Atoi(c.Param("channel_id"))
		var req struct {
			DeviceID string `json:"device_id" binding:"required"`
			DataHex  string `json:"data_hex" binding:"required"`
			ReadSize int    `json:"read_size"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		data, err := hex.DecodeString(req.DataHex)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid hex data"})
			return
		}
		var readSize uint32
		if req.ReadSize > 0 {
			readSize = uint32(req.ReadSize)
		}
		if err := nodeMgr.SendWriteCommand(req.DeviceID, uint32(channelID), data, readSize); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"message":    "command sent",
			"channel_id": channelID,
			"data_hex":   req.DataHex,
			"read_size":  readSize,
		})
	})
}
