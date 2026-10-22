package automation

// e6_prefix_bug_probe_test.go —— 当前代码树上的 F4 正确性探针（与基线探针成对）。
//
// 断言一件事：F4 复核查最新值时，用的是**触发时成员**（ev.SourceEdgeDeviceID），
// 而不是规则配置里的 trigger_edge_device_id（逻辑设备 ID）。这正是 E6 要防的错源。
//
// 与基线探针（.analysis/e6_baseline_probe.txt，门禁脚本拷进基线 worktree）成对：
//   基线树只能编译那份（单参入口 checkConditionsStillSatisfied(rule)），必红；
//   本树只能编译这份（带 source 证据的入口），必绿。
// 两者合起来才是「修复前红、修复后绿」的双向证明 —— 只测后者无法区分
// 「bug 修好了」与「测试压根没摸到 bug」。
//
// 与 e2e 的 TestE6_F4PinsTriggerMemberOnLogicalIDDivergence 分工：
//   e2e 走完整链路证明「功能可用」；本文件是单元级探针，直接钉住「查的是哪个
//   deviceID」，因此能脱离 sourceref/databus 在基线上单独运行。

import (
	"testing"
	"time"

	"ehome/backend/internal/models"
)

func TestE6Probe_F4QueriesTriggerMemberNotLogicalID(t *testing.T) {
	const (
		physicalEdgeID = uint(101)
		logicalID      = uint(7)
	)
	var queried []uint
	// 必须用 NewPlanner 而不是裸 &Planner{}：nowFn 默认值是构造函数里赋的，
	// 裸结构体会在 checkConditionsStillSatisfiedWithSource 里 p.nowFn() 处 nil deref。
	// （这也是个真实教训：测试里绕过构造函数造对象，会在恰好用到默认字段时炸。）
	p := NewPlanner(nil, nil, func(string, any) {}, 0)
	p.SetLatestValueBySensorFn(func(deviceID uint, sensorName string) (models.UnifiedData, bool) {
		queried = append(queried, deviceID)
		if deviceID == physicalEdgeID && sensorName == "illuminance" {
			return models.UnifiedData{DeviceID: physicalEdgeID, SensorName: "illuminance", Value: 600}, true
		}
		return models.UnifiedData{}, false
	})
	rule := models.AutomationRule{
		ID: 1, Enabled: true,
		TriggerType:         models.AutomationTriggerSensorThreshold,
		TriggerSensorName:   "illuminance",
		TriggerComparator:   "gt",
		TriggerThreshold:    500,
		TriggerEdgeDeviceID: logicalID, // 配置列 = 逻辑 ID（分歧根源）
	}
	ev := TriggerEvent{Rule: rule, SourceEdgeDeviceID: physicalEdgeID, At: time.Now()}
	reason, result, ok := p.checkConditionsStillSatisfiedWithSource(rule, ev)
	if !ok {
		t.Fatalf("F4 复核失败（queried=%v, result=%q, reason=%q）—— 未对齐触发成员",
			queried, result, reason)
	}
	if len(queried) == 0 {
		t.Fatal("F4 复核没有查询任何设备（未接线假绿）")
	}
	for _, id := range queried {
		if id == logicalID {
			t.Fatalf("F4 用逻辑 ID %d 查最新值 —— 错源 bug 复现（应为触发成员 %d）；queried=%v",
				logicalID, physicalEdgeID, queried)
		}
	}
}
