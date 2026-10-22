package sourceref

import (
	"testing"
	"time"
)

// 固定基准时间：全部测试必须确定性（不依赖 time.Now）。
var baseTime = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

// member 构造成员快照（默认运行态 active）。
func member(memberID, edgeDeviceID uint, priority int, required bool) MemberSnapshot {
	return MemberSnapshot{
		MemberID:     memberID,
		EdgeDeviceID: edgeDeviceID,
		Priority:     priority,
		Required:     required,
		Status:       MemberStatusActive,
		LastSuccess:  baseTime,
	}
}

// okComponent 返回 StateOK 分量。
func okComponent(m MemberSnapshot, value float64) Component {
	return Component{
		MemberID:     m.MemberID,
		EdgeDeviceID: m.EdgeDeviceID,
		Required:     m.Required,
		Value:        value,
		At:           baseTime,
		State:        StateOK,
	}
}

// evalAllOK 给所有成员返回给定值表里对应值（缺省 0），StateOK。
func evalAllOK(values map[uint]float64) EvalMemberFunc {
	return func(m MemberSnapshot) Component {
		return okComponent(m, values[m.MemberID])
	}
}

// newGroup 构造组快照（成员顺序即传入顺序，调用方负责按契约排好）。
func newGroup(kind, combine, onDegrade string, minMembers int, activeMemberID uint, members ...MemberSnapshot) *GroupSnapshot {
	return &GroupSnapshot{
		ID:             1,
		Kind:           kind,
		Category:       "power",
		Combine:        combine,
		MinMembers:     minMembers,
		StalenessSec:   60,
		OnDegrade:      onDegrade,
		Enabled:        true,
		Revision:       7,
		TransitionSeq:  3,
		ActiveMemberID: activeMemberID,
		Members:        members,
		MembersHash:    ComputeMembersHash(members),
	}
}

// mustTrue 是测试辅助：条件为假直接 Fatal（带上下文）。
func mustTrue(t *testing.T, cond bool, format string, args ...any) {
	t.Helper()
	if !cond {
		t.Fatalf(format, args...)
	}
}
