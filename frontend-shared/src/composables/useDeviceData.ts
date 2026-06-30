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
import { ref, computed, watch, type Ref } from 'vue'
import { ElMessage } from 'element-plus'
import { edgeDeviceApi, type EdgeDevice } from '@/api/edgeDevice'
import { haApi } from '@/api/homeassistant'
import { useWebSocketStore } from '@/stores/websocket'
import { useRealtimeData } from '@/composables/useRealtimeData'
import { logger } from '@/utils/logger'

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
  const wsConnected = computed(() => wsStore.connected)

  const device = ref<EdgeDevice | null>(null)
  const loading = ref(true)
  const refreshing = ref(false)
  const syncingHA = ref(false)

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
  const fetchDeviceDetail = async () => {
    const id = deviceId.value
    if (id === null) return
    loading.value = true
    try {
      device.value = await edgeDeviceApi.getDetail(id)
      // Merge device.last_data into latestData without clobbering
      // potential WS real-time updates that arrived during the API call.
      if (device.value.last_data) {
        if (latestData.value) {
          Object.assign(latestData.value, device.value.last_data)
        } else {
          latestData.value = { ...device.value.last_data }
        }
      }
    } catch {
      ElMessage.error(errorPrefix)
    } finally {
      loading.value = false
    }
  }

  // ── API: fetch latest data snapshot ──────────────────────────
  const fetchLatestData = async () => {
    const id = deviceId.value
    if (id === null) return
    try {
      const response = await edgeDeviceApi.getLatestData(id)
      if (response) {
        let parsedData = response.data || {}
        if (response.data_json) {
          try {
            const dj = JSON.parse(response.data_json)
            if (dj.sensors && Array.isArray(dj.sensors)) {
              parsedData = {}
              for (const s of dj.sensors) parsedData[s.Name] = s.Value
            }
          } catch { /* ignore parse error */ }
        }
        latestData.value = parsedData
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
      await fetchDeviceDetail()
      await fetchLatestData()
      onChartsRefreshed?.()
      ElMessage.success('数据已刷新')
    } catch {
      ElMessage.error('刷新失败')
    } finally {
      refreshing.value = false
    }
  }

  // ── Actions: sync to Home Assistant ──────────────────────────
  const handleSyncToHA = async () => {
    const id = deviceId.value
    if (id === null) return
    syncingHA.value = true
    try {
      await haApi.syncDevice(id)
      ElMessage.success('设备已同步到HomeAssistant')
    } catch (error: any) {
      ElMessage.error('同步到HomeAssistant失败: ' + (error.message || '未知错误'))
    } finally {
      syncingHA.value = false
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
