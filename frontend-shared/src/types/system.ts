/**
 * 启动期维护态相关类型（对应后端 `internal/bootstrap.Snapshot`）。
 *
 * 契约：docs/设计/启动期数据库优化与进度展示-契约-2026-11-06.md §3.1
 *
 * 只放**传输层**形状：后端 json tag 是 snake_case，这里逐字对齐，
 * 不做 camelCase 转换（转换层会引入第二份真相，字段一多就漂移）。
 */

/** 单个启动阶段的五种状态（与后端 bootstrap.StepState 一一对应）。 */
export type MaintenanceStepState = 'pending' | 'running' | 'done' | 'failed' | 'skipped'

/** 一个启动阶段。 */
export interface MaintenanceStep {
  /** 稳定标识（前端可依赖，如 partition_unified_data）。 */
  id: string
  /** 中文展示名。 */
  label: string
  /** 进度权重（相对值，总和不一定是 100）。 */
  weight: number
  state: MaintenanceStepState
  /** 步骤内进度 0..100。 */
  progress: number
  detail?: string
  error?: string
  /** RFC3339 时间串；未开始为 undefined。 */
  started_at?: string
  finished_at?: string
}

/**
 * 启动期快照。
 *
 * ⚠️ 就绪判据**只用 ready**，不要用 `percent === 100`：
 * 失败时 percent 会停在中间值，用百分比判断会把「失败」误判成「还没好」，
 * 于是永远不会跳到正常页面。
 */
export interface MaintenanceSnapshot {
  /** 全部阶段完成且全量引擎已接管 → true。 */
  ready: boolean
  /** 某个 Fatal 阶段失败 → true（后端进程随后会退出，前端靠轮询自恢复）。 */
  failed: boolean
  /** 加权总进度 0..100。 */
  percent: number
  /** 当前阶段的 step id，全部完成时为空串。 */
  current_step: string
  /** 当前阶段的中文名，全部完成时为空串。 */
  current_label: string
  /** 启动开始时间（RFC3339）。 */
  started_at: string
  /** 距离启动开始已过的毫秒数（服务端口径）。 */
  elapsed_ms: number
  steps: MaintenanceStep[]
  /** 应用版本。 */
  version: string
  /** 构建 commit（可能为空）。 */
  build_commit?: string
}

/* ────────────────────────────────────────────────────────────────────────
 * 历史数据回填（老 unified_data → telemetry_frame 的一次性后台迁移）
 *
 * 契约：docs/设计/老数据迁移到帧表-分析与契约-2026-11-07.md §4.6
 * 数据源：GET /api/v1/system/backfill → backend/internal/backfill.Progress
 *
 * ⚠️ 与维护态（MaintenanceSnapshot）是**两个不同生命周期的东西**，不要混用：
 *   · MaintenanceSnapshot 描述「启动是否完成」，ready=true 后前端就离开维护页；
 *   · BackfillProgress 描述「历史数据迁完没有」，**ready 之后才真正开始跑**
 *     （生产实测 3.42M 帧 / 20~40 分钟）。所以它出现在业务页，而不是维护页。
 * ──────────────────────────────────────────────────────────────────────── */

/**
 * 回填状态。逐字对应后端 `backfill.State*` 常量（5 个）。
 *
 * ⚠️ **不要与状态表 `telemetry_backfill_progress.state` 的取值混为一谈** ——
 * 它们是两套不同的 state，作用域不同：
 *
 * | 出处 | 取值 | 作用域 |
 * |---|---|---|
 * | 契约 §4.3 的**数据库状态表**（每分区一行） | `pending` / `running` / `done` / `failed` | **单个分区** |
 * | 契约 §4.6 的**接口载荷**（本类型，全局 Runner） | `idle` / `running` / `done` / `failed` / `disabled` | **整个回填引擎** |
 *
 * 因此 `idle` 与 `disabled` **只存在于接口载荷**，DB 表里永远不会出现它们。
 * 反过来 `pending` 只存在于 DB 表，接口不会返回。后人若看到 `disabled` 就去
 * DB 表里找，会白找一场。
 *
 * 前端的处理口径：
 *   · running  → 渲染进度卡（有进度、帧数、ETA）；
 *   · failed   → 渲染错误卡（**不**隐藏：历史数据没迁完是必须告知的事实）；
 *   · disabled → 渲染信息卡（**不**隐藏：运维关掉回填意味着老数据永远不会迁移，
 *                用户有权知道这件事；静默隐藏等于让"数据没迁"这件事永不可见）；
 *   · done / idle → 隐藏（`done` 是一次性任务已完成，`idle` 是尚未启动，
 *                两者都没有"未完成的工作"需要汇报）。
 */
export type BackfillState = 'idle' | 'running' | 'done' | 'failed' | 'disabled'

/**
 * 回填进度快照（契约 §4.6 的 **8 个 key**，与后端 `api.BackfillSnapshot` 逐字一致）。
 *
 * 所有字段**恒出现**（后端不加 omitempty，`errors` 恒为数组）—— 故这里全部必选，
 * 前端不需要为「字段可能不存在」写防御分支。
 *
 * ⚠ 刻意**不含** `rows_done` 与 `partitions`：那是引擎内部的对账量/排障明细，
 * 后端镜像类型刻意没往外暴露（高频轮询的探针不该塞大数组）。
 * 别因为 `internal/backfill.Progress` 里有它们就往这里加 —— 两边的
 * 字段集合由 backfillContractParity.spec.ts 与 Go 侧
 * TestBackfillSnapshotJSONShapeMatchesContract 双向锁住。
 *
 * 但「取值」本身有约定，**0 ≠ 零值语义**，两处必须按注释理解：
 *   · `frames_total`：**0 表示「总数未知」**，不是「共 0 帧」。精确总数要全表
 *     count(DISTINCT device_id,timestamp)，成本高且对进度无必要（percent 是按
 *     分区时间跨度算的）。故 UI **不得**把它当分母直接拼 "x / 0"。
 *   · `eta_seconds`：**0 表示「尚未算出」**（刚启动时速率样本不足），
 *     不是「马上完成」。UI 显示占位符「—」，不得显示「剩余 0 秒」。
 *
 * 把这两条做成必选 + 「0 = 未知」的显式契约（而不是 `?: number`），是因为
 * `?` 在类型层面就丢了信息：TS 无法强制调用方处理 undefined，而
 * `number` + 文档化的哨兵值能让每个读取点都必须做一次显式判断。
 */
export interface BackfillProgress {
  state: BackfillState
  /** 0..100，按**分区时间跨度**加权（后端 O(1) 单调递增）。 */
  percent: number
  /** 当前正在回填的分区名（如 unified_data_202610）；完成/未开始时为空串。 */
  current_partition: string
  /** 已回填帧数（跨全部分区累计）。 */
  frames_done: number
  /** 总帧数；**0 = 未知**（见上的字段说明，不是「共 0 帧」）。 */
  frames_total: number
  /** 剩余秒数估算；**0 = 未知**（不是「马上完成」）。 */
  eta_seconds: number
  /** 当前保留期配置是否为「永久保留」（0 天）。为 true 时回填**不会**删任何老数据。 */
  retention_permanent: boolean
  /** 非致命问题 / 失败原因（恒为数组，不会是 null；后端最多保留最近 20 条）。 */
  errors: string[]
}
