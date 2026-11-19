package api

// handler_firmware_ops.go — 固件列表（T1b.1）与生命周期（T1b.2：撤回/删除保护）。
//
// 为什么单独一个文件：handler_ota.go 已经承载上传解析、下载、任务与状态机入口；
// 列表筛选/逐行兼容判定/撤回删除保护是**管理面**的独立职责，放一起会让那个文件
// 继续膨胀。判定逻辑本身不在这里 —— 全部复用 firmwareid.CheckCompat（建任务用的
// 同一个纯函数），本文件只做「查库 → 映射 → 响应」。

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ehome/backend/internal/audit"
	"ehome/backend/internal/firmwareid"
	"ehome/backend/internal/models"
	"ehome/backend/internal/ota"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// firmwareListItem 是列表行：models.Firmware 的字段原样展开，**仅当查询带 node_id**
// 时追加 compatible / reasons（方案 §4.3：data 仍是 Firmware[]，不做分页）。
//
// reasons 与 409 的 data.reasons 同源（OTA_GATE_* 机器码），前端只维护一张映射表。
type firmwareListItem struct {
	*models.Firmware
	Compatible *bool    `json:"compatible,omitempty"`
	Reasons    []string `json:"reasons,omitempty"`
}

// firmwareListQuery 是 GET /api/v1/firmwares 的查询参数（§4.3 / T1b.1）。
type firmwareListQuery struct {
	NodeID           string
	Chip             string
	FlashMB          uint16
	Channel          string
	Q                string
	IncludeWithdrawn bool
}

// parseFirmwareListQuery 解析并校验查询参数。
//
// 非法值一律 400（而不是静默返回空列表）：筛选条件是用户手选的，静默空态会让人
// 以为「库里没有这个固件」，实际是参数拼错了。空串一律视为「不过滤」——
// 前端 buildFirmwareListParams 也保证空串不发。
func parseFirmwareListQuery(c *gin.Context) (firmwareListQuery, error) {
	q := firmwareListQuery{
		NodeID:  strings.TrimSpace(c.Query("node_id")),
		Q:       strings.TrimSpace(c.Query("q")),
		Channel: strings.TrimSpace(c.Query("channel")),
	}
	if raw := strings.TrimSpace(c.Query("chip")); raw != "" {
		chip := firmwareid.CanonicalChip(raw)
		if chip == "" {
			return q, fmt.Errorf("chip must be one of esp32c6/esp32s3 (got %q)", raw)
		}
		q.Chip = chip
	}
	if raw := strings.TrimSpace(c.Query("flash_mb")); raw != "" {
		flashMB, err := strconv.ParseUint(raw, 10, 16)
		if err != nil || (flashMB != 8 && flashMB != 16) {
			return q, fmt.Errorf("flash_mb must be 8 or 16 (got %q)", raw)
		}
		q.FlashMB = uint16(flashMB)
	}
	if q.Channel != "" {
		switch q.Channel {
		case models.FirmwareChannelStable, models.FirmwareChannelBeta, models.FirmwareChannelInternal:
		default:
			return q, fmt.Errorf("channel must be one of stable/beta/internal (got %q)", q.Channel)
		}
	}
	if raw := strings.TrimSpace(c.Query("include_withdrawn")); raw != "" {
		include, err := strconv.ParseBool(raw)
		if err != nil {
			return q, fmt.Errorf("include_withdrawn must be a boolean (got %q)", raw)
		}
		q.IncludeWithdrawn = include
	}
	return q, nil
}

// apply 把筛选条件落到查询上。
//
// 默认排除已撤回（§4.3「列表默认语义」）；软删行由 GORM 的 deleted_at 默认作用域排除。
func (q firmwareListQuery) apply(db *gorm.DB) *gorm.DB {
	if !q.IncludeWithdrawn {
		db = db.Where("withdrawn_at IS NULL")
	}
	if q.Chip != "" {
		db = db.Where("chip = ?", q.Chip)
	}
	if q.FlashMB != 0 {
		db = db.Where("flash_mb = ?", q.FlashMB)
	}
	if q.Channel != "" {
		db = db.Where("channel = ?", q.Channel)
	}
	if q.Q != "" {
		like := "%" + strings.ToLower(q.Q) + "%"
		db = db.Where(
			"LOWER(version) LIKE ? OR LOWER(model_key) LIKE ? OR LOWER(build_profile) LIKE ? OR LOWER(filename) LIKE ? OR LOWER(changelog) LIKE ?",
			like, like, like, like, like,
		)
	}
	return db
}

// queryFirmwares 执行列表查询；带 node_id 时逐行给出闸门判定。
//
// 判据就是建任务用的同一个 CheckCompat（经 ota.NewCompatRequest 映射），所以
// 「列表说可选」与「建任务能过」不会漂移。legacy/duplicate 行命中第 2 条 →
// compatible=false + reasons=[OTA_GATE_FIRMWARE_UNVERIFIED]，前端据此置灰
// （AC-17：列表可见但不可选）。
func queryFirmwares(c *gin.Context, db *gorm.DB, q firmwareListQuery) ([]firmwareListItem, error) {
	var firmwares []models.Firmware
	// 稳定排序（D2 观察项）：PG 堆序不保证顺序，而前端是分页展示 —— 同一页翻两次
	// 看到不同行会被当成「列表在抖」。created_at DESC 是展示语义（新固件在前），
	// id DESC 是同一时间戳下的确定性 tie-break（同批上传常有相同 created_at）。
	if err := q.apply(db).Order("created_at DESC, id DESC").Find(&firmwares).Error; err != nil {
		return nil, err
	}
	// 响应里重签下载票（列表是只读语义，不写库；DEF-3/AC-11）。
	refreshFirmwareDownloadURLs(c, firmwares)

	var node *models.Node
	if q.NodeID != "" {
		var found models.Node
		if err := db.Where("node_id = ?", q.NodeID).First(&found).Error; err != nil {
			return nil, err
		}
		node = &found
	}

	items := make([]firmwareListItem, 0, len(firmwares))
	for i := range firmwares {
		item := firmwareListItem{Firmware: &firmwares[i]}
		if node != nil {
			result := firmwareid.CheckCompat(ota.NewCompatRequest(node, item.Firmware, firmwareid.SourceUser))
			compatible := result.Compatible
			item.Compatible = &compatible
			if !result.Compatible {
				item.Reasons = result.Reasons
			}
		}
		items = append(items, item)
	}
	return items, nil
}

// requestActor 是从登录态/请求头提取的审计主体。
type requestActor struct {
	Type      string
	UserID    *uint
	Snapshot  string
	RequestID string
	SourceIP  string
}

// newRequestActor 取审计主体：subject_id（JWTAuthWithDB）→ user_id（legacy JWTAuth）。
//
// 两者都取不到时仍记录 actor_type=admin + 快照 system_admin：审计宁可「主体弱」，
// 不可「没有行」（REQ-8）。用户名只用于快照，查不到就用 system_admin 兜底。
func newRequestActor(c *gin.Context, db *gorm.DB) requestActor {
	actor := requestActor{
		Type:      "admin",
		RequestID: strings.TrimSpace(c.GetHeader("X-Request-ID")),
		SourceIP:  c.ClientIP(),
	}
	for _, key := range []string{"subject_id", "user_id"} {
		raw, exists := c.Get(key)
		if !exists {
			continue
		}
		id, ok := raw.(uint)
		if !ok || id == 0 {
			continue
		}
		uid := id
		actor.UserID = &uid
		var user models.User
		if err := db.Select("username").First(&user, id).Error; err == nil && user.Username != "" {
			actor.Snapshot = user.Username
		}
		break
	}
	if actor.Snapshot == "" {
		actor.Snapshot = models.SystemAdminSubjectKey
	}
	return actor
}

// otaAudit 转成 ota 包的审计主体（建任务的 force 审计用）。
func (a requestActor) otaAudit() ota.AuditActor {
	return ota.AuditActor{
		Type:      a.Type,
		UserID:    a.UserID,
		Snapshot:  a.Snapshot,
		RequestID: a.RequestID,
		SourceIP:  a.SourceIP,
	}
}

// respondOTAManagerError 把 ota.Manager 的错误映射成 HTTP 响应。
//
// 建任务与手动回滚共用这一个映射：同一道闸门不允许出现「建任务 409、回滚 500」
// 这种两套语义的漂移。
func respondOTAManagerError(c *gin.Context, err error) {
	var gateErr *firmwareid.GateError
	var deliveryErr *ota.DeliveryError
	switch {
	case errors.As(err, &gateErr):
		// 409 + data.reasons / node_hw / firmware / forceable / fix（方案 §4.3）。
		ErrorWithCodeData(c, http.StatusConflict, gateErr.Code, gateErr.Message, gateErr.Data())
	case errors.As(err, &deliveryErr):
		// 任务已落库并置 failed，但下发失败（D2）：503 + task_id，状态与响应一致。
		errorMsg := deliveryErr.Error()
		if deliveryErr.Err != nil {
			errorMsg = deliveryErr.Err.Error()
		}
		ErrorWithCodeData(c, http.StatusServiceUnavailable, "OTA_DELIVERY_FAILED", deliveryErr.Error(), gin.H{
			"task_id":   deliveryErr.TaskID,
			"ota_id":    deliveryErr.OtaID,
			"status":    deliveryErr.Status,
			"error_msg": errorMsg,
		})
	case errors.Is(err, ota.ErrForceReasonRequired):
		// force 本身合法，缺的是理由：400（前端据此提示「请填写强制原因」）。
		ErrorWithCode(c, http.StatusBadRequest, "force_reason_required", err.Error())
	default:
		Error(c, http.StatusInternalServerError, err.Error())
	}
}

// respondFirmwareInUse 是撤回/删除的在途任务保护（方案 §4.3 / DEF-10）。
func respondFirmwareInUse(c *gin.Context, action string) {
	ErrorWithCode(c, http.StatusConflict, "FIRMWARE_IN_USE",
		fmt.Sprintf("firmware has in-flight OTA tasks; wait for them to finish or cancel them before trying to %s", action))
}

// errFirmwareAlreadyWithdrawn 是「并发下已被别人撤回」的内部信号（不写审计）。
var errFirmwareAlreadyWithdrawn = errors.New("firmware already withdrawn")

// writeFirmwareLifecycleAudit 落一条固件生命周期审计（REQ-8：撤回/删除必须留痕）。
//
// 与业务写同一事务：审计是危险操作的凭据，不允许「固件改了、审计丢了」。
func writeFirmwareLifecycleAudit(tx *gorm.DB, actor requestActor, eventName string, firmware *models.Firmware) error {
	return audit.NewWriter(tx).Write(audit.Event{
		ActorType:     actor.Type,
		ActorUserID:   actor.UserID,
		ActorSnapshot: actor.Snapshot,
		EventName:     eventName,
		EventVersion:  1,
		Result:        "success",
		RequestID:     actor.RequestID,
		SourceIP:      actor.SourceIP,
		TargetType:    "firmware",
		TargetID:      strconv.FormatUint(uint64(firmware.ID), 10),
		Metadata: map[string]interface{}{
			"version":    firmware.Version,
			"model_key":  firmware.ModelKey,
			"filename":   firmware.Filename,
			"elf_sha256": firmware.ElfSHA256,
		},
	})
}

// withdrawFirmware 处理 POST /api/v1/firmwares/:id/withdraw（§4.3 / T1b.2）。
//
// 语义：撤回 = 写 withdrawn_at（可恢复，仍可下载给历史任务），不是删除；
// 有在途任务 → 409 FIRMWARE_IN_USE（否则设备可能拿着一份已不可选的下载地址继续刷）。
// 重复撤回幂等：已撤回再调一次返回 200，不重复写审计。
func withdrawFirmware(c *gin.Context, db *gorm.DB) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid firmware id")
		return
	}
	var fw models.Firmware
	if err := db.First(&fw, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			Error(c, http.StatusNotFound, "firmware not found")
			return
		}
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	if fw.WithdrawnAt != nil {
		SuccessWithCodeMsg(c, http.StatusOK, fw, "already withdrawn")
		return
	}
	inFlight, err := ota.InFlightTaskCount(db, fw.ID)
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	if inFlight > 0 {
		respondFirmwareInUse(c, "withdraw")
		return
	}

	now := time.Now().UTC()
	actor := newRequestActor(c, db)
	err = db.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.Firmware{}).
			Where("id = ? AND withdrawn_at IS NULL", fw.ID).
			Update("withdrawn_at", &now)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errFirmwareAlreadyWithdrawn
		}
		return writeFirmwareLifecycleAudit(tx, actor, "firmware.withdrawn", &fw)
	})
	if err != nil {
		if errors.Is(err, errFirmwareAlreadyWithdrawn) {
			if reloadErr := db.First(&fw, fw.ID).Error; reloadErr == nil {
				SuccessWithCodeMsg(c, http.StatusOK, fw, "already withdrawn")
				return
			}
		}
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	fw.WithdrawnAt = &now
	SuccessWithCodeMsg(c, http.StatusOK, fw, "withdrawn")
}
