//go:build simulation

package catalog

// authTamperSegment 的不变量回归 —— 2026-09-17。
//
// 背景：SIM-AUTH-007 断言「篡改签名的令牌必须 401」，但它先前**时红时绿**，
// 已两次让 CI 的仿真 job 变红（本地多轮绿）。根因不在服务端，而在**构造篡改的方式**：
//
//   HS256 签名 32 字节 → base64url(无填充) 恰为 **43 字符**（43×6=258 位 > 256 位有效），
//   ⇒ **末字符只有低 2 位参与解码**。旧实现把末字符改成 'A'（若已是 'A' 则改 'B'），
//   当末字符本来就是 'A' 时，'B' 的低 2 位与 'A' 相同 ⇒ 解码后签名**逐字节不变** ⇒
//   令牌其实没被篡改，服务端返回 200，用例却断言 401 ⇒ 假红。概率 1/16 ≈ 6.25%。
//
// 本测试把「篡改必须真的改变解码结果」钉死为**确定性**判据，不依赖概率。
// 为什么必须有它：SIM-AUTH-007 是端到端场景，跑一次要起整套服务；
// 而 6.25% 的偶发率意味着「本地多轮绿」根本不能证伪缺陷 —— 只能靠穷举输入。
import (
	"encoding/base64"
	"strings"
	"testing"
)

// b64urlDecode 按 base64url(无填充) 解码。
func b64urlDecode(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("base64url 解码失败 %q: %v", s, err)
	}
	return b
}

// TestAuthTamperSegmentAlwaysChangesDecodedBytes 是核心不变量：
// 对**所有** 64 个 base64url 字母表字符作为末字符/首字符的情形，
// 篡改后的解码结果都必须与原串不同。
func TestAuthTamperSegmentAlwaysChangesDecodedBytes(t *testing.T) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

	// 构造一个 43 字符的合法 base64url 串（32 字节 = HS256 签名长度）。
	// 逐字符替换首位/末位，覆盖全部 64 种取值 —— 这是穷举，不是抽样。
	for _, pos := range []string{"first", "last"} {
		for i := 0; i < len(alphabet); i++ {
			body := strings.Repeat("A", 42)
			var raw string
			if pos == "first" {
				raw = string(alphabet[i]) + body
			} else {
				raw = body + string(alphabet[i])
			}
			if len(raw) != 43 {
				t.Fatalf("构造的段长度应为 43，实际 %d", len(raw))
			}

			tampered := authTamperSegment(raw)
			if tampered == raw {
				t.Fatalf("%s 字符 %q：篡改后字符串未变化", pos, alphabet[i])
			}

			// 关键判据：**解码后的字节**必须不同（仅字符串不同是不够的）。
			orig := b64urlDecode(t, raw)
			after := b64urlDecode(t, tampered)
			if string(orig) == string(after) {
				t.Fatalf("%s 字符 %q：篡改后**解码字节未变**（长度 %d）—— 「篡改」没生效，"+
					"这会让 SIM-AUTH-007 变成假红（服务端返回 200 而非 401）", pos, alphabet[i], len(orig))
			}
		}
	}
}

// TestAuthTamperSegmentLastCharIsUnsafe 记录**为什么不能用末字符**，
// 作为「别改回末字符」的反向守卫：本用例断言「确实存在末字符取值使解码不变」，
// 从而说明末字符方案在原理上不可靠。
func TestAuthTamperSegmentLastCharIsUnsafe(t *testing.T) {
	// 'A'(index 0) 与 'B'(index 1) 的低 2 位相同；43 字符时末字符只低 2 位有效。
	base := strings.Repeat("A", 42) + "A"
	swappedB := strings.Repeat("A", 42) + "B"
	if string(b64urlDecode(t, base)) != string(b64urlDecode(t, swappedB)) {
		t.Fatalf("前提失效：43 字符 base64url 的末字符 A→B 本应不改变解码结果；" +
			"若此断言失败，说明编码/长度前提变了，需重新评估 authTamperSegment 的修法")
	}
}
