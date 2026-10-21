package automation

// F4 条件复核的 selector 接入测试（修 AU-12）。
//
// 缺陷：F4 固定用"每设备最后一条最新值"复核，特殊 selector（@soc / family /
// virtual）无法命中 ⇒ trigger 一律拒绝、conditions 一律跳过（计划永不执行）。
// 修复后：literal 走 (device,物理名) 精确最新值；特殊 selector 走最新一帧 + plan +
// EvalPredicate；取不到 fail-closed 并区分 unresolved_selector/field_resolver_disabled。

import (
	"strings"
	"testing"

	"ehome/backend/internal/models"
	"ehome/backend/internal/sensorfield"
)

func selectorRule(sensor, cmp string, threshold float64) models.AutomationRule {
	return models.AutomationRule{
		TriggerType:         models.AutomationTriggerSensorThreshold,
		TriggerEdgeDeviceID: 1,
		TriggerSensorName:   sensor,
		TriggerComparator:   cmp,
		TriggerThreshold:    threshold,
	}
}

func latestBySensor(rec models.UnifiedData, ok bool) func(uint, string) (models.UnifiedData, bool) {
	return func(uint, string) (models.UnifiedData, bool) { return rec, ok }
}

func latestFrame(records ...models.UnifiedData) func(uint) []models.UnifiedData {
	return func(uint) []models.UnifiedData { return records }
}

// AU-12 主用例：@soc selector 能通过 F4 复核（修复前恒拒绝）。
func TestRecheckSelectorSemanticSatisfied(t *testing.T) {
	p := NewPlanner(nil, nil, nil, 0)
	prov := newFakeProvider(alwaysVerdict(sensorfield.KindSemantic, true, true, 20, ""))
	p.SetPlanProvider(prov)
	p.SetLatestValuesFn(latestFrame(models.UnifiedData{SensorName: "rsoc", Value: 20}))

	result, _, ok := p.checkConditionsStillSatisfiedDetailed(selectorRule("@soc", "lt", 30))
	if !ok {
		t.Fatalf("@soc<30 在最新帧 rsoc=20 下应通过复核，result=%q", result)
	}
}

// 条件已失效 ⇒ condition_changed（result 为空），不得误报解析失败。
func TestRecheckSelectorConditionChanged(t *testing.T) {
	p := NewPlanner(nil, nil, nil, 0)
	prov := newFakeProvider(alwaysVerdict(sensorfield.KindSemantic, true, false, 40, ""))
	p.SetPlanProvider(prov)
	p.SetLatestValuesFn(latestFrame(models.UnifiedData{SensorName: "rsoc", Value: 40}))

	result, reason, ok := p.checkConditionsStillSatisfiedDetailed(selectorRule("@soc", "lt", 30))
	if ok || result != "" {
		t.Fatalf("条件不满足应落 condition_changed（result 空），got result=%q ok=%v reason=%q", result, ok, reason)
	}
	if !strings.Contains(reason, "no longer satisfied") {
		t.Fatalf("reason 应说明条件失效，got %q", reason)
	}
}

// unbound（OK=false）⇒ unresolved_selector + fail-closed。
func TestRecheckSelectorUnboundFailsClosed(t *testing.T) {
	p := NewPlanner(nil, nil, nil, 0)
	prov := newFakeProvider(alwaysVerdict(sensorfield.KindFamily, false, false, 0, "partial_frame"))
	p.SetPlanProvider(prov)
	p.SetLatestValuesFn(latestFrame(models.UnifiedData{SensorName: "temperature_1", Value: 50}))

	result, reason, ok := p.checkConditionsStillSatisfiedDetailed(selectorRule("temperature_*:max", "gt", 45))
	if ok {
		t.Fatal("unbound 必须 fail-closed（不执行动作）")
	}
	if result != ResultUnresolvedSelector {
		t.Fatalf("应落 unresolved_selector，got %q (reason=%q)", result, reason)
	}
}

// kill switch ⇒ field_resolver_disabled（与"解析不到"分开，运维处置不同）。
func TestRecheckSelectorDisabledFailsClosed(t *testing.T) {
	p := NewPlanner(nil, nil, nil, 0)
	prov := newFakeProvider(alwaysVerdict(sensorfield.KindFamily, true, true, 50, ""))
	prov.setEnabled(false)
	p.SetPlanProvider(prov)
	p.SetLatestValuesFn(latestFrame(models.UnifiedData{SensorName: "temperature_1", Value: 50}))

	result, _, ok := p.checkConditionsStillSatisfiedDetailed(selectorRule("temperature_*:max", "gt", 45))
	if ok || result != ResultFieldResolverDisabled {
		t.Fatalf("kill switch 应 fail-closed 且落 field_resolver_disabled，got ok=%v result=%q", ok, result)
	}
}

// literal 走 (device,物理名) 精确最新值（api.LatestValueBySensor 语义）。
func TestRecheckLiteralUsesLatestValueBySensor(t *testing.T) {
	p := NewPlanner(nil, nil, nil, 0)
	p.SetLatestValueBySensorFn(func(deviceID uint, sensorName string) (models.UnifiedData, bool) {
		if deviceID != 1 || sensorName != "temperature" {
			return models.UnifiedData{}, false
		}
		return models.UnifiedData{SensorName: "temperature", Value: 60}, true
	})

	if _, _, ok := p.checkConditionsStillSatisfiedDetailed(selectorRule("temperature", "gt", 50)); !ok {
		t.Fatal("literal 60>50 应通过复核")
	}
	// 取不到该物理名 ⇒ fail-closed（reason 指明物理名）。
	p2 := NewPlanner(nil, nil, nil, 0)
	p2.SetLatestValueBySensorFn(func(uint, string) (models.UnifiedData, bool) {
		return models.UnifiedData{}, false
	})
	_, reason, ok := p2.checkConditionsStillSatisfiedDetailed(selectorRule("temperature", "gt", 50))
	if ok || !strings.Contains(reason, "temperature") {
		t.Fatalf("literal 取不到应 fail-closed 且点明物理名，ok=%v reason=%q", ok, reason)
	}
}

// 特殊 selector 的附加条件也要复核（AU-12 的 conditions 半边）。
func TestRecheckSelectorConditionsSatisfied(t *testing.T) {
	p := NewPlanner(nil, nil, nil, 0)
	prov := newFakeProvider(alwaysVerdict(sensorfield.KindSemantic, true, true, 20, ""))
	p.SetPlanProvider(prov)
	p.SetLatestValueBySensorFn(func(uint, string) (models.UnifiedData, bool) {
		return models.UnifiedData{SensorName: "temperature", Value: 60}, true
	})
	p.SetLatestValuesFn(latestFrame(
		models.UnifiedData{SensorName: "temperature", Value: 60},
		models.UnifiedData{SensorName: "rsoc", Value: 20},
	))

	r := selectorRule("temperature", "gt", 50)
	r.ConditionsJSON = condJSON("@soc", "lt", 30)
	if _, _, ok := p.checkConditionsStillSatisfiedDetailed(r); !ok {
		t.Fatal("trigger 满足且 @soc 条件满足时应通过复核")
	}

	// 条件不满足 ⇒ condition_changed。
	provFalse := newFakeProvider(alwaysVerdict(sensorfield.KindSemantic, true, false, 80, ""))
	p2 := NewPlanner(nil, nil, nil, 0)
	p2.SetPlanProvider(provFalse)
	p2.SetLatestValueBySensorFn(func(uint, string) (models.UnifiedData, bool) {
		return models.UnifiedData{SensorName: "temperature", Value: 60}, true
	})
	p2.SetLatestValuesFn(latestFrame(models.UnifiedData{SensorName: "rsoc", Value: 80}))
	result, _, ok := p2.checkConditionsStillSatisfiedDetailed(r)
	if ok || result != "" {
		t.Fatalf("@soc 条件不满足应落 condition_changed，got ok=%v result=%q", ok, result)
	}
}

// 未接线时保持既有"跳过复核"行为（旧单测/纯通知规则不受影响）。
func TestRecheckSelectorUnwiredSkips(t *testing.T) {
	p := NewPlanner(nil, nil, nil, 0)
	if _, _, ok := p.checkConditionsStillSatisfiedDetailed(selectorRule("@soc", "lt", 30)); !ok {
		t.Fatal("未注入任何最新值来源时应跳过复核（保持既有行为）")
	}
}
