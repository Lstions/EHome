import { getCurrentInstance, onUnmounted, ref, type Ref } from 'vue'
import type { BackfillProgress } from '@/types/system'
import { getBackfillStatus } from '@/api/system'
import { BACKFILL_POLL_MS, isBackfillTerminal } from './backfill'

/**
 * 历史数据回填进度的轮询（契约 §4.6）。
 *
 * ── 为什么这是**独立**于指标轮询的一条轮询 ────────────────────────────
 * Monitor 页已有的指标轮询默认 10s、用户可改（10/30/60s）。回填进度若挂在它上面：
 *   · 用户把间隔调成 60s ⇒ 一个 20~40 分钟的任务只刷新几十次，进度条看起来是「一跳一跳」的；
 *   · 用户把自动刷新关掉 ⇒ 回填进度**永不更新**，卡片卡在最初的百分比上，
 *     这比不显示更糟（用户会以为回填卡住了）。
 * 回填进度是**有明确起止的一次性任务**，与「实时指标」的刷新语义不同，故单列。
 *
 * ── 为什么用 setTimeout 链而不是 setInterval ─────────────────────────
 * 与 useStartupGate 同一决策：后端在回填期间要跑批量编码 + 逐帧写入，
 * 偶发一次请求超过 5s 完全可能。setInterval 会在慢请求未返回时继续叠加新请求
 * （后端越慢 → 前端压得越狠）。链式 setTimeout 天然串行，且「下一次」在
 * 本次落地后才排，不会堆积。
 *
 * ── 失败处理 ──────────────────────────────────────────────────────────
 * `getBackfillStatus` 内部已把所有失败归约成 null（见其注释）。这里只处理 null：
 * 它可能是①引擎切换瞬间的 503、②用户未登录的 401、③旧后端 404。
 * 三者对用户是同一件事 —— 继续用系统 —— 故不区分，但**不立刻停轮询**：
 * ①是毫秒级的窗口，停掉就再也起不来了。改为记连续失败次数，超过
 * {@link MAX_CONSECUTIVE_NULLS} 才认定「这个接口确实不可用」并停止。
 */

/**
 * 连续多少次拿不到快照就停止轮询。
 *
 * 24 × 5s = **2 分钟**。定这个数的依据：
 * · 引擎切换窗口（唯一正常会失败的时刻）是毫秒级，2 分钟远超它；
 * · 若 2 分钟都拿不到，只可能是「后端没有这个接口」或「用户长期未登录」，
 *   两种情况都不会自愈，继续请求只是无谓开销；
 * · 卡片在 progress 为 null 时本就隐藏，用户看不到任何异常态。
 */
export const MAX_CONSECUTIVE_NULLS = 24

export interface BackfillProgressOptions {
  /** 取数函数（单测注入）；默认 getBackfillStatus。 */
  fetch?: () => Promise<BackfillProgress | null>
  /** 轮询间隔；默认 {@link BACKFILL_POLL_MS}。 */
  pollMs?: number
  /** 排程器（单测注入）；默认 setTimeout。 */
  schedule?: (fn: () => void, ms: number) => ReturnType<typeof setTimeout>
  /** 取消排程（单测注入）；默认 clearTimeout。 */
  cancel?: (handle: ReturnType<typeof setTimeout>) => void
}

export interface BackfillProgressState {
  /** 最近一次拿到的快照；从未拿到为 null。 */
  progress: Ref<BackfillProgress | null>
  /** 是否仍在轮询（终态或失败次数超限后为 false）。 */
  polling: Ref<boolean>
  /** 已连续拿到 null 的次数（单测断言用）。 */
  nullStreak: Ref<number>
  /** 立即拉一次（不经排程）。 */
  refresh: () => Promise<void>
  /** 启动轮询（幂等）。 */
  start: () => void
  /** 停止轮询（幂等）。 */
  stop: () => void
}

export function useBackfillProgress(options: BackfillProgressOptions = {}): BackfillProgressState {
  const fetcher = options.fetch ?? getBackfillStatus
  const pollMs = options.pollMs ?? BACKFILL_POLL_MS
  const schedule = options.schedule ?? ((fn, ms) => setTimeout(fn, ms))
  const cancel = options.cancel ?? ((handle) => clearTimeout(handle))

  const progress = ref<BackfillProgress | null>(null)
  const polling = ref(false)
  const nullStreak = ref(0)

  /** 已排程的句柄（null 表示没有待执行的 tick）。 */
  let timer: ReturnType<typeof setTimeout> | null = null
  /** 组件已卸载 / 已 stop：之后任何回调都不得再排下一次。 */
  let disposed = false

  const clearTimer = () => {
    if (timer !== null) {
      cancel(timer)
      timer = null
    }
  }

  const stop = () => {
    disposed = true
    polling.value = false
    clearTimer()
  }

  /**
   * 依据**最新一次**的快照决定是否继续。
   *
   * 判据集中在这一处，避免「refresh 里判一次、tick 里再判一次」两份真相。
   * 返回 true = 继续轮询。
   */
  const shouldContinue = (): boolean => {
    if (disposed) return false
    // 终态（done/idle/disabled）不再变，没有继续问的必要。
    if (isBackfillTerminal(progress.value?.state)) return false
    // 拿不到快照的容忍上限（见 MAX_CONSECUTIVE_NULLS）。
    if (nullStreak.value >= MAX_CONSECUTIVE_NULLS) return false
    return true
  }

  const scheduleNext = () => {
    clearTimer()
    if (!shouldContinue()) {
      polling.value = false
      return
    }
    polling.value = true
    timer = schedule(() => {
      timer = null
      void tick()
    }, pollMs)
  }

  const refresh = async (): Promise<void> => {
    let next: BackfillProgress | null = null
    try {
      next = await fetcher()
    } catch {
      // fetcher 默认实现不抛（失败即 null）；注入的实现可能抛 —— 同样按「拿不到」处理，
      // 绝不让一次取数失败把整页带崩（回填是辅助信息，见 api/system.ts 的说明）。
      next = null
    }
    if (disposed) return
    if (next) {
      progress.value = next
      nullStreak.value = 0
    } else {
      nullStreak.value += 1
    }
  }

  const tick = async (): Promise<void> => {
    await refresh()
    scheduleNext()
  }

  const start = () => {
    if (disposed) return
    if (timer !== null || polling.value) return
    polling.value = true
    void tick()
  }

  // 组件卸载时自动停：否则页面切走后轮询会一直跑（定时器泄漏 + 无谓请求）。
  // getCurrentInstance() 判空是为了支持在组件外调用（单测直接调 useBackfillProgress()）。
  if (getCurrentInstance()) {
    onUnmounted(stop)
  }

  return { progress, polling, nullStreak, refresh, start, stop }
}
