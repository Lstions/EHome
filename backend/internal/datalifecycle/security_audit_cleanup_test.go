package datalifecycle

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/commandexec"
	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

// ─ security_audit_events 清理器的测试 (设计 §2.3) ──────────────────────
//
// 每条的注释都写明【什么样的改动会让它变红】—— 让它成为约束, 而不是当前取值的快照
// (与 command_domain_cleanup_test.go 同一手法)。
//
// 覆盖:
//   - 边界: 730 天整 (留在窗内) / 729 天 (留) / 731 天 (删);
//   - 只按 created_at 判 (不按 event_name / result / actor_type);
//   - fail-closed: SetRetention(0) / SetRetention(-1) 回落到 730 ——
//     【不得】解释成"永久保留", 也【不得】删光;
//   - 幂等 (第二轮 0 行)、分批 (SetBatchSize(1) 仍删尽);
//   - 不碰别的表 (INV-3: command_executions / automation_events / notifications /
//     notification_deliveries / node_events 行数不变);
//   - INV-6: 本表窗口 == command_executions 窗口 (不得更长, 否则产生孤证审计行);
//   - 挂载点: 第 7 个清理器必须真的在每日 RetentionTask.RunOnce 里被调用。

// auditNow 与命令域用例共用同一注入时钟 (cmdNow), 但单独起名以免两处将来漂移时
// 相互掩盖; 两者当前必须相等。
func auditNow() time.Time { return cmdNow() }

func seedSecurityAuditEvent(t *testing.T, db *gorm.DB, eventName, result, actorSnapshot string, createdAt time.Time) *models.SecurityAuditEvent {
	t.Helper()
	row := &models.SecurityAuditEvent{
		ActorType:     "user",
		ActorSnapshot: actorSnapshot,
		EventName:     eventName,
		EventVersion:  1,
		Result:        result,
		RequestID:     "req-" + eventName,
		SourceIP:      "10.0.0.1",
		TargetType:    "edge_device",
		TargetID:      "1",
		Metadata:      "{}",
		CreatedAt:     createdAt,
	}
	if err := db.Create(row).Error; err != nil {
		t.Fatalf("seed security audit event %s: %v", eventName, err)
	}
	return row
}

func auditEventNames(t *testing.T, db *gorm.DB) []string {
	t.Helper()
	var names []string
	if err := db.Model(&models.SecurityAuditEvent{}).Order("id").Pluck("event_name", &names).Error; err != nil {
		t.Fatalf("pluck security audit event names: %v", err)
	}
	return names
}

func auditCount(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&models.SecurityAuditEvent{}).Count(&n).Error; err != nil {
		t.Fatalf("count security audit events: %v", err)
	}
	return n
}

func assertAuditStrings(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v (count %d), want %v (count %d)", label, got, len(got), want, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s = %v, want %v", label, got, want)
		}
	}
}

func newSecurityAuditCleaner(db *gorm.DB, now time.Time) *SecurityAuditCleaner {
	c := NewSecurityAuditCleaner(db)
	c.SetBatchSleep(0)
	c.now = func() time.Time { return now }
	return c
}

// ── 边界: 730 整 / 729 / 731 ─────────────────────────────────────────

// TestSecurityAuditCleaner_BoundaryDays 钉住时间窗的每一条边界。
//
// cutoff = now - 730×24h, 删除条件是 created_at < cutoff (严格小于):
//   - 731 天前  ⇒ 删;
//   - 730 天【整】⇒ 【留】(恰好等于 cutoff 不算"更早", 与 purge/retention/
//     command_attempt 的既有语义逐字一致 —— 有人把 < 改成 <= 本用例立刻红);
//   - 729 天前  ⇒ 留;
//   - 刚刚写入  ⇒ 留。
//
// 变红条件: 窗口写成 400/365/90、比较符写成 <=、cutoff 用日历天 (AddDate) 算。
func TestSecurityAuditCleaner_BoundaryDays(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := auditNow()

	seedSecurityAuditEvent(t, db, "old-731", "success", "admin", cmdDaysAgo(now, 731))
	seedSecurityAuditEvent(t, db, "exact-730", "success", "admin", cmdDaysAgo(now, 730))
	seedSecurityAuditEvent(t, db, "recent-729", "success", "admin", cmdDaysAgo(now, 729))
	seedSecurityAuditEvent(t, db, "fresh", "success", "admin", now.Add(-time.Hour))

	c := newSecurityAuditCleaner(db, now)
	report, err := c.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("security audit cleanup: %v", err)
	}
	if report.Deleted != 1 {
		t.Errorf("Deleted = %d, want 1 (只有 731 天前那条)", report.Deleted)
	}
	if report.RetentionDays != 730 {
		t.Errorf("RetentionDays = %d, want 730", report.RetentionDays)
	}
	// 断言列表按 id 升序 (播种顺序): old-731 被删后依次是 730/729/fresh 三条。
	assertAuditStrings(t, "remaining audit events", auditEventNames(t, db),
		[]string{"exact-730", "recent-729", "fresh"})

	// 默认窗口必须是设计裁决的 730 (不是系统级保留期 90, 也不是 400)。
	if got := NewSecurityAuditCleaner(db).retentionWindowDays(); got != 730 {
		t.Errorf("默认窗口 = %d, want 730 (设计 §2.3 裁决)", got)
	}
}

// ── 只按 created_at 删 ──────────────────────────────────────────────

// TestSecurityAuditCleaner_DeletesByCreatedAtOnly 断言删除判定【只看 created_at】。
//
// 构造一组"created_at 旧但别的列看起来全新/更晚"的行: event_name 的字典序、
// result 取值、actor_snapshot、request_id 都在时间窗的【另一侧】。若有人把判定
// 改成 event_name/result/actor_type 白名单 (命令域那套范式照搬过来), 或对
// "success" 之类的取值单独开恩, 本用例立刻红。
//
// 反向对照: created_at 新、其余列"看起来旧" (actor=ehome-system/request_id 很老)
// 的行必须留下 —— 清理器不得从任何别的列推断时间。
//
// 变红条件: 删除语句里出现 created_at 以外的判定列。
func TestSecurityAuditCleaner_DeletesByCreatedAtOnly(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := auditNow()
	old, recent := cmdDaysAgo(now, 800), cmdDaysAgo(now, 10)

	// 删 (created_at 旧, 与取值无关):
	seedSecurityAuditEvent(t, db, "zzz.old.success", "success", "admin", old)
	seedSecurityAuditEvent(t, db, "aaa.old.queued", "queued", "system", old)
	seedSecurityAuditEvent(t, db, "mmm.old.weird", "some_future_result_value", "admin", old)
	seedSecurityAuditEvent(t, db, "auth.password.reset", "failure", "ehomectl", old)

	// 留 (created_at 新, 哪怕其它列"看起来像很久以前的事"):
	seedSecurityAuditEvent(t, db, "aaa.recent.success", "success", "ehome-system", recent)
	seedSecurityAuditEvent(t, db, "zzz.recent.denied", "denied", "legacy-admin", recent)

	report, err := newSecurityAuditCleaner(db, now).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("security audit cleanup: %v", err)
	}
	if report.Deleted != 4 {
		t.Errorf("Deleted = %d, want 4 (全部按 created_at 到期)", report.Deleted)
	}
	assertAuditStrings(t, "remaining audit events", auditEventNames(t, db),
		[]string{"aaa.recent.success", "zzz.recent.denied"})
}

// ── fail-closed: 0 / 负数【不是】"永久保留" ──────────────────────────

// TestSecurityAuditCleaner_NonPositiveRetentionFallsBackTo730 把设计 §2.3 的明文
// 要求写成测试:
//
//	"保留期 ... 非法值 fail-closed 回落到该值, 不允许 0/负数表示「永久」——
//	 那会重新打开无界增长"
//
// 三条断言, 任一条被破坏即红:
//  1. SetRetention(0) 与 SetRetention(-1) 的【生效窗口】必须是 730;
//  2. 到期行仍然被删 (不能被解释成永久保留 ⇒ 表重新无界增长);
//  3. 730 天内的行必须留下 (负值若被当成 cutoff 会让 cutoff 落到未来, 把窗内的
//     行也删掉 —— 那是比"不删"更危险的另一种失败)。
//
// 变红条件: retentionWindowDays 写成 "if days == 0 { 永久 }"、写成 "return c.windowDays"
// (负值直接生效)、或把非正值静默当成 1 天 (窗内行被删光)。
func TestSecurityAuditCleaner_NonPositiveRetentionFallsBackTo730(t *testing.T) {
	for _, tc := range []struct {
		name string
		days int
	}{
		{"zero-means-fallback-not-forever", 0},
		{"negative-means-fallback-not-future-cutoff", -1},
		{"large-negative", -730},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := testutil.OpenTestDB(t)
			now := auditNow()
			seedSecurityAuditEvent(t, db, "expired", "success", "admin", cmdDaysAgo(now, 800))
			seedSecurityAuditEvent(t, db, "in-window", "success", "admin", cmdDaysAgo(now, 700))

			c := newSecurityAuditCleaner(db, now)
			c.SetRetention(tc.days)

			if got := c.retentionWindowDays(); got != 730 {
				t.Fatalf("SetRetention(%d) 后生效窗口 = %d, want 730 (fail-closed 回落; "+
					"0/负数【不得】表示永久保留 —— 设计 §2.3 明文)", tc.days, got)
			}
			report, err := c.RunOnce(context.Background())
			if err != nil {
				t.Fatalf("security audit cleanup: %v", err)
			}
			if report.RetentionDays != 730 {
				t.Errorf("报告窗口 = %d, want 730", report.RetentionDays)
			}
			if report.Deleted != 1 {
				t.Errorf("Deleted = %d, want 1 (0/负数绝不能被解释成永不删除)", report.Deleted)
			}
			// 窗内行不得被删 —— 负值若直接参与 cutoff 计算就会走到这里。
			assertAuditStrings(t, "remaining audit events", auditEventNames(t, db), []string{"in-window"})
		})
	}
}

// TestSecurityAuditCleaner_PositiveRetentionIsConfigurable 确认保留期【确实可配置】
// (§2.3 原文: "保留期作为可配置常量"), 而不是把 730 写死在删除语句里。
//
// 变红条件: retentionWindowDays 忽略 windowDays 覆盖 (例如直接 return 730)。
func TestSecurityAuditCleaner_PositiveRetentionIsConfigurable(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := auditNow()
	seedSecurityAuditEvent(t, db, "age-100", "success", "admin", cmdDaysAgo(now, 100))
	seedSecurityAuditEvent(t, db, "age-10", "success", "admin", cmdDaysAgo(now, 10))

	c := newSecurityAuditCleaner(db, now)
	c.SetRetention(30)
	if got := c.retentionWindowDays(); got != 30 {
		t.Fatalf("SetRetention(30) 后生效窗口 = %d, want 30", got)
	}
	report, err := c.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("security audit cleanup: %v", err)
	}
	if report.Deleted != 1 {
		t.Errorf("Deleted = %d, want 1 (窗口 30 天)", report.Deleted)
	}
	assertAuditStrings(t, "remaining audit events", auditEventNames(t, db), []string{"age-10"})
}

// ── 幂等 + 分批 + 不碰别的表 ─────────────────────────────────────────

// TestSecurityAuditCleaner_IdempotentBatchedAndTouchesNothingElse 一次钉住三件事。
//
//  1. 幂等: 第二轮 Deleted = 0;
//  2. 分批: SetBatchSize(1) 时 5 条到期行仍被删尽 (LIMIT 循环走满; 若有人把
//     "删满一批就 break" 写成 "删不满也 break" 或漏掉 affected==batchSize 的续跑,
//     这里会剩行);
//  3. INV-3 不碰别的表: command_executions / command_attempts / command_outboxes /
//     automation_events / notifications / notification_deliveries / node_events
//     的行数逐表不变。
//
// 变红条件: DELETE 写了别的表 / 加了级联 / 分批循环提前终止。
func TestSecurityAuditCleaner_IdempotentBatchedAndTouchesNothingElse(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := auditNow()
	old := cmdDaysAgo(now, 900)

	for i, name := range []string{"a", "b", "c", "d", "e"} {
		seedSecurityAuditEvent(t, db, "batch-"+name, "success", "admin", old.Add(-time.Duration(i)*time.Hour))
	}
	seedSecurityAuditEvent(t, db, "keep", "success", "admin", cmdDaysAgo(now, 500))

	// 别的表的对照行 (全都在各自的保留期内, 与审计清理无任何关系)。
	seedCmdExecution(t, db, "x1", commandexec.StatusSucceeded, "{}", cmdDaysAgo(now, 500), cmdPtr(cmdDaysAgo(now, 500)))
	seedCmdAttempt(t, db, "x1", "x1", cmdDaysAgo(now, 500), cmdPtr(cmdDaysAgo(now, 500)))
	seedCmdOutbox(t, db, "x1-outbox", "x1", "PROCESSED", "{}", cmdDaysAgo(now, 500), cmdPtr(cmdDaysAgo(now, 500)))
	if err := db.Create(&models.AutomationEvent{RuleID: 1, TriggeredAt: cmdDaysAgo(now, 100), Result: models.AutomationResultExecuted, CreatedAt: cmdDaysAgo(now, 100)}).Error; err != nil {
		t.Fatalf("seed automation event: %v", err)
	}
	if err := db.Create(&models.Notification{Type: "warning", Message: "m", Title: "t", CreatedAt: cmdDaysAgo(now, 10)}).Error; err != nil {
		t.Fatalf("seed notification: %v", err)
	}
	if err := db.Create(&models.NotificationDelivery{NotificationID: 1, ChannelID: 1, State: "delivered", AttemptNo: 1, CreatedAt: cmdDaysAgo(now, 10)}).Error; err != nil {
		t.Fatalf("seed notification delivery: %v", err)
	}
	if err := db.Create(&models.NodeEvent{NodeID: "node-1", EventType: "online", NewStatus: "online", CreatedAt: cmdDaysAgo(now, 10)}).Error; err != nil {
		t.Fatalf("seed node event: %v", err)
	}

	otherTables := []struct {
		name  string
		model interface{}
	}{
		{"command_executions", &models.CommandExecution{}},
		{"command_attempts", &models.CommandAttempt{}},
		{"command_outboxes", &models.CommandOutbox{}},
		{"automation_events", &models.AutomationEvent{}},
		{"notifications", &models.Notification{}},
		{"notification_deliveries", &models.NotificationDelivery{}},
		{"node_events", &models.NodeEvent{}},
	}
	before := make(map[string]int64, len(otherTables))
	for _, tt := range otherTables {
		before[tt.name] = cmdCount(t, db, tt.model)
	}

	c := newSecurityAuditCleaner(db, now)
	c.SetBatchSize(1) // 每批 1 行: 强制把 LIMIT 分批循环走满

	first, err := c.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("first security audit cleanup: %v", err)
	}
	if first.Deleted != 5 {
		t.Errorf("first Deleted = %d, want 5 (SetBatchSize(1) 也必须删尽)", first.Deleted)
	}
	assertAuditStrings(t, "remaining audit events", auditEventNames(t, db), []string{"keep"})

	second, err := c.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("second security audit cleanup: %v", err)
	}
	if second.Deleted != 0 {
		t.Errorf("second Deleted = %d, want 0 (幂等)", second.Deleted)
	}

	for _, tt := range otherTables {
		if got := cmdCount(t, db, tt.model); got != before[tt.name] {
			t.Errorf("INV-3 违反: %s %d → %d (审计清理不得改变其它表)", tt.name, before[tt.name], got)
		}
	}
}

// TestSecurityAuditCleaner_TouchesNothingElseInRetentionRun 是"不碰别的表"的
// 第二面: 在【完整 RetentionTask.RunOnce】里, 别的表的行数只应由它们各自的清理器
// 决定, 而不是被审计清理牵连。这里让别的表全部"未到期"(审计到期), 于是除了审计行,
// 其余表必须一行不少。
//
// 变红条件: 审计清理的 DELETE 漏了表名限定 / 写了跨表语句。
func TestSecurityAuditCleaner_TouchesNothingElseInRetentionRun(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Now().UTC()
	expiredAudit := now.AddDate(0, 0, -800) // 只有审计行到期
	inWindow := now.AddDate(0, 0, -100)     // 其余表全部落在各自的窗内
	outboxAge := now.AddDate(0, 0, -10)     // outbox 窗只有 30 天, 单独取更近的时间

	seedSecurityAuditEvent(t, db, "expired-audit", "success", "admin", expiredAudit)
	seedCmdExecution(t, db, "r1", commandexec.StatusSucceeded, "{}", inWindow, cmdPtr(inWindow))
	seedCmdAttempt(t, db, "r1", "r1", inWindow, cmdPtr(inWindow))
	seedCmdOutbox(t, db, "r1-outbox", "r1", "PROCESSED", "{}", outboxAge, cmdPtr(outboxAge))
	if err := db.Create(&models.AutomationEvent{RuleID: 1, TriggeredAt: inWindow, Result: models.AutomationResultExecuted, CreatedAt: inWindow}).Error; err != nil {
		t.Fatalf("seed automation event: %v", err)
	}
	if err := db.Create(&models.Notification{Type: "warning", Message: "m", Title: "t", Read: false, CreatedAt: inWindow}).Error; err != nil {
		t.Fatalf("seed notification: %v", err)
	}

	otherTables := []struct {
		name  string
		model interface{}
	}{
		{"command_executions", &models.CommandExecution{}},
		{"command_attempts", &models.CommandAttempt{}},
		{"command_outboxes", &models.CommandOutbox{}},
		{"automation_events", &models.AutomationEvent{}},
		{"notifications", &models.Notification{}},
	}
	before := make(map[string]int64, len(otherTables))
	for _, tt := range otherTables {
		before[tt.name] = cmdCount(t, db, tt.model)
		if before[tt.name] == 0 {
			t.Fatalf("seed 失败: %s 为 0", tt.name)
		}
	}

	r := NewRetentionTask(db)
	r.SetBatchSleep(0)
	if _, err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("retention RunOnce: %v", err)
	}
	if got := auditCount(t, db); got != 0 {
		t.Errorf("到期审计行存活数 = %d, want 0 (第 7 个调用点没跑)", got)
	}
	// 其余表都还在自己的窗内 ⇒ 完整 RunOnce 之后必须一行不少。
	for _, tt := range otherTables {
		if got := cmdCount(t, db, tt.model); got != before[tt.name] {
			t.Errorf("%s %d → %d: 未到期却变了 (审计清理不得牵连别表)",
				tt.name, before[tt.name], got)
		}
	}
}

// ── INV-6 + 挂载点 + 空 db ──────────────────────────────────────────

// TestSecurityAuditCleaner_RetentionMatchesCommandExecution 把 INV-6 落成常量级断言。
//
// 设计 §2.3 的强前置依赖: 本表 request_id → command_executions.command_id (无 DB 外键),
// 本表窗口若【长于】命令执行行, 401~730 天之后的审计行就成了"指向一条已不存在的命令"
// 的孤证 —— 审计还在, 被审计对象没了。
//
// 变红条件: 有人把 DefaultSecurityAuditRetentionDays 调到 730 以上 (或把 execution
// 窗口调短)。两处常量必须一起改, 并重新走一遍裁决。
func TestSecurityAuditCleaner_RetentionMatchesCommandExecution(t *testing.T) {
	if DefaultSecurityAuditRetentionDays != DefaultCommandExecutionRetentionDays {
		t.Errorf("INV-6 违反: 审计窗口 %d != command_executions 窗口 %d (孤证审计行)",
			DefaultSecurityAuditRetentionDays, DefaultCommandExecutionRetentionDays)
	}
	if DefaultSecurityAuditRetentionDays <= 0 {
		t.Fatal("审计窗口必须为正数 (0/负数不得表示永久保留)")
	}
}

// TestSecurityAuditCleaner_RequiresDB 是最小安全网: 没有 db 时报错而不是 panic。
func TestSecurityAuditCleaner_RequiresDB(t *testing.T) {
	if _, err := NewSecurityAuditCleaner(nil).RunOnce(context.Background()); err == nil {
		t.Error("SecurityAuditCleaner(nil).RunOnce 未报错")
	}
}

// TestRetentionTask_RunOnceInvokesSecurityAuditCleaner 是第 7 个挂载点的接线测试:
// 有人把 r.audit.runOnceLogged(ctx) 从 RunOnce 里删掉, 本用例立刻变红
// (清理器写得再好, 没人调用等于没上线 —— 本表此前正是这个状态)。
//
// 这里用真实时钟走生产构造路径 NewRetentionTask, 不注入任何窗口覆盖。
func TestRetentionTask_RunOnceInvokesSecurityAuditCleaner(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Now().UTC()
	old := now.AddDate(0, 0, -800)
	recent := now.AddDate(0, 0, -10)

	seedSecurityAuditEvent(t, db, "w1-expired", "success", "admin", old)
	seedSecurityAuditEvent(t, db, "w2-in-window", "success", "admin", recent)

	r := NewRetentionTask(db)
	r.SetBatchSleep(0)
	if _, err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("retention RunOnce: %v", err)
	}
	assertAuditStrings(t, "audit events after RunOnce", auditEventNames(t, db), []string{"w2-in-window"})
}
