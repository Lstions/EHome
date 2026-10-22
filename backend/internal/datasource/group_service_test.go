package datasource

// 组适配器单测（task-28 / 方案 v0.3 §12.9.1 F-1/F-2/F-3 + N-21 自愈）。
//
// 重点（对应放行条件）：
//   - R-1 F-1 revision 白名单：连续 MarkSuccess 不 bump；active 迁移必须 +1；
//   - R-2 F-2/V-3：first_available 归一 + 自相矛盾 400；
//   - R-4 F-5：被规则引用的组删除 409；
//   - N-21：R4 降级后无 active 的组必须能被 60s 扫描自愈；
//   - 快照：唯一发布者（组变化后 registry 立即可见）。

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/sourceref"
	"ehome/backend/testutil"

	"gorm.io/gorm"
)

// groupFixture 造两台可用边缘设备 + illuminance 观测（V-5 只能对已上报类别建组）。
type groupFixture struct {
	db   *gorm.DB
	gsvc *GroupService
	reg  *sourceref.Registry
	now  time.Time

	mu   sync.Mutex
	evs  []ActiveChange
	stop context.CancelFunc
}

func newGroupFixture(t *testing.T) *groupFixture {
	t.Helper()
	db := testutil.OpenTestDB(t)
	f := &groupFixture{db: db, reg: sourceref.NewRegistry(), now: testBase()}
	f.gsvc = NewGroupService(db, Options{
		Cooldown:     5 * time.Minute,
		MinResidency: 2 * time.Minute,
		Staleness:    5 * time.Minute,
		ScanInterval: time.Hour,
		Now:          func() time.Time { return f.now },
	})
	f.gsvc.SetGroupRegistry(f.reg)
	f.gsvc.SetActiveChangeHook(func(ev ActiveChange) {
		f.mu.Lock()
		f.evs = append(f.evs, ev)
		f.mu.Unlock()
	})
	ctx, cancel := context.WithCancel(context.Background())
	f.stop = cancel
	go f.gsvc.RunDispatcher(ctx)
	t.Cleanup(cancel)
	return f
}

// addEdgeDevice 造一台 Enabled 的 prs3001 光照设备，并写一条 illuminance 观测。
func (f *groupFixture) addEdgeDevice(t *testing.T, name string) *models.EdgeDevice {
	t.Helper()
	ed := models.EdgeDevice{Name: name, NodeID: "node-1", ChannelID: 1, IntervalMs: 5000, Enabled: true, Type: "prs3001"}
	if err := f.db.Create(&ed).Error; err != nil {
		t.Fatalf("create edge device: %v", err)
	}
	obs := models.SensorFieldObs{DeviceID: ed.ID, SensorName: "illuminance"}
	if err := f.db.Create(&obs).Error; err != nil {
		t.Fatalf("create sensor field obs: %v", err)
	}
	return &ed
}

func (f *groupFixture) events() []ActiveChange {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]ActiveChange, len(f.evs))
	copy(out, f.evs)
	return out
}

func (f *groupFixture) clearEvents() {
	f.mu.Lock()
	f.evs = nil
	f.mu.Unlock()
}

func (f *groupFixture) waitEvents(t *testing.T, n int) []ActiveChange {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if evs := f.events(); len(evs) >= n {
			return evs
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("wait active-change events: got %d, want >=%d", len(f.events()), n)
	return nil
}

func (f *groupFixture) createPrimaryBackup(t *testing.T, devs ...*models.EdgeDevice) *models.SourceGroup {
	t.Helper()
	members := make([]GroupMemberInput, 0, len(devs))
	for i, d := range devs {
		members = append(members, GroupMemberInput{EdgeDeviceID: d.ID, Priority: 100 - i*10})
	}
	row, err := f.gsvc.CreateGroup(CreateGroupInput{
		Name: "光照主备", Kind: models.SourceGroupKindPrimaryBackup,
		Category: "illuminance", Members: members,
	})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	return row
}

func (f *groupFixture) members(t *testing.T, groupID uint) []models.SourceGroupMember {
	t.Helper()
	members, err := f.gsvc.ListMembers(groupID)
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	return members
}

// ── 1. 创建：首成员 active、快照发布、MembersHash ──────────────────────────
func TestGroupCreateFirstMemberActiveAndSnapshot(t *testing.T) {
	f := newGroupFixture(t)
	d1 := f.addEdgeDevice(t, "light-1")
	d2 := f.addEdgeDevice(t, "light-2")
	group := f.createPrimaryBackup(t, d1, d2)

	if group.Revision != 1 {
		t.Fatalf("创建首个 active 应 bump 一次 revision，got %d", group.Revision)
	}
	members := f.members(t, group.ID)
	if len(members) != 2 {
		t.Fatalf("members=%d, want 2", len(members))
	}
	if members[0].Status != models.SourceMemberStatusActive {
		t.Fatalf("priority 最高的成员应为 active，got status=%s", members[0].Status)
	}
	if members[1].Status != models.SourceMemberStatusStandby {
		t.Fatalf("第二成员应为 standby，got %s", members[1].Status)
	}

	snap, ok := f.reg.Group(group.ID)
	if !ok {
		t.Fatalf("组快照未发布到 registry（唯一发布者失效）")
	}
	if snap.ActiveMemberID != members[0].ID {
		t.Fatalf("snapshot.ActiveMemberID=%d, want %d", snap.ActiveMemberID, members[0].ID)
	}
	if snap.MembersHash == "" || snap.Revision != group.Revision {
		t.Fatalf("快照 hash/revision 异常: hash=%q rev=%d", snap.MembersHash, snap.Revision)
	}
	// MembersHash 只含静态集合：改运行态不得改变 hash（F-1）。
	hashBefore := snap.MembersHash
	f.now = f.now.Add(time.Minute)
	f.gsvc.MarkSuccess(d1.ID, []string{"illuminance"}, f.now)
	snap2, _ := f.reg.Group(group.ID)
	if snap2.MembersHash != hashBefore {
		t.Fatalf("MembersHash 不应包含运行态: before=%q after=%q", hashBefore, snap2.MembersHash)
	}
}

// ── 2. F-1 反向断言：连续 MarkSuccess 不得 bump revision ───────────────────
func TestGroupMarkSuccessDoesNotBumpRevision(t *testing.T) {
	f := newGroupFixture(t)
	d1 := f.addEdgeDevice(t, "light-1")
	d2 := f.addEdgeDevice(t, "light-2")
	group := f.createPrimaryBackup(t, d1, d2)
	rev := group.Revision

	for i := 0; i < 100; i++ {
		f.now = f.now.Add(time.Second)
		f.gsvc.MarkSuccess(d1.ID, []string{"illuminance"}, f.now)
	}
	got, err := f.gsvc.GetGroup(group.ID)
	if err != nil {
		t.Fatalf("GetGroup: %v", err)
	}
	if got.Revision != rev {
		t.Fatalf("连续 100 次 MarkSuccess 后 revision 必须不变（F-1/SG-13），got %d want %d", got.Revision, rev)
	}
	m := f.members(t, group.ID)
	if m[0].LastSuccess == nil {
		t.Fatalf("MarkSuccess 未写 last_success")
	}
	// error → standby（R1），且不抢占 active。
	if err := f.db.Model(&models.SourceGroupMember{}).Where("id = ?", m[1].ID).
		Update("status", models.SourceMemberStatusError).Error; err != nil {
		t.Fatalf("set error: %v", err)
	}
	f.gsvc.MarkSuccess(d2.ID, []string{"illuminance"}, f.now.Add(time.Second))
	m = f.members(t, group.ID)
	if m[1].Status != models.SourceMemberStatusStandby {
		t.Fatalf("R1: error 成员成功后应回到 standby，got %s", m[1].Status)
	}
}

// ── 3. R2/R3：达阈值自动切换 + ActiveChange ────────────────────────────────
func TestGroupMarkFailureFailover(t *testing.T) {
	f := newGroupFixture(t)
	d1 := f.addEdgeDevice(t, "light-1")
	d2 := f.addEdgeDevice(t, "light-2")
	group := f.createPrimaryBackup(t, d1, d2)
	members := f.members(t, group.ID)
	revBefore := group.Revision
	f.waitEvents(t, 1) // 创建事件是异步分发，先等它落地再清（避免竞态）
	f.clearEvents()

	// 阈值降为 1，便于单次失败即熔断。
	if err := f.db.Model(&models.SourceGroupMember{}).Where("id = ?", members[0].ID).
		Update("max_fail_count", 1).Error; err != nil {
		t.Fatalf("set max_fail: %v", err)
	}
	f.gsvc.MarkFailure(d1.ID, TriggerDeviceOffline)

	evs := f.waitEvents(t, 1)
	ev := evs[0]
	if ev.PrevMemberID != members[0].ID || ev.NextMemberID != members[1].ID {
		t.Fatalf("事件成员错误: %+v", ev)
	}
	if ev.Reason != ReasonGroupAuto || ev.Trigger != TriggerDeviceOffline {
		t.Fatalf("事件原因错误: %+v", ev)
	}
	got, _ := f.gsvc.GetGroup(group.ID)
	if got.Revision != revBefore+1 {
		t.Fatalf("active 迁移必须 bump revision: got %d want %d", got.Revision, revBefore+1)
	}
	members = f.members(t, group.ID)
	if members[0].Status != models.SourceMemberStatusError || members[1].Status != models.SourceMemberStatusActive {
		t.Fatalf("切换后状态错误: %s/%s", members[0].Status, members[1].Status)
	}
}

// ── 4. R4：无候选降级必须发 Next=0（旧版只发通知，是本方案点名的危险点）──
func TestGroupMarkFailureDegradedEmitsNextZero(t *testing.T) {
	f := newGroupFixture(t)
	d1 := f.addEdgeDevice(t, "light-1")
	d2 := f.addEdgeDevice(t, "light-2")
	group := f.createPrimaryBackup(t, d1, d2)
	members := f.members(t, group.ID)
	f.waitEvents(t, 1) // 创建事件异步分发，先等落地再清
	f.clearEvents()
	for _, m := range members {
		if err := f.db.Model(&models.SourceGroupMember{}).Where("id = ?", m.ID).
			Update("max_fail_count", 1).Error; err != nil {
			t.Fatalf("set max_fail: %v", err)
		}
	}
	// 冷静期设为 0 会回落到默认值；这里推进时间越过冷却窗，验证两次切换。
	f.now = f.now.Add(10 * time.Minute)
	f.gsvc.MarkFailure(d1.ID, TriggerDeviceOffline) // → d2 active
	f.now = f.now.Add(10 * time.Minute)
	f.gsvc.MarkFailure(d2.ID, TriggerDeviceOffline) // → 无候选 degraded

	evs := f.waitEvents(t, 2)
	last := evs[len(evs)-1]
	if last.NextMemberID != 0 {
		t.Fatalf("R4 降级事件必须 Next=0，got %+v", last)
	}
	_, ms, err := f.gsvc.GetGroupWithMembers(group.ID)
	if err != nil {
		t.Fatalf("GetGroupWithMembers: %v", err)
	}
	for _, m := range ms {
		if m.Status == models.SourceMemberStatusActive {
			t.Fatalf("降级后不应有 active，got member %d", m.ID)
		}
	}
}

// ── 5. N-21：无 active 组（R4 降级后）必须被扫描自愈 ──────────────────────
func TestGroupScanSelfHealsDegradedGroup(t *testing.T) {
	f := newGroupFixture(t)
	d1 := f.addEdgeDevice(t, "light-1")
	d2 := f.addEdgeDevice(t, "light-2")
	group := f.createPrimaryBackup(t, d1, d2)
	members := f.members(t, group.ID)
	f.waitEvents(t, 1) // 创建事件异步分发，先等落地再清
	f.clearEvents()
	for _, m := range members {
		if err := f.db.Model(&models.SourceGroupMember{}).Where("id = ?", m.ID).
			Update("max_fail_count", 1).Error; err != nil {
			t.Fatalf("set max_fail: %v", err)
		}
	}
	f.now = f.now.Add(10 * time.Minute)
	f.gsvc.MarkFailure(d1.ID, TriggerDeviceOffline)
	f.now = f.now.Add(10 * time.Minute)
	f.gsvc.MarkFailure(d2.ID, TriggerDeviceOffline)
	f.waitEvents(t, 2)

	// 备机恢复：MarkSuccess（error→standby + last_success），扫描应补选为 active。
	f.now = f.now.Add(10 * time.Minute)
	f.gsvc.MarkSuccess(d2.ID, []string{"illuminance"}, f.now)
	switched, err := f.gsvc.ScanGroups(f.now)
	if err != nil {
		t.Fatalf("ScanGroups: %v", err)
	}
	if switched != 1 {
		t.Fatalf("自愈应切换 1 次，got %d", switched)
	}
	_, ms, _ := f.gsvc.GetGroupWithMembers(group.ID)
	foundActive := false
	for _, m := range ms {
		if m.Status == models.SourceMemberStatusActive {
			foundActive = true
			if m.EdgeDeviceID != d2.ID {
				t.Fatalf("自愈应选中健康备机 d2，got edge=%d", m.EdgeDeviceID)
			}
		}
	}
	if !foundActive {
		t.Fatalf("自愈后仍无 active（N-21 失效）")
	}
	evs := f.waitEvents(t, 3) // 1 次 R3 切换 + 1 次 R4 降级 + 1 次自愈
	last := evs[len(evs)-1]
	if last.Reason != ReasonGroupSelfHeal || last.NextMemberID == 0 {
		t.Fatalf("自愈事件错误: %+v", last)
	}
}

// ── 6. V-1/V-2/V-3/D-4：composite 校验、归一、禁 active ────────────────────
func TestGroupCompositeRules(t *testing.T) {
	f := newGroupFixture(t)
	d1 := f.addEdgeDevice(t, "light-1")
	d2 := f.addEdgeDevice(t, "light-2")

	// V-1：composite 缺 combine ⇒ 400。
	if _, err := f.gsvc.CreateGroup(CreateGroupInput{
		Kind: models.SourceGroupKindComposite, Category: "illuminance",
	}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("composite 缺 combine 应 400，got %v", err)
	}
	// V-3：first_available + required=true 成员 ⇒ 400。
	required := true
	if _, err := f.gsvc.CreateGroup(CreateGroupInput{
		Kind: models.SourceGroupKindComposite, Category: "illuminance",
		Combine: models.CombineFirstAvailable,
		Members: []GroupMemberInput{{EdgeDeviceID: d1.ID, Required: &required}},
	}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("first_available+required 应 400（V-3），got %v", err)
	}
	// V-3：first_available + min_members>1 ⇒ 400。
	if _, err := f.gsvc.CreateGroup(CreateGroupInput{
		Kind: models.SourceGroupKindComposite, Category: "illuminance",
		Combine: models.CombineFirstAvailable, MinMembers: 2,
		Members: []GroupMemberInput{{EdgeDeviceID: d1.ID}},
	}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("first_available+min_members=2 应 400（V-3），got %v", err)
	}
	// D-4 归一：合法 first_available ⇒ on_degrade=use_available、min_members=1、required=false。
	group, err := f.gsvc.CreateGroup(CreateGroupInput{
		Kind: models.SourceGroupKindComposite, Category: "illuminance",
		Combine: models.CombineFirstAvailable, OnDegrade: models.OnDegradeFailClosed,
		Members: []GroupMemberInput{{EdgeDeviceID: d1.ID}, {EdgeDeviceID: d2.ID}},
	})
	if err != nil {
		t.Fatalf("CreateGroup first_available: %v", err)
	}
	if group.OnDegrade != models.OnDegradeUseAvailable || group.MinMembers != 1 {
		t.Fatalf("D-4 归一失败: on_degrade=%q min_members=%d", group.OnDegrade, group.MinMembers)
	}
	for _, m := range f.members(t, group.ID) {
		if m.Required {
			t.Fatalf("D-4：first_available 成员 required 必须归一为 false，member=%d", m.ID)
		}
	}
	// I5：composite 无 active，activate ⇒ 400。
	ms := f.members(t, group.ID)
	if _, err := f.gsvc.ActivateGroupMember(group.ID, ms[0].ID); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("composite activate 应 400（V-6），got %v", err)
	}
	_, compositeMembers, err := f.gsvc.GetGroupWithMembers(group.ID)
	if err != nil {
		t.Fatalf("GetGroupWithMembers: %v", err)
	}
	for _, m := range compositeMembers {
		if m.Status == models.SourceMemberStatusActive {
			t.Fatalf("composite 组不应有 active 成员（I5），member=%d", m.ID)
		}
	}
}

// ── 7. I6/V-6：被规则引用的组删除必须 409 ──────────────────────────────────
func TestGroupDeleteReferencedByRule(t *testing.T) {
	f := newGroupFixture(t)
	d1 := f.addEdgeDevice(t, "light-1")
	d2 := f.addEdgeDevice(t, "light-2")
	group := f.createPrimaryBackup(t, d1, d2)

	rule := models.AlertRule{SensorName: "illuminance", SourceGroupID: &group.ID}
	if err := f.db.Create(&rule).Error; err != nil {
		t.Fatalf("create alert rule: %v", err)
	}
	if err := f.gsvc.DeleteGroup(group.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("被规则引用的组删除应 409（I6），got %v", err)
	}
	if err := f.db.Delete(&rule).Error; err != nil {
		t.Fatalf("delete alert rule: %v", err)
	}
	if err := f.gsvc.DeleteGroup(group.ID); err != nil {
		t.Fatalf("解除引用后删除应成功: %v", err)
	}
	if _, ok := f.reg.Group(group.ID); ok {
		t.Fatalf("删除后快照必须从 registry 移除")
	}
}

// ── 8. N-1：composite 组 active 自检必须拒绝启动 ───────────────────────────
func TestGroupSelfCheckRejectsCompositeActive(t *testing.T) {
	f := newGroupFixture(t)
	d1 := f.addEdgeDevice(t, "light-1")
	d2 := f.addEdgeDevice(t, "light-2")
	group, err := f.gsvc.CreateGroup(CreateGroupInput{
		Name: "光照复合", Kind: models.SourceGroupKindComposite, Category: "illuminance",
		Combine: models.CombineAvg,
		Members: []GroupMemberInput{{EdgeDeviceID: d1.ID}, {EdgeDeviceID: d2.ID}},
	})
	if err != nil {
		t.Fatalf("CreateGroup composite: %v", err)
	}
	if err := f.gsvc.SelfCheck(); err != nil {
		t.Fatalf("健康库 SelfCheck 应通过: %v", err)
	}
	// 模拟 DB 直写/旧库恢复出的自相矛盾状态（绕过 API 的 active 写入）。
	members := f.members(t, group.ID)
	if err := f.db.Model(&models.SourceGroupMember{}).Where("id = ?", members[0].ID).
		Update("status", models.SourceMemberStatusActive).Error; err != nil {
		t.Fatalf("force active: %v", err)
	}
	if err := f.gsvc.SelfCheck(); err == nil {
		t.Fatalf("composite 组存在 active 时 SelfCheck 必须报错（N-1 fail-closed）")
	}
}

// ── 9. 纯决策函数：R1/R2/R3 语义（legacy 与 group 共用的唯一实现）──────────
func TestGroupDecisionHelpers(t *testing.T) {
	now := testBase()
	up := applySuccess(StatusError, now)
	if up["status"] != StatusStandby || up["fail_count"] != 0 {
		t.Fatalf("R1 error→standby: %#v", up)
	}
	if _, ok := up["fail_count"]; !ok {
		t.Fatalf("R1 必须清零 fail_count")
	}
	// 阈值 3：第 3 次失败才熔断（countFailure 的 >= 判定）。
	if next, status, reached := countFailure(1, 3, now); reached || next != 2 || status != "" {
		t.Fatalf("R2 未达阈值: next=%d status=%q reached=%v", next, status, reached)
	}
	if next, status, reached := countFailure(2, 3, now); !reached || next != 3 || status != StatusError {
		t.Fatalf("R2 达阈值: next=%d status=%q reached=%v", next, status, reached)
	}
	members := []models.SourceGroupMember{
		{ID: 3, Status: StatusStandby, Priority: 10},
		{ID: 1, Status: StatusStandby, Priority: 20},
		{ID: 2, Status: StatusActive, Priority: 99},
		{ID: 4, Status: StatusStandby, Priority: 20},
	}
	c, ok := pickCandidate(members)
	if !ok || c.ID != 1 {
		t.Fatalf("R3 候选应 priority DESC,id ASC 取第一：got %+v ok=%v", c, ok)
	}
	anchor, ok := AnchorMember(members)
	if !ok || anchor.ID != 2 {
		t.Fatalf("锚点应取 priority 最高（不看状态）: got %+v", anchor)
	}
	if got := DeriveStaleness(0, []int{5000, 0}); got != 30*time.Second {
		t.Fatalf("staleness 下限 30s: got %v", got)
	}
	if got := DeriveStaleness(0, []int{60000}); got != 2*time.Minute {
		t.Fatalf("staleness=2×interval: got %v", got)
	}
	if got := DeriveStaleness(45, nil); got != 45*time.Second {
		t.Fatalf("显式 staleness 优先: got %v", got)
	}
	if got := CooldownFloor([]int{0, 0}); got != 60*time.Second {
		t.Fatalf("cooldown 全 0 ⇒ 60s: got %v", got)
	}
	if got := CooldownFloor([]int{5000, 12000}); got != 12*time.Second {
		t.Fatalf("cooldown=max interval: got %v", got)
	}
}

// ── 10. R5 顺序铁律回归锁：deactivate active（有候选）必须成功且始终 ≤1 active ──
//
// 背景：impl-core 独立验证抓到的生产级 bug —— 先升候选再降旧 active 会在同一事务内
// 出现双 active 窗口，部分唯一索引 ux_sgm_one_active 直接报 UNIQUE constraint failed，
// 整个事务回滚（deactivate 功能不可用）。本用例为永久回归锁。
func TestGroupDeactivateActiveSingleActiveInvariant(t *testing.T) {
	f := newGroupFixture(t)
	d1 := f.addEdgeDevice(t, "light-1")
	d2 := f.addEdgeDevice(t, "light-2")
	group := f.createPrimaryBackup(t, d1, d2)
	members := f.members(t, group.ID)
	revBefore := group.Revision
	f.waitEvents(t, 1)
	f.clearEvents()

	out, err := f.gsvc.DeactivateGroupMember(group.ID, members[0].ID)
	if err != nil {
		t.Fatalf("R5 deactivate active 必须成功（顺序铁律：先降旧再升新）: %v", err)
	}
	if out.Revision != revBefore+1 {
		t.Fatalf("active 迁移必须 bump revision: got %d want %d", out.Revision, revBefore+1)
	}
	var activeCount int64
	if err := f.db.Model(&models.SourceGroupMember{}).
		Where("group_id = ? AND status = ?", group.ID, models.SourceMemberStatusActive).
		Count(&activeCount).Error; err != nil {
		t.Fatalf("count active: %v", err)
	}
	if activeCount != 1 {
		t.Fatalf("I1：切换后必须恰好 1 条 active，got %d", activeCount)
	}
	members = f.members(t, group.ID)
	if members[0].Status != models.SourceMemberStatusDisabled {
		t.Fatalf("R5：原 active 应 disabled，got %s", members[0].Status)
	}
	if members[1].Status != models.SourceMemberStatusActive {
		t.Fatalf("R5：候选应接替为 active，got %s", members[1].Status)
	}
	evs := f.waitEvents(t, 1)
	ev := evs[len(evs)-1]
	if ev.Reason != ReasonGroupManualDeactivate || ev.PrevMemberID != members[0].ID || ev.NextMemberID != members[1].ID {
		t.Fatalf("R5 事件错误: %+v", ev)
	}
	logs, err := f.gsvc.FailoverLogs(group.ID, 10)
	if err != nil || len(logs) != 1 {
		t.Fatalf("R5 必须写一条切换日志: n=%d err=%v", len(logs), err)
	}
	if logs[0].Reason != ReasonGroupManualDeactivate {
		t.Fatalf("切换日志 reason 错误: %s", logs[0].Reason)
	}
}

// ── 11. R6/R8：无候选 409；standby 直接 disabled ───────────────────────────
func TestGroupDeactivateConflictAndStandby(t *testing.T) {
	f := newGroupFixture(t)
	d1 := f.addEdgeDevice(t, "light-1")
	d2 := f.addEdgeDevice(t, "light-2")
	// R6：单成员组停用唯一 active ⇒ ErrConflict（要求先指定接替者）。
	single := f.createPrimaryBackup(t, d1)
	sm := f.members(t, single.ID)
	if _, err := f.gsvc.DeactivateGroupMember(single.ID, sm[0].ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("R6 无候选应 409，got %v", err)
	}
	// R8：standby 停用 ⇒ disabled，无需候选。
	group := f.createPrimaryBackup(t, d1, d2)
	ms := f.members(t, group.ID)
	if _, err := f.gsvc.DeactivateGroupMember(group.ID, ms[1].ID); err != nil {
		t.Fatalf("R8 停用 standby 应成功: %v", err)
	}
	ms = f.members(t, group.ID)
	if ms[1].Status != models.SourceMemberStatusDisabled {
		t.Fatalf("R8 standby 应 disabled，got %s", ms[1].Status)
	}
}

// ── 12. R9：error 重置，组内无 active 时提升 ──────────────────────────────
func TestGroupResetErrorPromotes(t *testing.T) {
	f := newGroupFixture(t)
	d1 := f.addEdgeDevice(t, "light-1")
	group := f.createPrimaryBackup(t, d1)
	members := f.members(t, group.ID)
	revBefore := group.Revision
	f.waitEvents(t, 1)
	f.clearEvents()
	if err := f.db.Model(&models.SourceGroupMember{}).Where("id = ?", members[0].ID).
		Update("status", models.SourceMemberStatusError).Error; err != nil {
		t.Fatalf("force error: %v", err)
	}
	out, err := f.gsvc.ResetGroupMember(group.ID, members[0].ID)
	if err != nil {
		t.Fatalf("R9 reset: %v", err)
	}
	if out.Revision != revBefore+1 {
		t.Fatalf("提升 active 必须 bump revision: got %d want %d", out.Revision, revBefore+1)
	}
	ms := f.members(t, group.ID)
	if ms[0].Status != models.SourceMemberStatusActive || ms[0].FailCount != 0 {
		t.Fatalf("R9 应提升为 active 且 fail_count=0: %+v", ms[0])
	}
	evs := f.waitEvents(t, 1)
	ev := evs[len(evs)-1]
	if ev.PrevMemberID != 0 || ev.NextMemberID != members[0].ID || ev.Reason != ReasonGroupReset {
		t.Fatalf("R9 事件错误: %+v", ev)
	}
}

// ── 13. F-2：删除组必须发带 GroupID/Revision/TransitionSeq 的事件（消费方按 GroupID 清窗）──
func TestGroupDeleteEmitsCompleteEvent(t *testing.T) {
	f := newGroupFixture(t)
	d1 := f.addEdgeDevice(t, "light-1")
	d2 := f.addEdgeDevice(t, "light-2")
	group := f.createPrimaryBackup(t, d1, d2)
	f.waitEvents(t, 1)
	f.clearEvents()

	if err := f.gsvc.DeleteGroup(group.ID); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	evs := f.waitEvents(t, 1)
	ev := evs[len(evs)-1]
	if ev.GroupID != group.ID || ev.Revision == 0 || ev.TransitionSeq == 0 || ev.At.IsZero() {
		t.Fatalf("F-2：删除事件字段不完整: %+v", ev)
	}
	if ev.NextMemberID != 0 || ev.Reason != ReasonGroupDelete {
		t.Fatalf("F-2：删除事件语义错误: %+v", ev)
	}
	if _, ok := f.reg.Group(group.ID); ok {
		t.Fatalf("删除后快照必须移除")
	}
}

// ── 14. F-4/F-5：改名/启停/停用 standby 不得 bump revision（白名单收敛）──
func TestGroupNonIdentityUpdateDoesNotBumpRevision(t *testing.T) {
	f := newGroupFixture(t)
	d1 := f.addEdgeDevice(t, "light-1")
	d2 := f.addEdgeDevice(t, "light-2")
	group := f.createPrimaryBackup(t, d1, d2)
	members := f.members(t, group.ID)
	rev := group.Revision

	name := "改名不改身份"
	enabled := false
	if _, err := f.gsvc.UpdateGroup(group.ID, UpdateGroupInput{Name: &name, Enabled: &enabled}); err != nil {
		t.Fatalf("UpdateGroup name/enabled: %v", err)
	}
	// F-5：停用 standby（R8）同样不 bump。
	if _, err := f.gsvc.DeactivateGroupMember(group.ID, members[1].ID); err != nil {
		t.Fatalf("deactivate standby: %v", err)
	}
	got, err := f.gsvc.GetGroup(group.ID)
	if err != nil {
		t.Fatalf("GetGroup: %v", err)
	}
	if got.Revision != rev {
		t.Fatalf("F-4/F-5：非身份变更不得 bump revision: got %d want %d", got.Revision, rev)
	}
}

// ── 15. F-6：composite deactivate 带 member_id ⇒ R8 置 disabled（领域层允许）──
func TestGroupCompositeMemberDeactivateAllowed(t *testing.T) {
	f := newGroupFixture(t)
	d1 := f.addEdgeDevice(t, "light-1")
	d2 := f.addEdgeDevice(t, "light-2")
	group, err := f.gsvc.CreateGroup(CreateGroupInput{
		Kind: models.SourceGroupKindComposite, Category: "illuminance",
		Combine: models.CombineAvg,
		Members: []GroupMemberInput{{EdgeDeviceID: d1.ID}, {EdgeDeviceID: d2.ID}},
	})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	ms := f.members(t, group.ID)
	if _, err := f.gsvc.DeactivateGroupMember(group.ID, ms[0].ID); err != nil {
		t.Fatalf("F-6：composite 成员显式停用应允许（R8）: %v", err)
	}
	ms = f.members(t, group.ID)
	if ms[0].Status != models.SourceMemberStatusDisabled {
		t.Fatalf("F-6：composite 成员应 disabled，got %s", ms[0].Status)
	}
}

