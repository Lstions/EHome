import { describe, expect, it } from 'vitest'
import {
  getHistoryQueueStats,
  MAX_CONCURRENT_HISTORY,
  withHistorySlot,
} from '@/composables/useHistoryQueue'

/**
 * 页面级历史请求并发闸门（BMS 慢修复契约 §2.2 单测）。
 *
 * 锁三件事：
 *  ① 上限是 3（设计文档 §4「并发 2~3，不要一次 6 个」）；
 *  ② 超出的任务真的排队、FIFO 补位，不会超发；
 *  ③ 任务抛错也释放名额（finally）—— 否则一次失败会永久锁死后续所有历史请求。
 */

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

describe('useHistoryQueue：页面级历史请求并发闸门', () => {
  it('MAX_CONCURRENT_HISTORY 固定为 3（plan §4）', () => {
    expect(MAX_CONCURRENT_HISTORY).toBe(3)
  })

  it('6 个任务同时提交：在途最多 3 个，其余排队后全部完成', async () => {
    let active = 0
    let maxActive = 0
    const started: number[] = []
    const tasks = Array.from({ length: 6 }, (_, i) =>
      withHistorySlot(async () => {
        active += 1
        maxActive = Math.max(maxActive, active)
        started.push(i)
        await sleep(10)
        active -= 1
        return i
      }),
    )
    const results = await Promise.all(tasks)
    expect(maxActive).toBe(3)
    expect(results).toEqual([0, 1, 2, 3, 4, 5])
    // 前 3 个立刻拿到名额（FIFO），第 4 个不会与它们并发
    expect(started.slice(0, 3).sort()).toEqual([0, 1, 2])
    expect(getHistoryQueueStats()).toEqual({ active: 0, waiting: 0 })
  })

  it('异常释放名额：任务 reject 后队列仍可继续（不会永久锁死）', async () => {
    const failing = withHistorySlot(async () => {
      await sleep(5)
      throw new Error('boom')
    })
    await expect(failing).rejects.toThrow('boom')
    expect(getHistoryQueueStats()).toEqual({ active: 0, waiting: 0 })

    // 3 个名额应全部恢复可用（若异常没释放，这里会永远挂着）
    let concurrent = 0
    let maxConcurrent = 0
    await Promise.all(
      [0, 1, 2].map(() =>
        withHistorySlot(async () => {
          concurrent += 1
          maxConcurrent = Math.max(maxConcurrent, concurrent)
          await sleep(5)
          concurrent -= 1
        }),
      ),
    )
    expect(maxConcurrent).toBe(3)
    expect(getHistoryQueueStats()).toEqual({ active: 0, waiting: 0 })
  })

  it('排队任务按 FIFO 顺序补位（不会插队/饿死）', async () => {
    const order: number[] = []
    const holders = [0, 1, 2].map((i) =>
      withHistorySlot(async () => {
        await sleep(15)
        order.push(i)
      }),
    )
    // 等 3 个名额都被占住，再提交 3 个（必然排队）
    await sleep(0)
    expect(getHistoryQueueStats().waiting).toBe(0)
    const queued = [3, 4, 5].map((i) =>
      withHistorySlot(async () => {
        order.push(i)
      }),
    )
    expect(getHistoryQueueStats().waiting).toBe(3)
    await Promise.all([...holders, ...queued])
    expect(order.filter((i) => i >= 3)).toEqual([3, 4, 5])
    expect(order.length).toBe(6)
  })
})
