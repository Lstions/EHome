package automation

// F4 条件复核（`checkConditionsStillSatisfied`）的**传感器归属**回归锁。
//
// 缺陷（2026-09-16 探针实测）：trigger 复核只比对**数值**，不比对 `SensorName`。
// 而最新值缓存每设备只留一条记录（`map[edge_device_id]UnifiedData`，见
// `internal/api/latest_value_cache.go`），它可能是**别的**物理量 ⇒
// 规则「temperature > 50」遇到缓存「humidity 80」时被判为「仍满足」，
// 于是**条件已失效却照常执行动作**（fail-open，方向危险）。
//
// 为什么这比 docs/设计/自动化引擎场景仿真验证.md §7.3 记载的盲区更严重：
// §7.3 记的是「非最新那个传感器的附加条件**复核不到** ⇒ 静默跳过」（偏保守）；
// 本条是「拿无关物理量的值凑出一个**结论**」—— 前者顶多漏复核，后者会**误判为满足**。
//
// 修法：trigger 复核前先比对 `rec.SensorName == rule.TriggerSensorName`；
// 不匹配即判「不再满足」并说明原因（fail-closed：涉及是否执行动作，判不准时偏向不执行）。

import (
	"strings"
	"testing"

	"ehome/backend/internal/models"
)

// newRecheckPlanner 造一个只注入最新值回调的 Planner（复核逻辑不碰 DB）。
func newRecheckPlanner(latest func(uint) (models.UnifiedData, bool)) *Planner {
	p := NewPlanner(nil, nil, nil, 0)
	p.SetLatestValueFn(latest)
	return p
}

func thresholdRule() models.AutomationRule {
	return models.AutomationRule{
		TriggerType:         models.AutomationTriggerSensorThreshold,
		TriggerEdgeDeviceID: 1,
		TriggerSensorName:   "temperature",
		TriggerComparator:   "gt",
		TriggerThreshold:    50,
	}
}

// TestRecheck_RefusesWhenCacheHoldsAnotherSensor 是本次修复的核心断言。
func TestRecheck_RefusesWhenCacheHoldsAnotherSensor(t *testing.T) {
	p := newRecheckPlanner(func(uint) (models.UnifiedData, bool) {
		return models.UnifiedData{SensorName: "humidity", Value: 80}, true
	})

	reason, ok := p.checkConditionsStillSatisfied(thresholdRule())
	if ok {
		t.Errorf("缓存持有 humidity=80，却被判为「温度>50 仍满足」—— " +
			"拿无关物理量凑结论会让已失效的条件照常执行动作")
	}
	if !strings.Contains(reason, "temperature") || !strings.Contains(reason, "humidity") {
		t.Errorf("拒绝原因应点明「哪个传感器不在缓存里、缓存里是什么」，实际: %q", reason)
	}
}

// TestRecheck_SameSensorStillEvaluated 是**反向对照**：
// 缓存里就是本触发器传感器时必须**照常复核**，不能被加固改成「一律拒绝」。
// 只测拒绝会漏掉「过度拒绝」这类回归 —— 那会把功能改坏而测试仍绿。
func TestRecheck_SameSensorStillEvaluated(t *testing.T) {
	// 条件仍满足（60 > 50）⇒ 放行
	pOK := newRecheckPlanner(func(uint) (models.UnifiedData, bool) {
		return models.UnifiedData{SensorName: "temperature", Value: 60}, true
	})
	if _, ok := pOK.checkConditionsStillSatisfied(thresholdRule()); !ok {
		t.Errorf("同传感器且条件仍满足时不应拒绝（加固不得变成一律拒绝）")
	}

	// 条件已失效（40 < 50）⇒ 必须拒绝（这条是既有语义，防加固时误删）
	pStale := newRecheckPlanner(func(uint) (models.UnifiedData, bool) {
		return models.UnifiedData{SensorName: "temperature", Value: 40}, true
	})
	if _, ok := pStale.checkConditionsStillSatisfied(thresholdRule()); ok {
		t.Errorf("同传感器但条件已失效（40 < 50）时必须拒绝")
	}
}

// TestRecheck_NilFnSkipsUnchanged 钉住既有兼容行为：未接线时跳过复核（返回 true）。
func TestRecheck_NilFnSkipsUnchanged(t *testing.T) {
	p := NewPlanner(nil, nil, nil, 0) // 不注入 latestValueFn
	if _, ok := p.checkConditionsStillSatisfied(thresholdRule()); !ok {
		t.Errorf("未注入最新值回调时应跳过复核（保持既有行为），实际拒绝了")
	}
}
