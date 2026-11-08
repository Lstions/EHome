package bootstrap

// tracker.go — 启动进度状态机（契约 §3.1）。
//
// 设计要点（都是为了"能被并发读、且读到的是一致快照"）：
//   - 全部导出方法走同一把 sync.RWMutex：写少（启动期各阶段）读多（HTTP 轮询）；
//   - Snapshot 返回**深拷贝**：调用方（HTTP handler 序列化、测试）改动返回值
//     绝不能回写内部状态，否则一次 JSON 序列化就能污染进度；
//   - Percent 按 Weight 加权：done/skipped 计满权重、running 按 Progress 折算、
//     pending/failed 计 0。skipped 计满是刻意的——它是"这一步不需要做"，
//     若计 0，正常启动（跳过某些步骤）会永远停在 <100%。

import (
	"sync"
	"time"
)

// StepState 是单个启动步骤的状态字面量（与前端契约同域）。
type StepState string

const (
	StepPending StepState = "pending"
	StepRunning StepState = "running"
	StepDone    StepState = "done"
	StepFailed  StepState = "failed"
	StepSkipped StepState = "skipped"
)

// Step 是单个启动步骤的可变状态（JSON tag 与契约 §3.1 逐字一致）。
type Step struct {
	ID         string     `json:"id"`     // 稳定标识，前端可依赖
	Label      string     `json:"label"`  // 中文展示名
	Weight     int        `json:"weight"` // 进度权重（相对值，不必和为 100）
	State      StepState  `json:"state"`
	Progress   float64    `json:"progress"` // 步骤内 0..100
	Detail     string     `json:"detail,omitempty"`
	Error      string     `json:"error,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// Snapshot 是 GET /api/v1/system/maintenance 的 data 载荷（契约 §3.1）。
type Snapshot struct {
	Ready        bool      `json:"ready"`
	Failed       bool      `json:"failed"`
	Percent      float64   `json:"percent"` // 0..100，按 Weight 加权
	CurrentStep  string    `json:"current_step"`
	CurrentLabel string    `json:"current_label"`
	StartedAt    time.Time `json:"started_at"`
	ElapsedMS    int64     `json:"elapsed_ms"`
	Steps        []Step    `json:"steps"`
	Version      string    `json:"version"`
	BuildCommit  string    `json:"build_commit,omitempty"`
}

// Tracker 是并发安全的启动进度跟踪器。
type Tracker struct {
	mu sync.RWMutex

	steps   []Step
	index   map[string]int
	started time.Time
	version string
	commit  string

	// now 可注入（测试用），生产用 time.Now。
	now func() time.Time
}

// NewTracker 用给定步骤构造 Tracker。steps 为空时回落 DefaultSteps()。
// 传入的切片会被**复制**，调用方之后改动它不影响 Tracker。
func NewTracker(steps []Step) *Tracker {
	if len(steps) == 0 {
		steps = DefaultSteps()
	}
	cp := make([]Step, len(steps))
	copy(cp, steps)
	idx := make(map[string]int, len(cp))
	for i := range cp {
		// 构造期统一归零可变字段：调用方可能复用了上一次的 Step 值。
		cp[i].State = StepPending
		cp[i].Progress = 0
		cp[i].Detail = ""
		cp[i].Error = ""
		cp[i].StartedAt = nil
		cp[i].FinishedAt = nil
		if _, dup := idx[cp[i].ID]; !dup && cp[i].ID != "" {
			idx[cp[i].ID] = i
		}
	}
	t := &Tracker{
		steps:   cp,
		index:   idx,
		started: time.Now(),
		version: DefaultVersion,
		now:     time.Now,
	}
	return t
}

// SetClock 注入时钟（测试用）；nil 忽略。
func (t *Tracker) SetClock(now func() time.Time) {
	if t == nil || now == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.now = now
}

// SetVersion 注入版本号（快照回显）。空串忽略。
func (t *Tracker) SetVersion(v string) {
	if t == nil || v == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.version = v
}

// SetBuildCommit 注入构建来源（快照回显）。空串忽略——与 api.SetBuildCommit
// 同一语义：未注入时不得把来源擦成空。
func (t *Tracker) SetBuildCommit(c string) {
	if t == nil || c == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.commit = c
}

// Begin 把步骤置为 running（幂等：已 done/skipped 的步骤不会被回退）。
// 未登记的 id 静默忽略——埋点来自 datalifecycle 的表名映射，
// 多一张未登记表不该让启动失败。
func (t *Tracker) Begin(id string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	i, ok := t.index[id]
	if !ok {
		return
	}
	st := &t.steps[i]
	if st.State == StepDone || st.State == StepSkipped {
		return
	}
	now := t.now()
	if st.StartedAt == nil {
		st.StartedAt = &now
	}
	st.State = StepRunning
	st.Error = ""
	st.FinishedAt = nil
}

// StepProgress 更新步骤内进度（0..100，钳位）与 detail。
// 只对 running 的步骤生效：done/skipped 之后迟到的回调不得把状态打回去
// （埋点走的是"每批上报"，与 Done 之间存在天然的竞态）。
func (t *Tracker) StepProgress(id string, pct float64, detail string) {
	if t == nil {
		return
	}
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	i, ok := t.index[id]
	if !ok {
		return
	}
	st := &t.steps[i]
	if st.State == StepDone || st.State == StepSkipped {
		return
	}
	if st.State == StepPending {
		now := t.now()
		if st.StartedAt == nil {
			st.StartedAt = &now
		}
		st.State = StepRunning
	}
	st.Progress = pct
	if detail != "" {
		st.Detail = detail
	}
}

// Done 把步骤置为完成（Progress=100, detail 覆盖）。已 skipped 的步骤保持 skipped。
func (t *Tracker) Done(id, detail string) {
	t.finish(id, StepDone, detail, "")
}

// Fail 把步骤置为失败并记录错误（err 为 nil 时只置状态）。
func (t *Tracker) Fail(id string, err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	t.finish(id, StepFailed, "", msg)
}

// Skip 把步骤置为跳过（计满权重，避免正常启动永远到不了 100%）。
func (t *Tracker) Skip(id, detail string) {
	t.finish(id, StepSkipped, detail, "")
}

func (t *Tracker) finish(id string, state StepState, detail, errMsg string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	i, ok := t.index[id]
	if !ok {
		return
	}
	st := &t.steps[i]
	if st.State == StepSkipped {
		return // 跳过是终态：迟到的 Done/Fail 不得改写
	}
	now := t.now()
	if st.StartedAt == nil {
		st.StartedAt = &now
	}
	st.FinishedAt = &now
	st.State = state
	st.Progress = 100
	if state == StepFailed {
		st.Error = errMsg
		st.Progress = 0
	} else {
		st.Error = ""
	}
	if detail != "" {
		st.Detail = detail
	}
}

// Ready 报告所有步骤是否都是 done/skipped（空步骤表视为 ready）。
// 语义由 Lead 裁决：**不要**用"最后一个 step done"之类的判据——
// 步骤顺序会变，且 skipped 也是合法终态。
func (t *Tracker) Ready() bool {
	if t == nil {
		return true
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	for i := range t.steps {
		switch t.steps[i].State {
		case StepDone, StepSkipped:
		default:
			return false
		}
	}
	return true
}

// Failed 报告是否有任一步骤失败。
func (t *Tracker) Failed() bool {
	if t == nil {
		return false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	for i := range t.steps {
		if t.steps[i].State == StepFailed {
			return true
		}
	}
	return false
}

// Snapshot 返回当前进度的**深拷贝**。
func (t *Tracker) Snapshot() Snapshot {
	if t == nil {
		return Snapshot{Ready: true, Version: DefaultVersion, Steps: []Step{}}
	}
	t.mu.RLock()
	defer t.mu.RUnlock()

	steps := make([]Step, len(t.steps))
	copy(steps, t.steps)
	// Step 里的 StartedAt/FinishedAt 是指针，必须逐个复制指向的时间值。
	for i := range steps {
		steps[i].StartedAt = cloneTimePtr(steps[i].StartedAt)
		steps[i].FinishedAt = cloneTimePtr(steps[i].FinishedAt)
	}

	// 耗时钳到 >= 0：注入时钟（测试）或系统时钟回拨都可能让 now < started，
	// 前端拿到负的 elapsed_ms 会显示成乱码时长。
	elapsed := t.now().Sub(t.started).Milliseconds()
	if elapsed < 0 {
		elapsed = 0
	}
	snap := Snapshot{
		Percent:     percentOf(steps),
		StartedAt:   t.started,
		ElapsedMS:   elapsed,
		Steps:       steps,
		Version:     t.version,
		BuildCommit: t.commit,
	}
	// Ready/Failed 必须与 Ready()/Failed() 同口径，直接从同一份拷贝推导，
	// 避免"两次加锁之间状态已变"的自相矛盾快照。
	snap.Ready, snap.Failed = true, false
	for i := range steps {
		switch steps[i].State {
		case StepDone, StepSkipped:
		case StepFailed:
			snap.Ready = false
			snap.Failed = true
		default:
			snap.Ready = false
		}
	}
	// current_step 分两趟：**running 优先**（启动间隙里前一步刚 done、后一步尚未
	// Begin 时，如果先命中 pending 就会点亮错误的阶段）。
	for i := range steps {
		if steps[i].State == StepRunning {
			snap.CurrentStep = steps[i].ID
			snap.CurrentLabel = steps[i].Label
			break
		}
	}
	if snap.CurrentStep == "" {
		// 无 running 时用"第一个未完成（pending/failed）步骤"当当前阶段：
		// 步骤之间的间隙里前端仍需要点亮下一步，否则进度条文案会空掉。
		for i := range steps {
			if steps[i].State == StepPending || steps[i].State == StepFailed {
				snap.CurrentStep = steps[i].ID
				snap.CurrentLabel = steps[i].Label
				break
			}
		}
	}
	if snap.Ready {
		snap.CurrentStep = ""
		snap.CurrentLabel = ""
		snap.Percent = 100
	}
	return snap
}

// percentOf 按 Weight 加权计算总体百分比（0..100，保留 1 位小数）。
func percentOf(steps []Step) float64 {
	total := 0
	for i := range steps {
		total += steps[i].Weight
	}
	if total <= 0 {
		// 无权重信息时退化为"按步骤数平均"，避免除零把进度打成 NaN。
		if len(steps) == 0 {
			return 100
		}
		done := 0
		for i := range steps {
			if steps[i].State == StepDone || steps[i].State == StepSkipped {
				done++
			}
		}
		return clampPercent(float64(done) / float64(len(steps)) * 100)
	}
	acc := 0.0
	for i := range steps {
		w := float64(steps[i].Weight)
		switch steps[i].State {
		case StepDone, StepSkipped:
			acc += w
		case StepRunning:
			p := steps[i].Progress
			if p < 0 {
				p = 0
			}
			if p > 100 {
				p = 100
			}
			acc += w * p / 100
		}
	}
	return clampPercent(acc / float64(total) * 100)
}

func clampPercent(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	// round 到 0.1：前端进度条不需要更高精度，且能避免浮点噪声（如 66.69999）。
	return float64(int64(v*10+0.5)) / 10
}

func cloneTimePtr(p *time.Time) *time.Time {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
