package datasource

// 应用层「单 active 守卫」的行为测试。
//
// # 为什么需要它（随机测试覆盖不到）
//
// 2026-10-23（M3 验证）实测：移除 group_state.go 里「组内已有 active 则跳过切换」
// 的守卫后，随机操作序列测试**仍然全绿** —— 因为 DB 的部分唯一索引挡住了写入、
// 事务回滚，最终状态没坏。
//
// 这意味着：「可观测状态正确」≠「应用层守卫还在」。而两者的失效后果不同：
//   · 有守卫：切换前就跳过，零 DB 往返、零错误日志、零回滚。
//   · 无守卫（仅靠索引）：每次都尝试写入 → 撞索引 → 事务回滚 → 刷错误日志，
//     且如果将来索引因任何原因缺失（迁移顺序、手工 DROP、测试库差异），
//     就立刻变成真实的双 active。
//
// 因此需要一条**直接观测行为**的测试：在组内已有 active 时，
// 对一个 standby 成员的失败**不应产生任何切换日志/事件**。
// 若守卫被移除，虽最终状态相同，但会产生一次失败的写入尝试 —— 而切换日志
// 与 ActiveChange 事件都不应出现（守卫的语义就是"什么都不做"）。

import (
	"testing"
	"time"

	"ehome/backend/internal/models"
)

// TestPrimaryBackup_AppGuardSkipsWhenActiveExists 验证应用层守卫「已有 active 就不切换」。
//
// 构造：主备组 3 成员，A 是 active。让 **standby 的 B** 连续失败到熔断阈值。
// 期望：B 自己变 error，但 A 仍是唯一 active，**没有**发生切换（无 ActiveChange）。
//
// 若应用层守卫缺失：代码会尝试把另一个候选提升为 active ⇒ 撞索引 ⇒ 回滚。
// 此时 B 的 fail_count 都不会落库（整体回滚），本测试会因 B 状态不符而变红 ——
// 这正是"守卫缺失"的可观测信号。
func TestPrimaryBackup_AppGuardSkipsWhenActiveExists(t *testing.T) {
	clock := time.Now().UTC().Add(5 * time.Minute)
	gw := setupGroupOnlyWorld(t, &clock)

	var events []ActiveChange
	gw.gsvc.SetActiveChangeHook(func(ev ActiveChange) { events = append(events, ev) })

	// 建组后第一个成员（优先级最高）应为 active。
	ms, err := gw.gsvc.ListMembers(gw.groupID)
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	var active, standby *models.SourceGroupMember
	for i := range ms {
		switch ms[i].Status {
		case models.SourceMemberStatusActive:
			active = &ms[i]
		case models.SourceMemberStatusStandby:
			if standby == nil {
				standby = &ms[i]
			}
		}
	}
	if active == nil || standby == nil {
		t.Fatalf("前提不成立：需要 1 个 active + 1 个 standby，实际 active=%v standby=%v",
			active != nil, standby != nil)
	}

	// 把 standby 的熔断阈值设为 1，让它一次失败就"想"触发切换。
	if err := gw.db.Model(&models.SourceGroupMember{}).
		Where("id = ?", standby.ID).
		Update("max_fail_count", 1).Error; err != nil {
		t.Fatalf("set max_fail: %v", err)
	}
	beforeEvents := len(events)

	// 让 standby 失败（它的 edgeDeviceID）。
	*&clock = clock.Add(time.Second)
	gw.gsvc.MarkFailure(standby.EdgeDeviceID, TriggerDeviceOffline)

	// 关键断言 1：**没有**发生切换（应用层守卫应直接跳过）。
	if len(events) != beforeEvents {
		t.Fatalf("组内已有 active 时发生了切换（事件数 %d → %d）—— 应用层守卫失效",
			beforeEvents, len(events))
	}

	// 关键断言 2：原 active 仍是唯一 active。
	after, err := gw.gsvc.ListMembers(gw.groupID)
	if err != nil {
		t.Fatalf("ListMembers after: %v", err)
	}
	n, stillActive := 0, false
	for _, m := range after {
		if m.Status == models.SourceMemberStatusActive {
			n++
			if m.ID == active.ID {
				stillActive = true
			}
		}
	}
	if n != 1 || !stillActive {
		t.Fatalf("active 集合被破坏：count=%d, 原 active 仍在=%v", n, stillActive)
	}

	// 关键断言 3：standby 的失败被记录（证明这次失败确实被处理过，
	// 不是"什么都没发生"导致的假绿）。
	var got models.SourceGroupMember
	if err := gw.db.First(&got, standby.ID).Error; err != nil {
		t.Fatalf("reload standby: %v", err)
	}
	if got.FailCount == 0 && got.Status != models.SourceMemberStatusError {
		t.Fatalf("standby 的失败既没计数也没置 error —— 失败事件未被处理（假绿）")
	}
}
