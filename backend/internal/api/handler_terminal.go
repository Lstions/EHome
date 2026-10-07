package api

import (
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"

	"ehome/backend/internal/nodemgr"
	"ehome/backend/internal/terminal"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func validatedTerminalWriteSender(db *gorm.DB, send terminal.WriteSender) terminal.WriteSender {
	return func(deviceID string, channelID uint32, data []byte, readSize uint32) error {
		if _, err := loadTransportChannel(db, uint(channelID), deviceID); err != nil {
			return err
		}
		if send == nil {
			return fmt.Errorf("terminal write sender is unavailable")
		}
		return send(deviceID, channelID, data, readSize)
	}
}

// registerTerminalRoutes sets up channel terminal history + write routes
func registerTerminalRoutes(v1 *gin.RouterGroup, db *gorm.DB, nodeMgr *nodemgr.Manager, policies ...ControlPolicy) {
	controlPolicy := resolveControlPolicy(policies...)
	// Get terminal history
	v1.GET("/channels/:channel_id/terminal", func(c *gin.Context) {
		if !controlPolicy.rawWritesEnabled() {
			c.JSON(http.StatusGone, gin.H{"error": "raw terminal diagnostics are disabled"})
			return
		}
		channelID, _ := strconv.Atoi(c.Param("channel_id"))
		// count 必须归一 (2026-10-07): 改前是 `count, _ := strconv.Atoi(...)` 直接透传,
		// 于是 ?count=-1 (或任何负值) 会让 term.History(-1) 走到
		// `make([]Entry, 0, -1)` ⇒ **panic: makeslice: cap out of range** ⇒ 该请求 500 且
		// gin 的 recover 会打一整段栈。已实测复现该 panic, 不是理论推演。
		// 上限取 ringBufferSize(256): 历史缓冲本身就是 256 条, 请求超过它也只是同一批数据。
		count, err := strconv.Atoi(c.DefaultQuery("count", "50"))
		if err != nil || count < 1 || count > 256 {
			count = 50
		}
		entries := nodeMgr.TerminalMgr().GetHistory(uint(channelID), count)
		c.JSON(http.StatusOK, gin.H{
			"channel_id": channelID,
			"count":      len(entries),
			"entries":    entries,
		})
	})

	// Send write command via terminal
	v1.POST("/channels/:channel_id/terminal/write", func(c *gin.Context) {
		if !controlPolicy.rawWritesEnabled() {
			c.JSON(http.StatusGone, gin.H{"error": "raw terminal writes are disabled; use an audited diagnostics service"})
			return
		}

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
		if _, err := loadTransportChannel(db, uint(channelID), req.DeviceID); err != nil {
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
