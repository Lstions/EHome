/**
 * 数据面板「实时数据流」（原 DataPanel.vue 的 WS 订阅 / 防抖重算 / 图表增量段原样搬出）。
 *
 * 边界依据（为什么整块搬）：
 *   · 一次 WS 推送必须**同时**改三处状态，三者共用同一个订阅与同一个防抖计时器：
 *       ① 实时计数（卡片「共 N 条」）；
 *       ② 整窗历史行（allHistoryRows 前插后整体重排当前页）；
 *       ③ 趋势序列（增量 append）；
 *     拆成多个文件后"一次推送要改哪几处"就不再是一个可读的整体；
 *   · 订阅解绑与防抖清理共用 onUnmounted（历史缺陷正是泄漏的订阅），
 *     生命周期因此必须跟在本模块里。
 *
 * 分页切片**仍留在页面**：listPageConventions 门禁把「页面文件里必须有切片分页文本」
 * 当作豁免口径（EXEMPT_LOCAL_SLICE 必须仍"有事可免"），所以这里通过 applyPage 回调
 * 触发重排，而不是自己切片 —— 行为与拆分前完全一致。
 */
import { onUnmounted, ref, type Ref } from 'vue'
import { ElMessage } from 'element-plus'
import { useWebSocketStore, type WebSocketMessage } from '@/stores/websocket'
import { WS_EVENT } from '@/events/events'
import type { CollectedRow } from '../historyRows'
import type { RealtimeDataPayload } from '../types'

/** 实时流的输出目标：页面拥有的分页状态与图表序列。 */
export interface DataPanelRealtimeSource {
  /** queryForm.deviceId 的读取器：只接收当前设备的推送。 */
  deviceId: () => number | null
  /** 整窗历史行（原 allHistoryRows）：新推送前插到这里，再由 applyPage 重排当前页。 */
  rows: Ref<CollectedRow[]>
  /** 全量采集数（分页器「共 N 条」）。 */
  total: Ref<number>
  /** 页面上的重排入口（内含切片 + 重算统计）。 */
  applyPage: () => void
  /** 统计重算入口（防抖后调用）。 */
  calculateStats: () => void
  /** 趋势序列：增量 append 到现有 series。 */
  chartSeries: Ref<any[]>
}

export function useDataPanelRealtime(source: DataPanelRealtimeSource) {
  // 实时数据
  const realtimeEnabled = ref(false)
  const realtimeCount = ref(0)
  const realtimeData = ref<any[]>([])

  const wsStore = useWebSocketStore()
  let unsubscribeData: (() => void) | null = null

  // 为与拆分前逐行对照，这里沿用它原来的局部名：
  // source.rows 就是页面的 allHistoryRows（整窗行），其余是页面下传的回调 / ref。
  const allHistoryRows = source.rows
  const { total, applyPage, calculateStats, chartSeries } = source

  // 实时数据处理
  // 性能优化：防抖 calculateStats，避免每条WS消息都触发重计算
  let statsDirty = false
  let debounceTimer: ReturnType<typeof setTimeout> | null = null
  const DEBOUNCE_MS = 2000  // 2秒内多条消息只触发一次重算

  const flushDebounced = () => {
    if (statsDirty) {
      statsDirty = false
      calculateStats()
    }
  }

  const scheduleDebounced = () => {
    if (debounceTimer) clearTimeout(debounceTimer)
    debounceTimer = setTimeout(flushDebounced, DEBOUNCE_MS)
  }

  const handleDataUpdate = (message: WebSocketMessage) => {
    const payload = message.payload as RealtimeDataPayload | undefined
    if (!payload || payload.edge_device_id !== source.deviceId()) return

    realtimeCount.value++
    // 形状必须与表格期望一致：一次采集一行、指标在 data 里。
    //
    // 2026-11-11 原始数据退役：不再往行上挂 raw_hex / error_code ——
    // 「原始数据」「错误码」两列已删除（前者无数据源，后者一直是 undefined）。
    const timestamp = payload.collected_at || new Date().toISOString()
    const newItem: CollectedRow = {
      // WS 行没有后端合成 id，用时间戳当 key：同一时间戳的两次推送本就该合并
      id: 0,
      timestamp,
      created_at: timestamp,
      data: (payload.data || payload.sensors || {}) as Record<string, number>,
      collected_at: timestamp,
    }

    // 添加到实时数据列表（页面上的「共 N 条」计数用）
    realtimeData.value.unshift(newItem)
    if (realtimeData.value.length > 50) {
      realtimeData.value = realtimeData.value.slice(0, 50)
    }

    // 同时插入表格数据源（顶部），并整体重排当前页：
    // 直接改 historyData 会把"当前页切片"这个不变量破坏掉（第 2 页会跟着变），
    // 所以改 allHistoryRows 再 applyPage()。
    allHistoryRows.value.unshift(newItem)
    total.value++
    applyPage()

    // 标记stats为脏（统计计算），但不标记chartDirty
    statsDirty = true
    scheduleDebounced()

    // 增量更新图表 — 直接append到现有series
    appendRealtimeData(payload)
  }

  // 新增函数: 增量更新图表series
  const appendRealtimeData = (payload: RealtimeDataPayload) => {
    if (chartSeries.value.length === 0) return
    const data = payload.data || payload.sensors
    if (!data || typeof data !== 'object') return
    const time = payload.collected_at || new Date().toISOString()

    let updated = false
    for (const series of chartSeries.value) {
      const key = series.category
      if (key && data[key] !== undefined && typeof data[key] === 'number') {
        series.data.push({ time, value: data[key] })
        // 限制长度
        if (series.data.length > 500) {
          series.data = series.data.slice(-500)
        }
        updated = true
      }
    }
    // 触发响应式更新 — 重新赋值引用
    if (updated) {
      chartSeries.value = [...chartSeries.value]
    }
  }

  const toggleRealtime = (enabled: string | number | boolean) => {
    if (enabled) {
      // 订阅实时数据
      unsubscribeData = wsStore.subscribe(WS_EVENT.DATA_UPDATE, handleDataUpdate)
      realtimeCount.value = 0
      realtimeData.value = []
      ElMessage.success('已开启实时数据接收')
    } else {
      if (unsubscribeData) {
        unsubscribeData()
        unsubscribeData = null
      }
      ElMessage.info('已关闭实时数据接收')
    }
  }

  onUnmounted(() => {
    if (unsubscribeData) {
      unsubscribeData()
    }
    if (debounceTimer) {
      clearTimeout(debounceTimer)
      debounceTimer = null
    }
  })

  return { realtimeEnabled, realtimeCount, toggleRealtime }
}
