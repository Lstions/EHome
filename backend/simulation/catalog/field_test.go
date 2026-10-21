//go:build simulation

// SIM-FIELD 骨架的清单门禁（不依赖 DB / HTTP，可在 CI 直接跑）：
//
//  1. 8 个场景必须全部注册、ID/标题/设计依据/前置数据/不变量完整（不吊占位场景）；
//  2. 覆盖矩阵必须与方案 §13.3 + §5.3 v0.3 缺员方向性逐项对应（不许静默删场景）；
//  3. 集成点未接线时"跳过原因"必须非空（Skip 是显式状态，不是默认通过）；
//     接线后同一判定必须转为 fail-closed（执行体未实现不得静默通过）。
package catalog

import (
	"strings"
	"testing"

	"ehome/backend/internal/sensorfield"
)

// fieldExpectedCoverage 是方案 §13.3 冻结的 8 个场景覆盖标签（一个都不能少）。
var fieldExpectedCoverage = map[string]bool{
	"cross_vendor_semantic": true,
	"family_shrink_4_to_2":  true,
	"family_disappeared":    true,
	"alias_conflict":        true,
	"virtual_missing_input": true,
	"empty_set":             true,
	"partial_frame_reason":  true,
	"legacy_value":          true,
}

// TestFieldSkeletonInventory 校验场景清单完整、可枚举、可解释。
func TestFieldSkeletonInventory(t *testing.T) {
	if got, want := len(fieldSpecs), 8; got != want {
		t.Fatalf("SIM-FIELD 场景数 %d，期望 %d（方案 §13.3 SIM-FIELD-001..008）", got, want)
	}
	pattern := ScenarioIDPattern()
	seenIDs := map[string]bool{}
	seenCoverage := map[string]bool{}
	registered := map[string]Scenario{}
	for _, s := range All() {
		registered[s.ID] = s
	}
	for i, spec := range fieldSpecs {
		expectedID := []string{
			"SIM-FIELD-001", "SIM-FIELD-002", "SIM-FIELD-003", "SIM-FIELD-004",
			"SIM-FIELD-005", "SIM-FIELD-006", "SIM-FIELD-007", "SIM-FIELD-008",
		}[i]
		if spec.ID != expectedID {
			t.Errorf("第 %d 个场景 ID=%q，期望 %q（顺序是冻结契约）", i, spec.ID, expectedID)
		}
		if !pattern.MatchString(spec.ID) {
			t.Errorf("%s 不符合场景 ID 形态 %s", spec.ID, pattern.String())
		}
		if seenIDs[spec.ID] {
			t.Errorf("场景 ID 重复：%s", spec.ID)
		}
		seenIDs[spec.ID] = true
		if spec.Title == "" || spec.Doc == "" || spec.Setup == "" {
			t.Errorf("%s 的 Title/Doc/Setup 不得为空（禁止占位场景）", spec.ID)
		}
		if len(spec.Checks) < 2 {
			t.Errorf("%s 只有 %d 条不变量（骨架要求 >=2 条，可解释）", spec.ID, len(spec.Checks))
		}
		for _, c := range spec.Checks {
			if strings.TrimSpace(c.Invariant) == "" || strings.TrimSpace(c.Expect) == "" {
				t.Errorf("%s 存在空的不变量/期望（禁止空断言）", spec.ID)
			}
		}
		if len(spec.Coverage) == 0 {
			t.Errorf("%s 缺覆盖标签（无法证明覆盖矩阵）", spec.ID)
		}
		for _, tag := range spec.Coverage {
			if !fieldExpectedCoverage[tag] {
				t.Errorf("%s 覆盖标签 %q 不在方案 §13.3 冻结集合内", spec.ID, tag)
			}
			seenCoverage[tag] = true
		}
		scenario, ok := registered[spec.ID]
		if !ok {
			t.Errorf("%s 未注册进 catalog（scenario 不可枚举 ⇒ 不会执行）", spec.ID)
			continue
		}
		if scenario.Domain != fieldDomain {
			t.Errorf("%s 的 Domain=%q，期望 %q", spec.ID, scenario.Domain, fieldDomain)
		}
		if scenario.Run == nil {
			t.Errorf("%s 的 Run 为 nil（禁止占位场景）", spec.ID)
		}
		if scenario.Title != spec.Title || scenario.Doc != spec.Doc {
			t.Errorf("%s 注册信息与 spec 不一致（两处定义会漂移）", spec.ID)
		}
	}
	for tag := range fieldExpectedCoverage {
		if !seenCoverage[tag] {
			t.Errorf("覆盖矩阵缺项：%q（方案 §13.3 要求；不得静默删除场景）", tag)
		}
	}
	if !IsKnownDomain(DomainFIELD) {
		t.Errorf("DomainFIELD 未登记进 KnownDomains（catalog 门禁 7 会红）")
	}
}

// fieldFakePlan / fieldFakeResolver 用于验证集成点判定，不访问 DB/HTTP。
type fieldFakePlan struct{}

func (fieldFakePlan) Selector() string       { return "fake" }
func (fieldFakePlan) Kind() sensorfield.Kind { return sensorfield.KindLiteral }
func (fieldFakePlan) Eval(_ []sensorfield.Reading) sensorfield.EvalResult {
	return sensorfield.EvalResult{OK: false, Reason: "fake"}
}

type fieldFakeResolver struct{}

func (fieldFakeResolver) Resolve(_ uint, selector string) (sensorfield.Resolved, error) {
	return sensorfield.Resolved{Selector: selector, Status: sensorfield.StatusUnbound}, nil
}
func (fieldFakeResolver) Compile(_ uint, selector string) (sensorfield.Plan, error) {
	return fieldFakePlan{}, nil
}

// TestFieldResolverGate 证明：未接线 ⇒ 显式 Skip 原因；接线后 ⇒ fail-closed。
func TestFieldResolverGate(t *testing.T) {
	old := fieldSetResolverFactory(nil)
	defer fieldSetResolverFactory(old)

	if reason := fieldPendingReason(); reason == "" {
		t.Fatal("factory=nil 时 pending reason 必须非空（否则 Skip 静默变成空原因）")
	} else if !strings.Contains(reason, "sensorfield") {
		t.Fatalf("pending reason 必须写明缺失依赖（含 sensorfield）：%q", reason)
	}
	if got := fieldRunDisposition(false); got != "skip" {
		t.Fatalf("未接线时 disposition=%q，期望 skip（显式跳过）", got)
	}

	fieldSetResolverFactory(func() fieldResolver { return fieldFakeResolver{} })
	if reason := fieldPendingReason(); reason != "" {
		t.Fatalf("factory 已注入但 pending reason 仍非空：%q", reason)
	}
	if got := fieldRunDisposition(true); got != "failclosed" {
		t.Fatalf("接线后 disposition=%q，期望 failclosed（执行体未实现必须红，不得跳过）", got)
	}
}

// TestFieldScenarioDocAnchors 防止场景 Doc 指向不存在的设计文件。
func TestFieldScenarioDocAnchors(t *testing.T) {
	for _, spec := range fieldSpecs {
		if !strings.Contains(spec.Doc, "数据字段解析层-方案-2026-10-21.md") {
			t.Errorf("%s 的 Doc 未指向唯一权威设计：%q", spec.ID, spec.Doc)
		}
	}
}

// TestFieldCheckSummary 证明清单可解释（摘要含 ID/检查数/覆盖标签）。
func TestFieldCheckSummary(t *testing.T) {
	for _, spec := range fieldSpecs {
		got := fieldCheckSummary(spec)
		if !strings.Contains(got, spec.ID) || !strings.Contains(got, "checks") {
			t.Errorf("check summary 不可解释：%q", got)
		}
	}
}

// 编译期断言：假实现确实满足集成点（接口漂移时构建失败而不是静默不匹配）。
var _ fieldResolver = fieldFakeResolver{}
