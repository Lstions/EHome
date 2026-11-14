import axios, { type AxiosInstance } from 'axios'
import type { MaintenanceSnapshot } from '@/types/system'

/**
 * 启动期维护态接口（**免鉴权**，契约 §3.3）。
 *
 * ⚠️ **必须用独立 axios 实例**，不能复用 `api/client.ts` 的 apiClient：
 * 那个实例的响应拦截器在「有本地 token + HTTP 401」时会清 session 并硬跳 `/login`。
 * 而维护期「未登录」是**正常态**、后端也可能正在启动并返回 4xx/5xx ——
 * 复用全局实例会把维护页直接弹成登录页，用户永远看不到「系统升级中」。
 * 独立实例不带任何拦截器：不注入 token、不跳登录、不做业务 envelope 解包。
 *
 * 超时 5s：启动期后端可能正在跑分钟级 DDL，宁可让前端早失败并继续轮询，
 * 也不要单个请求挂 10s 把轮询节奏拖死（轮询用 setTimeout 链，天然不并发）。
 */

/** 与 api/client.ts 完全同口径的 baseURL 推导（完整覆盖 → 子路径前缀）。 */
const apiBaseURL = import.meta.env.VITE_API_BASE_URL
  ? import.meta.env.VITE_API_BASE_URL
  : (import.meta.env.VITE_BASE_PATH ? import.meta.env.VITE_BASE_PATH.replace(/\/+$/, '') : '')

/** 维护态探测超时（毫秒）。导出便于单测断言，避免魔法数字两处漂移。 */
export const MAINTENANCE_TIMEOUT_MS = 5000

/** 维护态专用 HTTP 实例：无拦截器、5s 超时。 */
export const maintenanceClient: AxiosInstance = axios.create({
  baseURL: apiBaseURL,
  timeout: MAINTENANCE_TIMEOUT_MS,
})

/** 后端统一 envelope（与全仓一致：code 200 成功，>=400 为业务错误）。 */
export interface MaintenanceEnvelope {
  code: number
  message?: string
  data: MaintenanceSnapshot
}

/**
 * 「后端根本没有这个接口」—— 与「后端暂时连不上」是**两种不同事实**，
 * 前端必须区别对待（见 App.vue 门禁的 fail-open 说明）：
 *   · unsupported ⇒ 后端是**不认识该接口的旧版本**（或纯静态托管），
 *     维护功能整体不存在 ⇒ 保持旧行为，直接进入正常页面；
 *   · 其它错误 ⇒ 后端正在启动/重启 ⇒ 显示维护页「连接中」并继续轮询。
 */
export class MaintenanceUnsupportedError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'MaintenanceUnsupportedError'
  }
}

/**
 * 判断某个响应体是否是合法的维护态快照（fail-closed：认不出就当作不认识该接口）。
 *
 * 判据的取舍：
 * · `ready` 必须是布尔 —— 这是门禁**唯一**的决策依据，缺了就没法判，必须 fail-closed；
 * · `steps` 允许缺失/null：Go 侧 `var steps []Step` 为 nil 时 json 序列化成 `null`
 *   而不是 `[]`（`json.Marshal(nil slice)` → `null`）。若因此判为「不认识该接口」，
 *   门禁会 fail-open 直接进入业务页 —— 恰恰是最坏的结果（维护期用户撞满 503）。
 *   故这里把缺失/null 归一成空数组，**只要有 ready 就认**。
 * · 但 steps 若**存在且不是数组**（例如是字符串/对象），仍判非法 —— 那是真的形状不对。
 */
export function isMaintenanceSnapshot(value: unknown): value is MaintenanceSnapshot {
  if (!value || typeof value !== 'object') return false
  const candidate = value as Record<string, unknown>
  if (typeof candidate.ready !== 'boolean') return false
  const steps = candidate.steps
  if (steps !== undefined && steps !== null && !Array.isArray(steps)) return false
  return true
}

/**
 * 把后端快照归一成前端可安全消费的形状。
 *
 * 归一的两处都是「Go 零值 ↔ TS 严格类型」的真实缝隙，且都**不会**被类型系统发现：
 * · `steps: null` → `[]`（nil slice 序列化）。页面渲染 `v-for` 时 null 会抛错。
 * · `steps[i].progress` 缺失 → 0；`percent` 缺失 → 0（omitempty/零值时可能不出现）。
 */
export function normalizeSnapshot(raw: MaintenanceSnapshot): MaintenanceSnapshot {
  const steps = Array.isArray(raw.steps) ? raw.steps : []
  return {
    ...raw,
    percent: typeof raw.percent === 'number' ? raw.percent : 0,
    elapsed_ms: typeof raw.elapsed_ms === 'number' ? raw.elapsed_ms : 0,
    current_step: raw.current_step ?? '',
    current_label: raw.current_label ?? '',
    steps: steps.map((step) => ({
      ...step,
      progress: typeof step.progress === 'number' ? step.progress : 0,
      weight: typeof step.weight === 'number' ? step.weight : 0,
    })),
  }
}

/**
 * 读取启动期维护态快照。
 *
 * - 成功：返回 Snapshot（判据是 envelope.code < 400，后端当前返回 200）。
 * - 后端不认识该接口（HTTP 404 / 响应不是合法 envelope 或不是合法快照）：
 *   抛 {@link MaintenanceUnsupportedError}。
 * - 其余失败（网络错误、超时、5xx、503）原样向上抛，由调用方进入「连接中 + 重试」。
 *
 * 刻意**不吞异常、不返回兜底快照**：把「没连上」伪造成「已就绪」会让门禁在
 * 真正维护期放行到业务页面，用户看到一片 503。
 */
export async function getMaintenanceStatus(): Promise<MaintenanceSnapshot> {
  let response
  try {
    response = await maintenanceClient.get<MaintenanceEnvelope>('/api/v1/system/maintenance')
  } catch (error) {
    // axios 的 HTTP 404：说明后端没有这个路由（旧版本 / 纯静态托管）
    if (isHttpStatus(error, 404)) {
      throw new MaintenanceUnsupportedError('后端不支持启动期维护态接口')
    }
    throw error
  }

  const envelope = response.data
  // 纯静态服务器（vite preview 等）对 /api/* 会回 SPA 的 index.html ⇒ data 是字符串 HTML，
  // 没有 code 字段。这同样属于「后端不认识该接口」，必须 fail-open，否则本地
  // 预览与既有 e2e（e2e/render-contract.spec.ts 用 page.route 注入业务接口但不注入本接口）
  // 会被永久锁在维护页。
  if (!envelope || typeof envelope !== 'object' || typeof envelope.code !== 'number') {
    throw new MaintenanceUnsupportedError('维护态接口响应不是统一 envelope（疑为旧后端或静态托管）')
  }
  if (envelope.code >= 400) {
    throw new Error(envelope.message || '维护状态查询失败')
  }
  if (!isMaintenanceSnapshot(envelope.data)) {
    throw new MaintenanceUnsupportedError('维护态接口返回的 data 不是合法快照')
  }
  return normalizeSnapshot(envelope.data)
}

/** 从 axios 错误里取 HTTP 状态码（不 import axios 类型，避免把实现细节泄进判据）。 */
function isHttpStatus(error: unknown, status: number): boolean {
  const response = (error as { response?: { status?: number } } | null)?.response
  return response?.status === status
}

/* ════════════════════════════════════════════════════════════════════════
 * 历史数据回填进度（契约 §4.6）
 * ════════════════════════════════════════════════════════════════════════ */

/**
 * 回填进度接口的 envelope（与维护态同形）。
 *
 * 超时沿用 {@link maintenanceClient} 的 5s，**不为它另建实例**：
 * 轮询间隔本身就是 5s，单次请求再宽也只会让轮询错位堆积；且每多建一个
 * axios 实例就多一份「拦截器会不会被后来人偷偷挂上」的隐患 ——
 * api/__tests__/system.spec.ts 断言「模块加载时 axios.create 恰好被调用 1 次」，
 * 这条断言正是为了防这个。 */
