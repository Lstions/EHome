package models

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestNotificationChannelSecretNeverSerialized 是设计 §2.3-b「只写不读」的**核心断言**:
// 密钥在库里、在出站请求里都存在, 但在**任何 JSON 序列化结果**里都不存在。
//
// 断言的是真实的序列化产物 (json.Marshal 的输出 + 反序列化后的键集合), 不是源码字符串。
// 变异自证: 把 Secret 的标签改成 json:"secret" (或去掉 json:"-"), 本测试必红。
func TestNotificationChannelSecretNeverSerialized(t *testing.T) {
	const secret = "SUPER-SECRET-WECOM-KEY-9876"
	channel := NotificationChannel{
		ID:           7,
		Name:         "家庭群",
		Type:         ChannelTypeWeCom,
		TargetURL:    "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=" + secret,
		MinLevel:     ChannelMinLevelInfo,
		Enabled:      true,
		AllowPrivate: false,
	}
	channel.ApplySecret(secret)

	if channel.Secret != secret {
		t.Fatalf("ApplySecret 未写入密钥")
	}
	if channel.SecretHint != "9876" {
		t.Fatalf("secret_hint = %q, 期望末 4 位 9876", channel.SecretHint)
	}

	// 1) 单对象序列化
	single, err := json.Marshal(channel)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	assertNoSecret(t, "单对象", single, secret)

	// 2) 列表序列化 (GET /api/v1/notification-channels 的真实形状)
	list, err := json.Marshal([]NotificationChannel{channel})
	if err != nil {
		t.Fatalf("列表序列化失败: %v", err)
	}
	assertNoSecret(t, "列表", list, secret)

	// 3) 嵌在信封里的序列化 (api/envelope.go 的 Success 形状)
	envelope, err := json.Marshal(map[string]any{"success": true, "data": channel})
	if err != nil {
		t.Fatalf("信封序列化失败: %v", err)
	}
	assertNoSecret(t, "信封", envelope, secret)

	// 4) 反序列化后的键集合里也不得有 secret
	var decoded map[string]any
	if err := json.Unmarshal(single, &decoded); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	if _, exists := decoded["secret"]; exists {
		t.Fatalf("序列化结果出现了 secret 键: %s", single)
	}
	if got, ok := decoded["secret_hint"]; !ok || got != "9876" {
		t.Fatalf("必须回显 secret_hint 供用户辨认, 实际 %v", decoded["secret_hint"])
	}
}

// TestNotificationChannelJSONContractMatchesFrozenModel 固化设计 §3 冻结的字段名。
// 前端五段式接线 (设计 §8) 直接依赖这些键名。
func TestNotificationChannelJSONContractMatchesFrozenModel(t *testing.T) {
	raw, err := json.Marshal(NotificationChannel{})
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	for _, want := range []string{
		"id", "name", "type", "target_url", "secret_hint", "template",
		"min_level", "enabled", "timeout_sec", "max_retries", "allow_private",
		"created_at", "updated_at",
	} {
		if _, ok := keys[want]; !ok {
			t.Fatalf("冻结契约缺少字段 %q (设计 §3)", want)
		}
	}
	for _, forbidden := range []string{"secret", "Secret"} {
		if _, ok := keys[forbidden]; ok {
			t.Fatalf("冻结契约不允许出现字段 %q (设计 §2.3-b 只写不读)", forbidden)
		}
	}
}

// TestNotificationDeliveryJSONContract 固化投递审计的字段名 (设计 §3)。
func TestNotificationDeliveryJSONContract(t *testing.T) {
	raw, err := json.Marshal(NotificationDelivery{})
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	for _, want := range []string{"id", "notification_id", "channel_id", "state", "attempt_no", "status_code", "error_message", "duration_ms", "created_at"} {
		if _, ok := keys[want]; !ok {
			t.Fatalf("投递审计缺少字段 %q (设计 §3)", want)
		}
	}
}

// TestNotificationChannelTableNames 固化表名 (AutoMigrate 与审计保留依赖它)。
func TestNotificationChannelTableNames(t *testing.T) {
	if got := (NotificationChannel{}).TableName(); got != "notification_channels" {
		t.Fatalf("通道表名 = %q, 期望 notification_channels", got)
	}
	if got := (NotificationDelivery{}).TableName(); got != "notification_deliveries" {
		t.Fatalf("审计表名 = %q, 期望 notification_deliveries", got)
	}
}

// TestSecretHintOf 守护"提示不等于密钥": 短密钥不得被完整回显。
func TestSecretHintOf(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"a", "*"},
		{"ab", "**"},
		{"abcd", "****"},
		{"abcdef", "cdef"},
		{"WECOM-KEY-1234", "1234"},
	}
	for _, tc := range cases {
		// got 在循环体内声明: 下面两个断言都要用它（原写法把它限制在 if 作用域，
		// 第二个断言引用不到 —— 这正是它编译不过的原因）。
		got := SecretHintOf(tc.in)
		if got != tc.want {
			t.Fatalf("SecretHintOf(%q) = %q, 期望 %q", tc.in, got, tc.want)
		}
		if tc.in != "" && len(tc.in) <= 4 && got == tc.in {
			t.Fatalf("长度 <=4 的密钥被完整回显: %q", got)
		}
	}
}

// TestNewNotificationChannelDefaults 固化"默认值只由应用层给出" (设计 §7.6):
// nil 表示未配置, 由 notify 包归一化到 10s / 2 次; 显式 0 必须原样保留。
func TestNewNotificationChannelDefaults(t *testing.T) {
	fresh := NewNotificationChannel("a", ChannelTypeWebhook, "https://example.com/hook", nil, nil)
	if !fresh.Enabled {
		t.Fatalf("新建通道必须默认启用 (Enabled=true)")
	}
	if fresh.TimeoutSec != nil || fresh.MaxRetries != nil {
		t.Fatalf("nil = 未配置, 必须保持 nil 交给应用层归一化")
	}
	if fresh.MinLevel != ChannelMinLevelInfo {
		t.Fatalf("新建通道的 min_level = %q, 期望 info", fresh.MinLevel)
	}

	zero := 0
	explicit := NewNotificationChannel("b", ChannelTypeWebhook, "https://example.com/hook", &zero, &zero)
	if explicit.MaxRetries == nil || *explicit.MaxRetries != 0 {
		t.Fatalf("显式 0 必须被原样保留 (设计 §7.6)")
	}
}

// assertNoSecret 断言序列化产物里既没有 secret 键, 也没有明文密钥。
func assertNoSecret(t *testing.T, label string, raw []byte, secret string) {
	t.Helper()
	text := string(raw)
	if strings.Contains(text, secret) {
		t.Fatalf("%s序列化结果泄露明文密钥: %s", label, text)
	}
	if strings.Contains(text, "\"secret\":") {
		t.Fatalf("%s序列化结果出现了 secret 键: %s", label, text)
	}
}
