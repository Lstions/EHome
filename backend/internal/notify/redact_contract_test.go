package notify

import (
	"fmt"
	"strings"
	"testing"

	"ehome/backend/internal/models"
)

// TestRedactTargetURLMatchesNotify 是 P0-A 补上的那条"注释声称存在、实际不存在"的
// 守护测试: models.RedactTargetURL 与 notify.RedactURL 必须**逐字节**给出同一结果。
//
// 为什么只能放在 notify 包 (而不是 models 包): 依赖方向是 notify → models,
// models 侧的测试文件 import notify 会在**编译期**就失败 (import cycle),
// 所以"两个函数一致"这句话只有在 notify 侧才可执行地被断言。
//
// 每条用例都**同时调用两个函数并互相比对**, 不断言任何一侧的期望串 —— 期望串会
// 把"某一侧的实现"固化成"标准", 那样另一边就算漂移也测不出来。唯一被硬编码的
// 期望是**安全性质**(明文密钥不得出现), 那是两侧都必须满足的不变量。
func TestRedactTargetURLMatchesNotify(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		// mustNotContain 是两侧都不得出现的内容 (安全性质, 不是格式快照)。
		mustNotContain []string
		// mustContain 是两侧都必须保留的内容 (否则无法定位端点)。
		mustContain []string
	}{
		{
			name:           "有 key (企业微信形态)",
			raw:            "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=SUPER-SECRET-KEY-1234",
			mustNotContain: []string{"SUPER-SECRET-KEY-1234"},
			mustContain:    []string{"qyapi.weixin.qq.com", "key="},
		},
		{
			name:           "无 key 的普通 URL",
			raw:            "https://example.com/hook/abc",
			mustNotContain: []string{},
			mustContain:    []string{"example.com", "/hook/abc"},
		},
		{
			name:           "带特殊字符 (未编码的值)",
			raw:            "https://example.com/hook?name=a b&key=SEC RET&tag=<x>&x=1",
			mustNotContain: []string{"SEC RET", "SEC%20RET"},
			mustContain:    []string{"key="},
		},
		{
			name: "值本身是百分号编码 (裸 ReplaceAll 的对照面)",
			raw:  "https://example.com/hook?key=a%2Ab&token=%2A%2A%2A",
			// 密钥已被屏蔽; 关键在于两侧对 %2A 的处理必须一致,
			// 而不是"把结果里所有 %2A%2A%2A 都换成 ***"。
			mustNotContain: []string{},
			mustContain:    []string{"key="},
		},
		{
			name: "裸 ReplaceAll 会篡改非敏感参数的用户数据",
			raw:  "https://example.com/hook?note=***&key=SECRET",
			// note 不是敏感参数, 它的 "***" 属于用户数据: 必须保持原样
			// (notify 输出 note=%2A%2A%2A —— 重编码后的形态), 不得被改成 note=***。
			mustNotContain: []string{"SECRET"},
			mustContain:    []string{"note="},
		},
		{
			name:           "空值 (空参数值)",
			raw:            "https://example.com/hook?key=&token=",
			mustNotContain: []string{},
			mustContain:    []string{"key="},
		},
		{
			name:           "空串与纯空白",
			raw:            "",
			mustNotContain: []string{},
			mustContain:    []string{},
		},
		{
			name:           "纯空白输入",
			raw:            "   \t ",
			mustNotContain: []string{},
			mustContain:    []string{},
		},
		{
			name:           "user:pass@host 用户凭据",
			raw:            "https://admin:hunter2@example.com/hook?key=SECRET",
			mustNotContain: []string{"hunter2", "admin:", "SECRET"},
			mustContain:    []string{"example.com"},
		},
		{
			name: "解析失败 (非法端口) 且带 ?token=",
			raw:  "http://[::1]:namedport/hook?token=LEAKED",
			// 解析失败时 models 必须与 notify 一样走文本级兜底, 绝不原样返回。
			mustNotContain: []string{"LEAKED"},
			mustContain:    []string{"token="},
		},
		{
			name:           "解析失败 (控制字符) 且带 ?token=",
			raw:            "https://example.com/hook?token=LEAKED\u007f",
			mustNotContain: []string{"LEAKED"},
			mustContain:    []string{"token="},
		},
		{
			name:           "只在 notify 表里的键 pwd",
			raw:            "https://example.com/hook?pwd=SECRET",
			mustNotContain: []string{"SECRET"},
			mustContain:    []string{"pwd="},
		},
		{
			name:           "只在 notify 表里的键 app_secret",
			raw:            "https://example.com/hook?app_secret=SECRET",
			mustNotContain: []string{"SECRET"},
			mustContain:    []string{"app_secret="},
		},
		// 以下三条是**专门用来发现"敏感名表少了一个键"**的用例。
		// 为什么普通用例做不到: 值非空时, 解析层重编码出的 %2A%2A%2A 会被最后那道
		// 文本级兜底正则收回成 *** —— 就算表里**没有** pwd, 正则也会把 ?pwd=SECRET
		// 变成 pwd=***, 两侧输出照样相同 (变异自证实测: 删掉 models 表的 pwd 键,
		// 只跑普通用例仍然全绿)。而**空值**参数不满足正则的 ([^&\s"'#]+) 至少一字符,
		// 兜底正则不会命中 ⇒ 输出形态只能由"表里有没有这个键"决定, 表一缺键立刻分歧。
		{
			name: "表缺键才会暴露: 空值的 pwd",
			raw:  "https://example.com/hook?pwd=",
			// notify 把空值也置为占位符 (query.Set 不看值); models 表若缺 pwd,
			// 则既不置占位符、正则也不命中 ⇒ 输出停在 "?pwd=" ⇒ 与 notify 分歧。
			mustNotContain: []string{},
			mustContain:    []string{"pwd="},
		},
		{
			name:           "表缺键才会暴露: 空值的 app_secret",
			raw:            "https://example.com/hook?app_secret=",
			mustNotContain: []string{},
			mustContain:    []string{"app_secret="},
		},
		{
			name: "表缺键才会暴露: 空格值被 Encode 成 + 后残留在兜底正则之外",
			raw:  "https://example.com/hook?pwd=a b",
			// notify: 整值置占位符 ⇒ pwd=***; models 缺 pwd: Encode 出 pwd=a+b,
			// 正则只吃到 "pwd=a" ⇒ pwd=***+b (残留 "+b") ⇒ 分歧。
			mustNotContain: []string{},
			mustContain:    []string{"pwd="},
		},
		{
			name:           "多参数混合 (敏感 + 非敏感 + 大小写)",
			raw:            "http://192.168.1.10:5700/send_msg?access_token=TOKEN-ABCDEF&KEY=MixedCase&user_id=1&note=hello%20world",
			mustNotContain: []string{"TOKEN-ABCDEF", "MixedCase"},
			mustContain:    []string{"192.168.1.10:5700", "user_id=1"},
		},
		{
			name:           "无查询串但带 fragment",
			raw:            "https://example.com/hook#section",
			mustNotContain: []string{},
			mustContain:    []string{"example.com", "section"},
		},
		{
			name:           "只有查询串 (裸 query)",
			raw:            "?key=SECRET&x=1",
			mustNotContain: []string{"SECRET"},
			mustContain:    []string{"key="},
		},
	}

	// 占位符常量必须同值。这条断言在下面"逐字节比对"之外**额外**存在的理由:
	// 输出相同是**必要**条件, 不是全部 —— 常量一旦被改成别的形态 (例如
	// "%2A%2A%2A"), Encode() 会先转义成 %252A%252A%252A, 兜底正则再把它收回
	// "***", 输出就与 notify 相同了; 此时函数体里读到的常量值其实已经跑偏。
	// 直接对常量断言能把这个"函数体内部值"钉死, 不必依赖它是否恰好泄漏到输出。
	t.Run("占位符常量同值", func(t *testing.T) {
		if RedactedPlaceholder != models.RedactedPlaceholder {
			t.Fatalf("占位符漂移: notify=%q models=%q", RedactedPlaceholder, models.RedactedPlaceholder)
		}
	})

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotModels := models.RedactTargetURL(tc.raw)
			gotNotify := RedactURL(tc.raw)
			t.Logf("raw=%q\n  models=%q\n  notify=%q", tc.raw, gotModels, gotNotify)

			if gotModels != gotNotify {
				t.Fatalf("两侧脱敏结果不一致 (逐字节比较失败):\n  models.RedactTargetURL = %q\n  notify.RedactURL       = %q\n  首个差异: %s",
					gotModels, gotNotify, firstByteDiff(gotModels, gotNotify))
			}

			for _, secret := range tc.mustNotContain {
				if strings.Contains(gotModels, secret) {
					t.Fatalf("脱敏结果仍含明文 %q: models=%q notify=%q", secret, gotModels, gotNotify)
				}
			}
			for _, want := range tc.mustContain {
				if !strings.Contains(gotModels, want) {
					t.Fatalf("脱敏结果 %q 应保留 %q (否则无法定位端点)", gotModels, want)
				}
			}
		})
	}
}

// firstByteDiff 给出第一个不一致字节的位置与原值, 让契约漂移的失败信息可直接定位。
func firstByteDiff(a, b string) string {
	n := min(len(a), len(b))
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return fmt.Sprintf("index=%d models[%d]=%q notify[%d]=%q", i, i, a[i], i, b[i])
		}
	}
	if len(a) != len(b) {
		return fmt.Sprintf("长度不同: models=%d notify=%d", len(a), len(b))
	}
	return "无差异 (不应到达这里)"
}

// TestRedactedPlaceholderMatchesModels 断言两侧占位符常量同值。
// 两份实现之所以存在, 只是因为 models 不能 import notify (import cycle);
// 占位符一旦不同值, "两边输出相同"立刻不成立。
func TestRedactedPlaceholderMatchesModels(t *testing.T) {
	if RedactedPlaceholder != models.RedactedPlaceholder {
		t.Fatalf("占位符漂移: notify=%q models=%q", RedactedPlaceholder, models.RedactedPlaceholder)
	}
	if RedactedPlaceholder != "***" {
		t.Fatalf("占位符 %q 与前端 stripToken 的 \"***\" 不一致", RedactedPlaceholder)
	}
}

// TestSensitiveKeyTablesAgree 是上面逐字节比对的**源头对拍**: 输出相同还不够,
// 两张敏感参数名表本身必须覆盖同一批键。表短一个键 = 一条真实泄露面
// (notify 认 pwd 而 models 不认时, ?pwd=SECRET 会在 API 响应里原样回显)。
func TestSensitiveKeyTablesAgree(t *testing.T) {
	// 冻结的键清单: 任何一侧增删键都必须同时改这里, 该测试就是那次改动的闸门。
	canonical := []string{
		"key", "token", "access_token", "accesstoken",
		"secret", "password", "passwd", "pwd",
		"sign", "signature", "sig", "auth", "authorization",
		"apikey", "api_key", "appkey", "app_secret",
	}

	if len(sensitiveQueryKeys) != len(canonical) {
		t.Fatalf("notify 敏感名表条目数 = %d, 冻结清单 = %d (表变了就必须同步清单与 models 侧)", len(sensitiveQueryKeys), len(canonical))
	}
	for _, k := range canonical {
		if !sensitiveQueryKeys[k] {
			t.Errorf("notify 敏感名表缺少冻结键 %q", k)
		}
		raw := "https://example.com/hook?" + k + "=SECRET"
		if got := RedactURL(raw); strings.Contains(got, "SECRET") {
			t.Errorf("notify 未脱敏键 %q: %q", k, got)
		}
		// models 侧的表在同包内不可见, 只能行为对拍: 同一个输入也必须被屏蔽。
		if got := models.RedactTargetURL(raw); strings.Contains(got, "SECRET") {
			t.Errorf("models 未脱敏键 %q (表比 notify 短 = 泄露面): %q", k, got)
		}

		// 关键探针: **空值**参数。上面那条非空值用例其实被最后的文本级兜底正则
		// 兜住了 —— 表里缺键时它照样输出 ***, 测不出表少了键 (变异自证已实测)。
		// 空值不满足正则的"至少一字符", 所以只有"表里有这个键"才能得到占位符:
		// 两侧都必须把 ?k= 变成 ?k=***, 任一侧表缺键 ⇒ 一侧停在 "?k=" ⇒ 红。
		emptyRaw := "https://example.com/hook?" + k + "="
		gotNotify := RedactURL(emptyRaw)
		gotModels := models.RedactTargetURL(emptyRaw)
		if gotNotify != gotModels {
			t.Errorf("键 %q 的两侧输出不一致: notify=%q models=%q", k, gotNotify, gotModels)
		}
		// 断言"值确实被换掉了", 而不是断言某个固定期望串: 有少数键 (apikey /
		// authorization / x-api-key) 同时被 redactHeaderRe 命中, 占位符会被写成
		// "apikey: ***" 而非 "apikey=***" —— 这是从 notify 继承来的既有行为,
		// 只能等价保留, 不能在本任务里顺手"修正"。判据是"原文没被原样留下"。
		if gotNotify == emptyRaw {
			t.Errorf("键 %q 在 notify 侧未被置为占位符 (表缺键?): %q", k, gotNotify)
		}
		if gotModels == emptyRaw {
			t.Errorf("键 %q 在 models 侧未被置为占位符 (models 表比 notify 短 = 泄露面): %q", k, gotModels)
		}
		if !strings.Contains(gotModels, RedactedPlaceholder) {
			t.Errorf("键 %q 的 models 输出不含占位符 %q: %q", k, RedactedPlaceholder, gotModels)
		}
	}
}
