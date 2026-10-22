package sourceref

import (
	"reflect"
	"testing"
	"time"
)

// TestConstantsAreStable 钉死对外字符串常量（与 models / DB 列 / 事件审计同值）。
func TestConstantsAreStable(t *testing.T) {
	// 组 kind
	mustTrue(t, KindPrimaryBackup == "primary_backup", "KindPrimaryBackup=%q", KindPrimaryBackup)
	mustTrue(t, KindComposite == "composite", "KindComposite=%q", KindComposite)
	// 合并算子
	wantCombine := map[string]bool{
		"first_available": true, "avg": true, "max": true, "min": true, "sum": true,
	}
	gotCombine := []string{CombineFirstAvailable, CombineAvg, CombineMax, CombineMin, CombineSum}
	for _, c := range gotCombine {
		mustTrue(t, wantCombine[c], "unexpected combine constant %q", c)
	}
	// 缺员处置
	mustTrue(t, OnDegradeFailClosed == "fail_closed", "OnDegradeFailClosed=%q", OnDegradeFailClosed)
	mustTrue(t, OnDegradeUseAvailable == "use_available", "OnDegradeUseAvailable=%q", OnDegradeUseAvailable)
	// 成员状态
	mustTrue(t, MemberStatusActive == "active", "MemberStatusActive=%q", MemberStatusActive)
	mustTrue(t, MemberStatusStandby == "standby", "MemberStatusStandby=%q", MemberStatusStandby)
	mustTrue(t, MemberStatusError == "error", "MemberStatusError=%q", MemberStatusError)
	mustTrue(t, MemberStatusDisabled == "disabled", "MemberStatusDisabled=%q", MemberStatusDisabled)
	// 分量状态
	mustTrue(t, StateOK == "ok", "StateOK")
	mustTrue(t, StateMissing == "missing", "StateMissing")
	mustTrue(t, StateStale == "stale", "StateStale")
	mustTrue(t, StateNotEligible == "not_eligible", "StateNotEligible")
	mustTrue(t, StateNotConfigured == "not_configured", "StateNotConfigured")
	// 原因码（方案 §7.1 / §12.9.2）
	wantReasons := map[string]string{
		"source_no_active":    ReasonNoActive,
		"required_missing":    ReasonRequiredMissing,
		"below_min_members":   ReasonBelowMinMembers,
		"no_available_member": ReasonNoAvailableMember,
		"active_stale":        ReasonActiveStale,
		"group_disabled":      ReasonGroupDisabled,
		"group_missing":       ReasonGroupMissing,
	}
	for want, got := range wantReasons {
		mustTrue(t, got == want, "reason constant = %q, want %q", got, want)
	}
	mustTrue(t, GroupKindSourceGroup == "source_group", "GroupKindSourceGroup=%q", GroupKindSourceGroup)
}

// TestActiveEdgeDeviceID 覆盖 nilsafe 与 active 查找。
func TestActiveEdgeDeviceID(t *testing.T) {
	var nilGroup *GroupSnapshot
	mustTrue(t, nilGroup.ActiveEdgeDeviceID() == 0, "nil group should return 0")

	members := []MemberSnapshot{member(10, 100, 10, false), member(20, 200, 5, false)}

	tests := []struct {
		name string
		g    *GroupSnapshot
		want uint
	}{
		{"no_active", &GroupSnapshot{Members: members}, 0},
		{"active_first", &GroupSnapshot{ActiveMemberID: 10, Members: members}, 100},
		{"active_second", &GroupSnapshot{ActiveMemberID: 20, Members: members}, 200},
		{"active_not_in_members", &GroupSnapshot{ActiveMemberID: 99, Members: members}, 0},
		{"empty_members", &GroupSnapshot{ActiveMemberID: 10}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.g.ActiveEdgeDeviceID(); got != tc.want {
				t.Fatalf("ActiveEdgeDeviceID = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestIsMemberAndMemberOf 覆盖成员查找（含 nil 安全）。
func TestIsMemberAndMemberOf(t *testing.T) {
	var nilGroup *GroupSnapshot
	mustTrue(t, !nilGroup.IsMember(1), "nil group IsMember should be false")

	g := &GroupSnapshot{Members: []MemberSnapshot{member(10, 100, 10, false), member(20, 200, 5, true)}}
	mustTrue(t, g.IsMember(100), "edge 100 should be member")
	mustTrue(t, g.IsMember(200), "edge 200 should be member")
	mustTrue(t, !g.IsMember(300), "edge 300 should not be member")

	m, ok := g.MemberOf(200)
	mustTrue(t, ok, "MemberOf(200) should find member")
	mustTrue(t, m.MemberID == 20 && m.EdgeDeviceID == 200 && m.Required, "MemberOf returned %+v", m)

	if _, ok := g.MemberOf(300); ok {
		t.Fatal("MemberOf(300) should not find member")
	}
}

// TestStaleness 覆盖陈旧阈值归一（<=0 = 不启用）。
func TestStaleness(t *testing.T) {
	var nilGroup *GroupSnapshot
	if got := nilGroup.Staleness(); got != 0 {
		t.Fatalf("nil Staleness = %v", got)
	}
	tests := []struct {
		sec  int
		want time.Duration
	}{
		{0, 0}, {-5, 0}, {1, time.Second}, {60, time.Minute},
	}
	for _, tc := range tests {
		g := &GroupSnapshot{StalenessSec: tc.sec}
		if got := g.Staleness(); got != tc.want {
			t.Fatalf("StalenessSec=%d Staleness()=%v want %v", tc.sec, got, tc.want)
		}
	}
}

// TestNormalizedOnDegrade 覆盖缺员处置归一（未知/空串 → fail_closed）。
func TestNormalizedOnDegrade(t *testing.T) {
	var nilGroup *GroupSnapshot
	mustTrue(t, nilGroup.NormalizedOnDegrade() == OnDegradeFailClosed, "nil group should be fail_closed")

	tests := []struct{ in, want string }{
		{"", OnDegradeFailClosed},
		{OnDegradeFailClosed, OnDegradeFailClosed},
		{OnDegradeUseAvailable, OnDegradeUseAvailable},
		{"bogus", OnDegradeFailClosed},
	}
	for _, tc := range tests {
		g := &GroupSnapshot{OnDegrade: tc.in}
		if got := g.NormalizedOnDegrade(); got != tc.want {
			t.Fatalf("OnDegrade=%q normalized=%q want %q", tc.in, got, tc.want)
		}
	}

	// first_available ⇒ 强制 use_available（方案 D-4；与 OnDegrade 字段无关）。
	for _, in := range []string{"", OnDegradeFailClosed, OnDegradeUseAvailable, "bogus"} {
		g := &GroupSnapshot{Combine: CombineFirstAvailable, OnDegrade: in}
		if got := g.NormalizedOnDegrade(); got != OnDegradeUseAvailable {
			t.Fatalf("first_available OnDegrade=%q normalized=%q want %q", in, got, OnDegradeUseAvailable)
		}
	}
	// 其它 combine 不受影响。
	for _, combine := range []string{CombineAvg, CombineMax, CombineMin, CombineSum, "", "bogus"} {
		g := &GroupSnapshot{Combine: combine, OnDegrade: OnDegradeFailClosed}
		if got := g.NormalizedOnDegrade(); got != OnDegradeFailClosed {
			t.Fatalf("combine=%q normalized=%q want fail_closed", combine, got)
		}
	}
}

// TestEffectiveMinMembers 覆盖 min_members 生效口径：
// first_available 强制 1；其余 max(1, MinMembers)。
func TestEffectiveMinMembers(t *testing.T) {
	var nilGroup *GroupSnapshot
	if got := nilGroup.EffectiveMinMembers(); got != 1 {
		t.Fatalf("nil EffectiveMinMembers = %d", got)
	}
	tests := []struct {
		name    string
		combine string
		min     int
		want    int
	}{
		{"first_available_ignores_min", CombineFirstAvailable, 5, 1},
		{"first_available_zero_min", CombineFirstAvailable, 0, 1},
		{"avg_min_3", CombineAvg, 3, 3},
		{"sum_min_2", CombineSum, 2, 2},
		{"max_min_0", CombineMax, 0, 1},
		{"min_min_negative", CombineMin, -2, 1},
		{"unknown_combine_min_2", "bogus", 2, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := &GroupSnapshot{Combine: tc.combine, MinMembers: tc.min}
			if got := g.EffectiveMinMembers(); got != tc.want {
				t.Fatalf("EffectiveMinMembers = %d want %d", got, tc.want)
			}
		})
	}
}

// TestSortMembers 钉死 R3 候选顺序：priority DESC, memberID ASC。
func TestSortMembers(t *testing.T) {
	ms := []MemberSnapshot{
		member(3, 300, 5, false),
		member(1, 100, 10, false),
		member(2, 200, 10, false), // 同 priority，按 memberID ASC ⇒ 2 在 3 前
		member(4, 400, 1, false),
		member(5, 500, 10, false),
	}
	SortMembers(ms)
	wantIDs := []uint{1, 2, 5, 3, 4}
	for i, want := range wantIDs {
		if ms[i].MemberID != want {
			t.Fatalf("sorted[%d].MemberID=%d want %d (order=%v)", i, ms[i].MemberID, want, idsOf(ms))
		}
	}
	// 幂等：再排一次不变。
	before := append([]MemberSnapshot(nil), ms...)
	SortMembers(ms)
	if !reflect.DeepEqual(before, ms) {
		t.Fatal("SortMembers is not idempotent")
	}
	// 单元素 / nil 不 panic。
	SortMembers(nil)
	one := []MemberSnapshot{member(9, 900, 1, false)}
	SortMembers(one)
	mustTrue(t, len(one) == 1 && one[0].MemberID == 9, "single member changed")
}

// TestComputeMembersHash_StaticOnly 是 F-1 最后一道门：hash 只含静态字段。
//
// 变更 status/last_success/fail_count/member_id（运行态与本地主键）必须**不改变** hash；
// 变更 edge_device_id/priority/required（拓扑）必须改变 hash。
func TestComputeMembersHash_StaticOnly(t *testing.T) {
	ms := []MemberSnapshot{member(10, 100, 10, true), member(20, 200, 5, false)}
	h := ComputeMembersHash(ms)
	mustTrue(t, h != "", "hash should not be empty for non-empty members")

	// 运行态字段：逐项改，hash 不变（这是「每帧 revision 打死窗口」回归的最后一道门）。
	runtimeMutations := []struct {
		name string
		fn   func(MemberSnapshot) MemberSnapshot
	}{
		{"status_active_to_standby", func(m MemberSnapshot) MemberSnapshot { m.Status = MemberStatusStandby; return m }},
		{"status_to_error", func(m MemberSnapshot) MemberSnapshot { m.Status = MemberStatusError; return m }},
		{"last_success_advance", func(m MemberSnapshot) MemberSnapshot { m.LastSuccess = baseTime.Add(24 * time.Hour); return m }},
		{"last_success_zero", func(m MemberSnapshot) MemberSnapshot { m.LastSuccess = time.Time{}; return m }},
		{"fail_count", func(m MemberSnapshot) MemberSnapshot { m.FailCount = 42; return m }},
		{"member_id", func(m MemberSnapshot) MemberSnapshot { m.MemberID += 1000; return m }},
	}
	for _, mut := range runtimeMutations {
		t.Run("invariant_"+mut.name, func(t *testing.T) {
			changed := make([]MemberSnapshot, len(ms))
			for i := range ms {
				changed[i] = mut.fn(ms[i])
			}
			if got := ComputeMembersHash(changed); got != h {
				t.Fatalf("hash changed after runtime-only mutation %q: %q != %q", mut.name, got, h)
			}
		})
	}

	// 静态字段：逐项改，hash 必须改变。
	staticMutations := []struct {
		name string
		fn   func(MemberSnapshot) MemberSnapshot
	}{
		{"edge_device_id", func(m MemberSnapshot) MemberSnapshot { m.EdgeDeviceID += 1; return m }},
		{"priority", func(m MemberSnapshot) MemberSnapshot { m.Priority += 1; return m }},
		{"required", func(m MemberSnapshot) MemberSnapshot { m.Required = !m.Required; return m }},
	}
	for _, mut := range staticMutations {
		t.Run("changes_"+mut.name, func(t *testing.T) {
			changed := make([]MemberSnapshot, len(ms))
			for i := range ms {
				changed[i] = mut.fn(ms[i])
			}
			if got := ComputeMembersHash(changed); got == h {
				t.Fatalf("hash unchanged after static mutation %q (hash=%q)", mut.name, got)
			}
		})
	}
}

// TestComputeMembersHash_DeterministicAndOrderIndependent 覆盖排序无关与确定性。
func TestComputeMembersHash_DeterministicAndOrderIndependent(t *testing.T) {
	a := []MemberSnapshot{member(10, 100, 10, true), member(20, 200, 5, false), member(30, 300, 7, true)}
	b := []MemberSnapshot{a[2], a[0], a[1]} // 打乱
	h1 := ComputeMembersHash(a)
	h2 := ComputeMembersHash(b)
	mustTrue(t, h1 == h2, "hash must be order-independent: %q != %q", h1, h2)
	mustTrue(t, ComputeMembersHash(a) == h1, "hash must be deterministic")
	// 空集：空串（stable，跨调用一致）。
	mustTrue(t, ComputeMembersHash(nil) == "", "empty members hash = %q want empty", ComputeMembersHash(nil))
	mustTrue(t, ComputeMembersHash([]MemberSnapshot{}) == "", "nil/empty hash mismatch")
	// 不同成员集合 ⇒ 不同 hash。
	other := []MemberSnapshot{member(10, 100, 10, true), member(20, 200, 5, false), member(40, 400, 7, true)}
	mustTrue(t, ComputeMembersHash(other) != h1, "different member sets should hash differently")
}

// TestComputeMembersHash_Format 钉死窗口 identity 的字符串格式（升级不得静默改变）。
//
// 格式：edgeDeviceID:priority:requiredFlag; 按 edgeDeviceID ASC, priority ASC 排序。
// 变更格式会让所有窗口 identity 在升级瞬间失效（DurationSec 重新累计），必须显式评审。
func TestComputeMembersHash_Format(t *testing.T) {
	got := ComputeMembersHash([]MemberSnapshot{
		{EdgeDeviceID: 7, Priority: 3, Required: true},
		{EdgeDeviceID: 5, Priority: 9, Required: false},
	})
	const want = "5:9:0;7:3:1;"
	if got != want {
		t.Fatalf("hash format = %q want %q", got, want)
	}
}

// idsOf 是测试辅助：提取 memberID 序列用于报错。
func idsOf(ms []MemberSnapshot) []uint {
	out := make([]uint, len(ms))
	for i := range ms {
		out[i] = ms[i].MemberID
	}
	return out
}
