package api

// 分页方言门禁（全站源码扫描）：**后端不允许再出现 `"list":` 作为分页响应键**。
//
// 背景（2026-09-15 实测）：本仓分页端点的 data 段已 11:1 分裂 —— 12 个分页端点里
// 11 个用 `{items,total,page,page_size}`，只有 `GET /device-configs` 用 `{list,...}`。
// 前端代码里自己写了这件事（api/automation.ts:164「待收敛的旧方言」），但**没有任何守卫**，
// 于是「声称会收敛」与「实际没收敛」并存了很久。
//
// 为什么既有测试抓不住它：`handler_device_crud_test.go` 的三个 List 用例**只断言 `total`**，
// 从不断言「数组挂在哪个键上」⇒ 键名换了、测试照绿。这正是本门禁存在的理由：
// **逐端点的行为测试覆盖不到「全站键名一致性」这类横向不变量。**
//
// ── 判据（fail-closed）──────────────────────────────────────────────────────
// 扫描 `internal/api/` 下所有**非测试** .go 文件，找出所有分页响应构造点，
// 断言其中不存在 `"list":`。认不出的写法一律报出来让人裁决，不静默放过。
//
// ── 本门禁查不了什么（能力边界）──────────────────────────────────────────────
// 1. **查不了运行时形状**：它证明「源码里没写 list」，不证明「响应里真的没有 list」
//    （例如键名由变量拼出）。运行时形状由 handler_device_dialect_test.go 与
//    simulation/catalog/err.go 的 SIM-ERR-001 序号端点抽样覆盖。
// 2. **查不了未被 scan 命中的新写法**：若将来出现第三种分页封装（不是 gin.H 字面量），
//    本门禁的 pattern 认不出 ⇒ 由下面的「分母守卫」在分页构造点变少时报警。
// 3. 它**不**判断非分页端点的 `list`（例如某个业务字段真的叫 list）是否有问题 ——
//    判据是「与分页信封同时出现」，见 paginatedConstructs。
//
// ── 变红条件 ────────────────────────────────────────────────────────────────
//   · 任何端点改回 / 新增 `"list":` 作为分页键；
//   · 分页构造点数量骤降（正则被改坏）⇒ 分母守卫报警。

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// legacyDialectPattern 匹配分页响应里的旧方言键。
// 刻意只认**双引号包起来的 `list` 后跟冒号**：
//
//	· 不认 `list:`（结构体字段/局部变量赋值，与信封键无关）；
//	· 不认注释（本文件与调用点都有大量解释性文字提到 "list"）。
var legacyDialectPattern = regexp.MustCompile(`"list"\s*:`)

// paginationShapePattern 匹配**分页信封**的构造点。
// 口径：同时出现 total 与 page_size 的 gin.H 字面量 ⇒ 是分页响应。
// （只带 total 的列表端点，如 data-sources，不算分页信封。）
var paginationShapePattern = regexp.MustCompile(`"total"\s*:`)

// stripGoComments 去掉行注释与块注释（保留换行，便于报行号）。
// 必须遮蔽注释：本仓注释里大量引用 `"list":` 作为反例说明。
func stripGoComments(src string) string {
	var out strings.Builder
	inBlock := false
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case inBlock:
			if idx := strings.Index(line, "*/"); idx >= 0 {
				inBlock = false
				out.WriteString(strings.Repeat(" ", idx+2) + line[idx+2:])
			} else {
				out.WriteString(strings.Repeat(" ", len(line)))
			}
		case strings.HasPrefix(trimmed, "/*"):
			// 单行块注释 `/* ... */` 必须**整行**遮蔽：
			// 只判 prefix 会把 `/* "list": 不算 */` 当成代码（本函数最初就是这么错的，
			// 被下面的分类器自检抓出来）。判据：本行内存在收尾标记 ⇒ 注释在本行结束。
			if idx := strings.Index(trimmed, "*/"); idx > 2 {
				out.WriteString(strings.Repeat(" ", len(line)))
			} else {
				inBlock = true
				out.WriteString(strings.Repeat(" ", len(line)))
			}
		case strings.HasPrefix(trimmed, "//"):
			out.WriteString(strings.Repeat(" ", len(line)))
		default:
			// 行尾注释：只截掉 `//` 之后（保守：字符串里的 // 极少见，宁可少截不漏判）
			if idx := strings.Index(line, "//"); idx >= 0 {
				line = line[:idx]
			}
			out.WriteString(line)
		}
		out.WriteString("\n")
	}
	return out.String()
}

// dialectViolation 是一处旧方言违规点。
type dialectViolation struct {
	line int
	text string
}

// legacyDialectSitesIn 抽取一份源码里全部的旧方言违规点（去注释后）。
// 导出以便**分类器自检**（同一函数喂正例/反例），这是本仓既有范式。
func legacyDialectSitesIn(src string) []dialectViolation {
	stripped := stripGoComments(src)
	var out []dialectViolation
	for i, line := range strings.Split(stripped, "\n") {
		if legacyDialectPattern.MatchString(line) {
			out = append(out, dialectViolation{line: i + 1, text: strings.TrimSpace(line)})
		}
	}
	return out
}

// paginatedConstructsIn 数一份源码里的分页信封构造点（去注释后）。
func paginatedConstructsIn(src string) int {
	stripped := stripGoComments(src)
	n := 0
	for _, line := range strings.Split(stripped, "\n") {
		if paginationShapePattern.MatchString(line) && strings.Contains(line, "gin.H{") {
			n++
		}
	}
	return n
}

// apiSourceFiles 返回 internal/api 下所有非测试 .go 文件（相对仓库根的路径）。
func apiSourceFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read internal/api: %v", err)
	}
	var files []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		files = append(files, filepath.Join(".", name))
	}
	sort.Strings(files)
	return files
}

// TestPaginationDialectGate_NoLegacyListKey 是全站方言门禁本体。
func TestPaginationDialectGate_NoLegacyListKey(t *testing.T) {
	files := apiSourceFiles(t)
	if len(files) < 20 {
		t.Fatalf("扫到的非测试 .go 文件只有 %d 个 —— 扫描路径错了，门禁形同虚设", len(files))
	}

	var violations []string
	paginated := 0
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		src := string(raw)
		paginated += paginatedConstructsIn(src)
		for _, v := range legacyDialectSitesIn(src) {
			violations = append(violations, f+":"+itoa(v.line)+"  "+v.text)
		}
	}

	// 分母守卫：分页构造点必须真的扫到。若正则被改坏成恒不命中，
	// 下面这条会变红 —— 避免「0 违规」其实来自「扫描器空转」的假绿。
	if paginated < 10 {
		t.Fatalf("只扫到 %d 个分页信封构造点（期望 >=10）—— 判据/正则被改坏，门禁形同虚设", paginated)
	}

	if len(violations) > 0 {
		t.Fatalf("分页响应仍在使用旧方言键 `\"list\":`（全仓契约是 `items`；"+
			"见 handler_pagination_contract_test.go 与 api/automation.ts 的说明）：\n  %s",
			strings.Join(violations, "\n  "))
	}
}

// TestPaginationDialectGate_ClassifierSelfCheck 是分类器自检。
// 同一纯函数喂正例/反例 —— 保证它真的能区分，而不是恒真/恒假。
func TestPaginationDialectGate_ClassifierSelfCheck(t *testing.T) {
	positive := []string{
		`Success(c, gin.H{"list": items, "total": total, "page": page, "page_size": pageSize})`,
		`c.JSON(200, gin.H{"list": []models.X{}, "total": 0})`,
	}
	for _, p := range positive {
		if got := legacyDialectSitesIn(p); len(got) != 1 {
			t.Errorf("正例必报，实际 %d 处: %s", len(got), p)
		}
	}

	negative := []string{
		`Success(c, gin.H{"items": items, "total": total, "page": page, "page_size": pageSize})`,
		`var list []models.X`,
		`list = append(list, x)`,
		`// 这里解释为什么不再用 "list": 作为键`,
		`/* 块注释里的 "list": 不算 */`,
		`Success(c, gin.H{"items": vendors, "total": total})`,
	}
	for _, n := range negative {
		if got := legacyDialectSitesIn(n); len(got) != 0 {
			t.Errorf("反例必不报，实际报出 %d 处: %s", len(got), n)
		}
	}

	// 分页构造点的计数判据也必须能区分
	if paginatedConstructsIn(`Success(c, gin.H{"items": items, "total": total, "page": page, "page_size": pageSize})`) != 1 {
		t.Error("分页构造点判据失效：应命中 1 处 gin.H 分页信封")
	}
	if paginatedConstructsIn(`db.Model(&X{}).Count(&total)`) != 0 {
		t.Error("分页构造点判据过宽：纯 Count 不应被当作分页信封构造点")
	}
}
