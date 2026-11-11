package bootstrap

// steps.go — 启动步骤清单（契约 docs/设计/启动期数据库优化与进度展示-契约-2026-11-06.md §3.1）。
//
// DefaultSteps 的 ID / Label / Weight 必须与契约表**逐字一致**：
//   · ID 是前端与 api 共同依赖的稳定标识，改名等于静默破坏两边的覆盖；
//   · Weight 合计必须为 100（有 TestDefaultStepsTable 守着），Percent 才有 0..100 语义。

// 步骤 ID 常量。用常量而不是散落的字面量：datalifecycle 埋点、api 适配器、
// 前端假设都指向同一组字符串，任何一处拼错都应该是编译期错误而不是运行期空进度。
const (
	StepDBConnect       = "db_connect"
	StepAutoMigrate     = "auto_migrate"
	StepPartitionRollup = "partition_rollup"
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
//
// ============ 2026-11-08 移除 StepPartitionUnifiedData（硬切换）============
//
// 该表已停写，且保留它会在「DROP 之后重启」时把空表重建出来
// （MigrateTableToPartitioned 的 flatExists==0 分支无法区分「全新部署」与「刚 DROP」）。
// 所以 main.go 不再调用 MigrateUnifiedDataToPartitioned，这一步随之消失。
//
// ============ 2026-11-11 移除 StepPartitionDeviceData（device_data 整表退役）============
//
// 产品裁决「原始数据不入库、只有解析值入库」（docs/设计/原始数据退役-改造方案-
// 2026-11-11.md §2.1）：device_data 的写点、读点、分区 DDL、剥离任务**全部删除**，
// 自然也就不再有「这张表的分区迁移」这一步。
//
// 与 2026-11-08 移除 unified_data 那一步**同因同果**：留下迁移调用不只是"多跑一次空转"，
// 而是会在表已被 DROP 之后把它**重新建出来**（MigrateTableToPartitioned 的
// flatExists==0 分支无法区分「全新部署」与「刚 DROP」）—— 那正好抵消本次改造的全部收益。
//
// 该步的 17 权重按**原相对比例**分给剩余步骤（不是塞给某一步）：
//
//	原合计 83（剔除 17 后）=> 放大 100/83 ≈ 1.205 倍
//	  db_connect             4 => 4.82  => 5
//	  auto_migrate          13 => 15.66 => 16
//	  partition_telemetry_frame 8 => 9.64 => 10
//	  partition_rollup       3 => 3.61  => 4
//	  ensure_partitions      4 => 4.82  => 5
//	  parent_indexes        30 => 36.14 => 35
//	  retire_dead_indexes    6 => 7.23  => 7
//	  warmup                 9 => 10.84 => 11
//	  startup_tasks          6 => 7.23  => 7
//	合计 = 5+16+10+4+5+35+7+11+7 = 100 ✅
//
// parent_indexes 取 35 而不是 36：它是"最坏情况下最耗时"的那一步，
// 只该增不该减，但为了把合计凑回 100 且不虚增到 37+，取 35（相对占比仍最大）。
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
		{ID: StepDBConnect, Label: "连接数据库", Weight: 5, State: StepPending},
		{ID: StepAutoMigrate, Label: "数据库结构迁移", Weight: 16, State: StepPending},
		// L2 帧表是主写入路径（2026-11-07 无条件启用），迁移失败即 Fatal，
		// 因此权重与剩下的主表同量级（2026-11-11 device_data 退役后，它是
		// 唯一还会走「分区迁移」这一步 Fatal 路径的时序表）。
		{ID: StepPartitionTelemetryFrame, Label: "L2 帧表分区迁移", Weight: 10, State: StepPending},
		{ID: StepPartitionRollup, Label: "聚合表分区就绪", Weight: 4, State: StepPending},
		{ID: StepEnsurePartitions, Label: "滚动分区保障", Weight: 5, State: StepPending},
		{ID: StepParentIndexes, Label: "补齐父表索引", Weight: 35, State: StepPending},
		{ID: StepRetireDeadIndexes, Label: "退役死索引", Weight: 7, State: StepPending},
		{ID: StepWarmup, Label: "缓存预热", Weight: 11, State: StepPending},
		{ID: StepStartupTasks, Label: "后台任务启动", Weight: 7, State: StepPending},
	}
}
