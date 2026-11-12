import { computed, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { feedback } from '@/utils/feedback'
import { sensorNameMap, sensorUnitMap } from '@/utils/sensor'
import { logger } from '@/utils/logger'
import { useHistoryData } from '@/composables/useHistoryData'
import { useTimeRange } from '@/composables/useTimeRange'
import type { HistoryDataPoint, SeriesData } from '@/types/chart'
import { describeQueryScope, knownCategoriesFor, statusCategories } from './historyChartGroups'

/**
 * 历史数据卡的「取数 / 时间窗 / CSV 导出」组合式。
 *
 * 边界依据（2026-11-12 纯搬移拆分，行为零变化）：
 *   · 搬入：useHistoryData 的接线（batch → 重试 → 逐测点降级；max_points=500 由服务端
 *     降采样，本组件没有前端降采样步骤）、useTimeRange 的档位换算、空态时间文案、
 *     CSV 行构造与下载。
 *   · 没搬：
 *       - 分组投影是纯函数（historyChartGroups.ts），tab 选中态见 useActiveChartGroup.ts；
 *       - 视口 / 图高 / 右轴预留留在原组件：源码门禁要求 CHART_HEIGHT_PX、
 *         maxReserveRightAxes、LINE_CHART_MAX_Y_AXIS 出现在原文件文本里；
 *       - 空态 DOM 仍由原组件模板渲染，本文件只提供 timeRangeLabel / queryScopeHint 文案。
 *   · 与拆分前一致：只有一条取数路径（unified-data/historical-batch），
 *     device_data 兜底已随原始数据退役整段删除，这里不复活。
 */
export interface HistoryChartSource {
  deviceId: number
  deviceType: string
  deviceTypeText: string
}

export function useHistoryChartData(source: HistoryChartSource) {
  const historyData = ref<HistoryDataPoint[]>([])
  const chartSeries = ref<SeriesData[]>([])

  const { loading: chartLoading, fetch: historyFetch } = useHistoryData({
    nameMap: sensorNameMap,
    unitMap: sensorUnitMap,
    excludeCategories: statusCategories,
    errorPrefix: '获取历史数据失败',
    showError: false, // caller handles errors for device_data fallback
    // 历史 API 单独放宽到 30s（client.ts 全局仍 10s）；不改其它行为（契约 §2.7）
    timeoutMs: 30000,
  })

  const { timeRange, customTimeRange, getTimeRange, handleTimeRangeChange } = useTimeRange(fetchHistoryData)

  /**
   * Unified handler for TimeRangeSelector @change events.
   * The selector emits 'change' for both radio-group (preset) and date-picker (custom) changes.
   * handleTimeRangeChange (from composable) only fires for preset ranges;
   * for 'custom' we trigger fetchHistoryData directly when the date picker changes.
   */
  const onTimeRangeChange = () => {
    handleTimeRangeChange()
    // handleTimeRangeChange skips 'custom'; trigger fetch for custom date changes
    if (timeRange.value === 'custom') fetchHistoryData()
  }

  /** 空态展示用的时间范围文案（与 TimeRangeSelector 的档位一一对应） */
  const timeRangeLabel = computed(() => {
    const map: Record<string, string> = {
      '1h': '最近 1 小时', '24h': '最近 24 小时', '7d': '最近 7 天',
    }
    if (timeRange.value === 'custom') {
      const [s, e] = getTimeRange()
      return s.toLocaleString('zh-CN') + ' ~ ' + e.toLocaleString('zh-CN')
    }
    return map[timeRange.value] || timeRange.value
  })

  /** 空态展示用的查询范围说明（告诉用户查了哪些物理量，便于判断是没采到还是选错范围） */
  const queryScopeHint = computed(() => describeQueryScope(source.deviceType))

  async function fetchHistoryData() {
    if (!source.deviceId) return
    const [startTime, endTime] = getTimeRange()
    const deviceType = source.deviceType

    chartLoading.value = true
    try {
      // Unified data devices: query specific categories via batch API
      if (['bmp280', 'sht40', 'temp_humidity'].includes(deviceType)) {
        const categories = deviceType === 'bmp280' ? ['temperature', 'pressure'] : ['temperature', 'humidity']
        const result = await historyFetch(source.deviceId, categories, startTime, endTime)
        chartSeries.value = result
        historyData.value = []
      } else {
        // Generic: try all known categories via unified-data batch API
        const knownCategories = knownCategoriesFor(deviceType)

        const result = await historyFetch(source.deviceId, knownCategories, startTime, endTime)

        // 主路径（unified-data/historical-batch）是**唯一**路径。
        //
        // 这里原本还有一段「批量返回空 ⇒ 回落 /edge-devices/:id/data 读 device_data」
        // 的兜底。2026-11-11 原始数据退役把 device_data 整表删除，兜底的数据源
        // 已不存在，故整段删除。
        //
        // 顺带记录一段历史：那个兜底**在删除之前就已经是死的** —— 它读
        // `firstItem.data`，而后端 JSON tag 是 `data_json`，且 2026-11-07 起内容
        // 只有 {channel_id, raw_hex, timestamp}（数值在 data_json.sensors 里，由后端
        // 读时合成）。实测兜底拿到 0 条（见 .scratch_chart/图表无数据-只读分析-
        // 2026-11-10.md §128）。所以删它不改变任何用户可见行为。
        chartSeries.value = result
        historyData.value = []
      }
    } catch (error: any) {
      logger.error('获取历史数据失败', { error: String(error) })
      feedback.handleError(error, '获取历史数据失败')
    } finally {
      chartLoading.value = false
    }
  }

  function handleExportCSV() {
    const rows: string[][] = [['时间', '数值']]
    if (chartSeries.value.length > 0) {
      const seriesNames = chartSeries.value.map(s => s.name)
      rows[0] = ['时间', ...seriesNames]
      const timeSet = new Set<number>()
      const seriesMap: Map<string, Map<number, number>> = new Map()
      chartSeries.value.forEach(s => {
        const valMap = new Map<number, number>()
        s.data.forEach((d: any) => {
          const ts = new Date(d.time).getTime()
          timeSet.add(ts)
          valMap.set(ts, d.value)
        })
        seriesMap.set(s.name, valMap)
      })
      const sortedTimes = Array.from(timeSet).sort((a, b) => a - b)
      sortedTimes.forEach(ts => {
        const row = [new Date(ts).toLocaleString('zh-CN')]
        seriesNames.forEach(name => {
          const val = seriesMap.get(name)?.get(ts)
          row.push(val !== undefined ? String(val) : '')
        })
        rows.push(row)
      })
    } else {
      historyData.value.forEach(item => rows.push([new Date(item.time).toLocaleString('zh-CN'), String(item.value)]))
    }
    const csvContent = rows.map(row => row.map(cell => `"${cell}"`).join(',')).join('\n')
    const blob = new Blob(['\ufeff' + csvContent], { type: 'text/csv;charset=utf-8' })
    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = `${source.deviceTypeText}_history.csv`
    link.click()
    URL.revokeObjectURL(url)
    ElMessage.success('导出成功')
  }

  return {
    chartLoading,
    historyData,
    chartSeries,
    timeRange,
    customTimeRange,
    timeRangeLabel,
    queryScopeHint,
    fetchHistoryData,
    handleExportCSV,
    onTimeRangeChange,
  }
}
