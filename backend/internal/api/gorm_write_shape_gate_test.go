package api

// GORM 写路径形态门禁（回归锁）。
//
// ===== 为什么需要它 =====
// 本仓已**五次**踩到「GORM 把显式零值静默替换掉」这一类缺陷（汇总见
// `internal/models/defaults_gate_test.go` 的文件头）。其中**三次**发生在**写路径**：
//   · `AlertRule.Enabled` 的 `default:true` ⇒ 用户创建「暂不启用」的告警规则实际立刻生效；
//   · `AutomationRule.CooldownSec` 的 `default:300` ⇒ `cooldown_sec: 0`（不冷却）无法表达；
//   · `Node.LogStreamLevel` 的 `default:2` ⇒ `Create(level:0)` 回读为 2（INFO）。
//
// 这三个的**共同规避手段**是：写路径改用 `Updates(map[string]interface{})`。
// GORM 对 map 形态**不做零值省略**（map 里的键是显式的「我要改这个字段」），
// 因此 `{"log_stream_level": 0}` 能忠实落库；而 `Updates(struct)` 会跳过零值字段。
//
// **本门禁把这条纪律固定下来**：新增写路径若用了非 map 形态，必须显式说明为什么安全。
// 否则「有人图省事写成 `Updates(rule)`」会让零值陷阱**重新长回来**，而且测试未必立刻发现。
//
// ===== 本门禁实际核对的形态（2026-09-16 全量实测）=====
//   · `Updates(...)` 共 **66** 处：**全部**是 `map[string]interface{}` 形态（39 处直接字面量 +
//     27 处经 `updates` 变量，逐文件核对过变量声明）；
//   · `.Save(...)` 共 **13** 处：全部是「先读后写」——对**已加载的完整结构体**保存，
//     不存在「构造局部结构体再 Save」这种会把未赋值字段写成零值的形态。
//
// ===== 本门禁查不了什么（诚实声明）=======================================
//   · **查不了「先读后写」是否真的读全**：若某处 `Save` 之前只 Select 了部分列，
//     未选的列仍是零值，本门禁看不出来（那需要逐处读代码判断语义）；
//   · **查不了 map 里的键是否写对**（例如把 `enabled` 写成 `Enabled`）——那要行为测试；
//   · 只覆盖 `internal/` 与 `cmd/` 下的非测试 `.go`；
//   · 它是**形态**门禁，不是**语义**门禁：允许登记例外，但例外必须写明理由。
//
// ===== 变红条件 =========================================================
//   · 出现 `Updates(<非 map 变量>)`（例如把结构体直接传进去）；
//   · 出现 `Save(&<未加载的结构体字面量>)` 这类局部构造再保存的形态。

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// updatesArgPattern 捕获 `Updates(<第一个参数>)` 的实参文本。
//
// **不要求前导点**：GORM 的链式调用经常折行，`Updates(...)` 会出现在行首，
// 例如 `internal/automation/planner.go` 的 `\n\t\tUpdates(map[string]interface{}{`。
// 本门禁初版写成 `\.Updates\(`，只数到 **36** 处而实际有 **66** 处 ——
// 分母守卫（阈值 40）当场把它抓了出来。这是「正则比目标字符串更严格」的又一实例
// （与 docs 计划 §1.7 (16) 的自我更正是同一族）。
var updatesArgPattern = regexp.MustCompile(`\bUpdates\(([^)\n]*)`)

// saveArgPattern 捕获 `Save(<实参>)`；同样不要求前导点（同理）。
var saveArgPattern = regexp.MustCompile(`\bSave\(([^)\n]*)`)

// allowedUpdatesArg 判定 Updates 的实参是否安全。
//
// **判据是「类型」而不是「名字」**：初版用一张变量名前缀白名单
// （updates / write.updates / toMap / fields / patch），结果把 `commandexec` 里
// 两个**确实是 map** 的 `attemptUpdates` 误判为违规 —— 按名字判断天然漏掉
// 「名字没猜到但类型正确」的写法。现改为：map 字面量直接放行；变量形态查
// 调用方传入的**已声明 map 变量名集合**（由 declaredMapVarsIn 从源码解析得到）。
func allowedUpdatesArg(arg string, mapVars map[string]bool) bool {
	a := strings.TrimSpace(arg)
	if strings.HasPrefix(a, "map[string]interface{}") || strings.HasPrefix(a, "map[string]any") {
		return true
	}
	// 逐段匹配：本仓两种写法都存在 ——
	//   `Updates(updates)`          → 变量名（首段）
	//   `Updates(write.updates)`    → 结构体字段名（末段）
	//   `Updates(attemptUpdates)`   → 变量名
	// 故先剥掉调用/索引尾巴，再按 '.' 拆分，**任一段**命中已声明的 map 名即视为安全。
	base := a
	if i := strings.IndexAny(base, "(["); i >= 0 {
		base = base[:i]
	}
	for _, part := range strings.Split(base, ".") {
		if part != "" && mapVars[part] {
			return true
		}
	}
	return false
}

// declaredMapVarsIn 从一份源码里解析「被声明为 map[string]… 的变量名」。
//
// 支持 `x := map[string]interface{}{` 与 `var x = map[string]any{` 两种形态。
// 保守解析：认不出就不放行（宁可多报让人裁决，不静默放过）。
func declaredMapVarsIn(src string) map[string]bool {
	out := map[string]bool{}
	for _, raw := range strings.Split(src, "\n") {
		line := raw
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "var ") {
			trimmed = strings.TrimSpace(trimmed[len("var "):])
		}
		for _, sep := range []string{":=", "="} {
			i := strings.Index(trimmed, sep)
			if i <= 0 {
				continue
			}
			name := strings.TrimSpace(trimmed[:i])
			rest := strings.TrimSpace(trimmed[i+len(sep):])
			if name != "" && !strings.ContainsAny(name, " .[(") &&
				(strings.HasPrefix(rest, "map[string]interface{}") || strings.HasPrefix(rest, "map[string]any")) {
				out[name] = true
			}
			break
		}
		// 结构体字段形态：`updates map[string]interface{}`（无 '=' 号）。
		// 本仓 `handler_node.go` 的 `edgeWrite.updates` 就是这种 —— 它是匿名字面量类型
		// 的字段，被 `Updates(write.updates)` 使用，必须一并识别，否则会误报。
		if fields := strings.Fields(trimmed); len(fields) == 2 &&
			!strings.ContainsAny(fields[0], ".[(") && fields[0] != "" &&
			(strings.HasPrefix(fields[1], "map[string]interface{}") || strings.HasPrefix(fields[1], "map[string]any")) {
			out[fields[0]] = true
		}
	}
	return out
}

// gormWriteOffencesIn 抽取一份源码里的写路径形态违规点（去注释后）。
// 导出以便**分类器自检**（同一函数喂正例/反例），这是本仓既有范式。
func gormWriteOffencesIn(src string) []string {
	var out []string
	mapVars := declaredMapVarsIn(src)
	for _, raw := range strings.Split(src, "\n") {
		line := raw
		// 遮蔽注释：本仓注释里大量引用 `Updates(...)` / `Save(...)` 作为说明
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		if m := updatesArgPattern.FindStringSubmatch(line); m != nil {
			if !allowedUpdatesArg(m[1], mapVars) {
				out = append(out, "Updates("+strings.TrimSpace(m[1])+") 不是 map 形态")
			}
		}
		if m := saveArgPattern.FindStringSubmatch(line); m != nil {
			arg := strings.TrimSpace(m[1])
			// 危险形态：Save(&Type{...}) —— 局部构造的结构体字面量
			if strings.HasPrefix(arg, "&") && strings.Contains(arg, "{") {
				out = append(out, "Save("+arg+") 保存了局部构造的结构体（未加载完整字段）")
			}
		}
	}
	return out
}

// TestGormWritePathShapes 扫描 internal/ 与 cmd/，断言写路径形态合规。
func TestGormWritePathShapes(t *testing.T) {
	var files []string
	for _, root := range []string{"..", "../../cmd"} {
		err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // 目录不存在等情况跳过，由分母守卫兜底
			}
			if info.IsDir() {
				if info.Name() == "node_modules" || info.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}

	// 分母守卫：必须真的扫到源码，否则门禁是空转
	if len(files) < 100 {
		t.Fatalf("只扫到 %d 个非测试 .go 文件（期望 >=100）—— 路径错了，门禁形同虚设", len(files))
	}

	var offenses []string
	updatesSeen, saveSeen := 0, 0
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			code := line
			if i := strings.Index(code, "//"); i >= 0 {
				code = code[:i]
			}
			if updatesArgPattern.MatchString(code) {
				updatesSeen++
			}
			if saveArgPattern.MatchString(code) {
				saveSeen++
			}
		}
		for _, off := range gormWriteOffencesIn(string(raw)) {
			offenses = append(offenses, f+": "+off)
		}
	}

	// 分母守卫 2：写路径的**总量**也要有下限，避免「扫描器没扫到」。
	// 阈值取实测值的约 2/3（2026-09-16 实测 Updates 66 / Save 13），
	// 这样正常的增删不会误报，而「正则失效导致归零」会被抓住。
	if updatesSeen < 40 {
		t.Fatalf("只扫到 %d 处 Updates(（期望 >=40）—— 判据失效", updatesSeen)
	}
	if saveSeen < 8 {
		t.Fatalf("只扫到 %d 处 Save(（期望 >=8）—— 判据失效", saveSeen)
	}

	sort.Strings(offenses)
	if len(offenses) > 0 {
		t.Errorf("GORM 写路径形态违规（零值会被静默丢弃，见本文件头）：\n  %s",
			strings.Join(offenses, "\n  "))
	}
}

// TestGormWritePathClassifierSelfCheck 是分类器自检：同一纯函数喂正例/反例。
func TestGormWritePathClassifierSelfCheck(t *testing.T) {
	positive := []string{
		`db.Model(&rule).Updates(rule)`,    // 结构体形态：危险
		`tx.Model(&d).Updates(dto)`,        // 变量但非 map：危险
		`db.Save(&models.Node{Name: "x"})`, // 局部构造再保存：危险
	}
	for _, p := range positive {
		if got := gormWriteOffencesIn(p); len(got) == 0 {
			t.Errorf("正例必报，实际未报: %s", p)
		}
	}

	// 反例要**自带声明**：本门禁的判据是「实参在源码里被声明为 map」，
	// 因此裸写 `Updates(updates)` 而没有 `updates := map[...]` 会被正确判为违规 ——
	// 把反例写成孤立的单行，测的是「我猜的名字」而不是「类型」，那正是初版的缺陷。
	negative := []struct {
		name string
		src  string
	}{
		{"map 字面量", `db.Model(&rule).Updates(map[string]interface{}{"enabled": false})`},
		{"局部 map 变量", "updates := map[string]interface{}{}\n" + `db.Model(&rule).Updates(updates)`},
		// 多行结构体（与真实代码 handler_node.go 的 edgeWrite 同形）：字段独占一行。
		{"结构体 map 字段", "type W struct {\n\tupdates map[string]interface{}\n}\n" + `db.Model(&w).Updates(w.updates)`},
		{"var 形态", "var patch = map[string]any{}\n" + `db.Model(&rule).Updates(patch)`},
		{"先读后写 Save", "var update models.AlertRule\n" + `tx.Save(&update)`},
		{"注释里的不算", `// 注释里的 db.Model(&x).Updates(x) 不算`},
	}
	for _, n := range negative {
		if got := gormWriteOffencesIn(n.src); len(got) != 0 {
			t.Errorf("反例必不报（%s），实际报出 %v: %s", n.name, got, n.src)
		}
	}
}
