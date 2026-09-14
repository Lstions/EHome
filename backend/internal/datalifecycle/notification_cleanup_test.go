package datalifecycle

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

// seedNotification 插入一条通知。created_at 显式指定 (GORM 的
// autoCreateTime 只在零值时接管)。
func seedNotification(t *testing.T, db *gorm.DB, title string, read bool, createdAt time.Time) *models.Notification {
	t.Helper()
	n := &models.Notification{
		Type:      "info",
		Title:     title,
		Message:   title,
		Source:    "test",
		SourceID:  title,
		Read:      read,
		CreatedAt: createdAt,
	}
	if err := db.Create(n).Error; err != nil {
		t.Fatalf("seed notification %q: %v", title, err)
	}
	return n
}

func countNotifications(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&models.Notification{}).Count(&n).Error; err != nil {
		t.Fatalf("count notifications: %v", err)
	}
	return n
}

// TestNotificationCleaner_DeletesOnlyExpiredRead — D-2 核心断言 (条数级):
// 已读 + 超期 → 删; 已读 + 未超期 → 留; 未读 + 超期 (未到未读窗口) → 留。
func TestNotificationCleaner_DeletesOnlyExpiredRead(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	seedNotification(t, db, "read-expired", true, now.AddDate(0, 0, -100))
	seedNotification(t, db, "read-fresh", true, now.AddDate(0, 0, -10))
	seedNotification(t, db, "unread-expired-for-read-window", false, now.AddDate(0, 0, -100))

	if got := countNotifications(t, db); got != 3 {
		t.Fatalf("seeded notifications = %d, want 3", got)
	}

	c := NewNotificationCleaner(db)
	c.SetRetention(90, 180) // 显式固定窗口, 不依赖全局快照
	c.SetBatchSleep(0)
	c.now = func() time.Time { return now }

	report, err := c.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("cleanup run: %v", err)
	}
	if report.ReadDeleted != 1 {
		t.Errorf("ReadDeleted = %d, want 1", report.ReadDeleted)
	}
	if report.UnreadDeleted != 0 {
		t.Errorf("UnreadDeleted = %d, want 0 (unread not yet past 180d)", report.UnreadDeleted)
	}

	// 具体条数断言 (不是"没报错")。
	if got := countNotifications(t, db); got != 2 {
		t.Fatalf("remaining notifications = %d, want 2 (only read+expired deleted)", got)
	}
	var titles []string
	db.Model(&models.Notification{}).Order("title").Pluck("title", &titles)
	want := []string{"read-fresh", "unread-expired-for-read-window"}
	if len(titles) != 2 || titles[0] != want[0] || titles[1] != want[1] {
		t.Errorf("remaining = %v, want %v", titles, want)
	}

	// 幂等: 再跑一轮不再删任何行。
	report, err = c.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if report.ReadDeleted != 0 || report.UnreadDeleted != 0 {
		t.Errorf("second run deleted %+v, want no-op (idempotent)", report)
	}
	if got := countNotifications(t, db); got != 2 {
		t.Errorf("after rerun notifications = %d, want 2", got)
	}
}

// TestNotificationCleaner_DeletesStaleUnread — 未读不做无界保留:
// 超过未读窗口 (已读窗口 × 2) 的未读通知同样删除, 否则
// "用户从不点开铃铛" 时表仍无限增长 (D-2 的原始症状)。
func TestNotificationCleaner_DeletesStaleUnread(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	seedNotification(t, db, "unread-ancient", false, now.AddDate(0, 0, -200))
	seedNotification(t, db, "unread-recent", false, now.AddDate(0, 0, -30))

	c := NewNotificationCleaner(db)
	c.SetRetention(90, 180)
	c.SetBatchSleep(0)
	c.now = func() time.Time { return now }

	report, err := c.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("cleanup run: %v", err)
	}
	if report.UnreadDeleted != 1 {
		t.Fatalf("UnreadDeleted = %d, want 1", report.UnreadDeleted)
	}
	if report.ReadDeleted != 0 {
		t.Errorf("ReadDeleted = %d, want 0", report.ReadDeleted)
	}
	if got := countNotifications(t, db); got != 1 {
		t.Fatalf("remaining = %d, want 1 (only stale unread deleted)", got)
	}
	var kept models.Notification
	db.First(&kept)
	if kept.Title != "unread-recent" {
		t.Errorf("kept = %q, want unread-recent", kept.Title)
	}
}

// TestNotificationCleaner_Batches — 分批删除 (与 purge 同范式):
// 每批独立事务, 多轮才能删尽; 断言总条数精确。
func TestNotificationCleaner_Batches(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	const expired = 5
	for i := 0; i < expired; i++ {
		seedNotification(t, db, "batch-"+string(rune('a'+i)), true, now.AddDate(0, 0, -100))
	}
	seedNotification(t, db, "batch-fresh", true, now.AddDate(0, 0, -1))

	c := NewNotificationCleaner(db)
	c.SetRetention(90, 180)
	c.SetBatchSize(1) // 强制每批 1 行
	c.SetBatchSleep(0)
	c.now = func() time.Time { return now }

	report, err := c.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("cleanup run: %v", err)
	}
	if report.ReadDeleted != expired {
		t.Errorf("ReadDeleted = %d, want %d", report.ReadDeleted, expired)
	}
	if got := countNotifications(t, db); got != 1 {
		t.Fatalf("remaining = %d, want 1 (fresh read row kept)", got)
	}
}

// TestNotificationCleaner_DefaultsToSystemRetention — 保留期沿用系统级
// 保留期快照 (retention.go), 与既有 retention 默认值同源; 未读 = 已读 × 2。
func TestNotificationCleaner_DefaultsToSystemRetention(t *testing.T) {
	db := testutil.OpenTestDB(t)

	c := NewNotificationCleaner(db)
	readDays, unreadDays := c.retentionWindows()
	if readDays != SystemRetentionDays() {
		t.Errorf("read retention = %d, want SystemRetentionDays() = %d", readDays, SystemRetentionDays())
	}
	if readDays != DefaultNotificationRetentionDays {
		t.Errorf("default read retention = %d, want %d", readDays, DefaultNotificationRetentionDays)
	}
	if unreadDays != readDays*notificationUnreadRetentionMultiplier {
		t.Errorf("unread retention = %d, want %d", unreadDays, readDays*notificationUnreadRetentionMultiplier)
	}

	// 系统级保留期变更后立即生效 (快照源语义)。
	SetSystemRetentionDays(30)
	defer SetSystemRetentionDays(90)
	if got, _ := c.retentionWindows(); got != 30 {
		t.Errorf("read retention after SetSystemRetentionDays(30) = %d, want 30", got)
	}
}

// TestRetentionTask_RunsNotificationCleanup — 接线自证: 清理挂在既有每日
// retention 任务上 (无新增 goroutine / 不动 main.go), 且不干扰逐设备保留期处理。
func TestRetentionTask_RunsNotificationCleanup(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	// 一条远早于未读窗口的未读通知 (与逻辑设备无关, 纯全局表)。
	seedNotification(t, db, "ancient-unread", false, now.AddDate(0, 0, -1000))
	// 一个到期的逻辑设备, 证明清理不会打断 retention 主流程。
	ld := seedRetentionDevice(t, db, "d2-expired", 365, now.AddDate(0, 0, -400))
	_ = ld

	r := NewRetentionTask(db)
	r.now = func() time.Time { return now }
	r.SetBatchSleep(0)
	r.notifier.SetRetention(90, 180)
	r.notifier.SetBatchSleep(0)
	r.notifier.now = func() time.Time { return now }

	results, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("retention run: %v", err)
	}
	var deleted int64
	for _, res := range results {
		deleted += res.RowsDeleted
	}
	if deleted != 1 {
		t.Errorf("retention RowsDeleted = %d, want 1 (per-device path unaffected)", deleted)
	}
	// 通知已被 retention 任务顺带清理: 只剩 retention 自己发的那条
	// (该设备到期且窗口已过, 不发临期通知) —— 故应为 0 条。
	if got := countNotifications(t, db); got != 0 {
		t.Errorf("notifications after retention run = %d, want 0", got)
	}
}
