import { ref } from 'vue'
import { feedback } from '@/utils/feedback'
import client from '@/api/client'

export interface HistoryDataPoint {
  time: string
  value: number
}

export interface HistorySeries {
  name: string
  unit: string
  category: string
  data: HistoryDataPoint[]
}

/** fetch 的第 5 个参数（可选，现有 4 参调用完全不变） */
export interface FetchHistoryOptions {
  /** 取消信号：时间范围切换 / 组件卸载时 abort 旧请求（axios 透传到 XHR） */
  signal?: AbortSignal
}

/** batch 失败后的降级方式：'per-category' = 逐测点重发（默认）；'none' = 只报错不降级 */
export type HistoryFallbackMode = 'per-category' | 'none'

export interface UseHistoryDataOptions {
  /** Map category → display name */
  nameMap?: Record<string, string>
  /** Map category → unit string */
  unitMap?: Record<string, string>
  /** Categories to exclude from results (e.g. status fields) */
  excludeCategories?: string[]
  /** Max data points per category (server-side downsampling) */
  maxPoints?: number
  /** Error message prefix */
  errorPrefix?: string
  /** Whether to show ElMessage on error (set false when caller handles errors itself) */
  showError?: boolean
  /** 单次历史请求超时（毫秒）。batch / 重试 / 逐测点降级都使用它。默认 10000（与 client.ts 全局一致）。 */
  timeoutMs?: number
  /** batch 失败后的降级方式，默认 'per-category'（保持既有行为）。 */
  fallback?: HistoryFallbackMode
  /**
   * 允许逐测点降级的 category 数量上限（默认 4）。
   * 根因报告 R4：一次 batch 失败后按 category 逐个重发，cells(16)/metrics(9) 会把
   * 1 个失败放大成 16/9 个重查询（浏览器实测一次进页 22 个降级请求、8 个再次超时）；
   * 因此超过上限时一律不降级，改成「明确错误态 + 用户点重试」。
   */
  maxFallbackCategories?: number
  /**
   * 最终失败时是否 throw（默认 false：只置 error ref 并返回 []，保持既有调用契约）。
   * true 时抛出的 Error.message 是**纯原因**（如「请求超时（30 秒），请稍后重试」），
   * 由调用方（如诊断卡）自己拼「XX 加载失败：」前缀，避免双层前缀。
   */
  throwOnError?: boolean
}

/** batch 失败后「原样重试一次」的等待时间（契约：300–500ms；给服务端一点喘息，避免立刻二次超时） */
export const HISTORY_RETRY_DELAY_MS = 400
/** 逐测点降级的最大并发（契约：≤2；不能用 Promise.all 一次全打出去） */
export const HISTORY_FALLBACK_CONCURRENCY = 2

const BATCH_URL = '/api/v1/unified-data/historical-batch'
const SINGLE_URL = '/api/v1/unified-data/historical'

/**
 * Encapsulates the batch-API → retry-once → per-category-fallback → item-mapping pattern
 * shared by BmsCellVoltageHistoryChart / HistoryChartSection / BmsChartsPanel / diag cards.
 *
 * 失败处理顺序（BMS 慢修复契约 §2.1 + Lead 补充契约 2026-10-20）：
 *   ① batch 失败（非取消）→ 同一 timeoutMs/signal **原样重试 1 次**（等 HISTORY_RETRY_DELAY_MS）；
 *   ② 重试仍失败 → 仅当 fallback==='per-category' 且 categories.length <= maxFallbackCategories
 *      时才逐测点降级（并发 ≤2）；否则直接置 error（throwOnError 时抛）；
 *   ③ 逐测点降级全部失败 → 置 error。
 * 取消（ERR_CANCELED / signal.aborted）不算失败：不置 error、不弹 toast、不覆盖已有数据。
 *
 * Usage:
 *   const { series, loading, error, fetch } = useHistoryData({ nameMap, unitMap })
 *   await fetch(deviceId, categories, startTime, endTime)                 // 旧 4 参调用
 *   await fetch(deviceId, categories, startTime, endTime, { signal })     // 可取消
 */
export function useHistoryData(options: UseHistoryDataOptions = {}) {
  const {
    nameMap = {},
    unitMap = {},
    excludeCategories = [],
    maxPoints = 500,
    errorPrefix = '获取历史数据失败',
    showError = true,
    timeoutMs = 10000,
    fallback = 'per-category',
    maxFallbackCategories = 4,
    throwOnError = false,
  } = options

  const series = ref<HistorySeries[]>([])
  const loading = ref(false)
  /** 最近一次请求的可读失败原因（空串 = 成功 / 进行中 / 已取消） */
  const error = ref('')

  function mapItem(item: any): HistoryDataPoint {
    return { time: item.created_at || item.timestamp, value: item.value }
  }

  function buildSeries(cat: string, data: any[]): HistorySeries | null {
    if (excludeCategories.includes(cat)) return null
    const items = (data || []).map(mapItem)
    if (items.length === 0) return null
    return {
      name: nameMap[cat] || cat,
      unit: unitMap[cat] || '',
      category: cat,
      data: items,
    }
  }

  /** 是否属于「主动取消」（axios 取消被 client.ts 的响应拦截器包成 ApiError，故多路判定） */
  function isCanceled(e: unknown, signal?: AbortSignal): boolean {
    // 自己 abort 的请求一定带 signal.aborted —— 这是最可靠的一路
    if (signal?.aborted) return true
    const anyErr = e as { code?: unknown; name?: unknown; message?: unknown } | null | undefined
    if (anyErr?.code === 'ERR_CANCELED') return true
    if (anyErr?.name === 'CanceledError' || anyErr?.name === 'AbortError') return true
    return typeof anyErr?.message === 'string' && /cancel(?:l)?ed|aborted/i.test(anyErr.message)
  }

  /** 异常 → 一句给用户看的原因（不含「哪个操作」上下文；上下文由 failHead 补） */
  function reasonOf(e: unknown): string {
    const anyErr = e as
      | { code?: unknown; message?: unknown; response?: { data?: { message?: unknown; msg?: unknown } } }
      | null
      | undefined
    const responseMessage = anyErr?.response?.data?.message ?? anyErr?.response?.data?.msg
    const raw =
      typeof responseMessage === 'string' && responseMessage
        ? responseMessage
        : typeof anyErr?.message === 'string'
          ? anyErr.message
          : ''
    if (anyErr?.code === 'ECONNABORTED' || /timeout/i.test(raw)) {
      const seconds = Math.max(1, Math.round(timeoutMs / 1000))
      return `请求超时（${seconds} 秒），请稍后重试`
    }
    if (raw && !/cancel(?:l)?ed|aborted/i.test(raw)) return raw
    return '请求失败，请稍后重试'
  }

  /** 失败文案头：保证 error ref 里出现「加载失败」（契约 §2.1） */
  function failHead(): string {
    if (errorPrefix.includes('加载失败')) return errorPrefix
    // 「获取电芯电压历史数据失败」→「获取电芯电压历史数据加载失败」
    if (errorPrefix.endsWith('失败')) return errorPrefix.slice(0, -'失败'.length) + '加载失败'
    return '历史数据加载失败'
  }

  async function requestBatch(
    deviceId: number | string,
    categories: string[],
    startTime: Date,
    endTime: Date,
    signal?: AbortSignal,
  ): Promise<HistorySeries[]> {
    const batchRes = await client.get<unknown, any>(BATCH_URL, {
      params: {
        device_pk: deviceId,
        categories: categories.join(','),
        start_time: startTime.toISOString(),
        end_time: endTime.toISOString(),
        max_points: maxPoints,
      },
      timeout: timeoutMs,
      signal,
    })
    const collected: HistorySeries[] = []
    const batchData = batchRes?.data || batchRes || []
    if (Array.isArray(batchData)) {
      for (const entry of batchData) {
        const cat = entry.category
        if (!cat) continue
        const s = buildSeries(cat, entry.data || [])
        if (s) collected.push(s)
      }
    }
    return collected
  }

  async function requestOneCategory(
    deviceId: number | string,
    cat: string,
    startTime: Date,
    endTime: Date,
    signal?: AbortSignal,
  ): Promise<HistorySeries | null> {
    const res = await client.get<unknown, any>(SINGLE_URL, {
      params: {
        device_pk: deviceId,
        category: cat,
        start_time: startTime.toISOString(),
        end_time: endTime.toISOString(),
        max_points: maxPoints,
      },
      timeout: timeoutMs,
      signal,
    })
    return buildSeries(cat, ((res as any)?.data ?? res ?? []) as any[])
  }

  /**
   * 逐测点降级（并发 ≤2，按 category 顺序回填）。
   * 单个失败不抛出（部分成功仍可用），全部失败由调用方判为整体失败。
   */
  async function requestFallback(
    deviceId: number | string,
    categories: string[],
    startTime: Date,
    endTime: Date,
    signal?: AbortSignal,
  ): Promise<{ series: HistorySeries[]; failed: number; firstError: unknown }> {
    const slots: Array<HistorySeries | null> = new Array(categories.length).fill(null)
    let cursor = 0
    let failed = 0
    let firstError: unknown = null
    const worker = async (): Promise<void> => {
      while (cursor < categories.length) {
        const index = cursor++
        const cat = categories[index]
        try {
          slots[index] = await requestOneCategory(deviceId, cat, startTime, endTime, signal)
        } catch (e) {
          // 取消：立即中断整个降级轮次（signal 已 abort，其余在途请求也会被取消）
          if (isCanceled(e, signal)) throw e
          failed += 1
          if (firstError === null) firstError = e
        }
      }
    }
    const workerCount = Math.min(HISTORY_FALLBACK_CONCURRENCY, categories.length)
    await Promise.all(Array.from({ length: workerCount }, () => worker()))
    return { series: slots.filter((s): s is HistorySeries => s !== null), failed, firstError }
  }

  /** 可被 abort 打断的等待（重试前的退避；卸载/切范围时不必白等 400ms） */
  function waitForRetry(signal?: AbortSignal): Promise<void> {
    return new Promise<void>((resolve) => {
      if (signal?.aborted) {
        resolve()
        return
      }
      let timer: ReturnType<typeof setTimeout> | undefined
      const onAbort = () => {
        if (timer !== undefined) clearTimeout(timer)
        resolve()
      }
      timer = setTimeout(() => {
        signal?.removeEventListener('abort', onAbort)
        resolve()
      }, HISTORY_RETRY_DELAY_MS)
      signal?.addEventListener('abort', onAbort, { once: true })
    })
  }

  /**
   * 请求序号：只有**最新一次** fetch 可以写 loading / series / error。
   *
   * 为什么需要（评审 2026-10-21 发现的真实竞态）：BmsChartsPanel 刷新时先
   * abort 旧请求再立即发新请求，而旧请求的 catch/finally 在微任务里稍后执行；
   * 若无护栏，旧请求的 finally 会把**新请求**的 loading 置回 false，模板随即
   * 落到 error / 空数据分支 —— 刷新期间出现「暂无数据」或旧图无加载指示。
   * 取消本身仍然不置错、不弹 toast、不覆盖旧数据。
   */
  let requestSeq = 0

  async function fetch(
    deviceId: number | string,
    categories: string[],
    startTime: Date,
    endTime: Date,
    fetchOptions: FetchHistoryOptions = {},
  ): Promise<HistorySeries[]> {
    const signal = fetchOptions.signal
    if (!deviceId || categories.length === 0) return []
    const seq = ++requestSeq
    const isLatest = () => seq === requestSeq
    loading.value = true
    error.value = ''
    try {
      let result: HistorySeries[]
      try {
        result = await requestBatch(deviceId, categories, startTime, endTime, signal)
      } catch (firstError) {
        if (isCanceled(firstError, signal)) return series.value
        // ② 原样重试一次（同一 timeoutMs / signal）——「一次重试 + 明确错误态」比无限降级便宜得多
        await waitForRetry(signal)
        if (signal?.aborted || !isLatest()) return series.value
        try {
          result = await requestBatch(deviceId, categories, startTime, endTime, signal)
        } catch (retryError) {
          if (isCanceled(retryError, signal)) return series.value
          const canFallback = fallback === 'per-category' && categories.length <= maxFallbackCategories
          if (!canFallback) throw retryError
          // ③ 逐测点降级（并发 ≤2）；只有全部失败才算整体失败
          const degraded = await requestFallback(deviceId, categories, startTime, endTime, signal)
          if (degraded.series.length === 0) throw degraded.firstError ?? retryError
          result = degraded.series
        }
      }
      // 已被更新的请求取代：不得覆盖更新请求的状态与数据
      if (!isLatest()) return series.value
      series.value = result
      return result
    } catch (e) {
      // 取消不是失败：不置错、不弹 toast、不覆盖旧数据（切换时间范围时旧请求被 abort 是常态）
      if (isCanceled(e, signal)) return series.value
      if (!isLatest()) return series.value
      const reason = reasonOf(e)
      error.value = `${failHead()}：${reason}`
      if (showError) feedback.handleError(e, errorPrefix)
      if (throwOnError) throw new Error(reason)
      return []
    } finally {
      // 只有最新请求可以结束 loading（旧请求的 finally 不得熄灭新请求的加载态）
      if (isLatest()) loading.value = false
    }
  }

  return { series, loading, error, fetch }
}
