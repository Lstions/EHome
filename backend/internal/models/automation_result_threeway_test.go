package models

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

// resultUnionLiteral 抓 TS union 里的单引号字面量（'xxx'）。
var resultUnionLiteral = regexp.MustCompile("'([a-z_]+)'")

// TestAutomationResultCodesThreeWayConsistency 是结果码三向一致性门禁（方案 N-18 / SG-16）。
//
// 为什么需要三向：既有 TestAutomationResultWhitelistMatchesModelConstants 只比较
// models ↔ simulation 两侧。前端 AutomationEventResult union 是**第三份副本**，
// 它漂移的后果是：后端写出 source_stale，前端 TS 类型没有该字面量 ⇒ 运行期渲染
// 走到默认分支（显示原始串或空白），而没有任何测试会红。
//
// 本门禁把三处逼成同一集合：
//  1. internal/models/automation.go 的 AutomationResult* = "literal"
//  2. simulation/catalog/audit.go 的 auditIsDefinedResult 白名单
//  3. frontend-shared/src/api/automation.ts 的 AutomationEventResult union
//
// 反向验证（变异自证）：从任一处的 source_* 删掉一个码 ⇒ 本门禁红。
func TestAutomationResultCodesThreeWayConsistency(t *testing.T) {
	backend := readModelsSource(t, "automation.go")
	declared := map[string]bool{}
	for _, m := range resultConstDecl.FindAllStringSubmatch(backend, -1) {
		declared[m[1]] = true
	}
	if len(declared) < 9 {
		t.Fatalf("从 automation.go 只提取到 %d 个结果码（期望 >=9）—— 提取逻辑失效", len(declared))
	}

	simSrc := readModelsSource(t, "../../simulation/catalog/audit.go")
	start := strings.Index(simSrc, "func auditIsDefinedResult(")
	if start < 0 {
		t.Fatal("audit.go 找不到 auditIsDefinedResult —— 门禁无法扫描")
	}
	body := simSrc[start:]
	if end := strings.Index(body, "\n}"); end >= 0 {
		body = body[:end]
	}
	simWhitelist := map[string]bool{}
	for _, m := range quotedLiteral.FindAllStringSubmatch(body, -1) {
		simWhitelist[m[1]] = true
	}
	if len(simWhitelist) < 9 {
		t.Fatalf("从 auditIsDefinedResult 只提取到 %d 个结果码 —— 提取逻辑失效", len(simWhitelist))
	}

	// 路径：internal/models → backend/ → 仓库根 → frontend-shared/（三层 ..）
	frontSrc := readModelsSource(t, "../../../frontend-shared/src/api/automation.ts")
	fs := strings.Index(frontSrc, "export type AutomationEventResult")
	if fs < 0 {
		t.Fatal("frontend automation.ts 找不到 AutomationEventResult —— 路径或类型名变了")
	}
	block := frontSrc[fs:]
	if end := strings.Index(block, "\n\n"); end >= 0 {
		block = block[:end]
	}
	frontUnion := map[string]bool{}
	for _, m := range resultUnionLiteral.FindAllStringSubmatch(block, -1) {
		frontUnion[m[1]] = true
	}
	if len(frontUnion) < 9 {
		t.Fatalf("从前端 union 只提取到 %d 个结果码 —— 提取逻辑失效", len(frontUnion))
	}

	report := func(side string, have map[string]bool) {
		var missing, extra []string
		for code := range declared {
			if !have[code] {
				missing = append(missing, code)
			}
		}
		for code := range have {
			if !declared[code] {
				extra = append(extra, code)
			}
		}
		sort.Strings(missing)
		sort.Strings(extra)
		if len(missing) > 0 {
			t.Errorf("%s 缺少后端已定义的 result 码（前端/仿真会走到默认分支或把合法码判成未定义）: %v", side, missing)
		}
		if len(extra) > 0 {
			t.Errorf("%s 存在后端未定义的 result 码（漂移/历史残留）: %v", side, extra)
		}
	}
	report("simulation/catalog/audit.go", simWhitelist)
	report("frontend AutomationEventResult", frontUnion)
}
