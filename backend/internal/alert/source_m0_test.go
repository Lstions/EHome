package alert

// M0（task-29）测试：告警侧窗口来源身份（S1/S7）、来源切换关闭陈旧 firing、
// 以及「Evaluate 不得持锁查 DB」（H2 类锁放大回归锁）。
//
// 用真实 *sourceref.Registry（与生产同结构），避免测试替身与实现漂移。

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/sourceref"
	"ehome/backend/pkg/metrics"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"gorm.io/gorm"
)

func backupGroupSnapshot(id, activeMemberID, activeEdge, standbyEdge uint, rev int64, seq uint64) *sourceref.GroupSnapshot {
	members := []sourceref.MemberSnapshot{
		{MemberID: 11, EdgeDeviceID: activeEdge, Priority: 20, Status: models.SourceMemberStatusActive},
		{MemberID: 12, EdgeDeviceID: standbyEdge, Priority: 10, Status: models.SourceMemberStatusStandby},
	}
	if activeMemberID == 12 {
		members[0].Status = models.SourceMemberStatusStandby
		members[0].EdgeDeviceID = standbyEdge
		members[1].Status = models.SourceMemberStatusActive
		members[1].EdgeDeviceID = activeEdge
	}
	sourceref.SortMembers(members)
	return &sourceref.GroupSnapshot{
		ID: id, Kind: models.SourceGroupKindPrimaryBackup, Category: "temperature",
		Enabled: true, Revision: rev, TransitionSeq: seq, ActiveMemberID: activeMemberID,
		Members: members, MembersHash: sourceref.ComputeMembersHash(members),
	}
}

func compositeGroupSnapshot(id uint, combine string) *sourceref.GroupSnapshot {
	members := []sourceref.MemberSnapshot{
		{MemberID: 21, EdgeDeviceID: 101, Priority: 10, Status: models.SourceMemberStatusActive},
		{MemberID: 22, EdgeDeviceID: 102, Priority: 10, Status: models.SourceMemberStatusActive},
	}
	sourceref.SortMembers(members)
	return &sourceref.GroupSnapshot{
		ID: id, Kind: models.SourceGroupKindComposite, Category: "temperature",
		Combine: combine, MinMembers: 1, OnDegrade: models.OnDegradeFailClosed,
		StalenessSec: 300, Enabled: true, Revision: 1,
		Members: members, MembersHash: sourceref.ComputeMembersHash(members),
	}
}

func groupAlertRule(id uint, groupID uint) models.AlertRule {
	r := rule(id, "temperature", "gt", 50, 60)
	kind := string(models.SourceKindSourceGroup)
	gid := groupID
	r.SourceKind = &kind
	r.SourceGroupID = &gid
	return r
}

// ── S1：主备组窗口不得跨成员拼接 ─────────────────────────────────────────

func TestM0AlertWindowNoCrossMemberSplice(t *testing.T) {
	db := newTestDB(t)
	r := groupAlertRule(1, 7)
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	reg := sourceref.NewRegistry()
	reg.Publish(backupGroupSnapshot(7, 11, 101, 102, 1, 1))
	ev := NewEvaluator(db, nil)
	ev.SetSourceResolver(reg)

	base := time.Now()
	ev.Evaluate(101, fields("temperature", 60), base)
	ev.Evaluate(101, fields("temperature", 60), base.Add(30*time.Second))
	reg.Publish(backupGroupSnapshot(7, 12, 102, 101, 2, 2))
	ev.Evaluate(101, fields("temperature", 60), base.Add(61*time.Second)) // 已非 active → 丢弃
	ev.Evaluate(102, fields("temperature", 60), base.Add(61*time.Second))
	if n := eventCount(db, 1); n != 0 {
		t.Fatalf("S1: A/B 跨成员拼接不得触发告警，got %d", n)
	}
	// 正例：B 连续满足 60s ⇒ firing，且事件带来源列。
	for s := 66; s <= 121; s += 5 {
		ev.Evaluate(102, fields("temperature", 60), base.Add(time.Duration(s)*time.Second))
	}
	if n := eventCount(db, 1); n != 1 {
		t.Fatalf("同 identity 连续满足 60s 必须触发一次，got %d", n)
	}
	var row models.AlertEvent
	if err := db.Where("rule_id = ?", 1).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.SourceKind == nil || *row.SourceKind != string(models.SourceKindSourceGroup) ||
		row.SourceGroupID == nil || *row.SourceGroupID != 7 ||
		row.SourceEdgeDeviceID == nil || *row.SourceEdgeDeviceID != 102 {
		t.Fatalf("告警事件必须写来源审计列: %+v", row)
	}
}

// ── S7：composite 身份不含触发成员 ⇒ A/B 交替不得打断 DurationSec ─────────

func TestM0AlertCompositeAlternatingFires(t *testing.T) {
	db := newTestDB(t)
	r := groupAlertRule(1, 8)
	// DurationSec=30：窗口 [now-30, now] 只留最近两点，交替成员跨出 30s 仍连续。
	// 若 identity 含触发成员（错误实现），两次采样会被清窗 ⇒ 永不满足。
	r.DurationSec = 30
	r.Threshold = 10
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	reg := sourceref.NewRegistry()
	reg.Publish(compositeGroupSnapshot(8, models.CombineAvg))
	ev := NewEvaluator(db, nil)
	ev.SetSourceResolver(reg)
	ev.SetLatestValueBySensorFn(func(deviceID uint, sensorName string) (models.UnifiedData, bool) {
		return models.UnifiedData{SensorName: sensorName, Value: 20, Timestamp: time.Now()}, true
	})

	base := time.Now()
	ev.Evaluate(101, fields("temperature", 20), base)                     // A
	ev.Evaluate(102, fields("temperature", 20), base.Add(20*time.Second)) // B
	ev.Evaluate(101, fields("temperature", 20), base.Add(35*time.Second)) // A
	ev.Evaluate(102, fields("temperature", 20), base.Add(65*time.Second)) // B
	// 最后两点 (35s,65s) 是不同成员，但组值连续满足 30s ⇒ 必须 firing（S7）。
	if n := eventCount(db, 1); n != 1 {
		t.Fatalf("S7: composite 身份不含触发成员，A/B 交替 + 组值持续满足 30s 必须触发，got %d", n)
	}
}

// ── S4：来源切换关闭陈旧 firing（就地 resolved + 不发"已恢复"通知）────────

func TestM0AlertSourceTransitionClosesStaleFiring(t *testing.T) {
	db := newTestDB(t)
	r := groupAlertRule(1, 7)
	r.DurationSec = 0
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	reg := sourceref.NewRegistry()
	reg.Publish(backupGroupSnapshot(7, 11, 101, 102, 1, 1))
	ev := NewEvaluator(db, nil)
	ev.SetSourceResolver(reg)

	now := time.Now()
	ev.Evaluate(101, fields("temperature", 60), now) // firing
	var n int64
	db.Model(&models.Notification{}).Count(&n)
	notificationsBefore := n

	reg.Publish(backupGroupSnapshot(7, 12, 102, 101, 2, 2))
	ev.OnSourceTransition(7)

	var row models.AlertEvent
	if err := db.Where("rule_id = ?", 1).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.State != stateResolved || row.ResolvedAt == nil {
		t.Fatalf("S4: 来源切换必须关闭陈旧 firing（就地 resolved），got state=%s", row.State)
	}
	db.Model(&models.Notification{}).Count(&n)
	if n != notificationsBefore {
		t.Fatalf("S4: 关闭陈旧 firing 不得发「已恢复」通知（notifications %d → %d）", notificationsBefore, n)
	}
	if ev.firing[1] {
		t.Fatal("S4: 内存 firing 状态必须清除")
	}
}

// ── Lead 2026-10-22：告警求值期降级也必须写 source_group_health ────────────

// 组件陈旧 ⇒ CombineGroup unbound(fail_closed) ⇒ 健康回调恰被调用 1 次。
func TestM0AlertSourceHealthCallbackOnCompositeStale(t *testing.T) {
	db := newTestDB(t)
	r := groupAlertRule(1, 8)
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	reg := sourceref.NewRegistry()
	reg.Publish(compositeGroupSnapshot(8, models.CombineAvg)) // fail_closed 默认
	ev := NewEvaluator(db, nil)
	ev.SetSourceResolver(reg)
	var calls []string
	ev.SetSourceHealthFn(func(groupID uint, reason string) {
		calls = append(calls, fmt.Sprintf("%d|%s", groupID, reason))
	})
	now := time.Now()
	ev.SetLatestValueBySensorFn(func(deviceID uint, sensorName string) (models.UnifiedData, bool) {
		ts := now
		if deviceID == 102 {
			ts = now.Add(-time.Hour) // 陈旧 > staleness(300s)
		}
		return models.UnifiedData{SensorName: sensorName, Value: 60, Timestamp: ts}, true
	})
	ev.Evaluate(101, fields("temperature", 60), now)
	if n := eventCount(db, 1); n != 0 {
		t.Fatalf("fail_closed + 组件陈旧不得告警，got %d", n)
	}
	if len(calls) != 1 {
		t.Fatalf("告警健康回调必须恰被调用 1 次，got %d: %v", len(calls), calls)
	}
	if !strings.HasPrefix(calls[0], "8|source_degraded") {
		t.Fatalf("健康回调参数应为 group=8 + source_degraded，got %q", calls[0])
	}
}

// ── B-2：告警侧丢帧可见性（F-8 / R-9）──────────────────────────────────────

// 备成员帧 = 正常丢弃，但必须可见：health 回调 1 次 + 指标每帧 +1。
func TestM0AlertStandbyFrameDiscardedObservable(t *testing.T) {
	db := newTestDB(t)
	r := groupAlertRule(1, 7)
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	reg := sourceref.NewRegistry()
	reg.Publish(backupGroupSnapshot(7, 11, 101, 102, 1, 1))
	ev := NewEvaluator(db, nil)
	ev.SetSourceResolver(reg)
	var calls []string
	ev.SetSourceHealthFn(func(groupID uint, reason string) {
		calls = append(calls, fmt.Sprintf("%d|%s", groupID, reason))
	})
	metric := metrics.SourceFrameDiscardedTotal.WithLabelValues(frameDiscardNotActive)
	before := counterValue(t, metric)

	ev.Evaluate(102, fields("temperature", 60), time.Now()) // 备成员帧

	if n := eventCount(db, 1); n != 0 {
		t.Fatalf("备成员帧不得触发告警，got %d", n)
	}
	if len(calls) != 1 {
		t.Fatalf("告警丢帧必须通知健康回调 1 次，got %d: %v", len(calls), calls)
	}
	if !strings.Contains(calls[0], "source_frame_discarded") ||
		!strings.Contains(calls[0], frameDiscardNotActive) {
		t.Fatalf("健康事件应含 source_frame_discarded:not_active_member，got %q", calls[0])
	}
	if got := counterValue(t, metric); got != before+1 {
		t.Fatalf("source_frame_discarded_total{reason=%s} 应 +1（alert 侧），before=%v got=%v",
			frameDiscardNotActive, before, got)
	}
}

// counterValue 读取 counter 当前值（与 automation 侧同款 helper，包内私有副本）。
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

// H-3（Lead 2026-10-22）：告警侧同款 —— 非法 source_kind 只 Warn+计数，不跳过、
// 不 fail-closed，求值仍按老路 target_type/target_id。
func TestM0AlertInvalidSourceKindWarnedButLoadsAndEvaluates(t *testing.T) {
	db := newTestDB(t)
	kind := "dataset"
	r := rule(1, "temperature", "gt", 50, 0)
	r.SourceKind = &kind
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	metric := metrics.RuleInvalidSourceKindTotal.WithLabelValues("alert")
	before := counterValue(t, metric)

	ev := NewEvaluator(db, nil)
	if got := counterValue(t, metric); got != before+1 {
		t.Fatalf("非法 source_kind 必须使 ehome_rule_invalid_source_kind_total{kind=alert} +1，before=%v got=%v",
			before, got)
	}
	if _, ok := ev.rules[1]; !ok {
		t.Fatal("H-3：非法 source_kind 的告警规则不得被跳过")
	}
	ev.Evaluate(1, fields("temperature", 60), time.Now())
	if n := eventCount(db, 1); n != 1 {
		t.Fatalf("H-3：非法 source_kind 必须回落老路继续求值，got %d 条事件", n)
	}
}

// ── 热路径零身份查库：Evaluate 不得为「目标解析」查任何表 ──────────────────
//
// 历史：本用例曾是 TestM0AlertEvaluateResolvesLogicalIDOutsideLock —— 它证明的是
// 「resolveLogicalID 的 SQL 往返发生在锁外」（H2 锁放大的修复）。逻辑设备概念层
// 整体移除后（2026-10-23, .contract/logical-device-removal.md §7），那条 SQL
// 往返本身不存在了。
//
// 现在的判据（比原来更强、也更准确）：
//
//	Evaluate 期间**只允许**触碰告警自身的簿记表 alert_events（firing→resolved
//	的状态机本来就要读它：onResolve 要找到最近一条 firing 行就地关闭）。
//	任何对身份/设备表（edge_devices、logical_devices）的查询都判红 ——
//	那正是「每帧一次身份解析」的表现形式。
//
// 为什么不能简单断言「零 SQL」：状态机自身的 bookkeeping 是**必须**的 DB 读写
// （否则 firing→resolved 无法闭环）。把「零 SQL」当判据会逼着实现把簿记挪到别处，
// 那不是本用例守护的目标。
//
// 实现注意（踩过）：查询计数必须注册在 **After**("gorm:query")，此时
// Statement.Table 才由 BuildQuerySQL 解析出来；注册在 Before 时 Table 恒空，
// 判据会恒绿 —— 那是一条假测试。
func TestM0AlertEvaluateIssuesNoIdentityQueries(t *testing.T) {
	db := newTestDB(t)
	if err := db.Create(&models.EdgeDevice{ID: 1}).Error; err != nil {
		t.Fatal(err)
	}
	r := rule(1, "temperature", "gt", 50, 0)
	r.TargetType = models.AlertTargetEdgeDevice
	r.TargetID = 1
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	ev := NewEvaluator(db, nil) // NewEvaluator 内部 LoadRules 已在计数注册之前完成

	// identityTables 是「解析帧目标身份」会碰的表。Evaluate 期间出现它们即回归。
	identityTables := map[string]bool{"edge_devices": true, "logical_devices": true}
	var identityQueries atomic.Int32
	var mu sync.Mutex
	var offending []string
	if err := db.Callback().Query().After("gorm:query").
		Register("test:count_identity_queries", func(tx *gorm.DB) {
			if identityTables[tx.Statement.Table] {
				identityQueries.Add(1)
				mu.Lock()
				offending = append(offending, tx.Statement.Table+" :: "+tx.Statement.SQL.String())
				mu.Unlock()
			}
		}); err != nil {
		t.Fatalf("register callback: %v", err)
	}

	// 求值三条帧，覆盖 触发 / 恢复 / 不匹配 三路（不匹配那路历史上会触发 identity 解析）。
	now := time.Now()
	ev.Evaluate(1, fields("temperature", 60), now)
	ev.Evaluate(1, fields("temperature", 40), now.Add(time.Second))
	ev.Evaluate(999, fields("temperature", 60), now.Add(2*time.Second))

	if got := identityQueries.Load(); got != 0 {
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("Evaluate 为身份解析查询了 %d 次边设备/逻辑设备表 —— 热路径必须零身份查库；明细: %v",
			got, offending)
	}
	// 求值确实发生了（否则"零查询"是因为什么都没做，属于假绿）。
	if n := eventCount(db, 1); n == 0 {
		t.Fatal("规则应已触发（本用例断言的是「零身份查库」，不是「什么都不做」）")
	}
}
