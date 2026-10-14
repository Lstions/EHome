package models

// 结果码白名单的防漂移门禁: models 冻结的 AutomationResult* 常量集合
// 必须与仿真侧 backend/simulation/catalog/audit.go 的 auditIsDefinedResult
// 白名单**逐项一致** (B1 评审 P2-4 记的债: 白名单漏同步会让仿真把合法结果码
// 判成"未定义"而误红)。
//
// 为什么是源码级双向比较而不是"读一遍常量列表": 单向比较挡不住"白名单里留了
// 一个已删除的旧码"这种反向漂移; 双向比较把两侧逼成同一个集合。
//
// 反向验证: 从 audit.go 的白名单里删掉 "partial_failure" → 本门禁红;
// 新增一个 AutomationResult* 常量而不同步白名单 → 本门禁红。

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// resultConstDecl 只认**常量声明**形态: AutomationResultXxx = "literal"。
var resultConstDecl = regexp.MustCompile(`AutomationResult[A-Za-z]+[ \t]*=[ \t]*"([a-z_]+)"`)

// quotedLiteral 抓源码里的双引号字符串字面量。
var quotedLiteral = regexp.MustCompile("\\\"([a-z_]+)\\\"")

func TestAutomationResultWhitelistMatchesModelConstants(t *testing.T) {
	modelSrc := readModelsSource(t, "automation.go")
	declared := map[string]bool{}
	for _, m := range resultConstDecl.FindAllStringSubmatch(modelSrc, -1) {
		declared[m[1]] = true
	}
	// 分母守卫: 常量集合必须非空, 否则门禁空转。
	if len(declared) < 9 {
		t.Fatalf("从 automation.go 只提取到 %d 个结果码常量 (期望 >=9) —— 提取逻辑失效", len(declared))
	}

	// 路径相对本包目录: internal/models → ../../simulation/catalog/audit.go。
	auditSrc := readModelsSource(t, "../../simulation/catalog/audit.go")
	start := strings.Index(auditSrc, "func auditIsDefinedResult(")
	if start < 0 {
		t.Fatal("audit.go 找不到 auditIsDefinedResult —— 门禁无法扫描")
	}
	body := auditSrc[start:]
	if end := strings.Index(body, "\n}"); end >= 0 {
		body = body[:end]
	}
	whitelist := map[string]bool{}
	for _, m := range quotedLiteral.FindAllStringSubmatch(body, -1) {
		whitelist[m[1]] = true
	}
	// 分母守卫: 白名单也必须非空。
	if len(whitelist) < 9 {
		t.Fatalf("从 auditIsDefinedResult 只提取到 %d 个结果码 (期望 >=9) —— 提取逻辑失效", len(whitelist))
	}

	var missing, extra []string
	for code := range declared {
		if !whitelist[code] {
			missing = append(missing, code)
		}
	}
	for code := range whitelist {
		if !declared[code] {
			extra = append(extra, code)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Errorf("仿真白名单 auditIsDefinedResult 缺少模型已冻结的结果码: %v"+
			" (漏同步会让仿真把合法结果码判成未定义而误红)", missing)
	}
	if len(extra) > 0 {
		t.Errorf("仿真白名单含模型未声明的结果码: %v (旧码残留或写错字面量)", extra)
	}
}

// readModelsSource 读本包或其上级目录的相对源码文件。
func readModelsSource(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(rel)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", rel, err)
	}
	if len(raw) == 0 {
		t.Fatalf("%s 为空 —— 门禁会因空文件假绿", rel)
	}
	return string(raw)
}
