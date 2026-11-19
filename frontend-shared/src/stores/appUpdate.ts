import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { getSystemVersion, type SystemVersionInfo } from '@/api/system'
import { useWebSocketStore } from '@/stores/websocket'
import { logger } from '@/utils/logger'

/**
 * 旧页面提醒刷新（契约 §6.4：4 态 + 四通道检测 + 更新内容弹窗）。
 *
 * ── 为什么是 store 而不是组件内逻辑 ──────────────────────────────────────
 * 四条检测通道分别落在 axios 拦截器（api/client.ts）、WebSocket 重连、
 * 定时/可见性、以及 vite:preloadError + router.onError。它们**都不能**直接
 * import 组件；而它们必须汇聚到**同一份**状态，否则「横幅说已最新、模态说
 * 需要刷新」这类自相矛盾一定会出现。故状态与判定全部收在这里，非组件调用方
 * 通过 module 级桥（notifyServerBuild / notifyModuleLoadFailure）投递事件 ——
 * 桥不依赖 Pinia 实例，拦截器在 app.use(pinia) 之前也能安全调用。
 *
 * ── 四条铁律（越界即缺陷）────────────────────────────────────────────────
 * 1. **绝不自动 `location.reload()`**：刷新只能由用户点击
 *    （{@link useAppUpdateStore.refreshNow} 是唯一出口），否则未提交的表单会被吞掉。
 * 2. `dismissed` 只压制 `stale` 的**主动**提示；`blocked` 不可推迟/不可压制 ——
 *    此时页面已无法正常导航，继续用只会不断撞 404。
 * 3. 部署**回滚**（服务端 build_time 更旧）不提示，避免把回滚误报成新版本。
 * 4. 检查失败 ≠ 已是最新：失败时保持现状（不进入 stale），但也不把已有提示清掉。
 */

/** 4 态（契约 §6.4 状态机表）。 */
export type AppUpdateState = 'same' | 'stale' | 'blocked' | 'dismissed'

/** 触发来源，只用于日志与节流分类（四通道各一档）。 */
export type AppUpdateCheckSource = 'header' | 'ws' | 'boot' | 'visibility' | 'poll' | 'manual'

/** 30 分钟轮询（通道 3）。 */
export const APP_UPDATE_POLL_INTERVAL_MS = 30 * 60 * 1000
/** 同一标签页 5 分钟硬节流：轮询与可见性通道不得比它更密。 */
export const APP_UPDATE_HARD_THROTTLE_MS = 5 * 60 * 1000
/** 全部通道的请求最小间隔：防止一次部署引发几十个并发检查（响应头通道会被每次响应触发）。 */
export const APP_UPDATE_MIN_GAP_MS = 5 * 1000
/** 「稍后」按 build_id 记（按标签页）：sessionStorage 天然是每标签独立。 */
export const APP_UPDATE_DISMISS_KEY = 'ehome.app-update.dismissed-build'

/** 缺失 build id 时展示的文案（**不回退**到任何写死的版本号）。 */
export const UNKNOWN_BUILD_TEXT = '未知'

/** 判定「后端不认识版本端点」——按 name 判，不用 instanceof（见 check 内注释）。 */
function isUnsupportedVersionError(error: unknown): boolean {
  return error instanceof Error
    ? error.name === 'SystemVersionUnsupportedError'
    : false
}

/** 模块加载失败的错误文案特征（Chrome / Safari / Firefox 与 vite 包装各不相同）。 */
const MODULE_LOAD_PATTERNS = [
  'failed to fetch dynamically imported module',
  'error loading dynamically imported module',
  'importing a module script failed',
  'unable to preload css',
  'failed to load module script',
  'load failed',
]

/**
 * 是否为「懒加载 chunk 取不到」这类错误（通道 4 的判据）。
 *
 * 只认 URL/模块加载失败的特征串，不把任意 router.onError 都当 blocked：
 * 导航守卫抛错、组件 setup 抛错也走 router.onError，那些不是「旧 hash 已不存在」，
 * 弹「刷新即可继续」会误导用户。判据宁可窄 —— 漏判仍由通道 1/3 兜底。
 */
export function isModuleLoadFailure(reason: unknown): boolean {
  if (!reason) return false
  const text = typeof reason === 'string'
    ? reason
    : `${(reason as Error)?.name ?? ''} ${(reason as Error)?.message ?? ''}`
  const lower = text.toLowerCase()
  return MODULE_LOAD_PATTERNS.some((pattern) => lower.includes(pattern))
}

/* ────────────────────────────────────────────────────────────────────────────
 * module 级事件桥（非组件调用方 → store）
 *
 * 单例集合而不是单个回调：测试里可能同时挂多个 store 实例（每个 pinia 一份）；
 * init 之前到达的事件先进队列，init 时补投 —— 拦截器在 App 挂载前就跑过一轮的
 * 情况必须能被看见（首屏接口请求通常早于 onMounted）。
 * ──────────────────────────────────────────────────────────────────────────── */

const buildListeners = new Set<(buildId: string) => void>()
const failureListeners = new Set<(detail: string) => void>()
let pendingBuilds: string[] = []
let pendingFailures: string[] = []

/** axios 响应拦截器读到 `x-ehome-build` 时投递（通道 1）。 */
export function notifyServerBuild(buildId: string): void {
  const id = (buildId || '').trim()
  if (!id) return
  if (buildListeners.size === 0) {
    // 队列只用于「挂载前的早到的响应头」，上限 8 条足够（同一时刻只会有 1~2 个 build）
    pendingBuilds.push(id)
    if (pendingBuilds.length > 8) pendingBuilds.shift()
    return
  }
  for (const listener of [...buildListeners]) listener(id)
}

/** vite:preloadError / router.onError 识别到模块加载失败时投递（通道 4）。 */
export function notifyModuleLoadFailure(detail = ''): void {
  if (failureListeners.size === 0) {
    pendingFailures.push(detail)
    return
  }
  for (const listener of [...failureListeners]) listener(detail)
}

/** 仅测试用：清空 module 级桥（避免用例之间互相投递）。 */
export function resetAppUpdateBridgeForTests(): void {
  buildListeners.clear()
  failureListeners.clear()
  pendingBuilds = []
  pendingFailures = []
}

function readEnv(key: 'VITE_BUILD_ID' | 'VITE_BUILD_TIME' | 'VITE_APP_VERSION'): string {
  const value = (import.meta.env as Record<string, unknown>)[key]
  return typeof value === 'string' ? value.trim() : ''
}

function readDismissedBuild(): string {
  try {
    return sessionStorage.getItem(APP_UPDATE_DISMISS_KEY) || ''
  } catch {
    // Safari 无痕 / 存储被禁用：降级为「本次页面生命周期内记住」
    return ''
  }
}

function writeDismissedBuild(buildId: string): void {
  try {
    if (buildId) sessionStorage.setItem(APP_UPDATE_DISMISS_KEY, buildId)
    else sessionStorage.removeItem(APP_UPDATE_DISMISS_KEY)
  } catch {
    /* 存储不可用时静默：不因为「记不住稍后」而阻断主流程 */
  }
}

export const useAppUpdateStore = defineStore('appUpdate', () => {
  // ── 本地构建身份（构建期由 vite define 注入）──────────────────────────────
  const localBuildId = ref(readEnv('VITE_BUILD_ID'))
  const localBuildTime = ref(readEnv('VITE_BUILD_TIME'))
  const fallbackVersion = ref(readEnv('VITE_APP_VERSION'))

  // ── 服务端快照 ────────────────────────────────────────────────────────────
  const state = ref<AppUpdateState>('same')
  const serverVersion = ref('')
  const serverBuildId = ref('')
  const serverBuildTime = ref('')
  const notes = ref<string[]>([])
  const blockedDetail = ref('')

  // ── 节流与「稍后」 ────────────────────────────────────────────────────────
  const lastCheckAt = ref(0)
  const lastCheckedBuild = ref('')
  const dismissedBuild = ref(readDismissedBuild())
  const checkCount = ref(0)

  // ── 弹窗（横幅与「关于」共用同一个实例）───────────────────────────────────
  const releaseNotesVisible = ref(false)

  // ── 生命周期句柄（init/dispose 幂等）──────────────────────────────────────
  let installed = false
  let inFlight: Promise<void> | null = null
  let pollTimer: ReturnType<typeof setInterval> | null = null
  let stopWs: (() => void) | null = null
  /** 尾沿补跑：被节流丢弃的触发要在边界时刻补一次（见 scheduleTrailingCheck）。 */
  let trailingTimer: ReturnType<typeof setTimeout> | null = null
  let trailingDueAt = 0
  let trailingSource: AppUpdateCheckSource = 'manual'

  /** 侧栏/登录页展示的版本号：服务端真实版本优先，其次构建期注入，最后「未知」。 */
  const displayVersion = computed(() => serverVersion.value || fallbackVersion.value || UNKNOWN_BUILD_TEXT)
  /**
   * 展示用 build id：**当前运行中的构建**优先（"关于"要告诉用户「你现在用的是哪个构建」），
   * 只有本地没注入时才退回服务端值。两者不同时由弹窗额外显示「服务端构建」。
   */
  const displayBuildId = computed(() => localBuildId.value || serverBuildId.value || UNKNOWN_BUILD_TEXT)
  /** notes 为空 ⇒ **不渲染任何入口**（§6.4 / AC-18）。 */
  const hasReleaseNotes = computed(() => notes.value.length > 0)
  /** 横幅是否可见：只有 stale 且未被网络横幅压住时才出横幅。 */
  const bannerVisible = computed(() => state.value === 'stale')
  /** 模态是否可见：blocked 不可推迟。 */
  const modalVisible = computed(() => state.value === 'blocked')

  /**
   * 服务端 build_time 是否不新于本地（= 部署回滚）。
   *
   * 任一侧时间无法解析时**不判回滚**（回退为「按 build_id 差异提示」）：
   * 旧后端可能不返回 build_time，此时若判回滚就等于永久静默。
   */
  function isRollback(serverTime: string): boolean {
    const server = Date.parse(serverTime)
    const local = Date.parse(localBuildTime.value)
    if (!Number.isFinite(server) || !Number.isFinite(local)) return false
    return server <= local
  }

  /** 把一次成功的检查结果落到状态机上。 */
  function applyVersionInfo(info: SystemVersionInfo): void {
    serverVersion.value = info.version
    serverBuildId.value = info.build_id
    serverBuildTime.value = info.build_time
    notes.value = info.notes
    lastCheckedBuild.value = info.build_id

    // blocked 是终态：页面已经无法正常导航，任何「检查说一切正常」都不能把它降级。
    if (state.value === 'blocked') return

    if (!localBuildId.value) {
      // 本地无 build id（开发服务器 / 未注入）⇒ 无法比对，绝不臆测「你过期了」。
      state.value = 'same'
      return
    }
    if (!info.build_id || info.build_id === localBuildId.value) {
      state.value = 'same'
      return
    }
    if (isRollback(info.build_time)) {
      // 部署回滚：服务端更旧，刷新只会拿到更旧的页面 —— 不提示（§6.4 UX 规则 5）。
      state.value = 'same'
      return
    }
    if (dismissedBuild.value && dismissedBuild.value === info.build_id) {
      // 同一 build 已被「稍后」压制：不主动提示（但 About 入口仍可用）。
      state.value = 'dismissed'
      return
    }
    state.value = 'stale'
  }

  /**
   * 通道 1 落点：读到响应头里的服务端 build。
   *
   * 一致性时**不发请求**（这是最热的路径：每个 /api/* 响应都会走到），
   * 不一致且未针对该 build 检查过才触发一次 check。
   */
  function handleServerBuild(buildId: string): void {
    if (state.value === 'blocked') return
    serverBuildId.value = buildId
    if (!localBuildId.value) return
    if (buildId === localBuildId.value) {
      if (state.value === 'stale') state.value = 'same'
      return
    }
    if (buildId === lastCheckedBuild.value) return
    void check('header')
  }

  /** 进入 blocked（不可推迟）。唯一的「降级」是刷新页面本身。 */
  function markBlocked(detail: string): void {
    blockedDetail.value = detail
    state.value = 'blocked'
  }

  /** 通道 4 落点：模块加载失败 ⇒ 直接 blocked（不可推迟）。 */
  function handleModuleLoadFailure(detail: string): void {
    markBlocked(detail)
    logger.warn('懒加载模块已失效，需要刷新页面', { detail })
  }

  /**
   * 查询一次服务端版本（四通道的公共出口）。
   *
   * 节流分两档：轮询/可见性 → 5 分钟硬节流；响应头/WS/首屏/手动 → 5 秒最小间隔。
   * 并发去重：同一时刻只允许一个请求在飞（避免「一次部署 → N 个请求」）。
   */
  function clearTrailingCheck(): void {
    if (trailingTimer) {
      clearTimeout(trailingTimer)
      trailingTimer = null
    }
    trailingDueAt = 0
  }

  /**
   * 尾沿补跑：本次触发被节流丢弃时，在「剩余节流时间到达」的那一刻补一次。
   *
   * ── 为什么必须有（2026-11-18 E2E-06 实测根因）──────────────────────────
   * 一次部署的真实时序是：用户刚发出某个 /api 请求 → 服务端换了新 build →
   * **下一个**响应带新 `X-EHome-Build` → 触发检查。若此时距上一次检查不足
   * `APP_UPDATE_MIN_GAP_MS`(5s)，旧实现直接 `return` 把这次触发**扔掉且不重试**：
   * 页面就停在「服务端已经换了、本页却没提示」的状态，直到 30 分钟轮询才可能发现。
   * 这正是「活跃用户 ≤1 次请求即提示」被破坏的唯一路径。
   * 尾沿补跑把「丢弃」变成「延后」：5s 最小间隔仍然成立，但**不会丢**。
   *
   * ── 合并规则 ────────────────────────────────────────────────────────────
   * 同一节流窗口内可能连着来多个触发（响应头 + 可见性 + 轮询），它们合并成
   * **边界时刻的一次**检查：只保留最早的到期时间，不堆定时器。
   */
  function scheduleTrailingCheck(source: AppUpdateCheckSource, delayMs: number): void {
    const dueAt = Date.now() + Math.max(0, delayMs)
    if (trailingTimer && trailingDueAt <= dueAt) return
    clearTrailingCheck()
    trailingDueAt = dueAt
    trailingSource = source
    trailingTimer = setTimeout(() => {
      trailingTimer = null
      trailingDueAt = 0
      void check(trailingSource)
    }, Math.max(0, dueAt - Date.now()))
  }

  async function check(source: AppUpdateCheckSource = 'manual'): Promise<void> {
    if (inFlight) return inFlight
    const now = Date.now()
    const gap = now - lastCheckAt.value
    const throttle = source === 'poll' || source === 'visibility'
      ? APP_UPDATE_HARD_THROTTLE_MS
      : APP_UPDATE_MIN_GAP_MS
    if (gap < throttle) {
      // 被节流 ⇒ 不丢，改为边界时刻补跑（尾沿触发）
      scheduleTrailingCheck(source, throttle - gap)
      return
    }

    // 通过节流：待补跑的那次已被本次检查覆盖，取消定时器避免多打一次请求
    clearTrailingCheck()
    lastCheckAt.value = now
    checkCount.value += 1
    inFlight = (async () => {
      try {
        const info = await getSystemVersion()
        applyVersionInfo(info)
        logger.info('版本检查完成', {
          source,
          state: state.value,
          localBuild: localBuildId.value || '(none)',
          serverBuild: info.build_id || '(none)',
        })
      } catch (error) {
        // 旧后端不认识该端点：保持旧行为（无提示），且不再反复重试刷日志。
        //
        // 判据用 **name** 而不是 instanceof：该 store 在测试里会被部分 mock 的
        // `@/api/system` 加载（只提供 getMaintenanceStatus），此时
        // SystemVersionUnsupportedError 是 undefined，instanceof 会抛第二次错，
        // 把「一次无害的检查失败」变成未捕获的 rejection。
        if (isUnsupportedVersionError(error)) {
          logger.info('后端不支持版本端点，跳过更新检测', { source })
          return
        }
        // 其它失败（网络/超时/5xx）：保持现状。**不**把失败当成 same 之外的东西，
        // 也不清除已有提示 —— 部署窗口内 API 可能 502/503（契约 §6.4 实现约束）。
        logger.warn('版本检查失败，维持现状', {
          source,
          error: error instanceof Error ? error.message : String(error),
        })
      } finally {
        inFlight = null
      }
    })()
    return inFlight
  }

  /** 「稍后」：只压制 stale；blocked 不可推迟（按钮本身不该出现在模态上）。 */
  function dismiss(): void {
    if (state.value !== 'stale') return
    const buildId = serverBuildId.value || ''
    dismissedBuild.value = buildId
    writeDismissedBuild(buildId)
    state.value = 'dismissed'
    logger.info('用户选择稍后刷新', { build: buildId })
  }

  /** 用户触发的刷新（唯一出口，绝不自动调用）。 */
  function refreshNow(): void {
    if (typeof window === 'undefined') return
    try {
      window.location.reload()
    } catch {
      /* 单测里 location 可能被替换成只读桩：吞掉即可，不影响状态断言 */
    }
  }

  function openReleaseNotes(): void {
    if (!hasReleaseNotes.value) return
    releaseNotesVisible.value = true
  }

  function closeReleaseNotes(): void {
    releaseNotesVisible.value = false
  }

  function onVisibilityChange(): void {
    if (typeof document === 'undefined') return
    if (document.visibilityState === 'visible') void check('visibility')
  }

  /**
   * 安装四通道监听 + 首屏检查。幂等（App.vue onMounted 调用一次）。
   *
   * 通道：(1) 响应头桥；(2) WS 重连成功；(3) 首屏 + visibilitychange + 30 分钟轮询；
   *       (4) 模块加载失败桥。
   */
  function init(): void {
    if (installed) return
    installed = true

    buildListeners.add(handleServerBuild)
    failureListeners.add(handleModuleLoadFailure)
    if (pendingBuilds.length) {
      const queued = pendingBuilds.splice(0)
      for (const buildId of queued) handleServerBuild(buildId)
    }
    if (pendingFailures.length) {
      const queued = pendingFailures.splice(0)
      for (const detail of queued) handleModuleLoadFailure(detail)
    }

    try {
      stopWs = useWebSocketStore().onConnected(() => { void check('ws') })
    } catch (error) {
      logger.warn('注册 WebSocket 更新检测失败', { error: String(error) })
    }

    if (typeof document !== 'undefined') {
      document.addEventListener('visibilitychange', onVisibilityChange)
    }
    pollTimer = setInterval(() => { void check('poll') }, APP_UPDATE_POLL_INTERVAL_MS)

    void check('boot')
  }

  /** 卸载监听（组件卸载/测试收尾；生产只在 App 卸载时发生）。 */
  function dispose(): void {
    if (!installed) return
    installed = false
    buildListeners.delete(handleServerBuild)
    failureListeners.delete(handleModuleLoadFailure)
    if (stopWs) {
      stopWs()
      stopWs = null
    }
    if (typeof document !== 'undefined') {
      document.removeEventListener('visibilitychange', onVisibilityChange)
    }
    if (pollTimer) {
      clearInterval(pollTimer)
      pollTimer = null
    }
    clearTrailingCheck()
    inFlight = null
  }

  return {
    // 状态
    state,
    serverVersion,
    serverBuildId,
    serverBuildTime,
    notes,
    blockedDetail,
    localBuildId,
    localBuildTime,
    dismissedBuild,
    checkCount,
    releaseNotesVisible,
    // 派生
    displayVersion,
    displayBuildId,
    hasReleaseNotes,
    bannerVisible,
    modalVisible,
    // 动作
    init,
    dispose,
    check,
    dismiss,
    refreshNow,
    openReleaseNotes,
    closeReleaseNotes,
    applyVersionInfo,
    handleServerBuild,
    handleModuleLoadFailure,
    markBlocked,
  }
})
