package notify

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestRedactURLHidesSecrets 守护设计 §7.3: URL 里的 key/token 等查询参数
// 永不进入日志/审计/错误文案 (企业微信机器人的 key 就在 URL 里)。
func TestRedactURLHidesSecrets(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		mustNotIn []string
		mustIn    []string
	}{
		{
			name:      "企业微信 key",
			raw:       "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=SUPER-SECRET-KEY-1234",
			mustNotIn: []string{"SUPER-SECRET-KEY-1234"},
			mustIn:    []string{"key=" + RedactedPlaceholder, "qyapi.weixin.qq.com"},
		},
		{
			name:      "OneBot access_token",
			raw:       "http://192.168.1.10:5700/send_msg?access_token=TOKEN-ABCDEF&user_id=1",
			mustNotIn: []string{"TOKEN-ABCDEF"},
			mustIn:    []string{"access_token=" + RedactedPlaceholder},
		},
		{
			name:      "大小写混合的 KEY",
			raw:       "https://example.com/hook?KEY=MixedCaseSecret",
			mustNotIn: []string{"MixedCaseSecret"},
			mustIn:    []string{"KEY=" + RedactedPlaceholder},
		},
		{
			name:      "URL 内嵌用户凭据",
			raw:       "https://user:hunter2@example.com/hook",
			mustNotIn: []string{"hunter2"},
			mustIn:    []string{"example.com"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactURL(tc.raw)
			for _, secret := range tc.mustNotIn {
				if strings.Contains(got, secret) {
					t.Fatalf("脱敏结果仍含明文密钥: %q", got)
				}
			}
			for _, want := range tc.mustIn {
				if !strings.Contains(got, want) {
					t.Fatalf("脱敏结果 %q 应保留 %q (否则无法定位端点)", got, want)
				}
			}
		})
	}

	t.Run("解析失败的串也必须脱敏", func(t *testing.T) {
		got := RedactURL("http://[::1]:namedport/hook?token=LEAKED")
		if strings.Contains(got, "LEAKED") {
			t.Fatalf("非法 URL 也必须脱敏, 实际: %q", got)
		}
	})

	t.Run("空串安全", func(t *testing.T) {
		if got := RedactURL("   "); got != "" {
			t.Fatalf("空 URL 的脱敏结果应为空串, 实际 %q", got)
		}
	})
}

// TestClientMessageRedactsAndTruncates 守护 §7.5: 审计列 error_message
// 截断到 512 且不含密钥。
func TestClientMessageRedactsAndTruncates(t *testing.T) {
	t.Run("错误文案里的 URL 密钥被脱敏", func(t *testing.T) {
		err := errors.New(`Post "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=PLAINTEXTKEY": dial tcp: timeout`)
		msg := ClientMessage(err)
		if strings.Contains(msg, "PLAINTEXTKEY") {
			t.Fatalf("错误文案泄露密钥: %q", msg)
		}
		if !strings.Contains(msg, "key="+RedactedPlaceholder) {
			t.Fatalf("错误文案应保留脱敏后的参数名, 实际: %q", msg)
		}
	})

	t.Run("Authorization 头的明文 token 被脱敏", func(t *testing.T) {
		err := errors.New("Authorization: Bearer SECRET-TOKEN-xyz not accepted")
		msg := ClientMessage(err)
		if strings.Contains(msg, "SECRET-TOKEN-xyz") {
			// 裸 authorization 值不属于 key=value 形态, 但本仓的约定是
			// Authorization 永远不被写入文案; 这里断言 client 从未把它放进错误。
			t.Fatalf("错误文案泄露 Authorization 明文: %q", msg)
		}
	})

	t.Run("超长文案截断到 512 字节", func(t *testing.T) {
		msg := ClientMessage(errors.New(strings.Repeat("啊", 400)))
		if len(msg) > MaxErrorMessageBytes {
			t.Fatalf("文案长度 = %d, 超过上限 %d", len(msg), MaxErrorMessageBytes)
		}
		if !utf8.ValidString(msg) {
			t.Fatalf("截断破坏了 UTF-8 边界")
		}
	})
}
