/**
 * 历史数据回填的展示口径（纯函数 + 常量，无 Vue 依赖）。
 *
 * 为什么单独成文件：这批判据要同时被三处消费 —— WS 卡片组件、轮询组合式函数、
 * 单测。写进 .vue 的 <script setup> 里就只能靠挂载组件来测，而「ETA 怎么格式化」
 * 这类纯字符串逻辑用挂载测试既慢又难覆盖边界（0 表示未知、负数、NaN）。
 *
 * 契约：docs/设计/老数据迁移到帧表-分析与契约-2026-11-07.md §4.6
 */

import type { BackfillProgress, BackfillState } from '@/types/system'
import { UNKNOWN } from '@/utils/format'

/**
 * 轮询间隔（毫秒）。
 *
 * 定 5s 的三个理由：
 * 1. 回填的**最小可观测进步**是「一帧」——生产实测 3.42M 帧 / 20~40 分钟
 *    ≈ 1400~2800 帧/秒，5s 内必然有明显变化，更密只是浪费请求；
 * 2. 后端 `Progress()` 每次都重新读保留期配置（`IsRetentionPermanent()`），
 *    不是纯内存读取；10 个标签页 × 1s 轮询 = 10 QPS 的无谓开销；
 * 3. 与 Monitor 页既有的指标刷新节奏（默认 10s）同数量级，用户感知不到延迟差。
 */
export const BACKFILL_POLL_MS = 5000

/** 状态的中文文案。disabled 是运维应急阀（EHOME_BACKFILL_ENABLED=false）的产物。 */
export const BACKFILL_STATE_TEXT: Record<BackfillState, string> = {
  idle: '未开始',
  running: '回填中',
  done: '已完成',
  failed: '已中断',
  disabled: '已关闭',
}

/**
 * 是否**可以停止轮询**的终态。
 *
 * done / idle / disabled 三者都不会再自行变成 running：
 *   · done      —— 全部分区已迁完，除非入选新分区（那是下个版本的事）；
 *   · idle      —— 后台 goroutine 还没把状态翻成 running，且**只有进程重启**才会重来；
 *   · disabled  —— 运维明确关掉了，本次进程内不会启动。
 *
 * `failed` **故意不算终态**：失败的 goroutine 已经返回，但容器重启后
 * `RunOnce` 会重新跑（已完成的分区被跳过、从 watermark 续跑）。继续轮询意味着
 * 卡片能在后端重启后**自动**从「已中断」变回「回填中」，用户不需要手刷 ——
 * 这正是回填「可中断续跑」这个设计承诺在界面上的兑现。
 */
export function isBackfillTerminal(state: BackfillState | string | null | undefined): boolean {
  return state === 'done' || state === 'idle' || state === 'disabled'
}

/**
 * 是否应该把这张卡显示出来。
 *
 * 三种状态需要占用用户的注意力，且**理由各不相同**（这是本函数的核心，不是三个并列的 or）：
 *   · running  —— 有正在进行的工作，用户要能看到「还要多久」；
 *   · failed   —— 历史数据**没迁完**，用户必须知道（否则会以为数据齐了）；
 *   · disabled —— 运维用 `EHOME_BACKFILL_ENABLED=false` 关掉了回填。
 *                 **不能隐藏**：关掉回填意味着老 unified_data **永远不会**被迁到帧表，
 *                 而「数据没迁」这件事恰恰是用户最需要知道的。静默隐藏 =
 *                 让这个事实永不可见，用户对着不完整的帧表发问时页面什么也不说。
 *                 生产会因运维关阀而真实出现此态，故按「信息卡」展示，不按错误展示。
 *
 * done / idle 隐藏：
 *   · done —— 一次性任务已完成，留一张「已完成」的卡只会长期占据首屏；
 *   · idle —— 后台 goroutine 尚未把状态翻成 running（毫秒级），显示反而闪一下。
 */
export function shouldShowBackfill(progress: BackfillProgress | null | undefined): boolean {
  if (!progress) return false
  const state = progress.state
  return state === 'running' || state === 'failed' || state === 'disabled'
}

/**
 * 这张卡是否处于「非错误、但无事可展示进度」的状态（当前只有 disabled）。
 *
 * 单独成一个判据而不是在模板里写 `state === 'disabled'`：将来若新增别的
 * 「不跑但没有错」的状态（如 blocked），只改这一处即可。
 */
export function isBackfillSuppressed(state: BackfillState | string | null | undefined): boolean {
  return state === 'disabled'
}

/**
 * 剩余时间文案。
 *
 * 三档，边界都要有确定文案（不能出现「剩余 0 秒」这种像卡住的表达）：
 *   · <= 0 或缺失 ⇒ {@link UNKNOWN}（后端 0 = 尚未算出，不是「马上完成」）；
 *   · < 60s      ⇒ 「约 45 秒」；
 *   · 其余       ⇒ 「约 23 分钟」/「约 2 小时 5 分钟」。
 */
export function formatEta(seconds: number | null | undefined): string {
  if (typeof seconds !== 'number' || !Number.isFinite(seconds) || seconds <= 0) return UNKNOWN
  if (seconds < 60) return `约 ${Math.round(seconds)} 秒`
  const totalMinutes = Math.round(seconds / 60)
  if (totalMinutes < 60) return `约 ${totalMinutes} 分钟`
  const hours = Math.floor(totalMinutes / 60)
  const minutes = totalMinutes % 60
  return minutes === 0 ? `约 ${hours} 小时` : `约 ${hours} 小时 ${minutes} 分钟`
}

/**
 * 帧数文案。
 *
 * ⚠️ `frames_total` 为 0 表示**未知**（后端注释明说：精确总数要全表
 * count(DISTINCT device_id, timestamp)，成本高且对进度无必要）。
 * 因此绝不能写成「已回填 1,234 / 0」——那看起来像分母错了。
 * 分母未知时只报已回填数。
 */
export function formatFrames(done: number | null | undefined, total: number | null | undefined): string {
  const d = typeof done === 'number' && Number.isFinite(done) ? done : 0
  const doneText = d.toLocaleString('zh-CN')
  const t = typeof total === 'number' && Number.isFinite(total) ? total : 0
  if (t <= 0) return `${doneText} 帧（总数统计中）`
  return `${doneText} / ${t.toLocaleString('zh-CN')} 帧`
}

/** 百分比取整并夹到 0..100（后端理论上已保证，但显示层不能信上游）。 */
export function clampBackfillPercent(value: number | null | undefined): number {
  if (typeof value !== 'number' || !Number.isFinite(value)) return 0
  if (value < 0) return 0
  if (value > 100) return 100
  return Math.round(value)
}
