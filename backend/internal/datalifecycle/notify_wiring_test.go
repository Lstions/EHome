package datalifecycle

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"

	"gorm.io/gorm"
)

// ============================================================================
// D-1 步骤 3: datalifecycle 两个通知写入点经 Notifier 落库的接线测试
// ============================================================================
//
// 本包有两个独立的通知写入点, 分属两个 worker:
//   - Migrator.notify      (merge 失败/受阻, migrate.go)
//   - RetentionTask.createNotification (保留期临期, retention_task.go)
// 两者各有一个 SetNotifier, 必须分别接线 —— 只接一个会有半个包仍然"永不外发"。

// dlFakeNotifier 记录经它写入的通知并真落库 (保持自增 ID 语义)。
type dlFakeNotifier struct {
	mu  sync.Mutex
	got []models.Notification
	db  *gorm.DB
}

func (f *dlFakeNotifier) Create(ctx context.Context, n *models.Notification) {
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

func (f *dlFakeNotifier) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.got)
}

func (f *dlFakeNotifier) first() models.Notification {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.got) == 0 {
		return models.Notification{}
	}
	return f.got[0]
}

// TestRetentionNotifyGoesThroughNotifier 断言保留期临期通知**经注入的 Notifier 落库**。
//
// 走真实任务路径: RetentionTask.RunOnce (每日任务), 不是直接调内部函数 ——
// 否则"SetNotifier 接线"本身不在被测范围内。
func TestRetentionNotifyGoesThroughNotifier(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	seedRetentionDevice(t, db, "wired", 365, now.AddDate(0, 0, -340))

	fake := &dlFakeNotifier{db: db}
	r := NewRetentionTask(db)
	r.SetNotifier(fake)
	r.now = func() time.Time { return now }
	r.SetBatchSleep(0)

	results, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(results) != 1 || results[0].NotifiedTier != 30 {
		t.Fatalf("results = %+v, want NotifiedTier=30", results)
	}
	if fake.count() != 1 {
		t.Fatalf("Notifier 落库 %d 条, 期望 1 条 (临期通知必须经它落库)", fake.count())
	}
	if got := fake.first(); got.Source != NotificationSourceRetentionExpiring {
		t.Fatalf("Source = %q, 期望 %q", got.Source, NotificationSourceRetentionExpiring)
	}
	// 通知在 notifications 表里真实可见。
	var n models.Notification
	if err := db.Where("source = ?", NotificationSourceRetentionExpiring).First(&n).Error; err != nil {
		t.Fatalf("notification not created: %v", err)
	}
	if n.Title != retentionNoticeTitle30 {
		t.Fatalf("Title = %q, 期望 %q", n.Title, retentionNoticeTitle30)
	}
}

// TestRetentionNotifyNilNotifierFallsBackToDB 断言 nil 回落是显式设计:
// 既有测试 NewRetentionTask(db) 不注入时, 通知仍落库且去重逻辑不变。
func TestRetentionNotifyNilNotifierFallsBackToDB(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	seedRetentionDevice(t, db, "fallback", 365, now.AddDate(0, 0, -340))

	r := NewRetentionTask(db) // 不注入 Notifier
	r.now = func() time.Time { return now }
	r.SetBatchSleep(0)

	results, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(results) != 1 || results[0].NotifiedTier != 30 {
		t.Fatalf("results = %+v, want NotifiedTier=30 (nil 回落行为必须不变)", results)
	}
	var n models.Notification
	if err := db.Where("source = ?", NotificationSourceRetentionExpiring).First(&n).Error; err != nil {
		t.Fatalf("nil Notifier 回落未落库: %v", err)
	}
}

// TestMigratorNotifyGoesThroughNotifier 断言 merge 受阻通知经注入的 Notifier 落库。
//
// 场景: 合并搬迁失败但未超重试上限 → 首次失败发一条 warning (migrate.go 受阻分支)。
// 该分支在独立的 Update 之后调用 notify, **不在事务内** —— 这是"同步投递安全"
// 这一裁决的实测依据之一。
func TestMigratorNotifyGoesThroughNotifier(t *testing.T) {
	db := testutil.OpenTestDB(t)
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	src := seedMergeSource(t, db, "notify-src", 2, base)
	target := seedMergeSource(t, db, "notify-dst", 0, base)
	startMerge(t, db, src, target)

	fake := &dlFakeNotifier{db: db}
	m := NewMigrator(db)
	m.SetNotifier(fake)
	m.SetBatchSleep(0)
	// 注入批次失败: 使搬迁失败但未超重试上限 → retries=1 走"首次受阻"分支
	// (未超限: 保留水位, 下次续跑; 首次失败发一条 warning)。
	m.SetBatchHook(func(string, int64, int64) error {
		return errors.New("injected batch failure")
	})

	if _, err := m.RunOnce(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	// 夹具是 2 个源 → 2 个 merge job, 每个 job 首次受阻各发一条 warning
	// (与 migrate_test.go 的 "warning notifications = 2 (one per source job)" 同口径)。
	// 断言"每条都经 Notifier"而不是"总共 1 条"。
	if fake.count() != 2 {
		t.Fatalf("Notifier 落库 %d 条, 期望 2 条 (每源 job 一条受阻通知, 全部必须经它落库)", fake.count())
	}
	for _, got := range fake.got {
		if got.Source != NotificationSourceMergeFailed {
			t.Fatalf("Source = %q, 期望 %q", got.Source, NotificationSourceMergeFailed)
		}
		if got.Type != "warning" {
			t.Fatalf("Type = %q, 期望 warning (首次受阻分支)", got.Type)
		}
	}
	// 通知在 notifications 表里真实可见。
	var rows []models.Notification
	db.Where("source = ?", NotificationSourceMergeFailed).Find(&rows)
	if len(rows) != 2 {
		t.Fatalf("notifications 行数 = %d, 期望 2", len(rows))
	}
}

// TestMigratorNotifyNilNotifierFallsBackToDB 断言 nil 回落: 不注入时仍落库。
func TestMigratorNotifyNilNotifierFallsBackToDB(t *testing.T) {
	db := testutil.OpenTestDB(t)
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	src := seedMergeSource(t, db, "fb-src", 2, base)
	target := seedMergeSource(t, db, "fb-dst", 0, base)
	startMerge(t, db, src, target)

	m := NewMigrator(db) // 不注入 Notifier
	m.SetBatchSleep(0)
	m.SetBatchHook(func(string, int64, int64) error {
		return errors.New("injected batch failure")
	})

	if _, err := m.RunOnce(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	var got []models.Notification
	if err := db.Where("source = ?", NotificationSourceMergeFailed).Find(&got).Error; err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("nil Notifier 回落落库 %d 行, 期望 2 行 (每源 job 一条, 行为必须与改造前一致)", len(got))
	}
}
