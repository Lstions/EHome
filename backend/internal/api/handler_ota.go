package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ehome/backend/internal/dbschema"
	"ehome/backend/internal/firmwareid"
	"ehome/backend/internal/firmwareticket"
	"ehome/backend/internal/models"
	"ehome/backend/internal/nodemgr"
	"ehome/backend/internal/ota"
	"ehome/backend/pkg/logger"
	"ehome/backend/pkg/metrics"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	// firmwareDir 是固件落盘目录，与启动迁移（dbschema）读取的目录同源。
	firmwareDir = dbschema.FirmwareDir
	// firmwareUploadMaxBytes 是 multipart 请求体上限（含 sidecar）。
	// 槽容量上限最大是 16MB 档的 6.5MB（firmwareid.SlotCapacity），留 ~1.5MB 余量；
	// 真正「超过该容量槽」的判定按镜像自报的 spi_size 做，返回 FW_TOO_LARGE。
	firmwareUploadMaxBytes = 8 << 20
	// firmwareMetaField 是同名 sidecar 的 multipart 字段名（方案 §4.2）。
	// 客户端也可以把 .meta.json 放在别的字段名下，按文件名兜底识别。
	firmwareMetaField = "meta"
	// firmwareMetaSuffix 是 sidecar 的固定后缀：ehome_collector.bin → ehome_collector.meta.json。
	firmwareMetaSuffix = ".meta.json"
	// firmwareMetaMaxBytes 防止用超大「sidecar」放大请求体。
	firmwareMetaMaxBytes = 64 << 10
	// firmwareTempDirName 是上传临时目录（相对 firmwares/）：
	// 所有校验在临时文件上做，通过后才 rename 到正式目录，失败即删（AC-6）。
	firmwareTempDirName = ".tmp"
)

// allowedFirmwareContentTypes 是 multipart 里 file part 允许的 Content-Type。
// 空值也算合法：部分客户端（含 Go 的 CreateFormFile）不填。
var allowedFirmwareContentTypes = map[string]bool{
	"application/octet-stream": true,
	"application/x-binary":     true,
	"binary/octet-stream":      true,
	"":                         true,
}

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
	//
	// AutoRollback 以 source=system 建任务（跳过版本方向判定），但**硬件类闸门照常**
	// 生效；稳定版本固件是 legacy / 硬件不匹配时返回 409（与建任务同一映射）。
	v1.POST("/ota/rollback/:nodeId", func(c *gin.Context) {
		nodeID := c.Param("nodeId")
		if err := otaMgr.AutoRollback(nodeID); err != nil {
			respondOTAManagerError(c, err)
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
	//
	// 闸门（方案 §6.3 / AC-8）：不兼容先 409 + data（reasons/node_hw/firmware/
	// forceable/fix）且**不产生任务行**；force 只对版本方向两条有效，必须带
	// force_reason 并落审计（REQ-8）。force/force_reason 是前端已在发的字段（DEF-13）。
	v1.POST("/ota/tasks", func(c *gin.Context) {
		var req struct {
			NodeID      string `json:"node_id" binding:"required"`
			FirmwareID  uint   `json:"firmware_id" binding:"required"`
			Force       bool   `json:"force"`
			ForceReason string `json:"force_reason"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}

		actor := newRequestActor(c, db)
		task, err := otaMgr.CreateTaskWithOptions(ota.CreateTaskOptions{
			NodeID:      req.NodeID,
			FirmwareID:  req.FirmwareID,
			Force:       req.Force,
			ForceReason: req.ForceReason,
			Source:      firmwareid.SourceUser,
			Actor:       actor.otaAudit(),
		})
		if err != nil {
			respondOTAManagerError(c, err)
			return
		}

		// Send OtaCmd to device; the task stays "pending" until the device confirms
		// with OtaProg(status=0).
		//
		// D2（沙箱实测）：下发失败不能留下「用户看不见的 pending」——客户端拿 5xx、
		// 库里却有一条 pending，30 分钟后被超时扫描改成 failed，重试还会再建一条。
		// 选定方案 (a)：状态与响应一致。任务行与 force 审计**先落库**（证据链，
		// 失败时绝不删除/回滚审计），下发失败把任务有条件推进到 failed，返回 503 +
		// data.task_id 让客户端能定位；重试是干净的新任务（旧任务已终态，不再参与抢占）。
		if sendErr := otaMgr.SendOtaCommand(task); sendErr != nil {
			msg := fmt.Sprintf("send failed: %v", sendErr)
			// 条件更新 + 回读真实状态在 Manager 里统一做（回滚路径共用）。
			status := otaMgr.MarkDeliveryFailed(task, sendErr)
			ErrorWithCodeData(c, http.StatusServiceUnavailable, "OTA_DELIVERY_FAILED", msg, gin.H{
				"task_id":   task.ID,
				"ota_id":    task.OtaID,
				"status":    status,
				"error_msg": msg,
			})
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
	//
	// 仅下载阶段 (pending/downloading) 可取消; 已进入 verifying/installing 或状态被
	// 并发改写时 CancelTask 返回 ErrTaskNotCancellable → HTTP 409 (而不是 400)。
	v1.POST("/ota/tasks/:id/cancel", func(c *gin.Context) {
		id := c.Param("id")
		taskID, err := strconv.ParseUint(id, 10, 64)
		if err != nil {
			Error(c, http.StatusBadRequest, "invalid task id")
			return
		}
		if err := otaMgr.CancelTask(uint(taskID)); err != nil {
			if errors.Is(err, ota.ErrTaskNotCancellable) {
				Error(c, http.StatusConflict, err.Error())
				return
			}
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		SuccessMsg(c, gin.H{"id": taskID}, "cancelled")
	})

	// List firmwares
	//
	// 方案 §4.3 / T1b.1：支持 node_id / chip / flash_mb / channel / q /
	// include_withdrawn；默认排除已撤回与软删；带 node_id 时逐行给出
	// compatible/reasons（与建任务闸门同源）。返回形状仍是 data: Firmware[]
	// （不做分页）。每行 URL 在响应时**重签**一次 30 分钟票（DEF-3/AC-11）。
	v1.GET("/firmwares", func(c *gin.Context) {
		query, err := parseFirmwareListQuery(c)
		if err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		items, err := queryFirmwares(c, db, query)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				Error(c, http.StatusNotFound, "node not found")
				return
			}
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		Success(c, items)
	})

	// 管理员取下载票（见 handler_firmware_download.go：公开下载路由只认 HMAC）。
	v1.POST("/firmwares/:id/download-ticket", firmwareDownloadTicketHandler(db))

	// Upload firmware .bin file
	//
	// 方案 §3.3 / §4.2：临时文件 → 解析镜像 → sidecar 交叉校验 → 按 elf_sha256 查重
	// → 唯一命名落盘 → 入库。任一步失败都删临时文件；入库失败连已落盘文件一起删，
	// 不留孤儿（AC-6 / DEF-14）。
	v1.POST("/firmwares/upload", func(c *gin.Context) {
		// 上限按 multipart 整体算：8MB 覆盖 16MB 档的 6.5MB 槽上限 + sidecar 余量。
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, firmwareUploadMaxBytes)

		// 表单**不再接收 version**（方案 §4.2）：版本一律取自镜像，少一条人工填错路径。
		file, err := c.FormFile("file")
		if err != nil {
			if isBodyTooLarge(err) {
				rejectFirmwareUpload(c, http.StatusRequestEntityTooLarge, "FW_TOO_LARGE", "firmware exceeds the maximum upload size")
				return
			}
			Error(c, http.StatusBadRequest, "file required")
			return
		}

		baseName := filepath.Base(file.Filename)
		if !strings.HasSuffix(strings.ToLower(baseName), ".bin") {
			Error(c, http.StatusBadRequest, "only .bin firmware files are allowed")
			return
		}
		if contentType := file.Header.Get("Content-Type"); !allowedFirmwareContentTypes[contentType] {
			Error(c, http.StatusBadRequest, fmt.Sprintf("unsupported content type: %s", contentType))
			return
		}

		baseURL, ok := firmwareBaseURLForRequest(c)
		if !ok {
			Error(c, http.StatusInternalServerError, "EHOME_EXTERNAL_HOST not configured; cannot generate firmware download URL")
			return
		}

		// 1) 收字节到临时文件，边写边算 SHA256。
		tmpPath, size, checksum, err := receiveFirmwareUpload(file)
		if err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		committed := false
		defer func() {
			if !committed {
				if rmErr := os.Remove(tmpPath); rmErr != nil && !os.IsNotExist(rmErr) {
					logger.Warnf("failed to remove rejected upload temp file %s: %v", tmpPath, rmErr)
				}
			}
		}()

		// 2) 解析镜像：身份以二进制自证为准。
		ident, err := readFirmwareIdentity(tmpPath, size)
		if err != nil {
			rejectFirmwareUpload(c, http.StatusBadRequest, firmwareid.Code(err), err.Error())
			return
		}

		// 3) sidecar 必填 + 与镜像/内容交叉校验。
		sidecarRaw, err := readFirmwareSidecar(c, baseName)
		if err != nil {
			rejectFirmwareUpload(c, http.StatusBadRequest, firmwareid.Code(err), err.Error())
			return
		}
		sidecar, err := firmwareid.ParseSidecar(sidecarRaw)
		if err != nil {
			rejectFirmwareUpload(c, http.StatusBadRequest, firmwareid.Code(err), err.Error())
			return
		}
		if err := firmwareid.CheckSidecar(sidecar, ident, checksum); err != nil {
			rejectFirmwareUpload(c, http.StatusBadRequest, firmwareid.Code(err), err.Error())
			return
		}

		// 4) 超过该容量槽上限：镜像自报 spi_size 决定槽位，不信任表单/文件名。
		if capacity, known := firmwareid.SlotCapacity(ident.FlashMB); known && uint64(size) > capacity {
			rejectFirmwareUpload(c, http.StatusRequestEntityTooLarge, "FW_TOO_LARGE",
				fmt.Sprintf("firmware is %d bytes, larger than the %dMB slot capacity %d", size, ident.FlashMB, capacity))
			return
		}

		// 5) 同 elf_sha256 的处置（方案 §5.5 + R3「未知即不可选」）：
		//    · verified / duplicate → 409 FW_DUPLICATE（同一份二进制只留一份可选记录）；
		//    · legacy → **就地补齐**。迁移把回填行一律标 legacy（镜像头没有 psram 位，
		//      拿不到 R3 要求的 psram ⇒ 不可选），而 elf_sha256 已写、唯一索引已建：
		//      没有这条分支的话重传必 409，legacy 永远补不回来，R3 的「补 sidecar 重传」
		//      就是死路一条。补齐保留原 id、不新增行。
		//    软删行不参与（GORM 软删自动排除：删除后允许重传）。
		var existing models.Firmware
		lookupErr := db.Where("elf_sha256 = ?", ident.ElfSHA256).First(&existing).Error
		if lookupErr != nil && !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
			Error(c, http.StatusInternalServerError, lookupErr.Error())
			return
		}
		completingLegacy := lookupErr == nil && existing.IdentityState == dbschema.IdentityLegacy
		if lookupErr == nil && !completingLegacy {
			rejectFirmwareUpload(c, http.StatusConflict, "FW_DUPLICATE",
				fmt.Sprintf("firmware with elf_sha256 %s already exists", ident.ElfSHA256))
			return
		}

		// 6) 唯一命名（elf 前缀）后落盘：同名重传不会覆盖旧二进制。
		storageName := firmwareStorageName(ident.ElfSHA256, baseName)
		dst := filepath.Join(firmwareDir, storageName)
		if err := os.Rename(tmpPath, dst); err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		committed = true // tmpPath 已不存在，defer 不再删

		// 7a) legacy 行就地补齐：身份列 / psram / build_profile / idf_ver / version 全部以
		//     本次镜像 + sidecar 为准，落盘名换成新的 elf 唯一名；**仍是同一行**（id 不变），
		//     引用它的历史任务不受影响。旧的迁移前二进制若与新名不同则删掉 ——
		//     该行已不再引用它，留着就是孤儿。
		if completingLegacy {
			updates := map[string]interface{}{
				"version":        ident.Version, // 版本以镜像为准（方案判断 5）
				"checksum":       checksum,
				"size_bytes":     uint64(size),
				"url":            signedFirmwareURL(baseURL, storageName),
				"filename":       baseName,
				"storage_path":   dst,
				"chip":           ident.Chip,
				"flash_mb":       ident.FlashMB,
				"psram":          sidecar.PSRAMValue(),
				"model_key":      ident.ModelKey(),
				"build_profile":  sidecar.Profile,
				"elf_sha256":     ident.ElfSHA256,
				"built_at_raw":   ident.BuiltAtRaw,
				"idf_ver":        ident.IDFVersion,
				"identity_state": dbschema.IdentityVerified,
			}
			// channel / changelog / min_from_version 保持原值：补齐身份不该顺手改运营字段。
			if err := db.Model(&models.Firmware{}).Where("id = ?", existing.ID).Updates(updates).Error; err != nil {
				removeFirmwareBinaryOnFailure(dst)
				Error(c, http.StatusInternalServerError, err.Error())
				return
			}
			if previous := firmwareBinaryDiskPath(existing.StoragePath, existing.Filename); previous != "" &&
				filepath.Clean(previous) != filepath.Clean(dst) {
				if rmErr := os.Remove(previous); rmErr != nil && !os.IsNotExist(rmErr) {
					logger.Warnf("firmware %d: failed to remove superseded legacy binary %s: %v", existing.ID, previous, rmErr)
				}
			}
			var completed models.Firmware
			if err := db.First(&completed, existing.ID).Error; err != nil {
				Error(c, http.StatusInternalServerError, err.Error())
				return
			}
			logger.Infof("[firmware] legacy identity completed in place id=%d name=%q chip=%s flash_mb=%d psram=%s version=%s elf=%s… profile=%s",
				completed.ID, completed.Filename, completed.Chip, completed.FlashMB, completed.Psram, completed.Version, shortSHA(completed.ElfSHA256), completed.BuildProfile)
			SuccessWithCode(c, http.StatusOK, firmwareUploadResponse{Firmware: &completed, CompletedLegacy: true})
			return
		}

		// 7b) 新行入库。行写失败时把已落盘文件删掉，避免「磁盘有、库里没有」的孤儿。
		fw := models.Firmware{
			Version:       ident.Version, // 版本以镜像为准（方案判断 5）
			Checksum:      checksum,
			SizeBytes:     uint64(size),
			URL:           signedFirmwareURL(baseURL, storageName),
			Filename:      baseName,
			StoragePath:   dst,
			Chip:          ident.Chip,
			FlashMB:       ident.FlashMB,
			Psram:         sidecar.PSRAMValue(),
			ModelKey:      ident.ModelKey(),
			BuildProfile:  sidecar.Profile,
			ElfSHA256:     ident.ElfSHA256,
			BuiltAtRaw:    ident.BuiltAtRaw,
			IDFVer:        ident.IDFVersion,
			Channel:       models.FirmwareChannelStable,
			IdentityState: dbschema.IdentityVerified,
		}
		if err := db.Create(&fw).Error; err != nil {
			removeFirmwareBinaryOnFailure(dst)
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		logger.Infof("[firmware] uploaded id=%d name=%q chip=%s flash_mb=%d psram=%s version=%s elf=%s… profile=%s",
			fw.ID, fw.Filename, fw.Chip, fw.FlashMB, fw.Psram, fw.Version, shortSHA(ident.ElfSHA256), fw.BuildProfile)
		SuccessWithCode(c, http.StatusCreated, firmwareUploadResponse{Firmware: &fw})
	})

	// Update firmware metadata
	// - PUT /api/v1/firmwares/:id
	//
	// 方案 §4.3：只允许 changelog / channel / min_from_version。
	// version 由镜像决定；target_model / stable 两列已随 M0 迁移删除。
	// 这三个字段不再出现在请求体里（旧前端由同批发布的镜像替换，不做兼容分支）。
	v1.PUT("/firmwares/:id", func(c *gin.Context) {
		var firmware models.Firmware
		if err := db.First(&firmware, c.Param("id")).Error; err != nil {
			Error(c, http.StatusNotFound, "")
			return
		}
		var req struct {
			Changelog      *string `json:"changelog"`
			Channel        *string `json:"channel"`
			MinFromVersion *string `json:"min_from_version"`
		}
		// A malformed body must fail loudly: ignoring this error made every request
		// look successful no matter what the client sent.
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		updates := map[string]interface{}{}
		if req.Changelog != nil {
			updates["changelog"] = *req.Changelog
		}
		if req.Channel != nil {
			channel := strings.TrimSpace(*req.Channel)
			switch channel {
			case models.FirmwareChannelStable, models.FirmwareChannelBeta, models.FirmwareChannelInternal:
				updates["channel"] = channel
			default:
				Error(c, http.StatusBadRequest, "channel must be one of stable/beta/internal")
				return
			}
		}
		if req.MinFromVersion != nil {
			updates["min_from_version"] = *req.MinFromVersion
		}
		if len(updates) > 0 {
			if err := db.Model(&firmware).Updates(updates).Error; err != nil {
				Error(c, http.StatusInternalServerError, err.Error())
				return
			}
		}
		// Reload to return updated data, with a freshly signed download URL.
		db.First(&firmware, firmware.ID)
		if baseURL, ok := firmwareBaseURLForRequest(c); ok {
			if name := firmwareDownloadName(&firmware); name != "" {
				firmware.URL = signedFirmwareURL(baseURL, name)
			}
		}
		Success(c, firmware)
	})

	// Withdraw firmware (T1b.2)
	// - POST /api/v1/firmwares/:id/withdraw
	//
	// 撤回 = 写 withdrawn_at（可恢复；仍可下载给历史任务），不是删除。
	// 有在途任务 → 409 FIRMWARE_IN_USE（否则设备可能拿着已不可选的地址继续刷）。
	v1.POST("/firmwares/:id/withdraw", func(c *gin.Context) {
		withdrawFirmware(c, db)
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
		// T1b.2 / DEF-10：有在途任务时禁止删除（与撤回同一判据）。
		inFlight, err := ota.InFlightTaskCount(db, fw.ID)
		if err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		if inFlight > 0 {
			respondFirmwareInUse(c, "delete")
			return
		}
		actor := newRequestActor(c, db)

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
		// 软删 + 审计同一事务（REQ-8）：审计是危险操作的凭据，不允许「删了、没记账」。
		if err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Delete(&fw).Error; err != nil {
				return err
			}
			return writeFirmwareLifecycleAudit(tx, actor, "firmware.deleted", &fw)
		}); err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		SuccessMsg(c, gin.H{"id": id, "version": fw.Version}, "deleted")
	})
}

// firmwareUploadResponse 是上传接口的 data 形状：**仍是 Firmware 对象**（既有契约不变，
// data.id / data.version 照旧可读），只在「legacy 就地补齐」这一条路径上多发一个
// completed_legacy=true —— 前端据此提示「这条固件的身份已补齐」，而不是「又传了新固件」。
type firmwareUploadResponse struct {
	*models.Firmware
	CompletedLegacy bool `json:"completed_legacy,omitempty"`
}

// removeFirmwareBinaryOnFailure 删掉「已落盘但入库失败」的二进制，避免孤儿（AC-6）。
func removeFirmwareBinaryOnFailure(path string) {
	if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) {
		logger.Warnf("failed to remove orphaned firmware binary %s after DB error: %v", path, rmErr)
	}
}

// ── 上传/下载辅助 ──

// receiveFirmwareUpload 把上传字节流写到临时文件，同时算 SHA256，返回
// (临时文件路径, 字节数, hex 摘要)。调用方负责在失败路径删除临时文件。
func receiveFirmwareUpload(file *multipart.FileHeader) (string, int64, string, error) {
	src, err := file.Open()
	if err != nil {
		return "", 0, "", err
	}
	defer func() { _ = src.Close() }()

	tempDir := filepath.Join(firmwareDir, firmwareTempDirName)
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		return "", 0, "", err
	}
	tmp, err := os.CreateTemp(tempDir, "upload-*")
	if err != nil {
		return "", 0, "", err
	}
	tmpPath := tmp.Name()

	hasher := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(tmp, hasher), src)
	closeErr := tmp.Close()
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		return "", 0, "", copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		return "", 0, "", closeErr
	}
	return tmpPath, size, hex.EncodeToString(hasher.Sum(nil)), nil
}

// readFirmwareIdentity 从落盘文件解析镜像身份。
func readFirmwareIdentity(path string, size int64) (*firmwareid.Identity, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return firmwareid.ReadIdentity(file, size)
}

// readFirmwareSidecar 取出与 .bin 同名的 .meta.json 内容。
//
// 识别顺序：① 字段名 "meta"；② 任意字段里文件名等于 <bin 基名>.meta.json；
// ③ 任意字段里以 .meta.json 结尾。都找不到即 FW_META_REQUIRED——
// 缺 sidecar 就没有 psram，而 psram 是必填项（方案 §4.2）。
func readFirmwareSidecar(c *gin.Context, binBase string) ([]byte, error) {
	form, err := c.MultipartForm()
	if err != nil {
		return nil, &firmwareid.Error{Code: firmwareid.CodeMetaRequired, Detail: "firmware upload is not a valid multipart form"}
	}
	wantName := strings.TrimSuffix(binBase, filepath.Ext(binBase)) + firmwareMetaSuffix

	var chosen *multipart.FileHeader
	if files := form.File[firmwareMetaField]; len(files) > 0 {
		chosen = files[0]
	}
	if chosen == nil {
		for _, files := range form.File {
			for _, candidate := range files {
				if strings.EqualFold(filepath.Base(candidate.Filename), wantName) {
					chosen = candidate
					break
				}
			}
			if chosen != nil {
				break
			}
		}
	}
	if chosen == nil {
		for _, files := range form.File {
			for _, candidate := range files {
				if strings.HasSuffix(strings.ToLower(candidate.Filename), firmwareMetaSuffix) {
					chosen = candidate
					break
				}
			}
			if chosen != nil {
				break
			}
		}
	}
	if chosen == nil {
		return nil, &firmwareid.Error{
			Code:   firmwareid.CodeMetaRequired,
			Detail: fmt.Sprintf("missing companion sidecar %s (multipart field %q)", wantName, firmwareMetaField),
		}
	}

	src, err := chosen.Open()
	if err != nil {
		return nil, &firmwareid.Error{Code: firmwareid.CodeMetaRequired, Detail: fmt.Sprintf("open sidecar: %v", err)}
	}
	defer func() { _ = src.Close() }()
	raw, err := io.ReadAll(io.LimitReader(src, firmwareMetaMaxBytes+1))
	if err != nil {
		return nil, &firmwareid.Error{Code: firmwareid.CodeMetaRequired, Detail: fmt.Sprintf("read sidecar: %v", err)}
	}
	if len(raw) > firmwareMetaMaxBytes {
		return nil, &firmwareid.Error{Code: firmwareid.CodeMetaRequired, Detail: fmt.Sprintf("sidecar is larger than %d bytes", firmwareMetaMaxBytes)}
	}
	return raw, nil
}

// firmwareStorageName 以 elf_sha256 前 16 位为前缀生成唯一落盘名。
//
// 「含 elf 前缀」是刻意的：运维在 firmwares/ 里一眼能看出这个是按 elf 命名的，
// 同时前 16 位足以区分（32 hex 全用会让文件名过长）。
func firmwareStorageName(elfSHA256, baseName string) string {
	digest := strings.ToLower(strings.TrimSpace(elfSHA256))
	if len(digest) > 16 {
		digest = digest[:16]
	}
	name := filepath.Base(baseName)
	if !strings.HasSuffix(strings.ToLower(name), ".bin") {
		name += ".bin"
	}
	if len(name) > 96 {
		ext := filepath.Ext(name)
		stem := strings.TrimSuffix(name, ext)
		if keep := 96 - len(ext); len(stem) > keep {
			stem = stem[:keep]
		}
		name = stem + ext
	}
	return "elf" + digest + "_" + name
}

// refreshFirmwareDownloadURLs 在响应里重签每一行的下载 URL（不写库）。
func refreshFirmwareDownloadURLs(c *gin.Context, firmwares []models.Firmware) {
	baseURL, ok := firmwareBaseURLForRequest(c)
	if !ok {
		return
	}
	for i := range firmwares {
		if name := firmwareDownloadName(&firmwares[i]); name != "" {
			firmwares[i].URL = signedFirmwareURL(baseURL, name)
		}
	}
}

// signedFirmwareURL 现签一张 DefaultTTL 的下载地址。
func signedFirmwareURL(baseURL, filename string) string {
	return firmwareticket.URLFor(baseURL, url.PathEscape(filename), time.Now().UTC(), firmwareticket.DefaultTTL, jwtSecret)
}

// firmwareBaseURLFromEnv 只认 EHOME_EXTERNAL_HOST（下发路径没有 HTTP 请求可借）。
func firmwareBaseURLFromEnv() (string, bool) {
	host := strings.TrimSpace(os.Getenv("EHOME_EXTERNAL_HOST"))
	if host == "" {
		return "", false
	}
	return "http://" + host, true
}

// firmwareBaseURLForRequest 解析下载 URL 的主机部分。
//
// 生产必须配 EHOME_EXTERNAL_HOST；仅开发模式允许退回请求 Host（并告警），
// 否则会产生 Host Header Injection 面（既有行为，保持不变）。
func firmwareBaseURLForRequest(c *gin.Context) (string, bool) {
	if baseURL, ok := firmwareBaseURLFromEnv(); ok {
		return baseURL, true
	}
	if isDevelopmentMode() {
		logger.Warnf("EHOME_EXTERNAL_HOST not set, falling back to request Host header for firmware URL (potential Host Header Injection)")
		return "http://" + c.Request.Host, true
	}
	return "", false
}

// rejectFirmwareUpload 统一上传拒绝出口：HTTP 码 + envelope.error_code 机器码。
// firmwareUploadRejectUnclassified 是「拒绝但拿不到机器码」的指标兜底标签。
//
// 正常路径不会出现：所有 rejectFirmwareUpload 调用点都传附录 A 的 FW_* 码。
// 但原始 I/O 失败（如 os.Open 临时文件）会得到空码，若直接当标签会产生一个
// 无意义的 "" 维度，让聚合结果多出一条谁也解释不了的序列。
const firmwareUploadRejectUnclassified = "FW_UNCLASSIFIED"

func rejectFirmwareUpload(c *gin.Context, statusCode int, errorCode, message string) {
	// 统一拒绝出口：所有上传失败路径都经这里（含未来新增的校验分支），
	// 指标因此不会随分支增加而漏埋（§9.5 上传拦截原因维度）。
	reason := errorCode
	if reason == "" {
		reason = firmwareUploadRejectUnclassified
	}
	metrics.FirmwareUploadRejected.WithLabelValues(reason).Inc()
	logger.Warnf("[firmware] upload rejected code=%s status=%d: %s", errorCode, statusCode, message)
	ErrorWithCode(c, statusCode, errorCode, message)
}

// isBodyTooLarge 判定 FormFile 的失败是否来自 MaxBytesReader。
func isBodyTooLarge(err error) bool {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		return true
	}
	return strings.Contains(err.Error(), "request body too large")
}

func shortSHA(digest string) string {
	if len(digest) > 12 {
		return digest[:12]
	}
	return digest
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
