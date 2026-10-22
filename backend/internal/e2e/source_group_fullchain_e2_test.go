//go:build e2e

package e2e

// 全链路用例 E2/E3：主备切换跟随 + 复合缺员 unbound。
//
// E2 回答「active 变了，规则会不会自动跟」——这是本方案最核心的产品价值，
// 也是最容易「看起来实现了其实没接线」的地方（active 在 DB、快照在内存、
// 求值在另一条路径上）。
// E3 回答「1+2 合成时 2 号挂了会怎样」——必须 unbound 而不是静默用 1 号单边。

import (
	"testing"
	"time"

	"ehome/backend/internal/api"
	"ehome/backend/internal/datasource"
	"ehome/backend/internal/models"
	"ehome/backend/internal/sourceref"
)

func TestSG_E2_PrimaryBackupFollowsActive(t *testing.T) {
	rule := &models.AutomationRule{
		Name: "主备光照保护", Enabled: false,
		TriggerType:       models.AutomationTriggerSensorThreshold,
		TriggerSensorName: "illuminance", TriggerComparator: "gt", TriggerThreshold: 500,
		ActionType: models.AutomationActionNotification,
	}
	c := newSGFullChain(t, rule)

	// 播种观测（V-5 前置条件）。
	c.publish(t, c.edgeA, prs3001Frame4B(0, 100), 1)
	c.publish(t, c.edgeB, prs3001Frame4B(0, 120), 2)

	// 建主备组：A 主（priority 100）、B 备（priority 50）。
	group, err := c.gsvc.CreateGroup(datasource.CreateGroupInput{
		Name: "主备光照源", Kind: models.SourceGroupKindPrimaryBackup, Category: "illuminance",
		StalenessSec: 300,
		Members: []datasource.GroupMemberInput{
			{EdgeDeviceID: c.edgeA.ID, Priority: 100},
			{EdgeDeviceID: c.edgeB.ID, Priority: 50},
		},
	})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	anchor, err := c.gsvc.AnchorEdgeDeviceID(group.ID)
	if err != nil {
		t.Fatalf("AnchorEdgeDeviceID: %v", err)
	}
	if err := c.db.Model(&models.AutomationRule{}).Where("id = ?", rule.ID).
		Updates(map[string]interface{}{
			"source_kind":            "source_group",
			"source_group_id":        group.ID,
			"trigger_edge_device_id": anchor,
		}).Error; err != nil {
		t.Fatalf("bind rule: %v", err)
	}
	if err := c.db.Model(&models.AutomationRule{}).Where("id = ?", rule.ID).
		Update("enabled", true).Error; err != nil {
		t.Fatalf("enable rule: %v", err)
	}
	c.eval.Invalidate()

	// 确认快照里 A 是 active。
	snap, ok := c.reg.Group(group.ID)
	if !ok || snap == nil {
		t.Fatal("组快照未发布：CreateGroup 后 Registry 里没有该组")
	}
	if snap.ActiveEdgeDeviceID() != c.edgeA.ID {
		t.Fatalf("active = %d, want %d（A 应是主）", snap.ActiveEdgeDeviceID(), c.edgeA.ID)
	}

	// ① 备成员帧**不得**驱动规则（主备语义：只认权威来源）。
	c.publish(t, c.edgeB, prs3001Frame4B(0, 900), 3)
	if evs := c.events(t, rule.ID); len(evs) != 0 {
		t.Fatalf("备成员帧驱动了规则（事件 %d 条）—— active 过滤没生效，DurationSec 会被跨成员拼接", len(evs))
	}

	// ② 主成员帧驱动规则（正常触发）。
	c.publish(t, c.edgeA, prs3001Frame4B(0, 600), 4)
	evs := c.events(t, rule.ID)
	if len(evs) != 1 {
		t.Fatalf("主成员帧事件数 = %d, want 1", len(evs))
	}
	if evs[0].SourceEdgeDeviceID == nil || *evs[0].SourceEdgeDeviceID != c.edgeA.ID {
		t.Fatalf("事件来源 = %v, want %d", evs[0].SourceEdgeDeviceID, c.edgeA.ID)
	}

	// ③ 切换：手动把 B 置为 active（走 service，不直插 DB）。
	if _, err := c.gsvc.ActivateGroupMember(group.ID, groupMemberID(t, c, group.ID, c.edgeB.ID)); err != nil {
		t.Fatalf("ActivateGroupMember: %v", err)
	}
	snap, _ = c.reg.Group(group.ID)
	if snap == nil || snap.ActiveEdgeDeviceID() != c.edgeB.ID {
		t.Fatalf("切换后 active = %v, want %d（快照没刷新 ⇒ 规则读不到新来源）", snap,
			c.edgeB.ID)
	}

	// ④ 切换后：新 active（B）帧驱动，旧 active（A）帧被丢弃。
	c.publish(t, c.edgeA, prs3001Frame4B(0, 700), 5) // A 已非 active
	if got := len(c.events(t, rule.ID)); got != 1 {
		t.Fatalf("旧 active 帧驱动了规则（事件 %d 条，want 1）—— 切换后仍在用旧来源", got)
	}
	c.publish(t, c.edgeB, prs3001Frame4B(0, 800), 6)
	evs = c.events(t, rule.ID)
	if len(evs) < 2 {
		t.Fatalf("切换后新 active 帧未触发（事件 %d 条）—— 「跟 active」没落地", len(evs))
	}
	last := evs[len(evs)-1]
	if last.SourceEdgeDeviceID == nil || *last.SourceEdgeDeviceID != c.edgeB.ID {
		t.Fatalf("切换后事件来源 = %v, want %d", last.SourceEdgeDeviceID, c.edgeB.ID)
	}

	// ⑤ 不双计：同一次物理变化（每次只上报一帧）不应产生重复事件。
	// 上面共 2 次有效触发（A 一次、B 一次），不允许出现第 3 条。
	if len(evs) != 2 {
		t.Errorf("事件总数 = %d, want 2（A 一次 + B 一次，不得双计）", len(evs))
	}
}

// groupMemberID 按 (组, 设备) 找成员行 id（service 只暴露成员列表，不暴露单查）。
func groupMemberID(t *testing.T, c *sgFullChain, groupID, edgeDeviceID uint) uint {
	t.Helper()
	members, err := c.gsvc.ListMembers(groupID)
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	for _, m := range members {
		if m.EdgeDeviceID == edgeDeviceID {
			return m.ID
		}
	}
	t.Fatalf("组 %d 里没有设备 %d 的成员行", groupID, edgeDeviceID)
	return 0
}

func TestSG_E3_CompositeMissingMemberUnbound(t *testing.T) {
	rule := &models.AutomationRule{
		Name: "1+2合成(严格)", Enabled: false,
		TriggerType:       models.AutomationTriggerSensorThreshold,
		TriggerSensorName: "illuminance", TriggerComparator: "gt", TriggerThreshold: 500,
		ActionType: models.AutomationActionNotification,
	}
	c := newSGFullChain(t, rule)

	c.publish(t, c.edgeA, prs3001Frame4B(0, 100), 1)
	c.publish(t, c.edgeB, prs3001Frame4B(0, 120), 2)

	// sum + min_members=2：两台都必须有新鲜值，缺一即 unbound。
	group, err := c.gsvc.CreateGroup(datasource.CreateGroupInput{
		Name: "1+2合成(sum,严格)", Kind: models.SourceGroupKindComposite, Category: "illuminance",
		Combine: models.CombineSum, MinMembers: 2, StalenessSec: 300,
		OnDegrade: models.OnDegradeFailClosed,
		Members: []datasource.GroupMemberInput{
			{EdgeDeviceID: c.edgeA.ID, Priority: 100},
			{EdgeDeviceID: c.edgeB.ID, Priority: 90},
		},
	})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	// cooldown_sec=300 是**生产合法配置**（方案 F-3：source_group 规则的冷却下限由
	// 成员采集间隔推导，cooldown=0 会被保存期 400 拒绝）。这里必须用合法值，否则
	// 测的是一条生产上不可能存在的路径 —— 而且复合组有 N 个成员帧，cooldown=0 时
	// 同一次物理变化会被执行 N 次（这正是 N-5 要求硬拒 cooldown=0 的原因）。
	if err := c.db.Model(&models.AutomationRule{}).Where("id = ?", rule.ID).
		Updates(map[string]interface{}{
			"source_kind":     "source_group",
			"source_group_id": group.ID,
			"cooldown_sec":    300,
		}).Error; err != nil {
		t.Fatalf("bind rule: %v", err)
	}
	if err := c.db.Model(&models.AutomationRule{}).Where("id = ?", rule.ID).
		Update("enabled", true).Error; err != nil {
		t.Fatalf("enable: %v", err)
	}
	c.eval.Invalidate()

	// ① 两台都有新鲜值。
	//
	// 关键语义（实测得到，不是设计猜想）：**复合组由任一成员帧驱动整组求值**，
	// 而"整组"取的是各成员**当前最新缓存值**。所以 A 先报 600 时，B 的最新值
	// 还是播种的 120 ⇒ 第一次求值是 sum(600,120)=720；B 再报 800 后才变成 1400。
	// 这不是缺陷 —— 它正是「复合来源 = 由成员共同供数」的直接体现，也让
	// 「每个成员帧都可能改变组值」这件事成为可观测事实。
	//
	// ⚠ 这里用 notification 事件而不是全部事件：复合组的第二帧落在冷却期内会产生
	// result=suppressed_cooldown 的审计记录（正确行为 —— 冷却压制必须可观测）。
	// ⚠ cooldown_sec=300 是生产合法值（方案 F-3：source_group 规则冷却下限由成员
	// 采集间隔推导；cooldown=0 会被保存期 400 拒绝，因为 N 个成员帧会把同一次
	// 物理变化执行 N 次 —— 这正是 N-5 的结论）。
	notifications := func() []models.AutomationEvent {
		var out []models.AutomationEvent
		for _, ev := range c.events(t, rule.ID) {
			if ev.Result == models.AutomationResultNotification {
				out = append(out, ev)
			}
		}
		return out
	}

	c.publish(t, c.edgeA, prs3001Frame4B(0, 600), 3)
	evs := notifications()
	if len(evs) != 1 {
		t.Fatalf("A 上报后 notification 事件数 = %d, want 1", len(evs))
	}
	// 第一次求值 = sum(600, B 播种值 120) = 720。
	if evs[0].TriggerValue == nil {
		t.Fatal("复合事件缺 trigger_value")
	} else if v := *evs[0].TriggerValue; v != 720 {
		t.Errorf("首次复合值 = %v, want 720（sum(A 新值 600, B 播种值 120)）", v)
	}
	if evs[0].MembersJSON == "" {
		t.Error("复合事件缺 members_json：无法看出这次用了哪些成员值")
	}

	// ② B 上报 800 ⇒ 组值变 1400，但冷却期内不得再次通知（只落 suppressed 审计）。
	c.publish(t, c.edgeB, prs3001Frame4B(0, 800), 4)
	if got := len(notifications()); got != 1 {
		t.Errorf("冷却期内 notification 事件数 = %d, want 1（不得双计）", got)
	}
	// 但复合值必须真的更新到 1400 —— 用纯函数在同一快照上复算，确认缓存与合并口径一致。
	snapNow, _ := c.reg.Group(group.ID)
	if snapNow == nil {
		t.Fatal("组快照缺失")
	}
	res := sourceref.CombineGroup(snapNow, func(ms sourceref.MemberSnapshot) sourceref.Component {
		rec, ok := api.LatestValueBySensor(ms.EdgeDeviceID, "illuminance")
		if !ok {
			return sourceref.Component{State: sourceref.StateMissing}
		}
		return sourceref.Component{State: sourceref.StateOK, Value: rec.Value, At: rec.Timestamp}
	})
	if !res.OK {
		t.Fatalf("两台都有值时 CombineGroup 未 OK: reason=%s", res.Reason)
	}
	if res.Value != 1400 {
		t.Errorf("合并值 = %v, want 1400（sum(600,800)）", res.Value)
	}

	// ③ 让 B 陈旧：把它的 last_success 推到 2 小时前，重建快照后 A 再上报。
	//    fail_closed 下必须 **不触发**，且落降级审计（不能静默单边）。
	old := time.Now().Add(-2 * time.Hour)
	if err := c.db.Model(&models.SourceGroupMember{}).
		Where("group_id = ? AND edge_device_id = ?", group.ID, c.edgeB.ID).
		Update("last_success", old).Error; err != nil {
		t.Fatalf("age member B: %v", err)
	}
	if err := c.gsvc.PublishAll(); err != nil {
		t.Fatalf("PublishAll: %v", err)
	}

	before := len(notifications())
	c.publish(t, c.edgeA, prs3001Frame4B(0, 700), 5)
	if after := len(notifications()); after != before {
		t.Fatalf("B 陈旧时仍然通知了（%d → %d）—— 静默单边，违反 fail_closed", before, after)
	}
	var n int64
	if err := c.db.Model(&models.SourceGroupHealth{}).
		Where("group_id = ?", group.ID).Count(&n).Error; err != nil {
		t.Fatalf("count health: %v", err)
	}
	if n == 0 {
		t.Error("成员陈旧导致 unbound，但组健康表没有任何记录 —— 静默失效，运维无从发现")
	}

	// ④ 用 sourceref 纯函数复算同语义，确认「机制层」与「链路层」结论一致。
	finalSnap, ok := c.reg.Group(group.ID)
	if !ok || finalSnap == nil {
		t.Fatal("组快照缺失")
	}
	allStale := sourceref.CombineGroup(finalSnap, func(ms sourceref.MemberSnapshot) sourceref.Component {
		return sourceref.Component{State: sourceref.StateStale, Value: 12345}
	})
	if allStale.OK {
		t.Errorf("全部分量陈旧时 CombineGroup 仍返回 OK（value=%v）—— 纯函数与链路口径不一致", allStale.Value)
	}
}
