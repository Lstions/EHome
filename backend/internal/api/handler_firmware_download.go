package api

// handler_firmware_download.go — 管理员取固件下载票（修复「下载/复制链接 401」）。
//
// 背景：公开下载路由 /api/v1/firmwares/:filename/download 只认 HMAC 票据
// （expires + signature），而固件页原先拼的是 ?token=<JWT> —— 那条分支在服务端
// 从未存在过，所以按钮一直 401（HEAD 即如此，非本次引入）。
//
// 修复取向：不给下载路由增加「JWT 也放行」的旁路（那会把长期凭证塞进 URL 并
// 扩大公开端点的鉴权面），而是让管理员在**已鉴权**的接口里换一张短时票：
// 设备侧 URL 语义不变，浏览器侧也不再暴露登录 token。
//
// 路由用 POST 而非 GET 的原因：gin 的同一方法树里 :id 与公开路由的 :filename
// 会因通配符名冲突 panic（同位置不同名）。POST 树里没有 :filename，天然避开；
// 语义上「签发一张票」也确实是创建动作。

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"ehome/backend/internal/firmwareticket"
	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// FirmwareDownloadTicket 是 POST /api/v1/firmwares/:id/download-ticket 的 data。
type FirmwareDownloadTicket struct {
	URL       string `json:"url"`
	ExpiresAt string `json:"expires_at"`
}

// requestBaseURL 用当前请求推导对外基址（管理员浏览器所在 origin）。
//
// 不用 EHOME_EXTERNAL_HOST：那个变量描述的是「设备该访问哪里」，而这张票是给
// 浏览器立即使用的，同源 Host 才是用户实际能连上的地址（反代场景由
// X-Forwarded-Proto 补齐 scheme）。
func requestBaseURL(c *gin.Context) string {
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	if proto := c.GetHeader("X-Forwarded-Proto"); proto == "http" || proto == "https" {
		scheme = proto
	}
	return scheme + "://" + c.Request.Host
}

// firmwareDownloadTicketHandler 为固件行签一张 30 分钟下载票。
//
// 未撤回/已撤回都可下载（撤回只影响「能否用于新任务」，历史二进制仍需可取证）；
// 软删的行由 GORM 默认作用域排除，返回 404。
func firmwareDownloadTicketHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := strconv.ParseUint(c.Param("id"), 10, 64)
		if err != nil || id == 0 {
			Error(c, http.StatusBadRequest, "invalid firmware id")
			return
		}

		var fw models.Firmware
		if err := db.First(&fw, uint(id)).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				Error(c, http.StatusNotFound, "firmware not found")
				return
			}
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}

		name := firmwareDownloadName(&fw)
		if name == "" {
			Error(c, http.StatusInternalServerError, "firmware storage path is empty")
			return
		}

		now := time.Now().UTC()
		Success(c, FirmwareDownloadTicket{
			URL:       firmwareticket.URLFor(requestBaseURL(c), name, now, firmwareticket.DefaultTTL, jwtSecret),
			ExpiresAt: now.Add(firmwareticket.DefaultTTL).Format(time.RFC3339),
		})
	}
}