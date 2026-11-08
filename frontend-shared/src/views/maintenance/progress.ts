import type { MaintenanceStep, MaintenanceStepState } from '@/types/system'

/**
 * 维护页 / 启动门禁的纯函数助手（与 .vue 分文件，便于直接单测，不必 mount）。
 */

/** 五种状态的展示文案（也是无障碍文本：图标之外必须有文字，读屏才读得懂）。 */
export const STEP_STATE_TEXT: Record<MaintenanceStepState, string> = {
  pending: '等待中',
  running: '进行中',
  done: '已完成',
  failed: '失败',
  skipped: '已跳过',
}

/**
 * 步骤展示名：`label` 为空时回落到 `id`，再回落到占位符 `—`。
 *
 * 为什么必须兜底：`label` 是后端给的展示串，任何序列化缝隙（omitempty、旧版本、
 * 后端改字段）都可能让它变成空串 —— 而空串在列表里就是**一行什么都没有**，
 * 用户看到的是「进度卡住了但不知道卡在哪」。
 * 回落到 `id` 而不是直接给 `—`：id 是稳定标识（如 `parent_indexes`），
 * 用户报给管理员时比一个破折号有用得多。
 */
export function stepDisplayLabel(step: { id?: string; label?: string }): string {
  const label = typeof step.label === 'string' ? step.label.trim() : ''
  if (label) return label
  const id = typeof step.id === 'string' ? step.id.trim() : ''
  return id || '—'
}

/**
 * 当前阶段展示名。
 *
 * 契约里 failed=true 时 `current_step`/`current_label` **可能为空**（失败发生在
 * 某一阶段中途，Tracker 不一定还记着"当前步"）。空串不能直接拼进 UI —— 会渲染成
 * 「当前阶段：」这样一个悬空标签。故缺省时给一句明确的状态文案。
 */
export function currentStageText(currentLabel?: string): string {
  const label = typeof currentLabel === 'string' ? currentLabel.trim() : ''
  return label ? '当前阶段：' + label : '当前阶段：已停止'
}

/** 步骤状态 → 展示文案；未知状态回落到「等待中」而不是抛错（后端新增状态不应白屏）。 */
export function stepStateText(state: MaintenanceStepState | string): string {
  return STEP_STATE_TEXT[state as MaintenanceStepState] ?? STEP_STATE_TEXT.pending
}

/**
 * 毫秒 → 「12 秒」/「3 分 05 秒」。
 *
 * 展示口径：< 1 分钟只给秒（启动期 DDL 常见区间，精确到秒足够）；
 * >= 1 分钟补分钟并把秒补零到两位，避免「3 分 5 秒」在等宽环境里宽度抖动。
 * 负数/NaN 一律回落到 0（服务端时钟回拨时不显示 -1 秒这种脏值）。
 */
export function formatElapsed(ms: number): string {
  const safe = Number.isFinite(ms) && ms > 0 ? Math.floor(ms / 1000) : 0
  if (safe < 60) return safe + ' 秒'
  const minutes = Math.floor(safe / 60)
  const seconds = safe % 60
  return minutes + ' 分 ' + String(seconds).padStart(2, '0') + ' 秒'
}

/** 轮询基础间隔：连得上就 2s 快问。 */
export const BASE_POLL_MS = 2000
/** 失败退避的第二档。 */
export const RETRY_POLL_MS = 3000
/** 失败退避的上限（封顶，不再增长）。 */
export const MAX_POLL_MS = 5000

/**
 * 轮询间隔（毫秒）：连得上就 2s；连续失败按 2s → 3s → 5s 封顶。
 *
 * 为什么要有上限退避：后端进程整个挂掉时（启动期 Fatal 退出），
 * 不退避的 2s 轮询会一直打一个不存在的端口，噪音大且无意义；
 * 但**不能停止轮询** —— 后端被拉起/重启后必须自动恢复，所以只降频不停。
 */
export function maintenancePollDelay(consecutiveFailures: number): number {
  if (consecutiveFailures <= 0) return BASE_POLL_MS
  if (consecutiveFailures === 1) return RETRY_POLL_MS
  return MAX_POLL_MS
}

/**
 * 首次探测的宽限期（毫秒）。
 *
 * 正常启动时探测只要几十毫秒；若一拿到「还没返回」就渲染维护页，
 * 每次正常打开应用都会闪一帧「系统升级中」。宽限期把这帧吃掉。
 * 宽限期内渲染**轻量启动骨架**（品牌 + 转圈，见 App.vue 的 .startup-gate-pending），
 * 既不是白屏，也不是误导性的「系统升级中」：这 800ms 是页面可见内容的**空白上限**。
 */
export const PROBE_GRACE_MS = 800

/**
 * 401/403 是否意味着「后端已完全就绪」。
 *
 * 推理链（这是**判据本身**，不是凭次数放行）：
 *   ① 维护期后端一定会返回 **503 + MAINTENANCE_IN_PROGRESS**（契约 §3.3），
 *      因为维护引擎对除白名单外的所有业务请求统一 503；
 *   ② `/api/v1/system/maintenance` 在维护引擎里是**免鉴权白名单**的一員，
 *      所以维护期它**不可能**返回 401/403 —— 能返回 401/403，说明请求已穿过
 *      维护引擎、打到了**完整引擎的鉴权中间件**上，即全量路由已经注册完成；
 *   ③ 完整引擎在跑但该路径被鉴权中间件拦截（反代/中间件顺序配置问题），
 *      这不是「启动中」，继续等只会把用户永久锁在维护页。
 * 故 401/403 ⇒ 立即放行到正常页（{@link isAuthDenial}）。
 *
 * ⚠️ 放行**只影响进入正常页**，不停轮询（见 useStartupGate 的 watchdog）：
 * 若之后后端又变成维护态（重启），门禁要能**重新**进入维护页。
 */
export function isAuthDenial(status: number | undefined): boolean {
  return status === 401 || status === 403
}

/**
 * 放行到正常页后的**看门狗**轮询间隔。
 *
 * 为什么放行后还要轮询：后端可能重启并重新进入维护态，此时页面必须能重新显示
 * 维护页（否则用户会在一个 503 满地的界面上反复点）。看门狗用最慢档（5s），
 * 既能把重入延迟控制在可接受范围，又不至于持续打后端。
 */
export const WATCHDOG_POLL_MS = MAX_POLL_MS

/** 从任意错误里取 HTTP 状态码（取不到返回 undefined）。 */
export function httpStatusOf(error: unknown): number | undefined {
  const response = (error as { response?: { status?: number } } | null)?.response
  const status = response?.status
  return typeof status === 'number' ? status : undefined
}

/** 步骤进度取整到 0..100（防御后端给出 100.0000001 或负数）。 */
export function clampPercent(value: number): number {
  if (!Number.isFinite(value)) return 0
  if (value < 0) return 0
  if (value > 100) return 100
  return Math.round(value)
}

/** 统计某状态的步骤数（失败态摘要用）。 */
export function countByState(steps: MaintenanceStep[], state: MaintenanceStepState): number {
  return steps.filter((step) => step.state === state).length
}
