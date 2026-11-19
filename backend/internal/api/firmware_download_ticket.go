package api

import (
	"time"

	"ehome/backend/internal/firmwareticket"
	"ehome/backend/internal/models"
	"ehome/backend/internal/ota"
)

// 固件下载票据：算法已下沉到 internal/firmwareticket（方案 T-1.1）。
//
// 为什么下沉而不是留在这里：票据有两个签发方——上传/列表接口（本包）与
// OtaCmd 下发（internal/ota）。api 已经 import ota，如果票据留在 api，
// ota 想「发送时现签」就必须反向 import api ⇒ 成环。下沉后两边都只依赖叶子包。
//
// 本文件因此只剩薄包装：参数名、消息格式、24h 上限都与改造前逐字一致，
// 老设备 URL 的语义不变（方案 §2.2 非目标：不重构票据形态）。

// signFirmwareDownload 计算 filename+expires 的 HMAC 签名。
func signFirmwareDownload(filename string, expiresAt time.Time, secret []byte) string {
	return firmwareticket.Sign(filename, expiresAt, secret)
}

// validateFirmwareDownload 校验票据（未过期、未超上限、签名匹配、绑定该文件名）。
func validateFirmwareDownload(filename, expires, signature string, secret []byte, now time.Time) bool {
	return firmwareticket.Validate(filename, expires, signature, secret, now)
}

// firmwareDownloadURL 拼一条带票据的下载地址；ttl 从**当前时刻**起算。
func firmwareDownloadURL(filename, baseURL string, ttl time.Duration, secret []byte) string {
	return firmwareticket.URLFor(baseURL, filename, time.Now().UTC(), ttl, secret)
}

// firmwareDownloadName 返回下载路由里的文件名（= 磁盘上的真实文件名）。
//
// 上传会以 elf_sha256 前缀重命名落盘（AC-6：同名重传不得覆盖旧二进制），
// 因此 URL 必须指向 StoragePath 的 basename；历史行没有 StoragePath 时退回 Filename。
func firmwareDownloadName(fw *models.Firmware) string {
	if fw == nil {
		return ""
	}
	if name := firmwareticket.SanitizeName(fw.StoragePath); name != "" {
		return name
	}
	return firmwareticket.SanitizeName(fw.Filename)
}

// otaFirmwareURLIssuer 把本包的票据签名能力注入 ota.Manager（方案 T-1.2）。
//
// 语义：OtaCmd 里的 url 在**发送时刻**现签一张 30 分钟票（DEF-2：上传时写死的
// 票据比任务寿命短，隔天再下发必然 401）。base URL 只能来自 EHOME_EXTERNAL_HOST
// ——下发路径没有 HTTP 请求可借用 Host；未配置时返回 ""，由 Manager 退回
// firmware.URL（仅本机开发会走到）。
type otaFirmwareURLIssuer struct{}

// NewOtaFirmwareURLIssuer 构造注入给 ota.NewManager 的签发器。
func NewOtaFirmwareURLIssuer() ota.FirmwareURLIssuer {
	return otaFirmwareURLIssuer{}
}

func (otaFirmwareURLIssuer) IssueDownloadURL(filename string) string {
	if filename == "" {
		return ""
	}
	baseURL, ok := firmwareBaseURLFromEnv()
	if !ok {
		return ""
	}
	return firmwareticket.URLFor(baseURL, filename, time.Now().UTC(), firmwareticket.DefaultTTL, jwtSecret)
}
