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

// ─ 命令域三表清理的测试 ───────────────────────────────────────────────
//
// 本文件把设计文档 §6 的不变式逐条落成【可执行的约束】, 每条的注释都写明
// "什么样的改动会让它变红" —— 让它成为约束, 而不是当前取值的快照。
//
//	INV-2  监控计数不被清理改写 (基线在同一事务内累加; unresolved_unknown 不吃基线)
//	INV-3  无副作用删除 (删一张表不得改变另外两张的行数)
//	INV-4  在途行永不删 (QUEUED/DISPATCHED/DEVICE_ACCEPTED/VERIFYING、PENDING/LEASED)
//	INV-6  attempt 保留期 >= execution 保留期 (证据不得先于被审计对象死亡)
//	INV-9  outbox 冗余性用【容差】断言, 不得写等号
//	硬约束1 未处置的 UNKNOWN 永不随时间删 (处置入口必须先加载该行)
//
// PG 侧的同一批断言见 command_domain_cleanup_pg_test.go (子查询 LIMIT / jsonb /
// 时间精度在两种方言下最可能出现差异)。

// cmdNow 是所有用例共用的注入时钟 (清理器的 now 可注入, 与既有清理器一致)。
func cmdNow() time.Time { return time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC) }

// cmdDaysAgo 精确按 24h 回退: 清理器算 cutoff 用的就是 24h×days, 不用日历天
// (用 AddDate 在夏令时/月末会与清理器的算术差几小时, 让边界用例变成偶发红)。
func cmdDaysAgo(now time.Time, days int) time.Time {
	return now.Add(-time.Duration(days) * 24 * time.Hour)
}

func cmdPtr(t time.Time) *time.Time { return &t }

// ── seed 辅助 ────────────────────────────────────────────────────────

func seedCmdExecution(t *testing.T, db *gorm.DB, id, status, params string, createdAt time.Time, completedAt *time.Time) {
	t.Helper()
	exec := models.CommandExecution{
		CommandID: id, EdgeDeviceID: 1, NodeID: "node-1", DeviceType: "valve",
		DeviceConfigID: 1, ChannelID: 1, ManifestID: "manifest-1",
		ActionID: "valve.set_angle", ActionVersion: 1, CommandEngineRevision: 1,
		ActorUserID:      1,
		IdempotencyScope: "scope-" + id, IdempotencyKey: "key-" + id,
		RequestHash: "hash-" + id,
		ParamsJSON:  params,
		Status:      status,
		DeadlineAt:  createdAt.Add(2 * time.Minute),
		// jsonb 列必须给合法 JSON: PG 上空串会直接报 invalid input syntax。
		VerifiedResultJSON: "[]",
		CreatedAt:          createdAt,
		CompletedAt:        completedAt,
	}
	if err := db.Create(&exec).Error; err != nil {
		t.Fatalf("seed execution %s: %v", id, err)
	}
}

func seedCmdAttempt(t *testing.T, db *gorm.DB, id, commandID string, createdAt time.Time, publishedAt *time.Time) {
	t.Helper()
	attempt := models.CommandAttempt{
		CommandID: commandID, AttemptNo: 1, Status: commandexec.StatusDispatched,
		EnvelopeID: "env-" + id, WireDigest: "digest-" + id, BootID: "boot-" + id,
		FencingToken: 1, CreatedAt: createdAt, PublishedAt: publishedAt,
	}
	if err := db.Create(&attempt).Error; err != nil {
		t.Fatalf("seed attempt %s: %v", id, err)
	}
}

func seedCmdOutbox(t *testing.T, db *gorm.DB, id, commandID, state, payload string, createdAt time.Time, processedAt *time.Time) {
	t.Helper()
	outbox := models.CommandOutbox{
		CommandID: commandID, EventType: "command.dispatch", PayloadJSON: payload,
		State: state, FencingToken: 1, CreatedAt: createdAt, ProcessedAt: processedAt,
	}
	if err := db.Create(&outbox).Error; err != nil {
		t.Fatalf("seed outbox %s: %v", id, err)
	}
}

func seedCmdResolution(t *testing.T, db *gorm.DB, commandID string, resolvedAt time.Time) {
	t.Helper()
	res := models.CommandManualResolution{
		CommandID: commandID, Outcome: "confirmed_not_executed",
		Reason: "现场核实阀门未动作", ResolvedBy: 7, ResolvedAt: resolvedAt,
	}
	if err := db.Create(&res).Error; err != nil {
		t.Fatalf("seed manual resolution %s: %v", commandID, err)
	}
}

// ── 读辅助 ───────────────────────────────────────────────────────────

func cmdExecutionIDs(t *testing.T, db *gorm.DB) []string {
	t.Helper()
	var ids []string
	if err := db.Model(&models.CommandExecution{}).Order("command_id").Pluck("command_id", &ids).Error; err != nil {
		t.Fatalf("pluck execution ids: %v", err)
	}
	return ids
}

func cmdAttemptIDs(t *testing.T, db *gorm.DB) []string {
	t.Helper()
	var ids []string
	if err := db.Model(&models.CommandAttempt{}).Order("command_id").Pluck("command_id", &ids).Error; err != nil {
		t.Fatalf("pluck attempt ids: %v", err)
	}
	return ids
}

func cmdOutboxIDs(t *testing.T, db *gorm.DB) []string {
	t.Helper()
	var ids []string
	if err := db.Model(&models.CommandOutbox{}).Order("command_id").Pluck("command_id", &ids).Error; err != nil {
		t.Fatalf("pluck outbox ids: %v", err)
	}
	return ids
}

func cmdCount(t *testing.T, db *gorm.DB, model interface{}) int64 {
	t.Helper()
	var n int64
	if err := db.Model(model).Count(&n).Error; err != nil {
		t.Fatalf("count %T: %v", model, err)
	}
	return n
}

func cmdBaseline(t *testing.T, db *gorm.DB) models.CommandMetricsBaseline {
	t.Helper()
	return commandexec.GetMetricsBaseline(db)
}

func assertCmdStrings(t *testing.T, label string, got, want []string) {
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

func newCmdExecutionCleaner(db *gorm.DB, now time.Time) *CommandExecutionCleaner {
	c := NewCommandExecutionCleaner(db)
	c.SetBatchSleep(0)
	c.now = func() time.Time { return now }
	return c
}

func newCmdAttemptCleaner(db *gorm.DB, now time.Time) *CommandAttemptCleaner {
	c := NewCommandAttemptCleaner(db)
	c.SetBatchSleep(0)
	c.now = func() time.Time { return now }
	return c
}

func newCmdOutboxCleaner(db *gorm.DB, now time.Time) *CommandOutboxCleaner {
	c := NewCommandOutboxCleaner(db)
	c.SetBatchSleep(0)
	c.now = func() time.Time { return now }
	return c
}

// ── 白名单 fail-closed + INV-4: 只有"到期 且 终态"的行被删 ──────────────

// TestCommandExecutionCleaner_DeletesOnlyExpiredTerminalRows 钉住删除范围的每一条边界。
//
// 变红条件 (每一档都对应一个经典写法错误):
//   - 用 created_at 代替 COALESCE(completed_at, created_at) ⇒ "创建很早但最近才完成"
//     的 e6 会被误删;
//   - 只用 completed_at ⇒ 终态但 completed_at 为空的 e2 永远删不掉;
//   - 忘了状态白名单 (只按时间删) ⇒ 在途的 e8~e11 与悬案 e7 被删;
//   - 把 UNKNOWN 加进 PrunableStatuses ⇒ 未处置悬案 e7 被删;
//   - 用黑名单 ("status <> 'QUEUED'") ⇒ 未知取值 e12 被删。
func TestCommandExecutionCleaner_DeletesOnlyExpiredTerminalRows(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := cmdNow()
	old, recent := cmdDaysAgo(now, 1000), cmdDaysAgo(now, 700)

	// 删: 三个普通终态 + 一个"终态但 completed_at 为空"(COALESCE 兜底) + 一个已处置 UNKNOWN。
	seedCmdExecution(t, db, "e1-succeeded-old", commandexec.StatusSucceeded, "{}", old, cmdPtr(cmdDaysAgo(now, 731)))
	seedCmdExecution(t, db, "e2-failed-nil-completed", commandexec.StatusFailed, "{}", cmdDaysAgo(now, 731), nil)
	seedCmdExecution(t, db, "e3-cancelled-old", commandexec.StatusCancelled, "{}", old, cmdPtr(old))
	seedCmdExecution(t, db, "e4-unknown-resolved", commandexec.StatusUnknown, "{}", old, cmdPtr(old))
	seedCmdResolution(t, db, "e4-unknown-resolved", old)

	// 留: 窗内的普通终态。
	seedCmdExecution(t, db, "e5-succeeded-recent", commandexec.StatusSucceeded, "{}", recent, cmdPtr(recent))
	// 留 (关键): 创建在窗外但【完成】在窗内 —— created_at 绝不能单独作 cutoff。
	seedCmdExecution(t, db, "e6-completed-recent", commandexec.StatusFailed, "{}", old, cmdPtr(recent))
	// 留 (硬约束 1): 未处置的 UNKNOWN 悬案, 无论多老都不随时间删。
	seedCmdExecution(t, db, "e7-unknown-unresolved", commandexec.StatusUnknown, "{}", old, cmdPtr(old))
	// 留 (INV-4): 四种非终态在途行, 且老到 2000 天。
	inflight := cmdDaysAgo(now, 2000)
	seedCmdExecution(t, db, "e8-queued", commandexec.StatusQueued, "{}", inflight, nil)
	seedCmdExecution(t, db, "e9-dispatched", commandexec.StatusDispatched, "{}", inflight, nil)
	seedCmdExecution(t, db, "e10-device-accepted", commandexec.StatusDeviceAccepted, "{}", inflight, nil)
	seedCmdExecution(t, db, "e11-verifying", commandexec.StatusVerifying, "{}", inflight, nil)
	// 留 (白名单 fail-closed): 将来新增的终态取值, 清理器不得把它当垃圾删。
	seedCmdExecution(t, db, "e12-unknown-status", "SUPERSEDED", "{}", inflight, cmdPtr(inflight))

	c := newCmdExecutionCleaner(db, now)
	c.SetBatchSize(2) // 强制多批: 每批 2 行, 走 PrunableExecutionsQuery + LIMIT 的循环
	report, err := c.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("execution cleanup: %v", err)
	}
	if report.DeletedTotal != 4 {
		t.Errorf("DeletedTotal = %d, want 4", report.DeletedTotal)
	}
	assertCmdStrings(t, "remaining executions", cmdExecutionIDs(t, db), []string{
		"e10-device-accepted", "e11-verifying", "e12-unknown-status",
		"e5-succeeded-recent", "e6-completed-recent", "e7-unknown-unresolved",
		"e8-queued", "e9-dispatched",
	})
	// 幂等: 再跑一轮不删任何行 (悬案仍在, 但它本来就不在候选集里)。
	report, err = c.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("second execution cleanup: %v", err)
	}
	if report.DeletedTotal != 0 {
		t.Errorf("second run DeletedTotal = %d, want 0", report.DeletedTotal)
	}
}

// TestCommandExecutionCleaner_KeepsUnresolvedUnknownResolvable 是硬约束 1 的【能力断言】,
// 不只是"行还在": commandexec/service.go 的处置入口必须先 First(&execution, command_id)
// 加载该行, 行没了悬案永远结不了案。这里把该入口的加载语句原样跑一遍。
//
// 变红条件: 有人把 UNKNOWN 放进删除白名单, 或去掉 EXISTS(command_manual_resolutions)
// 分支 ⇒ 悬案行消失 ⇒ 加载失败 ⇒ 本测试红。
func TestCommandExecutionCleaner_KeepsUnresolvedUnknownResolvable(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := cmdNow()
	seedCmdExecution(t, db, "unresolved-unknown", commandexec.StatusUnknown, "{}",
		cmdDaysAgo(now, 5000), cmdPtr(cmdDaysAgo(now, 5000)))

	c := newCmdExecutionCleaner(db, now)
	c.SetRetention(1) // 窗口压到 1 天: 时间上早就该删, 但它是未处置悬案
	for i := 0; i < 3; i++ {
		if _, err := c.RunOnce(context.Background()); err != nil {
			t.Fatalf("cleanup round %d: %v", i, err)
		}
	}
	var loaded models.CommandExecution
	if err := db.First(&loaded, "command_id = ?", "unresolved-unknown").Error; err != nil {
		t.Fatalf("人工处置入口无法加载悬案行 (清理器把未处置 UNKNOWN 删了): %v", err)
	}
	if loaded.Status != commandexec.StatusUnknown {
		t.Fatalf("悬案行 status = %q, want UNKNOWN", loaded.Status)
	}
	// 未处置的 UNKNOWN 也【不得】进基线: 它是集合成员数, 不是累计量。
	if b := cmdBaseline(t, db); b.OperationsTotal != 0 || b.Unknown != 0 {
		t.Errorf("未处置 UNKNOWN 被算进了基线 %+v —— 会让 unresolved_unknown 永久虚高", b)
	}
}

// TestCommandExecutionCleaner_BaselineKeepsPanelCountsInvariant 是 INV-2 的核心:
// 面板口径 = 存活行 COUNT(*) + 基线, 清理前后【不得减少】。
//
// 变红条件:
//   - 累加基线的代码被删 / 被挪出删除事务 ⇒ after < before;
//   - 累加写成覆盖而非累加 ⇒ 第二轮之后数字回退 (本测试跑了两轮);
//   - 把 unresolved_unknown 也基线化 ⇒ panelUnresolvedUnknown 虚高 (≠1);
//   - 把未处置 UNKNOWN 删掉 ⇒ panelUnresolvedUnknown 掉到 0。
func TestCommandExecutionCleaner_BaselineKeepsPanelCountsInvariant(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := cmdNow()
	old := cmdDaysAgo(now, 800)

	seedCmdExecution(t, db, "b1-failed-old", commandexec.StatusFailed, "{}", old, cmdPtr(old))
	seedCmdExecution(t, db, "b2-succeeded-old", commandexec.StatusSucceeded, "{}", old, cmdPtr(old))
	seedCmdExecution(t, db, "b3-cancelled-old", commandexec.StatusCancelled, "{}", old, cmdPtr(old))
	seedCmdExecution(t, db, "b4-unknown-resolved-old", commandexec.StatusUnknown, "{}", old, cmdPtr(old))
	seedCmdResolution(t, db, "b4-unknown-resolved-old", old)
	seedCmdExecution(t, db, "b5-unknown-unresolved-old", commandexec.StatusUnknown, "{}", old, cmdPtr(old))
	seedCmdExecution(t, db, "b6-failed-fresh", commandexec.StatusFailed, "{}", now.Add(-time.Hour), cmdPtr(now.Add(-time.Hour)))

	panel := func() [6]int64 {
		var p [6]int64 // total / succeeded / failed / unknown / cancelled / unresolved_unknown
		b := cmdBaseline(t, db)
		p[0] = cmdCount(t, db, &models.CommandExecution{}) + b.OperationsTotal
		db.Model(&models.CommandExecution{}).Where("status = ?", commandexec.StatusSucceeded).Count(&p[1])
		p[1] += b.Succeeded
		db.Model(&models.CommandExecution{}).Where("status = ?", commandexec.StatusFailed).Count(&p[2])
		p[2] += b.Failed
		db.Model(&models.CommandExecution{}).Where("status = ?", commandexec.StatusUnknown).Count(&p[3])
		p[3] += b.Unknown
		db.Model(&models.CommandExecution{}).Where("status = ?", commandexec.StatusCancelled).Count(&p[4])
		p[4] += b.Cancelled
		// unresolved_unknown: 【刻意不加基线】, 它随人工处置下降。
		db.Model(&models.CommandExecution{}).
			Where("status = ? AND NOT EXISTS (SELECT 1 FROM command_manual_resolutions cmr WHERE cmr.command_id = command_executions.command_id)",
				commandexec.StatusUnknown).
			Count(&p[5])
		return p
	}
	before := panel()

	c := newCmdExecutionCleaner(db, now)
	c.SetBatchSize(3) // 强制多批 (5 条到期行 / 每批 3)
	first, err := c.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	// b1~b4 可删; b5 (未处置 UNKNOWN) 与 b6 (窗内) 必须留下。
	if first.DeletedTotal != 4 {
		t.Fatalf("DeletedTotal = %d, want 4", first.DeletedTotal)
	}
	// 基线逐字段 = 实际删除行数 (删除与累加不许分叉)。
	if first.BaselineApplied.Total != 4 || first.BaselineApplied.Failed != 1 ||
		first.BaselineApplied.Succeeded != 1 || first.BaselineApplied.Cancelled != 1 ||
		first.BaselineApplied.Unknown != 1 {
		t.Errorf("BaselineApplied = %+v, want {Total:4 Failed:1 Succeeded:1 Cancelled:1 Unknown:1}", first.BaselineApplied)
	}
	// 【只累加已处置的 UNKNOWN】: 未处置的那条根本不该被删, 更不该进基线。
	if b := cmdBaseline(t, db); b.Unknown != 1 {
		t.Errorf("baseline.Unknown = %d, want 1 (只有已处置 UNKNOWN)", b.Unknown)
	}

	after := panel()
	if after[0] != before[0] {
		t.Errorf("operations_total 被清理改写: %d → %d (INV-2)", before[0], after[0])
	}
	if after[2] != before[2] {
		t.Errorf("failed 被清理改写: %d → %d (INV-2)", before[2], after[2])
	}
	if after[3] != before[3] {
		t.Errorf("unknown 被清理改写: %d → %d (INV-2)", before[3], after[3])
	}
	if after[1] != before[1] || after[4] != before[4] {
		t.Errorf("succeeded/cancelled 被清理改写: %d/%d → %d/%d", before[1], before[4], after[1], after[4])
	}
	if after[5] != 1 {
		t.Errorf("unresolved_unknown = %d, want 1 (既不得被删, 也不得吃基线)", after[5])
	}

	// 第二轮: 已经删尽 ⇒ 不删行、不改基线 (幂等)。若累加写成覆盖写, 这里会掉数。
	second, err := c.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("second cleanup: %v", err)
	}
	if second.DeletedTotal != 0 {
		t.Errorf("second run DeletedTotal = %d, want 0", second.DeletedTotal)
	}
	if b := cmdBaseline(t, db); b.OperationsTotal != 4 || b.Unknown != 1 {
		t.Errorf("second run changed baseline: %+v", b)
	}
	after2 := panel()
	if after2[0] != before[0] || after2[2] != before[2] || after2[5] != 1 {
		t.Errorf("两轮清理后面板口径漂移: before=%v after2=%v", before, after2)
	}
}

// ── INV-4: 在途行永不删 (三表一起) ───────────────────────────────────

// TestCommandDomain_InFlightRowsNeverDeleted 是所有清理器一起跑时的 INV-4 断言。
//
// 变红条件: 任何一个清理器只按时间过滤而忘了状态白名单。
func TestCommandDomain_InFlightRowsNeverDeleted(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := cmdNow()
	ancient := cmdDaysAgo(now, 5000)

	seedCmdExecution(t, db, "f1-queued", commandexec.StatusQueued, "{}", ancient, nil)
	seedCmdExecution(t, db, "f2-dispatched", commandexec.StatusDispatched, "{}", ancient, nil)
	seedCmdExecution(t, db, "f3-verifying", commandexec.StatusVerifying, "{}", ancient, nil)
	// outbox 的在途态: 无论多老都不删 (删一条 PENDING = 一条已持久化的命令永远发不出去)。
	seedCmdOutbox(t, db, "f1-outbox", "f1-queued", "PENDING", "{}", ancient, nil)
	seedCmdOutbox(t, db, "f2-outbox", "f2-dispatched", "LEASED", "{}", ancient, nil)
	// 【关键 fail-closed】: PENDING 的 outbox 即使对应 execution 已经终态也不删
	// (判定条件是 state 白名单【且】execution 非在途, 两者缺一不可)。
	seedCmdExecution(t, db, "f4-terminal", commandexec.StatusSucceeded, "{}", ancient, cmdPtr(ancient))
	seedCmdOutbox(t, db, "f4-outbox-pending", "f4-terminal", "PENDING", "{}", ancient, nil)
	// 未知 state 同样 fail-closed (command_id 上有唯一索引, 故另起一条命令)。
	seedCmdExecution(t, db, "f5-terminal", commandexec.StatusSucceeded, "{}", ancient, cmdPtr(ancient))
	seedCmdOutbox(t, db, "f5-outbox-weird", "f5-terminal", "DEAD_LETTER", "{}", ancient, nil)

	if _, err := newCmdExecutionCleaner(db, now).RunOnce(context.Background()); err != nil {
		t.Fatalf("execution cleanup: %v", err)
	}
	if _, err := newCmdOutboxCleaner(db, now).RunOnce(context.Background()); err != nil {
		t.Fatalf("outbox cleanup: %v", err)
	}
	if _, err := newCmdAttemptCleaner(db, now).RunOnce(context.Background()); err != nil {
		t.Fatalf("attempt cleanup: %v", err)
	}

	// f4/f5 的 execution 是【终态且已到期】⇒ 被删除是正确的; 它们的 outbox 因为
	// state 不在白名单 (PENDING / 未知态) 而必须留存 —— 这正是本条要钉的 fail-closed。
	assertCmdStrings(t, "in-flight executions", cmdExecutionIDs(t, db),
		[]string{"f1-queued", "f2-dispatched", "f3-verifying"})
	// cmdOutboxIDs 取的是 command_id (outbox 的自增 ID 不可预测, 见 models.CommandOutbox)。
	assertCmdStrings(t, "in-flight outboxes", cmdOutboxIDs(t, db),
		[]string{"f1-queued", "f2-dispatched", "f4-terminal", "f5-terminal"})
}

// ── outbox: 终态 + execution 非在途 双重条件 ──────────────────────────

// TestCommandOutboxCleaner_TerminalOnlyAndExecutionTerminal 钉住 outbox 的删除边界。
//
// 变红条件:
//   - 忘了 state 白名单 ⇒ o6(PENDING)/o7(LEASED)/o8(未知态) 被删;
//   - 忘了"对应 execution 非在途" ⇒ o3 被删 (execution 还在 VERIFYING);
//   - 用 processed_at 单独作 cutoff ⇒ o5(processed_at 为空的脏终态行) 永远删不掉;
//   - 窗口写错 ⇒ o4 (29 天) 被删或 o1 (40 天) 留下;
//   - 窗口变成 30 天以外的值 ⇒ 末尾的默认窗口断言红。
func TestCommandOutboxCleaner_TerminalOnlyAndExecutionTerminal(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := cmdNow()
	ancient := cmdDaysAgo(now, 1000)
	expired := cmdDaysAgo(now, 40)
	inWindow := cmdDaysAgo(now, 29)

	// 每条 outbox 一条独立命令 (command_outboxes.command_id 上有唯一索引)。
	// 对应的 execution 全是终态, 只有 o3 是例外。
	for _, id := range []string{"o1", "o2", "o4", "o5", "o6", "o7", "o8"} {
		seedCmdExecution(t, db, id, commandexec.StatusSucceeded, "{}", ancient, cmdPtr(ancient))
	}
	seedCmdExecution(t, db, "o3", commandexec.StatusVerifying, "{}", ancient, nil)

	seedCmdOutbox(t, db, "o1-processed-expired", "o1", "PROCESSED", "{}", ancient, cmdPtr(expired))
	seedCmdOutbox(t, db, "o2-cancelled-expired", "o2", "CANCELLED", "{}", ancient, cmdPtr(expired))
	// 留: execution 非终态 (在途) —— 这条 outbox 是排查该在途命令的唯一线索。
	seedCmdOutbox(t, db, "o3-exec-inflight", "o3", "PROCESSED", "{}", ancient, cmdPtr(expired))
	// 留: 仍在 30 天窗内。
	seedCmdOutbox(t, db, "o4-in-window", "o4", "PROCESSED", "{}", ancient, cmdPtr(inWindow))
	// 删: state 是终态但 processed_at 为空 (脏行) ⇒ COALESCE 回落到 created_at。
	seedCmdOutbox(t, db, "o5-processed-nil-at", "o5", "PROCESSED", "{}", expired, nil)
	// 留: 非终态, 无论多老。
	seedCmdOutbox(t, db, "o6-pending", "o6", "PENDING", "{}", ancient, nil)
	seedCmdOutbox(t, db, "o7-leased", "o7", "LEASED", "{}", ancient, nil)
	seedCmdOutbox(t, db, "o8-weird-state", "o8", "DEAD_LETTER", "{}", ancient, nil)

	c := newCmdOutboxCleaner(db, now)
	c.SetBatchSize(1) // 每批 1 行: 强制把 LIMIT 分批循环走满
	report, err := c.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("outbox cleanup: %v", err)
	}
	if report.Deleted != 3 {
		t.Errorf("Deleted = %d, want 3 (o1/o2/o5)", report.Deleted)
	}
	assertCmdStrings(t, "remaining outboxes", cmdOutboxIDs(t, db), []string{"o3", "o4", "o6", "o7", "o8"})

	// 默认窗口必须是 30 天 (唯一可以短留的表), 且【不得】变成 730。
	if got := NewCommandOutboxCleaner(db).retentionWindowDays(); got != 30 {
		t.Errorf("outbox 默认窗口 = %d, want 30", got)
	}
}

// TestCommandOutboxCleaner_OrphanOutboxIsNotStuckForever 钉住一个反向故障:
// execution 行已被删 (730 天清理 / 运维整体清理) 的终态 outbox 若也留着,
// 就会成为【永远删不掉的孤儿】—— 表重新无界增长, 正是本任务要消除的东西。
//
// 变红条件: 有人把判定写成 EXISTS(终态 execution) —— 孤儿行从此永不满足条件。
func TestCommandOutboxCleaner_OrphanOutboxIsNotStuckForever(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := cmdNow()
	old := cmdDaysAgo(now, 800)

	// 只有 outbox, 没有对应的 execution 行 (孤儿)。
	seedCmdOutbox(t, db, "orphan-outbox", "gone-execution", "PROCESSED", "{}", old, cmdPtr(old))
	if _, err := newCmdOutboxCleaner(db, now).RunOnce(context.Background()); err != nil {
		t.Fatalf("outbox cleanup: %v", err)
	}
	if got := cmdCount(t, db, &models.CommandOutbox{}); got != 0 {
		t.Errorf("孤儿 outbox 存活数 = %d, want 0 (否则永远删不掉)", got)
	}
}

// ── attempt: 730 天, 与 execution 同寿命, 且不碰别的表 ────────────────

// TestCommandAttemptCleaner_DeletesOnlyExpiredAndTouchesNothingElse 同时钉 INV-3 与窗口。
//
// 变红条件:
//   - 窗口被调到短于 execution (INV-6) ⇒ 常量断言与跨表用例同时红;
//   - DELETE 写了跨表语句 / 加了 ON DELETE CASCADE ⇒ 另外两张表的行数变红;
//   - 按 published_at 而不是 created_at 删 ⇒ 崩溃窗口内 published_at 为空的 a3 永不删。
func TestCommandAttemptCleaner_DeletesOnlyExpiredAndTouchesNothingElse(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := cmdNow()

	// INV-6 的常量级断言 (attempt 不得短于 execution)。
	if DefaultCommandAttemptRetentionDays < DefaultCommandExecutionRetentionDays {
		t.Fatalf("INV-6 违反: attempt 窗口 %d < execution 窗口 %d",
			DefaultCommandAttemptRetentionDays, DefaultCommandExecutionRetentionDays)
	}

	seedCmdExecution(t, db, "a1", commandexec.StatusSucceeded, "{}", cmdDaysAgo(now, 900), cmdPtr(cmdDaysAgo(now, 900)))
	seedCmdOutbox(t, db, "a1-outbox", "a1", "PROCESSED", "{}", cmdDaysAgo(now, 900), cmdPtr(cmdDaysAgo(now, 900)))
	// 每条 attempt 一条独立命令 (command_id+attempt_no 上有唯一索引)。
	seedCmdAttempt(t, db, "a1", "a1", cmdDaysAgo(now, 731), cmdPtr(cmdDaysAgo(now, 731)))
	// 留: 仍在 730 天窗内。
	seedCmdAttempt(t, db, "a2", "a2", cmdDaysAgo(now, 700), cmdPtr(cmdDaysAgo(now, 700)))
	// 删: created_at 在窗外, published_at 为空 (传输层从未回填)。
	seedCmdAttempt(t, db, "a3", "a3", cmdDaysAgo(now, 800), nil)

	execBefore, outboxBefore := cmdCount(t, db, &models.CommandExecution{}), cmdCount(t, db, &models.CommandOutbox{})

	if got := NewCommandAttemptCleaner(db).retentionWindowDays(); got != 730 {
		t.Errorf("attempt 默认窗口 = %d, want 730 (与 execution 同寿命)", got)
	}
	c := newCmdAttemptCleaner(db, now)
	report, err := c.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("attempt cleanup: %v", err)
	}
	if report.Deleted != 2 {
		t.Errorf("Deleted = %d, want 2 (a1/a3)", report.Deleted)
	}
	assertCmdStrings(t, "remaining attempts", cmdAttemptIDs(t, db), []string{"a2"})
	// INV-3: 删 attempt 不得改变另外两张表。
	if got := cmdCount(t, db, &models.CommandExecution{}); got != execBefore {
		t.Errorf("INV-3 违反: command_executions %d → %d", execBefore, got)
	}
	if got := cmdCount(t, db, &models.CommandOutbox{}); got != outboxBefore {
		t.Errorf("INV-3 违反: command_outboxes %d → %d", outboxBefore, got)
	}
}

// TestCommandDomain_DeletingOutboxDoesNotCascade 是 INV-3 的针对性用例:
// 三张表各造一条【都已过期】的行, 只跑 outbox 清理 ⇒ outbox 消失, 另外两张原样。
//
// 变红条件: 有人加了 ON DELETE CASCADE、写了跨表 DELETE、或让 outbox 清理
// 牵连 execution/attempt。
func TestCommandDomain_DeletingOutboxDoesNotCascade(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := cmdNow()
	old := cmdDaysAgo(now, 800)

	seedCmdExecution(t, db, "c1", commandexec.StatusSucceeded, "params-c1", old, cmdPtr(old))
	seedCmdAttempt(t, db, "c1", "c1", old, cmdPtr(old))
	seedCmdOutbox(t, db, "c1-outbox", "c1", "PROCESSED", "params-c1", old, cmdPtr(old))

	report, err := newCmdOutboxCleaner(db, now).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("outbox cleanup: %v", err)
	}
	if report.Deleted != 1 {
		t.Fatalf("outbox Deleted = %d, want 1", report.Deleted)
	}
	assertCmdStrings(t, "outboxes", cmdOutboxIDs(t, db), nil)
	assertCmdStrings(t, "executions (不得级联删除)", cmdExecutionIDs(t, db), []string{"c1"})
	assertCmdStrings(t, "attempts (不得级联删除)", cmdAttemptIDs(t, db), []string{"c1"})
}

// ── INV-6 跨表: execution 还活着 ⇒ 证据必须还活着 ─────────────────────

// TestCommandDomain_AttemptOutlivesExecutionWithinWindow 复刻设计文档 INV-6 的测试:
// execution E 在 730 天窗内 (700 天) 且有 attempt ⇒ 三个清理器一起跑后,
// E 存活必须推出 attempt 存活。
//
// 变红条件: 有人把 attempt 的窗口调短 (例如照抄 outbox 的 30 天) —— 那会抹掉
// "这条命令当时是否真的发出去了"的唯一物理证据 (硬约束 3)。
func TestCommandDomain_AttemptOutlivesExecutionWithinWindow(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := cmdNow()
	age := cmdDaysAgo(now, 700)

	// execution 已终态 (SUCCEEDED) 且 completed_at 在 730 天窗内 (700 天):
	// 它活着 ⇒ attempt 必须活着; 而 outbox (30 天窗) 到期被删是预期行为。
	seedCmdExecution(t, db, "e-in-window", commandexec.StatusSucceeded, "params-in-window", age, cmdPtr(age))
	seedCmdAttempt(t, db, "e-in-window", "e-in-window", age, cmdPtr(age))
	seedCmdOutbox(t, db, "e-in-window", "e-in-window", "PROCESSED", "params-in-window", age, cmdPtr(age))

	if _, err := newCmdExecutionCleaner(db, now).RunOnce(context.Background()); err != nil {
		t.Fatalf("execution cleanup: %v", err)
	}
	outboxReport, err := newCmdOutboxCleaner(db, now).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("outbox cleanup: %v", err)
	}
	if _, err := newCmdAttemptCleaner(db, now).RunOnce(context.Background()); err != nil {
		t.Fatalf("attempt cleanup: %v", err)
	}

	// outbox (30 天) 到期被删是【预期且正确】的 —— 它的两条事实在别处各有一份。
	if outboxReport.Deleted != 1 {
		t.Errorf("outbox Deleted = %d, want 1 (30 天窗)", outboxReport.Deleted)
	}
	assertCmdStrings(t, "executions", cmdExecutionIDs(t, db), []string{"e-in-window"})
	assertCmdStrings(t, "attempts (证据不得先于被审计对象死亡)", cmdAttemptIDs(t, db), []string{"e-in-window"})

	// 删 outbox 之后, 它承载的两条事实必须仍可从别处读到 (冗余性的真正含义)。
	var exec models.CommandExecution
	if err := db.First(&exec, "command_id = ?", "e-in-window").Error; err != nil {
		t.Fatalf("execution 消失: %v", err)
	}
	if exec.ParamsJSON != "params-in-window" {
		t.Errorf("params_json = %q, want 原样保留", exec.ParamsJSON)
	}
	var attempt models.CommandAttempt
	if err := db.First(&attempt, "command_id = ?", "e-in-window").Error; err != nil {
		t.Fatalf("attempt 消失: %v", err)
	}
	if attempt.PublishedAt == nil || !attempt.PublishedAt.Equal(age) {
		t.Errorf("published_at = %v, want %v (发布时刻的第二处记录必须还在)", attempt.PublishedAt, age)
	}
}

// ── INV-9: outbox 冗余性用容差断言, 不得写等号 ────────────────────────

// TestCommandDomain_OutboxRedundancyIsToleranceNotEquality 把 INV-9 的两条断言
// 都跑成代码, 并且【显式证明等号形式不成立】。
//
// 实测依据 (设计 §2.5, 主控已复现): 3773 对里只有 11 对 processed_at 与 published_at
// 精确相等, 最大差 93µs, 且无一条 outbox 早于对应 attempt。根因是同一事务内两次
// 独立的 time.Now() (dispatcher 写 processed_at 与传输层写 PublishedAt)。
//
// 变红条件 (方向相反, 两条都要能红):
//  1. 有人把 payload_json 改成与 params_json 不同的内容 ⇒ A 变红 ⇒ 本表不再冗余
//     ⇒ 必须停止短留;
//  2. 有人把 B 写成 processed_at == published_at ⇒ 第二段断言立刻变红
//     (93µs 的差在这一对上是真实存在的)。
func TestCommandDomain_OutboxRedundancyIsToleranceNotEquality(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := cmdNow()
	published := cmdDaysAgo(now, 40)
	processed := published.Add(93 * time.Microsecond) // 实测最大差 93µs
	const payload = `{"angle":90,"duration_ms":1500}`

	seedCmdExecution(t, db, "inv9", commandexec.StatusSucceeded, payload, published, cmdPtr(published))
	seedCmdAttempt(t, db, "inv9", "inv9", published, cmdPtr(published))
	seedCmdOutbox(t, db, "inv9-outbox", "inv9", "PROCESSED", payload, published, cmdPtr(processed))

	// 断言 A: 逐字节相等 (这条可以写等号)。
	var pair struct {
		Payload   string
		Params    string
		Processed *time.Time
		Published *time.Time
	}
	if err := db.Table("command_outboxes o").
		Select("o.payload_json AS payload, e.params_json AS params, o.processed_at AS processed, a.published_at AS published").
		Joins("JOIN command_executions e ON e.command_id = o.command_id").
		Joins("JOIN command_attempts a ON a.command_id = o.command_id").
		Scan(&pair).Error; err != nil {
		t.Fatalf("read INV-9 pair: %v", err)
	}
	if pair.Payload != pair.Params {
		t.Errorf("INV-9 断言 A 不成立: payload_json = %q, params_json = %q ⇒ outbox 不再冗余, 禁止清理",
			pair.Payload, pair.Params)
	}
	// 断言 B: 容差 (< 1ms), 且【不是】等号。
	if pair.Processed == nil || pair.Published == nil {
		t.Fatalf("processed_at/published_at 缺失: %+v", pair)
	}
	diff := pair.Processed.Sub(*pair.Published)
	if diff < 0 {
		diff = -diff
	}
	if diff >= time.Millisecond {
		t.Errorf("INV-9 断言 B 不成立: |processed_at - published_at| = %v, want < 1ms", diff)
	}
	if diff == 0 {
		t.Error("本条数据构造的是 93µs 的差值却观察到精确相等 —— 若这是有人把 B '修'成等号的结果, " +
			"请回到容差形式 (实测仅 11/3773 精确相等, 等号会永久变红并阻止一个正确的清理)")
	}

	// 跑清理: outbox (30 天) 被删, 两条冗余来源必须都还在。
	report, err := newCmdOutboxCleaner(db, now).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("outbox cleanup: %v", err)
	}
	if report.Deleted != 1 {
		t.Fatalf("outbox Deleted = %d, want 1", report.Deleted)
	}
	var left struct {
		Params    string
		Published *time.Time
	}
	if err := db.Table("command_executions e").
		Select("e.params_json AS params, a.published_at AS published").
		Joins("JOIN command_attempts a ON a.command_id = e.command_id").
		Where("e.command_id = ?", "inv9").
		Scan(&left).Error; err != nil {
		t.Fatalf("read after cleanup: %v", err)
	}
	if left.Params != payload {
		t.Errorf("清理后 params_json = %q, want %q (冗余来源之一必须还在)", left.Params, payload)
	}
	if left.Published == nil || !left.Published.Equal(published) {
		t.Errorf("清理后 published_at = %v, want %v (冗余来源之二必须还在)", left.Published, published)
	}
}

// ── 纯函数: 删除范围快照 ─────────────────────────────────────────────

// TestCommandDomain_PrunableScopeIsWhitelistNotBlacklist 直接调用删除范围函数,
// 让"有人把非终态/未处置 UNKNOWN 放进删除范围"这件事立刻变红, 而不是等上线后
// 由用户发现"命令发不出去"或"悬案结不了案"。手法与 commandexec/retention_scope.go
// 的 PrunableStatuses 一致。
func TestCommandDomain_PrunableScopeIsWhitelistNotBlacklist(t *testing.T) {
	for _, s := range []string{
		commandexec.StatusQueued, commandexec.StatusDispatched,
		commandexec.StatusDeviceAccepted, commandexec.StatusVerifying,
	} {
		if ExecutionStatusPrunable(s) {
			t.Errorf("非终态 %s 被判定为可清理 (INV-4 违反)", s)
		}
	}
	// UNKNOWN 【单独】不可判定为可清理: 它必须先被 command_manual_resolutions 处置,
	// 那条路径由 PrunableExecutionsQuery 的 EXISTS 分支实现。
	if ExecutionStatusPrunable(commandexec.StatusUnknown) {
		t.Error("UNKNOWN 被判定为可清理 —— 未处置悬案会随时间被删 (硬约束 1 违反)")
	}
	for _, s := range []string{commandexec.StatusSucceeded, commandexec.StatusFailed, commandexec.StatusCancelled} {
		if !ExecutionStatusPrunable(s) {
			t.Errorf("终态 %s 不可清理: 保留策略形同虚设", s)
		}
	}
	scope := ExecutionCleanupScope()
	if scope.TimeExpression != "COALESCE(completed_at, created_at)" {
		t.Errorf("时间字段 = %q, want COALESCE(completed_at, created_at)", scope.TimeExpression)
	}
	if scope.RetentionDays != 730 {
		t.Errorf("execution 窗口 = %d, want 730", scope.RetentionDays)
	}
	// 三张表的窗口【必须各不相同】(outbox 唯一可短留): 一套参数删三表是设计 §3 要防的错误。
	if !(DefaultCommandOutboxRetentionDays < DefaultCommandAttemptRetentionDays &&
		DefaultCommandAttemptRetentionDays == DefaultCommandExecutionRetentionDays) {
		t.Errorf("窗口档位错误: outbox=%d attempt=%d execution=%d",
			DefaultCommandOutboxRetentionDays, DefaultCommandAttemptRetentionDays, DefaultCommandExecutionRetentionDays)
	}
	if len(outboxTerminalStates()) != 2 {
		t.Errorf("outbox 终态白名单 = %v, want 恰好 PROCESSED/CANCELLED", outboxTerminalStates())
	}
}

// TestCommandDomain_CleanersRequireDB 是最小安全网: 没有 db 时报错而不是 panic。
func TestCommandDomain_CleanersRequireDB(t *testing.T) {
	ctx := context.Background()
	if _, err := NewCommandExecutionCleaner(nil).RunOnce(ctx); err == nil {
		t.Error("CommandExecutionCleaner(nil).RunOnce 未报错")
	}
	if _, err := NewCommandAttemptCleaner(nil).RunOnce(ctx); err == nil {
		t.Error("CommandAttemptCleaner(nil).RunOnce 未报错")
	}
	if _, err := NewCommandOutboxCleaner(nil).RunOnce(ctx); err == nil {
		t.Error("CommandOutboxCleaner(nil).RunOnce 未报错")
	}
}

// ── 挂载点: 三个清理器必须在每日 RetentionTask.RunOnce 里被调用 ──────

// TestRetentionTask_RunOnceInvokesCommandDomainCleaners 是挂载点的接线测试:
// 有人把三个 runOnceLogged 调用从 RunOnce 里删掉, 本用例立刻变红
// (清理器写得再好, 没人调用等于没上线)。
func TestRetentionTask_RunOnceInvokesCommandDomainCleaners(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Now().UTC() // 这里用真实时钟: 走的是生产构造路径 NewRetentionTask
	old := now.AddDate(0, 0, -800)
	recent := now.AddDate(0, 0, -10)

	seedCmdExecution(t, db, "w1-exec", commandexec.StatusSucceeded, "{}", old, cmdPtr(old))
	seedCmdExecution(t, db, "w2-exec", commandexec.StatusUnknown, "{}", old, cmdPtr(old))
	seedCmdResolution(t, db, "w2-exec", old)
	seedCmdExecution(t, db, "w3-exec-unknown-unresolved", commandexec.StatusUnknown, "{}", old, cmdPtr(old))
	seedCmdAttempt(t, db, "w1-attempt", "w1-exec", old, cmdPtr(old))
	seedCmdOutbox(t, db, "w1-outbox", "w1-exec", "PROCESSED", "{}", old, cmdPtr(old))
	// 30 天窗内: 不得被误删。
	seedCmdExecution(t, db, "w4-exec", commandexec.StatusFailed, "{}", recent, cmdPtr(recent))
	seedCmdOutbox(t, db, "w4-outbox", "w4-exec", "PROCESSED", "{}", recent, cmdPtr(recent))

	r := NewRetentionTask(db)
	r.SetBatchSleep(0)
	if _, err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("retention RunOnce: %v", err)
	}
	assertCmdStrings(t, "executions after RunOnce", cmdExecutionIDs(t, db),
		[]string{"w3-exec-unknown-unresolved", "w4-exec"})
	assertCmdStrings(t, "attempts after RunOnce", cmdAttemptIDs(t, db), nil)
	assertCmdStrings(t, "outboxes after RunOnce", cmdOutboxIDs(t, db), []string{"w4-exec"})
	if b := cmdBaseline(t, db); b.OperationsTotal != 2 || b.Unknown != 1 {
		t.Errorf("baseline after RunOnce = %+v, want Total:2 Unknown:1", b)
	}
}
