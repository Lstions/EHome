package source_test

import (
	"testing"

	"ehome/backend/internal/models"
	"ehome/backend/internal/source"
	"ehome/backend/internal/sourceref"
)

// TestHandle_PrimaryBackupActiveMemberIDIsNotEdgeDeviceID 是**回归防护测试**。
//
// # 它防什么
//
// GroupSnapshot.ActiveMemberID 是**成员行 ID**（SourceGroupMember.ID），
// 不是 EdgeDevice.ID。我曾在这里写错过：直接拿 m.EdgeDeviceID == g.ActiveMemberID
// 比较，而当时测试用例里 MemberID 恰好等于 EdgeDeviceID ⇒ bug 被完全掩盖。
//
// 本测试**刻意让 MemberID ≠ EdgeDeviceID**（成员行 101/102，设备 1/2），
// 这样一旦有人再写错，active 判定会立刻失效并变红。
func TestHandle_PrimaryBackupActiveMemberIDIsNotEdgeDeviceID(t *testing.T) {
	g := &sourceref.GroupSnapshot{
		ID: 9, Kind: "primary_backup", Category: "v", Enabled: true,
		// 权威是成员行 102，它对应设备 2
		ActiveMemberID: 102,
		Members: []sourceref.MemberSnapshot{
			{MemberID: 101, EdgeDeviceID: 1, Priority: 10},
			{MemberID: 102, EdgeDeviceID: 2, Priority: 5},
		},
	}
	handles := source.Derive(source.DeriveInput{
		Devices: []models.EdgeDevice{dev(1, 10, true), dev(2, 10, true)},
		Groups:  []*sourceref.GroupSnapshot{g},
	})
	if len(handles) != 1 {
		t.Fatalf("handles = %d, want 1", len(handles))
	}
	h := handles[0]

	// 设备 2 是权威（因为其成员行 102 == ActiveMemberID）
	if !h.Accepts(2) {
		t.Error("设备 2 应 Accepts（成员行 102 是权威）—— ActiveMemberID 用错会导致这里失败")
	}
	if h.Accepts(1) {
		t.Error("设备 1 不应 Accepts（它是 standby）")
	}
	if !h.Authoritative(2) {
		t.Error("设备 2 应是权威")
	}
	if h.Authoritative(1) {
		t.Error("设备 1 不应是权威")
	}
}

// TestHandle_PrimaryBackupNoActiveMember 验证组没有 active 成员时全员 standby。
func TestHandle_PrimaryBackupNoActiveMember(t *testing.T) {
	g := &sourceref.GroupSnapshot{
		ID: 10, Kind: "primary_backup", Category: "v", Enabled: true,
		ActiveMemberID: 0, // 无权威（例如全部故障）
		Members: []sourceref.MemberSnapshot{
			{MemberID: 201, EdgeDeviceID: 5},
		},
	}
	handles := source.Derive(source.DeriveInput{
		Devices: []models.EdgeDevice{dev(5, 10, true)},
		Groups:  []*sourceref.GroupSnapshot{g},
	})
	h := handles[0]
	if h.Accepts(5) {
		t.Error("无 active 成员时不应 Accepts（fail-closed）")
	}
	if h.Authoritative(5) {
		t.Error("无 active 成员时不应是权威")
	}
}
