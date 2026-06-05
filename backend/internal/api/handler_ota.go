package api

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"ehome/backend/internal/collector"
	"ehome/backend/internal/models"
	"ehome/backend/internal/ota"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// registerOTARoutes sets up OTA task + firmware routes
func registerOTARoutes(v1 *gin.RouterGroup, db *gorm.DB, otaMgr *ota.Manager, collectorMgr *collector.Manager) {
	// List OTA tasks
	v1.GET("/ota/tasks", func(c *gin.Context) {
		var tasks []models.OTATask
		db.Find(&tasks)
		c.JSON(http.StatusOK, tasks)
	})

	// Create OTA task + send OtaCmd to device
	v1.POST("/ota/tasks", func(c *gin.Context) {
		var req struct {
			NodeID uint `json:"node_id" binding:"required"`
			FirmwareID  uint `json:"firmware_id" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		task, err := otaMgr.CreateTask(req.NodeID, req.FirmwareID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		// Send OtaCmd to device (fire-and-forget via MQTT)
		// Task stays "pending" until device confirms with OtaProg(status=0)
		if err := otaMgr.SendOtaCommand(task); err != nil {
			// MQTT publish failed — mark task as failed
			task.Status = "failed"
			task.ErrorMsg = fmt.Sprintf("send failed: %v", err)
			db.Save(task)
			c.JSON(http.StatusInternalServerError, gin.H{"error": task.ErrorMsg})
			return
		}

		c.JSON(http.StatusCreated, task)
	})

	// Get OTA task status
	v1.GET("/ota/tasks/:id", func(c *gin.Context) {
		id := c.Param("id")
		var task models.OTATask
		if err := db.First(&task, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
			return
		}
		c.JSON(http.StatusOK, task)
	})

	// Cancel OTA task
	// - POST /api/v1/ota/tasks/:id/cancel
	v1.POST("/ota/tasks/:id/cancel", func(c *gin.Context) {
		id := c.Param("id")
		taskID, err := strconv.ParseUint(id, 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "invalid task id"})
			return
		}
		if err := otaMgr.CancelTask(uint(taskID)); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "cancelled", "data": gin.H{"id": taskID}})
	})

	// List firmwares
	v1.GET("/firmwares", func(c *gin.Context) {
		var firmwares []models.Firmware
		db.Find(&firmwares)
		c.JSON(http.StatusOK, firmwares)
	})

	// Upload firmware .bin file
	v1.POST("/firmwares/upload", func(c *gin.Context) {
		version := c.PostForm("version")
		if version == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "version required"})
			return
		}

		file, err := c.FormFile("file")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "file required"})
			return
		}

		// Save to firmware dir
		fwDir := "firmwares"
		os.MkdirAll(fwDir, 0755)
		filename := filepath.Base(file.Filename)
		dst := filepath.Join(fwDir, filename)
		if err := c.SaveUploadedFile(file, dst); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		// Calculate SHA256 checksum
		data, _ := os.ReadFile(dst)
		hash := sha256.Sum256(data)
		checksum := fmt.Sprintf("%x", hash)

		// Build download URL (ESP32 will fetch from this URL)
		// Use configurable external address, fallback to request host
		extHost := os.Getenv("EHOME_EXTERNAL_HOST")
		if extHost == "" {
			extHost = c.Request.Host
		}
		url := fmt.Sprintf("http://%s/api/v1/firmwares/%s/download", extHost, filename)

		fw := models.Firmware{
			Version:   version,
			Checksum:  checksum,
			SizeBytes: uint64(len(data)),
			URL:       url,
		}
		if err := db.Create(&fw).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusCreated, fw)
	})

	// Download firmware .bin file (ESP32 fetches from here)
	v1.GET("/firmwares/:filename/download", func(c *gin.Context) {
		filename := filepath.Base(c.Param("filename")) // prevent path traversal
		dst := filepath.Join("firmwares", filename)
		if _, err := os.Stat(dst); os.IsNotExist(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "firmware not found"})
			return
		}
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))
		c.File(dst)
	})

	// Delete firmware by id
	// - DELETE /api/v1/firmwares/:id
	v1.DELETE("/firmwares/:id", func(c *gin.Context) {
		id := c.Param("id")
		var fw models.Firmware
		if err := db.First(&fw, id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "firmware not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}
		// Also remove the .bin file from disk
		if fw.URL != "" {
			binary := filepath.Base(fw.URL)
			_ = os.Remove(filepath.Join("firmwares", binary))
		}
		if err := db.Delete(&fw).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "deleted", "data": gin.H{"id": id, "version": fw.Version}})
	})

	// List notifications
	v1.GET("/notifications", func(c *gin.Context) {
		var notifications []models.Notification
		db.Order("created_at DESC").Find(&notifications)
		c.JSON(http.StatusOK, notifications)
	})
}
