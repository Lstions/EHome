//go:build e2e

package e2e

// 全链路用例 E1：三种数据来源组合各触发一次（用户需求 ①②③ 的贯通验证）。
//
// 这是对「两个光照传感器、最多 3 种组合」这条需求的**唯一权威验证**：
//   - 1 号单独用（存量单设备语义，证明零回归）；
//   - 2 号单独用；
//   - 1+2 合成（复合来源，逐成员取值再按 combine 合并）。
// 三条规则同时存在，靠**真实上报帧**驱动，断言互不串台。

import (
	"testing"

	"ehome/backend/internal/datasource"
	"ehome/backend/internal/models"
)

func TestSG_E1_ThreeBindingCombinations(t *testing.T) {
	// 顺序（生产同理，不是测试取巧）：
	//   来源组创建有 V-5 前置校验「该设备是否上报过该类别」，而观测来自真实入库，
	//   所以必须「两台设备各跑一帧建立观测 → 建组 → 绑定规则」。
	// 规则先以禁用状态建库，绑完来源后再启用（否则 Evaluator 装载的是禁用快照）。
	newRule := func(name string) *models.AutomationRule {
		return &models.AutomationRule{
			Name: name, Enabled: false,
			TriggerType:       models.AutomationTriggerSensorThreshold,
			TriggerSensorName: "illuminance", TriggerComparator: "gt", TriggerThreshold: 500,
			ActionType: models.AutomationActionNotification,
		}
	}
	ruleA, ruleB, ruleC := newRule("只用1号光照"), newRule("只用2号光照"), newRule("1+2合成光照")
	c := newSGFullChain(t, ruleA, ruleB, ruleC)

	// ① 播种：两台各上报一帧（低光照，未达阈值），建立「已观测」与最新值。
	c.publish(t, c.edgeA, prs3001Frame4B(0, 100), 1)
	c.publish(t, c.edgeB, prs3001Frame4B(0, 120), 2)
	for _, dev := range []models.EdgeDevice{c.edgeA, c.edgeB} {
		var n int64
		if err := c.db.Model(&models.UnifiedData{}).
			Where("device_id = ? AND sensor_name = ?", dev.ID, "illuminance").Count(&n).Error; err != nil {
			t.Fatalf("count unified_data: %v", err)
		}
		if n == 0 {
			t.Fatalf("设备 %d 没有 illuminance 行：播种帧没被解析入库，V-5 前置条件不成立", dev.ID)
		}
	}

	// ② 绑定：A/B 走存量单设备语义。
	if err := c.db.Model(&models.AutomationRule{}).Where("id = ?", ruleA.ID).
		Update("trigger_edge_device_id", c.edgeA.ID).Error; err != nil {
		t.Fatalf("bind ruleA: %v", err)
	}
	if err := c.db.Model(&models.AutomationRule{}).Where("id = ?", ruleB.ID).
		Update("trigger_edge_device_id", c.edgeB.ID).Error; err != nil {
		t.Fatalf("bind ruleB: %v", err)
	}

	// ③ 建复合组：avg(A,B)、min_members=2（两台都要在，缺一即 unbound）。
	composite, err := c.gsvc.CreateGroup(datasource.CreateGroupInput{
		Name: "1+2合成光照源", Kind: models.SourceGroupKindComposite, Category: "illuminance",
		Combine: models.CombineAvg, MinMembers: 2, StalenessSec: 300,
		Members: []datasource.GroupMemberInput{
			{EdgeDeviceID: c.edgeA.ID, Priority: 100},
			{EdgeDeviceID: c.edgeB.ID, Priority: 90},
		},
	})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	// ④ C 绑组 + 双写锚点（方案 N-7）：回退旧二进制时旧逻辑只看锚点成员，
	//    而不是「任意设备触发该字段」（后者是静默乱触发）。
	anchor, err := c.gsvc.AnchorEdgeDeviceID(composite.ID)
	if err != nil {
		t.Fatalf("AnchorEdgeDeviceID: %v", err)
	}
	if anchor != c.edgeA.ID {
		t.Fatalf("锚点 = %d, want %d（priority DESC 首成员）", anchor, c.edgeA.ID)
	}
	if err := c.db.Model(&models.AutomationRule{}).Where("id = ?", ruleC.ID).
		Updates(map[string]interface{}{
			"source_kind":            "source_group",
			"source_group_id":        composite.ID,
			"trigger_edge_device_id": anchor,
		}).Error; err != nil {
		t.Fatalf("bind ruleC: %v", err)
	}

	// ⑤ 启用三条规则并重载求值器缓存。
	if err := c.db.Model(&models.AutomationRule{}).
		Where("id IN ?", []uint{ruleA.ID, ruleB.ID, ruleC.ID}).
		Update("enabled", true).Error; err != nil {
		t.Fatalf("enable rules: %v", err)
	}
	c.eval.Invalidate()

	// ⑥ 驱动：A=600 lx、B=800 lx，两台都超阈值（>500）。
	c.publish(t, c.edgeA, prs3001Frame4B(0, 600), 3)
	c.publish(t, c.edgeB, prs3001Frame4B(0, 800), 4)

	// ⑦ 三条规则各恰好 1 条事件，且来源列可追溯。
	for name, rule := range map[string]*models.AutomationRule{"A": ruleA, "B": ruleB, "C": ruleC} {
		evs := c.events(t, rule.ID)
		if len(evs) != 1 {
			t.Errorf("规则%s(%s) 事件数 = %d, want 1", name, rule.Name, len(evs))
			continue
		}
		ev := evs[0]
		if ev.Result != models.AutomationResultNotification {
			t.Errorf("规则%s result = %q, want notification（detail=%s）", name, ev.Result, ev.Detail)
		}
		if ev.SourceEdgeDeviceID == nil || *ev.SourceEdgeDeviceID == 0 {
			t.Errorf("规则%s 事件缺 source_edge_device_id（不可追溯）", name)
		}
	}

	// ⑧ 复合规则：group_id + 成员快照 + 值为 avg(600,800)=700。
	cev := c.events(t, ruleC.ID)
	if len(cev) == 1 {
		if cev[0].SourceGroupID == nil || *cev[0].SourceGroupID != composite.ID {
			t.Errorf("规则C source_group_id = %v, want %d", cev[0].SourceGroupID, composite.ID)
		}
		if cev[0].MembersJSON == "" {
			t.Error("规则C 事件缺 members_json：复合来源不可追溯")
		}
		if cev[0].TriggerValue == nil {
			t.Error("规则C 事件缺 trigger_value")
		} else if v := *cev[0].TriggerValue; v < 699 || v > 701 {
			t.Errorf("规则C 触发值 = %v, want 700 左右（avg(600,800)）：说明没有逐成员取值再合并", v)
		}
	}
}
