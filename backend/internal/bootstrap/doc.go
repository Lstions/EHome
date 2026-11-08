// Package bootstrap 是启动期进度的状态机：main.go 在启动序列的每一步
// Begin/Done/Fail/Skip，HTTP 层（维护引擎）随时 Snapshot() 出前端要的进度快照。
//
// 契约全文见 docs/设计/启动期数据库优化与进度展示-契约-2026-11-06.md §3.1。
// 本文件是给**使用者**看的语义说明（boot-api 的适配器、main.go 的接线、
// datalifecycle 的埋点都按这份说明理解行为）；字段定义见 tracker.go / steps.go。
//
// ============ 1. 十个步骤的语义与顺序 ============
//
// DefaultSteps() 的返回顺序就是 main.go 的启动顺序（前面的先跑），权重合计 100：
//
//	ID                        Label                Weight  它在 main.go 里对应什么
//	------------------------  -------------------  ------  --------------------------------------------
//	db_connect                连接数据库                4  database.Connect（含重试）
//	auto_migrate              数据库结构迁移           13  database.AutoMigrate
//	partition_device_data     device_data 分区迁移     17  datalifecycle.MigrateTableToPartitioned(device_data)
//	partition_telemetry_frame L2 帧表分区迁移           8  datalifecycle.MigrateTelemetryFrameToPartitioned
//	partition_rollup          聚合表分区就绪            3  datalifecycle.MigrateTelemetryRollup1mToPartitioned
//	ensure_partitions         滚动分区保障              4  EnsurePartitionsFor × N（[上月, 未来 3 月]）
//	parent_indexes            补齐父表索引             30  ensurePartitionParentIndexes（分钟级 DDL，最耗时）
//	retire_dead_indexes       退役死索引                6  retireDeadTimeseriesIndexes（DROP INDEX，ACCESS EXCLUSIVE）
//	warmup                    缓存预热                  9  最新值缓存/字段解析器预热
//	startup_tasks             后台任务启动              6  purger / retention / notify / 分区每日巡检 …
//
// 权重**不是**按耗时精确标定的，而是按"最坏情况下这一段会不会让维护页看起来卡死"排：
// 最耗时的分钟级 DDL（parent_indexes 30）独占近三分之一。
//
// ============ 1.0 为什么没有 partition_unified_data（2026-11-08 硬切换）============
//
// 那一步原本调用 datalifecycle.MigrateUnifiedDataToPartitioned，权重 22。
//
// 它被**删除**而不是"保留但跳过"，因为保留它有真实危害：
// MigrateTableToPartitioned 的判定链是 IsTablePartitioned(relkind='p') → 否；
// flatExists(relkind='r') → 否；于是走「原表不存在（全新部署）→ createPartitionedParent」。
// 代码**无法区分**「全新部署」和「我们刚把表 DROP 了」⇒ DROP 之后每次重启
// 都会得到一张空的 unified_data（5 个月分区、0 行）：磁盘没省，数据没了，
// 而 pg_class 里它看起来一切正常。
//
// 一次性测试证明该分支真实存在（走的就是 DROP 之后的路径）：
// TestMigrateUnifiedData_FreshDeploy_PartitionedParentAtFinalName
//
// MigrateUnifiedDataToPartitioned 函数本身保留（partition_mgr 的泛化入口，
// device_data / telemetry_frame 仍走同一套机制），只是 main.go 不再为 unified_data 调它。
//
// ============ 1.1 为什么 partition_telemetry_frame 单独一步（2026-11-07 增补）============
//
// 它没有并进 partition_rollup，尽管两者都是"遥测相关的建表"。**因为失败语义相反**：
//
//	partition_telemetry_frame  失败 ⇒ **Fatal**（中止启动）
//	  理由: L2 帧表在 2026-11-07「L1/L2 无条件启用」裁决后是**主写入路径**之一，
//	        每帧解析结果都要写进去。表没建但服务照跑 = 主写成功而帧表静默空着，
//	        所有面板看起来正常，直到有人去读帧才暴露 —— 那时已经丢了一整天数据。
//	        （契约 docs/设计/遥测存储-L1L2无条件启用-契约-2026-11-07.md §3.4）
//
//	partition_rollup           失败 ⇒ **只 Warn**（Skip 计满权重后继续启动）
//	  理由: rollup 是**派生数据**（下一分钟重新累积），且读路径 fail-open 回落原始表。
//	        为一张派生表挡住整个服务启动，是把"预聚合不可用"升级成"服务不可用"。
//
// 并成一步的后果是维护页只能显示一个状态：要么把 Fatal 显示成"不挡启动"，要么把
// Warn 显示成"启动失败"。前端运维看到的那一行，会与真实后果不符。
//
// 权重 6 的来源：它插在两条主表迁移之后、rollup 之前，让出方式为
// unified_data 25→22 / device_data 15→13 / parent_indexes 25→24，合计仍为 100
// （TestDefaultStepsTable 守着这张表，漂移即红）。
//
// ⚠ 步骤顺序可以调整，但 **ID 不能改**：前端的阶段列表、api 的 /health phase、
// datalifecycle 的埋点 stepID 三处都按字符串字面量对齐（见 steps.go 的导出常量）。
//
// ============ 2. Ready 与 Percent 的计算规则 ============
//
// Ready（Tracker.Ready() 与 Snapshot.Ready 同口径，从同一份拷贝推导，不会自相矛盾）:
//
//	ready = 所有步骤的状态 ∈ {done, skipped}
//	· **不用**"最后一个步骤 done"之类的判据 —— 步骤顺序会变，且 skipped 是合法终态；
//	· failed / running / pending 任一存在 ⇒ ready=false；
//	· 空步骤表、nil Tracker ⇒ ready=true（"没有要等的工作"）。
//
// Percent（0..100，保留 1 位小数，全程 clamp，绝不产生 NaN）:
//
//	percent = Σ(step 的折算权重) / Σ(全部 Weight) × 100
//	  其中 done / skipped ⇒ 计**满**权重
//	       running      ⇒ 计 weight × Progress/100（所以建索引期间进度条是动的）
//	       pending      ⇒ 计 0
//	       failed       ⇒ 计 0
//
//	· **skipped 计满**是刻意的：skipped 表示"这一步不需要做"（例：非 PG 方言、
//	  表已经是分区表），若计 0，正常启动会永远停在 <100%，前端那个"升级中"的页面
//	  就再也关不掉了；
//	· running 按 Progress 折算是硬要求：ensurePartitionParentIndexes 这类分钟级
//	  DDL 期间，进度必须随时间前进，而不是一格不动地等它返回；
//	· ready=true 时 Percent 恒为 100（不看累计值，避免权重写错导致"已就绪但 87%"）。
//
// ============ 3. 埋点侧与 main 侧的职责边界 ============
//
// 一条铁律：**进度包只管"到哪了"，不管"成没成"。**
//
//	datalifecycle 埋点（SetMigrationProgress 注入的回调）:
//	  · 只有 StepProgress(stepID, pct, detail) 一个方法，**结构上没有报错通道**；
//	  · 它只上报"正在做第 k/N 批 / 第 i/M 条索引"，永远不会把某个 step 标成 failed，
//	    也不会把 step 标成 done —— **终态一律由 main.go 决定**。
//	  · 契约原文：迁移失败即 Fatal，但 telemetry_rollup_1m 失败**只 Warn**。
//	    ⇒ main 侧在拿到 err 后自己选：Fatal 的那两条用 Fail(stepID, err)，
//	    rollup 那条用 Skip(StepPartitionRollup, err.Error())（"这一步没做成，但不挡启动"）。
//	    埋点侧若自作主张标失败，就会把"派生表迁移失败"升级成"服务不可用"的观感。
//
//	main.go:
//	  · 每进入一段 Begin(id)，做完 Done(id, detail)，不需要做就 Skip(id, 原因)，
//	    致命错误在 Fatal **之前**先 Fail(id, err)（让维护页能显示失败原因）；
//	  · 若失败要中止启动，Fail 之后照旧 Fatalf —— 埋点不改变任何既有失败语义。
//
//	HTTP 层（api 维护引擎）:
//	  · 只读 Snapshot()，把 Snapshot.Ready 映射到 /readyz 的 200/503；
//	  · **不要**在 handler 里改步骤状态（那是 main 的事）。
//
// ============ 4. 迟到回调免疫 ============
//
// 埋点是"每批上报"，与 main 侧的 Done 之间存在天然竞态：main 在迁移函数返回后
// 立刻 Done，而 datalifecycle 的最后一批回调可能刚好晚一拍。因此:
//
//	· StepProgress 对已 done / 已 skipped 的步骤**完全无效**（不回退状态、
//	  不覆盖 Progress/Detail），也**不会**把 pending 之外的状态打回 running；
//	· skipped 是**终态**：之后的 Done / Fail / StepProgress 一律忽略；
//	· StepProgress 对 pending 步骤等价于隐式 Begin（首次上报会自动置 running 并记
//	  StartedAt），这样遗漏一次 Begin 不会让该步骤永远停在 pending；
//	· 未知 stepID 一律静默忽略（埋点按表名派生 stepID，多一张未登记的表不该让启动失败）。
//
// ============ 5. 并发模型 ============
//
// Tracker 全部导出方法并发安全（单把 sync.RWMutex：写少读多，HTTP 轮询是读侧）。
// Snapshot() 返回**深拷贝**（含 *time.Time 指向的时间值），调用方随便改返回值都
// 不会污染内部状态 —— handler 里做 JSON 序列化、测试里就地改字段都是安全的。
package bootstrap
