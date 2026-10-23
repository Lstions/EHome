package datasource

// 主备来源的**随机操作序列不变量验证**。
//
// # 历史与变更（重要）
//
// 本文件原名 group_verify_diff_test.go，原本做的是「legacy data_sources 状态机
// vs 来源组适配器」的**等价性差分**：同一随机操作序列打在两套独立实现上，
// 逐步比较可观测状态。
//
// 2026-10-23（M3，数据流架构 v1 §六）legacy 状态机被**删除** —— 主备语义现在
// 只有来源组一套（用户要求「只有来源组支持主备」）。因此本文件的语义变为：
//
//   **单实现对随机操作序列的不变量验证**。
//
// # 为什么保留而不是连同 legacy 一起删掉
//
// 原差分测试的价值有两层：
//   (a) 证明两套实现判定一致 —— 随 legacy 删除而失效（没有第二套可比了）；
//   (b) 用**随机操作序列**（40 步 × 3 seed）覆盖人工不会枚举到的操作组合，
//       并在每一步断言关键不变量 —— 这一层**仍然有效且不可替代**。
//
// 固定用例（TestGroupMarkFailureFailover 等）只覆盖设计者想到的路径；
// 随机序列能撞出"先 deactivate 再 failure 再 reset 再 scan"这类组合态 bug。
// 所以保留 (b)，把断言从"与 legacy 相等"改为"单 active 不变量 + 状态自洽"。
//
// # 断言的不变量（每一步都查）
//
//  1. **单 active 不变量**：主备组任何时刻至多一个 active 成员。
//     这是 R1–R11 全部规则的根本安全属性 —— 违反它意味着「两个权威同时供数」。
//  2. **状态枚举合法**：成员 status ∈ {active, standby, error, disabled}。
//  3. **fail_count 非负**。
//
// 这些不变量**不依赖具体判定阈值**，所以将来调整 R3 候选序或 R10 冷却期时，
// 本测试仍能发现真正危险的变更（改坏不变量），而不会因为阈值变化误报。

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/sourceref"

	"gorm.io/gorm"
)

// groupWorld 是一个来源组世界的测试视角。
type groupWorld struct {
	gsvc    *GroupService
	reg     *sourceref.Registry
	db      *gorm.DB
	groupID uint
	memIDs  map[uint]uint // edgeDeviceID -> member row ID
}

// diffOp 是一次操作。
type diffOp struct {
	kind string // success|failure|scan|activate|deactivate|reset
	edge uint   // 目标边缘设备（scan 忽略）
}

// setupGroupOnlyWorld 建一个主备组世界（3 台设备，优先级 100/90/80）。
//
// 时钟由调用方注入（*clock），让 Cooldown / MinResidency 这类时间相关判定
// 在随机序列中可控 —— 否则测试会依赖真实时间流逝，无法复现。
func setupGroupOnlyWorld(t *testing.T, clock *time.Time) *groupWorld {
	t.Helper()
	nowFn := func() time.Time { return *clock }
	opts := Options{Cooldown: 5 * time.Minute, MinResidency: 2 * time.Minute,
		Staleness: 5 * time.Minute, ScanInterval: time.Hour, Now: nowFn}
	const category = "illuminance"
	priorities := []int{100, 90, 80}
	edges := make([]uint, len(priorities))
	for i := range edges {
		edges[i] = uint(i + 1)
	}

	gdb := openVerifyDB(t)
	reg := sourceref.NewRegistry()
	gw := &groupWorld{gsvc: NewGroupService(gdb, opts), reg: reg, db: gdb, memIDs: map[uint]uint{}}
	gw.gsvc.SetGroupRegistry(reg)
	gw.gsvc.SetNotifier(func(models.Notification) {})
	members := make([]GroupMemberInput, 0, len(edges))
	for i, edge := range edges {
		ed := models.EdgeDevice{ID: edge, Name: fmt.Sprintf("edge-%d", edge), NodeID: "node-1",
			ChannelID: 1, IntervalMs: 5000, Enabled: true, Type: "prs3001"}
		if err := gdb.Create(&ed).Error; err != nil {
			t.Fatalf("create edge device: %v", err)
		}
		if err := gdb.Create(&models.SensorFieldObs{DeviceID: ed.ID, SensorName: category}).Error; err != nil {
			t.Fatalf("create obs: %v", err)
		}
		members = append(members, GroupMemberInput{EdgeDeviceID: ed.ID, Priority: priorities[i]})
	}
	groupRow, err := gw.gsvc.CreateGroup(CreateGroupInput{
		Name: "invariant-group", Kind: models.SourceGroupKindPrimaryBackup, Category: category,
		// StalenessSec 显式给 300s：否则 DeriveStaleness 会按采集间隔推导出 30s 默认，
		// 让「停滞判定」在随机序列里过早触发，掩盖真正的组合态 bug。
		StalenessSec: 300,
		Members:      members,
	})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	gw.groupID = groupRow.ID
	for _, m := range gw.gsvc.mustListMembers(t, groupRow.ID) {
		gw.memIDs[m.EdgeDeviceID] = m.ID
	}
	if len(gw.memIDs) != len(priorities) {
		t.Fatalf("members = %d want %d", len(gw.memIDs), len(priorities))
	}
	return gw
}

// mustListMembers 是测试辅助（避免 t 参数在循环里丢失）。
func (g *GroupService) mustListMembers(t *testing.T, groupID uint) []models.SourceGroupMember {
	t.Helper()
	ms, err := g.ListMembers(groupID)
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	return ms
}

// runGroupOp 执行一次操作。
//
// 错误一律**忽略**：随机序列会撞出"对 disabled 成员再 deactivate"这类非法组合，
// 返回错误是正确行为。我们不比较错误类别（那需要第二套实现），
// 而是在每步之后检查不变量 —— 非法操作必须不留坏状态。
func runGroupOp(t *testing.T, gw *groupWorld, clock *time.Time, op diffOp) {
	t.Helper()
	switch op.kind {
	case "success":
		*clock = clock.Add(time.Second)
		gw.gsvc.MarkSuccess(op.edge, []string{"illuminance"}, *clock)
	case "failure":
		*clock = clock.Add(time.Second)
		gw.gsvc.MarkFailure(op.edge, TriggerDeviceOffline)
	case "scan":
		*clock = clock.Add(30 * time.Second)
		_, _ = gw.gsvc.ScanGroups(*clock)
	case "activate":
		*clock = clock.Add(time.Second)
		_, _ = gw.gsvc.ActivateGroupMember(gw.groupID, gw.memIDs[op.edge])
	case "deactivate":
		*clock = clock.Add(time.Second)
		_, _ = gw.gsvc.DeactivateGroupMember(gw.groupID, gw.memIDs[op.edge])
	case "reset":
		*clock = clock.Add(time.Second)
		_, _ = gw.gsvc.ResetGroupMember(gw.groupID, gw.memIDs[op.edge])
	default:
		t.Fatalf("unknown op %q", op.kind)
	}
}

// genGroupOps 生成确定性操作序列（同 seed 同序列，可复现）。
func genGroupOps(seed int64, steps int, gw *groupWorld) []diffOp {
	rng := rand.New(rand.NewSource(seed))
	edges := make([]uint, 0, len(gw.memIDs))
	for e := range gw.memIDs {
		edges = append(edges, e)
	}
	// 稳定顺序（map 遍历随机）。
	for i := 1; i < len(edges); i++ {
		for j := i; j > 0 && edges[j] < edges[j-1]; j-- {
			edges[j], edges[j-1] = edges[j-1], edges[j]
		}
	}
	// 偏向 failure/success（状态迁移主路径），少量手动操作与扫描。
	kinds := []string{"success", "success", "failure", "failure", "failure",
		"reset", "activate", "deactivate"}
	ops := make([]diffOp, 0, steps)
	for len(ops) < steps {
		kind := kinds[rng.Intn(len(kinds))]
		if rng.Intn(100) < 12 {
			kind = "scan"
		}
		ops = append(ops, diffOp{kind: kind, edge: edges[rng.Intn(len(edges))]})
	}
	return ops
}

// assertGroupInvariants 断言主备组的核心不变量。
//
// 为什么这些不变量值得单独测：它们跨越多条规则（R1/R2/R3/R4/R10/R11），
// 任何一条规则改坏都可能先表现为不变量被破坏。把不变量做成独立断言，
// 比逐条核对规则更容易发现"没想到的组合"。
func assertGroupInvariants(t *testing.T, gw *groupWorld, when string) {
	t.Helper()
	ms, err := gw.gsvc.ListMembers(gw.groupID)
	if err != nil {
		t.Fatalf("%s ListMembers: %v", when, err)
	}
	actives := 0
	for _, m := range ms {
		switch m.Status {
		case models.SourceMemberStatusActive:
			actives++
		case models.SourceMemberStatusStandby, models.SourceMemberStatusError,
			models.SourceMemberStatusDisabled:
			// 合法状态
		default:
			t.Fatalf("%s 成员 %d 状态非法: %q", when, m.ID, m.Status)
		}
		if m.FailCount < 0 {
			t.Fatalf("%s 成员 %d fail_count 为负: %d", when, m.ID, m.FailCount)
		}
	}
	if actives > 1 {
		t.Fatalf("%s 单 active 不变量被破坏: %d 个 active 成员（两个权威同时供数）", when, actives)
	}
}

// TestVerifyDiff_LegacyVsGroupEquivalence 保留原标题（改 name 会打断历史追溯与
// CI 报告连续性），但语义已变：现在是**单实现随机序列不变量验证**，不再是差分。
func TestVerifyDiff_LegacyVsGroupEquivalence(t *testing.T) {
	for _, seed := range []int64{1, 7, 23} {
		t.Run(fmt.Sprintf("seed_%d", seed), func(t *testing.T) {
			clock := time.Now().UTC().Add(5 * time.Minute)
			gw := setupGroupOnlyWorld(t, &clock)

			// 起点也查一次（建组后应恰有一个 active）。
			assertGroupInvariants(t, gw, "建组后")
			if got := countActive(t, gw.gsvc, gw.groupID); got != 1 {
				t.Fatalf("建组后 active=%d want 1（首成员应被置 active）", got)
			}

			ops := genGroupOps(seed, 40, gw)
			for i, op := range ops {
				runGroupOp(t, gw, &clock, op)
				assertGroupInvariants(t, gw, fmt.Sprintf("第 %d 步(%s edge=%d)后", i+1, op.kind, op.edge))
			}
			t.Logf("seed=%d 40 步全部不变量成立（末态 active 数=%d）",
				seed, countActive(t, gw.gsvc, gw.groupID))
		})
	}
}

// countActive 数当前 active 成员数（不变量断言用）。
func countActive(t *testing.T, gsvc *GroupService, groupID uint) int {
	t.Helper()
	ms, err := gsvc.ListMembers(groupID)
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	n := 0
	for _, m := range ms {
		if m.Status == models.SourceMemberStatusActive {
			n++
		}
	}
	return n
}
