import { ref, readonly, type Ref } from 'vue'

/**
 * 全局路由进度状态（轻量自实现，替代 nprogress，零新依赖）。
 * 模块级单例：仅内部持有状态，使用方通过 useRouteProgress() 订阅。
 *
 * 约定（P0-B）：
 * - start()   —— 路由切换开始（router.beforeEach），进度条从顶部出现
 * - advance() —— 由内部 tick 缓慢自动推进（模拟网络不确定性），最终停在 90% 以内
 * - done()    —— 路由切换完成（router.afterEach），进度条快速收满并淡出
 * - fail()    —— 路由切换失败（router.onError），与 done() 同样收敛（不会卡死）
 * - reset()   —— 立即重置到隐藏态（过渡层等需要强制退出时使用）
 * - isBusy()  —— 查询当前是否有进行中的导航（登录过渡层据此等待路由解析）
 *
 * 实现细节：
 * - busy 标志防重入：导航进行中再次 start() 不重置进度（避免连续跳转抖动）。
 * - tick 衰减推进：200ms 一跳，越接近上限增量越小，保证进度条一直在动但
 *   不会在路由真正完成前走完（上限 85%，与 router 完成信号解耦）。
 * - done/fail 后 250ms 再隐藏，给顶部细条的收尾动画留出播放时间。
 */
const progress: Ref<number> = ref(0)
const visible: Ref<boolean> = ref(false)
const busy: Ref<boolean> = ref(false)

let timer: ReturnType<typeof setInterval> | null = null
let hideTimer: ReturnType<typeof setTimeout> | null = null

const TICK_MS = 200
const HIDE_DELAY_MS = 250
const SOFT_CAP = 85

/** 每次 tick 的推进量随进度衰减（30→8、60→4、85→1.5、85+→0.6） */
function tickIncrement(current: number): number {
  if (current < 30) return 8
  if (current < 60) return 4
  if (current < SOFT_CAP) return 1.5
  return 0.6
}

function stopTimer() {
  if (timer) {
    clearInterval(timer)
    timer = null
  }
  if (hideTimer) {
    clearTimeout(hideTimer)
    hideTimer = null
  }
}

function start() {
  if (busy.value) return // 防重入：切换过程中再次 start 不重置
  busy.value = true
  visible.value = true
  progress.value = 0
  stopTimer()
  timer = setInterval(() => {
    progress.value = Math.min(SOFT_CAP, progress.value + tickIncrement(progress.value))
  }, TICK_MS)
}

function finish() {
  stopTimer()
  if (progress.value < 100) progress.value = 100
  hideTimer = setTimeout(() => {
    visible.value = false
    progress.value = 0
    busy.value = false
  }, HIDE_DELAY_MS)
}

function done() {
  if (!busy.value) return // 非活跃状态忽略（例如无重定向的普通导航）
  finish()
}

function fail() {
  if (!busy.value) return // 与 done() 相同约束：非活跃导航不干扰
  finish()
}

function reset() {
  stopTimer()
  visible.value = false
  progress.value = 0
  busy.value = false
}

export function useRouteProgress() {
  return {
    progress: readonly(progress),
    visible: readonly(visible),
    start,
    done,
    fail,
    reset,
    isBusy: () => busy.value,
  }
}

/** 供测试直接断言模块级单例状态 */
export function __routeProgressState() {
  return { progress, visible, busy }
}
