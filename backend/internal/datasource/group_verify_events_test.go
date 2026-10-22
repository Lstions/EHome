package datasource

// 独立验证（impl-core）：ActiveChange 发射点完整性（R1–R11 唯一 helper）。
//
// 判据：**每个状态迁移恰发 1 条 ActiveChange，不该发时 0 条（多/少都红）**。
// 事件消费方（automation/alert）靠它清窗与切 active 过滤，漏发 = 组降级后永不清窗；
// 多发 = 无意义窗口重置。

import (
	"errors"
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/sourceref"
)

// healthEvents 返回组的健康事件（按 id ASC）。
func (f *vf) healthEvents(groupID uint) []models.SourceGroupHealth {
	f.t.Helper()
	var rows []models.SourceGroupHealth
	if err := f.db.Where("group_id = ?", groupID).Order("id ASC").Find(&rows).Error; err != nil {
		f.t.Fatalf("query health: %v", err)
	}
	return rows
}

// failoverLogs 返回组的切换日志（按 id ASC）。
func (f *vf) failoverLogs(groupID uint) []models.SourceGroupFailoverLog {
	f.t.Helper()
	var rows []models.SourceGroupFailoverLog
	if err := f.db.Where("group_id = ?", groupID).Order("id ASC").Find(&rows).Error; err != nil {
		f.t.Fatalf("query failover logs: %v", err)
	}
	return rows
}

// memberOf 返回某 edge 的成员行。
func (f *vf) memberOf(gid, edge uint) models.SourceGroupMember {
	f.t.Helper()
	m, ok := memberByEdge(f.members(gid), edge)
	if !ok {
		f.t.Fatalf("group %d has no member for edge %d", gid, edge)
	}
	return m
}

// ── R1/R2：成功与未达阈值失败不得发事件 ───────────────────────────────────

func TestVerifyEvents_R1R2_NoEvent(t *testing.T) {
	f := newVF(t)
	a, b := f.addDevice("verify-a"), f.addDevice("verify-b")
	g := f.createPB(a, b)

	// R1：成功 → 0 事件（且不 bump，已由 revision 用例覆盖）。
	f.advance(time.Second)
	f.gsvc.MarkSuccess(a.ID, []string{"illuminance"}, f.now)
	f.expectNoEvents()

	// R2：未达阈值的失败 → 0 事件。
	am := f.memberOf(g.ID, a.ID)
	f.setMaxFail(am.ID, 5)
	f.advance(time.Second)
	f.gsvc.MarkFailure(a.ID, TriggerDeviceOffline)
	f.expectNoEvents()

	ms := f.members(g.ID)
	if cur, _ := memberByEdge(ms, a.ID); cur.FailCount != 1 {
		t.Fatalf("未达阈值应只计次，fail_count=%d", cur.FailCount)
	}
}

// ── R3：自动切换恰 1 条，字段完整 ─────────────────────────────────────────

func TestVerifyEvents_R3_FailoverExactlyOne(t *testing.T) {
	f := newVF(t)
	a, b := f.addDevice("verify-a"), f.addDevice("verify-b")
	g := f.createPB(a, b)
	rev0 := f.revision(g.ID)

	am := f.memberOf(g.ID, a.ID)
	f.setMaxFail(am.ID, 1)
	f.advance(time.Second)
	f.gsvc.MarkFailure(a.ID, TriggerDeviceOffline)

	evs := f.waitEvents(1)
	if len(evs) != 1 {
		t.Fatalf("R3 应恰 1 条事件，got %d: %+v", len(evs), evs)
	}
	ev := evs[0]
	if ev.GroupID != g.ID || ev.PrevMemberID != am.ID || ev.NextMemberID != f.memberOf(g.ID, b.ID).ID {
		t.Fatalf("R3 事件字段错误: %+v", ev)
	}
	if ev.Reason != ReasonGroupAuto || ev.Trigger != TriggerDeviceOffline {
		t.Fatalf("R3 reason/trigger={%q,%q}", ev.Reason, ev.Trigger)
	}
	if ev.Revision != rev0+1 {
		t.Fatalf("R3 revision=%d want %d", ev.Revision, rev0+1)
	}
	if ev.TransitionSeq == 0 || ev.Category != "illuminance" || ev.At.IsZero() {
		t.Fatalf("R3 事件缺字段: %+v", ev)
	}
	f.expectNoMoreEvents(1) // 确保没有多余的第二条
}

// ── R4：无候选降级必须发 Next=0（方案 §5.4 点名的危险点）──────────────────

func TestVerifyEvents_R4_DegradedNextZero(t *testing.T) {
	f := newVF(t)
	a := f.addDevice("verify-a")
	g := f.createPB(a)
	rev0 := f.revision(g.ID)
	am := f.memberOf(g.ID, a.ID)
	f.setMaxFail(am.ID, 1)

	f.advance(time.Second)
	f.gsvc.MarkFailure(a.ID, TriggerDeviceOffline)

	evs := f.waitEvents(1)
	if len(evs) != 1 {
		t.Fatalf("R4 应恰 1 条事件，got %d: %+v", len(evs), evs)
	}
	ev := evs[0]
	if ev.NextMemberID != 0 || ev.PrevMemberID != am.ID || ev.Reason != ReasonGroupAuto {
		t.Fatalf("R4 必须 Next=0 且带 Prev/Reason: %+v", ev)
	}
	if ev.Revision != rev0+1 {
		t.Fatalf("R4 降级也是权威迁移，revision 必须 +1：got %d want %d", ev.Revision, rev0+1)
	}
	// 组确实失去权威（不是只发事件不改状态）。
	if edge := f.activeEdge(g.ID); edge != 0 {
		t.Fatalf("R4 后组不应有 active，got edge %d", edge)
	}
	if snap := f.snapshot(g.ID); snap.ActiveMemberID != 0 {
		t.Fatalf("R4 后快照 ActiveMemberID 必须 0，got %d", snap.ActiveMemberID)
	}
	// 规则侧：R4 后该组帧不得再驱动求值（fail-closed 方向）。
	m := f.reg.MatchFrame(a.ID, sourceref.GroupKindSourceGroup, g.ID)
	if m.Accepts() {
		t.Fatalf("R4 后仍 Accepts（会用旧 active 过滤）: %+v", m)
	}
	if h := f.healthEvents(g.ID); len(h) == 0 || h[len(h)-1].Event != "degraded" {
		t.Fatalf("R4 必须落 degraded 健康事件，got %+v", h)
	}
	f.expectNoMoreEvents(1)
}

// ── R4→N-21 自愈闭链：无 active 的组必须能被扫描补选回权威 ────────────────

func TestVerifyEvents_R4_SelfHealClosedLoop(t *testing.T) {
	f := newVF(t)
	a, b := f.addDevice("verify-a"), f.addDevice("verify-b")
	g := f.createPB(a, b)
	am, bm := f.memberOf(g.ID, a.ID), f.memberOf(g.ID, b.ID)
	f.setMaxFail(am.ID, 1)
	f.setMaxFail(bm.ID, 1)
	rev0 := f.revision(g.ID)

	// 先把 standby B 熔断（A 仍 active ⇒ 不切换、不发事件）。
	f.advance(time.Second)
	f.gsvc.MarkFailure(b.ID, TriggerDeviceOffline)
	f.expectNoEvents()
	if cur := f.memberOf(g.ID, b.ID); cur.Status != models.SourceMemberStatusError {
		t.Fatalf("B 应先熔断为 error，got %q", cur.Status)
	}

	// 再熔断 active A ⇒ 无候选 ⇒ R4 降级 Next=0。
	f.advance(time.Second)
	f.gsvc.MarkFailure(a.ID, TriggerDeviceOffline)
	evs := f.waitEvents(1)
	if len(evs) != 1 || evs[0].NextMemberID != 0 {
		t.Fatalf("R4 事件错误: %+v", evs)
	}
	if f.activeEdge(g.ID) != 0 {
		t.Fatal("R4 后组应无 active")
	}

	// B 恢复（error→standby + 新鲜 last_success），扫描应自愈补选。
	f.advance(10 * time.Second)
	f.gsvc.MarkSuccess(b.ID, []string{"illuminance"}, f.now)
	f.clearEvents()
	switched, err := f.gsvc.ScanGroups(f.now)
	if err != nil {
		t.Fatalf("ScanGroups: %v", err)
	}
	if switched != 1 {
		t.Fatalf("自愈应切 1 次，got %d", switched)
	}
	evs = f.waitEvents(1)
	if len(evs) != 1 {
		t.Fatalf("自愈应恰 1 条事件，got %d: %+v", len(evs), evs)
	}
	ev := evs[0]
	if ev.Reason != ReasonGroupSelfHeal || ev.NextMemberID != bm.ID || ev.PrevMemberID != 0 {
		t.Fatalf("自愈事件字段错误: %+v", ev)
	}
	if ev.Revision != rev0+2 {
		t.Fatalf("R4+自愈应 bump 两次：got %d want %d", ev.Revision, rev0+2)
	}
	if edge := f.activeEdge(g.ID); edge != b.ID {
		t.Fatalf("自愈后 active 应为 B(%d)，got %d", b.ID, edge)
	}
	if m := f.reg.MatchFrame(b.ID, sourceref.GroupKindSourceGroup, g.ID); !m.Accepts() {
		t.Fatalf("自愈后 B 帧必须可驱动求值: %+v", m)
	}
	if h := f.healthEvents(g.ID); len(h) == 0 || h[len(h)-1].Event != "selfheal" {
		t.Fatalf("自愈必须落 selfheal 健康事件，got %+v", h)
	}
	f.expectNoMoreEvents(1)
}

// ── R10：冷却窗内不得再次切换（但必须落抑制痕迹）─────────────────────────

func TestVerifyEvents_R10_CooldownSuppresses(t *testing.T) {
	f := newVF(t)
	a, b := f.addDevice("verify-a"), f.addDevice("verify-b")
	g := f.createPB(a, b)
	am, bm := f.memberOf(g.ID, a.ID), f.memberOf(g.ID, b.ID)
	f.setMaxFail(am.ID, 1)
	f.setMaxFail(bm.ID, 1)

	// 第一次切换 A→B（日志时间 = 现在）。
	f.advance(time.Second)
	f.gsvc.MarkFailure(a.ID, TriggerDeviceOffline)
	f.waitEvents(1)
	f.clearEvents()
	if got := len(f.failoverLogs(g.ID)); got != 1 {
		t.Fatalf("首次切换应写 1 条日志，got %d", got)
	}

	// A 恢复为 standby；10s 后 B 熔断 ⇒ 冷却窗内抑制（0 事件）。
	f.gsvc.MarkSuccess(a.ID, []string{"illuminance"}, f.now)
	f.advance(10 * time.Second)
	f.gsvc.MarkFailure(b.ID, TriggerDeviceOffline)
	f.expectNoEvents()
	if got := len(f.failoverLogs(g.ID)); got != 1 {
		t.Fatalf("冷却抑制不得写切换日志，got %d", got)
	}
	found := false
	for _, h := range f.healthEvents(g.ID) {
		if h.Event == "silent" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("冷却抑制必须落 silent 健康事件（可观测）")
	}
	if f.activeEdge(g.ID) != 0 {
		t.Fatal("冷却抑制后组应无 active（B 已熔断）")
	}
}

// ── R11：刚 active 的成员不得立刻计失败；驻留期满后可计 ──────────────────

func TestVerifyEvents_R11_MinResidency(t *testing.T) {
	t.Run("fresh_active_skipped", func(t *testing.T) {
		f := newVF(t)
		a, b := f.addDevice("verify-a"), f.addDevice("verify-b")
		g := f.createPB(a, b)
		am, bm := f.memberOf(g.ID, a.ID), f.memberOf(g.ID, b.ID)
		f.setMaxFail(am.ID, 1)
		f.setMaxFail(bm.ID, 1)
		f.advance(time.Second)
		f.gsvc.MarkFailure(a.ID, TriggerDeviceOffline)
		f.waitEvents(1)
		f.clearEvents()

		// 模拟"刚成为 active"：updated_at = 当前时刻（真实写入时间在未来时钟之前）。
		if err := f.db.Model(&models.SourceGroupMember{}).Where("id = ?", bm.ID).
			UpdateColumn("updated_at", f.now).Error; err != nil {
			t.Fatalf("set updated_at: %v", err)
		}
		// B 无 last_success ⇒ 陈旧；但驻留期未满，扫描必须跳过（不计次、不切换）。
		_, err := f.gsvc.ScanGroups(f.now)
		if err != nil {
			t.Fatalf("ScanGroups: %v", err)
		}
		f.expectNoEvents()
		if cur := f.memberOf(g.ID, b.ID); cur.FailCount != 0 {
			t.Fatalf("R11 驻留期内不得计失败，fail_count=%d", cur.FailCount)
		}
	})

	t.Run("residency_elapsed_counts", func(t *testing.T) {
		f := newVF(t)
		a, b := f.addDevice("verify-a"), f.addDevice("verify-b")
		g := f.createPB(a, b)
		am := f.memberOf(g.ID, a.ID)
		f.setMaxFail(am.ID, 2) // 阈值 2：本次只计次不切换，隔离 R11 判定
		// active 成员：驻留期已过（updated_at 拨回 3 分钟前）、无 last_success ⇒ 陈旧。
		if err := f.db.Model(&models.SourceGroupMember{}).Where("id = ?", am.ID).
			UpdateColumn("updated_at", f.now.Add(-3*time.Minute)).Error; err != nil {
			t.Fatalf("set updated_at: %v", err)
		}
		_, err := f.gsvc.ScanGroups(f.now)
		if err != nil {
			t.Fatalf("ScanGroups: %v", err)
		}
		if cur := f.memberOf(g.ID, a.ID); cur.FailCount != 1 {
			t.Fatalf("驻留期满后陈旧 active 应计 1 次失败，got %d", cur.FailCount)
		}
		f.expectNoEvents()
	})

	t.Run("stale_triggers_switch_after_residency", func(t *testing.T) {
		f := newVF(t)
		a, b := f.addDevice("verify-a"), f.addDevice("verify-b")
		g := f.createPB(a, b)
		am := f.memberOf(g.ID, a.ID)
		f.setMaxFail(am.ID, 1)
		if err := f.db.Model(&models.SourceGroupMember{}).Where("id = ?", am.ID).
			UpdateColumn("updated_at", f.now.Add(-3*time.Minute)).Error; err != nil {
			t.Fatalf("set updated_at: %v", err)
		}
		switched, err := f.gsvc.ScanGroups(f.now)
		if err != nil {
			t.Fatalf("ScanGroups: %v", err)
		}
		if switched != 1 {
			t.Fatalf("陈旧主动成员应触发 1 次切换，got %d", switched)
		}
		evs := f.waitEvents(1)
		if len(evs) != 1 || evs[0].Trigger != TriggerStaleData || evs[0].NextMemberID == 0 {
			t.Fatalf("R11 切换事件错误: %+v", evs)
		}
	})
}

// ── R5/R6/R8/R7：手动迁移事件语义 ─────────────────────────────────────────

func TestVerifyEvents_ManualMigrations(t *testing.T) {
	t.Run("R5_deactivate_active_switches_once", func(t *testing.T) {
		f := newVF(t)
		a, b := f.addDevice("verify-a"), f.addDevice("verify-b")
		g := f.createPB(a, b)
		am, bm := f.memberOf(g.ID, a.ID), f.memberOf(g.ID, b.ID)
		rev0 := f.revision(g.ID)

		f.advance(time.Second)
		row, err := f.gsvc.DeactivateGroupMember(g.ID, am.ID)
		if err != nil {
			t.Fatalf("Deactivate active: %v", err)
		}
		if row == nil {
			t.Fatal("Deactivate 应返回组")
		}
		evs := f.waitEvents(1)
		if len(evs) != 1 {
			t.Fatalf("R5 应恰 1 条事件，got %d: %+v", len(evs), evs)
		}
		ev := evs[0]
		if ev.PrevMemberID != am.ID || ev.NextMemberID != bm.ID || ev.Reason != ReasonGroupManualDeactivate {
			t.Fatalf("R5 事件字段错误: %+v", ev)
		}
		if f.revision(g.ID) != rev0+1 {
			t.Fatalf("R5 迁移应 +1 revision，got %d want %d", f.revision(g.ID), rev0+1)
		}
		if cur := f.memberOf(g.ID, a.ID); cur.Status != models.SourceMemberStatusDisabled {
			t.Fatalf("R5 原 active 应变 disabled，got %q", cur.Status)
		}
		if edge := f.activeEdge(g.ID); edge != b.ID {
			t.Fatalf("R5 后 active 应为 B，got %d", edge)
		}
		f.expectNoMoreEvents(1)
	})

	t.Run("R6_deactivate_no_candidate_conflict", func(t *testing.T) {
		f := newVF(t)
		a := f.addDevice("verify-a")
		g := f.createPB(a)
		am := f.memberOf(g.ID, a.ID)
		rev0 := f.revision(g.ID)
		f.advance(time.Second)
		_, err := f.gsvc.DeactivateGroupMember(g.ID, am.ID)
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("R6 无候选应 ErrConflict，got %v", err)
		}
		if f.revision(g.ID) != rev0 {
			t.Fatalf("R6 被拒不得改状态/revision")
		}
		f.expectNoEvents()
	})

	t.Run("R8_deactivate_non_active_no_event", func(t *testing.T) {
		f := newVF(t)
		a, b := f.addDevice("verify-a"), f.addDevice("verify-b")
		g := f.createPB(a, b)
		bm := f.memberOf(g.ID, b.ID)
		f.advance(time.Second)
		if _, err := f.gsvc.DeactivateGroupMember(g.ID, bm.ID); err != nil {
			t.Fatalf("R8 deactivate standby: %v", err)
		}
		f.expectNoEvents()
		if cur := f.memberOf(g.ID, b.ID); cur.Status != models.SourceMemberStatusDisabled {
			t.Fatalf("R8 非 active 应直接 disabled，got %q", cur.Status)
		}
	})

	t.Run("R7_manual_activate_once_then_idempotent", func(t *testing.T) {
		f := newVF(t)
		a, b := f.addDevice("verify-a"), f.addDevice("verify-b")
		g := f.createPB(a, b)
		am, bm := f.memberOf(g.ID, a.ID), f.memberOf(g.ID, b.ID)
		rev0 := f.revision(g.ID)
		f.advance(time.Second)

		if _, err := f.gsvc.ActivateGroupMember(g.ID, bm.ID); err != nil {
			t.Fatalf("R7 activate standby: %v", err)
		}
		evs := f.waitEvents(1)
		if len(evs) != 1 || evs[0].PrevMemberID != am.ID || evs[0].NextMemberID != bm.ID ||
			evs[0].Reason != ReasonGroupManual {
			t.Fatalf("R7 事件字段错误: %+v", evs)
		}
		if f.revision(g.ID) != rev0+1 {
			t.Fatalf("R7 应 +1 revision，got %d want %d", f.revision(g.ID), rev0+1)
		}
		// 幂等：已是 active 再次 activate ⇒ 0 事件、0 bump。
		f.clearEvents()
		rev1 := f.revision(g.ID)
		if _, err := f.gsvc.ActivateGroupMember(g.ID, bm.ID); err != nil {
			t.Fatalf("R7 idempotent activate: %v", err)
		}
		f.expectNoEvents()
		if f.revision(g.ID) != rev1 {
			t.Fatalf("R7 幂等操作不得 bump：%d → %d", rev1, f.revision(g.ID))
		}
	})

	t.Run("R9_reset_promotes_when_no_active", func(t *testing.T) {
		f := newVF(t)
		a := f.addDevice("verify-a")
		g := f.createPB(a)
		am := f.memberOf(g.ID, a.ID)
		f.setMaxFail(am.ID, 1)
		f.advance(time.Second)
		f.gsvc.MarkFailure(a.ID, TriggerDeviceOffline)
		f.waitEvents(1) // R4
		f.clearEvents()

		rev1 := f.revision(g.ID)
		if _, err := f.gsvc.ResetGroupMember(g.ID, am.ID); err != nil {
			t.Fatalf("R9 reset: %v", err)
		}
		evs := f.waitEvents(1)
		if len(evs) != 1 || evs[0].Reason != ReasonGroupReset || evs[0].NextMemberID != am.ID || evs[0].PrevMemberID != 0 {
			t.Fatalf("R9 事件字段错误: %+v", evs)
		}
		if f.revision(g.ID) != rev1+1 {
			t.Fatalf("R9 提升 active 应 +1 revision，got %d want %d", f.revision(g.ID), rev1+1)
		}
		if edge := f.activeEdge(g.ID); edge != a.ID {
			t.Fatalf("R9 后应恢复 active A，got %d", edge)
		}
	})
}

// ── 删除组：Next=0 事件（消费方必须清窗）──────────────────────────────────

func TestVerifyEvents_DeleteGroupNextZero(t *testing.T) {
	f := newVF(t)
	a, b := f.addDevice("verify-a"), f.addDevice("verify-b")
	g := f.createPB(a, b)
	am := f.memberOf(g.ID, a.ID)
	f.clearEvents()

	if err := f.gsvc.DeleteGroup(g.ID); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	evs := f.waitEvents(1)
	if len(evs) != 1 {
		t.Fatalf("删除组应恰 1 条事件，got %d: %+v", len(evs), evs)
	}
	ev := evs[0]
	if ev.NextMemberID != 0 || ev.PrevMemberID != am.ID || ev.Reason != ReasonGroupDelete {
		t.Fatalf("删除事件字段错误: %+v", ev)
	}
	if _, ok := f.reg.Group(g.ID); ok {
		t.Fatal("删除后快照必须移除")
	}
	// F-2（已修复）：删除事件必须走 commitGroupChange，携带完整身份，
	// 否则消费方按 GroupID 清窗会失败（多/少都红）。
	if ev.GroupID != g.ID {
		t.Fatalf("删除事件 GroupID=%d want %d", ev.GroupID, g.ID)
	}
	if ev.Revision == 0 || ev.TransitionSeq == 0 || ev.At.IsZero() {
		t.Fatalf("删除事件必须含 Revision/TransitionSeq/At: %+v", ev)
	}
}

// ── composite：全程不得出现 active/ActiveChange ──────────────────────────

func TestVerifyEvents_CompositeNeverEmits(t *testing.T) {
	f := newVF(t)
	a, b := f.addDevice("verify-a"), f.addDevice("verify-b")
	g := f.createComposite(models.CombineAvg, a, b)
	f.clearEvents()

	f.advance(time.Second)
	f.gsvc.MarkSuccess(a.ID, []string{"illuminance"}, f.now)
	f.gsvc.MarkFailure(a.ID, TriggerDeviceOffline)
	f.gsvc.MarkFailure(b.ID, TriggerDeviceOffline)
	f.expectNoEvents()
	if edge := f.activeEdge(g.ID); edge != 0 {
		t.Fatalf("composite 组不得有 active，got %d", edge)
	}
	if snap := f.snapshot(g.ID); snap.ActiveMemberID != 0 {
		t.Fatalf("composite 快照 ActiveMemberID 必须 0，got %d", snap.ActiveMemberID)
	}
}
