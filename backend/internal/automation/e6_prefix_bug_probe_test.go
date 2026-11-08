package automation

// e6_prefix_bug_probe_test.go —— 当前代码树上的 F4 正确性探针（与基线探针成对）。
//
// 断言一件事：F4 复核查最新值时，用的是**触发时成员**（ev.SourceEdgeDeviceID），
// 而不是规则配置里的 trigger_edge_device_id。这正是 E6 要防的错源。
//
// 2026-10-23 注：逻辑设备概念层已整体移除，trigger_edge_device_id 现在**只能**是
// edge device id —— 也就是说「配置列恰好存了别的东西」在正常路径上已不可能发生。
// 本探针仍然保留：它守的是**实现方式**（查的是 ev.SourceEdgeDeviceID 还是规则列），
// 而这个区别在「主备组成员触发」场景下依然真实存在（规则列的锚点成员 ≠ 触发成员）。
// 因此把场景从「逻辑 ID 分歧」重述为「配置锚点 ≠ 触发成员」，断言完全不变。
//
// 与基线探针（.analysis/e6_baseline_probe.txt，门禁脚本拷进基线 worktree）成对：
//   基线树只能编译那份（单参入口 checkConditionsStillSatisfied(rule)），必红；
//   本树只能编译这份（带 source 证据的入口），必绿。
// 两者合起来才是「修复前红、修复后绿」的双向证明 —— 只测后者无法区分
// 「bug 修好了」与「测试压根没摸到 bug」。
//
// 与 e2e 的 TestE6_F4PinsTriggerMemberOnIDDivergence 分工：
//   e2e 走完整链路证明「功能可用」；本文件是单元级探针，直接钉住「查的是哪个
//   deviceID」，因此能脱离 sourceref/databus 在基线上单独运行。

import (
	"testing"
	"time"

	"ehome/backend/internal/models"
)

func TestE6Probe_F4QueriesTriggerMemberNotConfiguredID(t *testing.T) {
	const (
		physicalEdgeID = uint(101) // 实际触发的成员设备
		configuredID   = uint(7)   // 规则配置里的锚点（组锚点/历史遗留），不等于触发成员
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
		TriggerEdgeDeviceID: configuredID, // 配置列 ≠ 触发成员（分歧根源）
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
		if id == configuredID {
			t.Fatalf("F4 用规则配置的 %d 查最新值 —— 错源 bug 复现（应为触发成员 %d）；queried=%v",
				configuredID, physicalEdgeID, queried)
		}
	}
}
