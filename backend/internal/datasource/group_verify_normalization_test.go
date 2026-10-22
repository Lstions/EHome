package datasource

// 独立验证（impl-core）：2026-10-22 Lead 裁决的两块新增改动
//   ① StalenessSec=0 归一为推导值（0 = 全体成员恒 stale ⇒ 复合规则静默永不触发）；
//   ② RecordDegraded 求值期降级健康事件 + 1min 节流（防每帧刷健康表）。
//
// 这两项在组适配器独立验证（task 交付）之后落地，按 ds-analyst 提示补做独立回归。

import (
	"strings"
	"sync"
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/sourceref"
)

// TestVerifyStalenessNormalization 钉死归一规则与「DB 永不落 0」不变量。
func TestVerifyStalenessNormalization(t *testing.T) {
	// 纯函数规则（与 DeriveStaleness 同口径 + 无间隔兜底 60s）。
	cases := []struct {
		cfg       int
		intervals []int
		want      int
	}{
		{0, []int{5000}, 30},      // 2×5s=10s → 下限 30s
		{0, []int{60000}, 120},    // 2×60s
		{0, []int{0, 0}, 60},      // 全 0 ⇒ 60s 兜底（不是 0）
		{0, nil, 60},              // 无成员 ⇒ 60s
		{0, []int{1800000}, 3600}, // 2×1800s=3600s（上限边界）
		{0, []int{3600000}, 3600}, // 超上限 ⇒ 截到 3600s
		{120, []int{5000}, 120},   // 显式值原样保留
	}
	for _, tc := range cases {
		if got := normalizeStalenessSec(tc.cfg, tc.intervals); got != tc.want {
			t.Fatalf("normalizeStalenessSec(%d,%v)=%d want %d", tc.cfg, tc.intervals, got, tc.want)
		}
	}

	f := newVF(t)
	a := f.addDevice("verify-a")

	// 默认（设备 interval=5000ms）⇒ 30s 落库 + 快照 Staleness()>0。
	g := f.createPB(a)
	if row := f.group(g.ID); row.StalenessSec != 30 {
		t.Fatalf("默认组 StalenessSec=%d want 30（0 会让全体恒 stale）", row.StalenessSec)
	}
	if snap := f.snapshot(g.ID); snap.Staleness() != 30*time.Second {
		t.Fatalf("快照 Staleness()=%v want 30s", snap.Staleness())
	}

	// 大采集间隔 ⇒ 2×间隔（60s → 120s）。
	big := f.addDevice("verify-big")
	if err := f.db.Model(&models.EdgeDevice{}).Where("id = ?", big.ID).
		UpdateColumn("interval_ms", 60000).Error; err != nil {
		t.Fatalf("set interval: %v", err)
	}
	gb := f.createPB(big)
	if row := f.group(gb.ID); row.StalenessSec != 120 {
		t.Fatalf("interval=60000 推导 StalenessSec=%d want 120", row.StalenessSec)
	}

	// IntervalMs=0（未配置）⇒ 60s 兜底。
	zero := f.addDevice("verify-zero")
	if err := f.db.Model(&models.EdgeDevice{}).Where("id = ?", zero.ID).
		UpdateColumn("interval_ms", 0).Error; err != nil {
		t.Fatalf("set interval 0: %v", err)
	}
	gz := f.createPB(zero)
	if row := f.group(gz.ID); row.StalenessSec != 60 {
		t.Fatalf("IntervalMs=0 兜底 StalenessSec=%d want 60", row.StalenessSec)
	}

	// 历史脏值 0：下一次 UpdateGroup 必须回填（绝不把 0 留在 DB）。
	if err := f.db.Model(&models.SourceGroup{}).Where("id = ?", g.ID).
		UpdateColumn("staleness_sec", 0).Error; err != nil {
		t.Fatalf("dirty staleness: %v", err)
	}
	name := "backfilled"
	if _, err := f.gsvc.UpdateGroup(g.ID, UpdateGroupInput{Name: &name}); err != nil {
		t.Fatalf("UpdateGroup backfill: %v", err)
	}
	if row := f.group(g.ID); row.StalenessSec <= 0 {
		t.Fatalf("历史 0 未被回填：StalenessSec=%d", row.StalenessSec)
	}
	if snap := f.snapshot(g.ID); snap.Staleness() <= 0 {
		t.Fatalf("回填后快照 Staleness() 必须 >0，got %v", snap.Staleness())
	}
}

// degradedHealthRows 返回某组的 degraded 健康事件（按 id ASC）。
func (f *vf) degradedHealthRows(groupID uint) []models.SourceGroupHealth {
	f.t.Helper()
	var rows []models.SourceGroupHealth
	if err := f.db.Where("group_id = ? AND event = ?", groupID, "degraded").
		Order("id ASC").Find(&rows).Error; err != nil {
		f.t.Fatalf("query degraded health: %v", err)
	}
	return rows
}

// TestVerifyRecordDegradedThrottle 覆盖 1min 节流、不同 reason 独立、消息扩展与边界防御。
func TestVerifyRecordDegradedThrottle(t *testing.T) {
	f := newVF(t)
	a, b := f.addDevice("verify-a"), f.addDevice("verify-b")
	g := f.createPB(a)

	// 同 (group,reason) 同一分钟内多次调用 ⇒ 只落 1 条。
	f.gsvc.RecordDegraded(g.ID, "no_available_member")
	f.gsvc.RecordDegraded(g.ID, "no_available_member")
	if rows := f.degradedHealthRows(g.ID); len(rows) != 1 {
		t.Fatalf("节流失效：same reason 落 %d 条，want 1", len(rows))
	}

	// 不同 reason 不受同组节流影响（键含 reason）。
	f.gsvc.RecordDegraded(g.ID, "active_stale")
	if rows := f.degradedHealthRows(g.ID); len(rows) != 2 {
		t.Fatalf("不同 reason 应独立落行，got %d want 2", len(rows))
	}

	// 超过节流窗（61s）⇒ 同 reason 可再落一行。
	f.advance(61 * time.Second)
	f.gsvc.RecordDegraded(g.ID, "no_available_member")
	if rows := f.degradedHealthRows(g.ID); len(rows) != 3 {
		t.Fatalf("节流窗过后应再落一行，got %d want 3", len(rows))
	}

	// 带成员 id 的扩展：消息必须包含 members=...，便于运维定位。
	f.advance(61 * time.Second)
	f.gsvc.RecordDegradedWithMembers(g.ID, "below_min_members", []uint{7, 9})
	rows := f.degradedHealthRows(g.ID)
	if len(rows) != 4 {
		t.Fatalf("RecordDegradedWithMembers 应落行，got %d want 4", len(rows))
	}
	if !strings.Contains(rows[len(rows)-1].Message, "members=7,9") {
		t.Fatalf("消息未包含 members 扩展: %q", rows[len(rows)-1].Message)
	}

	// 边界：groupID=0 / 空 reason ⇒ 无操作不 panic。
	f.gsvc.RecordDegraded(0, "x")
	f.gsvc.RecordDegraded(g.ID, "   ")
	if rows := f.degradedHealthRows(g.ID); len(rows) != 4 {
		t.Fatalf("非法参数不应落行，got %d", len(rows))
	}

	// 并发节流：同一 (group,reason) 16 goroutine 并发 ⇒ 恰 1 条（-race 下同时验证无竞态）。
	concurrent := f.createPB(b)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.gsvc.RecordDegraded(concurrent.ID, "no_available_member")
		}()
	}
	wg.Wait()
	if rows := f.degradedHealthRows(concurrent.ID); len(rows) != 1 {
		t.Fatalf("并发节流失效：落 %d 条，want 1", len(rows))
	}

	// 组已删除 ⇒ 回调晚到不得写孤儿健康行。
	if err := f.gsvc.DeleteGroup(g.ID); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	before := len(f.degradedHealthRows(g.ID))
	f.advance(2 * time.Minute)
	f.gsvc.RecordDegraded(g.ID, "no_available_member")
	if after := len(f.degradedHealthRows(g.ID)); after != before {
		t.Fatalf("已删除组不应新增健康行：before=%d after=%d", before, after)
	}
}

// TestVerifyFastPath 验证 B-3 零组快速路径的语义边界（impl-core 观测点 O-1 修复锁）：
//
//   - registry == nil（装配缺失）⇒ **保守执行**：不能把"无法证明有组"当成"没有组"，
//     否则漏注入会静默丢失 R1/R2（离线检测不再触发组切换）；
//   - registry != nil 且 Len()==0（已发布为空）⇒ 零查询短路（热路径收益）。
func TestVerifyFastPath(t *testing.T) {
	newSvc := func(injectRegistry bool) *GroupService {
		db := openVerifyDB(t)
		svc := NewGroupService(db, Options{Now: func() time.Time { return testBase() }})
		if injectRegistry {
			svc.SetGroupRegistry(sourceref.NewRegistry())
		} else {
			svc.SetNotifier(func(models.Notification) {})
		}
		return svc
	}
	addDev := func(svc *GroupService, name string) *models.EdgeDevice {
		t.Helper()
		ed := models.EdgeDevice{Name: name, NodeID: "n", ChannelID: 1, IntervalMs: 5000, Enabled: true, Type: "prs3001"}
		if err := svc.db.Create(&ed).Error; err != nil {
			t.Fatalf("create device: %v", err)
		}
		if err := svc.db.Create(&models.SensorFieldObs{DeviceID: ed.ID, SensorName: "illuminance"}).Error; err != nil {
			t.Fatalf("create obs: %v", err)
		}
		return &ed
	}

	t.Run("nil_registry_runs_conservatively", func(t *testing.T) {
		svc := newSvc(false) // 不注入 registry
		ed := addDev(svc, "fp-a")
		g, err := svc.CreateGroup(CreateGroupInput{Name: "fp", Kind: models.SourceGroupKindPrimaryBackup, Category: "illuminance",
			Members: []GroupMemberInput{{EdgeDeviceID: ed.ID, Priority: 100}}})
		if err != nil {
			t.Fatalf("CreateGroup: %v", err)
		}
		if svc.groupsLive() != true {
			t.Fatal("registry=nil 时 groupsLive 必须保守返回 true（漏注入不得静默丢状态机）")
		}
		svc.MarkSuccess(ed.ID, []string{"illuminance"}, testBase())
		var m models.SourceGroupMember
		if err := svc.db.Where("group_id = ?", g.ID).First(&m).Error; err != nil {
			t.Fatalf("reload member: %v", err)
		}
		if m.LastSuccess == nil {
			t.Fatal("registry=nil 时 MarkSuccess 必须真执行（last_success 应写入），不得静默 no-op")
		}
	})

	t.Run("injected_empty_registry_short_circuits", func(t *testing.T) {
		svc := newSvc(true) // 注入 registry，但未 PublishAll（Len=0）
		ed := addDev(svc, "fp-b")
		// 直写 DB 造组（不走 CreateGroup，避免 publish 后 Len>0）。
		g := models.SourceGroup{Name: "fp-db", Kind: models.SourceGroupKindPrimaryBackup, Category: "illuminance", Enabled: true, StalenessSec: 30}
		if err := svc.db.Create(&g).Error; err != nil {
			t.Fatalf("create group row: %v", err)
		}
		m := models.SourceGroupMember{GroupID: g.ID, EdgeDeviceID: ed.ID, Priority: 100, Status: models.SourceMemberStatusActive, MaxFailCount: 3}
		if err := svc.db.Create(&m).Error; err != nil {
			t.Fatalf("create member row: %v", err)
		}
		if svc.groupsLive() {
			t.Fatal("已注入但 Len=0 时 groupsLive 必须 false（零查询短路）")
		}
		svc.MarkSuccess(ed.ID, []string{"illuminance"}, testBase())
		var after models.SourceGroupMember
		if err := svc.db.First(&after, m.ID).Error; err != nil {
			t.Fatalf("reload member: %v", err)
		}
		if after.LastSuccess != nil {
			t.Fatal("已发布 0 组时 MarkSuccess 应短路（不写 DB）")
		}
		// PublishAll 后 Len>0 ⇒ 恢复正常执行。
		if err := svc.PublishAll(); err != nil {
			t.Fatalf("PublishAll: %v", err)
		}
		svc.MarkSuccess(ed.ID, []string{"illuminance"}, testBase())
		if err := svc.db.First(&after, m.ID).Error; err != nil {
			t.Fatalf("reload member: %v", err)
		}
		if after.LastSuccess == nil {
			t.Fatal("PublishAll 后 MarkSuccess 必须真执行")
		}
	})
}
