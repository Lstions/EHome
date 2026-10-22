package automation

// M0（task-29）测试：窗口来源身份（S1/S2）、复合整组求值（S6）、
// F4 钉住触发时成员（FC-1/2/4/5）、事件来源审计列。
//
// 测试用真实 *sourceref.Registry（不是 mock）：快照结构与生产一致，
// 避免"测试替身与真实现漂移"（契约 SG-18 的唯一解析器要求）。

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/sourceref"
	"ehome/backend/pkg/metrics"
	"ehome/backend/pkg/parser"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
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
	// 正例：B 以 5s 节奏连续满足 60s（真实帧连续到达，无断流）。
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

	// 两段断言（Lead 2026-10-22 B-2）：①不触发/不落失败审计；②丢帧必须可见
	//（health 事件 + 指标）。丢帧是正常行为，但"静默"不是——这是 F-8 的核心。
	var healthCalls []string
	ev.SetSourceHealthFn(func(groupID uint, reason string) {
		healthCalls = append(healthCalls, fmt.Sprintf("%d|%s", groupID, reason))
	})
	metric := metrics.SourceFrameDiscardedTotal.WithLabelValues(frameDiscardNotActive)
	before := counterValue(t, metric)

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
	// ② health 事件：每次判定都通知（节流由 GroupService 侧做）。
	if len(healthCalls) != 1 {
		t.Fatalf("丢帧必须通知健康回调 1 次，got %d: %v", len(healthCalls), healthCalls)
	}
	if !strings.HasPrefix(healthCalls[0], "7|source_frame_discarded") ||
		!strings.Contains(healthCalls[0], frameDiscardNotActive) {
		t.Fatalf("健康事件应含 group=7 + source_frame_discarded:not_active_member，got %q", healthCalls[0])
	}
	// ② 指标：每帧 +1（counter 不写库、每帧累加）。
	if got := counterValue(t, metric); got != before+1 {
		t.Fatalf("source_frame_discarded_total{reason=%s} 应 +1，before=%v got=%v",
			frameDiscardNotActive, before, got)
	}
}

// 指标存在性断言：名字与有界 label 必须与方案 §12.9.3 R-9 一致
//（ehome_source_frame_discarded_total{reason}），防止后续改名导致仪表盘/D4 验收静默失配。
func TestM0SourceFrameDiscardedMetricExists(t *testing.T) {
	c := metrics.SourceFrameDiscardedTotal.WithLabelValues(frameDiscardGroupMissing)
	desc := c.Desc().String()
	if !strings.Contains(desc, "ehome_source_frame_discarded_total") {
		t.Fatalf("指标名不符（期望 ehome_source_frame_discarded_total）: %s", desc)
	}
	if !strings.Contains(desc, "reason") {
		t.Fatalf("指标缺少 reason label（应为有界枚举）: %s", desc)
	}
	before := counterValue(t, c)
	c.Inc()
	if got := counterValue(t, c); got != before+1 {
		t.Fatalf("counter 不递增: before=%v got=%v", before, got)
	}
}

// counterValue 读取 counter 当前值（与 internal/downlink 的测试 helper 同款：
// 用 client_model dto 而不是 prometheus/testutil，避免引入第二套读法）。
func counterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	ch := make(chan prometheus.Metric, 1)
	c.Collect(ch)
	var out dto.Metric
	if err := (<-ch).Write(&out); err != nil {
		t.Fatalf("read counter: %v", err)
	}
	return out.GetCounter().GetValue()
}

// H-3（Lead 2026-10-22）：非法 source_kind 只 Warn + 计数，**不跳过、不 fail-closed**，
// 求值仍按老路 TriggerEdgeDeviceID。判据三条：加载不丢、求值仍生效、指标 +1。
func TestM0InvalidSourceKindWarnedButLoadsAndEvaluates(t *testing.T) {
	db := newTestDB(t)
	h := &captureHandler{}
	kind := "dataset" // 未知取值（脏数据）
	r := automationRule(1, "temperature", "gt", 40, 0)
	r.CooldownSec = 0
	r.SourceKind = &kind
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	metric := metrics.RuleInvalidSourceKindTotal.WithLabelValues("automation")
	before := counterValue(t, metric)

	ev := NewEvaluator(db, h) // LoadRules 内 Warn+Inc；不 panic、不跳过
	if got := counterValue(t, metric); got != before+1 {
		t.Fatalf("非法 source_kind 必须使 ehome_rule_invalid_source_kind_total{kind=automation} +1，before=%v got=%v",
			before, got)
	}
	if _, ok := ev.rules[1]; !ok {
		t.Fatal("H-3：非法 source_kind 的规则不得被跳过（应保持加载）")
	}
	ev.Evaluate(1, []parser.Field{{Name: "temperature", Value: 50}}, time.Now())
	if len(h.events) != 1 {
		t.Fatalf("H-3：非法 source_kind 必须回落老路（TriggerEdgeDeviceID）继续求值，got %d 次触发", len(h.events))
	}
}

// ── 窗口截断仍成立：跨长断流由 durationSec cutoff 自然处理 ────────────────
//
// S8（gap 上限）已按 ADR 删除：它用配置 IntervalMs 判实际 gap，稀疏上报时会反复
// 截断窗口 ⇒ 存量 DurationSec>0 规则可能永不触发（false negative 且无指标）。
// 删除后窗口仍由 durationSec cutoff 截断（保留 cutoff 前最后一个样本作为起点），
// 本用例钉住这一行为，防止将来误以为"断流没人处理"而重新引入 gap 判据。
func TestM0WindowTruncatedByDurationCutoff(t *testing.T) {
	db := newTestDB(t)
	h := &captureHandler{}
	r := automationRule(1, "illuminance", "gt", 50, 60)
	r.CooldownSec = 0
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	ev := NewEvaluator(db, h)

	base := time.Now()
	ev.Evaluate(1, illuminanceField(60), base)                     // t0
	ev.Evaluate(1, illuminanceField(60), base.Add(5*time.Second))  // t5
	ev.Evaluate(1, illuminanceField(60), base.Add(100*time.Second)) // t100（断流 95s）

	win := ev.windows[1]
	if len(win) != 2 {
		t.Fatalf("窗口应按 durationSec cutoff 截断为 2 条（t5 起点 + t100），got %d: %+v", len(win), win)
	}
	// cutoff = t100-60s = t40 ⇒ t0 被裁掉；"保留 cutoff 前最后一个样本"= t5 成为起点。
	if !win[0].at.Equal(base.Add(5 * time.Second)) {
		t.Fatalf("窗口起点应为 cutoff 前最后一个样本 t5，got %v", win[0].at)
	}
	if !win[1].at.Equal(base.Add(100 * time.Second)) {
		t.Fatalf("窗口末样本应为 t100，got %v", win[1].at)
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
	// 结果码收敛（models 5→3）：未接线不再单独占一个 result 码，映射为
	// unresolved_selector；原因字符串保留在 detail（诊断能力不丢）。
	if row.Result != ResultUnresolvedSelector {
		t.Fatalf("FC-4: 未接线 ⇒ %s，got %s (detail=%s)",
			ResultUnresolvedSelector, row.Result, row.Detail)
	}
	if !strings.Contains(row.Detail, "source_resolver_not_configured") {
		t.Fatalf("FC-4: detail 必须保留原因字符串 source_resolver_not_configured，got %q", row.Detail)
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

// ── Lead 2026-10-22：求值期降级必须写入 source_group_health（E-1/E-2 可见性）──

// 组件陈旧 ⇒ CombineGroup unbound(fail_closed) ⇒ 健康回调必须被调用（1 次判定 1 次）。
func TestM0SourceHealthCallbackOnCompositeStale(t *testing.T) {
	db := newTestDB(t)
	h := &captureHandler{}
	r := groupRule(1, string(models.SourceKindSourceGroup), 8)
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	reg := sourceref.NewRegistry()
	reg.Publish(compositeGroup(8, models.OnDegradeFailClosed, models.CombineAvg, 1, 101, 102))
	ev := NewEvaluator(db, h)
	ev.SetSourceResolver(reg)

	var calls []string
	ev.SetSourceHealthFn(func(groupID uint, reason string) {
		calls = append(calls, fmt.Sprintf("%d|%s", groupID, reason))
	})
	now := time.Now()
	ev.SetLatestValueBySensorFn(func(deviceID uint, sensorName string) (models.UnifiedData, bool) {
		ts := now
		if deviceID == 102 {
			ts = now.Add(-time.Hour) // B 的 last_success 陈旧 > staleness(300s)
		}
		return models.UnifiedData{SensorName: sensorName, Value: 60, Timestamp: ts}, true
	})

	ev.Evaluate(101, illuminanceField(65), now)
	if len(h.events) != 0 {
		t.Fatalf("fail_closed + 组件陈旧不得触发，got %d", len(h.events))
	}
	if len(calls) != 1 {
		t.Fatalf("健康回调必须恰被调用 1 次（每次判定 1 次），got %d: %v", len(calls), calls)
	}
	if !strings.HasPrefix(calls[0], "8|source_degraded") {
		t.Fatalf("健康回调参数应为 group=8 + source_degraded 类别，got %q", calls[0])
	}
	var n int64
	db.Model(&models.AutomationEvent{}).Where("rule_id = ? AND result = ?", 1, ResultSourceDegraded).Count(&n)
	if n == 0 {
		t.Fatal("审计事件仍必须落库（与健康回调同源判定）")
	}
}

// 组不存在/未启用（求值期 member_changed 类）⇒ 健康回调也必须可见。
func TestM0SourceHealthCallbackOnGroupMissing(t *testing.T) {
	db := newTestDB(t)
	h := &captureHandler{}
	r := groupRule(1, string(models.SourceKindSourceGroup), 9) // 未发布快照
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	ev := NewEvaluator(db, h)
	ev.SetSourceResolver(sourceref.NewRegistry())
	var calls []string
	ev.SetSourceHealthFn(func(groupID uint, reason string) {
		calls = append(calls, fmt.Sprintf("%d|%s", groupID, reason))
	})
	ev.Evaluate(101, illuminanceField(65), time.Now())
	// 组缺失同时产生两条健康证据（B-2 后）：①丢帧级 source_frame_discarded
	//（F-8/R-9：运维看"组在不在供数"）；②类别级 source_member_changed
	//（前一版要求的"为什么不可用"）。两者由 GroupService 各自按 reason 节流。
	if len(calls) != 2 {
		t.Fatalf("组缺失应通知健康回调 2 次（discard + 类别），got %d: %v", len(calls), calls)
	}
	joined := strings.Join(calls, ",")
	if !strings.Contains(joined, "9|source_frame_discarded:group_missing") {
		t.Fatalf("缺丢帧级健康证据，got %v", calls)
	}
	if !strings.Contains(joined, "9|source_member_changed:group_missing") {
		t.Fatalf("缺类别级健康证据，got %v", calls)
	}
}

// F4 失败（证据过期）同样属于"组来源不可用"，planner 侧健康回调必须收到。
func TestM0F4SourceHealthCallbackOnStale(t *testing.T) {
	at := time.Now()
	p, r, _ := setupSourceF4(t, 101, 101, at, true)
	var calls []string
	p.SetSourceHealthFn(func(groupID uint, reason string) {
		calls = append(calls, fmt.Sprintf("%d|%s", groupID, reason))
	})
	ev := newTriggerEvent(r, at, 60, "", &triggerSource{
		kind: string(models.SourceKindSourceGroup), groupID: 7, edgeDeviceID: 101,
		sourceAt: at.Add(-10 * time.Minute),
	})
	p.HandleTrigger(ev)
	if len(calls) != 1 {
		t.Fatalf("F4 证据过期必须通知健康回调 1 次，got %d: %v", len(calls), calls)
	}
	if !strings.HasPrefix(calls[0], "7|source_stale") {
		t.Fatalf("健康回调应带 source_stale 类别与组 ID，got %q", calls[0])
	}
}

