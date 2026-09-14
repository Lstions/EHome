package datalifecycle

import (
	"context"
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

// PG-only 反向证明 (D-2 执行者如实披露的证据缺口):
//
// D-2 的 5 个测试**只跑在 SQLite** (testutil.OpenTestDB 默认 sqlite), 而清理
// 用的是 `DELETE ... WHERE id IN (SELECT ... LIMIT ?)` —— 子查询 LIMIT 的语义,
// 恰恰是 PG 与 SQLite 最可能不同的地方。因此本文件用仓库既有的 PG 测试范式
// (partition_mgr_pg_test.go 的 requirePostgres + testutil.OpenTestDB) 把同一批
// 断言在 PostgreSQL 下**真跑一遍**, 并且断言**具体条数** (不是"没报错")。
//
// 运行方式 (与 make test-integration 同范式):
//
//	EHOME_TEST_DB=postgres EHOME_DB_HOST=127.0.0.1 EHOME_DB_PORT=5432 \
//	EHOME_DB_USER=ehome EHOME_DB_PASSWORD=<pw> EHOME_DB_NAME=<隔离库> \
//	go test -buildvcs=false -count=1 ./internal/datalifecycle/ -run 'Postgres' -v
//
// 未设 EHOME_TEST_DB=postgres 时 requirePostgres(t) 会 skip (SQLite 单测
// 已由 notification_cleanup_test.go / notification_delivery_cleanup_test.go
// 覆盖同样的语义) —— skip 是显式的, 不会伪装成"跑过了"。
//
// 库隔离 (数据库纪律): testutil.OpenTestDB 在 `EHOME_DB_NAME` 指定的库里
// 建一个随机 schema `test_<pid>_<nanos>`, 把所有表建在该 schema 内, 结束时
// t.Cleanup 执行 `DROP SCHEMA ... CASCADE`。因此本文件对落脚库的**表数据零写入**
// —— 全部读写都发生在一次性 schema 里, 跑完不留痕。

// TestNotificationCleaner_Postgres_CountsExact — notifications 清理在 PG 下的
// 条数级验证 (D-2 未覆盖的那一半)。三档窗口都用上, 断言每档精确条数。
func TestNotificationCleaner_Postgres_CountsExact(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	if !testutil.IsPostgres() {
		t.Fatal("EHOME_TEST_DB=postgres 但 IsPostgres() 为假: 方言判定与连接不一致")
	}
	if db.Dialector.Name() != "postgres" {
		t.Fatalf("dialector = %q, want postgres (SQLite 下本测试必须 skip 而不是静默替换方言)", db.Dialector.Name())
	}

	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	// 已读档 (窗口 90): 2 删 / 1 留
	seedNotification(t, db, "pg-read-old-1", true, now.AddDate(0, 0, -100))
	seedNotification(t, db, "pg-read-old-2", true, now.AddDate(0, 0, -91))
	seedNotification(t, db, "pg-read-new", true, now.AddDate(0, 0, -89))
	// 未读档 (窗口 180): 1 删 / 1 留
	seedNotification(t, db, "pg-unread-old", false, now.AddDate(0, 0, -181))
	seedNotification(t, db, "pg-unread-new", false, now.AddDate(0, 0, -179))
	if got := countNotifications(t, db); got != 5 {
		t.Fatalf("seeded notifications = %d, want 5", got)
	}

	c := NewNotificationCleaner(db)
	c.SetRetention(90, 180) // notifications 档位: 已读 90 / 未读 180
	c.SetBatchSize(2)       // 强制多批: 走 DELETE ... id IN (SELECT ... LIMIT 2) 的循环
	c.SetBatchSleep(0)
	c.now = func() time.Time { return now }

	report, err := c.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("PG notification cleanup: %v", err)
	}
	if report.ReadDeleted != 2 {
		t.Errorf("PG ReadDeleted = %d, want 2", report.ReadDeleted)
	}
	if report.UnreadDeleted != 1 {
		t.Errorf("PG UnreadDeleted = %d, want 1", report.UnreadDeleted)
	}
	if got := countNotifications(t, db); got != 2 {
		t.Fatalf("PG remaining notifications = %d, want 2", got)
	}
	var titles []string
	db.Model(&models.Notification{}).Order("title").Pluck("title", &titles)
	want := []string{"pg-read-new", "pg-unread-new"}
	if len(titles) != 2 || titles[0] != want[0] || titles[1] != want[1] {
		t.Errorf("PG remaining = %v, want %v", titles, want)
	}

	// 幂等: PG 上再跑一轮同样 0 行。
	report, err = c.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("PG second run: %v", err)
	}
	if report.ReadDeleted != 0 || report.UnreadDeleted != 0 {
		t.Errorf("PG second run deleted %+v, want no-op", report)
	}
	if got := countNotifications(t, db); got != 2 {
		t.Errorf("PG after rerun = %d, want 2", got)
	}
}

// TestNotificationDeliveryCleaner_Postgres_CountsExact — 投递审计清理在 PG 下的
// 条数级验证: 成功档 (45 天) 与失败/未决档 (180 天) 各自的删/留边界, 以及
// 白名单外 state 的 fail-closed 保留。
func TestNotificationDeliveryCleaner_Postgres_CountsExact(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	if db.Dialector.Name() != "postgres" {
		t.Fatalf("dialector = %q, want postgres", db.Dialector.Name())
	}

	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	// 三档共用审计窗口 180 天 (不变式: 不得短于通知本体最长保留期):
	// 窗外 → 删; 窗内 → 留。特别地 60 天前的 delivered **必须留** ——
	// 它对应的未读通知本体 (180 天窗) 还在, 追问对象还在。
	seedDelivery(t, db, models.DeliveryStateDelivered, now.AddDate(0, 0, -181)) // 删
	seedDelivery(t, db, models.DeliveryStateFailed, now.AddDate(0, 0, -181))    // 删
	seedDelivery(t, db, models.DeliveryStatePending, now.AddDate(0, 0, -181))   // 删
	seedDelivery(t, db, models.DeliveryStateDelivered, now.AddDate(0, 0, -60))  // 留 (关键: 曾被误删)
	seedDelivery(t, db, models.DeliveryStateDelivered, now.AddDate(0, 0, -179)) // 留
	seedDelivery(t, db, models.DeliveryStateFailed, now.AddDate(0, 0, -179))    // 留
	seedDelivery(t, db, models.DeliveryStatePending, now.AddDate(0, 0, -179))   // 留
	// 白名单外: 永不删
	seedDelivery(t, db, "suppressed", now.AddDate(0, 0, -9999))
	if got := countDeliveries(t, db); got != 8 {
		t.Fatalf("seeded deliveries = %d, want 8", got)
	}

	c := NewNotificationDeliveryCleaner(db)
	c.SetRetention(180) // = 通知本体最长保留期 (不变式下限)
	c.SetBatchSize(2)   // 强制多批
	c.SetBatchSleep(0)
	c.now = func() time.Time { return now }

	report, err := c.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("PG delivery cleanup: %v", err)
	}
	if report.DeliveredDeleted != 1 {
		t.Errorf("PG DeliveredDeleted = %d, want 1 (only the 181d row)", report.DeliveredDeleted)
	}
	if report.FailedDeleted != 1 {
		t.Errorf("PG FailedDeleted = %d, want 1", report.FailedDeleted)
	}
	if report.PendingDeleted != 1 {
		t.Errorf("PG PendingDeleted = %d, want 1", report.PendingDeleted)
	}
	if report.Total() != 3 {
		t.Errorf("PG total deleted = %d, want 3", report.Total())
	}
	if got := countDeliveries(t, db); got != 5 {
		t.Fatalf("PG remaining deliveries = %d, want 5", got)
	}
	// 留下的必须是: 2 条窗内 delivered (含 60 天那条) + 窗内 failed + 窗内 pending + suppressed
	var states []string
	db.Model(&models.NotificationDelivery{}).Order("id").Pluck("state", &states)
	want := []string{"delivered", "delivered", "failed", "pending", "suppressed"}
	if len(states) != len(want) {
		t.Fatalf("PG remaining states = %v (count %d), want %d", states, len(states), len(want))
	}
	for i := range want {
		if states[i] != want[i] {
			t.Fatalf("PG remaining states = %v, want %v", states, want)
		}
	}
	// 关键不变式在 PG 上的直接断言: "已过 45 天但仍在 180 天审计窗内"的
	// delivered 证据必须**全部**存活 (本用例里有 60 天与 179 天两条)。
	// 旧的短窗策略 (45 天) 会把这两条全删 —— 这正是被证伪的那条论据。
	var aged int64
	db.Model(&models.NotificationDelivery{}).
		Where("state = ? AND created_at < ?", models.DeliveryStateDelivered, now.AddDate(0, 0, -45)).
		Count(&aged)
	if aged != 2 {
		t.Errorf("PG: 45~180 天窗口内的 delivered 证据存活数 = %d, want 2 "+
			"(证据不得比通知本体先死)", aged)
	}

	// PG 下幂等 + 白名单行在多轮后仍然存活 (fail-closed 不是一次性的巧合)。
	report, err = c.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("PG second run: %v", err)
	}
	if report.Total() != 0 {
		t.Errorf("PG second run deleted %+v, want no-op", report)
	}
	if got := countDeliveries(t, db); got != 5 {
		t.Errorf("PG after rerun = %d, want 5", got)
	}
}

// TestNotificationDeliveryCleaner_Postgres_BatchLoopTerminates —
// PG 下的 LIMIT 分批循环语义: 每批 1 行也能删尽 (若 PG 的子查询 LIMIT
// 行为与 SQLite 不同 —— 例如每批重复删同一批行或提前终止 —— 条数断言会红)。
func TestNotificationDeliveryCleaner_Postgres_BatchLoopTerminates(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	const expired = 7
	for i := 0; i < expired; i++ {
		seedDelivery(t, db, models.DeliveryStateDelivered, now.AddDate(0, 0, -100))
	}

	c := NewNotificationDeliveryCleaner(db)
	// 本测试只验证 LIMIT 分批循环的语义 (与保留期取值无关), 故用 30 天窗口
	// 让全部行落在窗外; 不变式本身由 TestDeliveryCleanup_WindowNeverShorterThanNotificationBody
	// 与 PG 的 CountsExact 用例约束 (那里窗口 = 180 = 不变式下限)。
	c.SetRetention(30)
	c.SetBatchSize(1)
	c.SetBatchSleep(0)
	c.now = func() time.Time { return now }

	report, err := c.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("PG batched delivery cleanup: %v", err)
	}
	if report.DeliveredDeleted != expired {
		t.Errorf("PG batched DeliveredDeleted = %d, want %d", report.DeliveredDeleted, expired)
	}
	if got := countDeliveries(t, db); got != 0 {
		t.Errorf("PG remaining = %d, want 0", got)
	}
}
