package models

import (
	"encoding/json"
	"net/url"
	"strings"
	"time"
)

// =====================================================================
// 外发通知通道 (设计/外发通知通道.md §3, 冻结数据模型)
// =====================================================================

// 通道类型 (预设模板标识, 设计 §2.4)。
// 预设只是前端表单的预填值: 三者共用同一套投递机制, 差异只在 URL 形态与请求体形状。
const (
	ChannelTypeWebhook = "webhook" // 通用 webhook (JSON POST)
	ChannelTypeWeCom   = "wecom"   // 企业微信群机器人 (key 在 URL 里)
	ChannelTypeOneBot  = "onebot"  // QQ 机器人 OneBot v11 (access_token 放 header)
)

// 通道最小级别 (MinLevel 取值; 与告警三级同名, 见 alert.go)。
// 语义: 只有级别 >= MinLevel 的通知才投递到该通道。
const (
	ChannelMinLevelInfo     = "info"
	ChannelMinLevelWarning  = "warning"
	ChannelMinLevelCritical = "critical"
)

// 投递状态 (NotificationDelivery.State 取值, 设计 §5 状态机)。
// pending 只在"尝试已开始、结论未落库"的窗口内存在 (崩溃窗口的审计证据);
// delivered 是成功终态; failed 表示该次尝试失败 (最后一次 failed 即整条通道投递的终态)。
const (
	DeliveryStatePending   = "pending"
	DeliveryStateDelivered = "delivered"
	DeliveryStateFailed    = "failed"
)

// NotificationChannel 外发通知通道 (通用 webhook)。
//
// 安全裁决 (设计 §2.3-b): Secret 是**只写不读**列 —— json:"-" 保证它
// 永不出现在任何 JSON 序列化结果里 (与 users.password_hash 同思路),
// 列表/详情接口只能回 SecretHint (末 4 位) 供用户辨认。
type NotificationChannel struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"size:64;not null" json:"name"`
	Type string `gorm:"size:24;not null" json:"type"` // webhook | wecom | onebot（预设模板标识）
	// TargetURL 的出站地址**本身可能携带密钥**：企业微信群机器人的 URL 形如
	// https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=<secret>，
	// 密钥就在查询串里。因此它必须经 MarshalJSON 脱敏后才可出站到 API
	// （只保护 Secret 字段是不够的 —— 这正是 TestNotificationChannelSecretNeverSerialized
	// 抓到的缺陷：Secret 是 json:"-" 了，但 target_url 原样回显了 key）。
	TargetURL  string `gorm:"size:512;not null" json:"target_url"`            // 出站地址（序列化时脱敏）
	Secret     string `gorm:"size:512" json:"-"`                              // 只写不读（裁决 §2.3-b）
	SecretHint string `gorm:"size:8" json:"secret_hint"`                      // 末 4 位，供用户辨认
	Template   string `gorm:"type:text" json:"template"`                      // 请求体模板（JSON with {{.Title}} 等）
	MinLevel   string `gorm:"size:16;not null;default:info" json:"min_level"` // info|warning|critical
	// Enabled 同样**不带 gorm default** (设计 §7.6 的可复用结论): 带 default 的列
	// 无法表达"显式 false" —— GORM 把 bool 零值当"未设置"省略该列, DB 填回
	// default true, 于是"创建时就禁用的通道"会静默变成启用通道, 并且 GORM 还会把
	// DB 的默认值**回写进结构体**, 让调用方以为 false 生效了。
	// 默认值改由应用层给出 (见 NewNotificationChannel)。
	Enabled bool `json:"enabled"`
	// TimeoutSec / MaxRetries 是**指针**: nil = 未配置 (用默认值 10s / 2),
	// 0 = 用户显式要求的零值 (max_retries:0 就是"不重试")。
	//
	// 设计 §7.6 裁决: 带 gorm:"default:N" 的 int 列无法表达"显式零值" —— GORM 把
	// int 零值当"未设置"省略该列, DB 填回 default, 于是用户配的 max_retries=0 被
	// 静默改成 2 (投递耗时变 3 倍且无从察觉)。指针让 nil 与 0 可区分; 同时去掉
	// gorm default, 默认值只由应用层 DefaultTimeoutSec/DefaultMaxRetries 给出,
	// 不再存在"两处各说各话"的语义冲突。
	TimeoutSec *int `json:"timeout_sec"`
	MaxRetries *int `json:"max_retries"`
	// AllowPrivate 是设计 §7.1 冻结的 SSRF 例外开关: 家庭内网部署 OneBot
	// (http://192.168.x.x:5700/send_msg) 是合法场景。默认关闭; 开启后该通道
	// 可投递到 private/loopback/link-local/ULA 地址, 调用方与 UI 必须给出警示。
	AllowPrivate bool      `gorm:"default:false" json:"allow_private"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (NotificationChannel) TableName() string { return "notification_channels" }

// MarshalJSON 让通道在任何 JSON 出站路径上都不泄露凭据。
//
// 为什么必须在这里做，而不是在各 handler 里逐个处理：
//  1. 凭据有**两个**载体 —— `Secret` 字段（json:"-" 已挡住）与 `TargetURL` 的查询串
//     （企业微信 key=、OneBot access_token= 等）。只挡前者是漏的。
//  2. 通道会经多个出口被序列化：列表、详情、创建/更新响应、审计、WS 推送。
//     逐个 handler 处理必然会漏掉某一个 —— 放在类型上才是"一处定义"。
//
// 实现要点：先 Alias 出去拿默认序列化（避免与 json.Marshal 递归），
// 再把 target_url 换成脱敏版本。RedactURL 按 sensitiveQueryKeys 判定哪些参数敏感，
// 命中则替换为 ***（与前端 stripToken 的占位一致）。
func (c NotificationChannel) MarshalJSON() ([]byte, error) {
	type alias NotificationChannel
	safe := alias(c)
	safe.TargetURL = RedactTargetURL(c.TargetURL)
	return json.Marshal(safe)
}

// RedactTargetURL 是 models 包对 notify.RedactURL 的薄封装。
//
// 为什么不直接引用 notify 包：notify 依赖 models（它要读 NotificationChannel），
// 反向引用会形成 import 环。脱敏规则本身只依赖 net/url 与一份敏感参数名表，
// 因此这里保留一份**最小实现**，并由 TestRedactTargetURLMatchesNotify 守住两者行为一致。
func RedactTargetURL(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	// 用户凭据 (user:pass@host) 一律剥掉。
	if parsed.User != nil {
		parsed.User = nil
	}
	if parsed.RawQuery != "" {
		query := parsed.Query()
		changed := false
		for name := range query {
			if sensitiveQueryKey(strings.ToLower(name)) {
				query.Set(name, "***")
				changed = true
			}
		}
		if changed {
			parsed.RawQuery = query.Encode()
		}
	}
	return parsed.String()
}

// sensitiveQueryKeys 与 notify 包的判定保持一致（见 RedactTargetURL 的说明）。
var sensitiveQueryKeys = map[string]bool{
	"key": true, "access_token": true, "token": true, "secret": true,
	"apikey": true, "api_key": true, "password": true, "passwd": true,
	"signature": true, "sig": true, "auth": true, "authorization": true,
}

func sensitiveQueryKey(lower string) bool { return sensitiveQueryKeys[lower] }

// ApplySecret 写入密钥并同步辨认提示 (只写不读的唯一写入口)。
// secret 为空串表示清空密钥。
func (c *NotificationChannel) ApplySecret(secret string) {
	c.Secret = secret
	c.SecretHint = SecretHintOf(secret)
}

// SecretHintOf 取密钥末 4 位作为辨认提示 (设计 §3 SecretHint 注释)。
//
// 长度不足 4 位的密钥不回显原文, 一律以 '*' 占位 —— 否则"提示"就等于"密钥",
// 与 §7.3「密钥不落日志/不回显」冲突。长度为 0 时返回空串 (表示未设置)。
func SecretHintOf(secret string) string {
	if secret == "" {
		return ""
	}
	runes := []rune(secret)
	if len(runes) <= 4 {
		return strings.Repeat("*", len(runes))
	}
	return string(runes[len(runes)-4:])
}

// NewNotificationChannel 构造一个带显式默认值的通道。
// nil = 未配置 (由应用层归一化到设计默认值), 非 nil 的 0 是用户显式零值。
func NewNotificationChannel(name, channelType, targetURL string, timeoutSec, maxRetries *int) NotificationChannel {
	return NotificationChannel{
		Name:       name,
		Type:       channelType,
		TargetURL:  targetURL,
		MinLevel:   ChannelMinLevelInfo,
		Enabled:    true,
		TimeoutSec: timeoutSec,
		MaxRetries: maxRetries,
	}
}

// NotificationDelivery 投递审计 (每次尝试一行)。
//
// 为什么不用 notifications 上的标记列 (设计 §3): 一条通知可投多个通道、每个通道可重试多次,
// 是 1:N:M 关系; 压进一张表会丢失"哪个通道、第几次尝试、响应码多少"。
type NotificationDelivery struct {
	ID             uint64    `gorm:"primaryKey" json:"id"`
	NotificationID uint      `gorm:"index" json:"notification_id"`
	ChannelID      uint      `gorm:"index" json:"channel_id"`
	State          string    `gorm:"size:24;not null;index" json:"state"` // pending|delivered|failed
	AttemptNo      uint32    `gorm:"not null;default:1" json:"attempt_no"`
	StatusCode     int       `json:"status_code"`
	ErrorMessage   string    `gorm:"size:512" json:"error_message"`
	DurationMs     int64     `json:"duration_ms"`
	CreatedAt      time.Time `gorm:"not null;index" json:"created_at"`
}

func (NotificationDelivery) TableName() string { return "notification_deliveries" }
