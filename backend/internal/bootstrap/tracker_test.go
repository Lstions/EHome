package bootstrap

// tracker_test.go — 契约 §3.1 / §6.1 的 bootstrap 包验收单测。
//
// 覆盖（逐条对应任务验收）:
//  1. DefaultSteps 与契约表逐字一致（ID/Label/Weight，合计 100）；
//  2. Percent 按 Weight 加权：done/skipped 满权重、running 按 Progress 折算、pending 0；
//  3. 并发安全（配合 -race 跑）：多 goroutine 同时 Begin/Done/Snapshot 不脏读不 panic；
//  4. 失败态：Fail 置 failed + Error，Ready=false/Failed=true；
//  5. 深拷贝：改 Snapshot 返回值（含 *time.Time 指向的值）不影响内部状态；
//  6. Ready/Failed 语义：全部 done|skipped ⇒ ready（Lead 裁决，不用"最后一步 done"）；
//  7. SetBuildCommit / SetVersion：注入生效，空串不覆盖。

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestDefaultStepsTable 钉住契约 §3.1 的步骤表（ID/Label/Weight 逐字）。
// 这张表是前端与 api 的共同依赖，任何一处漂移都必须在这里变红。
func TestDefaultStepsTable(t *testing.T) {
	want := []struct {
		id, label string
		weight    int
	}{
		// 2026-11-08 硬切换移除 partition_unified_data（该表已停写，且保留迁移
		// 会在 DROP 后重启时把空表重建出来）。权重按原相对比例分给剩余步骤，
		// 合计仍为 100（由本用例末尾的 sum 断言守着）。
		// 2026-11-11: device_data 整表退役（原始数据退役方案 §2.1），
		// partition_device_data 一步随之删除，其权重按原相对比例分给剩余步骤。
		//
		// 2026-11-08 硬切换移除 partition_unified_data（该表已停写，且保留迁移
		// 会在 DROP 后重启时把空表重建出来）。权重同样按原相对比例分给剩余步骤，
		// 合计仍为 100（由本用例末尾的 sum 断言守着）。
		{"db_connect", "连接数据库", 5},
		{"auto_migrate", "数据库结构迁移", 16},
		// 2026-11-07: L2 帧表迁移（契约 docs/设计/遥测存储-L1L2无条件启用-契约-2026-11-07.md §3.5）。
		// 它单独一步是因为失败语义与 rollup 相反：帧表 Fatal，rollup 只 Warn。
		{"partition_telemetry_frame", "L2 帧表分区迁移", 10},
		{"partition_rollup", "聚合表分区就绪", 4},
		{"ensure_partitions", "滚动分区保障", 5},
		{"parent_indexes", "补齐父表索引", 35},
		{"retire_dead_indexes", "退役死索引", 7},
		{"warmup", "缓存预热", 11},
		{"startup_tasks", "后台任务启动", 7},
	}
	got := DefaultSteps()
	if len(got) != len(want) {
		t.Fatalf("DefaultSteps len = %d, want %d", len(got), len(want))
	}
	sum := 0
	for i, w := range want {
		if got[i].ID != w.id {
			t.Errorf("step[%d].ID = %q, want %q", i, got[i].ID, w.id)
		}
		if got[i].Label != w.label {
			t.Errorf("step[%d].Label = %q, want %q", i, got[i].Label, w.label)
		}
		if got[i].Weight != w.weight {
			t.Errorf("step[%d].Weight = %d, want %d", i, got[i].Weight, w.weight)
		}
		if got[i].State != StepPending {
			t.Errorf("step[%d].State = %q, want pending", i, got[i].State)
		}
		sum += got[i].Weight
	}
	if sum != 100 {
		t.Errorf("weights sum = %d, want 100 (契约 §3.1)", sum)
	}
	// 每次返回新切片：就地改动不得到下一次调用。
	got[0].ID = "mutated"
	if DefaultSteps()[0].ID != "db_connect" {
		t.Error("DefaultSteps must return a fresh slice each call (got shared backing array)")
	}
}

// TestNewTrackerCopiesAndResets 守构造语义：传入切片被复制且可变字段归零。
func TestNewTrackerCopiesAndResets(t *testing.T) {
	src := []Step{
		{ID: "a", Label: "A", Weight: 1, State: StepDone, Progress: 99, Detail: "x", Error: "boom"},
		{ID: "b", Label: "B", Weight: 1},
	}
	tr := NewTracker(src)
	snap := tr.Snapshot()
	if snap.Steps[0].State != StepPending || snap.Steps[0].Progress != 0 || snap.Steps[0].Error != "" {
		t.Errorf("NewTracker must reset mutable fields, got %+v", snap.Steps[0])
	}
	src[0].ID = "mutated"
	if tr.Snapshot().Steps[0].ID != "a" {
		t.Error("NewTracker must copy the input slice")
	}
	// nil / 空切片回落 DefaultSteps。
	if got := len(NewTracker(nil).Snapshot().Steps); got != len(DefaultSteps()) {
		t.Errorf("NewTracker(nil) steps = %d, want %d", got, len(DefaultSteps()))
	}
}

// TestPercentWeighted 守加权百分比（契约 §3.1 Percent 语义）。
func TestPercentWeighted(t *testing.T) {
	// 两个步骤，权重 3:1（总 4）。done 的计满，running 按 Progress 折算。
	newTwo := func() *Tracker {
		return NewTracker([]Step{
			{ID: "heavy", Label: "重", Weight: 3},
			{ID: "light", Label: "轻", Weight: 1},
		})
	}

	t.Run("all_pending_is_zero", func(t *testing.T) {
		if p := newTwo().Snapshot().Percent; p != 0 {
			t.Errorf("percent = %v, want 0", p)
		}
	})

	t.Run("running_counts_progress", func(t *testing.T) {
		tr := newTwo()
		tr.Begin("heavy")
		tr.StepProgress("heavy", 50, "")
		// 3 * 50% / 4 = 37.5
		if p := tr.Snapshot().Percent; p != 37.5 {
			t.Errorf("percent = %v, want 37.5", p)
		}
	})

	t.Run("done_plus_running", func(t *testing.T) {
		tr := newTwo()
		tr.Done("light", "")
		tr.Begin("heavy")
		tr.StepProgress("heavy", 50, "")
		// (1 + 1.5) / 4 = 62.5
		if p := tr.Snapshot().Percent; p != 62.5 {
			t.Errorf("percent = %v, want 62.5", p)
		}
	})

	t.Run("skipped_counts_full_weight", func(t *testing.T) {
		// skipped 是"这一步不需要做"，必须计满，否则正常启动永远到不了 100。
		tr := newTwo()
		tr.Skip("heavy", "no-op")
		if p := tr.Snapshot().Percent; p != 75 {
			t.Errorf("percent = %v, want 75 (skipped counts full weight)", p)
		}
		tr.Done("light", "")
		if p := tr.Snapshot().Percent; p != 100 {
			t.Errorf("percent = %v, want 100", p)
		}
	})

	t.Run("failed_counts_zero", func(t *testing.T) {
		tr := newTwo()
		tr.Done("light", "")
		tr.Fail("heavy", errors.New("boom"))
		if p := tr.Snapshot().Percent; p != 25 {
			t.Errorf("percent = %v, want 25 (failed counts 0)", p)
		}
	})
}

// TestPercentOnDefaultSteps 用真实权重表复核一次中间态。
func TestPercentOnDefaultSteps(t *testing.T) {
	tr := NewTracker(nil)
	tr.Done(StepDBConnect, "")            // 5
	tr.Done(StepAutoMigrate, "")          // 16
	tr.Begin(StepPartitionTelemetryFrame) // 10
	tr.StepProgress(StepPartitionTelemetryFrame, 40, "202608 第 2/7 批")
	// (5 + 16 + 10*0.4) / 100 = 25.0
	if p := tr.Snapshot().Percent; p != 25 {
		t.Errorf("percent = %v, want 25", p)
	}
	// 补齐剩余：全部 done/skip 后必须正好 100。
	tr.Done(StepPartitionTelemetryFrame, "")
	tr.Done(StepPartitionTelemetryFrame, "")
	tr.Skip(StepPartitionRollup, "non-pg")
	tr.Done(StepEnsurePartitions, "")
	tr.Done(StepParentIndexes, "")
	tr.Done(StepRetireDeadIndexes, "")
	tr.Done(StepWarmup, "")
	tr.Done(StepStartupTasks, "")
	snap := tr.Snapshot()
	if snap.Percent != 100 || !snap.Ready {
		t.Errorf("final percent/ready = %v/%v, want 100/true", snap.Percent, snap.Ready)
	}
}

// TestProgressClamped 守进度钳位与 label 语义。
func TestProgressClamped(t *testing.T) {
	tr := NewTracker([]Step{{ID: "s", Label: "S", Weight: 1}})
	tr.Begin("s")
	tr.StepProgress("s", 250, "over")
	if got := tr.Snapshot().Steps[0].Progress; got != 100 {
		t.Errorf("progress = %v, want clamped 100", got)
	}
	tr.StepProgress("s", -40, "under")
	if got := tr.Snapshot().Steps[0].Progress; got != 0 {
		t.Errorf("progress = %v, want clamped 0", got)
	}
	if p := tr.Snapshot().Percent; p != 0 {
		t.Errorf("percent = %v, want 0", p)
	}
}

// TestFailedState 守失败态（含 Fatal 前 tracker.Fail 的用法）。
func TestFailedState(t *testing.T) {
	tr := NewTracker(nil)
	// 用 partition_telemetry_frame 而不是原来的 partition_unified_data /
	// partition_device_data：两者都已随各自的退役移除（2026-11-08 / 2026-11-11）。
	// 这条用例测的是 **tracker 的失败态语义**，与具体步骤无关 —— 换一个仍然存在的
	// 步骤即可，不必为它保留一个已删除的常量。
	tr.Begin(StepPartitionTelemetryFrame)
	tr.Fail(StepPartitionTelemetryFrame, errors.New("row count mismatch"))

	snap := tr.Snapshot()
	if snap.Ready {
		t.Error("ready must be false on failed step")
	}
	if !snap.Failed {
		t.Error("failed must be true on failed step")
	}
	if !tr.Failed() || tr.Ready() {
		t.Errorf("Ready()/Failed() = %v/%v, want false/true", tr.Ready(), tr.Failed())
	}
	var st Step
	for _, s := range snap.Steps {
		if s.ID == StepPartitionTelemetryFrame {
			st = s
		}
	}
	if st.State != StepFailed {
		t.Errorf("state = %q, want failed", st.State)
	}
	if !strings.Contains(st.Error, "row count mismatch") {
		t.Errorf("error = %q, want it to carry the cause", st.Error)
	}
	if st.FinishedAt == nil || st.StartedAt == nil {
		t.Error("failed step must carry started_at and finished_at")
	}
	if st.Progress != 0 {
		t.Errorf("failed step progress = %v, want 0", st.Progress)
	}
	// Fail(nil) 不 panic，Error 保持空串。
	tr2 := NewTracker([]Step{{ID: "x", Weight: 1}})
	tr2.Fail("x", nil)
	if got := tr2.Snapshot().Steps[0].Error; got != "" {
		t.Errorf("Fail(nil) error = %q, want empty", got)
	}
}

// TestReadySemantics 钉住 Lead 裁决的 Ready 语义：全部 done|skipped ⇒ ready。
func TestReadySemantics(t *testing.T) {
	steps := []Step{{ID: "a", Weight: 1}, {ID: "b", Weight: 1}, {ID: "c", Weight: 1}}

	tr := NewTracker(steps)
	if tr.Ready() {
		t.Error("fresh tracker must not be ready")
	}
	tr.Done("a", "")
	tr.Done("b", "")
	if tr.Ready() {
		t.Error("one pending step ⇒ not ready")
	}
	tr.Skip("c", "not needed")
	if !tr.Ready() {
		t.Error("all done|skipped ⇒ ready (回归: 不得用『最后一个 step done』判据)")
	}
	if tr.Failed() {
		t.Error("skipped is not failed")
	}
	// Snapshot.Ready 必须与 Ready() 同口径。
	if !tr.Snapshot().Ready {
		t.Error("Snapshot().Ready must agree with Ready()")
	}
	if got := tr.Snapshot().Percent; got != 100 {
		t.Errorf("ready percent = %v, want 100", got)
	}

	// 空步骤表视为 ready（NewTracker 会回落 DefaultSteps，故直接构造零值场景需显式跳过）。
	if !(*Tracker)(nil).Ready() {
		t.Error("nil tracker must report ready (no startup work to wait for)")
	}
}

// TestSkipIsTerminal 守 skipped 是终态：迟到的 Done/Fail 不得改写。
func TestSkipIsTerminal(t *testing.T) {
	tr := NewTracker([]Step{{ID: "s", Weight: 1}})
	tr.Skip("s", "non-pg")
	tr.Done("s", "late done")
	tr.Fail("s", errors.New("late fail"))
	tr.StepProgress("s", 42, "late progress")
	snap := tr.Snapshot()
	if snap.Steps[0].State != StepSkipped {
		t.Errorf("state = %q, want skipped (terminal)", snap.Steps[0].State)
	}
	if snap.Steps[0].Progress != 100 {
		t.Errorf("progress = %v, want 100", snap.Steps[0].Progress)
	}
}

// TestLateCallbackAfterDone 守 done 之后迟到的 StepProgress 不得把状态打回 running。
// 埋点是"每批上报"，与 Done 之间存在天然竞态（main 在函数返回后立刻 Done，
// 而 datalifecycle 的最后一批回调可能刚好晚一拍）。
func TestLateCallbackAfterDone(t *testing.T) {
	tr := NewTracker([]Step{{ID: "s", Weight: 1}})
	tr.Begin("s")
	tr.Done("s", "完成 1000 行")
	tr.StepProgress("s", 10, "迟到的回调")
	snap := tr.Snapshot()
	if snap.Steps[0].State != StepDone {
		t.Errorf("state = %q, want done", snap.Steps[0].State)
	}
	if snap.Steps[0].Progress != 100 {
		t.Errorf("progress = %v, want 100", snap.Steps[0].Progress)
	}
	if p := snap.Percent; p != 100 {
		t.Errorf("percent = %v, want 100", p)
	}
}

// TestUnknownIDIgnored 守未知 step ID 静默忽略（埋点来自表名映射，多一张表不该炸启动）。
func TestUnknownIDIgnored(t *testing.T) {
	tr := NewTracker([]Step{{ID: "known", Weight: 1}})
	tr.Begin("nope")
	tr.StepProgress("nope", 50, "x")
	tr.Done("nope", "x")
	tr.Fail("nope", errors.New("x"))
	tr.Skip("nope", "x")
	snap := tr.Snapshot()
	if len(snap.Steps) != 1 || snap.Steps[0].State != StepPending {
		t.Errorf("unknown step leaked into state: %+v", snap.Steps)
	}
	// nil Tracker 的所有方法必须 nil 安全（main.go 里 tracker 可能因开关为 nil）。
	var nilTr *Tracker
	nilTr.Begin("x")
	nilTr.StepProgress("x", 1, "")
	nilTr.Done("x", "")
	nilTr.Fail("x", nil)
	nilTr.Skip("x", "")
	nilTr.SetBuildCommit("abc")
	nilTr.SetVersion("v")
	_ = nilTr.Snapshot()
}

// TestSnapshotDeepCopy 守深拷贝：改返回值（含指针指向的时间值）不影响内部状态。
func TestSnapshotDeepCopy(t *testing.T) {
	tr := NewTracker([]Step{{ID: "s", Label: "S", Weight: 1}})
	tr.Begin("s")
	tr.StepProgress("s", 30, "第一批")

	snap1 := tr.Snapshot()
	snap1.Steps[0].State = StepFailed
	snap1.Steps[0].Progress = 999
	snap1.Steps[0].Detail = "篡改"
	snap1.Steps[0].ID = "篡改"
	if snap1.Steps[0].StartedAt != nil {
		*snap1.Steps[0].StartedAt = time.Unix(0, 0) // 改指针指向的值
	}
	snap1.Percent = -1
	snap1.Ready = true

	snap2 := tr.Snapshot()
	if snap2.Steps[0].State != StepRunning || snap2.Steps[0].Progress != 30 {
		t.Errorf("mutating snapshot leaked into tracker: %+v", snap2.Steps[0])
	}
	if snap2.Steps[0].Detail != "第一批" || snap2.Steps[0].ID != "s" {
		t.Errorf("snapshot strings leaked: %+v", snap2.Steps[0])
	}
	if snap2.Steps[0].StartedAt != nil && snap2.Steps[0].StartedAt.Unix() == 0 {
		t.Error("snapshot *time.Time must be deep-copied (pointer aliased)")
	}
	if snap2.Percent == -1 || snap2.Ready {
		t.Errorf("snapshot scalar fields leaked: percent=%v ready=%v", snap2.Percent, snap2.Ready)
	}
	// 两次快照的 Steps 必须是不同底层数组。
	if &snap1.Steps[0] == &snap2.Steps[0] {
		t.Error("Snapshot must return a fresh Steps slice")
	}
}

// TestSetBuildCommitAndVersion 守构建来源/版本注入（空串不覆盖）。
func TestSetBuildCommitAndVersion(t *testing.T) {
	tr := NewTracker(nil)
	if got := tr.Snapshot().Version; got != DefaultVersion {
		t.Errorf("default version = %q, want %q", got, DefaultVersion)
	}
	if got := tr.Snapshot().BuildCommit; got != "" {
		t.Errorf("default commit = %q, want empty", got)
	}
	tr.SetBuildCommit("deadbeef")
	tr.SetBuildCommit("") // 空串不得擦掉已有值（与 api.SetBuildCommit 同语义）
	if got := tr.Snapshot().BuildCommit; got != "deadbeef" {
		t.Errorf("commit = %q, want deadbeef (empty must not overwrite)", got)
	}
	tr.SetVersion("v2.0.1")
	if got := tr.Snapshot().Version; got != "v2.0.1" {
		t.Errorf("version = %q, want v2.0.1", got)
	}
	tr.SetVersion("")
	if got := tr.Snapshot().Version; got != "v2.0.1" {
		t.Errorf("version = %q, want v2.0.1 (empty must not overwrite)", got)
	}
}

// TestSnapshotJSONContract 守 JSON 字段名与契约 §3.1 逐字一致（前端与 api 都依赖）。
func TestSnapshotJSONContract(t *testing.T) {
	tr := NewTracker(nil)
	tr.SetBuildCommit("cafe")
	tr.Begin(StepDBConnect)
	tr.StepProgress(StepDBConnect, 10, "dial")
	tr.Done(StepDBConnect, "ok")

	raw, err := json.Marshal(tr.Snapshot())
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	for _, want := range []string{
		`"ready"`, `"failed"`, `"percent"`, `"current_step"`, `"current_label"`,
		`"started_at"`, `"elapsed_ms"`, `"steps"`, `"version"`, `"build_commit"`,
		`"id"`, `"label"`, `"weight"`, `"state"`, `"progress"`, `"detail"`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("snapshot JSON missing key %s: %s", want, raw)
		}
	}
	// omitempty 字段在空值时不得出现（build_commit 有值，故单独验 error 缺席）。
	if strings.Contains(string(raw), `"error"`) {
		t.Errorf("empty error must be omitted: %s", raw)
	}
}

// TestElapsedAndCurrentStep 守耗时与"当前阶段"语义。
func TestElapsedAndCurrentStep(t *testing.T) {
	tr := NewTracker(nil)
	snap := tr.Snapshot()
	if snap.ElapsedMS < 0 {
		t.Errorf("elapsed_ms = %d, want >= 0", snap.ElapsedMS)
	}
	// 无 running 时 current_step 指向第一个 pending 步骤（启动间隙点亮下一步）。
	if snap.CurrentStep != StepDBConnect || snap.CurrentLabel != "连接数据库" {
		t.Errorf("current_step/label = %q/%q, want %q/连接数据库",
			snap.CurrentStep, snap.CurrentLabel, StepDBConnect)
	}
	// 有 running 时优先 running。
	tr.Begin(StepParentIndexes)
	if got := tr.Snapshot().CurrentStep; got != StepParentIndexes {
		t.Errorf("current_step = %q, want %q (running wins)", got, StepParentIndexes)
	}
	// 全部完成后为空。
	for _, s := range tr.Snapshot().Steps {
		tr.Done(s.ID, "")
	}
	snap = tr.Snapshot()
	if snap.CurrentStep != "" || snap.CurrentLabel != "" {
		t.Errorf("current_step/label = %q/%q, want empty when ready", snap.CurrentStep, snap.CurrentLabel)
	}
}

// TestNewTrackerStartedAt 守 StartedAt 有值且不随时钟注入漂移。
func TestNewTrackerStartedAt(t *testing.T) {
	tr := NewTracker(nil)
	snap := tr.Snapshot()
	if snap.StartedAt.IsZero() {
		t.Error("StartedAt must be set at construction")
	}
	tr.SetClock(func() time.Time { return snap.StartedAt.Add(2 * time.Second) })
	if got := tr.Snapshot().ElapsedMS; got != 2000 {
		t.Errorf("elapsed_ms = %d, want 2000", got)
	}
}

// TestConcurrentSafety 并发压测（-race 下跑才算数）：
// 多 goroutine 同时 Begin/StepProgress/Done/Skip/Fail/Snapshot/Ready/Failed。
func TestConcurrentSafety(t *testing.T) {
	tr := NewTracker(DefaultSteps())
	ids := make([]string, 0, len(DefaultSteps()))
	for _, s := range DefaultSteps() {
		ids = append(ids, s.ID)
	}

	const writers = 8
	const iters = 200
	var wg sync.WaitGroup
	wg.Add(writers + 2)
	for w := 0; w < writers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				id := ids[(w+i)%len(ids)]
				tr.Begin(id)
				tr.StepProgress(id, float64((i*7)%101), "batch")
			}
		}(w)
	}
	// 读者：快照/Ready/Failed 并发跑，验证不会被写者撕裂（race detector 判）。
	for r := 0; r < 2; r++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				snap := tr.Snapshot()
				if snap.Percent < 0 || snap.Percent > 100 {
					t.Errorf("percent out of range: %v", snap.Percent)
					return
				}
				for _, s := range snap.Steps {
					if s.Progress < 0 || s.Progress > 100 {
						t.Errorf("step %s progress out of range: %v", s.ID, s.Progress)
						return
					}
				}
				_ = tr.Ready()
				_ = tr.Failed()
			}
		}()
	}
	wg.Wait()

	// 收尾：全部 done 后必须 ready 且 100。
	for _, id := range ids {
		tr.Done(id, "")
	}
	if !tr.Ready() {
		t.Error("tracker must be ready after all steps done")
	}
	if got := tr.Snapshot().Percent; got != 100 {
		t.Errorf("percent = %v, want 100", got)
	}
}

// TestPercentMonotonicWhileRunning 守"建索引期间进度条是动的"（Lead 硬要求）：
// 单步 running 且 Progress 单调递增时，总体 Percent 必须单调不减。
func TestPercentMonotonicWhileRunning(t *testing.T) {
	tr := NewTracker(nil)
	tr.Begin(StepParentIndexes)
	prev := tr.Snapshot().Percent
	for pct := 0.0; pct <= 100; pct += 5 {
		tr.StepProgress(StepParentIndexes, pct, "creating index")
		got := tr.Snapshot().Percent
		if got < prev {
			t.Fatalf("percent went backwards: %v -> %v at step pct %v", prev, got, pct)
		}
		prev = got
	}
	if prev <= 0 {
		t.Error("running step with progress must move the overall percent")
	}
}
