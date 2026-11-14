package api

// backfill_state.go — 回填状态字面量。
//
// 2026-11-13：unified_data 整表退役，老数据回填（unified_data → telemetry_frame）
// 的 HTTP 端点、快照类型与引擎一并删除。下面这组字面量仍被 **L3 rollup 回填**
// 的状态端点复用（rollup_backfill_status.go）——语义相同：一次可中断长任务的
// 四态 + 被开关关闭态。跨包一致性（与 datalifecycle.Backfill* 常量）由
// rollup backfill 的既有测试守着。
const (
	// BackfillStateIdle 未开始（含"根本没有回填引擎"这一情形）。
	BackfillStateIdle = "idle"
	// BackfillStateRunning 正在回填。
	BackfillStateRunning = "running"
	// BackfillStateDone 全部完成。
	BackfillStateDone = "done"
	// BackfillStateFailed 上次失败（断点保留，可重试）。
	BackfillStateFailed = "failed"
	// BackfillStateDisabled 被环境变量关闭（运维应急阀）。
	BackfillStateDisabled = "disabled"
)
