package datasource

import (
	"context"
	"sync"
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"

	"gorm.io/gorm"
)

// ============================================================================
// 通知写入点经 Notifier 落库的接线测试（D-1 步骤 3）
// ============================================================================
//
// 2026-10-24（遗留处置方案 A）本文件的**对象已迁移**：
// 原文件测的是 datasource.Service.emit 的 7 个 emitAll 调用点，但那些调用点全部
// 挂在 Service 的 Delete/Activate/Deactivate/Reset 上 —— 那四个方法已删除
// （组键二元化后它们在生产上恒失败或无意义，见 handler_data_source.go 注释）。
//
// 本包现在**唯一**的通知生产者是 GroupService（source_groups 域）。
// 因此下面三条断言原样迁到 GroupService，锁住同样的不变量：
//   优先级 notifySink(Dispatcher) > notify(旧回调) > 直接落库；
//   且 Dispatcher 一旦注入，旧回调**必须**不被调用（否则通知会落库但永不外发）。

// gsFakeNotifier 记录经它写入的通知并真落库（保持自增 ID 语义）。
type gsFakeNotifier struct {
	mu  sync.Mutex
	got []models.Notification
	db  *gorm.DB
}

func (f *gsFakeNotifier) Create(ctx context.Context, n *models.Notification) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n == nil {
		return
	}
	if err := f.db.WithContext(ctx).Create(n).Error; err != nil {
		return
	}
	f.got = append(f.got, *n)
}

func (f *gsFakeNotifier) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.got)
}

func newGroupNotifyTestService(t *testing.T, now time.Time) (*GroupService, *gorm.DB) {
	t.Helper()
	db := testutil.OpenTestDB(t)
	gsvc := NewGroupService(db, Options{
		Cooldown:     5 * time.Minute,
		MinResidency: 2 * time.Minute,
		Staleness:    5 * time.Minute,
		ScanInterval: time.Hour,
		Now:          func() time.Time { return now },
	})
	return gsvc, db
}

// seedGroupForNotify 造一个主备两成员组，并返回 (groupId, 备用成员 id)。
//
// 为什么两个成员：组域通知只在**状态迁移**时产生（切换/降级/恢复），
// 单成员组没有可迁移的目标。成员必须先在 edge_devices 里存在（外键 + 观测口径）。
func seedGroupForNotify(t *testing.T, gsvc *GroupService, db *gorm.DB) (uint, uint) {
	t.Helper()
	mk := func(name string) *models.EdgeDevice {
		ed := models.EdgeDevice{Name: name, NodeID: "node-1", ChannelID: 1,
			IntervalMs: 5000, Enabled: true, Type: "prs3001"}
		if err := db.Create(&ed).Error; err != nil {
			t.Fatalf("create edge device: %v", err)
		}
		if err := db.Create(&models.SensorFieldObs{DeviceID: ed.ID, SensorName: "illuminance"}).Error; err != nil {
			t.Fatalf("create sensor obs: %v", err)
		}
		return &ed
	}
	d1, d2 := mk("notify-a"), mk("notify-b")

	group, err := gsvc.CreateGroup(CreateGroupInput{
		Name:     "notify-group",
		Kind:     models.SourceGroupKindPrimaryBackup,
		Category: "illuminance",
		Members: []GroupMemberInput{
			{EdgeDeviceID: d1.ID, Priority: 100},
			{EdgeDeviceID: d2.ID, Priority: 90},
		},
	})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	// 切换目标用**成员 id**（不是设备 id）—— ActivateGroupMember 的第二个参数是 memberID。
	var backup models.SourceGroupMember
	if err := db.Where("group_id = ? AND edge_device_id = ?", group.ID, d2.ID).
		First(&backup).Error; err != nil {
		t.Fatalf("load backup member: %v", err)
	}
	return group.ID, backup.ID
}

// TestGroupEmitGoesThroughDispatcherNotifier 断言：注入 Dispatcher 后，
// 组内切换通知经它落库。
func TestGroupEmitGoesThroughDispatcherNotifier(t *testing.T) {
	now := testBase()
	gsvc, db := newGroupNotifyTestService(t, now)
	fake := &gsFakeNotifier{db: db}
	gsvc.SetDispatchNotifier(fake)

	groupID, backupID := seedGroupForNotify(t, gsvc, db)
	if _, err := gsvc.ActivateGroupMember(groupID, backupID); err != nil {
		t.Fatalf("ActivateGroupMember: %v", err)
	}

	if fake.count() == 0 {
		t.Fatal("Dispatcher 入口未被调用：通知没有经 SetDispatchNotifier 注入的 Notifier 落库")
	}
	for _, n := range fake.got {
		if n.Source != notifySource {
			t.Fatalf("Source = %q，期望 %q", n.Source, notifySource)
		}
	}
}

// TestGroupDispatcherTakesPrecedenceOverLegacyCallback 断言优先级：
// 两个入口都注入时 **Dispatcher 赢**（它是落库+外发的唯一入口；若旧回调抢先，
// 通知会落库但永不外发 —— 正是 D-1 要消灭的孤儿状态）。
func TestGroupDispatcherTakesPrecedenceOverLegacyCallback(t *testing.T) {
	now := testBase()
	gsvc, db := newGroupNotifyTestService(t, now)

	var legacyCalls int
	gsvc.SetNotifier(func(n models.Notification) { legacyCalls++ })
	fake := &gsFakeNotifier{db: db}
	gsvc.SetDispatchNotifier(fake)

	groupID, backupID := seedGroupForNotify(t, gsvc, db)
	if _, err := gsvc.ActivateGroupMember(groupID, backupID); err != nil {
		t.Fatalf("ActivateGroupMember: %v", err)
	}

	if fake.count() == 0 {
		t.Fatal("Dispatcher 未接管：通知走了旧回调，将永不外发")
	}
	if legacyCalls != 0 {
		t.Fatalf("旧回调被调用 %d 次，期望 0（Dispatcher 优先级更高）", legacyCalls)
	}
}

// TestGroupNoInjectionFallsBackToDatabase 断言未接线时**仍落库**（fail-open）：
// 漏接线不能让通知凭空消失，测试/早期部署只靠 DB 也必须可用。
func TestGroupNoInjectionFallsBackToDatabase(t *testing.T) {
	now := testBase()
	gsvc, db := newGroupNotifyTestService(t, now)

	groupID, backupID := seedGroupForNotify(t, gsvc, db)
	if _, err := gsvc.ActivateGroupMember(groupID, backupID); err != nil {
		t.Fatalf("ActivateGroupMember: %v", err)
	}

	var n int64
	if err := db.Model(&models.Notification{}).Where("source = ?", notifySource).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("未注入任何 Notifier 时通知必须直接落库（fail-open），实际 0 条")
	}
}
