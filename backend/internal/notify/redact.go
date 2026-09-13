package notify

import (
	"net/url"
	"regexp"
	"strings"
)

// 脱敏纪律 (设计/外发通知通道.md §7.3/§7.5): URL 里的 key/token 等查询参数与
// Authorization 头**永不**出现在日志、错误文案或投递审计里。
// 本仓此前的脱敏范式是 token=*** (MainLayout / stores/websocket.ts); Go 侧此前
// 没有实现, 这里是唯一一处, 所有出站相关文案都必须过这两个函数。

// RedactedPlaceholder 与前端 stripToken 的 "***" 保持一致。
const RedactedPlaceholder = "***"

// sensitiveQueryKeys 是必须脱敏的查询参数名 (大小写不敏感, 覆盖企业微信 key、
// OneBot access_token、通用 webhook 的 token/sign 等常见写法)。
var sensitiveQueryKeys = map[string]bool{
	"key": true, "token": true, "access_token": true, "accesstoken": true,
	"secret": true, "password": true, "passwd": true, "pwd": true,
	"sign": true, "signature": true, "sig": true, "auth": true, "authorization": true,
	"apikey": true, "api_key": true, "appkey": true, "app_secret": true,
}

// redactURLRe 兜底匹配任何形态 URL 里的敏感查询参数 (含裸 query 串与
// net/http 错误文案里被引号包住的 URL)。
// 目标串: key=..., token=..., access_token=... 等, 值到 &/#/空白/引号为止。
var redactURLRe = regexp.MustCompile("(?i)\\b(key|token|access_?token|secret|password|passwd|pwd|sign|signature|sig|api_?key|app_?key|app_?secret|auth|authorization)=([^&\\s\"'#]+)")

// redactHeaderRe 兜底匹配 header 形态的凭据 (Authorization: xxx / X-Api-Key: xxx)。
// 本仓的 client 从不把 header 值放进任何文案, 这是纵深防御。
var redactHeaderRe = regexp.MustCompile("(?i)((?:proxy-)?authorization|x-api-key|apikey)\\s*[:=]\\s*([^\\s,;\"']+)")

// redactBearerRe 兜底匹配 Bearer/Basic 方案里的令牌本体。
var redactBearerRe = regexp.MustCompile("(?i)\\b(bearer|basic)\\s+([A-Za-z0-9\\-._~+/=]{4,})")

// RedactText 脱敏任意文案中出现的凭据 (错误消息、响应片段、日志)。
func RedactText(text string) string {
	if text == "" {
		return text
	}
	// 顺序有讲究: 先剥 Bearer/Basic 的令牌本体, 再处理 header 形态 ——
	// 反过来会把 "Bearer" 这个词当成值吃掉, 令牌本体就漏出去了。
	text = redactBearerRe.ReplaceAllString(text, "$1 "+RedactedPlaceholder)
	text = redactHeaderRe.ReplaceAllString(text, "$1: "+RedactedPlaceholder)
	return redactURLRe.ReplaceAllString(text, "$1="+RedactedPlaceholder)
}

// RedactURL 脱敏 URL 中的敏感查询参数与用户凭据。
// 解析失败时回退到正则脱敏 (绝不原样返回)。
func RedactURL(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return RedactText(raw)
	}
	if parsed.User != nil {
		parsed.User = nil
	}
	if parsed.RawQuery != "" {
		query := parsed.Query()
		changed := false
		for name := range query {
			if sensitiveQueryKeys[strings.ToLower(name)] {
				query.Set(name, RedactedPlaceholder)
				changed = true
			}
		}
		if changed {
			// url.Values.Encode 按 key 排序; 这是脱敏后的重编码, 不要求与原文逐字节一致。
			parsed.RawQuery = query.Encode()
		}
	}
	return RedactText(parsed.String())
}

// ClientMessage 把出站失败转成可落审计/日志的短文案。
//
// 三层清洗: (1) net/http 的错误里会带完整 URL → RedactText;
// (2) 响应片段由调用方先截断再传入 → 这里统一再兜一次;
// (3) 硬截断到 512 (设计 §7.5: error_message 截断到 512 且去除密钥)。
func ClientMessage(err error) string {
	if err == nil {
		return ""
	}
	return TruncateMessage(RedactText(err.Error()), MaxErrorMessageBytes)
}

// MaxErrorMessageBytes 与 models.NotificationDelivery.ErrorMessage 的 gorm size:512 对齐。
const MaxErrorMessageBytes = 512

// TruncateMessage 按字节截断并保证不切裂 UTF-8 (审计列 size:512)。
func TruncateMessage(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8Start(text[cut]) {
		cut--
	}
	return text[:cut]
}

// utf8Start 判断 b 是否是一个 UTF-8 字符的首字节。
func utf8Start(b byte) bool { return b&0xC0 != 0x80 }
