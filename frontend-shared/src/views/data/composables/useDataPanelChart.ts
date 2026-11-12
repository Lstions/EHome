/**
 * 数据面板「历史趋势图」的取数与视图状态（原 DataPanel.vue 的 buildChartSeries 段原样搬出）。
 *
 * 边界依据（为什么不按"批量请求 / 逐个回退 / 移动端单指标"再切）：
 *   · chartLoading / chartFailed / chartSeries 是同一个请求令牌（chartLoadToken）的产物，
 *     分开后"旧请求不得写回"的防护会散落在多个模块里（拆分前它就因为异步而假空态，
 *     见 buildChartSeries 顶部注释）；
 *   · 移动端单指标（mobileMetric / chartMetricOptions / visibleChartSeries / chartHeight）
 *     完全由 chartSeries 派生，且必须在每次序列重建后 sync —— 属于同一生命周期；
 *   · 本模块不碰分页、不碰表格行，只依赖「设备 + 时间范围 + 已取回的整窗行 + 指标类别」。
 *
 * 保留在页面的：卡片外壳与骨架/错误/空态的**位置**（DOM 结构是门禁判据），
 * 本模块只产出序列与三个状态位。
 */
import { computed, ref, type Ref } from 'vue'
import client from '@/api/client'
import { logger } from '@/utils/logger'
import { sensorNameMap, sensorUnitMap } from '@/utils/sensor'
import { unwrapList } from '../envelope'
import { parseRowData } from '../rowData'
import { CHART_METRIC_PRIORITY } from '../rowDisplay'
import type { CollectedRow } from '../historyRows'
import type { MeasurementBatch, MeasurementCategory, MeasurementPoint } from '../types'

/** 趋势图取数的输入：全部用读取器 / ref 传入，保持"调用时取当前值"的原时序。 */
export interface DataPanelChartSource {
  /** queryForm.deviceId 的读取器。 */
  deviceId: () => number | null
  /** queryForm.timeRange 的读取器（1h / 24h / 7d / 30d）。 */
  timeRange: () => string
  /** 设备可用指标类别（envelope 已解包）。 */
  categories: Ref<MeasurementCategory[]>
  /** 整窗历史行（原 allHistoryRows）：判"有没有数据"与最后兜底建序列都用它。 */
  rows: Ref<CollectedRow[]>
  /** 窄屏判定（useResponsive 的 isMobile，由页面下传，避免重复注册 resize 监听）。 */
  isMobile: Ref<boolean>
}

/**
 * 小工具：把并发压到 limit（趋势图回退请求专用，见下方注释）。
 * 不用 p-limit 之类新依赖，保持零新增包。
 */
async function mapWithConcurrency<T, R>(items: T[], limit: number, worker: (item: T) => Promise<R>): Promise<R[]> {
  const results: R[] = new Array(items.length)
  let cursor = 0
  const run = async () => {
    while (cursor < items.length) {
      const index = cursor
      cursor += 1
      results[index] = await worker(items[index])
    }
  }
  await Promise.all(Array.from({ length: Math.min(limit, items.length) }, () => run()))
  return results
}

export function useDataPanelChart(source: DataPanelChartSource) {
  const chartSeries = ref<any[]>([])

  // ── 趋势图异步态（P0：historical-batch 生产实测挂起 38s+，超时回退还需 10–30s）──
  const chartLoading = ref(false)
  const chartFailed = ref(false)
  /** 只允许最后一次构建写回状态（快速连续查询时防旧请求回写）。 */
  let chartLoadToken = 0
  /** 移动端趋势图当前查看的指标（单指标，避免 4 轴 + 20 图例挤在 303px 画布里）。 */
  const mobileMetric = ref('')

  const buildChartSeries = async () => {
    // P0：趋势序列是**异步**构建的。改前 chartSeries 未就绪就渲染 el-empty「暂无趋势数据」，
    // 而生产实测 historical-batch 挂起 38s+、超时回退还要 10–30s ⇒ 有数据却报"暂无"。
    // 这里显式建模 chartLoading / chartFailed，并用请求令牌防止旧构建写回。
    const token = ++chartLoadToken
    chartLoading.value = true
    chartFailed.value = false
    /** 逐个类别回退时统计"请求本身失败"的条数：全失败且无序列 ⇒ 真失败（错误态）。 */
    let fallbackFailures = 0
    // 判据用 allHistoryRows（整窗）而不是 historyData（当前页）：
    // 趋势图画的是整个时间范围，用当前页判断"有没有数据"会让"第 2 页为空但
    // 第 1 页有数据"的窗口把整张图判成无数据。
    if (source.rows.value.length === 0) {
      chartSeries.value = []
      chartLoading.value = false
      syncMobileMetric()
      return
    }

    // 尝试从 unified_data API 获取解析后的数值数据
    try {
      const endTime = new Date()
      const startTime = new Date()
      switch (source.timeRange()) {
        case '1h':
          startTime.setHours(startTime.getHours() - 1)
          break
        case '24h':
          startTime.setHours(startTime.getHours() - 24)
          break
        case '7d':
          startTime.setDate(startTime.getDate() - 7)
          break
        case '30d':
          startTime.setDate(startTime.getDate() - 30)
          break
      }

      const categoryNames = source.categories.value.map(category => category.code)
      if (categoryNames.length === 0) {
        chartSeries.value = []
        return
      }

      const series: any[] = []

      // 批量请求：1 个请求替代 19 个并行请求，服务端降采样 max_points=500
      const batchParams = {
        device_pk: source.deviceId(),
        categories: categoryNames.join(','),
        start_time: startTime.toISOString(),
        end_time: endTime.toISOString(),
        max_points: 500
      }

      try {
        const batchRes = await client.get<unknown, MeasurementBatch[]>('/api/v1/unified-data/historical-batch', {
          params: batchParams,
          // 生产实测该端点会挂起（>38s 无响应）：给独立短超时，尽快转入逐个回退，
          // 避免整页趋势图长时间停在骨架（P0 配套；不影响全局 axios timeout）。
          timeout: 8000,
        })
        // 批量 API 返回格式: [{category: "temperature", data: [{...}]}, ...]
        const batchResults = unwrapList<MeasurementBatch>(batchRes)
        if (batchResults.length > 0) {
          for (const result of batchResults) {
            const cat = result.category
            if (!cat) continue
            const items = (result.data || []).filter((item) => {
              const t = item.timestamp || item.created_at
              return t && !t.startsWith('0001-01-01')
            })
            if (items.length > 0) {
              const catName = sensorNameMap[cat] || cat
              const catUnit = sensorUnitMap[cat] || ''
              series.push({
                name: catName,
                unit: catUnit,
                category: cat,
                data: items.map((item) => ({
                  time: item.timestamp || item.created_at,
                  value: item.value
                }))
              })
            }
          }
        }

        // 如果批量 API 未返回任何数据，尝试 fallback 逐个请求
        if (series.length === 0) {
          throw new Error('batch API returned no data, falling back')
        }
      } catch {
        // Fallback: 批量 API 失败时逐个请求 + max_points=500。
        // ⚠ 并发必须限流：浏览器同域只有 ~6 条 HTTP/1.1 连接，31 个回退请求全并行时，
        //   挂起的那几条会吃满连接池 ⇒ 用户点翻页发出的 /edge-devices/:id/data 请求被排队，
        //   实测点击分页后 8s 内一个请求都没发出去（页面一直骨架）。限 4 并发给交互请求留 2 条。
        const fallbackResults = await mapWithConcurrency(categoryNames, 4, async (cat) => {
          try {
            const res = await client.get<unknown, MeasurementPoint[]>('/api/v1/unified-data/historical', {
              params: {
                device_pk: source.deviceId(),
                category: cat,
                start_time: startTime.toISOString(),
                end_time: endTime.toISOString(),
                max_points: 500
              }
            })
            return { cat, data: unwrapList<MeasurementPoint>(res) }
          } catch {
            fallbackFailures += 1
            return { cat, data: [] as MeasurementPoint[] }
          }
        })
        for (const r of fallbackResults) {
          if (r.data && r.data.length > 0) {
            const filteredData = r.data
              .filter((item) => {
                const t = item.timestamp || item.created_at
                return t && !t.startsWith('0001-01-01')
              })
            if (filteredData.length > 0) {
              const catName = sensorNameMap[r.cat] || r.cat
              const catUnit = sensorUnitMap[r.cat] || ''
              series.push({
                name: catName,
                unit: catUnit,
                category: r.cat,
                data: filteredData.map((item) => ({
                  time: item.timestamp || item.created_at,
                  value: item.value
                }))
              })
            }
          }
        }
      }

      chartSeries.value = series
      // 所有类别请求都失败且没有任何序列 ⇒ 真失败；若请求成功但数据为空 ⇒ 真空数据（非错误）。
      chartFailed.value = series.length === 0
        && categoryNames.length > 0
        && fallbackFailures === categoryNames.length
    } catch (error) {
      logger.error('获取趋势数据失败', { error: String(error) })
      // Fallback: 直接用已聚合的历史行里出现过的数值字段建序列
      // （与表格/统计卡同一解析入口）。趋势是数值图：同样只看 numbers，
      // 字符串读数不能进（否则画出掉到 0 的假线）。
      const numericKeys = Object.keys(parseRowData(source.rows.value[0]).numbers ?? {})

      if (numericKeys.length > 0) {
        chartSeries.value = numericKeys.map(key => ({
          name: key,
          unit: '',
          category: key,
          data: source.rows.value
            .filter((item) => {
              const t = item.timestamp || item.collected_at || item.created_at
              return t && !t.startsWith('0001-01-01')
            })
            // 该行没这个指标 ⇒ null，**不是 0**：折线应当断开，而不是伪造一个落零点
            .map(item => ({
              time: item.timestamp || item.collected_at || item.created_at,
              value: parseRowData(item).numbers?.[key] ?? null,
            }))
        }))
        chartFailed.value = false
      } else {
        chartSeries.value = []
        chartFailed.value = true
      }
    } finally {
      if (token === chartLoadToken) {
        chartLoading.value = false
        syncMobileMetric()
      }
    }
  }

  // ── 移动端趋势图：单指标 + 紧凑高度 ──
  // P1（B-data 审计）：393 的 303px 画布里 4 根 Y 轴 + 20 条图例不可读（轴标签重叠、
  // 图例被折叠成 1/20）；移动端改为单指标 + height 220（LineChart 的紧凑模式阈值 ≤220）。
  // 指标优先级表见 ../rowDisplay 的 CHART_METRIC_PRIORITY（与移动端摘要卡片共用同一张）。

  const chartMetricOptions = computed(() => {
    const rank = (code: string) => {
      const index = CHART_METRIC_PRIORITY.indexOf(code)
      return index < 0 ? CHART_METRIC_PRIORITY.length : index
    }
    // 关键指标排前面（否则下拉第一屏全是 cell_voltage_1/10/11… 字母序噪声）。
    return [...chartSeries.value]
      .sort((a, b) => rank(String(a.category)) - rank(String(b.category)))
      .map((item) => ({
        code: String(item.category || ''),
        label: item.unit ? `${item.name} (${item.unit})` : String(item.name || item.category || ''),
      }))
  })

  const visibleChartSeries = computed(() => {
    if (!source.isMobile.value) return chartSeries.value
    const chosen = chartSeries.value.find(item => String(item.category) === mobileMetric.value)
    return chosen ? [chosen] : []
  })

  const chartHeight = computed(() => (source.isMobile.value ? '220px' : '350px'))

  function pickDefaultMetric(series: Array<{ category?: unknown }>): string {
    for (const code of CHART_METRIC_PRIORITY) {
      if (series.some(item => String(item.category) === code)) return code
    }
    const first = series[0]
    return first ? String(first.category ?? '') : ''
  }

  /** 让移动端选中项始终落在当前 series 上（换设备/时间范围后自动回到优先指标）。 */
  function syncMobileMetric() {
    if (mobileMetric.value && chartSeries.value.some(item => String(item.category) === mobileMetric.value)) return
    mobileMetric.value = pickDefaultMetric(chartSeries.value)
  }

  function retryChart() {
    void buildChartSeries()
  }

  return {
    chartSeries,
    chartLoading,
    chartFailed,
    mobileMetric,
    chartMetricOptions,
    visibleChartSeries,
    chartHeight,
    buildChartSeries,
    retryChart,
  }
}
