package bootstrap

// steps.go — 启动步骤清单（契约 docs/设计/启动期数据库优化与进度展示-契约-2026-11-06.md §3.1）。
//
// DefaultSteps 的 ID / Label / Weight 必须与契约表**逐字一致**：
//   · ID 是前端与 api 共同依赖的稳定标识，改名等于静默破坏两边的覆盖；
//   · Weight 合计必须为 100（有 TestDefaultStepsTable 守着），Percent 才有 0..100 语义。

// 步骤 ID 常量。用常量而不是散落的字面量：datalifecycle 埋点、api 适配器、
// 前端假设都指向同一组字符串，任何一处拼错都应该是编译期错误而不是运行期空进度。
const (
	StepDBConnect           = "db_connect"
	StepAutoMigrate         = "auto_migrate"
	StepPartitionDeviceData = "partition_device_data"
	StepPartitionRollup      = "partition_rollup"
	// StepPartitionTelemetryFrame 是 L2 帧表（+ slot 字典表）的分区迁移。
	//
	// 为什么单独一步而不是并进 StepPartitionRollup: 两者失败语义相反 ——
	// 帧表失败 **Fatal**（L2 是主写入路径，缺表 = 静默丢帧），rollup 失败只
	// Warn（派生数据 + 读路径 fail-open）。并进同一步会让维护页把两种截然
	// 不同的结局显示成同一个状态。
	StepPartitionTelemetryFrame = "partition_telemetry_frame"
	StepEnsurePartitions        = "ensure_partitions"
	StepParentIndexes           = "parent_indexes"
	StepRetireDeadIndexes       = "retire_dead_indexes"
	StepWarmup                  = "warmup"
	StepStartupTasks            = "startup_tasks"

	// DefaultVersion 是 Snapshot.Version 的缺省值（未注入时）。
	DefaultVersion = "unknown"
)

// DefaultSteps 返回契约 §3.1 表格里的步骤（权重视为相对值，但这里合计 100）。
//
// 2026-11-07 增补 StepPartitionTelemetryFrame（L2 帧表迁移，契约
// docs/设计/遥测存储-L1L2无条件启用-契约-2026-11-07.md §3.5）：权重从相邻
// 步骤里让出，**合计仍必须是 100**（TestDefaultStepsTable 守着）。
// 让出方式：unified_data 25→22、device_data 15→13、parent_indexes 25→24
// （帧表迁移插在两主表之后、rollup 之前，与 main.go 的执行顺序一致）。
//
// ============ 2026-11-08 移除 StepPartitionUnifiedData（硬切换）============
//
// 该表已停写，且保留它会在「DROP 之后重启」时把空表重建出来
// （MigrateTableToPartitioned 的 flatExists==0 分支无法区分「全新部署」与「刚 DROP」）。
// 所以 main.go 不再调用 MigrateUnifiedDataToPartitioned，这一步随之消失。
//
// 它的 22 权重**按原相对比例分给剩余步骤**（不是塞给某一步）：
//
//	原合计 78（剔除 22 后）=> 放大 100/78 ≈ 1.282 倍
//	  db_connect            3 => 3.85  => 4
//	  auto_migrate         10 => 12.82 => 13
//	  device_data          13 => 16.67 => 17（实取 18 凑整，见下）
//	  其余同理，个别 ±1 微调把合计凑回 100
//
// 权重是**相对值**，语义只要求「合计 100 + 大致反映耗时占比」，
// 合计由 TestDefaultStepsTable 硬断言。
//
// ⚠ 进度条不会因为「少了一步」错位：percentOf 用**当前步骤列表的权重和**做分母
// （tracker.go:353 `total += steps[i].Weight`），不是硬编码 100。
// 即使一时忘了调整权重、合计变成 78，进度仍会正常走完到 100%。
//
// 每次返回**新切片**：调用方（NewTracker）持有它并对副本做状态变更，
// 共享同一底层数组会让两个 Tracker（例如测试里并行构造）互相污染。
func DefaultSteps() []Step {
	// State 显式写 StepPending 而不是靠零值：StepState 的零值是空串，
	// 直接序列化成 {"state":""}，前端的状态机不认这个值（契约 §3.1 只定义了
	// pending/running/done/failed/skipped 五个字面量）。
	return []Step{
		{ID: StepDBConnect, Label: "连接数据库", Weight: 4, State: StepPending},
		{ID: StepAutoMigrate, Label: "数据库结构迁移", Weight: 13, State: StepPending},
		{ID: StepPartitionDeviceData, Label: "device_data 分区迁移", Weight: 17, State: StepPending},
		// L2 帧表是主写入路径（2026-11-07 无条件启用），迁移失败即 Fatal，
		// 因此权重与两张主表同量级。
		{ID: StepPartitionTelemetryFrame, Label: "L2 帧表分区迁移", Weight: 8, State: StepPending},
		{ID: StepPartitionRollup, Label: "聚合表分区就绪", Weight: 3, State: StepPending},
		{ID: StepEnsurePartitions, Label: "滚动分区保障", Weight: 4, State: StepPending},
		{ID: StepParentIndexes, Label: "补齐父表索引", Weight: 30, State: StepPending},
		{ID: StepRetireDeadIndexes, Label: "退役死索引", Weight: 6, State: StepPending},
		{ID: StepWarmup, Label: "缓存预热", Weight: 9, State: StepPending},
		{ID: StepStartupTasks, Label: "后台任务启动", Weight: 6, State: StepPending},
	}
}
