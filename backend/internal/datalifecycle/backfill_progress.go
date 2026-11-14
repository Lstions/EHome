package datalifecycle

// backfill_state.go（原 backfill_progress.go）— 回填状态机常量。
//
// 2026-11-13：unified_data 整表退役，本文件原有的
//   · BackfillProgressTable（telemetry_backfill_progress）
//   · BackfillProgressRow（断点续跑状态行）
//   · EnsureBackfillProgressTable（AutoMigrate）
// 已一并删除 —— 老数据回填（unified_data → telemetry_frame）没有源表、也没有
// 待迁移的部署，留着一张"永远 pending"的状态表只会让运维误以为还有任务在跑。
//
// 下面四个常量仍被 L3 rollup 回填（rollup_backfill.go 的覆盖元数据）复用：
// pending/running/done/failed 是"一次可中断长任务"的通用状态机，与数据源无关。

// 回填状态机取值。
const (
	// BackfillPending 尚未开始（或上次还没跑到）。
	BackfillPending = "pending"
	// BackfillRunning 正在回填。进程崩溃留下的 running 是可续跑的（从 watermark 继续），
	// 不是错误态 —— 这正是断点续跑依赖的性质。
	BackfillRunning = "running"
	// BackfillDone 该分区已回填完成（可重复运行，幂等）。
	BackfillDone = "done"
	// BackfillFailed 上次失败（保留 watermark，下次从断点重试）。
	BackfillFailed = "failed"
)
