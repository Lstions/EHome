package backfill

// runner.go — 回填引擎主体：分区发现 → 按帧分页 → 编码 → 落库 → 断点推进。
//
// ============ 整体流程（契约 §4.4）============
//
//	启动完成（bootstrap 第 11 步之后）→ 后台 goroutine：
//	  1. 发现 unified_data 的全部分区（月）
//	  2. 每个分区的状态行取 telemetry_backfill_progress（没有则建 pending）
//	  3. 按 (watermark, partHi) 范围分页扫帧 → 编码 → 落库 → 推进 watermark
//	  4. 全部 done ⇒ State=done
//
// ============ 三个必须理解的实现决策 ============
//
// **决策 1：一次取「整帧」而不是「一行」（契约 §4.4 step a/b）**
//
// unified_data 一行是一个**字段**。如果逐行编码，每行都会变成一帧 ⇒ 帧数放大
// 29 倍、压缩率归零、且每帧只有一个字段（变化抑制完全失效）。
// 因此两步走：
//
//	① 先取本批的**帧键**：(device_id, timestamp) 有序分页；
//	② 再按 [minTS, maxTS] 一次性取回这些帧的**全部行**，在内存里按
//	   (device_id, timestamp) 分组。
//
// 为什么不在循环里逐帧查（`WHERE device_id=? AND timestamp=?`）：2000 帧就是
// 2000 次往返，342 批 = 68 万次查询。②把每批压到 1 次范围查询，代价是可能多取
// 少量同 ts 的行（同一毫秒内多设备），那点开销远小于 68 万次往返。
//
// **决策 2：watermark 必须落在「帧边界」上（断点续跑的命门）**
//
// 如果一批在某个 timestamp 中间截断（部分设备的帧进了本批、其余没进），
// 而 watermark 记成该 ts，那么恢复时用 `timestamp > watermark` 会把没处理完的
// 那些帧**永久跳过** —— 静默丢数据，且完整性校验（帧数不等）要跑完整个分区
// 才会发现。
//
// 因此取帧键时多取 1 条做前瞻：若第 batch+1 条与第 batch 条**同一 ts**，
// 就把最后一个 ts 整体让给下一批。这样 watermark 恒为一个干净的 ts 边界。
// （病态情况：整批都是同一个 ts ⇒ 让让会让本轮零进展，此时放弃让位直接处理，
// 见 truncateAtCleanBoundary 的注释。）
//
// **决策 3：每批重置编码器（不是性能优化，是内存正确性问题）**
//
// telemetry.Encoder 内部有两张按帧记账的 map：
//
//	states: map[(device,signature)]*slotState   —— 有界（slot 数 ≤ 约 7.4k）
//	seqs:   map[(device,ts)]int16               —— **无界**（每帧一个 entry）
//
// 3.42M 帧全量跑完，seqs 会累计 3.42M 个 entry（Go map 约 160 MB，且永不释放）。
// 每批 Reset 把它钉在 BatchFrames 量级。
//
// 代价：每批首帧对每个 slot 变成关键帧。342 批 × 约 35 B ≈ 12 KB，
// 相对 463 MB 可忽略。**收益是内存有界**，这在 40 分钟的长任务里比 12 KB 重要。
//
// 注意 Reset 不影响正确性：关键帧是解码的绝对锚点，多几个只会更容易解码。

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/telemetry"
)

// 回填 State（与 datalifecycle.Backfill* 常量对齐，进度接口直接用）。
const (
	// StateIdleRunner 未开始（Runner 构造了但还没跑）。
	StateIdle = "idle"
	// StateRunning 正在回填。
	StateRunning = "running"
	// StateDone 全部完成。
	StateDone = "done"
	// StateFailed 上次失败（可重试，断点保留）。
	StateFailed = "failed"
	// StateDisabled 被环境变量关掉（运维应急阀）。
	StateDisabled = "disabled"
)

// Progress 是回填进度快照（契约 §4.6 的接口数据源）。
type Progress struct {
	// State 见 State* 常量。
	State string `json:"state"`
	// CurrentPartition 当前正在回填的分区名。
	CurrentPartition string `json:"current_partition,omitempty"`
	// Percent 0..100。按**分区时间跨度**加权（O(1)，单调），不依赖帧总数。
	Percent float64 `json:"percent"`
	// FramesDone 已回填帧数（跨全部分区累计）。
	FramesDone int64 `json:"frames_done"`
	// FramesTotal 总帧数；0 表示尚未知（见 Runner 的说明：精确总数要全表
	// count(DISTINCT)，成本高且对进度无必要）。
	FramesTotal int64 `json:"frames_total,omitempty"`
	// RowsDone 已消费的 unified_data 行数（对账用）。
	RowsDone int64 `json:"rows_done"`
	// ETASeconds 剩余秒数估算（按本进程实测速率；0 = 未知）。
	ETASeconds int64 `json:"eta_seconds,omitempty"`
	// Errors 运行中的非致命问题（配置非法、单帧编码失败等）。
	Errors []string `json:"errors,omitempty"`
	// Partitions 每个分区的明细（运维排查"卡在哪个月"）。
	Partitions []PartitionProgress `json:"partitions,omitempty"`

	// RetentionPermanent 反映当前保留期配置是否为「永久保留」
	// （datalifecycle.IsRetentionPermanent()）。
	//
	// 为什么要暴露它：回填完成后，老 unified_data 分区是否 DROP 是**人工决策**
	// （契约 §7 裁决 2：先迁移 → 验证 1 周 → 再 DROP）。而永久保留模式下，
	// 「删老数据」直接违背用户配置 —— 运维在按钮旁边必须能看到这一点，否则会在
	// 不知道后果的情况下释放 15 GB。
	//
	// ⚠ 回填引擎**从不自动 DROP 任何老数据**（本包没有一条 DELETE/DROP 语句，
	// 由 TestBackfillNeverDeletesSourceData 的源码扫描守着）。本字段只是把
	// 「能不能删」的事实交给上层，删除动作永远由人触发。
	RetentionPermanent bool `json:"retention_permanent"`
}

// PartitionProgress 是单个分区的进度。
type PartitionProgress struct {
	Name       string     `json:"name"`
	State      string     `json:"state"`
	Watermark  *time.Time `json:"watermark,omitempty"`
	FramesDone int64      `json:"frames_done"`
	RowsDone   int64      `json:"rows_done"`
	Error      string     `json:"error,omitempty"`
}

// Runner 是老数据回填引擎。
//
// 并发安全：RunOnce 与 Progress 可从不同 goroutine 调用（Progress 用 mutex 保护
// 快照）。RunOnce 自身**不可重入**（同一 Runner 同时跑两遍没有意义且会互相
// 推进 watermark），调用方（main.go）只启动一个 goroutine。
type Runner struct {
	db   *gorm.DB
	dict telemetry.SlotDictionary
	enc  *telemetry.Encoder
	cfg  Config

	mu       sync.Mutex
	progress Progress
	parts    []partition

	startedAt time.Time
	// startedFramesAt 是 ETA 的基准：进入 running 时的 FramesDone 与时刻。
	startedFramesAt int64
	// startedRowsAt 同上（行数，用于按行速率估算时间进度）。
	startedRowsAt int64
	// disabled 由 ConfigFromEnv 的 enabled 决定。
	disabled bool
	// initWarnings 是构造期的配置告警（进 Progress.Errors）。
	initWarnings []string

	// testHookAfterBatch 是**测试专用**的批后钩子（生产恒 nil）。
	//
	// 为什么需要：契约 §6.3 要求验证「中途 kill 进程，重启从 watermark 继续，
	// 不重复不遗漏」。而真实打断（kill -9 / ctx 超时）与回填速度存在竞态 ——
	// 数据量小时回填比取消更快，用例会**假绿**（第一轮就跑完了，续跑根本没发生）。
	// 这个钩子让「在第 N 批之后取消」成为确定性事件，用例才有鉴别力。
	//
	// 它是字段而不是配置项：绝不能让运维/环境变量碰到（那不是产品能力）。
	testHookAfterBatch func(batch int) error
}

// NewRunner 构造回填引擎。
//
// dict 必须是**已有 slot 字典**（生产用 telemetry.NewDBSlots(db) + WarmUp，
// 与线上写路径共用同一份，保证回填出的 slot_id 与线上一致）。
//
// disabled=true 时 RunOnce 立即返回 State=disabled（不碰数据库）。
func NewRunner(db *gorm.DB, dict telemetry.SlotDictionary, cfg Config, disabled bool, warnings []string) *Runner {
	cfg = cfg.normalize()
	r := &Runner{
		db:           db,
		dict:         dict,
		cfg:          cfg,
		disabled:     disabled,
		initWarnings: warnings,
	}
	r.progress = Progress{State: StateIdle}
	if disabled {
		r.progress.State = StateDisabled
	}
	if len(warnings) > 0 {
		r.progress.Errors = append(r.progress.Errors, warnings...)
	}
	if db != nil && dict != nil {
		r.enc = telemetry.NewEncoder(telemetry.EncoderConfig{
			KeyFrameInterval: cfg.KeyFrameInterval,
		}, dict)
	}
	return r
}

// Progress 返回当前进度快照（深拷贝，调用方可随意改）。
func (r *Runner) Progress() Progress {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.progress
	// 每次取快照时重读：保留期配置运行期可改（SetSystemRetentionDays）。
	out.RetentionPermanent = datalifecycle.IsRetentionPermanent()
	out.Errors = append([]string(nil), r.progress.Errors...)
	out.Partitions = append([]PartitionProgress(nil), r.progress.Partitions...)
	return out
}

// Enabled 报告回填是否可用（nil Runner / 被关闭 / 未配置都返回 false）。
func (r *Runner) Enabled() bool {
	return r != nil && !r.disabled && r.db != nil && r.enc != nil
}

// RunOnce 处理全部待回填分区，直到完成或 ctx 取消；可安全重入。
//
// 返回最终进度快照。ctx 取消返回 ctx.Err()，但**已完成的分区与 watermark 都
// 已落库** —— 这就是断点续跑的基础。
func (r *Runner) RunOnce(ctx context.Context) (Progress, error) {
	if r == nil || r.disabled {
		return r.Progress(), nil
	}
	if r.db == nil || r.enc == nil || r.dict == nil {
		return r.Progress(), fmt.Errorf("backfill: runner not properly initialised (db/dict/encoder)")
	}

	parts, err := r.discoverPartitions(ctx)
	if err != nil {
		r.setFailed(err)
		return r.Progress(), err
	}
	r.mu.Lock()
	r.parts = parts
	r.startedAt = time.Now()
	r.progress.State = StateRunning
	r.startedFramesAt = r.progress.FramesDone
	r.startedRowsAt = r.progress.RowsDone
	r.mu.Unlock()

	if len(parts) == 0 {
		r.mu.Lock()
		r.progress.State = StateDone
		r.progress.Percent = 100
		r.mu.Unlock()
		return r.Progress(), nil
	}

	if err := r.reloadProgressRows(ctx, parts); err != nil {
		r.setFailed(err)
		return r.Progress(), err
	}

	for _, part := range parts {
		if err := ctx.Err(); err != nil {
			// 取消不是失败：已提交的分区是 done，watermark 已落库。
			r.mu.Lock()
			r.progress.State = StateIdle
			r.mu.Unlock()
			return r.Progress(), err
		}
		row, err := r.loadProgressRow(ctx, part.name)
		if err != nil {
			r.setFailed(err)
			return r.Progress(), err
		}
		if row.State == datalifecycle.BackfillDone {
			continue
		}
		r.setCurrentPartition(part.name)
		if err := r.backfillPartition(ctx, part); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				r.mu.Lock()
				r.progress.State = StateIdle
				r.mu.Unlock()
				return r.Progress(), ctxErr
			}
			r.markPartitionFailed(ctx, part.name, err)
			r.setFailed(err)
			return r.Progress(), err
		}
	}

	r.mu.Lock()
	r.progress.State = StateDone
	r.progress.Percent = 100
	r.progress.CurrentPartition = ""
	r.mu.Unlock()
	return r.Progress(), nil
}

// setCurrentPartition 更新当前分区并重算百分比。
func (r *Runner) setCurrentPartition(name string) {
	r.mu.Lock()
	r.progress.CurrentPartition = name
	r.mu.Unlock()
	r.recomputePercent()
}

// setFailed 记录失败状态。
func (r *Runner) setFailed(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.progress.State = StateFailed
	msg := err.Error()
	// 只保留最近 20 条：错误列表是给人看的，不是日志流。
	r.progress.Errors = append(r.progress.Errors, msg)
	if len(r.progress.Errors) > 20 {
		r.progress.Errors = r.progress.Errors[len(r.progress.Errors)-20:]
	}
}

// addError 追加一条非致命错误。
func (r *Runner) addError(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	slog.Warn("backfill: " + msg)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.progress.Errors = append(r.progress.Errors, msg)
	if len(r.progress.Errors) > 20 {
		r.progress.Errors = r.progress.Errors[len(r.progress.Errors)-20:]
	}
}
