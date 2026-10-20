/**
 * 页面级历史数据请求并发闸门（BMS 页慢修复契约 2026-10-20 §2.2）。
 *
 * 为什么需要它：
 *   根因报告 R3 —— BMS 页一次进页会并发打出 5 个重型 historical-batch 查询
 *   （cells 16 类 / metrics 9 类 / protection 30d / temp 3 类 / soc 2 类）+ latest-data，
 *   把 3 核后端与 PG 连接池一次占满，便宜请求被饿死（detail 6ms → 8.6s）。
 *   设计文档 §4 要求「并发上限 2~3，不要一次 6 个」，但此前只有 4 张诊断卡走信号量，
 *   BmsChartsPanel 的两张主图完全不受控。
 *
 * 语义：模块级（页面级）共享计数，所有历史数据请求都经 withHistorySlot 排队。
 *   - 在途 ≤ MAX_CONCURRENT_HISTORY 时立即执行；
 *   - 超过上限时 FIFO 排队，前一个释放名额后按序补位；
 *   - 任务抛错/被取消也一定释放名额（finally），不会永久锁死队列。
 *
 * 为什么不按组件实例计数：BMS 页同时挂 4 张诊断卡 + 2 张主图，它们分属不同组件实例，
 * 只在页面级共享一个上限才等价于「页面级历史请求并发 ≤3」。
 */

/** 同时在途的历史数据请求上限（设计文档 §4：2~3） */
export const MAX_CONCURRENT_HISTORY = 3

let activeCount = 0
const waiting: Array<() => void> = []

function release(): void {
  activeCount = Math.max(0, activeCount - 1)
  const next = waiting.shift()
  if (next) next()
}

/**
 * 在并发名额内执行历史请求（超出上限时排队）。仅用于历史数据请求，不要包裹上传/写入类长任务。
 *
 * 有名额时**同步**开始执行 task（async 函数体在第一个 await 前同步跑）：
 * 这样 useHistoryData 的 loading 置位与调用方在同一个 tick，UI 不会先闪一帧「暂无数据」。
 */
export async function withHistorySlot<T>(task: () => Promise<T>): Promise<T> {
  if (activeCount < MAX_CONCURRENT_HISTORY) {
    activeCount += 1
    try {
      return await task()
    } finally {
      release()
    }
  }
  // 名额已满：排队，等 release() 唤醒（唤醒时同步占名额，保证不会超发）
  await new Promise<void>((resolve) => {
    waiting.push(() => {
      activeCount += 1
      resolve()
    })
  })
  try {
    return await task()
  } finally {
    release()
  }
}

/**
 * 只读观测：当前在途 / 排队数。
 * 供单测断言并发上限（含异常释放）与线上诊断使用，**不要**在生产逻辑里依赖它做控制。
 */
export function getHistoryQueueStats(): { active: number; waiting: number } {
  return { active: activeCount, waiting: waiting.length }
}
