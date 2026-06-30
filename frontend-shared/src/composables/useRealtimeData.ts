/**
 * useRealtimeData — 统一实时数据流 composable
 *
 * 消除 BmsDetailPage / InverterDetailPage / GenericDeviceDetail / DataPanel 中
 * 重复的 WebSocket 订阅、设备ID过滤、数据项构造逻辑。
 *
 * 核心设计：
 * 1. 单一订阅：一个 composable 实例只创建一个 WS 订阅
 * 2. 设备过滤：自动按 deviceId 过滤，只回调匹配的消息
 * 3. 批量处理：微任务合并高频消息，减少 Vue 响应式触发次数
 * 4. 生命周期安全：onUnmounted 自动取消订阅
 */
import { ref, onMounted, onUnmounted, type Ref } from 'vue'
import { useWebSocketStore, type WebSocketMessage } from '@/stores/websocket'
import { WS_EVENT } from '@/events/events'
import type { DataItem } from '@/types/realtime'

export interface RealtimeDataOptions {
  /** 设备ID，用于过滤WS消息 */
  deviceId: Ref<number | null> | number
  /** 监听的事件类型列表，默认 [DATA_UPDATE] */
  eventTypes?: string[]
  /** 最大缓存条数，默认 200 */
  maxItems?: number
  /** 是否在收到数据时更新 latestData，默认 true */
  trackLatest?: boolean
  /** 是否同时监听 channel_data 事件，默认 true */
  includeChannelData?: boolean
}

export interface RealtimeDataReturn {
  /** 实时数据列表（新的在前） */
  dataItems: Ref<DataItem[]>
  /** 最新数据（用于指标卡片等） */
  latestData: Ref<Record<string, any> | null>
  /** 接收到的消息计数 */
  messageCount: Ref<number>
  /** 添加单条数据 */
  addData: (item: DataItem) => void
  /** 批量添加数据 */
  addDataBatch: (items: DataItem[]) => void
  /** 清空数据 */
  clear: () => void
}

let idCounter = 0
function genId(): string {
  return `${Date.now()}-${++idCounter}`
}

export function useRealtimeData(options: RealtimeDataOptions): RealtimeDataReturn {
  const {
    deviceId,
    eventTypes,
    maxItems = 200,
    trackLatest = true,
    includeChannelData = true,
  } = options

  const wsStore = useWebSocketStore()
  const dataItems = ref<DataItem[]>([]) as Ref<DataItem[]>
  const latestData = ref<Record<string, any> | null>(null)
  const messageCount = ref(0)

  // 批量缓冲：微任务合并
  let pendingBatch: DataItem[] = []
  let batchScheduled = false

  const flushBatch = () => {
    batchScheduled = false
    if (pendingBatch.length === 0) return

    // 合并到头部
    const merged = [...pendingBatch.reverse(), ...dataItems.value]
    dataItems.value = merged.length > maxItems ? merged.slice(0, maxItems) : merged
    pendingBatch = []
  }

  const scheduleFlush = () => {
    if (!batchScheduled) {
      batchScheduled = true
      // 使用 queueMicrotask 在当前同步代码完成后批量刷新
      queueMicrotask(flushBatch)
    }
  }

  const addData = (item: DataItem) => {
    if (!item.id) item.id = genId()
    pendingBatch.push(item)
    scheduleFlush()

    if (trackLatest && item.data && typeof item.data === 'object') {
      latestData.value = { ...latestData.value, ...item.data }
    }
  }

  const addDataBatch = (items: DataItem[]) => {
    for (const item of items) {
      if (!item.id) item.id = genId()
      pendingBatch.push(item)
    }
    scheduleFlush()

    // W7: Update latestData from the last item in the batch when trackLatest is true
    if (trackLatest && items.length > 0) {
      const last = items[items.length - 1]
      if (last.data && typeof last.data === 'object') {
        latestData.value = { ...latestData.value, ...last.data }
      }
    }
  }

  const clear = () => {
    dataItems.value = []
    pendingBatch = []
    messageCount.value = 0
    if (trackLatest) {
      latestData.value = null
    }
  }

  // 获取当前设备ID
  const getDeviceId = (): number | null => {
    if (typeof deviceId === 'number') return deviceId
    return deviceId.value
  }

  // WS 消息处理
  const handleMessage = (message: WebSocketMessage) => {
    const p = (message.payload || message) as any
    const msgDeviceId = Number(p.sensor_device_id || p.edge_device_id || p.device_id)
    const targetId = getDeviceId()

    // W6: Reject messages without a device ID — NaN would match ALL device pages
    if (isNaN(msgDeviceId)) return
    if (targetId !== null && msgDeviceId !== targetId) return

    const sensorData = p.data || p.sensors
    if (!sensorData) return

    messageCount.value++

    const dataItem: DataItem = {
      id: genId(),
      timestamp: p.collected_at || (p.timestamp ? new Date(p.timestamp * 1000).toISOString() : new Date().toISOString()),
      data: sensorData,
      rawData: p.raw_hex || p.raw_data,
      isRealtime: true,
    }
    addData(dataItem)
  }

  // 订阅管理
  const types = eventTypes || (includeChannelData
    ? [WS_EVENT.DATA_UPDATE, WS_EVENT.CHANNEL_DATA]
    : [WS_EVENT.DATA_UPDATE])

  const unsubscribers: (() => void)[] = []

  onMounted(() => {
    for (const type of types) {
      unsubscribers.push(wsStore.subscribe(type, handleMessage))
    }
  })

  onUnmounted(() => {
    for (const unsub of unsubscribers) {
      unsub()
    }
    if (batchScheduled) {
      batchScheduled = false
      pendingBatch = []
    }
  })

  return {
    dataItems,
    latestData,
    messageCount,
    addData,
    addDataBatch,
    clear,
  }
}
