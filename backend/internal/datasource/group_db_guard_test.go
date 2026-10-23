package datasource

// 主备组的**数据库层单 active 防线**测试。
//
// # 为什么需要独立测这道防线
//
// 单 active 不变量有三层保护：
//   1. 应用层：切换前用 Count(active) 检查，已有 active 就跳过（group_state.go）。
//   2. **GORM 模型 tag**：SourceGroupMember.GroupID 上的
//      uniqueIndex:ux_sgm_one_active,where:status='active'。
//   3. **专用迁移**：internal/dbschema/source_group.go 的 ensureSingleActiveIndex
//      显式建部分唯一索引（用表达式索引，避免 GORM tag 在某些 PG 版本上的差异）。
//
// 实测发现（2026-10-23，M3 验证）：移除**应用层守卫**后随机不变量测试仍通过 ——
// 因为 DB 索引挡住了写入、事务回滚，状态没坏。这说明「只测可观测状态」无法
// 验证应用层守卫本身是否还在。
//
// # 一个必须记住的验证教训
//
// 我最初只变异了 (2) GORM tag，测试仍通过 —— 因为 (3) 迁移仍在建索引。
// 只有**同时**移除 (2) 与 (3) 才会红。这说明：
// **对"防线存在性"的变异测试，必须把该防线的所有创建路径都移除**，
// 否则会得到"测试无效"的错误结论（实际是变异不彻底）。
// 本测试对 (2)(3) 二者的组合失效敏感 —— 已验证（见提交说明）。
//
// # 本测试怎么变红
//
// 直接对 DB 写入第二条 active 成员：
//   - 索引存在 ⇒ 写入被拒（错误非 nil）⇒ 本测试通过；
//   - 索引被删 ⇒ 写入成功（错误 nil）⇒ 本测试失败，提示"物理防线已丢失"。

import (
	"testing"
	"time"

	"ehome/backend/internal/models"
)

// TestPrimaryBackup_DBRejectsSecondActive 验证 partial unique index 仍在生效。
func TestPrimaryBackup_DBRejectsSecondActive(t *testing.T) {
	clock := time.Now().UTC().Add(5 * time.Minute)
	gw := setupGroupOnlyWorld(t, &clock)

	ms, err := gw.gsvc.ListMembers(gw.groupID)
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	if len(ms) < 2 {
		t.Fatalf("需要至少 2 个成员，实际 %d", len(ms))
	}

	// 建组后恰有一个 active。
	activeIdx := -1
	for i, m := range ms {
		if m.Status == models.SourceMemberStatusActive {
			activeIdx = i
		}
	}
	if activeIdx < 0 {
		t.Fatal("建组后没有 active 成员 —— 前提不成立")
	}
	otherIdx := -1
	for i := range ms {
		if i != activeIdx {
			otherIdx = i
			break
		}
	}

	// 强行把另一个成员也置 active —— 必须被 DB 拒绝。
	err = gw.db.Model(&models.SourceGroupMember{}).
		Where("id = ?", ms[otherIdx].ID).
		Update("status", models.SourceMemberStatusActive).Error
	if err == nil {
		// 写入成功了 ⇒ 没有索引拦着 ⇒ 物理防线丢失。
		t.Fatalf("数据库允许同组两个 active 成员（成员 %d 与 %d）—— "+
			"ux_sgm_one_active partial unique index 已失效或丢失。\n"+
			"单 active 现在是仅靠应用层自觉，属于安全降级，必须修回索引。",
			ms[activeIdx].ID, ms[otherIdx].ID)
	}
	t.Logf("DB 正确拒绝第二条 active（索引生效）: %v", err)

	// 拒绝后状态必须未被污染。
	after, err := gw.gsvc.ListMembers(gw.groupID)
	if err != nil {
		t.Fatalf("ListMembers after: %v", err)
	}
	n := 0
	for _, m := range after {
		if m.Status == models.SourceMemberStatusActive {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("写入被拒后 active 数 = %d，want 1（状态被污染）", n)
	}
}
