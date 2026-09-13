package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"
	"time"

	"ehome/backend/internal/models"
)

// 请求体模板与级别过滤。
//
// 设计依据: 设计/外发通知通道.md §2.4 (两个预设模板都是 HTTP POST JSON,
// 差异只在 URL 形态与 body 形状)、§11.3 (MinLevel ↔ 通知 type 的映射规则)。
// 模板语法是 Go text/template: {{.Title}} / {{.Message}} / {{.Level}} ...

// Message 是投递给单个通道的消息事实 (模板可见的全部字段)。
type Message struct {
	NotificationID uint
	Type           string
	Title          string
	Message        string
	Description    string
	Source         string
	SourceID       string
	CreatedAt      time.Time
}

// Level 把通知的 type 映射成通道侧的三级 min_level (设计 §11.3 要求明确
// error→critical 的反向映射): critical|error→critical, warning→warning,
// 其余 (含 info) →info。
//
// 与 models.NotificationType (models/alert.go:71) 是**同一张映射表的两个方向**:
// 那边 critical→error, 这边 error→critical。
func (m Message) Level() string {
	switch m.Type {
	case "error", models.AlertLevelCritical:
		return models.ChannelMinLevelCritical
	case models.AlertLevelWarning:
		return models.ChannelMinLevelWarning
	default:
		return models.ChannelMinLevelInfo
	}
}

// MinLevelOf 归一化通道级别配置: 非法值一律按 info 处理 (最宽松),
// 避免脏配置把通知静默丢掉 (取证 §7 指出的"脏值静默降级"反例)。
func MinLevelOf(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case models.ChannelMinLevelCritical:
		return models.ChannelMinLevelCritical
	case models.ChannelMinLevelWarning:
		return models.ChannelMinLevelWarning
	default:
		return models.ChannelMinLevelInfo
	}
}

// levelRank 是级别的大小关系: info < warning < critical。
func levelRank(level string) int {
	switch MinLevelOf(level) {
	case models.ChannelMinLevelCritical:
		return 3
	case models.ChannelMinLevelWarning:
		return 2
	default:
		return 1
	}
}

// ShouldDeliver 判定一条消息是否投递到该通道。
// 语义 (设计 §5 通道状态 + §9.1 MinLevel 过滤): 只有 message 级别 >= 通道
// min_level 才投递; 被过滤掉**不是失败**, 不写投递审计、不计指标。
func ShouldDeliver(channel models.NotificationChannel, msg Message) bool {
	return levelRank(msg.Level()) >= levelRank(channel.MinLevel)
}

// 预设模板 (设计 §2.4)。预设只是**默认值**: 通道 Template 为空时按 Type 取,
// 用户可完全覆盖; 三者共用同一套投递机制。
const (
	// DefaultWebhookTemplate 是通用 webhook: 把通知事实结构化外发, 便于第三方自行解析。
	// 用 backtick 原始字符串是因为模板内部同时含 JSON 双引号与 text/template 的 printf 双引号。
	DefaultWebhookTemplate = `{"title":{{printf "%q" .Title}},"message":{{printf "%q" .Message}},"level":"{{.Level}}","source":"{{.Source}}","source_id":"{{.SourceID}}","notification_id":{{.NotificationID}}}`
	// DefaultWeComTemplate 是企业微信群机器人 (markdown 消息, 设计 §2.4)。
	DefaultWeComTemplate = `{"msgtype":"markdown","markdown":{"content":{{printf "%q" (printf "**%s**\n%s" .Title .Message)}}}}`
	// DefaultOneBotTemplate 是 OneBot v11 私聊消息 (设计 §2.4; 群聊把 user_id 换成 group_id)。
	DefaultOneBotTemplate = `{"user_id":0,"message":{{printf "%q" (printf "%s\n%s" .Title .Message)}},"auto_escape":false}`
)

// DefaultTemplate 返回预设模板。
func DefaultTemplate(channelType string) string {
	switch channelType {
	case models.ChannelTypeWeCom:
		return DefaultWeComTemplate
	case models.ChannelTypeOneBot:
		return DefaultOneBotTemplate
	default:
		return DefaultWebhookTemplate
	}
}

// BuildPayload 渲染通道的请求体。渲染结果必须是合法 JSON
// (所有预设模板与外发端点都按 application/json 接收)。
func BuildPayload(channel models.NotificationChannel, msg Message) ([]byte, error) {
	text := strings.TrimSpace(channel.Template)
	if text == "" {
		text = DefaultTemplate(channel.Type)
	}
	parsed, err := template.New("notification").Option("missingkey=zero").Parse(text)
	if err != nil {
		return nil, fmt.Errorf("channel template is invalid: %w", err)
	}
	var buf bytes.Buffer
	if err := parsed.Execute(&buf, msg); err != nil {
		return nil, fmt.Errorf("channel template render failed: %w", err)
	}
	payload := buf.Bytes()
	if !json.Valid(payload) {
		return nil, fmt.Errorf("channel template did not produce valid JSON")
	}
	return payload, nil
}

// MessageFromNotification 把通知行转成模板可见的消息事实。
func MessageFromNotification(n models.Notification) Message {
	return Message{
		NotificationID: n.ID,
		Type:           n.Type,
		Title:          n.Title,
		Message:        n.Message,
		Description:    n.Description,
		Source:         n.Source,
		SourceID:       n.SourceID,
		CreatedAt:      n.CreatedAt,
	}
}
