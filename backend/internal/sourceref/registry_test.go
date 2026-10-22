package sourceref

import (
	"sync"
	"testing"
)

// compositeGroup 构造 composite 组（2 成员，无 active）。
func compositeGroup(combine string) *GroupSnapshot {
	return newGroup(KindComposite, combine, OnDegradeFailClosed, 1, 0,
		member(1, 101, 20, false), member(2, 102, 10, false))
}

// primaryBackupGroup 构造主备组（成员 101 active，102 standby）。
func primaryBackupGroup() *GroupSnapshot {
	return newGroup(KindPrimaryBackup, "", OnDegradeFailClosed, 1, 1,
		member(1, 101, 20, false), member(2, 102, 10, false))
}

// TestMatchFrameKindDispatch 覆盖 kind 分流：非 source_group / groupID=0 ⇒ 零值。
func TestMatchFrameKindDispatch(t *testing.T) {
	r := NewRegistry()
	r.Publish(primaryBackupGroup())

	for _, kind := range []string{"", "edge_device", "logical_device", "device", "source_group_x", "SOURCE_GROUP"} {
		got := r.MatchFrame(101, kind, 1)
		if got != (FrameMatch{}) {
			t.Fatalf("kind=%q should return zero FrameMatch, got %+v", kind, got)
		}
		if got.Accepts() {
			t.Fatalf("kind=%q zero FrameMatch must not accept", kind)
		}
	}
	// groupID=0（source_kind 非法绑定）⇒ 零值，fail-closed 由调用方处理。
	if got := r.MatchFrame(101, GroupKindSourceGroup, 0); got != (FrameMatch{}) {
		t.Fatalf("groupID=0 = %+v want zero", got)
	}
}

// TestMatchFrameGroupMissing 覆盖组不存在 ⇒ Configured=false + group_missing。
func TestMatchFrameGroupMissing(t *testing.T) {
	r := NewRegistry()
	got := r.MatchFrame(101, GroupKindSourceGroup, 42)
	mustTrue(t, !got.Configured, "missing group must be Configured=false")
	mustTrue(t, !got.Accepts(), "missing group must not accept")
	mustTrue(t, got.Reason == ReasonGroupMissing, "reason = %q", got.Reason)
	mustTrue(t, got.Group == nil, "missing group pointer should be nil")
	mustTrue(t, got.GroupID == 42, "GroupID = %d", got.GroupID)
	// 非法 kind 时分流优先：仍为零值（不给 group_missing 审计噪声）。
	if got := r.MatchFrame(101, "", 42); got != (FrameMatch{}) {
		t.Fatalf("invalid kind + missing group = %+v want zero", got)
	}
}

// TestMatchFrameGroupDisabled 覆盖禁用组：Configured=false + group_disabled，但保留身份字段。
func TestMatchFrameGroupDisabled(t *testing.T) {
	r := NewRegistry()
	g := primaryBackupGroup()
	g.Enabled = false
	g.Revision = 99
	g.TransitionSeq = 7
	r.Publish(g)

	got := r.MatchFrame(101, GroupKindSourceGroup, g.ID)
	mustTrue(t, !got.Configured, "disabled group Configured=false")
	mustTrue(t, !got.Accepts(), "disabled group must not accept")
	mustTrue(t, got.Reason == ReasonGroupDisabled, "reason = %q want %q", got.Reason, ReasonGroupDisabled)
	mustTrue(t, got.Group == g, "disabled group pointer should be preserved for auditing")
	mustTrue(t, got.Kind == KindPrimaryBackup && got.Category == "power", "kind/category = %q/%q", got.Kind, got.Category)
	mustTrue(t, got.Revision == 99 && got.TransitionSeq == 7, "revision/transition = %d/%d", got.Revision, got.TransitionSeq)
	mustTrue(t, !got.IsMember && !got.IsActive, "disabled group should not compute membership")
}

// TestMatchFramePrimaryBackup 是 S1 过滤面：只有 active 成员帧可驱动求值。
func TestMatchFramePrimaryBackup(t *testing.T) {
	r := NewRegistry()
	g := primaryBackupGroup()
	r.Publish(g)

	// active 帧：可驱动。
	active := r.MatchFrame(101, GroupKindSourceGroup, g.ID)
	mustTrue(t, active.Configured, "active frame Configured")
	mustTrue(t, active.IsMember && active.IsActive, "active frame member/active = %v/%v", active.IsMember, active.IsActive)
	mustTrue(t, active.Accepts(), "active frame must accept")
	mustTrue(t, active.Group == g, "Group pointer")
	mustTrue(t, active.Revision == g.Revision && active.TransitionSeq == g.TransitionSeq, "identity passthrough")

	// standby 帧：是成员但不 active ⇒ 不得驱动（否则 DurationSec 被备帧拼接）。
	standby := r.MatchFrame(102, GroupKindSourceGroup, g.ID)
	mustTrue(t, standby.Configured, "standby frame Configured")
	mustTrue(t, standby.IsMember && !standby.IsActive, "standby frame member/active = %v/%v", standby.IsMember, standby.IsActive)
	mustTrue(t, !standby.Accepts(), "standby frame must NOT accept (S1)")

	// 非成员帧：不是成员、不 active、不接收。
	other := r.MatchFrame(999, GroupKindSourceGroup, g.ID)
	mustTrue(t, other.Configured, "other frame Configured")
	mustTrue(t, !other.IsMember && !other.IsActive, "other frame member/active = %v/%v", other.IsMember, other.IsActive)
	mustTrue(t, !other.Accepts(), "non-member frame must NOT accept")

	// 无 active（ActiveMemberID=0）⇒ 所有成员帧都不接收。
	g2 := primaryBackupGroup()
	g2.ActiveMemberID = 0
	r.Publish(g2)
	noActive := r.MatchFrame(101, GroupKindSourceGroup, g2.ID)
	mustTrue(t, noActive.IsMember && !noActive.IsActive && !noActive.Accepts(),
		"no-active group: member=%v active=%v accept=%v", noActive.IsMember, noActive.IsActive, noActive.Accepts())
}

// TestMatchFrameComposite 覆盖复合组：任一成员帧都驱动整组求值（无 active 概念）。
func TestMatchFrameComposite(t *testing.T) {
	r := NewRegistry()
	g := compositeGroup(CombineMax)
	r.Publish(g)

	for _, edge := range []uint{101, 102} {
		got := r.MatchFrame(edge, GroupKindSourceGroup, g.ID)
		mustTrue(t, got.Configured, "composite member %d Configured", edge)
		mustTrue(t, got.IsMember, "composite member %d IsMember", edge)
		mustTrue(t, got.Accepts(), "composite member %d must accept", edge)
		mustTrue(t, got.Kind == KindComposite, "kind = %q", got.Kind)
	}
	// 非成员不接收（但仍 Configured=true，供审计"组存在但设备不属于"）。
	other := r.MatchFrame(999, GroupKindSourceGroup, g.ID)
	mustTrue(t, other.Configured && !other.IsMember && !other.Accepts(), "composite non-member = %+v", other)
}

// TestFrameMatchAccepts 直接钉死 Accepts 真值表。
func TestFrameMatchAccepts(t *testing.T) {
	tests := []struct {
		name string
		m    FrameMatch
		want bool
	}{
		{"zero", FrameMatch{}, false},
		{"not_configured", FrameMatch{Configured: false, IsMember: true, IsActive: true, Kind: KindComposite}, false},
		{"composite_member", FrameMatch{Configured: true, IsMember: true, Kind: KindComposite}, true},
		{"composite_non_member", FrameMatch{Configured: true, IsMember: false, Kind: KindComposite}, false},
		{"primary_active", FrameMatch{Configured: true, IsMember: true, IsActive: true, Kind: KindPrimaryBackup}, true},
		{"primary_standby", FrameMatch{Configured: true, IsMember: true, IsActive: false, Kind: KindPrimaryBackup}, false},
		{"primary_non_member", FrameMatch{Configured: true, IsMember: false, IsActive: true, Kind: KindPrimaryBackup}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.m.Accepts(); got != tc.want {
				t.Fatalf("Accepts() = %v want %v", got, tc.want)
			}
		})
	}
}

// TestRegistryPublishGroupRemoveLen 覆盖注册表生命周期。
func TestRegistryPublishGroupRemoveLen(t *testing.T) {
	r := NewRegistry()
	mustTrue(t, r.Len() == 0, "new registry Len = %d", r.Len())
	if _, ok := r.Group(1); ok {
		t.Fatal("empty registry should not contain group 1")
	}

	g1 := compositeGroup(CombineMax)
	g1.ID = 1
	r.Publish(g1)
	mustTrue(t, r.Len() == 1, "Len after publish = %d", r.Len())
	got, ok := r.Group(1)
	mustTrue(t, ok && got == g1, "Group(1) should return published pointer")

	// nil / ID=0 发布是 no-op。
	r.Publish(nil)
	r.Publish(&GroupSnapshot{})
	mustTrue(t, r.Len() == 1, "nil/ID0 publish must be no-op, Len = %d", r.Len())

	// 覆盖发布同 ID：指针替换、Len 不变。
	g1b := compositeGroup(CombineAvg)
	g1b.ID = 1
	g1b.Revision = 100
	r.Publish(g1b)
	mustTrue(t, r.Len() == 1, "overwrite Len = %d", r.Len())
	if cur, ok := r.Group(1); !ok || cur != g1b || cur.Revision != 100 {
		t.Fatalf("overwrite did not take effect: %+v", cur)
	}

	// 多组 + 移除。
	g2 := compositeGroup(CombineSum)
	g2.ID = 2
	r.Publish(g2)
	mustTrue(t, r.Len() == 2, "Len = %d", r.Len())
	r.Remove(1)
	mustTrue(t, r.Len() == 1, "Len after remove = %d", r.Len())
	if _, ok := r.Group(1); ok {
		t.Fatal("group 1 should be removed")
	}
	// 移除不存在组：no-op。
	r.Remove(1)
	r.Remove(999)
	mustTrue(t, r.Len() == 1, "removing missing group must be no-op")

	// Group(0) 恒 false。
	if _, ok := r.Group(0); ok {
		t.Fatal("Group(0) must be false")
	}
}

// TestRegistryReplaceAll 覆盖启动/全量重载。
func TestRegistryReplaceAll(t *testing.T) {
	r := NewRegistry()
	g1 := compositeGroup(CombineMax)
	g1.ID = 1
	r.Publish(g1)

	g2 := compositeGroup(CombineAvg)
	g2.ID = 2
	g3 := compositeGroup(CombineSum)
	g3.ID = 3
	// nil 与 ID=0 应被过滤；重复 ID 后者覆盖前者。
	r.ReplaceAll([]*GroupSnapshot{g2, nil, {ID: 0}, g3, g2})
	mustTrue(t, r.Len() == 2, "ReplaceAll Len = %d", r.Len())
	if _, ok := r.Group(1); ok {
		t.Fatal("ReplaceAll must drop groups not present in the new set")
	}
	if cur, ok := r.Group(2); !ok || cur != g2 {
		t.Fatalf("ReplaceAll group 2 = %+v", cur)
	}

	// 空替换清空。
	r.ReplaceAll(nil)
	mustTrue(t, r.Len() == 0, "ReplaceAll(nil) Len = %d", r.Len())
	// 再次 ReplaceAll 可恢复。
	r.ReplaceAll([]*GroupSnapshot{g1})
	mustTrue(t, r.Len() == 1, "restore Len = %d", r.Len())

	// MatchFrame 反映新视图。
	got := r.MatchFrame(101, GroupKindSourceGroup, 1)
	mustTrue(t, got.Configured && got.IsMember, "MatchFrame after ReplaceAll = %+v", got)
}

// TestRegistryNilReceiver 覆盖 nil Registry 的防御（调用方未接线时 fail-closed）。
func TestRegistryNilReceiver(t *testing.T) {
	var r *Registry
	if _, ok := r.Group(1); ok {
		t.Fatal("nil registry Group must be false")
	}
	mustTrue(t, r.Len() == 0, "nil registry Len = %d", r.Len())

	// nil registry 必须 fail-closed（Configured=false 且不接收）；reason 由实现给
	// group_missing（与"组不存在"共用审计口径），不强制零值。
	got := r.MatchFrame(101, GroupKindSourceGroup, 1)
	mustTrue(t, !got.Configured && !got.Accepts() && got.Group == nil, "nil registry MatchFrame must fail-closed, got %+v", got)
}

// TestMatchFrameZeroAllocs 钉死热路径约定「无分配」（方案 §12.2.1）。
func TestMatchFrameZeroAllocs(t *testing.T) {
	r := NewRegistry()
	g := primaryBackupGroup()
	r.Publish(g)
	allocs := testing.AllocsPerRun(1000, func() {
		m := r.MatchFrame(101, GroupKindSourceGroup, g.ID)
		if !m.Accepts() {
			panic("unexpected reject")
		}
	})
	if allocs != 0 {
		t.Fatalf("MatchFrame allocations = %v/op want 0", allocs)
	}
}

// TestRegistryConcurrentAccess 是 -race 门禁：发布（冷）与读（热）并发安全。
func TestRegistryConcurrentAccess(t *testing.T) {
	r := NewRegistry()
	base := compositeGroup(CombineMax)
	base.ID = 1
	r.Publish(base)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// 读路径：Group / Len / MatchFrame。
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if g, ok := r.Group(1); ok && g == nil {
					t.Error("Group returned nil snapshot with ok=true")
					return
				}
				_ = r.Len()
				m := r.MatchFrame(101, GroupKindSourceGroup, 1)
				if m.Configured && m.Group == nil {
					t.Error("configured match without snapshot")
					return
				}
			}
		}()
	}

	// 写路径：Publish（单组 COW）+ 周期 ReplaceAll/Remove，全部通过写锁串行。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			g := compositeGroup(CombineMax)
			g.ID = 1
			g.Revision = int64(i)
			r.Publish(g)
			if i%50 == 0 {
				r.ReplaceAll([]*GroupSnapshot{g})
			}
			if i%70 == 0 {
				r.Remove(1)
			}
		}
	}()

	// 让写路径先完成，再关 reader 停止信号。
	doneWrite := make(chan struct{})
	go func() {
		defer close(doneWrite)
		for i := 0; i < 50; i++ {
			_ = r.Len()
		}
	}()
	<-doneWrite
	close(stop)
	wg.Wait()

	// 终态一致性：快照至少合法（Len/Group 自洽）。
	if r.Len() == 0 {
		// 写路径可能以 Remove 收尾；重新发布验证可恢复。
		r.Publish(base)
	}
	if g, ok := r.Group(1); ok && g == nil {
		t.Fatal("final Group(1) returned nil with ok=true")
	}
}

// TestRegistryPublishVisibility 覆盖发布后立即可见（atomic.Pointer store 顺序语义）。
func TestRegistryPublishVisibility(t *testing.T) {
	r := NewRegistry()
	for i := 0; i < 100; i++ {
		g := compositeGroup(CombineMax)
		g.ID = uint(i + 1)
		r.Publish(g)
		if cur, ok := r.Group(uint(i + 1)); !ok || cur != g {
			t.Fatalf("publish %d not immediately visible", i+1)
		}
	}
	mustTrue(t, r.Len() == 100, "Len = %d want 100", r.Len())
}
