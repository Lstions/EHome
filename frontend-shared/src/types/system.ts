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
