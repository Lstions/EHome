package models

import (
	"encoding/json"
	"net/url"
	"regexp"
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
// 再把 target_url 换成脱敏版本。脱敏规则见 RedactTargetURL —— 它与
// notify.RedactURL 逐字节一致，占位符是 ***（与前端 stripToken 的占位一致）。
func (c NotificationChannel) MarshalJSON() ([]byte, error) {
	type alias NotificationChannel
	safe := alias(c)
	safe.TargetURL = RedactTargetURL(c.TargetURL)
	return json.Marshal(safe)
}

// RedactedPlaceholder 是 URL / 文案脱敏后的占位符，与 notify.RedactedPlaceholder
// （以及前端 stripToken 的占位）必须同值 —— 两处各有一份实现（原因见
// RedactTargetURL），唯一能防止再次漂移的办法就是"值只有一个定义、由测试断言相等"。
// notify 包的 TestRedactTargetURLMatchesNotify 里有一条断言专门守这个等式。
//
// 写成具名常量而不是字面量：占位符一旦被散布成魔法字符串，改一处漏一处就会
// 重新变成"两边输出不同"的契约漂移（P0-A 修的就是这个）。
const RedactedPlaceholder = "***"

// RedactTargetURL 是 models 包内的**最小脱敏实现**：同一输入下，输出与
// notify.RedactURL **逐字节相同**（由 notify 包的 TestRedactTargetURLMatchesNotify 守护）。
//
// 为什么不直接调用 notify.RedactURL：依赖方向是 notify → models（notify 要读
// NotificationChannel 才能投递），models → notify 会构成 import 环。
// 因此这里只能保留一份语义相同的副本。
//
// 由此还推出一条硬约束：**这个一致性测试不可能写在 models 包**——models 侧的
// 测试文件一旦 import notify，整个测试二进制在编译期就失败（import cycle）。
// 它只能待在 notify 包（internal/notify/redact_contract_test.go）。改本函数时
// 必须同步跑那条测试，否则契约会再次漂移成"注释声称有测试、实际没有"。
//
// 流水线与 notify.RedactURL 完全同构，三步缺一不可：
//  1. 空串早退；url.Parse 失败 ⇒ 走本包的文本级兜底 redactText（绝不原样返回）；
//  2. 剥掉 user:pass@ 用户凭据，并把敏感查询参数置为 RedactedPlaceholder；
//  3. query.Encode() 会把 '*' 百分号转义成 %2A，所以最后再套一遍 redactText，
//     把 key=%2A%2A%2A 收回成 key=***。
//
// 第 3 步**不能**简化成 strings.ReplaceAll(out, "%2A%2A%2A", "***")：那会把
// **非敏感参数里的用户数据**也一起改掉。反例 ?note=***&key=SECRET —— notify
// 输出 key=***&note=%2A%2A%2A（note 是用户数据，保持其百分号编码形态），
// 裸替换会把它篡改成 note=***。必须复用同一套正则，让"哪个值被改"只由参数名决定。
func RedactTargetURL(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		// 解析失败绝不原样回显：非法 URL 里同样可能带 ?token=...（notify 侧同理）。
		return redactText(raw)
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
				query.Set(name, RedactedPlaceholder)
				changed = true
			}
		}
		if changed {
			// url.Values.Encode 按 key 排序重编码（脱敏后的重编码，不要求与原文
			// 逐字节一致）；转义出来的 %2A 由下面 redactText 的兜底正则收回成 ***。
			parsed.RawQuery = query.Encode()
		}
	}
	return redactText(parsed.String())
}

// sensitiveQueryKeys 必须与 notify 包的 sensitiveQueryKeys **逐键一致**。
// 少一个键就是一条真实泄露面：notify 认 pwd / app_secret 而 models 不认时，
// ?pwd=SECRET 会在 API 响应里原样回显（P0-A 实测复现过的漂移）。
//
// 两个包的变量同名不冲突（不同包）；**故意保持同名**，好让"改一个必须改另一个"
// 在 diff 与审阅时一眼可见。守护测试会逐键对拍两张表。
var sensitiveQueryKeys = map[string]bool{
	"key": true, "token": true, "access_token": true, "accesstoken": true,
	"secret": true, "password": true, "passwd": true, "pwd": true,
	"sign": true, "signature": true, "sig": true, "auth": true, "authorization": true,
	"apikey": true, "api_key": true, "appkey": true, "app_secret": true,
}

func sensitiveQueryKey(lower string) bool { return sensitiveQueryKeys[lower] }

// redactURLRe / redactHeaderRe / redactBearerRe 是 notify 包同名正则的**等价副本**
// （import 环说明见 RedactTargetURL）。语义必须与 notify 侧逐字符一致：任何一侧
// 改了正则而另一侧没跟，TestRedactTargetURLMatchesNotify 就会变红。
var (
	// 兜底匹配任何形态 URL 里的敏感查询参数（含裸 query 串与 net/http 错误
	// 文案里被引号包住的 URL）。目标串: key=..., token=..., access_token=... 等，
	// 值到 &/#/空白/引号为止。
	redactURLRe = regexp.MustCompile("(?i)\\b(key|token|access_?token|secret|password|passwd|pwd|sign|signature|sig|api_?key|app_?key|app_?secret|auth|authorization)=([^&\\s\"'#]+)")
	// 兜底匹配 header 形态的凭据 (Authorization: xxx / X-Api-Key: xxx)。
	redactHeaderRe = regexp.MustCompile("(?i)((?:proxy-)?authorization|x-api-key|apikey)\\s*[:=]\\s*([^\\s,;\"']+)")
	// 兜底匹配 Bearer/Basic 方案里的令牌本体。
	redactBearerRe = regexp.MustCompile("(?i)\\b(bearer|basic)\\s+([A-Za-z0-9\\-._~+/=]{4,})")
)

// redactText 与 notify.RedactText 等价（说明见 RedactTargetURL）。
// 用途与 notify 侧一致：解析失败时兜底、以及把 Encode() 转义出的 %2A 收回成 ***。
func redactText(text string) string {
	if text == "" {
		return text
	}
	// 顺序有讲究: 先剥 Bearer/Basic 的令牌本体, 再处理 header 形态 ——
	// 反过来会把 "Bearer" 这个词当成值吃掉, 令牌本体就漏出去了。
	text = redactBearerRe.ReplaceAllString(text, "$1 "+RedactedPlaceholder)
	text = redactHeaderRe.ReplaceAllString(text, "$1: "+RedactedPlaceholder)
	return redactURLRe.ReplaceAllString(text, "$1="+RedactedPlaceholder)
}

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
