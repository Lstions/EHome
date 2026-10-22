package datasource

// 独立验证（impl-core）：数据源域组适配器 —— revision bump 白名单 + 快照不变量。
//
// 与 ds-analyst 的 group_service_test.go 相互独立（自有 fixture/断言），重点验证方案
// §12.9.1 F-1 / 放行条件 R-1：**运行态写绝不 bump revision**（否则窗口 identity 每帧变，
// DurationSec 永不满足 —— 复评最高危回归），而 active 迁移/拓扑变更必须 bump。
//
// 判据来源：
//   - F-1 bump 白名单：组创建/删除、成员新增/删除、priority、required、combine/min_members/
//     staleness_sec/on_degrade、kind/category、active 迁移 ⇒ bump；
//     MarkSuccess / MarkFailure 计数 / last_success / last_failure / fail_count ⇒ 不 bump。
//   - MembersHash 只含静态字段（priority/required/edgeDeviceID），运行态变化不得改变。

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"ehome/backend/internal/dbschema"
	"ehome/backend/internal/models"
	"ehome/backend/internal/sourceref"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// openVerifyDB 开一个**文件型** SQLite（而非 :memory:）验证库。
//
// 为什么不用 testutil.OpenTestDB（:memory:）：SQLite :memory: 是**每连接一个库**，
// 而 GroupService.UpdateGroup 在事务内仍用 g.db 做成员校验（见验证报告 F-3），
// 事务占住一条连接后 g.db 会另开连接 ⇒ 在 :memory: 上表现为 "no such table"。
// 文件型库多连接共享同一 schema，能真实验证状态机语义（生产即文件型/PG）。
func openVerifyDB(t *testing.T) *gorm.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "verify.db")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open verify sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&models.EdgeDevice{},
		&models.SensorFieldObs{},
		&models.UnifiedData{},
		&models.DataSource{},
		&models.DataSourceHealth{},
		&models.FailoverLog{},
		&models.Notification{},
		&models.SourceGroup{},
		&models.SourceGroupMember{},
		&models.SourceGroupFailoverLog{},
		&models.SourceGroupHealth{},
		&models.AutomationRule{},
		&models.AlertRule{},
	); err != nil {
		t.Fatalf("verify automigrate: %v", err)
	}
	if err := dbschema.EnsureSourceGroupSchema(db); err != nil {
		t.Fatalf("verify source_group schema: %v", err)
	}
	return db
}

// vf 是独立验证用的 fixture（不依赖 group_service_test.go 的 fixture 行为细节）。
type vf struct {
	t    *testing.T
	db   *gorm.DB
	gsvc *GroupService
	reg  *sourceref.Registry
	now  time.Time

	mu  sync.Mutex
	evs []ActiveChange
}

// newVF 造一个 fixture。now 取"当前时刻 + 5 分钟"：
//   - GORM 自动写 UpdatedAt 用真实时钟；把假时钟放在未来 5 分钟，
//     可让 R11 MinResidency（2m）稳定通过，同时 last_success 用假时钟显式写入，
//     从而让扫描/时间相关断言与真实时钟无关、可复现。
func newVF(t *testing.T) *vf {
	t.Helper()
	db := openVerifyDB(t)
	f := &vf{t: t, db: db, reg: sourceref.NewRegistry(), now: time.Now().UTC().Add(5 * time.Minute)}
	f.gsvc = NewGroupService(db, Options{
		Cooldown:     5 * time.Minute,
		MinResidency: 2 * time.Minute,
		Staleness:    5 * time.Minute,
		ScanInterval: time.Hour,
		Now:          func() time.Time { return f.now },
	})
	f.gsvc.SetGroupRegistry(f.reg)
	f.gsvc.SetNotifier(func(models.Notification) {})
	f.gsvc.SetActiveChangeHook(func(ev ActiveChange) {
		f.mu.Lock()
		f.evs = append(f.evs, ev)
		f.mu.Unlock()
	})
	ctx, cancel := context.WithCancel(context.Background())
	go f.gsvc.RunDispatcher(ctx)
	t.Cleanup(cancel)
	return f
}

func (f *vf) advance(d time.Duration) { f.now = f.now.Add(d) }

// addDevice 造一台 Enabled 的 prs3001 光照设备 + 一条 illuminance 观测。
func (f *vf) addDevice(name string) *models.EdgeDevice {
	f.t.Helper()
	ed := models.EdgeDevice{Name: name, NodeID: "node-1", ChannelID: 1, IntervalMs: 5000, Enabled: true, Type: "prs3001"}
	if err := f.db.Create(&ed).Error; err != nil {
		f.t.Fatalf("create edge device %s: %v", name, err)
	}
	if err := f.db.Create(&models.SensorFieldObs{DeviceID: ed.ID, SensorName: "illuminance"}).Error; err != nil {
		f.t.Fatalf("create sensor field obs: %v", err)
	}
	return &ed
}

// createPB 建主备组：prio 依次 100、90、80…（首个成员 active）。
func (f *vf) createPB(devs ...*models.EdgeDevice) *models.SourceGroup {
	f.t.Helper()
	members := make([]GroupMemberInput, 0, len(devs))
	for i, d := range devs {
		members = append(members, GroupMemberInput{EdgeDeviceID: d.ID, Priority: 100 - i*10})
	}
	row, err := f.gsvc.CreateGroup(CreateGroupInput{
		Name: "verify-pb", Kind: models.SourceGroupKindPrimaryBackup, Category: "illuminance", Members: members,
	})
	if err != nil {
		f.t.Fatalf("CreateGroup: %v", err)
	}
	// 创建时首成员自动 active 会发 1 条 ActiveChange（R7 反向）；等它落地后清空，
	// 让各用例的"本操作事件数"断言从零开始（异步 dispatcher 可能在 clear 之后才追加）。
	f.waitEvents(1)
	f.clearEvents()
	return row
}

func (f *vf) createComposite(combine string, devs ...*models.EdgeDevice) *models.SourceGroup {
	f.t.Helper()
	members := make([]GroupMemberInput, 0, len(devs))
	for i, d := range devs {
		members = append(members, GroupMemberInput{EdgeDeviceID: d.ID, Priority: 100 - i*10})
	}
	row, err := f.gsvc.CreateGroup(CreateGroupInput{
		Name: "verify-composite", Kind: models.SourceGroupKindComposite,
		Category: "illuminance", Combine: combine, MinMembers: 1, OnDegrade: models.OnDegradeFailClosed,
		Members: members,
	})
	if err != nil {
		f.t.Fatalf("CreateGroup composite: %v", err)
	}
	return row
}

func (f *vf) group(gid uint) models.SourceGroup {
	f.t.Helper()
	var row models.SourceGroup
	if err := f.db.First(&row, gid).Error; err != nil {
		f.t.Fatalf("reload group %d: %v", gid, err)
	}
	return row
}

func (f *vf) revision(gid uint) int64 { return f.group(gid).Revision }

func (f *vf) members(gid uint) []models.SourceGroupMember {
	f.t.Helper()
	ms, err := f.gsvc.ListMembers(gid)
	if err != nil {
		f.t.Fatalf("ListMembers(%d): %v", gid, err)
	}
	return ms
}

func (f *vf) activeMember(gid uint) (models.SourceGroupMember, bool) {
	for _, m := range f.members(gid) {
		if m.Status == models.SourceMemberStatusActive {
			return m, true
		}
	}
	return models.SourceGroupMember{}, false
}

// activeEdge 返回权威成员的 edge_device_id（0 = 无 active）。
func (f *vf) activeEdge(gid uint) uint {
	if m, ok := f.activeMember(gid); ok {
		return m.EdgeDeviceID
	}
	return 0
}

func (f *vf) setMaxFail(memberID uint, n int) {
	f.t.Helper()
	if err := f.db.Model(&models.SourceGroupMember{}).Where("id = ?", memberID).
		UpdateColumn("max_fail_count", n).Error; err != nil {
		f.t.Fatalf("set max_fail_count: %v", err)
	}
}

func (f *vf) snapshot(gid uint) *sourceref.GroupSnapshot {
	f.t.Helper()
	snap, ok := f.reg.Group(gid)
	if !ok {
		f.t.Fatalf("registry missing snapshot for group %d", gid)
	}
	return snap
}

func (f *vf) events() []ActiveChange {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]ActiveChange, len(f.evs))
	copy(out, f.evs)
	return out
}

func (f *vf) clearEvents() {
	f.mu.Lock()
	f.evs = nil
	f.mu.Unlock()
}

// waitEvents 轮询等待事件数达到 want（dispatcher 异步消费）；超时失败。
func (f *vf) waitEvents(want int) []ActiveChange {
	f.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if evs := f.events(); len(evs) >= want {
			return evs
		}
		time.Sleep(2 * time.Millisecond)
	}
	f.t.Fatalf("wait events: got %d, want >=%d", len(f.events()), want)
	return nil
}

// expectNoEvents 断言 settle 窗口内没有任何事件（负向断言，防"多"）。
func (f *vf) expectNoEvents() {
	f.t.Helper()
	deadline := time.Now().Add(150 * time.Millisecond)
	for time.Now().Before(deadline) {
		if evs := f.events(); len(evs) > 0 {
			f.t.Fatalf("expected no ActiveChange, got %+v", evs)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// expectNoMoreEvents 断言"除已观察到的 seen 条外不再追加"（防多发）。
func (f *vf) expectNoMoreEvents(seen int) {
	f.t.Helper()
	deadline := time.Now().Add(150 * time.Millisecond)
	for time.Now().Before(deadline) {
		if evs := f.events(); len(evs) > seen {
			f.t.Fatalf("expected at most %d ActiveChange, got %d: %+v", seen, len(evs), evs)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ── 1. F-1 反向断言：100 次 MarkSuccess 不得 bump ──────────────────────────

func TestVerifyRevision_100xMarkSuccessNoBump(t *testing.T) {
	f := newVF(t)
	a, b := f.addDevice("verify-a"), f.addDevice("verify-b")
	g := f.createPB(a, b)

	rev0 := f.revision(g.ID)
	snap0 := f.snapshot(g.ID)
	hash0 := snap0.MembersHash
	f.clearEvents()

	for i := 0; i < 100; i++ {
		f.advance(time.Second)
		f.gsvc.MarkSuccess(a.ID, []string{"illuminance"}, f.now)
	}
	if got := f.revision(g.ID); got != rev0 {
		t.Fatalf("F-1 违反：100 次 MarkSuccess 后 revision 变化 %d → %d", rev0, got)
	}
	snap := f.snapshot(g.ID)
	if snap.Revision != rev0 {
		t.Fatalf("快照 revision 必须等于 DB revision %d，got %d", rev0, snap.Revision)
	}
	if snap.MembersHash != hash0 {
		t.Fatalf("F-1 违反：MarkSuccess 改变了 MembersHash %q → %q", hash0, snap.MembersHash)
	}
	// 运行态确实写进去了（防止"什么都不写"的假绿）。
	m, ok := f.activeMember(g.ID)
	if !ok {
		t.Fatal("MarkSuccess 后应仍有 active 成员")
	}
	if m.FailCount != 0 {
		t.Fatalf("MarkSuccess 应清零 fail_count，got %d", m.FailCount)
	}
	if m.LastSuccess == nil || !m.LastSuccess.Equal(f.now) {
		t.Fatalf("MarkSuccess 应写 last_success=%v，got %v", f.now, m.LastSuccess)
	}
	f.expectNoEvents()
}

// ── 2. 运行态：失败计数/error↔standby 不得 bump ────────────────────────────

func TestVerifyRevision_RuntimeOnlyNoBump(t *testing.T) {
	f := newVF(t)
	a, b := f.addDevice("verify-a"), f.addDevice("verify-b")
	g := f.createPB(a, b)
	rev0 := f.revision(g.ID)
	f.clearEvents()

	ms := f.members(g.ID)
	var am, bm models.SourceGroupMember
	for _, m := range ms {
		switch m.EdgeDeviceID {
		case a.ID:
			am = m
		case b.ID:
			bm = m
		}
	}
	f.setMaxFail(am.ID, 5)
	f.setMaxFail(bm.ID, 1)

	// 未达阈值：仅计次，不 bump。
	for i := 0; i < 2; i++ {
		f.advance(time.Second)
		f.gsvc.MarkFailure(a.ID, TriggerDeviceOffline)
	}
	if got := f.revision(g.ID); got != rev0 {
		t.Fatalf("失败计数（未达阈值）不得 bump：%d → %d", rev0, got)
	}

	// standby 达到阈值 → error（无 active 迁移）⇒ 不 bump。
	f.gsvc.MarkFailure(b.ID, TriggerDeviceOffline)
	if m, ok := memberByEdge(f.members(g.ID), b.ID); !ok || m.Status != models.SourceMemberStatusError {
		t.Fatalf("standby 达阈值应转 error，got %+v ok=%v", m, ok)
	}
	if got := f.revision(g.ID); got != rev0 {
		t.Fatalf("standby→error 是运行态，不得 bump：%d → %d", rev0, got)
	}
	// error → standby（MarkSuccess）也不 bump。
	f.advance(time.Second)
	f.gsvc.MarkSuccess(b.ID, []string{"illuminance"}, f.now)
	if got := f.revision(g.ID); got != rev0 {
		t.Fatalf("error→standby 是运行态，不得 bump：%d → %d", rev0, got)
	}
	f.expectNoEvents()
}

// memberByEdge 找某 edge 的成员（返回快照副本）。
func memberByEdge(ms []models.SourceGroupMember, edge uint) (models.SourceGroupMember, bool) {
	for _, m := range ms {
		if m.EdgeDeviceID == edge {
			return m, true
		}
	}
	return models.SourceGroupMember{}, false
}

// ── 3. active 迁移必须 +1，且 MembersHash 不变（静态身份稳定）─────────────

func TestVerifyRevision_ActiveMigrationBumpsAndHashStable(t *testing.T) {
	f := newVF(t)
	a, b := f.addDevice("verify-a"), f.addDevice("verify-b")
	g := f.createPB(a, b)
	rev0 := f.revision(g.ID)
	hash0 := f.snapshot(g.ID).MembersHash
	f.clearEvents()

	// 让 active A 一次失败即熔断。
	ms := f.members(g.ID)
	am, ok := memberByEdge(ms, a.ID)
	if !ok {
		t.Fatal("active member A missing")
	}
	f.setMaxFail(am.ID, 1)
	f.advance(time.Second)
	f.gsvc.MarkFailure(a.ID, TriggerDeviceOffline)

	wantRev := rev0 + 1
	if got := f.revision(g.ID); got != wantRev {
		t.Fatalf("active 迁移必须 +1 revision：want %d got %d", wantRev, got)
	}
	if edge := f.activeEdge(g.ID); edge != b.ID {
		t.Fatalf("迁移后 active 应为 B(%d)，got %d", b.ID, edge)
	}
	snap := f.snapshot(g.ID)
	if snap.Revision != wantRev {
		t.Fatalf("快照 revision = %d want %d", snap.Revision, wantRev)
	}
	if snap.ActiveMemberID == 0 {
		t.Fatal("快照 ActiveMemberID 应为新 active")
	}
	// 关键：成员静态集合没变 ⇒ MembersHash 不变（active 迁移只 bump revision）。
	if snap.MembersHash != hash0 {
		t.Fatalf("active 迁移不得改变静态 MembersHash：%q → %q", hash0, snap.MembersHash)
	}
	if snap.MembersHash != sourceref.ComputeMembersHash(snap.Members) {
		t.Fatal("快照 MembersHash 与成员集合不一致")
	}
	evs := f.waitEvents(1)
	if len(evs) != 1 {
		t.Fatalf("active 迁移应恰 1 条事件，got %d: %+v", len(evs), evs)
	}
	ev := evs[0]
	if ev.PrevMemberID != am.ID || ev.NextMemberID == 0 || ev.Reason != ReasonAuto {
		t.Fatalf("R3 事件字段错误: %+v", ev)
	}
	if ev.Revision != wantRev {
		t.Fatalf("事件 revision = %d want %d", ev.Revision, wantRev)
	}

	// 新 active 上 100 次 MarkSuccess 仍不得 bump（迁移后再验证一次白名单）。
	for i := 0; i < 100; i++ {
		f.advance(time.Second)
		f.gsvc.MarkSuccess(b.ID, []string{"illuminance"}, f.now)
	}
	if got := f.revision(g.ID); got != wantRev {
		t.Fatalf("迁移后 MarkSuccess 不得 bump：%d → %d", wantRev, got)
	}
}

// ── 4. 拓扑变更必须 bump（正向断言，防"永不 bump"的另一端）────────────────

func TestVerifyRevision_TopologyChangeBumps(t *testing.T) {
	f := newVF(t)
	a, b, c := f.addDevice("verify-a"), f.addDevice("verify-b"), f.addDevice("verify-c")
	g := f.createPB(a, b)
	rev0 := f.revision(g.ID)
	hash0 := f.snapshot(g.ID).MembersHash

	// priority 变更：bump + hash 变化。
	newPrio := 200
	if _, err := f.gsvc.UpdateGroup(g.ID, UpdateGroupInput{Members: &[]GroupMemberInput{
		{EdgeDeviceID: a.ID, Priority: 100},
		{EdgeDeviceID: b.ID, Priority: newPrio},
	}}); err != nil {
		t.Fatalf("UpdateGroup priority: %v", err)
	}
	rev1 := f.revision(g.ID)
	if rev1 <= rev0 {
		t.Fatalf("priority 变更必须 bump revision：%d → %d", rev0, rev1)
	}
	if hash := f.snapshot(g.ID).MembersHash; hash == hash0 {
		t.Fatalf("priority 变更必须改变 MembersHash（静态身份）：%q", hash)
	}

	// 成员新增：bump。
	if _, err := f.gsvc.UpdateGroup(g.ID, UpdateGroupInput{Members: &[]GroupMemberInput{
		{EdgeDeviceID: a.ID, Priority: 100},
		{EdgeDeviceID: b.ID, Priority: newPrio},
		{EdgeDeviceID: c.ID, Priority: 10},
	}}); err != nil {
		t.Fatalf("UpdateGroup add member: %v", err)
	}
	rev2 := f.revision(g.ID)
	if rev2 <= rev1 {
		t.Fatalf("成员新增必须 bump revision：%d → %d", rev1, rev2)
	}

	// 组合配置变更（combine/on_degrade 对 primary 无意义；用 staleness_sec 验证）。
	staleness := 120
	if _, err := f.gsvc.UpdateGroup(g.ID, UpdateGroupInput{StalenessSec: &staleness}); err != nil {
		t.Fatalf("UpdateGroup staleness: %v", err)
	}
	rev3 := f.revision(g.ID)
	if rev3 <= rev2 {
		t.Fatalf("staleness 变更必须 bump revision：%d → %d", rev2, rev3)
	}
	_ = rev3
}

// ── 5. 快照不变量（revision/active/排序/hash 与 DB 一致）────────────────────

func TestVerifySnapshotInvariants(t *testing.T) {
	f := newVF(t)
	a, b := f.addDevice("verify-a"), f.addDevice("verify-b")
	g := f.createPB(a, b)

	check := func(step string) {
		t.Helper()
		row := f.group(g.ID)
		snap := f.snapshot(g.ID)
		if snap.Revision != row.Revision {
			t.Fatalf("%s: 快照 revision=%d DB=%d", step, snap.Revision, row.Revision)
		}
		active, ok := f.activeMember(g.ID)
		wantActive := uint(0)
		if ok {
			wantActive = active.ID
		}
		if snap.ActiveMemberID != wantActive {
			t.Fatalf("%s: 快照 ActiveMemberID=%d want %d", step, snap.ActiveMemberID, wantActive)
		}
		if snap.MembersHash != sourceref.ComputeMembersHash(snap.Members) {
			t.Fatalf("%s: MembersHash 与成员不一致", step)
		}
		// 排序：priority DESC, memberID ASC。
		for i := 1; i < len(snap.Members); i++ {
			prev, cur := snap.Members[i-1], snap.Members[i]
			if prev.Priority < cur.Priority ||
				(prev.Priority == cur.Priority && prev.MemberID > cur.MemberID) {
				t.Fatalf("%s: 成员排序错误 at %d: %+v/%+v", step, i, prev, cur)
			}
		}
	}

	check("create")
	f.advance(time.Second)
	f.gsvc.MarkSuccess(a.ID, []string{"illuminance"}, f.now)
	check("mark_success")

	ms := f.members(g.ID)
	am, _ := memberByEdge(ms, a.ID)
	f.setMaxFail(am.ID, 1)
	f.advance(time.Second)
	f.gsvc.MarkFailure(a.ID, TriggerDeviceOffline)
	check("failover")

	prio := 55
	if _, err := f.gsvc.UpdateGroup(g.ID, UpdateGroupInput{Members: &[]GroupMemberInput{
		{EdgeDeviceID: a.ID, Priority: prio},
		{EdgeDeviceID: b.ID, Priority: 90},
	}}); err != nil {
		t.Fatalf("UpdateGroup: %v", err)
	}
	check("update_priority")
}

// ── 6. F-4/F-5：非身份字段更新不得 bump ────────────────────────────────────
//
// F-1 bump 白名单只含身份/拓扑字段；name/description/enabled 是展示或运行开关，
// 成员 status=disabled（R8）是运行态（不进 MembersHash）—— 这些都不应重置
// 窗口 identity（否则改名/禁用一次成员会让 DurationSec 重新累计）。

func TestVerifyRevision_NonIdentityUpdateNoBump(t *testing.T) {
	f := newVF(t)
	a, b := f.addDevice("verify-a"), f.addDevice("verify-b")
	g := f.createPB(a, b)
	rev0 := f.revision(g.ID)
	hash0 := f.snapshot(g.ID).MembersHash
	f.clearEvents()

	// name / description 更新：不 bump、hash 不变、0 事件。
	name := "renamed-group"
	desc := "updated description"
	if _, err := f.gsvc.UpdateGroup(g.ID, UpdateGroupInput{Name: &name, Description: &desc}); err != nil {
		t.Fatalf("UpdateGroup name/description: %v", err)
	}
	if got := f.revision(g.ID); got != rev0 {
		t.Fatalf("F-4 违反：name/description 更新 bump 了 revision %d → %d", rev0, got)
	}
	if got := f.snapshot(g.ID).MembersHash; got != hash0 {
		t.Fatalf("F-4 违反：name/description 更新改变了 MembersHash")
	}
	row := f.group(g.ID)
	if row.Name != name || row.Description != desc {
		t.Fatalf("name/description 未写入: %+v", row)
	}
	f.expectNoEvents()

	// enabled 开关：不 bump（展示/开关字段，不影响窗口 identity）。
	disabled := false
	if _, err := f.gsvc.UpdateGroup(g.ID, UpdateGroupInput{Enabled: &disabled}); err != nil {
		t.Fatalf("UpdateGroup enabled: %v", err)
	}
	if got := f.revision(g.ID); got != rev0 {
		t.Fatalf("F-4 违反：enabled 开关 bump 了 revision %d → %d", rev0, got)
	}
	if f.snapshot(g.ID).Enabled {
		t.Fatal("快照 Enabled 未更新")
	}
	// 恢复启用。
	enabled := true
	if _, err := f.gsvc.UpdateGroup(g.ID, UpdateGroupInput{Enabled: &enabled}); err != nil {
		t.Fatalf("UpdateGroup re-enable: %v", err)
	}
	if got := f.revision(g.ID); got != rev0 {
		t.Fatalf("F-4 违反：重新启用 bump 了 revision %d → %d", rev0, got)
	}
	f.expectNoEvents()

	// R8（停用非 active 成员）：不 bump（status 属运行态，不在 MembersHash）。
	bm := f.memberOf(g.ID, b.ID)
	if _, err := f.gsvc.DeactivateGroupMember(g.ID, bm.ID); err != nil {
		t.Fatalf("R8 deactivate standby: %v", err)
	}
	if got := f.revision(g.ID); got != rev0 {
		t.Fatalf("F-5 违反：R8 停用非 active 成员 bump 了 revision %d → %d", rev0, got)
	}
	if cur := f.memberOf(g.ID, b.ID); cur.Status != models.SourceMemberStatusDisabled {
		t.Fatalf("R8 应 disabled，got %q", cur.Status)
	}
	f.expectNoEvents()

	// 反证：真正的身份字段变化必须 bump（防"永不 bump"的假绿）。
	prio := 42
	if _, err := f.gsvc.UpdateGroup(g.ID, UpdateGroupInput{Members: &[]GroupMemberInput{
		{EdgeDeviceID: a.ID, Priority: prio},
	}}); err != nil {
		t.Fatalf("UpdateGroup priority (positive control): %v", err)
	}
	if got := f.revision(g.ID); got <= rev0 {
		t.Fatalf("正对照失败：priority/成员变更必须 bump，rev0=%d got=%d", rev0, got)
	}
}
