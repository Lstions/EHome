p = 'internal/e2e/source_group_fullchain_e2_test.go'
s = open(p, encoding='utf-8').read()
start = s.index('	// ① 两台都有新鲜值：sum = 600+800 = 1400 > 500 ⇒ 触发。')
end = s.index('	// ④ 用 sourceref 纯函数复算同语义，确认「机制层」与「链路层」结论一致。')
new = '''	// ① 两台都有新鲜值。
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

'''
s = s[:start] + new + s[end:]
# ④ 段的变量名冲突修正
s = s.replace('''	snap, ok := c.reg.Group(group.ID)
	if !ok || snap == nil {
		t.Fatal("组快照缺失")
	}
	res := sourceref.CombineGroup(snap, func(ms sourceref.MemberSnapshot) sourceref.Component {
		return sourceref.Component{State: sourceref.StateStale, Value: 12345}
	})
	if res.OK {
		t.Errorf("全部分量陈旧时 CombineGroup 仍返回 OK（value=%v）—— 纯函数与链路口径不一致", res.Value)
	}''', '''	finalSnap, ok := c.reg.Group(group.ID)
	if !ok || finalSnap == nil {
		t.Fatal("组快照缺失")
	}
	allStale := sourceref.CombineGroup(finalSnap, func(ms sourceref.MemberSnapshot) sourceref.Component {
		return sourceref.Component{State: sourceref.StateStale, Value: 12345}
	})
	if allStale.OK {
		t.Errorf("全部分量陈旧时 CombineGroup 仍返回 OK（value=%v）—— 纯函数与链路口径不一致", allStale.Value)
	}''')
# 需要 api import
s = s.replace('''	"ehome/backend/internal/datasource"''', '''	"ehome/backend/internal/api"
	"ehome/backend/internal/datasource"''')
open(p, 'w', encoding='utf-8').write(s)
print('E3 rewritten')
