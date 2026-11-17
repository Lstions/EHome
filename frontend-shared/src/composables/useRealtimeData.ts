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
  /** 每个字段最近一次被上报的采集时刻（epoch ms；缺键 = 该字段还没上报过） */
  fieldAt: Ref<Record<string, number>>
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
  /**
   * 每个字段**最近一次被上报**的采集时刻（epoch ms）。2026-11-17 新增。
   *
   * 为什么 latestData 不够：它只是"各字段当前值"的字典，看不出某个字段有多新。
   * BMS 的 MOS 卡需要判断「手里这个 fet_status 是不是比那次写后回读更新」——
   * 更旧的帧不得覆盖权威回读，否则开关会先被打回旧状态再跳回来（真机实测的"跳一下"）。
   *
   * ⚠ 不能用"最新一帧的时刻"代替：0xAA 保护次数 / 0x04 单体电压等帧**不含 fet_status**，
   * 它们只能说明"有别的新数据到了"，不能说明 MOS 状态更新过 —— 用错就会把刚确认的
   * 开关状态打回旧值，等于把要修的抖动原样搬回来。故按**字段**记时刻。
   * 只前进不回退：乱序/重投递的旧帧不会把时刻拉回去。
   */
  const fieldAt = ref<Record<string, number>>({})
  const messageCount = ref(0)

  /** 记录一帧里各字段的采集时刻（只前进，不回退） */
  const trackFieldAt = (data: unknown, timestamp: unknown) => {
    if (!data || typeof data !== 'object') return
    const ms = typeof timestamp === 'string' ? Date.parse(timestamp) : NaN
    if (!Number.isFinite(ms)) return
    let changed = false
    const next = { ...fieldAt.value }
    for (const key of Object.keys(data as Record<string, unknown>)) {
      if (ms > (next[key] ?? 0)) {
        next[key] = ms
        changed = true
      }
    }
    if (changed) fieldAt.value = next
  }

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
      trackFieldAt(item.data, item.timestamp)
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
      // 批次内逐条记录字段时刻（不假设 items 已排序）
      for (const item of items) trackFieldAt(item.data, item.timestamp)
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
    const msgDeviceId = Number(p.edge_device_id)
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
    fieldAt,
    messageCount,
    addData,
    addDataBatch,
    clear,
  }
}
