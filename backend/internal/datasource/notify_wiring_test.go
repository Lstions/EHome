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
// D-1 步骤 3: datasource 通知写入点经 Notifier 落库的接线测试
// ============================================================================
//
// 本包的通知写入点是 Service.emit -> 7 个 emitAll 调用点 (全部在业务事务提交后)。
// 改造前它已有一个 func(models.Notification) 回调注入点; D-1 新增 SetDispatchNotifier
// 让 Dispatcher 成为**优先**入口 (Dispatcher 落库+外发), 既有回调保留为回落。
//
// 这三个注入点的优先级 (notifySink > notify > 直接落库) 是本文件的断言对象。

// dsFakeNotifier 记录经它写入的通知并真落库 (保持自增 ID 语义)。
type dsFakeNotifier struct {
	mu  sync.Mutex
	got []models.Notification
	db  *gorm.DB
}

func (f *dsFakeNotifier) Create(ctx context.Context, n *models.Notification) {
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

func (f *dsFakeNotifier) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.got)
}

// newDispatchTestService 构造**不注入既有 notify 回调**的 Service, 以便干净地
// 断言 D-1 的 Dispatcher 入口 (既有 newTestService 会先注入回调, 那会与本次断言
// 的优先级判定混淆)。
func newDispatchTestService(t *testing.T, now time.Time) (*Service, *gorm.DB) {
	t.Helper()
	db := testutil.OpenTestDB(t)
	svc := New(db, Options{
		Cooldown:     5 * time.Minute,
		MinResidency: 2 * time.Minute,
		Staleness:    5 * time.Minute,
		ScanInterval: time.Hour,
		Now:          func() time.Time { return now },
	})
	return svc, db
}

// TestDatasourceEmitGoesThroughDispatcherNotifier 断言: 注入 Dispatcher 后,
// "组内失去来源"的通知经它落库 (Delete 路径是 7 个 emitAll 点之一)。
func TestDatasourceEmitGoesThroughDispatcherNotifier(t *testing.T) {
	now := testBase()
	svc, db := newDispatchTestService(t, now)
	fake := &dsFakeNotifier{db: db}
	svc.SetDispatchNotifier(fake)

	ds := mustCreate(t, svc, CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 10})
	if err := svc.Delete(ds.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if fake.count() == 0 {
		t.Fatal("Dispatcher 入口未被调用: 通知没有经 SetDispatchNotifier 注入的 Notifier 落库")
	}
	for _, n := range fake.got {
		if n.Source != notifySource {
			t.Fatalf("Source = %q, 期望 %q", n.Source, notifySource)
		}
	}
}

// TestDatasourceDispatcherTakesPrecedenceOverLegacyCallback 断言优先级:
// 两个入口都注入时, **Dispatcher 赢** (它是落库+外发的唯一入口; 若旧回调抢先,
// 通知会落库但永不外发 —— 正是本步骤要消灭的孤儿状态)。
func TestDatasourceDispatcherTakesPrecedenceOverLegacyCallback(t *testing.T) {
	now := testBase()
	svc, db := newDispatchTestService(t, now)

	var legacyCalls int
	svc.SetNotifier(func(n models.Notification) { legacyCalls++ })
	fake := &dsFakeNotifier{db: db}
	svc.SetDispatchNotifier(fake)

	ds := mustCreate(t, svc, CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 10})
	if err := svc.Delete(ds.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if fake.count() == 0 {
		t.Fatal("Dispatcher 未接管: 通知走了旧回调, 将永不外发")
	}
	if legacyCalls != 0 {
		t.Fatalf("旧回调被调用 %d 次, 期望 0 (Dispatcher 优先级更高)", legacyCalls)
	}
}

// TestDatasourceNilDispatcherFallsBackToLegacyCallback 断言回落链第一级:
// 只注入旧回调 (既有测试正是这样) 时行为与改造前一致。
func TestDatasourceNilDispatcherFallsBackToLegacyCallback(t *testing.T) {
	now := testBase()
	svc, _ := newDispatchTestService(t, now)

	var legacyNotes []models.Notification
	svc.SetNotifier(func(n models.Notification) { legacyNotes = append(legacyNotes, n) })

	ds := mustCreate(t, svc, CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 10})
	if err := svc.Delete(ds.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if len(legacyNotes) == 0 {
		t.Fatal("未注入 Dispatcher 时旧回调未被调用 (既有行为被破坏)")
	}
}

// TestDatasourceNoNotifierFallsBackToDB 断言回落链末端: 什么都不注入时直接落库,
// 不 panic (既有行为不变)。
func TestDatasourceNoNotifierFallsBackToDB(t *testing.T) {
	now := testBase()
	svc, db := newDispatchTestService(t, now)

	ds := mustCreate(t, svc, CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 10})
	if err := svc.Delete(ds.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	var rows []models.Notification
	if err := db.Where("source = ?", notifySource).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("无任何 Notifier 时通知未落库 (直接落库回落被破坏)")
	}
}
