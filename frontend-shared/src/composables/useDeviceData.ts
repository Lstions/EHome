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
  // 2026-11-11 原始数据退役后，只剩**一条**取数路径：
  //
  //   /latest-values ⇒ **每个传感器**的最新值（跨命令聚合）。
  //
  // 原 `/latest-data` 已随 device_data 表一并删除。它当年存在的理由是
  // "拿最新一条原始记录喂实时数据流"，而这正是它与本页面主诉求冲突的地方：
  // ASCII 一问一答设备（逆变器）一个轮询周期发 9 条命令、每条各带 2~14 个
  // 物理量，单条记录只含**最后执行的那条命令**的产物 —— 实测 36 个字段里
  // 只显示 3 个，其余在库里但界面永远看不到。
  //
  // 实时数据流（realtimeDataItems）**只由 WebSocket 推送填充**：
  // 它是"刚收到了什么"的流，而快照是"现在每个字段是多少"的点 —— 把快照
  // 塞进流里等于凭空造一条用户从未见过的"收到记录"。删除端点顺带结束了
  // 这个语义混装。
  let latestDataSequence = 0
  const fetchLatestData = async (throwOnError = false) => {
    const id = deviceId.value
    if (id === null) return
    const sequence = ++latestDataSequence
    try {
      const values = await edgeDeviceApi.getLatestValues(id)
      if (sequence !== latestDataSequence || deviceId.value !== id) return
      if (values.length === 0) return
      // ⚠ **合并**，不是覆盖。
      //
      // 这里曾经写的是 `latestData.value = parsedData` —— 快照里的同名字段
      // 应**更新**已有值，其余保留：页面可能已经通过 WS 收到了更新的读数，
      // 而快照是几秒前的。直接赋值会把 WS 的新值抹掉（回退成旧值）。
      // 只取数值：/latest-values 的契约就是 {sensor_name, value, unit, timestamp}。
      //
      // ⚠ 已知的**能力损失**（不是本次引入的 bug）：字符串型读数
      // （BMS hardware_version="V19" / serial_number）过去只存在
      // device_data.data_json.str_values 里，随表删除后**没有任何落库位置**
      // （unified_data 与 telemetry_frame 都没有 StringValue 列）。
      // 方案 §3.1 实测：生产全库 str_values 0 行 —— 这条路径从未被走过，
      // 所以这次退役不会丢任何真实数据；但它意味着"将来读 BMS 版本号"
      // 需要新的存储设计，不能指望接口自动带回来。
      const aggregated: Record<string, unknown> = {}
      for (const item of values) {
        aggregated[item.sensor_name] = item.value
      }
      latestData.value = { ...(latestData.value || {}), ...aggregated }
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
