import { onBeforeUnmount, onMounted, ref, type Ref } from 'vue'
import type { MaintenanceSnapshot } from '@/types/system'
import { MaintenanceUnsupportedError, getMaintenanceStatus } from '@/api/system'
import {
  PROBE_GRACE_MS,
  WATCHDOG_POLL_MS,
  httpStatusOf,
  isAuthDenial,
  maintenancePollDelay,
} from './progress'

/**
 * 启动期门禁（App.vue 顶层使用，契约 §4）。
 *
 * 语义：应用启动时先探一次后端维护态，**ready 之前绝不渲染业务路由**，
 * 就绪后自动 `location.reload()` 回到正常入口。三个阶段：
 *
 *   probing     —— 首次探测尚未返回，且宽限期（{@link PROBE_GRACE_MS}）未过。
 *                  此阶段**不渲染业务内容**、也不渲染维护页：正常启动时探测只需几十毫秒，
 *                  若一上来就渲染维护页，每次正常打开应用都会闪一帧「系统升级中」。
 *                  但也不能白屏 —— App.vue 在该阶段渲染轻量启动骨架，
 *                  故「无内容可见」的上限就是这 800ms。
 *   maintenance —— 后端明确未就绪 / 探测失败 / 启动失败。渲染维护页并按退避轮询。
 *   app         —— 后端就绪，或判定为「维护功能不适用」（见下 §fail-open）。渲染正常应用。
 *
 * 四个反直觉但必须遵守的细节（都来自契约与实测）：
 *
 * 1. **就绪判据只用 `ready`**，不用 `percent === 100`：失败时 percent 会停在中间值，
 *    用百分比判断会把「启动失败」误判为「尚未就绪」，永远进不去、也永远退不出来。
 *
 * 2. **轮询不因连续失败而停止**：后端 Fatal 之后会退出进程，恢复后需重新拉起。
 *    停止轮询 = 页面永远停在维护页，用户只能手刷。故只做**退避降频**
 *    （2s → 3s → 5s 封顶），永不终止。
 *
 * 3. **失败时保留上一份快照**：后端 Fatal 退出后连接会断，若把快照清空，
 *    页面会从「启动失败：xxx」退化成一片「连接中」，把真正的原因抹掉。
 *
 * 4. **首次探测就 ready 时不要 reload**：只有「从维护页转正常」才 reload。
 *    否则每次正常打开应用都整页重载一次，形成自我循环。
 *
 * ── fail-open（两类，理由各自独立，见 progress.ts 的注释）─────────────
 * · `MaintenanceUnsupportedError`（后端不认识该接口 / 纯静态托管 / 返回非快照）：
 *   维护功能整体不存在 ⇒ 保持旧行为，直接进应用。否则旧后端与 vite preview
 *   会被永久锁在维护页（既有 e2e 渲染契约用例正是这种环境）。
 * · `isAuthDenial`（401/403）：维护期后端必然回 503 而非 401/403，能收到 401/403
 *   说明请求已穿过维护引擎打到完整引擎的鉴权中间件 ⇒ 后端**已完全就绪**。
 *   此时放行到正常页，并挂一个看门狗（{@link WATCHDOG_POLL_MS}）：
 *   若后端之后又进入维护态（重启），看门狗拿到 ready=false 的快照就把用户**带回来**。
 *   ⚠️ 看门狗只在「拿到合法且 ready=false 的快照」时才切回维护页；
 *   其它错误（网络抖动）不改判 —— 那属于业务页自己的错误横幅职责。
 */

export type StartupGatePhase = 'probing' | 'maintenance' | 'app'

export interface StartupGateOptions {
  /** 探测函数（单测注入）；默认 `getMaintenanceStatus`。 */
  probe?: () => Promise<MaintenanceSnapshot>
  /** 重新加载（单测注入）；默认 `window.location.reload()`。 */
  reload?: () => void
  /** 轮询间隔（单测注入）；默认 `maintenancePollDelay`。 */
  pollDelay?: (consecutiveFailures: number) => number
  /** 首次探测宽限期；默认 `PROBE_GRACE_MS`。 */
  probeGraceMs?: number
}

export interface StartupGate {
  phase: Ref<StartupGatePhase>
  snapshot: Ref<MaintenanceSnapshot | null>
  /** 最近一次探测是否因「连不上服务器」失败（与后端明确报告 failed 区分）。 */
  connectionFailed: Ref<boolean>
  /** 连续失败次数（单测断言退避用）。 */
  failures: Ref<number>
  start: () => void
  stop: () => void
}

export function useStartupGate(options: StartupGateOptions = {}): StartupGate {
  const probe = options.probe ?? getMaintenanceStatus
  const reload = options.reload ?? (() => window.location.reload())
  const pollDelay = options.pollDelay ?? maintenancePollDelay
  const probeGraceMs = options.probeGraceMs ?? PROBE_GRACE_MS

  const phase = ref<StartupGatePhase>('probing')
  const snapshot = ref<MaintenanceSnapshot | null>(null)
  const connectionFailed = ref(false)
  const failures = ref(0)

  let graceTimer: ReturnType<typeof setTimeout> | null = null
  let pollTimer: ReturnType<typeof setTimeout> | null = null
  let started = false
  let stopped = false
  /** reload 已发起：不再接受任何探测结果，避免重复 reload。 */
  let reloading = false
  /** 是否处于 fail-open 后的看门狗模式（后端重入维护态时要把用户带回来）。 */
  let watching = false

  function clearTimers() {
    if (graceTimer) {
      clearTimeout(graceTimer)
      graceTimer = null
    }
    if (pollTimer) {
      clearTimeout(pollTimer)
      pollTimer = null
    }
  }

  function schedulePoll(delay: number) {
    if (stopped || reloading) return
    if (pollTimer) clearTimeout(pollTimer)
    pollTimer = setTimeout(() => {
      pollTimer = null
      void runProbe()
    }, delay)
  }

  function enterApp(watchdog = false) {
    phase.value = 'app'
    connectionFailed.value = false
    watching = watchdog
    clearTimers()
    if (watchdog) schedulePoll(WATCHDOG_POLL_MS)
  }

  function enterMaintenance(current: MaintenanceSnapshot | null, unreachable: boolean) {
    phase.value = 'maintenance'
    watching = false
    if (current) snapshot.value = current
    connectionFailed.value = unreachable
  }

  async function runProbe() {
    if (stopped || reloading) return
    const wasWatching = watching
    try {
      const result = await probe()
      if (stopped || reloading) return
      failures.value = 0
      if (result && result.ready) {
        snapshot.value = result
        if (phase.value === 'maintenance') {
          // 从维护页恢复：整页重载回正常入口（此时 router 尚未加载任何业务路由）
          reloading = true
          clearTimers()
          reload()
          return
        }
        // watch 模式下拿到 ready：继续看着（后端可能再重启）
        if (wasWatching) {
          schedulePoll(WATCHDOG_POLL_MS)
        } else {
          enterApp()
        }
        return
      }
      // 拿到**合法的 ready=false 快照**：后端明确处于维护态 —— 无论此前在哪个阶段都要回到维护页
      enterMaintenance(result ?? null, false)
      schedulePoll(pollDelay(failures.value))
    } catch (error) {
      if (stopped || reloading) return
      if (error instanceof MaintenanceUnsupportedError) {
        // 旧后端 / 纯静态托管：维护功能整体不存在 ⇒ 保持旧行为（直接进应用）
        enterApp()
        return
      }
      if (isAuthDenial(httpStatusOf(error))) {
        // 见文件头 §fail-open 第二条：收到 401/403 即证明后端已就绪
        if (phase.value === 'app') {
          // 已在正常页（看门狗期间）：继续保持，等下一次
          schedulePoll(WATCHDOG_POLL_MS)
        } else {
          enterApp(true)
        }
        return
      }
      failures.value += 1
      if (phase.value === 'app') {
        // 看门狗期间的网络抖动：不把用户拽回维护页（业务页已有自己的错误横幅）
        schedulePoll(WATCHDOG_POLL_MS)
        return
      }
      enterMaintenance(snapshot.value, true)
      schedulePoll(pollDelay(failures.value))
    }
  }

  function start() {
    if (started) return
    started = true
    stopped = false
    phase.value = 'probing'
    graceTimer = setTimeout(() => {
      graceTimer = null
      // 宽限期内没返回：先给用户维护页的 '连接中' 态，而不是继续空白。
      // 此时**尚未收到任何响应**，断言「系统升级中」没有依据（可能只是网络慢/后端未监听），
      // 故置 connectionFailed=true 让文案变成「正在连接服务器」；一旦探测返回真实快照，
      // 这个标志会被刷新（ready=false 的合法快照会把文案换回「系统升级中」）。
      if (!stopped && !reloading && phase.value === 'probing') {
        phase.value = 'maintenance'
        connectionFailed.value = true
      }
    }, probeGraceMs)
    void runProbe()
  }

  function stop() {
    stopped = true
    clearTimers()
  }

  onMounted(start)
  onBeforeUnmount(stop)

  return { phase, snapshot, connectionFailed, failures, start, stop }
}
