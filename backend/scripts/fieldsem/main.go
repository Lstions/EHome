// Command fieldsem 从 drivers 源码推导「声明集」与「产出可达集」，供门禁 #4/#5 使用。
//
// 为什么需要它（而不是 grep）：方案 §12.3 门禁 4/5 的判据是
// 「GetSensorDefinitions() 声明并集 ⊆ 产出可达集（字面量 ∪ Sprintf 族）」与
// 「所有 SensorData{Name:...} 字面量匹配物理名文法」。这两条的输入是 Go 结构
// （方法体、复合字面量、格式串），grep 会把注释/测试/动作参数名一起算进去 ——
// 评审 F1 已实测：按「Name: 字面量」粗数会把 18 个动作参数名混进分母。
// 用 go/ast 精确取「GetSensorDefinitions 方法体内的 SensorData 复合字面量」，
// 才能既不加白名单、也不写死分母。
//
// 用法（由 scripts/check_field_semantics.sh 调用）：
//
//	go run ./scripts/fieldsem -drivers internal/drivers -mode decl
//	go run ./scripts/fieldsem -drivers internal/drivers -mode name
//
// 退出码：0 通过；1 有违例；2 用法/IO 错误。
package main

import (
	"flag"
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

// physicalRe 是 §5.1 的物理名文法（同时也是 family 前缀的合法形态）。
var physicalRe = regexp.MustCompile("^[a-z][a-z0-9_]*$")

// familyFmtRe 匹配 fmt.Sprintf 的「族格式串」：prefix_%d / prefix_%02d。
// 产出可达集里的族按 ^prefix_[0-9]+$ 计（与 §5.2 的 family 严格匹配一致）。
var familyFmtRe = regexp.MustCompile("^([a-z][a-z0-9_]*)_%(?:0?[0-9]+)?d$")

type posRange struct{ start, end token.Pos }

type nameSite struct {
	Name string
	Pos  string
}

type report struct {
	Mode               string
	Declared           []nameSite
	DeclaredNonLiteral []nameSite
	ReachableLiterals  int
	FamilyPatterns     []string
	Violations         []string
	Warns              []string
	AckNames           []string
	DynamicNameSites   []nameSite
	ActionParamNames   int
	ConcatSuffixes     []string
	reachable          map[string]bool
}

func main() {
	driversDir := flag.String("drivers", "internal/drivers", "drivers 包目录（相对 backend 根）")
	mode := flag.String("mode", "decl", "decl=声明⊆可达；name=产出名可寻址")
	flag.Parse()

	if *mode != "decl" && *mode != "name" {
		fmt.Fprintf(os.Stderr, "fieldsem: -mode 只能是 decl 或 name（收到 %q）\n", *mode)
		os.Exit(2)
	}

	absDir, err := filepath.Abs(*driversDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fieldsem: 解析目录失败: %v\n", err)
		os.Exit(2)
	}
	files, err := parsePackage(absDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fieldsem: %v\n", err)
		os.Exit(2)
	}
	if len(files) == 0 {
		fmt.Fprintf(os.Stderr, "fieldsem: 目录 %s 下没有可解析的非测试 .go 文件\n", absDir)
		os.Exit(2)
	}

	rep := analyze(absDir, files)
	if *mode == "decl" {
		checkDeclared(rep)
	} else {
		checkNames(rep)
	}
	emit(rep)
	if len(rep.Violations) > 0 {
		os.Exit(1)
	}
}

type parsedFile struct {
	path string // 相对 driversDir，便于打印
	fset *token.FileSet
	file *ast.File
}

func parsePackage(dir string) ([]parsedFile, error) {
	var out []parsedFile
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if path != dir {
				return filepath.SkipDir // drivers 包是单层目录；子目录（testdata）不参与
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return fmt.Errorf("解析 %s 失败: %w", path, err)
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			rel = name
		}
		out = append(out, parsedFile{path: rel, fset: fset, file: f})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out, nil
}

func analyze(dir string, files []parsedFile) *report {
	rep := &report{Mode: "decl", reachable: map[string]bool{}}

	// ① 声明：GetSensorDefinitions 方法体内的 SensorData 复合字面量。
	declRanges := map[string][]posRange{}
	for _, pf := range files {
		ast.Inspect(pf.file, func(n ast.Node) bool {
			fd, ok := n.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || fd.Name.Name != "GetSensorDefinitions" || fd.Body == nil {
				return true
			}
			declRanges[pf.path] = append(declRanges[pf.path], posRange{fd.Body.Pos(), fd.Body.End()})
			for _, c := range sensorComposites(fd.Body) {
				name, pos, literal := nameOf(c)
				where := relPosition(dir, pf.fset.Position(pos))
				if !literal {
					rep.DeclaredNonLiteral = append(rep.DeclaredNonLiteral, nameSite{Name: "(非字面量)", Pos: where})
					continue
				}
				rep.Declared = append(rep.Declared, nameSite{Name: name, Pos: where})
			}
			return true
		})
	}

	// ② 可达：声明体之外的物理名字面量（含字段表/告警映射/动作参数名 —— 这是
	//    「超集」口径，见文件头；因此本门禁只能证明「无幽灵声明」，不能证明反向）。
	for _, pf := range files {
		ranges := declRanges[pf.path]
		ast.Inspect(pf.file, func(n ast.Node) bool {
			bl, ok := n.(*ast.BasicLit)
			if !ok || bl.Kind != token.STRING {
				return true
			}
			if inRanges(bl.Pos(), ranges) {
				return true // 声明体自身不算「产出可达」
			}
			s, err := strconv.Unquote(bl.Value)
			if err != nil {
				return true
			}
			if physicalRe.MatchString(s) {
				rep.reachable[s] = true
			}
			return true
		})
		// ②b 拼接产出：parsePV 这类 "prefix + "_voltage"" 的产出名无法静态展开成字面量，
		//     但可抽出后缀常量 + 已在可达集里的前缀候选，组合成覆盖判据（超集，见文件头）。
		ast.Inspect(pf.file, func(n ast.Node) bool {
			be, ok := n.(*ast.BinaryExpr)
			if !ok || be.Op != token.ADD {
				return true
			}
			lit, ok := be.Y.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			suffix, err := strconv.Unquote(lit.Value)
			if err != nil || len(suffix) < 2 || suffix[0] != '_' {
				return true
			}
			if physicalRe.MatchString(suffix[1:]) {
				rep.ConcatSuffixes = appendUnique(rep.ConcatSuffixes, suffix)
			}
			return true
		})
		// ③ 族：fmt.Sprintf 的格式串 prefix_%d。
		ast.Inspect(pf.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Sprintf" || len(call.Args) == 0 {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			format, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			if m := familyFmtRe.FindStringSubmatch(format); m != nil {
				rep.FamilyPatterns = appendUnique(rep.FamilyPatterns, "^"+m[1]+"_[0-9]+$")
			}
			return true
		})
	}
	rep.ReachableLiterals = len(rep.reachable)

	// ④ 产出名可寻址（门禁 #5 的输入）：所有非声明体里的 SensorData 复合字面量。
	//    *_ack 是回读伪量（评审 F1 点名的 6 个），单独列出、不按物理名文法判。
	for _, pf := range files {
		ranges := declRanges[pf.path]
		for _, c := range sensorComposites(pf.file) {
			if inRanges(c.Pos(), ranges) {
				continue
			}
			name, pos, literal := nameOf(c)
			where := relPosition(dir, pf.fset.Position(pos))
			if !literal {
				rep.DynamicNameSites = append(rep.DynamicNameSites, nameSite{Name: "(动态)", Pos: where})
				continue
			}
			if strings.HasSuffix(name, "_ack") {
				rep.AckNames = appendUnique(rep.AckNames, name)
				continue
			}
			if !physicalRe.MatchString(name) {
				rep.Violations = append(rep.Violations, fmt.Sprintf(
					"产出名 %q 不匹配物理名文法 %s（%s）", name, physicalRe.String(), where))
			}
		}
	}

	// ⑤ 透明度：动作参数名计数（评审 F1 的 18 个混入项）。它只用于报告，
	//    不参与判定 —— 但要让读者看到「可达集是超集，超集里有多少动作参数」。
	for _, pf := range files {
		ast.Inspect(pf.file, func(n ast.Node) bool {
			c, ok := n.(*ast.CompositeLit)
			if !ok || (!isIdentType(c.Type, "ControlParameter") && !isIdentType(c.Type, "ControlAction")) {
				return true
			}
			for _, e := range c.Elts {
				kv, ok := e.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				k, ok := kv.Key.(*ast.Ident)
				if !ok || k.Name != "Name" {
					continue
				}
				if bl, ok := kv.Value.(*ast.BasicLit); ok && bl.Kind == token.STRING {
					rep.ActionParamNames++
				}
			}
			return true
		})
	}
	return rep
}

func checkDeclared(rep *report) {
	for _, d := range rep.Declared {
		covered := false
		for _, pat := range rep.FamilyPatterns {
			if ok, _ := regexp.MatchString(pat, d.Name); ok {
				covered = true
				break
			}
		}
		if !covered {
			// 拼接产出：name = prefix + suffix，prefix 必须在可达字面量集里。
			for _, suffix := range rep.ConcatSuffixes {
				if strings.HasSuffix(d.Name, suffix) {
					prefix := strings.TrimSuffix(d.Name, suffix)
					if prefix != "" && rep.reachable[prefix] {
						covered = true
						break
					}
				}
			}
		}
		if !covered && !rep.reachable[d.Name] {
			rep.Violations = append(rep.Violations, fmt.Sprintf(
				"幽灵声明 %q（%s）：drivers 里找不到任何产出它的物理名字面量/族", d.Name, d.Pos))
		}
	}
	for _, s := range rep.DeclaredNonLiteral {
		rep.Violations = append(rep.Violations, fmt.Sprintf(
			"GetSensorDefinitions 出现非字面量 Name（%s）—— 声明集必须可静态枚举（fail-closed）", s.Pos))
	}
}

func checkNames(rep *report) {
	for _, s := range rep.DynamicNameSites {
		rep.Warns = append(rep.Warns, fmt.Sprintf(
			"动态产出名（无法静态校验，需保证 loop/map 里的字面量合法）：%s", s.Pos))
	}
}

func emit(rep *report) {
	fmt.Printf("MODE=%s\n", rep.Mode)
	fmt.Printf("DECLARED=%d\n", len(rep.Declared))
	fmt.Printf("DECLARED_NONLITERAL=%d\n", len(rep.DeclaredNonLiteral))
	fmt.Printf("REACHABLE_LITERALS=%d\n", rep.ReachableLiterals)
	fmt.Printf("FAMILY_PATTERNS=%d\n", len(rep.FamilyPatterns))
	for _, p := range rep.FamilyPatterns {
		fmt.Printf("FAMILY=%s\n", p)
	}
	fmt.Printf("CONCAT_SUFFIXES=%d\n", len(rep.ConcatSuffixes))
	for _, s := range rep.ConcatSuffixes {
		fmt.Printf("CONCAT_SUFFIX=%s\n", s)
	}
	fmt.Printf("ACTION_PARAM_NAMES=%d\n", rep.ActionParamNames)
	fmt.Printf("ACK_NAMES=%d\n", len(rep.AckNames))
	for _, n := range rep.AckNames {
		fmt.Printf("ACK=%s\n", n)
	}
	for _, w := range rep.Warns {
		fmt.Printf("WARN=%s\n", w)
	}
	for _, v := range rep.Violations {
		fmt.Printf("VIOLATION=%s\n", v)
	}
}

// ---- AST 小工具 ----

func sensorComposites(root ast.Node) []*ast.CompositeLit {
	var out []*ast.CompositeLit
	ast.Inspect(root, func(n ast.Node) bool {
		c, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		switch t := c.Type.(type) {
		case *ast.Ident:
			if t.Name == "SensorData" {
				out = append(out, c)
			}
		case *ast.ArrayType:
			if isIdentType(t.Elt, "SensorData") {
				for _, e := range c.Elts {
					if el, ok := e.(*ast.CompositeLit); ok {
						out = append(out, el)
					}
				}
			}
		}
		return true
	})
	return out
}

func isIdentType(expr ast.Expr, name string) bool {
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == name
}

// nameOf 返回复合字面量的 Name 字段值；literal=false 时 name 无意义。
func nameOf(c *ast.CompositeLit) (name string, pos token.Pos, literal bool) {
	for _, e := range c.Elts {
		kv, ok := e.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		k, ok := kv.Key.(*ast.Ident)
		if !ok || k.Name != "Name" {
			continue
		}
		if bl, ok := kv.Value.(*ast.BasicLit); ok && bl.Kind == token.STRING {
			if s, err := strconv.Unquote(bl.Value); err == nil {
				return s, bl.Pos(), true
			}
		}
		return "", kv.Value.Pos(), false
	}
	return "", c.Pos(), false
}

func inRanges(pos token.Pos, ranges []posRange) bool {
	for _, r := range ranges {
		if pos >= r.start && pos <= r.end {
			return true
		}
	}
	return false
}

func appendUnique(list []string, s string) []string {
	for _, v := range list {
		if v == s {
			return list
		}
	}
	return append(list, s)
}

func relPosition(dir string, pos token.Position) string {
	rel, err := filepath.Rel(dir, pos.Filename)
	if err != nil {
		return pos.String()
	}
	return fmt.Sprintf("%s:%d", rel, pos.Line)
}
