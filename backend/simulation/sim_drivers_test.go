//go:build simulation

package simulation

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/simdrivers"
)

// 本文件是仿真型号登记的**结构性门禁**（不需要 PG/EMQX 基础设施）。
//
// 背景（2026-09-21 门禁收紧）：POST /device-configs 现在要求 device_type 能在
// drivers.Registry 中解析，地址门禁对"未注册型号"也 fail-closed。仿真框架过去
// 刻意依赖未注册型号，现在改为在 internal/simdrivers 里显式登记
// （只在该构建标签下编译，生产不带该后门）。
//
// 这条用例守的是"登记清单与仿真源码同步"：新增一个仿真型号而忘了登记时，
// 症状会离原因很远（场景在三个不同的公开端点上陆续 400），所以在这里直接变红。
func TestSimulationDriverTypesCoverEverySpecType(t *testing.T) {
	registry := drivers.NewRegistry()
	drivers.RegisterBuiltInDrivers(registry)
	simdrivers.Register(registry)

	files := []string{}
	for _, dir := range []string{"catalog", "harness"} {
		matches, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, matches...)
	}

	found := map[string][]string{}
	scanned := 0
	for _, file := range files {
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", file, err)
		}
		scanned++
		ast.Inspect(parsed, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			// 只认"设备型号"的复合字面量：edgeDeviceSpec / sceneSpec。
			// 不限定类型名会把 sceneField{Name:..., Type:"uint16"} 里的**字段数据类型**
			// 也当成型号（首版就误报过 int16/uint16/uint32 —— 门禁的假阳性同样有害）。
			typeName := ""
			switch t := lit.Type.(type) {
			case *ast.Ident:
				typeName = t.Name
			case *ast.SelectorExpr:
				typeName = t.Sel.Name
			}
			if typeName != "edgeDeviceSpec" && typeName != "sceneSpec" {
				return true
			}
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok || (key.Name != "DeviceType" && key.Name != "Type") {
					continue
				}
				val, ok := kv.Value.(*ast.BasicLit)
				if !ok || val.Kind != token.STRING {
					continue
				}
				deviceType := strings.Trim(val.Value, "\"")
				found[deviceType] = append(found[deviceType], filepath.Base(file))
			}
			return true
		})
		// 第三种型号位置：autoProvisionDevice(e, scenarioID, suffix, deviceType)
		// 的第 4 实参。它不是复合字面量，上面的 Inspect 抓不到 —— 而 138 个场景里
		// 绝大多数自定义型号正是从这里进来的（首版遗漏后实测 42 个场景变红）。
		ast.Inspect(parsed, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			ident, ok := call.Fun.(*ast.Ident)
			if !ok || ident.Name != "autoProvisionDevice" || len(call.Args) != 4 {
				return true
			}
			val, ok := call.Args[3].(*ast.BasicLit)
			if !ok || val.Kind != token.STRING {
				return true
			}
			deviceType := strings.Trim(val.Value, "\"")
			found[deviceType] = append(found[deviceType], filepath.Base(file))
			return true
		})
	}

	// 下界断言（防止扫描器本身坏掉后永远绿）：必须真的扫到文件与型号。
	if scanned == 0 {
		t.Fatal("没有扫描到任何仿真源文件 —— 门禁路径写错了")
	}
	if len(found) == 0 {
		t.Fatal("没有提取到任何型号字面量 —— 提取逻辑失效，门禁形同虚设")
	}
	// 分类器自检：已知**未登记**的样本必须被判为缺失，已知已登记的必须通过。
	if _, err := registry.Get("definitely_not_a_registered_driver"); err == nil {
		t.Fatal("分类器自检失败：明显未注册的型号被判为已注册")
	}
	if _, err := registry.Get("sn3001_rain"); err != nil {
		t.Fatalf("分类器自检失败：内置型号 sn3001_rain 解析不了: %v", err)
	}

	var missing []string
	for deviceType, places := range found {
		if _, err := registry.Get(deviceType); err != nil {
			missing = append(missing, fmt.Sprintf("%s (%s)", deviceType, strings.Join(places, ",")))
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("以下仿真型号未在驱动注册表中登记，收紧后的门禁会拒绝它们：\n  %s\n请在 internal/simdrivers/register.go 的 types 中补登记。",
			strings.Join(missing, "\n  "))
	}
	// 清单本身也要自证没瞎：simdrivers.Types() 里的每一项都必须真的可解析。
	for _, deviceType := range simdrivers.Types() {
		if _, err := registry.Get(deviceType); err != nil {
			t.Fatalf("simdrivers.Types() 中的 %q 注册后仍解析不了: %v", deviceType, err)
		}
	}
	t.Logf("已扫描 %d 个仿真源文件，%d 个型号全部可在（含仿真登记的）注册表中解析", scanned, len(found))
}
