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

// ─ node_events 清理器的测试 (设计 §2.2 + INV-5) ────────────────────────
//
// 每条的注释都写明【什么样的改动会让它变红】—— 让它成为约束, 而不是当前取值的快照
// (与 command_domain_cleanup_test.go / security_audit_cleanup_test.go 同一手法)。
//
// 覆盖:
//   - 边界: 400 天整 (留在窗内) / 399 天 (留) / 401 天 (删);
//   - 只按 created_at 判 (不按 event_type / node_id / old_status / new_status);
//   - 【不分层】: offline 与 online 用同一窗口 (§2.2 的明确裁决, 防后人"顺手"加分档);
//   - INV-5: 保留窗口内 limit=200 仍能取满 200 条 (400 天下自然满足);
//   - fail-closed: SetRetention(0) / SetRetention(-1) 回落到 400 ——
//     【不得】解释成"永久保留", 也【不得】删光;
//   - 幂等 (第二轮 0 行)、分批 (SetBatchSize(1) 仍删尽);
//   - 不碰别的表 (INV-3: security_audit_events / automation_events /
//     command_executions / notifications / notification_deliveries 行数不变),
//     以及【绝不碰 nodes】(本表是"已不存在的节点"的唯一历史);
//   - 挂载点: 第 8 个清理器必须真的在每日 RetentionTask.RunOnce 里被调用。

// nodeEventNow 与命令域/审计用例共用同一注入时钟 (cmdNow), 但单独起名以免两处将来
// 漂移时相互掩盖; 两者当前必须相等。
func nodeEventNow() time.Time { return cmdNow() }

func seedNodeEvent(t *testing.T, db *gorm.DB, nodeID, eventType, oldStatus, newStatus string, createdAt time.Time) *models.NodeEvent {
	t.Helper()
	row := &models.NodeEvent{
		NodeID:    nodeID,
		EventType: eventType,
		OldStatus: oldStatus,
		NewStatus: newStatus,
		CreatedAt: createdAt,
	}
	if err := db.Create(row).Error; err != nil {
		t.Fatalf("seed node event %s/%s: %v", nodeID, eventType, err)
	}
	return row
}

// nodeEventNodeIDs 按 id 升序返回 node_id (播种顺序) —— 用例里每行用唯一 node_id 标识。
func nodeEventNodeIDs(t *testing.T, db *gorm.DB) []string {
	t.Helper()
	var ids []string
	if err := db.Model(&models.NodeEvent{}).Order("id").Pluck("node_id", &ids).Error; err != nil {
		t.Fatalf("pluck node event node_ids: %v", err)
	}
	return ids
}

func nodeEventCount(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&models.NodeEvent{}).Count(&n).Error; err != nil {
		t.Fatalf("count node events: %v", err)
	}
	return n
}

func nodeEventCountByType(t *testing.T, db *gorm.DB, eventType string) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&models.NodeEvent{}).Where("event_type = ?", eventType).Count(&n).Error; err != nil {
		t.Fatalf("count node events by type %s: %v", eventType, err)
	}
	return n
}

func newNodeEventCleaner(db *gorm.DB, now time.Time) *NodeEventCleaner {
	c := NewNodeEventCleaner(db)
	c.SetBatchSleep(0)
	c.now = func() time.Time { return now }
	return c
}

// ── 边界: 400 整 / 399 / 401 ─────────────────────────────────────────

// TestNodeEventCleaner_BoundaryDays 钉住时间窗的每一条边界。
//
// cutoff = now - 400×24h, 删除条件是 created_at < cutoff (严格小于):
//   - 401 天前  ⇒ 删;
//   - 400 天【整】⇒ 【留】(恰好等于 cutoff 不算"更早", 与 purge/retention/
//     command_attempt/security_audit 的既有语义逐字一致 —— 有人把 < 改成 <=
//     本用例立刻红);
//   - 399 天前  ⇒ 留;
//   - 刚刚写入  ⇒ 留。
//
// 变红条件: 窗口写成 730/365/90、比较符写成 <=、cutoff 用日历天 (AddDate) 算。
func TestNodeEventCleaner_BoundaryDays(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := nodeEventNow()

	seedNodeEvent(t, db, "n-old-401", "offline", "online", "offline", cmdDaysAgo(now, 401))
	seedNodeEvent(t, db, "n-exact-400", "offline", "online", "offline", cmdDaysAgo(now, 400))
	seedNodeEvent(t, db, "n-recent-399", "offline", "online", "offline", cmdDaysAgo(now, 399))
	seedNodeEvent(t, db, "n-fresh", "offline", "online", "offline", now.Add(-time.Hour))

	c := newNodeEventCleaner(db, now)
	report, err := c.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("node event cleanup: %v", err)
	}
	if report.Deleted != 1 {
		t.Errorf("Deleted = %d, want 1 (只有 401 天前那条)", report.Deleted)
	}
	if report.RetentionDays != 400 {
		t.Errorf("RetentionDays = %d, want 400", report.RetentionDays)
	}
	assertCmdStrings(t, "remaining node events", nodeEventNodeIDs(t, db),
		[]string{"n-exact-400", "n-recent-399", "n-fresh"})

	// 默认窗口必须是设计裁决的 400 (不是系统级保留期 90, 也不是命令域/审计的 730)。
	if got := NewNodeEventCleaner(db).retentionWindowDays(); got != 400 {
		t.Errorf("默认窗口 = %d, want 400 (设计 §2.2 裁决)", got)
	}
	if DefaultNodeEventRetentionDays != 400 {
		t.Errorf("DefaultNodeEventRetentionDays = %d, want 400", DefaultNodeEventRetentionDays)
	}
}

// ── 只按 created_at 删 ──────────────────────────────────────────────

// TestNodeEventCleaner_DeletesByCreatedAtOnly 断言删除判定【只看 created_at】。
//
// ⚠️ 与审计表不同, 本表【没有第二个时间列】(models.NodeEvent 只有 CreatedAt,
// 没有 updated_at / processed_at), 因此"created_at 旧但别的时间列新"的对照
// 只能由【非时间列】承担: 构造一组 created_at 旧、而 event_type / node_id /
// old_status / new_status 全部"看起来像刚刚发生的事"的行 (online 转换、
// 字典序最大的 node_id、状态字符串为 online)。若有人把判定改成按 event_type
// 白名单 (命令域那套范式照搬过来), 或对 online 之类的取值单独开恩, 本用例立刻红。
//
// 反向对照: created_at 新、其余列"看起来像很久以前" (old_status/new_status 为空、
// event_type=offline) 的行必须留下 —— 清理器不得从任何别的列推断时间。
//
// 变红条件: 删除语句里出现 created_at 以外的判定列。
func TestNodeEventCleaner_DeletesByCreatedAtOnly(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := nodeEventNow()
	old, recent := cmdDaysAgo(now, 800), cmdDaysAgo(now, 10)

	// 删 (created_at 旧, 与取值无关):
	seedNodeEvent(t, db, "zzz-old-online", "online", "offline", "online", old)
	seedNodeEvent(t, db, "zzz-old-revived", "online", "offline", "online", old)
	seedNodeEvent(t, db, "mmm-old-weird", "future_event_type", "", "whatever", old)
	seedNodeEvent(t, db, "aaa-old-offline", "offline", "online", "offline", old)

	// 留 (created_at 新, 哪怕其它列"看起来像很久以前的事"):
	seedNodeEvent(t, db, "aaa-recent-offline", "offline", "", "", recent)
	seedNodeEvent(t, db, "zzz-recent-legacy", "offline", "", "", recent)

	report, err := newNodeEventCleaner(db, now).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("node event cleanup: %v", err)
	}
	if report.Deleted != 4 {
		t.Errorf("Deleted = %d, want 4 (全部按 created_at 到期)", report.Deleted)
	}
	assertCmdStrings(t, "remaining node events", nodeEventNodeIDs(t, db),
		[]string{"aaa-recent-offline", "zzz-recent-legacy"})
}

// ── 不分层 (§2.2 的明确裁决) ─────────────────────────────────────────

// TestNodeEventCleaner_NotTieredByEventType 是本文件最重要的一条: 把 §2.2 的
//
//	"裁决: 统一保留 400 天, 按 created_at 删, 【不设分层档位】"
//	"99.6% 是 offline, 而 offline 恰恰是运维时间线的主内容, 按 event_type 分层
//	 等于删掉时间线本身"
//
// 写成断言: offline 与 online (以及任何其它 event_type) 必须用【同一个窗口】。
//
// 构造: 两类事件各 3 条 —— 401 天前 (两类都必须删) / 400 天整 (两类都必须留) /
// 1 天前 (两类都必须留)。若有人给 offline 单独开一个更短的窗口 (例如 90 天),
// 或给 online 单独开一个更长的窗口, 两条断言中必有一条变红。
//
// 第二道保险 (常量级): 本清理器【没有】eventTypeTiers / 白名单这类结构 ——
// 由下面这条断言钉住: 生效窗口与 event_type 无关, 只由 SetRetention/常量决定。
// 把 offline 的 400 天整行删掉、或把 online 的 401 天前行留下, 都会立刻红。
func TestNodeEventCleaner_NotTieredByEventType(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := nodeEventNow()

	for _, et := range []string{"offline", "online"} {
		seedNodeEvent(t, db, et+"-expired-401", et, "", "", cmdDaysAgo(now, 401))
		seedNodeEvent(t, db, et+"-exact-400", et, "", "", cmdDaysAgo(now, 400))
		seedNodeEvent(t, db, et+"-recent-1", et, "", "", cmdDaysAgo(now, 1))
	}

	report, err := newNodeEventCleaner(db, now).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("node event cleanup: %v", err)
	}
	if report.Deleted != 2 {
		t.Fatalf("Deleted = %d, want 2 (每个 event_type 各删 1 条 401 天前的行; "+
			"删多/删少都说明两类用了不同窗口)", report.Deleted)
	}
	// 同一窗口的正面断言: 两类各剩 2 条, 且都是 400 整 + 1 天前。
	for _, et := range []string{"offline", "online"} {
		if got := nodeEventCountByType(t, db, et); got != 2 {
			t.Errorf("event_type=%s 存活 %d 条, want 2 (offline/online 必须同一窗口 —— "+
				"§2.2 裁决不分层)", et, got)
		}
	}
	assertCmdStrings(t, "remaining node events", nodeEventNodeIDs(t, db),
		[]string{"offline-exact-400", "offline-recent-1", "online-exact-400", "online-recent-1"})
}

// ── INV-5: 保留期必须能承载展示上限 (200) ────────────────────────────

// TestNodeEventCleaner_INV5RetentionCoversDisplayLimit 把设计 §7 的 INV-5 写成断言。
//
// 两个消费端 (api/handler_node.go:104-107 与 :154-157) 的展示上限是
// limit 默认 50 / 硬上限 200, 且都按 created_at DESC 排序。
// INV-5: "在保留窗口内, GET /nodes/status-history?limit=200 必须仍能取满 200 条"。
//
// ⚠️ 对设计原文测试的一处澄清: 原文写"断言按 created_at DESC LIMIT 200 仍返回
// 200 条且最老一条的年龄 ≥ 保留期 × 0.9"。本用例按【存活行中最老一条】的年龄
// 解释该断言 —— 它的含义是"保留期没有被提前截断, 窗口内最老的历史仍在"
// (若把年龄理解成"返回的 200 条里最老那条", 在全窗口均匀播种下会算出一个
// 与保留期无关的值, 那条断言就失去了约束力)。
//
// 构造: 300 条事件均匀撒在保留窗口的第 1 天到第 399 天 (全部在窗内, 无一条到期)。
// 跑清理后:
//  1. 一行都不该被删 (全部在窗内 ⇒ LIMIT 200 才有 200 条可取);
//  2. LIMIT 200 仍返回 200 条;
//  3. 存活行中最老一条的年龄 ≥ 400 × 0.9 = 360 天 ⇒ 保留窗口完整。
//
// 变红条件: 有人把保留期从 400 降到 90 (窗口内只剩约 67 条 ⇒ 断言 2 红,
// 这正是 §2.2 末尾点名的隐患场景 —— 1000 节点仿真下页面无感, 历史已丢)。
func TestNodeEventCleaner_INV5RetentionCoversDisplayLimit(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := nodeEventNow()

	const total = 300
	for i := 0; i < total; i++ {
		// day 1 → day 399 均匀分布 (全部在 400 天窗内)。
		day := 1 + i*(399-1)/(total-1)
		seedNodeEvent(t, db, "inv5-"+time.Duration(day).String(), "offline", "", "",
			cmdDaysAgo(now, day))
	}

	report, err := newNodeEventCleaner(db, now).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("node event cleanup: %v", err)
	}
	if report.Deleted != 0 {
		t.Fatalf("Deleted = %d, want 0 (300 条全在 400 天窗内, 一条都不该删)", report.Deleted)
	}
	if got := nodeEventCount(t, db); got != total {
		t.Fatalf("存活 = %d, want %d", got, total)
	}

	var page []models.NodeEvent
	if err := db.Model(&models.NodeEvent{}).Order("created_at DESC").Limit(200).Find(&page).Error; err != nil {
		t.Fatalf("query status history limit 200: %v", err)
	}
	if len(page) != 200 {
		t.Fatalf("limit=200 只返回 %d 条, want 200 (INV-5 违反: 保留期承载不了展示上限)", len(page))
	}
	// 存活行中【全局】最老一条 (不是返回页里最老那条 —— 300 条均匀撒在 399 天里,
	// 前 200 条只覆盖到第 265 天, 用它算年龄会得到一个与保留期无关的数)。
	var oldestSurviving models.NodeEvent
	if err := db.Model(&models.NodeEvent{}).Order("created_at ASC").Limit(1).Find(&oldestSurviving).Error; err != nil {
		t.Fatalf("query oldest surviving node event: %v", err)
	}
	ageDays := now.Sub(oldestSurviving.CreatedAt).Hours() / 24
	if ageDays < float64(DefaultNodeEventRetentionDays)*0.9 {
		t.Errorf("存活行中最老一条的年龄 = %.1f 天, want >= %.1f (INV-5: 保留窗口被提前截断)",
			ageDays, float64(DefaultNodeEventRetentionDays)*0.9)
	}
}

// ── fail-closed: 0 / 负数【不是】"永久保留" ──────────────────────────

// TestNodeEventCleaner_NonPositiveRetentionFallsBackTo400 把"非法值 fail-closed
// 回落到裁决值"写成测试 (与 security_audit_cleanup_test.go 同型):
//
//	"不允许 0/负数表示「永久」—— 那会重新打开无界增长"
//
// 三条断言, 任一条被破坏即红:
//  1. SetRetention(0) 与 SetRetention(-1) 的【生效窗口】必须是 400;
//  2. 到期行仍然被删 (不能被解释成永久保留 ⇒ 表重新无界增长);
//  3. 400 天内的行必须留下 (负值若被当成 cutoff 会让 cutoff 落到未来, 把窗内的
//     行也删掉 —— 那是比"不删"更危险的另一种失败)。
//
// 变红条件: retentionWindowDays 写成 "if days == 0 { 永久 }"、写成 "return c.windowDays"
// (负值直接生效)、或把非正值静默当成 1 天 (窗内行被删光)。
func TestNodeEventCleaner_NonPositiveRetentionFallsBackTo400(t *testing.T) {
	for _, tc := range []struct {
		name string
		days int
	}{
		{"zero-means-fallback-not-forever", 0},
		{"negative-means-fallback-not-future-cutoff", -1},
		{"large-negative", -400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := testutil.OpenTestDB(t)
			now := nodeEventNow()
			seedNodeEvent(t, db, "expired-800", "offline", "", "", cmdDaysAgo(now, 800))
			seedNodeEvent(t, db, "in-window-300", "offline", "", "", cmdDaysAgo(now, 300))

			c := newNodeEventCleaner(db, now)
			c.SetRetention(tc.days)

			if got := c.retentionWindowDays(); got != 400 {
				t.Fatalf("SetRetention(%d) 后生效窗口 = %d, want 400 (fail-closed 回落; "+
					"0/负数【不得】表示永久保留)", tc.days, got)
			}
			report, err := c.RunOnce(context.Background())
			if err != nil {
				t.Fatalf("node event cleanup: %v", err)
			}
			if report.RetentionDays != 400 {
				t.Errorf("报告窗口 = %d, want 400", report.RetentionDays)
			}
			if report.Deleted != 1 {
				t.Errorf("Deleted = %d, want 1 (0/负数绝不能被解释成永不删除)", report.Deleted)
			}
			// 窗内行不得被删 —— 负值若直接参与 cutoff 计算就会走到这里。
			assertCmdStrings(t, "remaining node events", nodeEventNodeIDs(t, db), []string{"in-window-300"})
		})
	}
}

// TestNodeEventCleaner_PositiveRetentionIsConfigurable 确认保留期【确实可配置】
// (决策原文: "保留期 ... 作为可配置常量"), 而不是把 400 写死在删除语句里。
//
// 变红条件: retentionWindowDays 忽略 windowDays 覆盖 (例如直接 return 400)。
func TestNodeEventCleaner_PositiveRetentionIsConfigurable(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := nodeEventNow()
	seedNodeEvent(t, db, "age-100", "offline", "", "", cmdDaysAgo(now, 100))
	seedNodeEvent(t, db, "age-10", "offline", "", "", cmdDaysAgo(now, 10))

	c := newNodeEventCleaner(db, now)
	c.SetRetention(30)
	if got := c.retentionWindowDays(); got != 30 {
		t.Fatalf("SetRetention(30) 后生效窗口 = %d, want 30", got)
	}
	report, err := c.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("node event cleanup: %v", err)
	}
	if report.Deleted != 1 {
		t.Errorf("Deleted = %d, want 1 (窗口 30 天)", report.Deleted)
	}
	assertCmdStrings(t, "remaining node events", nodeEventNodeIDs(t, db), []string{"age-10"})
}

// ── 幂等 + 分批 + 不碰别的表 ─────────────────────────────────────────

// TestNodeEventCleaner_IdempotentBatchedAndTouchesNothingElse 一次钉住三件事。
//
//  1. 幂等: 第二轮 Deleted = 0;
//  2. 分批: SetBatchSize(1) 时 5 条到期行仍被删尽 (LIMIT 循环走满; 若有人把
//     "删满一批就 break" 写成 "删不满也 break" 或漏掉 affected==batchSize 的续跑,
//     这里会剩行);
//  3. INV-3 不碰别的表: security_audit_events / automation_events /
//     command_executions / command_attempts / command_outboxes / notifications /
//     notification_deliveries 的行数逐表不变。
//
// 变红条件: DELETE 写了别的表 / 加了级联 / 分批循环提前终止。
func TestNodeEventCleaner_IdempotentBatchedAndTouchesNothingElse(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := nodeEventNow()
	old := cmdDaysAgo(now, 900)

	for i, name := range []string{"a", "b", "c", "d", "e"} {
		seedNodeEvent(t, db, "batch-"+name, "offline", "", "", old.Add(-time.Duration(i)*time.Hour))
	}
	seedNodeEvent(t, db, "keep", "offline", "", "", cmdDaysAgo(now, 300)) // 在 400 天窗内

	// 别的表的对照行 (全都在各自的保留期内, 与 node_events 清理无任何关系)。
	seedSecurityAuditEvent(t, db, "unrelated.audit", "success", "admin", cmdDaysAgo(now, 500))
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

	otherTables := []struct {
		name  string
		model interface{}
	}{
		{"security_audit_events", &models.SecurityAuditEvent{}},
		{"command_executions", &models.CommandExecution{}},
		{"command_attempts", &models.CommandAttempt{}},
		{"command_outboxes", &models.CommandOutbox{}},
		{"automation_events", &models.AutomationEvent{}},
		{"notifications", &models.Notification{}},
		{"notification_deliveries", &models.NotificationDelivery{}},
		{"nodes", &models.Node{}},
	}
	before := make(map[string]int64, len(otherTables))
	for _, tt := range otherTables {
		before[tt.name] = cmdCount(t, db, tt.model)
	}

	c := newNodeEventCleaner(db, now)
	c.SetBatchSize(1) // 每批 1 行: 强制把 LIMIT 分批循环走满

	first, err := c.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("first node event cleanup: %v", err)
	}
	if first.Deleted != 5 {
		t.Errorf("first Deleted = %d, want 5 (SetBatchSize(1) 也必须删尽)", first.Deleted)
	}
	assertCmdStrings(t, "remaining node events", nodeEventNodeIDs(t, db), []string{"keep"})

	second, err := c.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("second node event cleanup: %v", err)
	}
	if second.Deleted != 0 {
		t.Errorf("second Deleted = %d, want 0 (幂等)", second.Deleted)
	}

	for _, tt := range otherTables {
		if got := cmdCount(t, db, tt.model); got != before[tt.name] {
			t.Errorf("INV-3 违反: %s %d → %d (node_events 清理不得改变其它表)", tt.name, before[tt.name], got)
		}
	}
}

// TestNodeEventCleaner_DoesNotTouchNodes 钉住 INV-3 里最要紧的一面 (§2.2 实测:
// 本表 6073 条 offline 对应 1002 个 node_id, 其中只有 2 个还存在于 nodes —
// 本表是"已不存在的节点"的【唯一历史】)。
//
// 构造: 一条节点的历史事件 + 一条【nodes 里根本不存在的节点】的历史事件, 两者都
// 到期。跑清理后: 两条事件都删, 但 nodes 行数一行不变 (删历史 ≠ 删节点行)。
//
// 变红条件: 清理语句里出现 nodes / 加了外键级联 / 有人"顺手"清了孤儿的 nodes 行。
func TestNodeEventCleaner_DoesNotTouchNodes(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := nodeEventNow()

	if err := db.Create(&models.Node{NodeID: "NODE-ALIVE", Name: "alive", Status: "offline"}).Error; err != nil {
		t.Fatalf("seed node: %v", err)
	}
	seedNodeEvent(t, db, "NODE-ALIVE", "offline", "online", "offline", cmdDaysAgo(now, 900))
	seedNodeEvent(t, db, "NODE-GONE", "offline", "online", "offline", cmdDaysAgo(now, 900))

	nodesBefore := cmdCount(t, db, &models.Node{})
	report, err := newNodeEventCleaner(db, now).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("node event cleanup: %v", err)
	}
	if report.Deleted != 2 {
		t.Errorf("Deleted = %d, want 2", report.Deleted)
	}
	if got := nodeEventCount(t, db); got != 0 {
		t.Errorf("到期 node_events 存活 = %d, want 0", got)
	}
	if got := cmdCount(t, db, &models.Node{}); got != nodesBefore {
		t.Errorf("INV-3 违反: nodes %d → %d (清理事件行绝不得改变 nodes)", nodesBefore, got)
	}
}

// TestNodeEventCleaner_TouchesNothingElseInRetentionRun 是"不碰别的表"的第二面:
// 在【完整 RetentionTask.RunOnce】里, 别的表的行数只应由它们各自的清理器决定,
// 而不是被 node_events 清理牵连。这里让别的表全部"未到期"(node_events 到期),
// 于是除了事件行, 其余表必须一行不少。
//
// 变红条件: 事件清理的 DELETE 漏了表名限定 / 写了跨表语句。
func TestNodeEventCleaner_TouchesNothingElseInRetentionRun(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Now().UTC()
	expiredEvent := now.AddDate(0, 0, -900) // 只有 node_events 到期
	inWindow := now.AddDate(0, 0, -100)     // 其余表全部落在各自的窗内
	outboxAge := now.AddDate(0, 0, -10)     // outbox 窗只有 30 天, 单独取更近的时间

	seedNodeEvent(t, db, "expired-event", "offline", "", "", expiredEvent)
	seedSecurityAuditEvent(t, db, "in-window.audit", "success", "admin", inWindow)
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
		{"security_audit_events", &models.SecurityAuditEvent{}},
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
	if got := nodeEventCount(t, db); got != 0 {
		t.Errorf("到期 node_events 存活数 = %d, want 0 (第 8 个调用点没跑)", got)
	}
	// 其余表都还在自己的窗内 ⇒ 完整 RunOnce 之后必须一行不少。
	for _, tt := range otherTables {
		if got := cmdCount(t, db, tt.model); got != before[tt.name] {
			t.Errorf("%s %d → %d: 未到期却变了 (node_events 清理不得牵连别表)",
				tt.name, before[tt.name], got)
		}
	}
}

// ── 安全网 + 挂载点 ─────────────────────────────────────────────────

// TestNodeEventCleaner_RequiresDB 是最小安全网: 没有 db 时报错而不是 panic。
func TestNodeEventCleaner_RequiresDB(t *testing.T) {
	if _, err := NewNodeEventCleaner(nil).RunOnce(context.Background()); err == nil {
		t.Error("NodeEventCleaner(nil).RunOnce 未报错")
	}
}

// TestRetentionTask_RunOnceInvokesNodeEventCleaner 是第 8 个挂载点的接线测试:
// 有人把 r.nodeEvents.runOnceLogged(ctx) 从 RunOnce 里删掉, 本用例立刻变红
// (清理器写得再好, 没人调用等于没上线 —— 本表此前正是这个状态)。
//
// 这里用真实时钟走生产构造路径 NewRetentionTask, 不注入任何窗口覆盖。
func TestRetentionTask_RunOnceInvokesNodeEventCleaner(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Now().UTC()
	old := now.AddDate(0, 0, -800)
	recent := now.AddDate(0, 0, -10)

	seedNodeEvent(t, db, "w1-expired", "offline", "online", "offline", old)
	seedNodeEvent(t, db, "w2-in-window", "offline", "online", "offline", recent)

	r := NewRetentionTask(db)
	r.SetBatchSleep(0)
	if _, err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("retention RunOnce: %v", err)
	}
	assertCmdStrings(t, "node events after RunOnce", nodeEventNodeIDs(t, db), []string{"w2-in-window"})
}
