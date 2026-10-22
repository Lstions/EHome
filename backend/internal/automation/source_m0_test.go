package automation

// M0（task-29）测试：窗口来源身份（S1/S2/S8）、复合整组求值（S6）、
// F4 钉住触发时成员（FC-1/2/4/5）、事件来源审计列。
//
// 测试用真实 *sourceref.Registry（不是 mock）：快照结构与生产一致，
// 避免"测试替身与真实现漂移"（契约 SG-18 的唯一解析器要求）。

import (
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/sourceref"
	"ehome/backend/pkg/parser"
)

// backupGroup 构造主备组快照（activeMemberID 对应 memberID，activeEdge 是其物理设备）。
func backupGroup(id, activeMemberID, activeEdge, standbyEdge uint, rev int64, seq uint64) *sourceref.GroupSnapshot {
	members := []sourceref.MemberSnapshot{
		{MemberID: 11, EdgeDeviceID: activeEdge, Priority: 20, Status: models.SourceMemberStatusActive},
		{MemberID: 12, EdgeDeviceID: standbyEdge, Priority: 10, Status: models.SourceMemberStatusStandby},
	}
	// active 换绑时同步 member 状态，快照语义与 datasource 发布保持一致。
	if activeMemberID == 12 {
		// 换绑：member 11 的物理设备变为 standbyEdge，member 12 变为 activeEdge。
		members[0].Status = models.SourceMemberStatusStandby
		members[0].EdgeDeviceID = standbyEdge
		members[1].Status = models.SourceMemberStatusActive
		members[1].EdgeDeviceID = activeEdge
	}
	sourceref.SortMembers(members)
	return &sourceref.GroupSnapshot{
		ID: id, Kind: models.SourceGroupKindPrimaryBackup, Category: "illuminance",
		Enabled: true, Revision: rev, TransitionSeq: seq, ActiveMemberID: activeMemberID,
		Members: members, MembersHash: sourceref.ComputeMembersHash(members),
	}
}

// compositeGroup 构造复合组快照。
func compositeGroup(id uint, onDegrade, combine string, minMembers int, edges ...uint) *sourceref.GroupSnapshot {
	members := make([]sourceref.MemberSnapshot, 0, len(edges))
	for i, edge := range edges {
		members = append(members, sourceref.MemberSnapshot{
			MemberID: uint(21 + i), EdgeDeviceID: edge, Priority: 10, Status: models.SourceMemberStatusActive,
		})
	}
	sourceref.SortMembers(members)
	return &sourceref.GroupSnapshot{
		ID: id, Kind: models.SourceGroupKindComposite, Category: "illuminance",
		Combine: combine, MinMembers: minMembers, OnDegrade: onDegrade,
		StalenessSec: 300, Enabled: true, Revision: 1,
		Members: members, MembersHash: sourceref.ComputeMembersHash(members),
	}
}

// groupRule 给规则挂上来源组绑定（两列指针，NULL 与 0 等价语义由 models 判据负责）。
func groupRule(id uint, kind string, groupID uint) models.AutomationRule {
	r := automationRule(id, "illuminance", "gt", 50, 0)
	r.CooldownSec = 0
	r.SourceKind = &kind
	r.SourceGroupID = &groupID
	return r
}

// ── S1：primary_backup 窗口不得跨成员拼接 ─────────────────────────────────

func TestM0SourceWindowNoCrossMemberSplice(t *testing.T) {
	db := newTestDB(t)
	h := &captureHandler{}
	r := groupRule(1, string(models.SourceKindSourceGroup), 7)
	r.TriggerDurationSec = 60
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	reg := sourceref.NewRegistry()
	reg.Publish(backupGroup(7, 11, 101, 102, 1, 1))

	ev := NewEvaluator(db, h)
	ev.SetSourceResolver(reg)
	ev.SetDeviceIntervalFn(func(uint) time.Duration { return 5 * time.Second })

	base := time.Now()
	// A 连续两帧（都在 active 期）。
	ev.Evaluate(101, illuminanceField(60), base)
	ev.Evaluate(101, illuminanceField(60), base.Add(30*time.Second))
	// 切到 B（revision/transitionSeq 变化）。
	reg.Publish(backupGroup(7, 12, 102, 101, 2, 2))
	// 关键三帧：A 已非 active（丢弃）→ B 从 base+61 起新段。
	ev.Evaluate(101, illuminanceField(60), base.Add(61*time.Second))
	ev.Evaluate(102, illuminanceField(60), base.Add(61*time.Second))

	if len(h.events) != 0 {
		t.Fatalf("S1: 主备跨成员拼接不得触发（A t0/t30 + B t61 不得拼成 60s 连续），got %d 次", len(h.events))
	}
	// 正例：B 以 5s 节奏连续满足 60s（与采集间隔一致，S8 不误伤）。
	for s := 66; s <= 121; s += 5 {
		ev.Evaluate(102, illuminanceField(60), base.Add(time.Duration(s)*time.Second))
	}
	if len(h.events) != 1 {
		t.Fatalf("S2: 同 identity 连续满足 60s 必须触发一次，got %d", len(h.events))
	}
	if h.events[0].SourceKind != string(models.SourceKindSourceGroup) || h.events[0].SourceGroupID != 7 ||
		h.events[0].SourceEdgeDeviceID != 102 {
		t.Fatalf("触发证据必须指向来源组与触发成员: %+v", h.events[0])
	}
}

// ── 主备组备成员帧必须丢弃（不写窗口）────────────────────────────────────

func TestM0StandbyFrameDiscarded(t *testing.T) {
	db := newTestDB(t)
	h := &captureHandler{}
	r := groupRule(1, string(models.SourceKindSourceGroup), 7)
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	reg := sourceref.NewRegistry()
	reg.Publish(backupGroup(7, 11, 101, 102, 1, 1))
	ev := NewEvaluator(db, h)
	ev.SetSourceResolver(reg)

	ev.Evaluate(102, illuminanceField(60), time.Now()) // 备成员，非 active
	if len(h.events) != 0 {
		t.Fatalf("备成员帧不得驱动求值，got %d", len(h.events))
	}
	var n int64
	db.Model(&models.AutomationEvent{}).Where("rule_id = ? AND result = ?", 1,
		ResultSourceMemberChanged).Count(&n)
	if n != 0 {
		t.Fatalf("备成员帧是正常丢弃，不应落失败审计，got %d", n)
	}
}

// ── S8：跨长断流不得拼接（直接断言窗口起点）──────────────────────────────

func TestM0S8GapDropsStaleWindowStart(t *testing.T) {
	db := newTestDB(t)
	h := &captureHandler{}
	r := automationRule(1, "illuminance", "gt", 50, 600)
	r.CooldownSec = 0
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	ev := NewEvaluator(db, h)
	ev.SetDeviceIntervalFn(func(uint) time.Duration { return 5 * time.Second }) // maxGap=10s

	base := time.Now()
	ev.Evaluate(1, illuminanceField(60), base)
	ev.Evaluate(1, illuminanceField(60), base.Add(5*time.Second))
	ev.Evaluate(1, illuminanceField(60), base.Add(100*time.Second)) // 断流 95s > 10s

	win := ev.windows[1]
	if len(win) == 0 {
		t.Fatal("窗口不应为空")
	}
	if !win[0].at.Equal(base.Add(100 * time.Second)) {
		t.Fatalf("S8: 断流前的样本不得作为窗口起点，got start=%v want %v",
			win[0].at, base.Add(100*time.Second))
	}
	if len(win) != 1 {
		t.Fatalf("S8: 断流前样本应全部丢弃，got %d 条", len(win))
	}
}

// ── S6：复合真值表（fail_closed / use_available）──────────────────────────

func TestM0CompositeTruthTable(t *testing.T) {
	db := newTestDB(t)
	h := &captureHandler{}
	r := groupRule(1, string(models.SourceKindSourceGroup), 8)
	r.TriggerThreshold = 60
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	reg := sourceref.NewRegistry()
	reg.Publish(compositeGroup(8, models.OnDegradeFailClosed, models.CombineAvg, 1, 101, 102))
	ev := NewEvaluator(db, h)
	ev.SetSourceResolver(reg)
	now := time.Now()
	values := map[uint]float64{101: 40, 102: 90}
	ev.SetLatestValueBySensorFn(func(deviceID uint, sensorName string) (models.UnifiedData, bool) {
		v, ok := values[deviceID]
		return models.UnifiedData{SensorName: sensorName, Value: v, Timestamp: now}, ok
	})

	// 全部可用：avg(40,90)=65 > 60 ⇒ 触发，值=65。
	ev.Evaluate(101, illuminanceField(65), now)
	if len(h.events) != 1 {
		t.Fatalf("复合 avg 全可用应触发，got %d", len(h.events))
	}
	if h.events[0].Value != 65 {
		t.Fatalf("复合触发值应为合并值 65，got %v", h.events[0].Value)
	}
	if kind := h.events[0].SourceKind; kind != string(models.SourceKindSourceGroup) {
		t.Fatalf("复合触发证据 kind=%q", kind)
	}

	// fail_closed + 成员缺席 ⇒ unbound（不触发 + 审计 source_degraded）。
	h.events = nil
	delete(values, 102)
	ev.Evaluate(101, illuminanceField(65), now.Add(time.Minute))
	if len(h.events) != 0 {
		t.Fatalf("fail_closed 缺员不得触发，got %d", len(h.events))
	}
	var n int64
	db.Model(&models.AutomationEvent{}).Where("rule_id = ? AND result = ?", 1, ResultSourceDegraded).Count(&n)
	if n == 0 {
		t.Fatal("fail_closed 缺员必须落 source_degraded 审计")
	}

	// use_available + 仅缺非 required 成员 ⇒ 允许触发且 Degraded 审计。
	reg.Publish(compositeGroup(8, models.OnDegradeUseAvailable, models.CombineAvg, 1, 101, 102))
	h.events = nil
	values[101] = 70 // 可用成员值需满足阈值 60，才能验证"降级仍允许触发"
	ev.Evaluate(101, illuminanceField(65), now.Add(2*time.Minute))
	if len(h.events) != 1 {
		t.Fatalf("use_available 仅缺非必选成员应允许触发，got %d", len(h.events))
	}
	if h.events[0].Value != 70 {
		t.Fatalf("use_available 应按可用成员合并（70），got %v", h.events[0].Value)
	}
	db.Model(&models.AutomationEvent{}).Where("rule_id = ? AND result = ?", 1, ResultSourceDegraded).Count(&n)
	if n < 2 {
		t.Fatalf("use_available 降级必须留审计（含前面 fail_closed 的一条），got %d", n)
	}
}

// ── F4：钉住触发时成员（FC-1/FC-2/FC-4）+ 审计列 ──────────────────────────

func setupSourceF4(t *testing.T, activeEdge uint, evEdge uint, sourceAt time.Time, withResolver bool) (*Planner, models.AutomationRule, *sourceref.Registry) {
	t.Helper()
	db := newTestDB(t)
	kind := string(models.SourceKindSourceGroup)
	gid := uint(7)
	r := automationRule(1, "illuminance", "gt", 50, 0)
	r.SourceKind = &kind
	r.SourceGroupID = &gid
	r.ActionType = models.AutomationActionDeviceAction
	r.ActionDeviceID = 1
	r.ActionID = "set_mos_policy"
	r.ActionParamsJSON = "{}"
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.AutomationRuleAction{
		RuleID: 1, Seq: 1, ActionType: models.AutomationActionDeviceAction,
		ActionDeviceID: 1, ActionID: "set_mos_policy", ActionParamsJSON: "{}", Enabled: true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	reg := sourceref.NewRegistry()
	reg.Publish(backupGroup(7, 11, activeEdge, 102, 2, 2))
	p := NewPlanner(db, nil, nil, 7)
	p.nowFn = func() time.Time { return sourceAt.Add(10 * time.Second) }
	p.SetLatestValueBySensorFn(func(deviceID uint, sensorName string) (models.UnifiedData, bool) {
		return models.UnifiedData{SensorName: sensorName, Value: 60, Timestamp: sourceAt}, true
	})
	if withResolver {
		p.SetSourceResolver(reg)
	}
	return p, r, reg
}

func TestM0F4MemberChangedFailClosed(t *testing.T) {
	at := time.Now()
	// 组当前 active=102，但触发证据是 101（触发后切走）。
	p, r, _ := setupSourceF4(t, 102, 101, at, true)
	ev := newTriggerEvent(r, at, 60, "", &triggerSource{
		kind: string(models.SourceKindSourceGroup), groupID: 7, edgeDeviceID: 101, sourceAt: at,
	})
	p.HandleTrigger(ev)

	var row models.AutomationEvent
	if err := p.db.Where("rule_id = ?", 1).Order("id DESC").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Result != ResultSourceMemberChanged {
		t.Fatalf("FC-1: 触发成员已非 active ⇒ %s，got %s (detail=%s)",
			ResultSourceMemberChanged, row.Result, row.Detail)
	}
	var cmds int64
	p.db.Model(&models.CommandExecution{}).Count(&cmds)
	if cmds != 0 {
		t.Fatalf("FC-1: 必须零下发，got %d", cmds)
	}
	if row.SourceKind == nil || *row.SourceKind != string(models.SourceKindSourceGroup) ||
		row.SourceGroupID == nil || *row.SourceGroupID != 7 ||
		row.SourceEdgeDeviceID == nil || *row.SourceEdgeDeviceID != 101 {
		t.Fatalf("审计列必须记录来源证据: %+v", row)
	}
}

func TestM0F4StaleEvidenceFailClosed(t *testing.T) {
	at := time.Now()
	p, r, _ := setupSourceF4(t, 101, 101, at, true)
	ev := newTriggerEvent(r, at, 60, "", &triggerSource{
		kind: string(models.SourceKindSourceGroup), groupID: 7, edgeDeviceID: 101,
		sourceAt: at.Add(-10 * time.Minute), // > staleness(300s)
	})
	p.HandleTrigger(ev)
	var row models.AutomationEvent
	if err := p.db.Where("rule_id = ?", 1).Order("id DESC").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Result != ResultSourceStale {
		t.Fatalf("FC-2: 证据过期 ⇒ %s，got %s", ResultSourceStale, row.Result)
	}
}

func TestM0F4ResolverNotConfiguredFailClosed(t *testing.T) {
	at := time.Now()
	p, r, _ := setupSourceF4(t, 101, 101, at, false) // 不注入 resolver
	ev := newTriggerEvent(r, at, 60, "", &triggerSource{
		kind: string(models.SourceKindSourceGroup), groupID: 7, edgeDeviceID: 101, sourceAt: at,
	})
	p.HandleTrigger(ev)
	var row models.AutomationEvent
	if err := p.db.Where("rule_id = ?", 1).Order("id DESC").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Result != ResultSourceResolverNotConfigured {
		t.Fatalf("FC-4: 未接线 ⇒ %s，got %s", ResultSourceResolverNotConfigured, row.Result)
	}
	var cmds int64
	p.db.Model(&models.CommandExecution{}).Count(&cmds)
	if cmds != 0 {
		t.Fatalf("FC-4: 必须零下发，got %d", cmds)
	}
}

// ── 存量规则行为不变（G-1）：不带来源证据的 Rule 走 legacy 路径 ─────────────

func TestM0LegacyRuleUnaffected(t *testing.T) {
	db := newTestDB(t)
	h := &captureHandler{}
	r := automationRule(1, "temperature", "gt", 40, 0)
	r.CooldownSec = 0
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	ev := NewEvaluator(db, h) // 不注入 sourceResolver
	ev.Evaluate(1, []parser.Field{{Name: "temperature", Value: 50}}, time.Now())
	if len(h.events) != 1 {
		t.Fatalf("存量规则不得因 M0 改造失效，got %d", len(h.events))
	}
	if h.events[0].SourceKind != "" || h.events[0].SourceGroupID != 0 {
		t.Fatalf("存量规则触发证据必须为空: %+v", h.events[0])
	}
}
