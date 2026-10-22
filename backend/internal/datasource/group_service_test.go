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
	"sync/atomic"
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

// ── 16. 求值期降级健康事件：写入 + 节流 + 删除组不写孤儿行（方案 §7.1 E-1/E-2）──
//
// 背景：Lead 全链路 e2e（TestSG_E3）实测 composite 因成员陈旧 unbound 时
// source_group_health 为 0 行 —— 规则 fail-closed 正确、audit 事件有，但运维看板
// (GET /source-groups/:id/health) 一片正常。本用例锁住"组视角可见"与节流。
func TestGroupRecordDegradedThrottled(t *testing.T) {
	f := newGroupFixture(t)
	d1 := f.addEdgeDevice(t, "light-1")
	d2 := f.addEdgeDevice(t, "light-2")
	group := f.createPrimaryBackup(t, d1, d2)

	healthCount := func() int {
		rows, err := f.gsvc.Health(group.ID, 50)
		if err != nil {
			t.Fatalf("Health: %v", err)
		}
		return len(rows)
	}

	// 同一 (组, 原因) 1 分钟内只写 1 条（模拟热路径每帧回调）。
	for i := 0; i < 20; i++ {
		f.now = f.now.Add(time.Second)
		f.gsvc.RecordDegraded(group.ID, "composite_member_stale")
	}
	if got := healthCount(); got != 1 {
		t.Fatalf("1 分钟内同组同原因应只写 1 条，got %d（节流失效会刷爆健康表）", got)
	}
	rows, _ := f.gsvc.Health(group.ID, 50)
	if rows[0].Event != "degraded" || !containsSub(rows[0].Message, "composite_member_stale") {
		t.Fatalf("健康事件内容错误: %+v", rows[0])
	}

	// 不同原因 ⇒ 独立节流键，立即可写。
	f.gsvc.RecordDegraded(group.ID, "below_min_members")
	if got := healthCount(); got != 2 {
		t.Fatalf("不同原因应独立写一条，got %d", got)
	}

	// 超过节流窗 ⇒ 同一原因可再写。
	f.now = f.now.Add(61 * time.Second)
	f.gsvc.RecordDegraded(group.ID, "composite_member_stale")
	if got := healthCount(); got != 3 {
		t.Fatalf("超过 1 分钟应再写一条，got %d", got)
	}

	// 带成员列表的扩展入口：成员 id 进消息。
	f.now = f.now.Add(61 * time.Second)
	f.gsvc.RecordDegradedWithMembers(group.ID, "required_member_missing", []uint{11, 22})
	rows, _ = f.gsvc.Health(group.ID, 50)
	last := rows[0]
	if !containsSub(last.Message, "members=11,22") {
		t.Fatalf("成员列表未拼入消息: %q", last.Message)
	}

	// 组已删除 ⇒ 不写孤儿健康行（回调可能晚于删除一个求值周期）。
	f.waitEvents(t, 1)
	if err := f.gsvc.DeleteGroup(group.ID); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	f.now = f.now.Add(10 * time.Minute)
	f.gsvc.RecordDegraded(group.ID, "composite_member_stale")
	var orphans int64
	if err := f.db.Model(&models.SourceGroupHealth{}).Where("group_id = ?", group.ID).Count(&orphans).Error; err != nil {
		t.Fatalf("count health: %v", err)
	}
	if orphans != 4 { // 前 4 条保留作审计，但不允许新增
		t.Fatalf("删除后不应新增健康行: got %d want 4", orphans)
	}
}

// containsSub 是测试内极小辅助（避免引入 strings 与后续冲突）。
func containsSub(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// ── 17. StalenessSec=0 归一（Lead 裁决 2026-10-22：0 ≠「不过期」，而是"永远过期"）──
//
// 背景：CreateGroupInput.StalenessSec 缺省 0 ⇒ GroupSnapshot.Staleness()=0 ⇒
// at.Sub(ts)>0 恒真 ⇒ 全体成员恒 stale ⇒ 复合规则静默永不触发（edge-analyst 全链路 e2e 实测）。
func TestGroupNormalizeStalenessOnCreate(t *testing.T) {
	f := newGroupFixture(t)
	d1 := f.addEdgeDevice(t, "light-1") // fixture IntervalMs=5000

	// 不传 StalenessSec：2×5s=10s 被下限抬到 30s。
	g1, err := f.gsvc.CreateGroup(CreateGroupInput{
		Kind: models.SourceGroupKindPrimaryBackup, Category: "illuminance",
		Members: []GroupMemberInput{{EdgeDeviceID: d1.ID}},
	})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if g1.StalenessSec != 30 {
		t.Fatalf("interval=5000 应归一为 30s（2×5s 被下限抬起），got %d", g1.StalenessSec)
	}

	// 空成员组：无 interval ⇒ 60s 兜底（不是 0）。
	empty, err := f.gsvc.CreateGroup(CreateGroupInput{
		Kind: models.SourceGroupKindPrimaryBackup, Category: "illuminance",
	})
	if err != nil {
		t.Fatalf("CreateGroup empty: %v", err)
	}
	if empty.StalenessSec != 60 {
		t.Fatalf("空成员组应兜底 60s，got %d", empty.StalenessSec)
	}

	// 60s 采集间隔 ⇒ 2×60s=120s。
	d2 := f.addEdgeDevice(t, "light-2")
	if err := f.db.Model(&models.EdgeDevice{}).Where("id = ?", d2.ID).
		Update("interval_ms", 60000).Error; err != nil {
		t.Fatalf("set interval: %v", err)
	}
	g2, err := f.gsvc.CreateGroup(CreateGroupInput{
		Kind: models.SourceGroupKindPrimaryBackup, Category: "illuminance",
		Members: []GroupMemberInput{{EdgeDeviceID: d2.ID}},
	})
	if err != nil {
		t.Fatalf("CreateGroup d2: %v", err)
	}
	if g2.StalenessSec != 120 {
		t.Fatalf("interval=60000 应归一为 120s，got %d", g2.StalenessSec)
	}

	// 显式非 0 值保留（用户可覆盖）。
	d3 := f.addEdgeDevice(t, "light-3")
	g3, err := f.gsvc.CreateGroup(CreateGroupInput{
		Kind: models.SourceGroupKindPrimaryBackup, Category: "illuminance",
		StalenessSec: 45,
		Members:      []GroupMemberInput{{EdgeDeviceID: d3.ID}},
	})
	if err != nil {
		t.Fatalf("CreateGroup explicit: %v", err)
	}
	if g3.StalenessSec != 45 {
		t.Fatalf("显式 staleness 应保留 45，got %d", g3.StalenessSec)
	}
}

func TestGroupNormalizeStalenessOnUpdate(t *testing.T) {
	f := newGroupFixture(t)
	d1 := f.addEdgeDevice(t, "light-1")
	group, err := f.gsvc.CreateGroup(CreateGroupInput{
		Kind: models.SourceGroupKindPrimaryBackup, Category: "illuminance",
		StalenessSec: 45, Members: []GroupMemberInput{{EdgeDeviceID: d1.ID}},
	})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	// 显式改成 0 ⇒ 归一为推导值（30s），不落库 0。
	zero := 0
	updated, err := f.gsvc.UpdateGroup(group.ID, UpdateGroupInput{StalenessSec: &zero})
	if err != nil {
		t.Fatalf("UpdateGroup zero: %v", err)
	}
	if updated.StalenessSec != 30 {
		t.Fatalf("UpdateGroup 传 0 应归一为 30s，got %d", updated.StalenessSec)
	}

	// 历史脏值（手工 SQL/旧版本写进 0）⇒ 任意 UpdateGroup 都必须回填，不能留着 0。
	if err := f.db.Model(&models.SourceGroup{}).Where("id = ?", group.ID).
		Update("staleness_sec", 0).Error; err != nil {
		t.Fatalf("dirty zero: %v", err)
	}
	name := "只改名字"
	updated2, err := f.gsvc.UpdateGroup(group.ID, UpdateGroupInput{Name: &name})
	if err != nil {
		t.Fatalf("UpdateGroup name: %v", err)
	}
	if updated2.StalenessSec == 0 {
		t.Fatalf("历史 0 值必须在 UpdateGroup 时回填（禁止 0 存活）")
	}
	if updated2.StalenessSec != 30 {
		t.Fatalf("回填值应为 30s，got %d", updated2.StalenessSec)
	}
}

func TestNormalizeStalenessSecTable(t *testing.T) {
	cases := []struct {
		name       string
		configured int
		intervals  []int
		want       int
	}{
		{"显式值保留", 45, []int{5000}, 45},
		{"无成员", 0, nil, 60},
		{"全零间隔", 0, []int{0, 0}, 60},
		{"5s 间隔被下限抬起", 0, []int{5000}, 30},
		{"60s 间隔", 0, []int{60000}, 120},
		{"超上限截断", 0, []int{3000000}, 3600},
	}
	for _, c := range cases {
		if got := normalizeStalenessSec(c.configured, c.intervals); got != c.want {
			t.Errorf("%s: normalizeStalenessSec(%d,%v)=%d want %d", c.name, c.configured, c.intervals, got, c.want)
		}
	}
}

// ── 18. B-3 第 1 步：0 组快速路径（热路径不产生任何 DB 查询）──
//
// 背景（impl-reviewer B-3）：生产 data_sources=0 / 组=0 时，MarkSuccess 每帧都白跑一次
// 成员 JOIN 查询。快速路径读状态机自己发布的 registry（atomic load + len），0→N 由
// publish 自动翻转，无需失效钩子。
func TestGroupFastPathZeroGroupsSkipsQueries(t *testing.T) {
	f := newGroupFixture(t)
	if got := f.reg.Len(); got != 0 {
		t.Fatalf("fixture 初始应为 0 组，got %d", got)
	}
	var queries int64
	f.db.Callback().Query().Before("gorm:query").Register("test:count_queries_zero_groups",
		func(tx *gorm.DB) { atomic.AddInt64(&queries, 1) })

	f.gsvc.MarkSuccess(1, []string{"illuminance"}, f.now)
	f.gsvc.MarkFailure(1, TriggerDeviceOffline)
	if got := atomic.LoadInt64(&queries); got != 0 {
		t.Fatalf("0 组时 MarkSuccess/MarkFailure 不得产生 DB 查询，got %d", got)
	}

	// 有组后快速路径必须自动放行（0→N 由 publish 翻转），且 R1 真正落库。
	d1 := f.addEdgeDevice(t, "light-1")
	d2 := f.addEdgeDevice(t, "light-2")
	group := f.createPrimaryBackup(t, d1, d2)
	if got := f.reg.Len(); got != 1 {
		t.Fatalf("建组后 registry.Len 应为 1，got %d", got)
	}
	atomic.StoreInt64(&queries, 0)
	f.gsvc.MarkSuccess(d1.ID, []string{"illuminance"}, f.now)
	if got := atomic.LoadInt64(&queries); got == 0 {
		t.Fatalf("有组后 MarkSuccess 必须真正查库更新（快速路径不得误杀）")
	}
	members := f.members(t, group.ID)
	var active models.SourceGroupMember
	for i := range members {
		if members[i].Status == models.SourceMemberStatusActive {
			active = members[i]
		}
	}
	if active.LastSuccess == nil {
		t.Fatalf("有组时 MarkSuccess 必须写 last_success（快速路径误杀）")
	}
}

// ── 19. B-3：registry 未注入（nil）同样短路，不 panic、不查库 ───────────────
func TestGroupFastPathNilRegistrySkipsQueries(t *testing.T) {
	db := testutil.OpenTestDB(t)
	gsvc := NewGroupService(db, Options{Now: func() time.Time { return testBase() }})
	var queries int64
	db.Callback().Query().Before("gorm:query").Register("test:count_queries_nil_registry",
		func(tx *gorm.DB) { atomic.AddInt64(&queries, 1) })

	gsvc.MarkSuccess(1, []string{"illuminance"}, testBase())
	gsvc.MarkFailure(1, TriggerDeviceOffline)
	if got := atomic.LoadInt64(&queries); got != 0 {
		t.Fatalf("registry=nil 时组状态机应短路，got %d 次查询", got)
	}
}

// ── 20. B-2：R10 冷却抑制但组已无权威 ⇒ 必须发 Next=0 + degraded 健康行 ──────
//
// 架构终审 B-1 的静默失效链：coldown 抑制切换 → active 已 error 但无新 active →
// MatchFrame 全成员 Accepts()=false → 引擎静默丢帧、规则停求值、**零审计**。
// 修法：抑制的是"切换动作"不是"失去权威"，必须发 ActiveChange{Next:0} 并落健康事件。
func TestGroupCooldownSuppressedEmitsNextZeroAndHealth(t *testing.T) {
	f := newGroupFixture(t)
	d1 := f.addEdgeDevice(t, "light-1")
	d2 := f.addEdgeDevice(t, "light-2")
	group := f.createPrimaryBackup(t, d1, d2)
	members := f.members(t, group.ID)
	f.waitEvents(t, 1)
	f.clearEvents()
	for _, m := range members {
		if err := f.db.Model(&models.SourceGroupMember{}).Where("id = ?", m.ID).
			Update("max_fail_count", 1).Error; err != nil {
			t.Fatalf("set max_fail: %v", err)
		}
	}

	// 第一次失败：正常切换 d1→d2（首条切换日志落在 now，形成冷却窗）。
	f.gsvc.MarkFailure(d1.ID, TriggerDeviceOffline)
	evs := f.waitEvents(t, 1)
	first := evs[len(evs)-1]
	if first.NextMemberID == 0 {
		t.Fatalf("首次失败应正常切换，got %+v", first)
	}
	f.clearEvents()
	revBefore, _ := f.gsvc.GetGroup(group.ID)

	// 冷却窗内第二次失败（d2 是 active）：抑制切换，但组已无权威。
	f.gsvc.MarkFailure(d2.ID, TriggerDeviceOffline)
	evs = f.waitEvents(t, 1)
	ev := evs[len(evs)-1]
	if ev.NextMemberID != 0 {
		t.Fatalf("R10 抑制时组已无 active，事件必须 Next=0，got %+v", ev)
	}
	if ev.Reason != ReasonGroupCooldownSuppressed {
		t.Fatalf("事件 reason 应为 %s，got %s", ReasonGroupCooldownSuppressed, ev.Reason)
	}
	got, _ := f.gsvc.GetGroup(group.ID)
	if got.Revision != revBefore.Revision+1 {
		t.Fatalf("失去权威必须 bump revision: got %d want %d", got.Revision, revBefore.Revision+1)
	}

	// 快照：ActiveMemberID=0 + 两个成员 MatchFrame 均不接受（引擎不会再按旧 active 过滤）。
	snap, ok := f.reg.Group(group.ID)
	if !ok {
		t.Fatalf("组快照丢失")
	}
	if snap.ActiveMemberID != 0 {
		t.Fatalf("无权威时快照 ActiveMemberID 必须为 0，got %d", snap.ActiveMemberID)
	}
	for _, d := range []uint{d1.ID, d2.ID} {
		m := f.reg.MatchFrame(d, sourceref.KindPrimaryBackup, group.ID)
		if m.Accepts() {
			t.Fatalf("无权威时设备 %d 的帧不应被接受（否则按旧 active 过滤）: %+v", d, m)
		}
	}

	// 组健康表：必须有一条 degraded 行（1min 节流键 no_active_after_cooldown）。
	health, err := f.gsvc.Health(group.ID, 50)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	found := false
	for _, h := range health {
		if h.Event == "degraded" && containsSub(h.Message, "no_active_after_cooldown") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("组健康表缺少 no_active_after_cooldown 降级行: %+v", health)
	}

	// R10 语义未破：抑制期间不新增切换日志。
	logs, err := f.gsvc.FailoverLogs(group.ID, 10)
	if err != nil {
		t.Fatalf("FailoverLogs: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("冷却窗内不得新增切换日志，got %d", len(logs))
	}
}

// ── 21. H-5：钩子通道溢出（dirty）由 RunDirtyReloader 消费，快照不会永久陈旧 ──
func TestGroupDirtyReloaderRestoresSnapshot(t *testing.T) {
	f := newGroupFixture(t)
	d1 := f.addEdgeDevice(t, "light-1")
	d2 := f.addEdgeDevice(t, "light-2")
	group := f.createPrimaryBackup(t, d1, d2)
	f.waitEvents(t, 1)

	before, ok := f.reg.Group(group.ID)
	if !ok {
		t.Fatalf("初始快照不存在")
	}
	// 模拟：钩子通道溢出丢事件（dirty 置位），同时 DB 已前进但快照仍是旧的。
	if err := f.db.Model(&models.SourceGroup{}).Where("id = ?", group.ID).
		UpdateColumn("revision", gorm.Expr("revision + 1")).Error; err != nil {
		t.Fatalf("bump revision: %v", err)
	}
	f.gsvc.dispatcher.dirty.Store(true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.gsvc.RunDirtyReloader(ctx, 5*time.Millisecond)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snap, _ := f.reg.Group(group.ID)
		if snap != nil && snap.Revision == before.Revision+1 && !f.gsvc.DirtySinceLastReload() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	snap, _ := f.reg.Group(group.ID)
	t.Fatalf("H-5：dirty 后快照未重载（registry rev=%v want %d, dirty=%v）",
		snapshotRevision(snap), before.Revision+1, f.gsvc.DirtySinceLastReload())
}

func snapshotRevision(s *sourceref.GroupSnapshot) int64 {
	if s == nil {
		return -1
	}
	return s.Revision
}






