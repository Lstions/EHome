package sourceref

import (
	"testing"
	"time"
)

// evalStates 按 memberID 指定 State（value 由 okValues 提供）。
func evalStates(states map[uint]string, okValues map[uint]float64) EvalMemberFunc {
	return func(m MemberSnapshot) Component {
		st, ok := states[m.MemberID]
		if !ok {
			st = StateMissing
		}
		c := Component{MemberID: m.MemberID, EdgeDeviceID: m.EdgeDeviceID, Required: m.Required, State: st, At: baseTime}
		if st == StateOK {
			c.Value = okValues[m.MemberID]
		}
		return c
	}
}

// TestCombineTruthTable12_9_2 是方案 §12.9.2 权威真值表**逐行**单测（R-2 放行条件）。
//
// 五条（2026-10-22 算子收敛为 avg/sum 后的最终版）：
//
//	① fail_closed + 任意缺席                    ⇒ unbound
//	② use_available + 含 required=true 缺席     ⇒ unbound(required_missing)
//	③ use_available + 仅 required=false 缺席且可用≥min ⇒ 合并 + degraded
//	④ use_available + 仅 required=false 缺席且可用<min ⇒ unbound(below_min_members)
//	⑤ 全部可用                                  ⇒ 按 combine 合并，无降级
func TestCombineTruthTable12_9_2(t *testing.T) {
	// 统一成员集合：m1 required（priority 30），m2 optional（priority 20），m3 optional（priority 10）。
	m1 := member(1, 101, 30, true)
	m2 := member(2, 102, 20, false)
	m3 := member(3, 103, 10, false)
	members := []MemberSnapshot{m1, m2, m3}

	tests := []struct {
		name        string
		group       *GroupSnapshot
		eval        EvalMemberFunc
		wantOK      bool
		wantValue   float64
		wantReason  string
		wantDegrade bool
	}{
		{
			// ① fail_closed + 任意缺席 ⇒ unbound（Required 不改变结果）。
			name:       "row1_fail_closed_any_missing_unbound",
			group:      newGroup(KindComposite, CombineAvg, OnDegradeFailClosed, 1, 0, members...),
			eval:       evalStates(map[uint]string{1: StateOK, 2: StateMissing, 3: StateOK}, map[uint]float64{1: 10, 3: 30}),
			wantOK:     false,
			wantReason: ReasonNoAvailableMember,
		},
		{
			// ① 变体：fail_closed 下即使 required=false 缺席也 unbound（Required 无额外作用）。
			name:       "row1b_fail_closed_optional_missing_unbound",
			group:      newGroup(KindComposite, CombineSum, OnDegradeFailClosed, 1, 0, m2, m3),
			eval:       evalStates(map[uint]string{2: StateOK, 3: StateMissing}, map[uint]float64{2: 5}),
			wantOK:     false,
			wantReason: ReasonNoAvailableMember,
		},
		{
			// ② use_available + required=true 缺席 ⇒ unbound(required_missing)。
			name:       "row2_use_available_required_missing_unbound",
			group:      newGroup(KindComposite, CombineAvg, OnDegradeUseAvailable, 1, 0, members...),
			eval:       evalStates(map[uint]string{1: StateMissing, 2: StateOK, 3: StateOK}, map[uint]float64{2: 20, 3: 30}),
			wantOK:     false,
			wantReason: ReasonRequiredMissing,
		},
		{
			// ② 变体：required=true 成员 stale/not_eligible 同样按缺席处理（不得用旧值）。
			name:       "row2b_use_available_required_stale_unbound",
			group:      newGroup(KindComposite, CombineAvg, OnDegradeUseAvailable, 1, 0, members...),
			eval:       evalStates(map[uint]string{1: StateStale, 2: StateOK, 3: StateOK}, map[uint]float64{2: 20, 3: 30}),
			wantOK:     false,
			wantReason: ReasonRequiredMissing,
		},
		{
			// ③ use_available + 仅 required=false 缺席 + 可用≥min ⇒ 合并 + degraded。
			name:        "row3_use_available_optional_missing_degraded_ok",
			group:       newGroup(KindComposite, CombineAvg, OnDegradeUseAvailable, 2, 0, members...),
			eval:        evalStates(map[uint]string{1: StateOK, 2: StateMissing, 3: StateOK}, map[uint]float64{1: 10, 3: 30}),
			wantOK:      true,
			wantValue:   20, // avg(10,30)：只对可用分量求平均
			wantDegrade: true,
		},
		{
			// ③ 变体：sum 合并可用分量（只算有的）。
			name:        "row3b_use_available_sum_only_available",
			group:       newGroup(KindComposite, CombineSum, OnDegradeUseAvailable, 1, 0, members...),
			eval:        evalStates(map[uint]string{1: StateOK, 2: StateMissing, 3: StateOK}, map[uint]float64{1: 10, 3: 30}),
			wantOK:      true,
			wantValue:   40,
			wantDegrade: true,
		},
		{
			// ④ use_available + 仅 optional 缺席 + 可用<min ⇒ unbound(below_min_members)。
			name:       "row4_use_available_below_min_unbound",
			group:      newGroup(KindComposite, CombineAvg, OnDegradeUseAvailable, 3, 0, members...),
			eval:       evalStates(map[uint]string{1: StateOK, 2: StateMissing, 3: StateMissing}, map[uint]float64{1: 10}),
			wantOK:     false,
			wantReason: ReasonBelowMinMembers,
		},
		{
			// ⑤ 全部可用 ⇒ 按 combine 合并，无降级标记。
			name:        "row7_all_available_merge_no_degrade",
			group:       newGroup(KindComposite, CombineAvg, OnDegradeFailClosed, 1, 0, m1, m2, m3),
			eval:        evalStates(map[uint]string{1: StateOK, 2: StateOK, 3: StateOK}, map[uint]float64{1: 10, 2: 20, 3: 30}),
			wantOK:      true,
			wantValue:   20,
			wantDegrade: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CombineGroup(tc.group, tc.eval)
			if got.OK != tc.wantOK {
				t.Fatalf("OK = %v want %v (reason=%q value=%v)", got.OK, tc.wantOK, got.Reason, got.Value)
			}
			if tc.wantOK {
				if got.Value != tc.wantValue {
					t.Fatalf("Value = %v want %v", got.Value, tc.wantValue)
				}
				if got.Reason != "" {
					t.Fatalf("success result should have empty reason, got %q", got.Reason)
				}
			} else if got.Reason != tc.wantReason {
				t.Fatalf("Reason = %q want %q", got.Reason, tc.wantReason)
			}
			if got.Degraded != tc.wantDegrade {
				t.Fatalf("Degraded = %v want %v", got.Degraded, tc.wantDegrade)
			}
			// 结果必须回填 revision/transition（窗口 identity 依据）。
			if got.Revision != tc.group.Revision || got.TransitionSeq != tc.group.TransitionSeq {
				t.Fatalf("revision passthrough = (%d,%d) want (%d,%d)",
					got.Revision, got.TransitionSeq, tc.group.Revision, tc.group.TransitionSeq)
			}
		})
	}
}

// TestCombineGroupPrimaryBackup 覆盖主备组：只看权威成员。
func TestCombineGroupPrimaryBackup(t *testing.T) {
	active := member(1, 101, 30, true)
	standby := member(2, 102, 20, false)
	members := []MemberSnapshot{active, standby}

	tests := []struct {
		name       string
		group      *GroupSnapshot
		eval       EvalMemberFunc
		wantOK     bool
		wantValue  float64
		wantReason string
	}{
		{
			name:       "no_active_unbound",
			group:      newGroup(KindPrimaryBackup, "", OnDegradeFailClosed, 1, 0, members...),
			eval:       evalAllOK(map[uint]float64{1: 42, 2: 43}),
			wantOK:     false,
			wantReason: ReasonNoActive,
		},
		{
			name:      "active_ok_uses_active_value",
			group:     newGroup(KindPrimaryBackup, "", OnDegradeFailClosed, 1, 1, members...),
			eval:      evalAllOK(map[uint]float64{1: 42, 2: 43}),
			wantOK:    true,
			wantValue: 42,
		},
		{
			name:       "active_stale_unbound",
			group:      newGroup(KindPrimaryBackup, "", OnDegradeFailClosed, 1, 1, members...),
			eval:       evalStates(map[uint]string{1: StateStale, 2: StateOK}, map[uint]float64{2: 43}),
			wantOK:     false,
			wantReason: ReasonActiveStale,
		},
		{
			name:       "active_missing_unbound",
			group:      newGroup(KindPrimaryBackup, "", OnDegradeFailClosed, 1, 1, members...),
			eval:       evalStates(map[uint]string{1: StateMissing, 2: StateOK}, map[uint]float64{2: 43}),
			wantOK:     false,
			wantReason: ReasonNoAvailableMember,
		},
		{
			name:       "active_not_eligible_unbound",
			group:      newGroup(KindPrimaryBackup, "", OnDegradeFailClosed, 1, 1, members...),
			eval:       evalStates(map[uint]string{1: StateNotEligible, 2: StateOK}, map[uint]float64{2: 43}),
			wantOK:     false,
			wantReason: ReasonNoAvailableMember,
		},
		{
			// standby 帧无关：即使 standby StateOK 且 active 缺席也不得回退（S1 拼接面）。
			name:       "standby_ok_does_not_rescue_active",
			group:      newGroup(KindPrimaryBackup, "", OnDegradeFailClosed, 1, 1, members...),
			eval:       evalStates(map[uint]string{1: StateMissing, 2: StateOK}, map[uint]float64{2: 99}),
			wantOK:     false,
			wantReason: ReasonNoAvailableMember,
		},
		{
			// active 指向不在成员列表里的 ID（数据不一致）⇒ fail-closed。
			name:       "active_not_in_members_fail_closed",
			group:      newGroup(KindPrimaryBackup, "", OnDegradeFailClosed, 1, 99, members...),
			eval:       evalAllOK(map[uint]float64{1: 42, 2: 43}),
			wantOK:     false,
			wantReason: ReasonNoActive,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CombineGroup(tc.group, tc.eval)
			if got.OK != tc.wantOK {
				t.Fatalf("OK = %v want %v (reason=%q)", got.OK, tc.wantOK, got.Reason)
			}
			if tc.wantOK {
				if got.Value != tc.wantValue {
					t.Fatalf("Value = %v want %v", got.Value, tc.wantValue)
				}
			} else if got.Reason != tc.wantReason {
				t.Fatalf("Reason = %q want %q", got.Reason, tc.wantReason)
			}
			mustTrue(t, !got.Degraded, "primary_backup must never be Degraded")
		})
	}
}

// TestCombineGroupGuards 覆盖 nil/disabled/空成员/未知 kind 的 fail-closed 前置。
func TestCombineGroupGuards(t *testing.T) {
	// nil 组：group_missing，无分量。
	got := CombineGroup(nil, evalAllOK(nil))
	mustTrue(t, !got.OK && got.Reason == ReasonGroupMissing, "nil group = %+v", got)
	mustTrue(t, len(got.Components) == 0, "nil group should have no components")

	// 禁用组：group_disabled（即使成员全可用）。
	disabled := newGroup(KindComposite, CombineAvg, OnDegradeUseAvailable, 1, 0, member(1, 101, 10, false))
	disabled.Enabled = false
	got = CombineGroup(disabled, evalAllOK(map[uint]float64{1: 5}))
	mustTrue(t, !got.OK && got.Reason == ReasonGroupDisabled, "disabled group = %+v", got)
	mustTrue(t, got.Revision == disabled.Revision && got.TransitionSeq == disabled.TransitionSeq,
		"disabled result should still carry revision identity")

	// 空成员：no_available_member。
	empty := newGroup(KindComposite, CombineAvg, OnDegradeUseAvailable, 1, 0)
	got = CombineGroup(empty, evalAllOK(nil))
	mustTrue(t, !got.OK && got.Reason == ReasonNoAvailableMember, "empty group = %+v", got)

	// 未知 kind：fail-closed（group_missing）。
	unknown := newGroup("bogus", CombineAvg, OnDegradeUseAvailable, 1, 0, member(1, 101, 10, false))
	got = CombineGroup(unknown, evalAllOK(map[uint]float64{1: 5}))
	mustTrue(t, !got.OK && got.Reason == ReasonGroupMissing, "unknown kind = %+v", got)
}

// TestCombineOperators 覆盖保留算子 avg/sum 的取值，并钉住
// 「已移除算子（max/min/first_available）按未知算子 fail-closed」。
func TestCombineOperators(t *testing.T) {
	members := []MemberSnapshot{member(1, 101, 30, false), member(2, 102, 20, false), member(3, 103, 10, false)}
	values := map[uint]float64{1: 10, 2: 20, 3: 30}

	tests := []struct {
		combine string
		want    float64
	}{
		{CombineAvg, 20},
		{CombineSum, 60},
	}
	for _, tc := range tests {
		t.Run(tc.combine, func(t *testing.T) {
			g := newGroup(KindComposite, tc.combine, OnDegradeFailClosed, 1, 0, members...)
			got := CombineGroup(g, evalAllOK(values))
			mustTrue(t, got.OK, "%s should be OK, got %+v", tc.combine, got)
			if got.Value != tc.want {
				t.Fatalf("%s value = %v want %v", tc.combine, got.Value, tc.want)
			}
		})
	}

	// 已移除算子 + 未知算子：即使全部可用也 fail-closed（不静默退化成别的语义）。
	for _, removed := range []string{"max", "min", "first_available", "bogus"} {
		t.Run("rejected_"+removed, func(t *testing.T) {
			g := newGroup(KindComposite, removed, OnDegradeFailClosed, 1, 0, members...)
			got := CombineGroup(g, evalAllOK(values))
			mustTrue(t, !got.OK && got.Reason == ReasonNoAvailableMember,
				"removed/unknown combine %q must be fail-closed, got %+v", removed, got)
		})
	}
}

// TestCombineComponentNormalization 覆盖 eval 责任边界：身份字段以快照为准，
// 空 State 归一为 missing，eval==nil ⇒ 全部 missing。
func TestCombineComponentNormalization(t *testing.T) {
	m1 := member(1, 101, 10, true)
	g := newGroup(KindComposite, CombineAvg, OnDegradeUseAvailable, 1, 0, m1)

	// eval 返回错误身份字段：必须被快照回填（防串成员）。
	evil := func(m MemberSnapshot) Component {
		return Component{MemberID: 999, EdgeDeviceID: 999, Required: false, Value: 7, State: StateOK}
	}
	got := CombineGroup(g, evil)
	mustTrue(t, got.OK, "evil eval should still be OK: %+v", got)
	mustTrue(t, len(got.Components) == 1, "components = %+v", got.Components)
	c := got.Components[0]
	mustTrue(t, c.MemberID == 1 && c.EdgeDeviceID == 101 && c.Required, "identity backfill failed: %+v", c)
	mustTrue(t, c.Value == 7, "value should be preserved: %+v", c)

	// 空 State ⇒ missing。
	emptyState := func(m MemberSnapshot) Component { return Component{Value: 1} }
	got = CombineGroup(g, emptyState)
	mustTrue(t, !got.OK && got.Reason == ReasonRequiredMissing, "empty state should be missing: %+v", got)

	// eval == nil ⇒ 全部 missing（复合组 required 缺席 ⇒ unbound）。
	got = CombineGroup(g, nil)
	mustTrue(t, !got.OK && got.Reason == ReasonRequiredMissing, "nil eval = %+v", got)
	mustTrue(t, len(got.Components) == 1 && got.Components[0].State == StateMissing, "nil eval components = %+v", got.Components)
}

// TestCombineStateNotConfigured 覆盖 StateNotConfigured 视为缺席（fail-closed）。
func TestCombineStateNotConfigured(t *testing.T) {
	members := []MemberSnapshot{member(1, 101, 10, false), member(2, 102, 5, false)}
	failClosed := newGroup(KindComposite, CombineSum, OnDegradeFailClosed, 1, 0, members...)
	got := CombineGroup(failClosed, evalStates(map[uint]string{1: StateNotConfigured, 2: StateOK}, map[uint]float64{2: 5}))
	mustTrue(t, !got.OK, "not_configured must be treated as missing: %+v", got)

	useAvailable := newGroup(KindComposite, CombineSum, OnDegradeUseAvailable, 1, 0, members...)
	got = CombineGroup(useAvailable, evalStates(map[uint]string{1: StateNotConfigured, 2: StateOK}, map[uint]float64{2: 5}))
	mustTrue(t, got.OK && got.Degraded && got.Value == 5, "use_available with not_configured = %+v", got)
}

// TestCombineTimeIndependence 覆盖纯函数约束：同一输入重复求值结果完全一致
// （combine.go 不得调用 time.Now / 不得读外部状态）。
func TestCombineTimeIndependence(t *testing.T) {
	members := []MemberSnapshot{member(1, 101, 10, false), member(2, 102, 5, false)}
	g := newGroup(KindComposite, CombineAvg, OnDegradeUseAvailable, 1, 0, members...)
	eval := evalAllOK(map[uint]float64{1: 10, 2: 20})

	first := CombineGroup(g, eval)
	for i := 0; i < 100; i++ {
		got := CombineGroup(g, eval)
		if got.OK != first.OK || got.Value != first.Value || got.Degraded != first.Degraded {
			t.Fatalf("non-deterministic Combine at iteration %d: %+v vs %+v", i, got, first)
		}
	}
}

// TestCombineResultComponentsOrder 覆盖分量顺序 = 快照成员顺序（供审计 members_json 对齐）。
func TestCombineResultComponentsOrder(t *testing.T) {
	members := []MemberSnapshot{member(3, 303, 10, false), member(1, 101, 20, false), member(2, 202, 5, false)}
	g := newGroup(KindComposite, CombineSum, OnDegradeFailClosed, 1, 0, members...)
	got := CombineGroup(g, evalAllOK(map[uint]float64{3: 1, 1: 2, 2: 3}))
	mustTrue(t, got.OK, "expected OK: %+v", got)
	if len(got.Components) != len(members) {
		t.Fatalf("components = %d want %d", len(got.Components), len(members))
	}
	for i := range members {
		if got.Components[i].MemberID != members[i].MemberID {
			t.Fatalf("components[%d].MemberID=%d want %d", i, got.Components[i].MemberID, members[i].MemberID)
		}
	}
}

// TestCombineAvgPrecision 覆盖 avg 的浮点求和（避免整除截断）。
func TestCombineAvgPrecision(t *testing.T) {
	members := []MemberSnapshot{member(1, 101, 10, false), member(2, 102, 5, false), member(3, 103, 1, false)}
	g := newGroup(KindComposite, CombineAvg, OnDegradeFailClosed, 1, 0, members...)
	got := CombineGroup(g, evalAllOK(map[uint]float64{1: 1, 2: 2, 3: 2}))
	mustTrue(t, got.OK, "expected OK: %+v", got)
	if got.Value != 5.0/3.0 {
		t.Fatalf("avg = %v want %v", got.Value, 5.0/3.0)
	}
}

// TestCombineStalenessBoundary 覆盖陈旧判定边界（=阈值为陈旧，>阈值为陈旧；
// 边界值本身由 EvalMemberFunc 负责，Combine 只透传），并钉住 stale 不得回退为有效值。
func TestCombineStalenessBoundary(t *testing.T) {
	m := member(1, 101, 10, false)
	g := newGroup(KindComposite, CombineAvg, OnDegradeFailClosed, 1, 0, m)
	_ = time.Second // 时间语义由 eval 负责；Combine 无 time.Now

	got := CombineGroup(g, func(mm MemberSnapshot) Component {
		return Component{State: StateStale, Value: 999, At: baseTime.Add(-time.Hour)}
	})
	mustTrue(t, !got.OK, "stale component must not be used as valid value: %+v", got)
	mustTrue(t, got.Reason == ReasonNoAvailableMember, "stale reason = %q", got.Reason)
}
