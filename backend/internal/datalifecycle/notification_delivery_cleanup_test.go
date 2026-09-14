package datalifecycle

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

// seedDelivery 插入一条投递审计行。created_at 显式指定 (GORM 的
// autoCreateTime 只在零值时接管)。
func seedDelivery(t *testing.T, db *gorm.DB, state string, createdAt time.Time) *models.NotificationDelivery {
	t.Helper()
	row := &models.NotificationDelivery{
		NotificationID: 1,
		ChannelID:      1,
		State:          state,
		AttemptNo:      1,
		CreatedAt:      createdAt,
	}
	if err := db.Create(row).Error; err != nil {
		t.Fatalf("seed delivery state=%s: %v", state, err)
	}
	return row
}

// seedDeliveryFor 插入一条绑定到指定通知的投递审计行。
func seedDeliveryFor(t *testing.T, db *gorm.DB, notificationID uint, state string, createdAt time.Time) *models.NotificationDelivery {
	t.Helper()
	row := &models.NotificationDelivery{
		NotificationID: notificationID,
		ChannelID:      1,
		State:          state,
		AttemptNo:      1,
		CreatedAt:      createdAt,
	}
	if err := db.Create(row).Error; err != nil {
		t.Fatalf("seed delivery for notification %d: %v", notificationID, err)
	}
	return row
}

func countDeliveries(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&models.NotificationDelivery{}).Count(&n).Error; err != nil {
		t.Fatalf("count deliveries: %v", err)
	}
	return n
}

func statesOfDeliveries(t *testing.T, db *gorm.DB) []string {
	t.Helper()
	var states []string
	if err := db.Model(&models.NotificationDelivery{}).Order("id").Pluck("state", &states).Error; err != nil {
		t.Fatalf("pluck states: %v", err)
	}
	return states
}

// ─────────────────────────────────────────────────────────────────────
// 不变式测试 (本轮领队复核后新增的核心约束, 不是"记录当前取值")
// ────────────────────────────────────────────────────────────────────

// TestDeliveryCleanup_EvidenceOutlivesNotificationBody —
// **不变式**: 只要通知本体还在 (用户仍能在通知中心看到、仍能追问"这条当时
// 送达没有"), 它的投递证据就必须还在。
//
// 这条测试**吸收**了领队独立复核 (原 leadproof/proof_test.go) 的证伪结论:
//
//	曾以 "delivered 行是 notifications 行的冗余第二份拷贝, 所以可以只留
//	基准/2 (45 天)" 为由设短窗 —— 该论据被反证推翻: models.Notification
//	压根没有投递状态列, "当时送达没有"只存在于 notification_deliveries。
//	60 天前的未读通知本体还在 (180 天窗内), 而 45 天窗把它的 delivered 证据
//	删了 ⇒ 追问对象在、证据没了。
//
// 本测试因此断言两件事, 且**能因"窗口被调回 45 天"而变红** (约束不变式):
//  1. 60 天前未读通知的 delivered 证据在默认清理后必须存活;
//  2. notifications 表不得存在任何投递状态列 —— 若将来有人给它加了这样一列
//     (冗余论据重新成立), 本测试也变红, 迫使重新裁决而不是默默沿用短窗。
func TestDeliveryCleanup_EvidenceOutlivesNotificationBody(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	// 60 天前的**未读**通知: 通知本体未读窗 = 180 天 ⇒ 本体必然存活,
	// 即"追问对象"仍在。它的投递证据与本体同龄。
	old := now.AddDate(0, 0, -60)
	n := seedNotification(t, db, "invariant-body-alive", false, old)
	d := seedDeliveryFor(t, db, n.ID, models.DeliveryStateDelivered, old)
	// 对照组: 同一条通知的失败尝试证据 (同样是唯一证据)。
	dFail := seedDeliveryFor(t, db, n.ID, models.DeliveryStateFailed, old)

	// 两侧都用**生产默认**窗口 (不调用 SetRetention): 这才是产品实际跑的路径。
	nc := NewNotificationCleaner(db)
	nc.SetBatchSleep(0)
	nc.now = func() time.Time { return now }
	if _, err := nc.RunOnce(context.Background()); err != nil {
		t.Fatalf("notification cleanup: %v", err)
	}

	dc := NewNotificationDeliveryCleaner(db)
	dc.SetBatchSleep(0)
	dc.now = func() time.Time { return now }
	dr, err := dc.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("delivery cleanup: %v", err)
	}

	var nLeft, dLeft, dFailLeft int64
	db.Model(&models.Notification{}).Where("id = ?", n.ID).Count(&nLeft)
	db.Model(&models.NotificationDelivery{}).Where("id = ?", d.ID).Count(&dLeft)
	db.Model(&models.NotificationDelivery{}).Where("id = ?", dFail.ID).Count(&dFailLeft)

	if nLeft != 1 {
		t.Fatalf("前提不成立: 60 天前的未读通知本体被删除, 本不变式测试无意义")
	}
	if dLeft != 1 {
		t.Errorf("不变式被违反: 通知本体存活(可追问), 但其 delivered 证据被删 "+
			"(deleted=%+v) —— 投递证据保留期不得短于通知本体的最长保留期", dr)
	}
	if dFailLeft != 1 {
		t.Errorf("不变式被违反: 通知本体存活, 但其 failed 证据被删 (deleted=%+v)", dr)
	}

	// 冗余论据的**结构性反证**: notifications 若真有投递状态列, "证据冗余、
	// 可以短留"才可能成立。列不存在 ⇒ delivered/failed 都是唯一证据。
	rows, err := db.Raw("SELECT * FROM notifications LIMIT 1").Rows()
	if err != nil {
		t.Fatalf("raw notifications: %v", err)
	}
	cols, err := rows.Columns()
	rows.Close()
	if err != nil {
		t.Fatalf("columns: %v", err)
	}
	for _, c := range cols {
		switch c {
		case "delivered", "state", "delivery_state", "delivery_status", "last_delivery_state":
			t.Fatalf("notifications 竟然有投递状态列 %q —— "+
				"冗余论据可能重新成立, 短窗策略需重新裁决 (不要默默沿用)", c)
		}
	}
}

// TestDeliveryCleanup_WindowNeverShorterThanNotificationBody — 不变式的
// **参数化**约束: 无论通知侧窗口怎么配, 投递审计窗口都不得短于
// max(已读窗, 未读窗)。这条不依赖任何具体数字, 因此"有人把某处常量改成
// 45 天"会让它红。
func TestDeliveryCleanup_WindowNeverShorterThanNotificationBody(t *testing.T) {
	db := testutil.OpenTestDB(t)

	cases := []struct{ base, readDays, unreadDays int }{
		{90, 0, 0},    // 生产默认 (系统快照 90) → 通知最长 180
		{30, 0, 0},    // 快照 30 → 通知最长 60
		{365, 0, 0},   // 快照 365 → 通知最长 730
		{90, 90, 180}, // 显式覆盖 (与生产默认一致)
		{90, 7, 30},   // 显式覆盖一个更短的通知窗 → 投递窗必须跟到 30
	}
	for _, tc := range cases {
		prev := SystemRetentionDays()
		SetSystemRetentionDays(tc.base)

		_, unread := notificationRetentionWindows(tc.readDays, tc.unreadDays)
		bodyMax := unread
		if tc.readDays > bodyMax {
			bodyMax = tc.readDays
		}

		// 生产默认路径 (不 SetRetention): 投递窗口必须 == 通知本体最长窗口。
		got := NewNotificationDeliveryCleaner(db).retentionWindowDays()
		if got < bodyMax {
			t.Errorf("base=%d read=%d unread=%d: 投递审计窗口 %d < 通知本体最长保留期 %d "+
				"—— 违反不变式 (证据不得比被审计对象先死)", tc.base, tc.readDays, tc.unreadDays, got, bodyMax)
		}

		// 通知侧窗口一旦变长, 投递侧必须自动跟随 (同一解析函数的直接后果)。
		SetSystemRetentionDays(tc.base)
		_ = prev
	}
	SetSystemRetentionDays(90)
}

// ─────────────────────────────────────────────────────────────────────
// 条数级行为测试
// ─────────────────────────────────────────────────────────────────────

// TestNotificationDeliveryCleaner_DeletesOnlyPastAuditWindow — 核心条数断言:
// 三档 state 共用审计窗口; 窗内一律保留, 窗外一律删除; 白名单外永不删。
func TestNotificationDeliveryCleaner_DeletesOnlyPastAuditWindow(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	const window = 180
	seedDelivery(t, db, models.DeliveryStateDelivered, now.AddDate(0, 0, -181)) // 窗外 → 删
	seedDelivery(t, db, models.DeliveryStateFailed, now.AddDate(0, 0, -181))    // 窗外 → 删
	seedDelivery(t, db, models.DeliveryStatePending, now.AddDate(0, 0, -181))   // 窗外 → 删
	seedDelivery(t, db, models.DeliveryStateDelivered, now.AddDate(0, 0, -179)) // 窗内 → 留
	seedDelivery(t, db, models.DeliveryStateFailed, now.AddDate(0, 0, -179))    // 窗内 → 留
	seedDelivery(t, db, models.DeliveryStatePending, now.AddDate(0, 0, -179))   // 窗内 → 留
	seedDelivery(t, db, "suppressed", now.AddDate(0, 0, -9999))                 // 白名单外 → 永不删

	if got := countDeliveries(t, db); got != 7 {
		t.Fatalf("seeded deliveries = %d, want 7", got)
	}

	c := NewNotificationDeliveryCleaner(db)
	c.SetRetention(window)
	c.SetBatchSleep(0)
	c.now = func() time.Time { return now }

	report, err := c.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("delivery cleanup run: %v", err)
	}
	if report.DeliveredDeleted != 1 || report.FailedDeleted != 1 || report.PendingDeleted != 1 {
		t.Errorf("deleted = %+v, want 1/1/1", report)
	}
	if report.Total() != 3 {
		t.Errorf("total deleted = %d, want 3", report.Total())
	}
	// 具体条数断言 (不是"没报错")。
	if got := countDeliveries(t, db); got != 4 {
		t.Fatalf("remaining deliveries = %d, want 4", got)
	}
	states := statesOfDeliveries(t, db)
	want := []string{"delivered", "failed", "pending", "suppressed"}
	if len(states) != len(want) {
		t.Fatalf("remaining states = %v, want %v", states, want)
	}
	for i := range want {
		if states[i] != want[i] {
			t.Fatalf("remaining states = %v, want %v", states, want)
		}
	}

	// 幂等: 再跑一轮不再删任何行。
	report, err = c.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if report.Total() != 0 {
		t.Errorf("second run deleted %+v, want no-op (idempotent)", report)
	}
	if got := countDeliveries(t, db); got != 4 {
		t.Errorf("after rerun deliveries = %d, want 4", got)
	}
}

// TestNotificationDeliveryCleaner_DefaultWindowFollowsSystemRetention —
// 保留期沿用系统级快照 (retention.go) 与通知本体的最长窗口, **没有新增配置项**。
func TestNotificationDeliveryCleaner_DefaultWindowFollowsSystemRetention(t *testing.T) {
	db := testutil.OpenTestDB(t)

	c := NewNotificationDeliveryCleaner(db)
	if got := c.retentionWindowDays(); got != DefaultNotificationDeliveryRetentionDays {
		t.Errorf("default audit window = %d, want %d", got, DefaultNotificationDeliveryRetentionDays)
	}
	if DefaultNotificationDeliveryRetentionDays != DefaultNotificationRetentionDays*notificationUnreadRetentionMultiplier {
		t.Errorf("audit default %d != notification body max %d",
			DefaultNotificationDeliveryRetentionDays, DefaultNotificationRetentionDays*notificationUnreadRetentionMultiplier)
	}

	// 系统级保留期变更后立即生效 (快照源语义), 且跟着通知本体窗口一起变。
	SetSystemRetentionDays(30)
	defer SetSystemRetentionDays(90)
	if got := c.retentionWindowDays(); got != 60 {
		t.Errorf("after SetSystemRetentionDays(30): audit window = %d, want 60 (通知最长窗)", got)
	}
}

// TestNotificationDeliveryCleaner_Batches — 分批删除 (与 purge 同范式):
// 每批独立事务, 多轮才能删尽; 断言总条数精确。
func TestNotificationDeliveryCleaner_Batches(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	const expired = 5
	for i := 0; i < expired; i++ {
		seedDelivery(t, db, models.DeliveryStateDelivered, now.AddDate(0, 0, -200))
	}
	seedDelivery(t, db, models.DeliveryStateDelivered, now.AddDate(0, 0, -1))

	c := NewNotificationDeliveryCleaner(db)
	c.SetRetention(180)
	c.SetBatchSize(1) // 强制每批 1 行
	c.SetBatchSleep(0)
	c.now = func() time.Time { return now }

	report, err := c.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("delivery cleanup run: %v", err)
	}
	if report.DeliveredDeleted != expired {
		t.Errorf("DeliveredDeleted = %d, want %d", report.DeliveredDeleted, expired)
	}
	if got := countDeliveries(t, db); got != 1 {
		t.Fatalf("remaining = %d, want 1 (fresh delivered row kept)", got)
	}
}

// TestRetentionTask_RunsDeliveryCleanup — 接线自证: 投递审计清理挂在既有
// 每日 retention 任务上 (无新增 goroutine / 不动 main.go), 与 notifications
// 清理同批执行, 且不干扰逐设备保留期处理。
func TestRetentionTask_RunsDeliveryCleanup(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	seedDelivery(t, db, models.DeliveryStateDelivered, now.AddDate(0, 0, -200)) // 审计窗外 → 删
	seedDelivery(t, db, models.DeliveryStateFailed, now.AddDate(0, 0, -200))    // 审计窗外 → 删
	seedDelivery(t, db, models.DeliveryStateFailed, now.AddDate(0, 0, -100))    // 审计窗内 → 留
	seedNotification(t, db, "ancient-unread", false, now.AddDate(0, 0, -1000))  // 同批的 notifications 清理
	ld := seedRetentionDevice(t, db, "d1-deliveries", 365, now.AddDate(0, 0, -400))
	_ = ld

	r := NewRetentionTask(db)
	r.now = func() time.Time { return now }
	r.SetBatchSleep(0)
	r.notifier.SetRetention(90, 180)
	r.notifier.SetBatchSleep(0)
	r.notifier.now = func() time.Time { return now }
	r.deliveries.SetBatchSleep(0)
	r.deliveries.now = func() time.Time { return now }

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
	// 同批: notifications 清空 (只剩 retention 自己发的, 该设备到期不发) + 投递审计剩 1 条
	if got := countNotifications(t, db); got != 0 {
		t.Errorf("notifications after retention run = %d, want 0", got)
	}
	if got := countDeliveries(t, db); got != 1 {
		t.Errorf("deliveries after retention run = %d, want 1 (inside-window kept)", got)
	}
	if states := statesOfDeliveries(t, db); states[0] != models.DeliveryStateFailed {
		t.Errorf("kept delivery state = %v, want failed", states)
	}
}
