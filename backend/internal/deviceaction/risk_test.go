package deviceaction

// 风险两级收敛的防漂移门禁 (方案 §3.3.4 R1/R4, §8 A-7/A-8)。
//
// 这三条都是**源码级/行为级**判据, 且每条都写明"如何注入违规让它变红",
// 避免出现"永远绿"的门禁 (本仓已有此教训, 见 internal/database/operation_logs_gate_test.go)。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ─── R4 / A-8: 4→2 映射完备且无 default 兜底 ───────────────────────────────
// 反向验证: 删掉 medium 分支 → TestRiskLevelFromDriverMappingComplete 红;
// 把出口改成 default 兜底 → TestRiskLevelFromDriverHasNoDefaultFallback 红。
func TestRiskLevelFromDriverMappingComplete(t *testing.T) {
	// 4 个驱动出厂字面量必须**全部**有显式映射 (方案 §3.3.2 映射表)。
	cases := []struct {
		driver string
		want   RiskLevel
		// wantConfirm 是 O-5 裁决 (medium→风险) 的落点: medium/high/critical 都要确认。
		wantConfirm bool
	}{
		{"low", RiskNormal, false},
		{"medium", RiskElevated, true},
		{"high", RiskElevated, true},
		{"critical", RiskElevated, true},
	}
	for _, c := range cases {
		got, ok := RiskLevelFromDriver(c.driver)
		if !ok {
			t.Errorf("驱动风险 %q 未映射 —— 4 级必须全部显式覆盖 (删分支即失败)", c.driver)
			continue
		}
		if got != c.want {
			t.Errorf("RiskLevelFromDriver(%q) = %q, want %q", c.driver, got, c.want)
		}
		if got.RequiresConfirmation() != c.wantConfirm {
			t.Errorf("%q(%q).RequiresConfirmation() = %v, want %v",
				c.driver, got, got.RequiresConfirmation(), c.wantConfirm)
		}
	}
	// 未声明的字面量必须 fail-closed (绝不静默归一化为 normal —— 那会把
	// 一个未声明的风险等级变成"无需确认", 是 fail-open)。
	for _, unknown := range []string{"", "LOW", "extreme", "unknown"} {
		if lvl, ok := RiskLevelFromDriver(unknown); ok {
			t.Errorf("未知风险字面量 %q 被接受为 %q —— 必须 fail-closed", unknown, lvl)
		}
	}
}

// TestRiskLevelFromDriverHasNoDefaultFallback 源码级判据: 归一化函数内
// **不得**出现 default 分支 —— 一旦有 default, 上面的"未知字面量 fail-closed"
// 断言会被一个静默兜底绕过, 映射完备性也就不再可失败。
func TestRiskLevelFromDriverHasNoDefaultFallback(t *testing.T) {
	src := readSourceFile(t, "risk.go")
	body := extractRegion(t, src, "func RiskLevelFromDriver(", "\n}\n")
	if strings.Contains(body, "default:") {
		t.Fatalf("RiskLevelFromDriver 出现 default 兜底 —— 映射完备性将不可失败 (A-8 反向验证)\n%s", body)
	}
	// 分母守卫: 提取到的区块必须真的包含 switch 与四个字面量, 否则提取逻辑失效。
	for _, marker := range []string{"switch raw {", "\"low\"", "\"medium\"", "\"high\"", "\"critical\""} {
		if !strings.Contains(body, marker) {
			t.Fatalf("提取到的 RiskLevelFromDriver 区块缺少 %q —— 门禁提取逻辑失效 (vacuous pass)\n%s", marker, body)
		}
	}
}

// riskLevelConstDecl 只认**常量声明**形态: <Ident> RiskLevel = "<字面量>"。
// 不认 def.RiskLevel = riskLevel 这类字段赋值 (那是消费点, 不是定义点 —— 它右边
// 没有字符串字面量)。
//
// 为什么**不加行首锚点**: 首版写成 `^\s*<Ident> RiskLevel = "` 时, 一行式声明
// `const ShadowRiskLevel RiskLevel = "normal"` 会从锚点后第一个标识符 "const"
// 开始匹配, 于是漏判 —— A-7 的反向验证注入当场逃逸 (门禁恒绿)。去掉锚点后,
// 判据变成"任意位置出现 <Ident> RiskLevel = "字面量""。
var riskLevelConstDecl = regexp.MustCompile("[A-Za-z_][A-Za-z0-9_]*[ \\t]+RiskLevel[ \\t]*=[ \\t]*\\\"")

// ─── R1 / A-7: 风险等级常量只有一个定义点 ─────────────────────────────────
// 反向验证: 在第二个文件里再写一行 RiskNormal RiskLevel = "normal" → 本门禁红。
func TestRiskLevelConstantsHaveSingleDefinitionPoint(t *testing.T) {
	root := ".."
	var (
		files    []string
		offenses []string
	)
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // 目录缺失等情况跳过, 由分母守卫兜底
		}
		if info.IsDir() {
			if info.Name() == "node_modules" || info.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		files = append(files, p)
		raw, readErr := os.ReadFile(p)
		if readErr != nil {
			return readErr
		}
		for i, line := range strings.Split(string(raw), "\n") {
			code := line
			if idx := strings.Index(code, "//"); idx >= 0 {
				code = code[:idx]
			}
			if riskLevelConstDecl.MatchString(code) {
				offenses = append(offenses, fmt.Sprintf("%s:%d: %s", p, i+1, strings.TrimSpace(line)))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	// 分母守卫: 必须真的扫到源码, 否则门禁是空转。
	if len(files) < 50 {
		t.Fatalf("只扫到 %d 个非测试 .go 文件 (期望 >=50) —— 扫描路径错了, 门禁形同虚设", len(files))
	}
	// 判据口径 (为什么是"定义文件 = 1"而不是"常量行 = 1"):
	// 一个枚举集合本来就有 N 个常量 (normal/elevated), 行数天然 > 1, 把行数
	// 钉成 1 只会让门禁无法表达真实意图。防漂移的真实意图是"**枚举只有一处
	// 定义**": 定义**文件**必须恰好 1 个。在第二个文件里再写一个 RiskLevel
	// 常量 → 文件数变 2 → 门禁红 (这就是 A-7 的反向验证注入)。
	definingFiles := map[string]bool{}
	for _, offense := range offenses {
		// 文件名是路径 + ".go"; 取到 ".go" 为止, 避免把行号当成文件名的一部分。
		if idx := strings.Index(offense, ".go:"); idx >= 0 {
			definingFiles[offense[:idx+3]] = true
		}
	}
	if len(definingFiles) != 1 {
		t.Fatalf("RiskLevel 常量定义文件数 = %d, 期望恰好 1 (方案 §3.3.4 R1/A-7):\n%s",
			len(definingFiles), strings.Join(offenses, "\n"))
	}
	// 分母守卫 2: 定义点本身不得为空 —— 否则"零定义"也会让上面恒绿。
	if len(offenses) < 2 {
		t.Fatalf("RiskLevel 常量定义行 = %d, 期望至少 2 (normal+elevated) —— 判据被改坏, 门禁形同虚设", len(offenses))
	}
}

// readSourceFile 读本包内的生产源码 (门禁只在被检代码变更时才有意义)。
func readSourceFile(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", name, err)
	}
	if len(raw) == 0 {
		t.Fatalf("%s 为空 —— 门禁会因空文件假绿", name)
	}
	return string(raw)
}

// extractRegion 取出从 start 起、到第一个 end 为止的源码区块。
// 找不到标记即 t.Fatal —— 防止有人改写法让门禁静默失效 (vacuous pass)。
func extractRegion(t *testing.T, src, start, end string) string {
	t.Helper()
	from := strings.Index(src, start)
	if from < 0 {
		t.Fatalf("源码中找不到起始标记 %q —— 门禁无法扫描 (写法变更需同步更新门禁)", start)
	}
	rest := src[from:]
	to := strings.Index(rest, end)
	if to < 0 {
		t.Fatalf("源码中找不到结束标记 %q —— 门禁无法扫描", end)
	}
	return rest[:to+len(end)]
}

// ─── Definition 对外字段: 归一化在注册期完成, 不由调用方手工赋值 ──────────
func TestRegisterDerivesRiskLevelAndRequiresConfirmation(t *testing.T) {
	for _, c := range []struct {
		driver      string
		wantLevel   RiskLevel
		wantConfirm bool
	}{
		{"low", RiskNormal, false},
		{"medium", RiskElevated, true},
		{"high", RiskElevated, true},
		{"critical", RiskElevated, true},
	} {
		r := NewRegistry()
		def := Definition{ID: "act_" + c.driver, Version: 1, Name: "n", DeviceType: "test",
			Semantics: "read", Risk: c.driver, Enabled: true, Transport: ChannelCmdV2Adapter,
			SingleStep: SingleStep{TXData: []byte{1}, RXTimeoutMS: 1}}
		if err := r.Register(def); err != nil {
			t.Fatalf("注册 %s 风险动作失败: %v", c.driver, err)
		}
		got, ok := r.Get("test", def.ID)
		if !ok {
			t.Fatalf("%s 动作未进入目录", c.driver)
		}
		// 兼容期: 4 级 risk 原样保留 (方案 §3.4 D1)。
		if got.Risk != c.driver {
			t.Errorf("兼容期 risk 字段 = %q, want %q (4 级不得被改写)", got.Risk, c.driver)
		}
		if got.RiskLevel != c.wantLevel {
			t.Errorf("risk %q → risk_level %q, want %q", c.driver, got.RiskLevel, c.wantLevel)
		}
		if got.RequiresConfirmation != c.wantConfirm {
			t.Errorf("risk %q → requires_confirmation %v, want %v", c.driver, got.RequiresConfirmation, c.wantConfirm)
		}
		// 对外 JSON 契约: 两个新字段必须在 (前端据此删除本地字面量)。
		blob, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(blob, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded["risk_level"] != string(c.wantLevel) {
			t.Errorf("JSON risk_level = %v, want %q", decoded["risk_level"], c.wantLevel)
		}
		if decoded["requires_confirmation"] != c.wantConfirm {
			t.Errorf("JSON requires_confirmation = %v, want %v", decoded["requires_confirmation"], c.wantConfirm)
		}
	}
}

// 未声明的驱动字面量必须在注册期被拒 (fail-closed), 不得进入目录。
func TestRegisterRejectsUnknownDriverRisk(t *testing.T) {
	r := NewRegistry()
	def := Definition{ID: "act_weird", Version: 1, Name: "n", DeviceType: "test",
		Semantics: "read", Risk: "extreme", Enabled: true, Transport: ChannelCmdV2Adapter,
		SingleStep: SingleStep{TXData: []byte{1}, RXTimeoutMS: 1}}
	if err := r.Register(def); err == nil {
		t.Fatal("未知风险字面量必须在注册期被拒 (fail-closed), 实际被接受")
	}
}
