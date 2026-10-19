/**
 * useDeviceData — shared composable for edge device detail pages.
 *
 * Extracts the duplicated fetchDeviceDetail / fetchLatestData / handleRefresh /
 * handleSyncToHA logic from GenericDeviceDetail.vue, BmsDetailPage.vue, and
 * InverterDetailPage.vue.
 *
 * Key improvements over the per-page inline code:
 * - W13 fix: Uses SERVER timestamps from WS messages for last_data_time
 *   instead of client time (new Date().toISOString()).
 * - Throttles last_data_time updates (3-second window) to avoid jitter
 *   from high-frequency WS pushes.
 * - Uses useRealtimeData internally for WS subscription + data management.
 */
import { ref, computed, watch, onUnmounted, type Ref } from 'vue'
import { feedback } from '@/utils/feedback'
import { ElMessage } from 'element-plus'
import { edgeDeviceApi, type EdgeDevice } from '@/api/edgeDevice'
import { haApi } from '@/api/homeassistant'
import { useWebSocketStore } from '@/stores/websocket'
import { useEdgeDeviceStore } from '@/stores/edgeDevice'
import { useRealtimeData } from '@/composables/useRealtimeData'
import { logger } from '@/utils/logger'
import { assertSessionGeneration, getSessionGeneration } from '@/utils/sessionCache'

export interface UseDeviceDataOptions {
  /** Max realtime data items to cache, default 200 */
  maxItems?: number
  /** Error message prefix for fetchDeviceDetail, default '获取边缘设备详情失败' */
  errorPrefix?: string
}

export function useDeviceData(
  deviceId: Ref<number | null>,
  options: UseDeviceDataOptions = {},
) {
  const { maxItems = 200, errorPrefix = '获取边缘设备详情失败' } = options

  const wsStore = useWebSocketStore()
  const edgeDeviceStore = useEdgeDeviceStore()
  const wsConnected = computed(() => wsStore.connected)

  const device = ref<EdgeDevice | null>(null)
  const loading = ref(true)
  const refreshing = ref(false)
  const syncingHA = ref(false)
  let operationGeneration = 0

  watch(deviceId, () => {
    operationGeneration++
    syncingHA.value = false
    refreshing.value = false
  })

  onUnmounted(() => {
    operationGeneration++
    syncingHA.value = false
    refreshing.value = false
  })

  // ── Realtime data via shared composable ──────────────────────
  const {
    dataItems: realtimeDataItems,
    latestData: realtimeLatest,
    messageCount,
    clear: clearRealtimeData,
  } = useRealtimeData({
    deviceId,
    maxItems,
  })

  // Alias for convenience — some pages use "latestData"
  const latestData = realtimeLatest

  // ── API: fetch device detail ─────────────────────────────────
  let deviceDetailSequence = 0
  const fetchDeviceDetail = async (throwOnError = false) => {
    const id = deviceId.value
    if (id === null) return
    const sequence = ++deviceDetailSequence
    loading.value = true
    try {
      const result = await edgeDeviceStore.fetchDetail(id, true)
      if (sequence !== deviceDetailSequence || deviceId.value !== id) return
      device.value = result
      // Merge device.last_data into latestData without clobbering
      // potential WS real-time updates that arrived during the API call.
      if (device.value.last_data) {
        if (latestData.value) {
          Object.assign(latestData.value, device.value.last_data)
        } else {
          latestData.value = { ...device.value.last_data }
        }
      }
    } catch (error) {
      feedback.handleError(error, errorPrefix)
      if (throwOnError) throw error
    } finally {
      if (sequence === deviceDetailSequence) loading.value = false
    }
  }

  // ── API: fetch latest data snapshot ──────────────────────────
  //
  // 两个端点并用，各自不可替代（2026-10-18 实测缺陷后改）：
  //
  //   /latest-values  ⇒ **每个传感器**的最新值（跨命令聚合）。这是详情页
  //                     展示实时读数必须用的：ASCII 一问一答设备（逆变器）
  //                     一个轮询周期发 9 条命令、每条各带 2~14 个物理量，
  //                     而 /latest-data 只返回**最后一条命令**的记录 ⇒
  //                     实测 36 个字段里只显示 3 个，其余在库里但界面看不到。
  //
  //   /latest-data    ⇒ 最新**一条原始记录**（含 raw_data），用于实时数据流
  //                     逐条展示"刚收到了什么帧"。它是有意义的，不能删。
  //
  // 合并顺序：先取全量快照，再用单条记录覆盖同名字段（后者更新）。
  // latest-values 失败**不阻断**页面 —— 退化为原来的单条行为，而不是白屏。
  let latestDataSequence = 0
  const fetchLatestData = async (throwOnError = false) => {
    const id = deviceId.value
    if (id === null) return
    const sequence = ++latestDataSequence
    try {
      // ① 全量传感器快照（跨命令）
      let aggregated: Record<string, unknown> | null = null
      try {
        const values = await edgeDeviceApi.getLatestValues(id)
        if (sequence !== latestDataSequence || deviceId.value !== id) return
        if (values.length > 0) {
          aggregated = {}
          for (const item of values) {
            aggregated[item.sensor_name] = item.value
          }
        }
      } catch (error) {
        logger.error('获取全量传感器快照失败，退化为单条记录', { error: String(error) })
      }

      if (aggregated) {
        latestData.value = { ...(latestData.value || {}), ...aggregated }
      }

      const response = await edgeDeviceApi.getLatestData(id)
      if (sequence !== latestDataSequence || deviceId.value !== id) return
      if (response) {
        let parsedData = response.data || {}
        if (response.data_json) {
          try {
            const dj = JSON.parse(response.data_json)
            if (dj.sensors && Array.isArray(dj.sensors)) {
              parsedData = {}
              for (const s of dj.sensors) {
                // StringValue 优先：字符串型读数（如 BMS hardware_version="V19"）的
                // 真值在 StringValue 里，而 Value 恒为 0 —— 只取 Value 会显示成 0，
                // 比「—」更糟（0 看起来像真实读数）。口径同 DeviceControlPanel.vue:76。
                const sv = s?.StringValue
                parsedData[s.Name] = (typeof sv === 'string' && sv.trim() !== '') ? sv : s.Value
              }
            }
          } catch { /* ignore parse error */ }
        }
        // ⚠ **合并**，不是覆盖。
        //
        // 这里曾经写的是 `latestData.value = parsedData` —— 单条记录只含
        // 最后一条命令的物理量（逆变器实测 3/36 个），直接赋值会把上面刚取到的
        // 全量快照整个抹掉，于是页面又变回"只有 3 个字段"。
        // 语义：单条记录里的同名字段**更新**（它更新），其余保留。
        latestData.value = { ...(latestData.value || {}), ...parsedData }
        // Prepend to realtime data stream as a historical item
        realtimeDataItems.value = [{
          id: `latest-${Date.now()}`,
          timestamp: response.created_at || new Date().toISOString(),
          data: parsedData,
          rawData: response.raw_data,
          isRealtime: false,
        }, ...realtimeDataItems.value].slice(0, maxItems)
      }
    } catch (error: any) {
      logger.error('获取最新数据失败', { error: String(error) })
      if (throwOnError) throw error
    }
  }

  // ── Actions: refresh ─────────────────────────────────────────
  /**
   * Refresh device data. Pass an optional callback to refresh
   * page-specific charts (e.g. historyChartRef.fetchHistoryData()).
   */
  const handleRefresh = async (onChartsRefreshed?: () => void) => {
    refreshing.value = true
    try {
      await fetchDeviceDetail(true)
      await fetchLatestData(true)
      onChartsRefreshed?.()
      ElMessage.success('数据已刷新')
    } catch {
      feedback.error('刷新失败')
    } finally {
      refreshing.value = false
    }
  }

  // ── Actions: sync to Home Assistant ──────────────────────────
  const handleSyncToHA = async () => {
    const id = deviceId.value
    if (id === null) return
    const operation = operationGeneration
    const sessionGeneration = getSessionGeneration()
    syncingHA.value = true
    try {
      await haApi.syncDevice(id)
      assertSessionGeneration(sessionGeneration)
      if (operation !== operationGeneration || deviceId.value !== id) return
      ElMessage.success('设备已同步到HomeAssistant')
    } catch (error: any) {
      if (operation !== operationGeneration || deviceId.value !== id) return
      feedback.handleError(error, '同步到HomeAssistant失败')
    } finally {
      if (operation === operationGeneration && deviceId.value === id) syncingHA.value = false
    }
  }

  // ── W13 fix: Update last_data_time using SERVER timestamps ──
  // Throttle to 3-second window to avoid jitter from high-frequency WS pushes.
  let lastDataTimeUpdate = 0
  watch(latestData, (data) => {
    if (!device.value || !data || Object.keys(data).length === 0) return
    const now = Date.now()
    if (now - lastDataTimeUpdate < 3000) return
    lastDataTimeUpdate = now
    // Prefer server-side timestamps from the WS payload.
    // Fields checked: timestamp (ISO string), ts (epoch seconds/ms), time
    const d = data as any
    const serverTime =
      d.timestamp ??
      (typeof d.ts === 'number'
        ? (d.ts > 1e12
            ? new Date(d.ts).toISOString()          // milliseconds
            : new Date(d.ts * 1000).toISOString())   // seconds
        : undefined) ??
      d.time ??
      new Date().toISOString()
    device.value.last_data_time = serverTime
  })

  return {
    // State
    device,
    loading,
    refreshing,
    syncingHA,
    wsConnected,
    latestData,
    realtimeDataItems,
    realtimeLatest,
    messageCount,
    // Actions
    fetchDeviceDetail,
    fetchLatestData,
    handleRefresh,
    handleSyncToHA,
    clearRealtimeData,
  }
}
