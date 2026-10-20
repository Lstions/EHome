import { beforeEach, describe, expect, it, vi } from 'vitest'
import {
  HISTORY_FALLBACK_CONCURRENCY,
  HISTORY_RETRY_DELAY_MS,
  useHistoryData,
} from '@/composables/useHistoryData'

/**
 * useHistoryData 修复契约单测（BMS 页慢修复契约 2026-10-20 §2.1 + Lead 补充契约）。
 *
 * 锁五件事（每一条都对应一个真实生产故障）：
 *  ① 超时：batch / 重试 / 降级统一用 timeoutMs（默认 10000，历史 API 覆盖为 30000）；
 *  ② 失败顺序：batch 失败先**原样重试 1 次**，重试仍失败才考虑逐测点降级；
 *  ③ 降级上限：categories > maxFallbackCategories（默认 4）时**不降级**（否则 cells 16 类
 *     会把 1 个失败放大成 16 个重查询 —— 根因 R4 的请求风暴）；降级并发 ≤2；
 *  ④ 失败可见：error ref 置可读文案（含「加载失败」），throwOnError 时抛出纯原因；
 *  ⑤ 取消（ERR_CANCELED / signal.aborted）不置错、不弹 toast、不覆盖旧数据。
 */

const { clientGet, handleError } = vi.hoisted(() => ({
  clientGet: vi.fn(),
  handleError: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  default: { get: (...args: unknown[]) => clientGet(...args) },
  ApiError: class ApiError extends Error {},
  isApiErrorCode: () => false,
}))

vi.mock('@/utils/feedback', () => ({
  feedback: {
    handleError: (...args: unknown[]) => handleError(...args),
    error: vi.fn(),
    success: vi.fn(),
    warning: vi.fn(),
    info: vi.fn(),
  },
}))

const DEVICE = 14
const START = new Date('2026-10-20T00:00:00Z')
const END = new Date('2026-10-21T00:00:00Z')
const BATCH = '/api/v1/unified-data/historical-batch'
const SINGLE = '/api/v1/unified-data/historical'
const POINT_TIME = '2026-10-20T01:02:03.000Z'

function timeoutError(): Error & { code: string } {
  const e = new Error('timeout of 30000ms exceeded') as Error & { code: string }
  e.code = 'ECONNABORTED'
  return e
}

function canceledError(): Error & { code: string } {
  const e = new Error('canceled') as Error & { code: string }
  e.code = 'ERR_CANCELED'
  return e
}

function batchPayload(categories: string[]) {
  return {
    code: 0,
    message: 'ok',
    data: categories.map((category) => ({
      category,
      data: [{ created_at: POINT_TIME, value: 3.3 }],
    })),
  }
}

function singlePayload() {
  return { code: 0, message: 'ok', data: [{ created_at: POINT_TIME, value: 1 }] }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

beforeEach(() => {
  clientGet.mockReset()
  handleError.mockReset()
})

describe('向后兼容：4 参调用与 series/loading 返回契约不变', () => {
  it('成功时 batch 只发一次，series 按 nameMap/unitMap/excludeCategories 映射', async () => {
    clientGet.mockResolvedValue(batchPayload(['a', 'status']))
    const c = useHistoryData({
      nameMap: { a: 'A' },
      unitMap: { a: 'V' },
      excludeCategories: ['status'],
    })
    const series = await c.fetch(DEVICE, ['a', 'status'], START, END)
    expect(series).toEqual([
      { name: 'A', unit: 'V', category: 'a', data: [{ time: POINT_TIME, value: 3.3 }] },
    ])
    expect(c.series.value).toEqual(series)
    expect(c.loading.value).toBe(false)
    expect(c.error.value).toBe('')
    expect(clientGet).toHaveBeenCalledTimes(1)
    expect(clientGet.mock.calls[0][0]).toBe(BATCH)
  })

  it('loading 在请求期间为 true，结束后回落 false', async () => {
    let resolveBatch: (value: unknown) => void = () => {}
    clientGet.mockReturnValue(
      new Promise((resolve) => {
        resolveBatch = resolve
      }),
    )
    const c = useHistoryData()
    const pending = c.fetch(DEVICE, ['a'], START, END)
    expect(c.loading.value).toBe(true)
    resolveBatch(batchPayload(['a']))
    await pending
    expect(c.loading.value).toBe(false)
  })

  it('deviceId 为空 / categories 为空时直接返回 []，不发请求', async () => {
    const c = useHistoryData()
    expect(await c.fetch(0, ['a'], START, END)).toEqual([])
    expect(await c.fetch(DEVICE, [], START, END)).toEqual([])
    expect(clientGet).not.toHaveBeenCalled()
  })
})

describe('超时：batch / 重试 / 降级统一用 timeoutMs', () => {
  it('默认 timeout=10000 透传给 batch（与 client.ts 全局一致）', async () => {
    clientGet.mockResolvedValue(batchPayload(['a']))
    const c = useHistoryData()
    await c.fetch(DEVICE, ['a'], START, END)
    expect(clientGet.mock.calls[0][1].timeout).toBe(10000)
  })

  it('timeoutMs: 30000 同时用于 batch、原样重试与逐测点降级', async () => {
    const calls: Array<{ url: string; timeout: number }> = []
    clientGet.mockImplementation(async (url: string, cfg: { timeout: number }) => {
      calls.push({ url, timeout: cfg.timeout })
      if (url === BATCH) throw timeoutError()
      return singlePayload()
    })
    const c = useHistoryData({ timeoutMs: 30000 })
    const series = await c.fetch(DEVICE, ['c1', 'c2', 'c3'], START, END)
    expect(series.map((s) => s.category)).toEqual(['c1', 'c2', 'c3'])
    expect(calls.filter((call) => call.url === BATCH)).toHaveLength(2) // 首取 + 原样重试一次
    expect(calls.filter((call) => call.url === SINGLE)).toHaveLength(3) // 3 类 ≤ 4 上限 ⇒ 允许降级
    for (const call of calls) expect(call.timeout).toBe(30000)
  })
})

describe('失败顺序：先原样重试 1 次，再按上限决定是否降级', () => {
  it('batch 超时 → 等 HISTORY_RETRY_DELAY_MS 后原样重试；16 类 > 上限 4 ⇒ 不降级、错误可见', async () => {
    const cats = Array.from({ length: 16 }, (_, i) => 'cell_voltage_' + (i + 1))
    const batchTimestamps: number[] = []
    clientGet.mockImplementation(async (url: string) => {
      if (url === BATCH) {
        batchTimestamps.push(Date.now())
        throw timeoutError()
      }
      return singlePayload()
    })
    const c = useHistoryData()
    const startedAt = Date.now()
    const result = await c.fetch(DEVICE, cats, START, END)
    expect(result).toEqual([])
    expect(clientGet.mock.calls.filter((call) => call[0] === BATCH)).toHaveLength(2)
    // cells 16 类：降级会变成 16 个重查询（根因 R4 的请求风暴）⇒ 一律不降级
    expect(clientGet.mock.calls.filter((call) => call[0] === SINGLE)).toHaveLength(0)
    expect(batchTimestamps[1] - batchTimestamps[0]).toBeGreaterThanOrEqual(HISTORY_RETRY_DELAY_MS - 20)
    expect(Date.now() - startedAt).toBeGreaterThanOrEqual(HISTORY_RETRY_DELAY_MS - 20)
    expect(c.error.value).toContain('加载失败')
    expect(c.error.value).toContain('超时')
    expect(handleError).toHaveBeenCalledTimes(1) // 只弹一次，不是每次重试都弹
  })

  it('重试成功则既不降级也不报错（一次偶发抖动被吸收）', async () => {
    let attempts = 0
    clientGet.mockImplementation(async (url: string) => {
      if (url === BATCH) {
        attempts += 1
        if (attempts === 1) throw timeoutError()
        return batchPayload(['c1', 'c2'])
      }
      return singlePayload()
    })
    const c = useHistoryData()
    const series = await c.fetch(DEVICE, ['c1', 'c2'], START, END)
    expect(series.map((s) => s.category)).toEqual(['c1', 'c2'])
    expect(attempts).toBe(2)
    expect(c.error.value).toBe('')
    expect(handleError).not.toHaveBeenCalled()
  })
})

describe('逐测点降级：仅 ≤maxFallbackCategories 时发生，并发 ≤2', () => {
  it('3 类（temp/soc 量级）≤ 默认上限 4：逐类降级且并发 ≤2', async () => {
    let active = 0
    let maxActive = 0
    const singleCats: string[] = []
    clientGet.mockImplementation(async (url: string, cfg: { params: { category?: string } }) => {
      if (url === BATCH) throw timeoutError()
      active += 1
      maxActive = Math.max(maxActive, active)
      await sleep(10)
      active -= 1
      const cat = String(cfg.params.category)
      singleCats.push(cat)
      if (cat === 'c2') throw timeoutError()
      return singlePayload()
    })
    const c = useHistoryData()
    const series = await c.fetch(DEVICE, ['c1', 'c2', 'c3'], START, END)
    expect(HISTORY_FALLBACK_CONCURRENCY).toBe(2)
    expect(maxActive).toBeLessThanOrEqual(HISTORY_FALLBACK_CONCURRENCY)
    expect(singleCats).toHaveLength(3)
    // 部分成功不算整体失败：有数据的类型正常返回，失败的类型缺失
    expect(series.map((s) => s.category).sort()).toEqual(['c1', 'c3'])
    expect(c.error.value).toBe('')
  })

  it('maxFallbackCategories 可调：5 类 <= 5 时才降级', async () => {
    clientGet.mockImplementation(async (url: string) => {
      if (url === BATCH) throw timeoutError()
      return singlePayload()
    })
    const c = useHistoryData({ maxFallbackCategories: 5 })
    const series = await c.fetch(DEVICE, ['c1', 'c2', 'c3', 'c4', 'c5'], START, END)
    expect(series).toHaveLength(5)
    expect(clientGet.mock.calls.filter((call) => call[0] === SINGLE)).toHaveLength(5)
  })

  it("fallback: 'none' 时完全不降级（只重试 1 次后报错）", async () => {
    clientGet.mockImplementation(async () => {
      throw timeoutError()
    })
    const c = useHistoryData({ fallback: 'none' })
    const series = await c.fetch(DEVICE, ['c1'], START, END)
    expect(series).toEqual([])
    expect(clientGet.mock.calls.filter((call) => call[0] === SINGLE)).toHaveLength(0)
    expect(clientGet.mock.calls.filter((call) => call[0] === BATCH)).toHaveLength(2)
    expect(c.error.value).toContain('加载失败')
  })

  it('逐类降级也全部失败 ⇒ 整体错误可见（不是静默空数据）', async () => {
    clientGet.mockImplementation(async () => {
      throw timeoutError()
    })
    const c = useHistoryData({ maxFallbackCategories: 2 })
    const series = await c.fetch(DEVICE, ['c1', 'c2'], START, END)
    expect(series).toEqual([])
    expect(c.error.value).toContain('加载失败')
    expect(c.error.value).toContain('超时')
    expect(handleError).toHaveBeenCalledTimes(1)
  })
})

describe('失败可见性：throwOnError 把纯原因抛给调用方', () => {
  it('throwOnError: true 时 reject 可读原因（诊断卡据此渲染「加载失败 + 重试」）', async () => {
    clientGet.mockImplementation(async () => {
      throw timeoutError()
    })
    const c = useHistoryData({ throwOnError: true, maxFallbackCategories: 0 })
    await expect(c.fetch(DEVICE, ['c1'], START, END)).rejects.toThrow(
      '请求超时（10 秒），请稍后重试',
    )
    expect(c.error.value).toContain('加载失败')
    expect(c.loading.value).toBe(false)
  })
})

describe('取消（AbortController）：不置错、不弹 toast、不覆盖旧数据', () => {
  it('signal 透传到请求配置；abort 后返回旧数据且 error 保持为空', async () => {
    clientGet.mockResolvedValueOnce(batchPayload(['old']))
    const c = useHistoryData()
    await c.fetch(DEVICE, ['old'], START, END)

    const controller = new AbortController()
    clientGet.mockImplementationOnce(
      (_url: string, cfg: { signal?: AbortSignal }) =>
        new Promise((_resolve, reject) => {
          cfg.signal?.addEventListener('abort', () => reject(canceledError()))
        }),
    )
    const pending = c.fetch(DEVICE, ['new'], START, END, { signal: controller.signal })
    await sleep(0)
    const cancelCall = clientGet.mock.calls[1]
    expect(cancelCall[1].signal).toBe(controller.signal)

    controller.abort()
    const result = await pending
    expect(result.map((s) => s.category)).toEqual(['old']) // 返回已有数据，不清空
    expect(c.error.value).toBe('')
    expect(c.loading.value).toBe(false)
    expect(handleError).not.toHaveBeenCalled()
  })

  it('第 5 参可省略（现有 4 参调用完全不变）', async () => {
    clientGet.mockResolvedValue(batchPayload(['a']))
    const c = useHistoryData()
    await c.fetch(DEVICE, ['a'], START, END)
    expect(clientGet.mock.calls[0][1].signal).toBeUndefined()
  })
})
