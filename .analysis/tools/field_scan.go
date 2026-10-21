// field_scan 是 D1 验收门禁 #4/#5 的 AST 扫描器（只读；仅 stdlib go/ast）。
//
// 口径（方案 §12.3 门禁 4/5，不写死分母，由脚本从代码推导）：
//   - 声明并集：所有 GetSensorDefinitions() 方法体里的 {Name: "..."} 字面量
//   - 产出字面量：任意 []SensorData{{Name: ...}} / SensorData{Name: ...} 复合字面量
//   - 动态族：fmt.Sprintf 格式串里含 "%d" 的前缀（如 cell_voltage_%d）
//
// 用法（worktree 根目录）：
//
//	go run .analysis/tools/field_scan.go backend/internal/drivers
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	physicalName   = regexp.MustCompile("^[a-z][a-z0-9_]*$")
	sprintfFamily  = regexp.MustCompile("^([a-z][a-z0-9_]*_)%d$")
)

func unquote(lit string) string {
	if s, err := strconv.Unquote(lit); err == nil {
		return s
	}
	return strings.Trim(lit, "\"")
}

func add(m map[string][]string, name, file string) {
	for _, f := range m[name] {
		if f == file {
			return
		}
	}
	m[name] = append(m[name], file)
}

func scanFields(lit *ast.CompositeLit, file string, out map[string][]string) {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "Name" {
			continue
		}
		val, ok := kv.Value.(*ast.BasicLit)
		if !ok || val.Kind != token.STRING {
			continue
		}
		add(out, unquote(val.Value), file)
	}
}

func main() {
	dir := "internal/drivers"
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	fset := token.NewFileSet()
	declared := map[string][]string{}
	outputs := map[string][]string{}
	families := map[string][]string{}

	var fileCount int
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fileCount++
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.FuncDecl:
				if node.Name != nil && node.Name.Name == "GetSensorDefinitions" && node.Body != nil {
					ast.Inspect(node.Body, func(m ast.Node) bool {
						kv, ok := m.(*ast.KeyValueExpr)
						if !ok {
							return true
						}
						if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "Name" {
							if lit, ok := kv.Value.(*ast.BasicLit); ok && lit.Kind == token.STRING {
								add(declared, unquote(lit.Value), path)
							}
						}
						return true
					})
				}
			case *ast.CompositeLit:
				switch typ := node.Type.(type) {
				case *ast.Ident:
					if typ.Name == "SensorData" {
						scanFields(node, path, outputs)
					}
				case *ast.ArrayType:
					if id, ok := typ.Elt.(*ast.Ident); ok && id.Name == "SensorData" {
						for _, elt := range node.Elts {
							if lit, ok := elt.(*ast.CompositeLit); ok {
								scanFields(lit, path, outputs)
							}
						}
					}
				}
			case *ast.CallExpr:
				sel, ok := node.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel == nil || sel.Sel.Name != "Sprintf" || len(node.Args) == 0 {
					return true
				}
				lit, ok := node.Args[0].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				format := unquote(lit.Value)
				if !strings.Contains(format, "%d") {
					return true
				}
				if m := sprintfFamily.FindStringSubmatch(format); m != nil {
					add(families, m[1], path)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		fmt.Println("SCAN_ERROR:", err)
		os.Exit(1)
	}

	declaredNames := sortedKeys(declared)
	outputNames := sortedKeys(outputs)
	familyPrefixes := sortedKeys(families)

	// 可达集合 = 产出字面量 ∪ 动态族（族成员视为可达）。
	reachable := func(name string) bool {
		if _, ok := outputs[name]; ok {
			return true
		}
		for _, prefix := range familyPrefixes {
			if strings.HasPrefix(name, prefix) {
				suffix := name[len(prefix):]
				if suffix != "" && isAllDigits(suffix) {
					return true
				}
			}
		}
		return false
	}

	var unreachable, undeclared, invalidOutputs, invalidDecls, ackExceptions []string
	for _, name := range declaredNames {
		if !reachable(name) {
			unreachable = append(unreachable, name)
		}
		if !physicalName.MatchString(name) {
			invalidDecls = append(invalidDecls, name)
		}
	}
	for _, name := range outputNames {
		if _, ok := declared[name]; !ok {
			undeclared = append(undeclared, name)
		}
		if !physicalName.MatchString(name) {
			invalidOutputs = append(invalidOutputs, name)
		}
		if strings.HasSuffix(name, "_ack") {
			ackExceptions = append(ackExceptions, name)
		}
	}

	fmt.Printf("=== SCOPE ===\n")
	fmt.Printf("dir=%s go_files=%d\n", dir, fileCount)
	fmt.Printf("=== COUNTS (分母) ===\n")
	fmt.Printf("declared_union=%d output_literal_union=%d dynamic_family_patterns=%d\n", len(declaredNames), len(outputNames), len(familyPrefixes))
	fmt.Printf("declared_and_output=%d outputs_not_declared=%d\n", len(declaredNames)-len(unreachable), len(undeclared))
	fmt.Printf("=== GATE #4 声明 ⊆ 产出可达（字面量 ∪ Sprintf 族）===\n")
	fmt.Printf("declared_unreachable=%d %v\n", len(unreachable), unreachable)
	fmt.Printf("=== 产出未声明（信息项，不是门禁失败）===\n")
	fmt.Printf("undeclared_outputs=%d %v\n", len(undeclared), undeclared)
	fmt.Printf("=== GATE #5 产出可寻址（物理名文法 + *_ack 例外）===\n")
	fmt.Printf("declaration_invalid_grammar=%d %v\n", len(invalidDecls), invalidDecls)
	fmt.Printf("output_invalid_grammar=%d %v\n", len(invalidOutputs), invalidOutputs)
	fmt.Printf("ack_pseudo_quantities=%d %v\n", len(ackExceptions), ackExceptions)
	fmt.Printf("=== DYNAMIC FAMILIES (Sprintf %%d) ===\n")
	for _, prefix := range familyPrefixes {
		fmt.Printf("family=%s* files=%v\n", prefix, families[prefix])
	}
	fmt.Printf("=== GATE #4/#5 VERDICT: %s ===\n", map[bool]string{true: "PASS", false: "FAIL"}[len(unreachable) == 0 && len(invalidDecls) == 0 && len(invalidOutputs) == 0])
}

func sortedKeys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func isAllDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}
