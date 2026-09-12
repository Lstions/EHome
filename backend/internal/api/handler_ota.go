package api

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/nodemgr"
	"ehome/backend/internal/ota"
	"ehome/backend/pkg/logger"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// registerOTARoutes sets up OTA task + firmware routes
func registerOTARoutes(v1 *gin.RouterGroup, db *gorm.DB, otaMgr *ota.Manager, nodeMgr *nodemgr.Manager) {
	// ── New OTA status / rollback endpoints ──

	// GET /api/v1/ota/status/:nodeId — query node OTA status
	v1.GET("/ota/status/:nodeId", func(c *gin.Context) {
		nodeID := c.Param("nodeId")
		status, err := otaMgr.GetNodeOTAStatus(nodeID)
		if err != nil {
			Error(c, http.StatusNotFound, err.Error())
			return
		}
		Success(c, status)
	})

	// POST /api/v1/ota/rollback/:nodeId — manual rollback to last stable version
	v1.POST("/ota/rollback/:nodeId", func(c *gin.Context) {
		nodeID := c.Param("nodeId")
		if err := otaMgr.AutoRollback(nodeID); err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		SuccessMsg(c, gin.H{"node_id": nodeID}, "rollback initiated")
	})

	// ── Existing OTA task + firmware routes ──
	// List OTA tasks
	v1.GET("/ota/tasks", func(c *gin.Context) {
		var tasks []models.OTATask
		db.Find(&tasks)
		Success(c, tasks)
	})

	// Create OTA task + send OtaCmd to device
	v1.POST("/ota/tasks", func(c *gin.Context) {
		var req struct {
			NodeID     string `json:"node_id" binding:"required"`
			FirmwareID uint   `json:"firmware_id" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}

		task, err := otaMgr.CreateTask(req.NodeID, req.FirmwareID)
		if err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}

		// Send OtaCmd to device (fire-and-forget via MQTT)
		// Task stays "pending" until device confirms with OtaProg(status=0)
		if err := otaMgr.SendOtaCommand(task); err != nil {
			// MQTT publish failed — mark task as failed
			task.Status = "failed"
			task.ErrorMsg = fmt.Sprintf("send failed: %v", err)
			db.Save(task)
			Error(c, http.StatusInternalServerError, task.ErrorMsg)
			return
		}

		SuccessWithCode(c, http.StatusCreated, task)
	})

	// Get OTA task status
	v1.GET("/ota/tasks/:id", func(c *gin.Context) {
		id := c.Param("id")
		var task models.OTATask
		if err := db.First(&task, id).Error; err != nil {
			Error(c, http.StatusNotFound, "task not found")
			return
		}
		Success(c, task)
	})

	// Cancel OTA task
	// - POST /api/v1/ota/tasks/:id/cancel
	v1.POST("/ota/tasks/:id/cancel", func(c *gin.Context) {
		id := c.Param("id")
		taskID, err := strconv.ParseUint(id, 10, 64)
		if err != nil {
			Error(c, http.StatusBadRequest, "invalid task id")
			return
		}
		if err := otaMgr.CancelTask(uint(taskID)); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		SuccessMsg(c, gin.H{"id": taskID}, "cancelled")
	})

	// List firmwares
	v1.GET("/firmwares", func(c *gin.Context) {
		var firmwares []models.Firmware
		db.Find(&firmwares)
		Success(c, firmwares)
	})

	// Upload firmware .bin file
	v1.POST("/firmwares/upload", func(c *gin.Context) {
		// Limit request body to 4MB to prevent abuse
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4<<20)

		version := c.PostForm("version")
		if version == "" {
			Error(c, http.StatusBadRequest, "version required")
			return
		}

		file, err := c.FormFile("file")
		if err != nil {
			Error(c, http.StatusBadRequest, "file required")
			return
		}

		// Validate file extension
		filename := filepath.Base(file.Filename)
		if !strings.HasSuffix(strings.ToLower(filename), ".bin") {
			Error(c, http.StatusBadRequest, "only .bin firmware files are allowed")
			return
		}

		// Validate Content-Type (common types for binary uploads)
		contentType := file.Header.Get("Content-Type")
		allowedTypes := map[string]bool{
			"application/octet-stream": true,
			"application/x-binary":     true,
			"binary/octet-stream":      true,
			"":                         true, // Some clients omit Content-Type for multipart files
		}
		if !allowedTypes[contentType] {
			Error(c, http.StatusBadRequest, fmt.Sprintf("unsupported content type: %s", contentType))
			return
		}

		// Save to firmware dir
		fwDir := "firmwares"
		os.MkdirAll(fwDir, 0755)
		dst := filepath.Join(fwDir, filename)
		if err := c.SaveUploadedFile(file, dst); err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}

		// Calculate SHA256 checksum
		data, _ := os.ReadFile(dst)
		hash := sha256.Sum256(data)
		checksum := fmt.Sprintf("%x", hash)

		// Build download URL (ESP32 will fetch from this URL)
		// Use configurable external address, fallback to request host with warning
		extHost := os.Getenv("EHOME_EXTERNAL_HOST")
		if extHost == "" {
			if isDevelopmentMode() {
				logger.Warnf("EHOME_EXTERNAL_HOST not set, falling back to request Host header for firmware URL (potential Host Header Injection)")
				extHost = c.Request.Host
			} else {
				Error(c, http.StatusInternalServerError, "EHOME_EXTERNAL_HOST not configured; cannot generate firmware download URL")
				return
			}
		}
		baseURL := "http://" + extHost
		downloadURL := firmwareDownloadURL(url.PathEscape(filename), baseURL, 30*time.Minute, jwtSecret)

		fw := models.Firmware{
			Version:  version,
			Filename: filename,
			Checksum: checksum,
			// StoragePath is what DELETE /firmwares/:id uses to find the binary on
			// disk (URL is a signed download ticket, not a path). Filename keeps its
			// existing meaning (the uploaded base name); StoragePath records where
			// the bytes were actually written.
			SizeBytes:   uint64(len(data)),
			URL:         downloadURL,
			StoragePath: dst,
			TargetModel: c.PostForm("target_model"),
		}
		if err := db.Create(&fw).Error; err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		SuccessWithCode(c, http.StatusCreated, fw)
	})

	// Update firmware metadata
	// - PUT /api/v1/firmwares/:id
	v1.PUT("/firmwares/:id", func(c *gin.Context) {
		var firmware models.Firmware
		if err := db.First(&firmware, c.Param("id")).Error; err != nil {
			Error(c, http.StatusNotFound, "")
			return
		}
		var req struct {
			Version        *string `json:"version"`
			Changelog      *string `json:"changelog"`
			NodeModel      *string `json:"node_model"`
			MinFromVersion *string `json:"min_from_version"`
			Stable         *bool   `json:"stable"`
		}
		c.ShouldBindJSON(&req)
		updates := map[string]interface{}{}
		if req.Version != nil {
			updates["version"] = *req.Version
		}
		if req.Changelog != nil {
			updates["changelog"] = *req.Changelog
		}
		if req.NodeModel != nil {
			updates["target_model"] = *req.NodeModel
		}
		if req.MinFromVersion != nil {
			updates["min_from_version"] = *req.MinFromVersion
		}
		if req.Stable != nil {
			updates["stable"] = *req.Stable
		}
		db.Model(&firmware).Updates(updates)
		// Reload to return updated data
		db.First(&firmware, firmware.ID)
		Success(c, firmware)
	})

	// - DELETE /api/v1/firmwares/:id
	v1.DELETE("/firmwares/:id", func(c *gin.Context) {
		id := c.Param("id")
		var fw models.Firmware
		if err := db.First(&fw, id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				Error(c, http.StatusNotFound, "firmware not found")
				return
			}
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		// Also remove the .bin file from disk. The path must come from
		// StoragePath/Filename, never from URL (see firmwareBinaryDiskPath).
		if path := firmwareBinaryDiskPath(fw.StoragePath, fw.Filename); path != "" {
			if err := os.Remove(path); err != nil {
				if os.IsNotExist(err) {
					// Already gone (manual cleanup, restored DB, ...): not an error.
					logger.Debugf("firmware %d: binary already absent at %s", fw.ID, path)
				} else {
					// Never swallow this again: a silent os.Remove failure is exactly
					// why deleted firmwares kept occupying disk indefinitely.
					logger.Warnf("firmware %d: failed to remove binary at %s: %v", fw.ID, path, err)
				}
			}
		}
		if err := db.Delete(&fw).Error; err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		SuccessMsg(c, gin.H{"id": id, "version": fw.Version}, "deleted")
	})
}

// firmwareBinaryDiskPath resolves the on-disk path of an uploaded firmware binary
// from stored metadata. Firmware.URL must NOT be used for this: it is a signed
// download ticket of the form
// http://host/api/v1/firmwares/<name>.bin/download?expires=...&signature=...
// (see firmwareDownloadURL), so filepath.Base(URL) yields "download?expires=..."
// and the real file is never removed.
//
// Resolution order: StoragePath when set (written by the upload endpoint), else
// firmwares/<base(Filename)> for rows created before StoragePath existed. The
// candidate is re-based with filepath.Base before the final join so a corrupted or
// hostile DB value cannot escape the firmwares/ directory - the same guard the
// download endpoint applies. Returns "" when both fields are empty (nothing to
// delete, e.g. an externally hosted firmware).
func firmwareBinaryDiskPath(storagePath, filename string) string {
	path := strings.TrimSpace(storagePath)
	if path == "" {
		if strings.TrimSpace(filename) == "" {
			return ""
		}
		path = filepath.Join("firmwares", filepath.Base(filename))
	}
	return filepath.Join("firmwares", filepath.Base(path))
}

// RegisterFirmwareDownload registers the firmware download endpoint WITHOUT auth.
// ESP32 devices fetch firmware without JWT tokens.
func RegisterFirmwareDownload(r *gin.Engine) {
	r.GET("/api/v1/firmwares/:filename/download", func(c *gin.Context) {
		filename := filepath.Base(c.Param("filename")) // prevent path traversal
		if !validateFirmwareDownload(filename, c.Query("expires"), c.Query("signature"), jwtSecret, time.Now().UTC()) {
			Error(c, http.StatusUnauthorized, "invalid or expired firmware ticket")
			return
		}
		dst := filepath.Join("firmwares", filename)
		if _, err := os.Stat(dst); os.IsNotExist(err) {
			Error(c, http.StatusNotFound, "firmware not found")
			return
		}
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))
		c.File(dst)
	})
}
