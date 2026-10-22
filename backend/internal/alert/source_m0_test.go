package alert

// M0（task-29）测试：告警侧窗口来源身份（S1/S7）、来源切换关闭陈旧 firing、
// 以及「Evaluate 不得持锁查 DB」（H2 类锁放大回归锁）。
//
// 用真实 *sourceref.Registry（与生产同结构），避免测试替身与实现漂移。

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/sourceref"

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
	ev.Evaluate(101, fields("temperature", 20), base)                       // A
	ev.Evaluate(102, fields("temperature", 20), base.Add(20*time.Second))  // B
	ev.Evaluate(101, fields("temperature", 20), base.Add(35*time.Second))  // A
	ev.Evaluate(102, fields("temperature", 20), base.Add(65*time.Second))  // B
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


// ── 锁放大回归：Evaluate 不得在 RLock 内查 DB ──────────────────────────────

func TestM0AlertEvaluateResolvesLogicalIDOutsideLock(t *testing.T) {
	db := newTestDB(t)
	lid := uint(50)
	if err := db.Create(&models.LogicalDevice{
		ID: lid, IdentityKey: "test:logical:50", Name: "测试逻辑设备", DeviceType: "bms",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.EdgeDevice{ID: 1, LogicalDeviceID: &lid}).Error; err != nil {
		t.Fatal(err)
	}
	r := rule(1, "temperature", "gt", 50, 0)
	r.TargetType = models.AlertTargetLogicalDevice
	r.TargetID = lid
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	ev := NewEvaluator(db, nil)

	// 在 gorm Query 回调里调用 LoadRules（要写锁）：若 Evaluate 持读锁查 DB，
	// 这里会死锁 ⇒ 超时即失败（回归锁的判据是"能返回"，不是"没报错"）。
	var fired atomic.Bool
	if err := db.Callback().Query().Before("gorm:query").Register("test:lock_out_db", func(tx *gorm.DB) {
		// 只在第一次 Query（即 resolveLogicalID 的 edge_devices 查询）时触发写锁；
		// 用 atomic 而不是 sync.Once：LoadRules 自身的 Query 会再次进入回调，
		// 而 sync.Once 在 Do 执行中不可重入（会自锁，制造假红）。
		if fired.CompareAndSwap(false, true) {
			ev.LoadRules()
		}
	}); err != nil {
		t.Fatalf("register callback: %v", err)
	}
	done := make(chan struct{})
	go func() {
		ev.Evaluate(1, fields("temperature", 60), time.Now())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Evaluate 疑似持锁查 DB（LoadRules 写锁被阻塞）——resolveLogicalID 必须移到锁外")
	}
}
