package datalifecycle

// retention_task_test.go — 系统级保留期窗口的测试。
//
// 2026-10-23 语义变更: 保留期从「每个逻辑身份一个窗口 + 30/7 天两档通知」回到
// 「一个系统级窗口，到期直接分批硬删」。因此这一组测试比改造前**更短**：
// 不再需要按身份设置 retention_days、不再有临期通知、不再有 pending 合并顺延。
// 保留的三条不变量:
//   1. 过期行被删、未过期行保留；
//   2. 删除与查询口径一致（都按 device_id 直查，不存在「查得到但删不掉」）；
//   3. 非 PG（无分区）时主路径照常工作 —— 分区清扫是 no-op 而非空转。

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

// seedRetentionRows 建一台设备并写入两行数据: expired(now-days 之前) 与 fresh。
func seedRetentionRows(t *testing.T, db *gorm.DB, name string, now time.Time, expiredDaysAgo, freshDaysAgo int) *models.EdgeDevice {
	t.Helper()
	dev := seedDevice(t, db, name, "bms_jbd", name+"-hw", false)
	if err := db.Create(&models.UnifiedData{
		DeviceID:   dev.ID,
		SensorName: "voltage",
		Value:      1,
		Timestamp:  now.AddDate(0, 0, -expiredDaysAgo),
	}).Error; err != nil {
		t.Fatalf("seed expired row: %v", err)
	}
	if err := db.Create(&models.UnifiedData{
		DeviceID:   dev.ID,
		SensorName: "voltage",
		Value:      2,
		Timestamp:  now.AddDate(0, 0, -freshDaysAgo),
	}).Error; err != nil {
		t.Fatalf("seed fresh row: %v", err)
	}
	return dev
}

func TestRetentionTask_DeletesExpiredKeepsFresh(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	SetSystemRetentionDays(90)
	defer SetSystemRetentionDays(90)
	seedRetentionRows(t, db, "ret", now, 400, 10)

	r := NewRetentionTask(db)
	r.now = func() time.Time { return now }
	r.SetBatchSize(1) // 强制分批
	r.SetBatchSleep(0)
	results, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(results) != 1 || results[0].Days != 90 {
		t.Fatalf("results = %+v, want one result with Days=90", results)
	}
	if results[0].RowsDeleted != 1 {
		t.Fatalf("RowsDeleted = %d, want 1 (only the expired row)", results[0].RowsDeleted)
	}
	var remaining int64
	db.Model(&models.UnifiedData{}).Count(&remaining)
	if remaining != 1 {
		t.Errorf("remaining rows = %d, want 1 (fresh row kept)", remaining)
	}
	var kept models.UnifiedData
	db.First(&kept)
	if kept.Value != 2 {
		t.Errorf("kept row value = %v, want 2 (the fresh row)", kept.Value)
	}
}

func TestRetentionTask_NothingExpired(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	SetSystemRetentionDays(90)
	defer SetSystemRetentionDays(90)
	seedRetentionRows(t, db, "safe", now, 80, 10) // 两行都在窗口内

	r := NewRetentionTask(db)
	r.now = func() time.Time { return now }
	results, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if results[0].RowsDeleted != 0 {
		t.Errorf("RowsDeleted = %d, want 0", results[0].RowsDeleted)
	}
	var count int64
	db.Model(&models.UnifiedData{}).Count(&count)
	if count != 2 {
		t.Errorf("rows = %d, want 2 (nothing expired)", count)
	}
}

// TestRetentionTask_CoversAllDevices — 系统级窗口对所有设备一视同仁（这正是
// 移除「按身份配置保留期」后要保住的性质: 不再有谁能逃过保留期）。
func TestRetentionTask_CoversAllDevices(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	SetSystemRetentionDays(90)
	defer SetSystemRetentionDays(90)
	seedRetentionRows(t, db, "dev-a", now, 400, 10)
	seedRetentionRows(t, db, "dev-b", now, 500, 20)

	r := NewRetentionTask(db)
	r.now = func() time.Time { return now }
	r.SetBatchSleep(0)
	results, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if results[0].RowsDeleted != 2 {
		t.Errorf("RowsDeleted = %d, want 2 (one expired row per device)", results[0].RowsDeleted)
	}
	var remaining int64
	db.Model(&models.UnifiedData{}).Count(&remaining)
	if remaining != 2 {
		t.Errorf("remaining = %d, want 2 (fresh rows on both devices kept)", remaining)
	}
}

// TestRetentionTask_NegativeWindowIgnored — 防御: 负值更可能是配置写错
// （shell 变量没展开、减一过头），SetSystemRetentionDays 会忽略它、保持原值。
//
// 2026-11-07 语义变更：**0 不再走这条路**，0 现在是「永久保留」（见下一条用例）。
// 保留负值拒绝的理由：把负值当「永久」会让一个笔误变成「再也不清理」的静默事故，
// 因此 fail-safe 到原窗口。
func TestRetentionTask_NegativeWindowIgnored(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	SetSystemRetentionDays(90)
	defer SetSystemRetentionDays(90)
	SetSystemRetentionDays(-5) // 非法值: 忽略，保持 90
	if got := SystemRetentionDays(); got != 90 {
		t.Fatalf("SystemRetentionDays = %d, want 90 (negative values ignored)", got)
	}
	seedRetentionRows(t, db, "def", now, 400, 10)

	r := NewRetentionTask(db)
	r.now = func() time.Time { return now }
	r.SetBatchSleep(0)
	results, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// 90 天窗口仍然生效：400 天前的行必须被删。
	if results[0].RowsDeleted != 1 {
		t.Errorf("RowsDeleted = %d, want 1 (90-day window still applies; negative value ignored)",
			results[0].RowsDeleted)
	}
}

// ==================== 永久保留（EHOME_DATA_RETENTION_DAYS=0，2026-11-07）====================
//
// 用户裁决「所有数据要长期保留」（docs/设计/老数据迁移到帧表-分析与契约-2026-11-07.md §5）。
// 改造前这里是个**反向 bug**：RunOnce 把 days<=0 兜底成 1 天，于是「想永久保留」的运维
// 把环境变量设成 0，反而删得比 90 天更狠 —— 且不报错，只有数据一天天变少。
//
// 这一组用例钉住三件事，缺一不可:
//   1. days=0 时**数据一行都不删**（分区 DROP + 行级 DELETE 都跳过）；
//   2. days=0 时**旁路清理器照跑** —— 它们没有独立 goroutine，唯一调用点就是 RunOnce，
//      整体 return 会让通知/审计/命令域等 8 张表无限增长；
//   3. 结果里 Permanent=true，让调用方能区分「永久」与「保留 N 天」。

// seedPermanentRetentionRows 造两件事：一条远古 unified_data 行 + 一条过期的已读通知。
// unified_data 用来证明「不删数据」，通知用来证明「旁路清理器仍然工作」。
func seedPermanentRetentionRows(t *testing.T, db *gorm.DB, now time.Time) (*models.EdgeDevice, *models.Notification) {
	t.Helper()
	dev := seedDevice(t, db, "perm", "bms_jbd", "perm-hw", false)
	if err := db.Create(&models.UnifiedData{
		DeviceID:   dev.ID,
		SensorName: "voltage",
		Value:      1,
		Timestamp:  now.AddDate(0, 0, -4000), // 远古行：任何有限窗口都该删它
	}).Error; err != nil {
		t.Fatalf("seed ancient row: %v", err)
	}
	// 已读通知 + 1000 天前 ⇒ 远超通知默认窗口（已读 90 天）。
	notif := seedNotification(t, db, "perm-notif", true, now.AddDate(0, 0, -1000))
	return dev, notif
}

// TestRetentionTask_PermanentRetentionKeepsAllData — days=0 ⇒ 不删任何数据行。
func TestRetentionTask_PermanentRetentionKeepsAllData(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	SetSystemRetentionDays(0)
	defer SetSystemRetentionDays(90)
	seedPermanentRetentionRows(t, db, now)

	r := NewRetentionTask(db)
	r.now = func() time.Time { return now }
	r.SetBatchSleep(0)
	results, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	if results[0].RowsDeleted != 0 {
		t.Errorf("RowsDeleted = %d, want 0（永久保留不得删任何行）", results[0].RowsDeleted)
	}
	if !results[0].Permanent {
		t.Error("Permanent = false, want true（调用方要能区分「永久」与「保留 N 天」）")
	}
	if results[0].Days != 0 {
		t.Errorf("Days = %d, want 0（0 就是「永久」这个事实本身，不该被改写成 1）", results[0].Days)
	}

	var remaining int64
	db.Model(&models.UnifiedData{}).Count(&remaining)
	if remaining != 1 {
		t.Errorf("unified_data 剩余 = %d, want 1（4000 天前的行也必须留着）", remaining)
	}
}

// TestRetentionTask_PermanentRetentionStillRunsBypassCleaners — days=0 ⇒ 旁路清理器照跑。
//
// 这是本轮最容易做错的一处：直接 return 会让 8 张元数据表无限增长，
// 而且不会有任何报错（它们的清理器没有独立 goroutine，唯一调用点就是 RunOnce）。
func TestRetentionTask_PermanentRetentionStillRunsBypassCleaners(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	SetSystemRetentionDays(0)
	defer SetSystemRetentionDays(90)
	seedPermanentRetentionRows(t, db, now)

	if got := countNotifications(t, db); got != 1 {
		t.Fatalf("前置: notifications = %d, want 1", got)
	}

	r := NewRetentionTask(db)
	r.now = func() time.Time { return now }
	r.SetBatchSleep(0)
	if _, err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}

	// 过期已读通知必须被删掉 —— 证明旁路清理器真的跑了（没有被永久保留连带跳过）。
	if got := countNotifications(t, db); got != 0 {
		t.Errorf("notifications 剩余 = %d, want 0（旁路清理器必须照跑；跳过会让 8 张表无限增长）", got)
	}
	// 同时数据行仍在（上一条用例已单独断言，这里再确认两条路径互不干扰）。
	var rows int64
	db.Model(&models.UnifiedData{}).Count(&rows)
	if rows != 1 {
		t.Errorf("unified_data 剩余 = %d, want 1", rows)
	}
}

// TestRetentionTask_FiniteWindowStillDeletes — days=1 回归：有限窗口行为不变。
//
// 契约要求「days=1 时行为不变」。这里同时钉住三个量：删掉过期行、留下新行、
// Permanent=false（不能因为 Days 小就误判成永久）。
func TestRetentionTask_FiniteWindowStillDeletes(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	SetSystemRetentionDays(1)
	defer SetSystemRetentionDays(90)
	seedRetentionRows(t, db, "one", now, 2, 0) // 2 天前 = 过期；今天 = 保留

	r := NewRetentionTask(db)
	r.now = func() time.Time { return now }
	r.SetBatchSleep(0)
	results, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if results[0].Permanent {
		t.Error("Permanent = true, want false（days=1 是有限窗口）")
	}
	if results[0].Days != 1 {
		t.Errorf("Days = %d, want 1", results[0].Days)
	}
	if results[0].RowsDeleted != 1 {
		t.Errorf("RowsDeleted = %d, want 1（2 天前的行超出 1 天窗口）", results[0].RowsDeleted)
	}
	var remaining int64
	db.Model(&models.UnifiedData{}).Count(&remaining)
	if remaining != 1 {
		t.Errorf("remaining = %d, want 1（今天的行必须留着）", remaining)
	}
}

// TestSetSystemRetentionDays_ZeroTakesEffect — 0 必须能覆盖掉原值。
//
// 改造前 SetSystemRetentionDays 用 if days > 0 忽略 0，于是 main.go 无条件写入
// 的 EHOME_DATA_RETENTION_DAYS=0 永远到不了 RunOnce —— 配置语义无法传导。
func TestSetSystemRetentionDays_ZeroTakesEffect(t *testing.T) {
	SetSystemRetentionDays(90)
	defer SetSystemRetentionDays(90)
	if IsRetentionPermanent() {
		t.Fatal("前置: 90 天不应该是永久")
	}
	SetSystemRetentionDays(0)
	if got := SystemRetentionDays(); got != 0 {
		t.Errorf("SystemRetentionDays = %d, want 0（0 必须生效，不能被 if days>0 忽略）", got)
	}
	if !IsRetentionPermanent() {
		t.Error("IsRetentionPermanent() = false, want true（0 = 永久保留）")
	}
	// 负值仍然被忽略（fail-safe 到原值）。
	SetSystemRetentionDays(-1)
	if got := SystemRetentionDays(); got != 0 {
		t.Errorf("负值把保留期改成了 %d，want 0（负值必须被忽略）", got)
	}
}

// TestNotificationWindowFallsBackWhenRetentionPermanent — 系统级保留期=0 时，
// 旁路清理器**不能**跟着变成「永久」。
//
// notificationRetentionWindows 的回落链是: 显式覆盖 → SystemRetentionDays() → 默认 90。
// 系统级保留期变成 0（永久）之后，第二级返回 0，于是落到第三级的 90 —— 这正是
// 「数据永久保留、元数据照旧清理」的实现基础。
//
// 为什么单独立一条: 这段逻辑长得像「脏值兜底」，后来的维护者很可能顺手把它删掉
// （理由会是「0 现在有语义了，不该回落」）—— 那会让通知表随数据一起永久膨胀。
func TestNotificationWindowFallsBackWhenRetentionPermanent(t *testing.T) {
	SetSystemRetentionDays(0)
	defer SetSystemRetentionDays(90)

	read, unread := notificationRetentionWindows(0, 0)
	if read != DefaultNotificationRetentionDays {
		t.Errorf("read = %d, want %d（系统级 0=永久时，通知仍按自己的默认窗口清理）",
			read, DefaultNotificationRetentionDays)
	}
	wantUnread := DefaultNotificationRetentionDays * notificationUnreadRetentionMultiplier
	if unread != wantUnread {
		t.Errorf("unread = %d, want %d", unread, wantUnread)
	}
	// 投递审计窗口同源，必须一起回落（否则证据比被审计对象活得短/长都不对）。
	if got := notificationBodyMaxRetentionDays(); got != wantUnread {
		t.Errorf("notificationBodyMaxRetentionDays = %d, want %d", got, wantUnread)
	}
}
