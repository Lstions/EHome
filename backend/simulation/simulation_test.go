//go:build simulation

// 场景仿真验证框架的测试入口（设计 §5.5）。
//
// 三个顶层测试的分工：
//   - TestMain：只做基础设施预检（PG / EMQX / 真实组合根可编译）并跑 m.Run()；
//   - TestScenarios：启动 harness（真实子进程 + 真实库）并顺序执行全部场景；
//   - TestCatalogGate：对**目录本身**做结构门禁，不需要任何基础设施。
//
// 为什么 harness 在 TestScenarios 里启动而不是 TestMain 里：harness.Start
// 的收尾（杀子进程 / DROP 场景库 / 写 summary）挂在 *testing.T 的 Cleanup 上，
// 而 TestMain 拿不到 *testing.T。用合成 T 会让 t.Fatalf → runtime.Goexit
// 落在主 goroutine 上（进程直接死掉且退出码为 0，失败被吞掉）。
// TestMain 因此只承担不需要 T 的预检，Start 放在 TestScenarios 里，
// 其 Cleanup 恰好在全部子测试结束后执行——语义与设计一致。
package simulation

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"ehome/backend/simulation/catalog"
	"ehome/backend/simulation/harness"
)

func TestMain(m *testing.M) {
	// 基础设施缺失时直接失败并给出可执行提示，不静默跳过（设计 §5.5）。
	if err := harness.Preflight(harness.DefaultOptions()); err != nil {
		fmt.Fprintf(os.Stderr, "\n场景仿真预检失败：%v\n"+
			"请先执行：make infra（PostgreSQL 5432 + EMQX 1883），并确认 Go 工具链在 PATH 中。\n\n", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// TestScenarios 顺序遍历 catalog.All() 执行全部场景（设计 §5.5）：
// 单进程单库、不开 t.Parallel，场景之间用自我清理保证互不污染。
func TestScenarios(t *testing.T) {
	env := harness.Start(t, harness.DefaultOptions())

	scenarios := catalog.All()
	if len(scenarios) == 0 {
		t.Fatalf("场景目录为空：catalog 包的 init() 未注册任何场景")
	}
	t.Logf("本次仿真运行 run_id=%s 库=%s 服务=%s 场景数=%d",
		env.RunID, env.DBName, env.BaseURL, len(scenarios))

	started := time.Now()
	for _, scenario := range scenarios {
		scenario := scenario
		t.Run(scenario.ID+" "+scenario.Title, func(t *testing.T) {
			env.T = t
			record := env.BeginScenario(scenario.ID, scenario.Title, string(scenario.Domain))
			defer func() {
				env.EndScenario(record, t.Failed())
			}()
			scenario.Run(env)
		})
	}
	t.Logf("全部 %d 个场景执行完毕，耗时 %s", len(scenarios), time.Since(started).Round(time.Millisecond))
}

// TestCatalogGate 是目录门禁（设计 §5.5）。它校验：
//
//  1. 场景总数 < 50；
//  2. 场景 ID 重复，或不符合 ^SIM-[A-Z]+-\d{3}$；
//  3. 场景 Title / Doc 为空，或 Run 为 nil（禁止占位场景）；
//  4. 域数量 < 10；
//  5. 任一域场景数 < 3；
//  6. 场景 ID 的域段与 Scenario.Domain 不一致；
//  7. Domain 不在冻结的 13 个取值之内；
//  8. 域文件的包级标识符未带本域前缀（设计 §4.1）。
//
// 第 8 条守护的是**可持续演进能力**：catalog 是一个 Go 包，13 个域文件
// 共享同一命名空间。没有这条门禁，两个域各自声明 simNodeStatus 这类
// 通用名只会在编译期暴露，且会随着并行开发反复发生。
//
// 本门禁在本期是**预期内失败**的：目录尚未达到 50 个场景的下限
// （设计 §6 明确"门禁下限 50，本目录设计 72，为个别不可行项留出余量"）。
// 本期验收以 TestScenarios 全绿为准。
func TestCatalogGate(t *testing.T) {
	scenarios := catalog.All()

	// --- 门禁 1：场景总数 ---
	if len(scenarios) < catalog.MinScenarios {
		t.Errorf("门禁1 场景总数 %d < 下限 %d（设计 §5.5；本目录设计 72 个，"+
			"未完成的场景必须在设计 §9 标注“未实现 + 原因”，不得静默删除）",
			len(scenarios), catalog.MinScenarios)
	}

	// --- 门禁 2：ID 形态与唯一性 ---
	idPattern := catalog.ScenarioIDPattern()
	seen := map[string]string{}
	for _, scenario := range scenarios {
		if !idPattern.MatchString(scenario.ID) {
			t.Errorf("门禁2 场景 ID %q 不符合 %s", scenario.ID, idPattern.String())
		}
		if previous, duplicate := seen[scenario.ID]; duplicate {
			t.Errorf("门禁2 场景 ID %q 重复（先出现于 %s）", scenario.ID, previous)
			continue
		}
		seen[scenario.ID] = scenario.Title
	}

	// --- 门禁 3：禁止占位场景 ---
	for _, scenario := range scenarios {
		label := scenario.ID
		if label == "" {
			label = "<空 ID>"
		}
		if strings.TrimSpace(scenario.Title) == "" {
			t.Errorf("门禁3 场景 %s 的 Title 为空", label)
		}
		if strings.TrimSpace(scenario.Doc) == "" {
			t.Errorf("门禁3 场景 %s 的 Doc 为空（必须写明设计依据）", label)
		}
		if scenario.Run == nil {
			t.Errorf("门禁3 场景 %s 的 Run 为 nil（禁止占位场景）", label)
		}
	}

	// --- 门禁 4/5：域数量与每域下限 ---
	byDomain := map[string][]string{}
	for _, scenario := range scenarios {
		byDomain[string(scenario.Domain)] = append(byDomain[string(scenario.Domain)], scenario.ID)
	}
	if len(byDomain) < catalog.MinDomains {
		t.Errorf("门禁4 域数量 %d < 下限 %d；实际域: %v",
			len(byDomain), catalog.MinDomains, sortedDomainKeys(byDomain))
	}
	for _, domain := range sortedDomainKeys(byDomain) {
		if len(byDomain[domain]) < catalog.MinPerDomain {
			t.Errorf("门禁5 域 %s 只有 %d 个场景（下限 %d）: %v",
				domain, len(byDomain[domain]), catalog.MinPerDomain, byDomain[domain])
		}
	}

	// --- 门禁 6/7：ID 域段与 Domain 的一致性、Domain 取值合法性 ---
	for _, scenario := range scenarios {
		domain := string(scenario.Domain)
		if !catalog.IsKnownDomain(scenario.Domain) {
			t.Errorf("门禁7 场景 %s 的 Domain %q 不在冻结的 13 个取值内（设计 §6 表）",
				scenario.ID, domain)
			continue
		}
		fromID := catalog.DomainOf(scenario.ID)
		if fromID != domain {
			t.Errorf("门禁6 场景 %s 的 ID 域段 %q 与 Domain %q 不一致"+
				"（不变量: ID == \"SIM-\" + string(Domain) + \"-\" + NNN）",
				scenario.ID, fromID, domain)
		}
	}

	// --- 门禁 8：域文件包级命名前缀 ---
	total := gatePackageNamespaces(t)
	if total > 0 {
		t.Errorf("门禁8 共 %d 个包级标识符未遵守“域文件必须带本域前缀”规则（设计 §4.1）", total)
	}

	t.Logf("目录门禁：场景 %d 个，域 %d 个，域分布 %v",
		len(scenarios), len(byDomain), domainCounts(byDomain))
}

// gateExemptFiles 是包级命名规则（门禁 8）的豁免文件。
//
// 理由：
//   - catalog.go 承载设计 §5.4 **冻结**的契约标识符（Scenario / Domain /
//     Register / All），它们按契约必须叫这个名字，不能加域前缀；该文件里
//     其余声明（跨域共用助手）统一使用 sim 前缀。
//   - *_test.go 不属于任何域（测试文件自带 Test 前缀约定）。
//
// 域文件（dep.go / auth.go / node.go / ...）不豁免：它们的包级标识符
// 必须以该域的小写短名为前缀。
var gateExemptFiles = map[string]bool{
	"catalog.go": true,
}

// gatePackageNamespaces 逐个域文件校验包级标识符前缀，返回违规总数。
func gatePackageNamespaces(t *testing.T) int {
	t.Helper()
	dir := gateCatalogDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("门禁8 无法读取场景目录 %s: %v", dir, err)
	}

	violations := 0
	files := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if gateExemptFiles[name] {
			continue
		}
		prefix := strings.TrimSuffix(name, ".go")
		if prefix == "" {
			continue
		}
		files++

		path := filepath.Join(dir, name)
		parsed, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			t.Errorf("门禁8 无法解析 %s: %v", name, parseErr)
			continue
		}
		for _, declared := range packageLevelNames(parsed) {
			if declared == "init" {
				continue // init 是 Go 语言固定入口，不是域标识符。
			}
			if !strings.HasPrefix(declared, prefix) {
				t.Errorf("门禁8 %s:%s 应以 %q 为前缀（设计 §4.1 包级命名规则）", name, declared, prefix)
				violations++
			}
		}
	}
	if files == 0 {
		t.Errorf("门禁8 在 %s 下未找到任何域源文件，门禁形同虚设", dir)
	}
	return violations
}

// packageLevelNames 收集一个源文件里全部包级声明的名字。
func packageLevelNames(file *ast.File) []string {
	var names []string
	for _, decl := range file.Decls {
		switch typed := decl.(type) {
		case *ast.FuncDecl:
			if typed.Recv == nil { // 方法名不属于包级命名空间。
				names = append(names, typed.Name.Name)
			}
		case *ast.GenDecl:
			for _, spec := range typed.Specs {
				switch item := spec.(type) {
				case *ast.TypeSpec:
					names = append(names, item.Name.Name)
				case *ast.ValueSpec:
					for _, ident := range item.Names {
						names = append(names, ident.Name)
					}
				}
			}
		}
	}
	return names
}

// gateCatalogDir 定位场景目录。go test 的工作目录是测试文件所在目录
// （backend/simulation），因此 catalog 子目录是首选；保留第二个候选
// 以便包被移动时门禁仍然可用而不是直接失败。
func gateCatalogDir(t *testing.T) string {
	t.Helper()
	candidates := []string{
		filepath.Join("catalog"),
		filepath.Join("simulation", "catalog"),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}
	t.Fatalf("门禁8 找不到场景目录，尝试过: %v", candidates)
	return ""
}

func sortedDomainKeys(byDomain map[string][]string) []string {
	keys := make([]string, 0, len(byDomain))
	for key := range byDomain {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func domainCounts(byDomain map[string][]string) map[string]int {
	counts := make(map[string]int, len(byDomain))
	for key, value := range byDomain {
		counts[key] = len(value)
	}
	return counts
}
