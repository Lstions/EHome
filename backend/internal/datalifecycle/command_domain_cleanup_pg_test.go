package datalifecycle

import (
	"context"
	"testing"
	"time"

	"ehome/backend/internal/commandexec"
	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

// PG-only 反向证明 (与 notification_cleanup_pg_test.go 同范式)。
//
// 命令域三表清理里有三处【PG 与 SQLite 最可能不同】的地方, 必须在真 PG 上跑:
//
//  1. `DELETE FROM t WHERE id IN (SELECT id ... LIMIT ?)` 的子查询 LIMIT 语义
//     (分批循环是否会重复删同一批 / 提前终止);
//  2. `COALESCE(completed_at, created_at) < ?` 的 timestamptz 比较与精度;
//  3. command_metrics_baselines 的 `ON CONFLICT DO UPDATE` 累加 (INV-2 的载体),
//     以及 command_executions.verified_result_json 的 jsonb 列。
//
// 库隔离 (数据库纪律): testutil.OpenTestDB 在 EHOME_DB_NAME 指定的库里建一个随机
// schema `test_<suffix>`, 所有表建在该 schema 内, 结束时 t.Cleanup 执行
// `DROP SCHEMA ... CASCADE`。本文件对落脚库的【表数据零写入】。
//
// 未设 EHOME_TEST_DB=postgres 时 requirePostgres(t) 会显式 skip —— 不伪装成"跑过了"。

// TestCommandDomainCleaner_Postgres_CountsExact 是 SQLite 用例在 PG 上的逐条重跑:
// 三张表各自的窗口、档位、白名单与 fail-closed 边界, 断言【精确条数】。
func TestCommandDomainCleaner_Postgres_CountsExact(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	if db.Dialector.Name() != "postgres" {
		t.Fatalf("dialector = %q, want postgres (SQLite 下本测试必须 skip 而不是静默替换方言)", db.Dialector.Name())
	}
	now := cmdNow()
	old, expired, inWindow := cmdDaysAgo(now, 1000), cmdDaysAgo(now, 40), cmdDaysAgo(now, 29)

	// ── executions ──────────────────────────────────────────────────
	seedCmdExecution(t, db, "pg-e1", commandexec.StatusFailed, "params-1", old, cmdPtr(cmdDaysAgo(now, 731))) // 删
	seedCmdExecution(t, db, "pg-e2", commandexec.StatusUnknown, "params-2", old, cmdPtr(old))                 // 删 (已处置)
	seedCmdResolution(t, db, "pg-e2", old)
	seedCmdExecution(t, db, "pg-e3", commandexec.StatusQueued, "params-3", old, nil)                 // 留 (在途)
	seedCmdExecution(t, db, "pg-e4", commandexec.StatusUnknown, "params-4", old, cmdPtr(old))        // 留 (未处置悬案)
	seedCmdExecution(t, db, "pg-e5", commandexec.StatusSucceeded, "params-5", old, cmdPtr(inWindow)) // 留 (完成在窗内)
	seedCmdExecution(t, db, "pg-e6", "SUPERSEDED", "params-6", old, cmdPtr(old))                     // 留 (白名单外)

	execCleaner := newCmdExecutionCleaner(db, now)
	execCleaner.SetBatchSize(1) // 每批 1 行: 把子查询 LIMIT 循环走满
	execReport, err := execCleaner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("PG execution cleanup: %v", err)
	}
	if execReport.DeletedTotal != 2 {
		t.Errorf("PG execution DeletedTotal = %d, want 2", execReport.DeletedTotal)
	}
	assertCmdStrings(t, "PG remaining executions", cmdExecutionIDs(t, db),
		[]string{"pg-e3", "pg-e4", "pg-e5", "pg-e6"})
	// INV-2 的 PG 侧断言: 基线累加走的是 ON CONFLICT DO UPDATE, 数字必须精确。
	if b := cmdBaseline(t, db); b.OperationsTotal != 2 || b.Failed != 1 || b.Unknown != 1 {
		t.Errorf("PG baseline = %+v, want {OperationsTotal:2 Failed:1 Unknown:1}", b)
	}

	// ── outboxes: state 白名单 + execution 非在途 ────────────────────
	// command_outboxes.command_id 上有【唯一索引】(1:1), 故每条 outbox 配一条独立命令。
	// 这些 execution 的 completed_at 取【窗内】—— 它们只用来满足 outbox 的
	// "对应 execution 非在途"条件, 必须在整段用例里存活 (否则第二轮 execution 清理
	// 会把它们删掉, 幂等断言就测不到它想测的东西)。
	seedCmdExecution(t, db, "pg-o1", commandexec.StatusSucceeded, "params-o1", old, cmdPtr(inWindow))
	seedCmdExecution(t, db, "pg-o2", commandexec.StatusSucceeded, "params-o2", old, cmdPtr(inWindow))
	seedCmdExecution(t, db, "pg-o4", commandexec.StatusSucceeded, "params-o4", old, cmdPtr(inWindow))
	seedCmdExecution(t, db, "pg-o6", commandexec.StatusSucceeded, "params-o6", old, cmdPtr(inWindow))
	seedCmdOutbox(t, db, "pg-ob1", "pg-o1", "PROCESSED", "params-o1", old, cmdPtr(expired))     // 删
	seedCmdOutbox(t, db, "pg-ob2", "pg-o2", "PENDING", "params-o2", old, nil)                   // 留 (非终态)
	seedCmdOutbox(t, db, "pg-ob3", "pg-e3", "PROCESSED", "params-3", old, cmdPtr(expired))      // 留 (execution 在途)
	seedCmdOutbox(t, db, "pg-ob4", "pg-o4", "PROCESSED", "params-o4", old, cmdPtr(inWindow))    // 留 (窗内)
	seedCmdOutbox(t, db, "pg-ob5", "pg-gone", "PROCESSED", "params-gone", old, cmdPtr(expired)) // 删 (孤儿, 否则永远删不掉)
	seedCmdOutbox(t, db, "pg-ob6", "pg-o6", "PROCESSED", "params-o6", expired, nil)             // 删 (processed_at 为空 → COALESCE)

	outboxCleaner := newCmdOutboxCleaner(db, now)
	outboxCleaner.SetBatchSize(2) // 强制多批
	outboxReport, err := outboxCleaner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("PG outbox cleanup: %v", err)
	}
	if outboxReport.Deleted != 3 {
		t.Errorf("PG outbox Deleted = %d, want 3 (pg-ob1/ob5/ob6)", outboxReport.Deleted)
	}
	// 剩下的 command_id 排序: pg-e3 (在途) / pg-o2 (PENDING) / pg-o4 (窗内)。
	assertCmdStrings(t, "PG remaining outboxes", cmdOutboxIDs(t, db), []string{"pg-e3", "pg-o2", "pg-o4"})

	// ── attempts: 730 天, 且不碰任何别的表 (INV-3) ────────────────────
	// command_attempts 的唯一索引是 (command_id, attempt_no): 每条一个独立 command_id。
	seedCmdAttempt(t, db, "pg-at1", "pg-at1", cmdDaysAgo(now, 731), cmdPtr(cmdDaysAgo(now, 731))) // 删
	seedCmdAttempt(t, db, "pg-at2", "pg-at2", cmdDaysAgo(now, 700), cmdPtr(cmdDaysAgo(now, 700))) // 留 (与 execution 同寿命)
	seedCmdAttempt(t, db, "pg-at3", "pg-at3", cmdDaysAgo(now, 800), nil)                          // 删 (published_at 为空)

	execBefore, outboxBefore := cmdCount(t, db, &models.CommandExecution{}), cmdCount(t, db, &models.CommandOutbox{})
	attemptCleaner := newCmdAttemptCleaner(db, now)
	attemptCleaner.SetBatchSize(1)
	attemptReport, err := attemptCleaner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("PG attempt cleanup: %v", err)
	}
	if attemptReport.Deleted != 2 {
		t.Errorf("PG attempt Deleted = %d, want 2 (pg-a1/a3)", attemptReport.Deleted)
	}
	assertCmdStrings(t, "PG remaining attempts", cmdAttemptIDs(t, db), []string{"pg-at2"})
	if got := cmdCount(t, db, &models.CommandExecution{}); got != execBefore {
		t.Errorf("PG INV-3 违反: command_executions %d → %d", execBefore, got)
	}
	if got := cmdCount(t, db, &models.CommandOutbox{}); got != outboxBefore {
		t.Errorf("PG INV-3 违反: command_outboxes %d → %d", outboxBefore, got)
	}

	// ── 幂等: 三个清理器再跑一轮都必须是 no-op ────────────────────────
	for name, run := range map[string]func() (int64, error){
		"execution": func() (int64, error) {
			r, err := execCleaner.RunOnce(context.Background())
			return r.DeletedTotal, err
		},
		"outbox": func() (int64, error) {
			r, err := outboxCleaner.RunOnce(context.Background())
			return r.Deleted, err
		},
		"attempt": func() (int64, error) {
			r, err := attemptCleaner.RunOnce(context.Background())
			return r.Deleted, err
		},
	} {
		deleted, err := run()
		if err != nil {
			t.Fatalf("PG second %s cleanup: %v", name, err)
		}
		if deleted != 0 {
			t.Errorf("PG second %s cleanup deleted %d, want 0 (幂等)", name, deleted)
		}
	}
	// 基线在第二轮后【不得回退】(覆盖写会让这里掉数)。
	if b := cmdBaseline(t, db); b.OperationsTotal != 2 || b.Unknown != 1 {
		t.Errorf("PG baseline after rerun = %+v, want {OperationsTotal:2 Unknown:1}", b)
	}
}

// TestCommandDomainCleaner_Postgres_JsonbAndTimestampPrecision 覆盖两处方言敏感点:
//
//   - params_json / payload_json 是 text, 但 verified_result_json 是 jsonb:
//     PG 上写非法 JSON 会直接报错 —— 顺带证明 seed 与生产写入同形;
//   - 时间列在 PG 是 timestamptz (微秒精度): INV-9 的 93µs 差值必须被【原样保存】,
//     否则容差断言会退化成"永远是 0 差"的假绿。
func TestCommandDomainCleaner_Postgres_JsonbAndTimestampPrecision(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	if db.Dialector.Name() != "postgres" {
		t.Fatalf("dialector = %q, want postgres", db.Dialector.Name())
	}
	now := cmdNow()
	published := cmdDaysAgo(now, 40)
	processed := published.Add(93 * time.Microsecond)
	const payload = `{"angle":90,"duration_ms":1500}`

	seedCmdExecution(t, db, "pg-p1", commandexec.StatusSucceeded, payload, published, cmdPtr(published))
	seedCmdAttempt(t, db, "pg-p1", "pg-p1", published, cmdPtr(published))
	seedCmdOutbox(t, db, "pg-p1-ob", "pg-p1", "PROCESSED", payload, published, cmdPtr(processed))

	var pair struct {
		Payload   string
		Params    string
		Processed time.Time
		Published time.Time
	}
	if err := db.Table("command_outboxes o").
		Select("o.payload_json AS payload, e.params_json AS params, o.processed_at AS processed, a.published_at AS published").
		Joins("JOIN command_executions e ON e.command_id = o.command_id").
		Joins("JOIN command_attempts a ON a.command_id = o.command_id").
		Where("o.command_id = ?", "pg-p1").
		Scan(&pair).Error; err != nil {
		t.Fatalf("PG read INV-9 pair: %v", err)
	}
	// 断言 A: 逐字节相等 (文本列, 与方言无关, 但必须在 PG 上验证一次)。
	if pair.Payload != pair.Params {
		t.Errorf("PG INV-9 断言 A 不成立: payload=%q params=%q", pair.Payload, pair.Params)
	}
	if pair.Params != payload {
		t.Errorf("PG params_json = %q, want %q (原样往返)", pair.Params, payload)
	}
	// 断言 B: 容差 < 1ms, 且【不是】等号 (PG timestamptz 微秒精度必须保住这 93µs)。
	diff := pair.Processed.Sub(pair.Published)
	if diff < 0 {
		diff = -diff
	}
	if diff != 93*time.Microsecond {
		t.Errorf("PG 时间精度丢失: |processed_at - published_at| = %v, want 93µs "+
			"(若为 0 则容差断言退化成假绿)", diff)
	}
	if diff >= time.Millisecond {
		t.Errorf("PG INV-9 断言 B 不成立: 差 = %v, want < 1ms", diff)
	}
	// 正方向: 无一条 outbox 早于对应 attempt (实测 outbox_before_attempt=0)。
	if pair.Processed.Before(pair.Published) {
		t.Errorf("PG: processed_at %v 早于 published_at %v (实测该方向从未出现)", pair.Processed, pair.Published)
	}
}

// TestCommandDomainCleaner_Postgres_BatchLoopTerminates 专钉 PG 的子查询 LIMIT:
// 7 行 × 每批 1 行, 必须恰好删 7 行后终止 (若 PG 每批重复删同一批行, 循环会空转
// 到 maxPurgeBatches; 若提前终止, 条数会少)。
func TestCommandDomainCleaner_Postgres_BatchLoopTerminates(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	now := cmdNow()
	old := cmdDaysAgo(now, 800)

	const expired = 7
	for i := 0; i < expired; i++ {
		id := "pg-batch-" + string(rune('a'+i))
		seedCmdExecution(t, db, id, commandexec.StatusCancelled, "{}", old, cmdPtr(old))
		seedCmdAttempt(t, db, id, id, old, cmdPtr(old))
		seedCmdOutbox(t, db, id+"-ob", id, "CANCELLED", "{}", old, cmdPtr(old))
	}

	execCleaner := newCmdExecutionCleaner(db, now)
	execCleaner.SetBatchSize(1)
	if report, err := execCleaner.RunOnce(context.Background()); err != nil {
		t.Fatalf("PG batched execution cleanup: %v", err)
	} else if report.DeletedTotal != expired {
		t.Errorf("PG batched execution DeletedTotal = %d, want %d", report.DeletedTotal, expired)
	}
	outboxCleaner := newCmdOutboxCleaner(db, now)
	outboxCleaner.SetBatchSize(1)
	if report, err := outboxCleaner.RunOnce(context.Background()); err != nil {
		t.Fatalf("PG batched outbox cleanup: %v", err)
	} else if report.Deleted != expired {
		t.Errorf("PG batched outbox Deleted = %d, want %d", report.Deleted, expired)
	}
	attemptCleaner := newCmdAttemptCleaner(db, now)
	attemptCleaner.SetBatchSize(1)
	if report, err := attemptCleaner.RunOnce(context.Background()); err != nil {
		t.Fatalf("PG batched attempt cleanup: %v", err)
	} else if report.Deleted != expired {
		t.Errorf("PG batched attempt Deleted = %d, want %d", report.Deleted, expired)
	}
	for _, m := range []interface{}{&models.CommandExecution{}, &models.CommandOutbox{}, &models.CommandAttempt{}} {
		if got := cmdCount(t, db, m); got != 0 {
			t.Errorf("PG remaining %T = %d, want 0", m, got)
		}
	}
	// 基线恰好等于 7 (每批 1 行的累加不能丢更新 —— 这正是 ON CONFLICT 的意义)。
	if b := cmdBaseline(t, db); b.OperationsTotal != expired || b.Cancelled != expired {
		t.Errorf("PG baseline = %+v, want {OperationsTotal:%d Cancelled:%d}", b, expired, expired)
	}
}
