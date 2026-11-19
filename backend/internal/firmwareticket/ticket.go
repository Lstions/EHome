// Package firmwareticket signs and verifies short-lived firmware download tickets.
//
// 为什么是独立包（方案 §3.5 / T-1.1）：票据的**签发方**有两处——上传/列表接口
// (api) 与 OtaCmd 下发 (ota)——而 api 已经 import ota，所以票据不能留在 api 里，
// 否则 ota 反向 import api 成环。下沉到本包后两边都只依赖它。
//
// 设计约束（方案 §2.2 非目标）：**不重构票据形态**，仍是 filename+expires 的
// HMAC-SHA256 短票，参数名与校验口径与改造前逐字一致，老设备的 URL 语义不变。
package firmwareticket

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultTTL 是单张票据的有效期，**从签发时刻算**（DEF-2：上传时写死 30 分钟，
	// 隔天再下发必然过期；M-1 改成 OtaCmd 发送时现签，TTL 从那一刻开始走）。
	DefaultTTL = 30 * time.Minute
	// MaxLifetime 是允许接受的最长剩余有效期：即便密钥泄露也无法签出一张十年票。
	// 它同时兜住「手改 expires 想绕过过期」的路径。
	MaxLifetime = 24 * time.Hour
)

// Sign 计算 filename + expires 的 HMAC-SHA256（base64url 无填充）。
//
// 消息格式刻意保持 "filename\nexpires"：改造前的票据是这一形状，
// 变更会让所有已写入 OtaCmd 的旧 URL 立刻失效（方案 §2.2）。
func Sign(filename string, expiresAt time.Time, secret []byte) string {
	message := filename + "\n" + strconv.FormatInt(expiresAt.Unix(), 10)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(message))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Validate 校验票据：expires 必须是十进制 Unix 秒、必须仍在有效期内、
// 不得超过 MaxLifetime，且签名按常量时间比较。
func Validate(filename, expires, signature string, secret []byte, now time.Time) bool {
	expiresUnix, err := strconv.ParseInt(expires, 10, 64)
	if err != nil || expiresUnix <= now.Unix() || expiresUnix > now.Add(MaxLifetime).Unix() {
		return false
	}
	expected := Sign(filename, time.Unix(expiresUnix, 0), secret)
	return hmac.Equal([]byte(expected), []byte(signature))
}

// URL 拼一条带票据的下载地址。expiresAt 就是票据的到期时刻。
func URL(baseURL, filename string, expiresAt time.Time, secret []byte) string {
	return fmt.Sprintf("%s/api/v1/firmwares/%s/download?expires=%d&signature=%s",
		baseURL, filename, expiresAt.Unix(), Sign(filename, expiresAt, secret))
}

// URLFor 是 URL 的便捷形态：从 now 起算 ttl。
func URLFor(baseURL, filename string, now time.Time, ttl time.Duration, secret []byte) string {
	return URL(baseURL, filename, now.Add(ttl), secret)
}

// SanitizeName 把存储路径/文件名收敛成一个安全的下载路由文件名。
//
// 上传会以 elf_sha256 前缀重命名落盘（AC-6：同名重传不得覆盖旧二进制），
// 下载 URL 必须指向磁盘上的真实名字；历史行只存了 Filename。
// 这里只取 basename，确保被污染的 DB 值不能把 URL 带出 firmwares/ 目录。
func SanitizeName(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	name := filepath.Base(path)
	if name == "" || name == "." || name == string(filepath.Separator) {
		return ""
	}
	return name
}
