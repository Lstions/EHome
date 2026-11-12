import { computed, ref } from 'vue'
import { useRoute } from 'vue-router'
import { configSyncStateLabel } from '@/utils/configSyncState'
import { nodeApi, type Node, type OTARecord } from '@/api/node'
import { channelApi, type Channel } from '@/api/channel'
import client from '@/api/client'
import { useWebSocketStore, type WebSocketMessage } from '@/stores/websocket'
import { useEdgeDeviceStore } from '@/stores/edgeDevice'
import { WS_EVENT } from '@/events/events'
import { UNKNOWN, formatTime } from '@/utils/format'
import { logger } from '@/utils/logger'
import { channelStatusText } from './channelDisplay'

/**
 * 节点总览的数据层：节点详情 / 通道 / 事件 / 关联设备 / OTA 历史，以及它们的
 * 派生展示文本与防竞态序列号。
 *
 * 为什么整块搬（而不是按接口再拆五个 composable）：这五份数据由同一个
 * `watch(route.params.id)` 生命周期驱动 —— 路由切换时**必须一起清空、一起递增代际**，
 * 否则会出现「节点已切换但上一节点的通道还在列表里」这类串台。拆开就要在页面里
 * 手工维护「谁依赖谁先重置」的顺序，反而把耦合藏到了调用点。派生文本
 * （在线时长/质量色/配置同步文案）只读 `node`，与取数共享同一个响应式源，
 * 单独放一份只会多一层无意义的转发。
 */

interface NodeEvent {
  id: number
  node_id: string
  event_type: string
  old_status: string
  new_status: string
  created_at: string
}

interface DataOptions {
  /** 当前页签标签；fetchDetail 只在「总线配置 / OTA 历史」按需补拉对应数据。 */
  activeTab: () => string
  /** 由页面提供的总线能力拉取（与页签激活共用同一份防重复逻辑）。 */
  fetchBusData: () => void | Promise<void>
  /** 节点在线但没有任何延迟数据时自动测一次（原逻辑在 fetchDetail 内）。 */
  onOnlineWithoutLatency: () => void
}

export function useNodeOverviewData(options: DataOptions) {
  const route = useRoute()
  const wsStore = useWebSocketStore()
  const edgeDeviceStore = useEdgeDeviceStore()

  // ── 防竞态序列号（NodeDetail 模式） ──
  //
  // 用普通对象而不是 ref：这些计数器只参与「异步回来时是否还属于当前代际」的判断，
  // 不驱动任何渲染；做成 ref 反而会让每次取数都触发一轮无意义的重渲染。
  const seq = {
    detailSequence: 0,
    channelsSequence: 0,
    eventsSequence: 0,
    devicesSequence: 0,
    otaSequence: 0,
  }

  // ── 页面状态 ──
  const loading = ref(false)
  const refreshing = ref(false)
  const node = ref<Node | null>(null)
  const channels = ref<Channel[]>([])
  const channelsLoading = ref(false)
  const nodeEvents = ref<NodeEvent[]>([])
  const eventsLoading = ref(false)
  const devices = ref<any[]>([])
  const devicesLoading = ref(false)
  const otaHistory = ref<OTARecord[]>([])
  const otaHistoryLoading = ref(false)

  // 会话时钟
  const nowTick = ref(Date.now())
  const lastRefreshAt = ref(0)
  let sessionTimer: ReturnType<typeof setInterval> | null = null
  let unsubscribeStatus: (() => void) | null = null

  // ── 派生状态 ──
  const nodeSerial = computed(() => node.value?.node_id || (route.params.id as string))
  const nodeOnline = computed(() => node.value?.status === 'online')
  const nodeOffline = computed(() => node.value?.status !== 'online')

  const pageTitle = computed(() => {
    if (node.value?.name) return node.value.name
    const id = nodeSerial.value
    return id ? `节点 ${id.slice(0, 8)}` : '节点总览'
  })

  const latencyMs = computed(() => node.value?.ping_latency_ms || node.value?.latency_ms || 0)

  const qualityText = computed(() => {
    const q = node.value?.connection_quality ?? 0
    if (q >= 80) return '优秀'
    if (q >= 60) return '良好'
    if (q >= 40) return '一般'
    return '较差'
  })
  const qualityColor = computed(() => {
    const q = node.value?.connection_quality ?? 0
    // F32：改为页面级 token 引用。该返回值只喂给 :style 的 color/background，
    // Vue 不解析它，最终由 CSS 引擎解析 var() ⇒ 亮/暗主题各自取到正确的页面 token。
    // 修复前返回字面量 hex，实测同一元素在亮/暗两主题下计算色**完全相同**。
    if (q >= 80) return 'var(--no-success-text)'
    if (q >= 60) return 'var(--no-primary)'
    if (q >= 40) return 'var(--no-warning-text)'
    return 'var(--no-danger)'
  })

  const lastOnlineText = computed(() => {
    const t = node.value?.last_online_time
    if (!t) return UNKNOWN
    return formatTime(t)
  })

  // 在线时长：从 last_online_time 到现在（每秒走字），离线显示统一未知占位符 UNKNOWN（'—'）
  const sessionDuration = computed(() => {
    const t = node.value?.last_online_time
    if (!t || !nodeOnline.value) return UNKNOWN
    const start = new Date(t).getTime()
    if (isNaN(start)) return UNKNOWN
    const diff = Math.floor((nowTick.value - start) / 1000)
    if (diff < 0) return UNKNOWN
    const days = Math.floor(diff / 86400)
    const hours = Math.floor((diff % 86400) / 3600)
    const minutes = Math.floor((diff % 3600) / 60)
    const parts: string[] = []
    if (days > 0) parts.push(`${days}天`)
    if (hours > 0) parts.push(`${hours}小时`)
    parts.push(`${minutes}分钟`)
    return parts.join(' ')
  })

  // 固件上报的 uptime（秒）转可读文本
  const uptimeText = computed(() => {
    const s = node.value?.uptime_seconds
    if (!s || !nodeOnline.value) return UNKNOWN
    const days = Math.floor(s / 86400)
    const hours = Math.floor((s % 86400) / 3600)
    const minutes = Math.floor((s % 3600) / 60)
    const parts: string[] = []
    if (days > 0) parts.push(`${days}天`)
    if (hours > 0) parts.push(`${hours}小时`)
    parts.push(`${minutes}分钟`)
    return parts.join(' ')
  })

  const freeHeapText = computed(() => {
    const b = node.value?.free_heap_bytes
    if (!b || b <= 0) return ''
    return (b / 1024).toFixed(0)
  })

  const connectionTypeText = computed(() => {
    const ct = node.value?.connection_type
    if (!ct) return UNKNOWN
    return { wifi: 'WiFi', ethernet: '以太网', mqtt: 'MQTT' }[ct] || ct
  })

  const syncStateLabel = computed(() => {
    if (nodeOffline.value) return '离线'
    return configSyncStateLabel(node.value?.config_sync_state)
  })

  /**
   * ⚠ 2026-10-09（用户要求："DMA 分不到降级为提示"）：
   * 设备上报的配置**告警**（不是错误）。
   *
   * ⚠ 与 syncStateLabel 分开：配置是**成功**的，只是某些通道降级了。
   *   合在一起显示会让用户以为配置失败。
   *
   * 容错：后端可能给 null/undefined（老节点没这个字段），
   * 且元素形状不保证 —— 只保留有 message 的项，避免渲染出空白行。
   */
  const configWarnings = computed(() => {
    const raw = node.value?.config_warnings
    if (!Array.isArray(raw)) return []
    return raw.filter((w): w is { code: string; channel_id?: number; message: string } =>
      !!w && typeof w === 'object' && typeof (w as { message?: unknown }).message === 'string' && (w as { message: string }).message !== '')
  })

  const metricsUpdatedText = computed(() => {
    if (!lastRefreshAt.value) return ''
    const ago = Math.max(0, Math.floor((nowTick.value - lastRefreshAt.value) / 1000))
    return `更新于 ${ago} 秒前`
  })

  const channelStats = computed(() => {
    const total = channels.value.length
    const ok = channels.value.filter(c => channelStatusText(c) === '正常').length
    const error = channels.value.filter(c => channelStatusText(c) === '异常').length
    return { total, ok, error, other: total - ok - error }
  })

  /**
   * 已被**已启用通道**占用的资源 id（给 ChannelManager 的下拉框标灰用）。
   *
   * 口径与后端 validateChannelCrossChannelPins 一致：只看 enabled 的通道。
   * 停用通道不占资源——否则"想换个串口"的用户会发现自己哪儿都选不了。
   * 用 hardware_id（"UART1"）而不是引脚号：下拉框的选项就是资源 id。
   */
  const occupiedHardwareIds = computed<string[]>(() => {
    return channels.value
      .filter(channel => channel.enabled !== false)
      .map(channel => String(channel.hardware_id || '').trim())
      .filter(Boolean)
  })

  // ── 事件 ──
  const recentEvents = computed(() => nodeEvents.value.slice(0, 5))
  function eventText(ev: NodeEvent): string {
    // A-nodes P2（2026-10-21）：status_change 原先不在映射表里，落到 fallback 直接渲染
    // 内部枚举「status_change（在线）」—— 1440 下事件卡只有 326px 宽，还被 ellipsis 截成
    // 「status_change（在…」，把"在线/离线"这一关键结论截掉。它和 status 是同一语义
    // （设备状态跃变），直接复用同一中文结论；new_status 缺失时给中性中文，不再泄漏英文。
    if (ev.event_type === 'status' || ev.event_type === 'status_change') {
      if (ev.new_status === 'online') return '设备上线'
      if (ev.new_status === 'offline') return '设备离线'
      return '状态变更'
    }
    // event_type 其他取值（如 hello/ota/config）做可读映射，避免英文原文泄漏
    const typeMap: Record<string, string> = {
      hello: '设备握手', ota: 'OTA 升级', config: '配置变更', offline: '设备离线', online: '设备上线',
    }
    const label = typeMap[ev.event_type] || ev.event_type
    // old/new_status 是英文枚举，仅在与 type 不同且能提供信息时追加，且翻译为中文
    const statusMap: Record<string, string> = { online: '在线', offline: '离线' }
    const newSt = statusMap[ev.new_status] || ev.new_status
    if (ev.event_type === 'offline' || ev.event_type === 'online') return label
    return newSt ? `${label}（${newSt}）` : label
  }

  // ── 数据加载 ──
  async function fetchDetail() {
    const id = route.params.id as string
    if (!id) return
    loading.value = true
    const sequence = ++seq.detailSequence
    try {
      const result = await nodeApi.getDetail(id)
      if (sequence !== seq.detailSequence || route.params.id !== id) return
      node.value = result
      lastRefreshAt.value = Date.now()
      // 序列号就绪后拉通道/事件（channel/events 按 node_id 序列号过滤）
      void fetchChannels()
      void fetchEvents()
      void fetchDevices()
      if (options.activeTab() === '总线配置') void options.fetchBusData()
      if (options.activeTab() === 'OTA 历史') void fetchOTAHistory()
      // 在线且无延迟数据时自动测一次延迟
      if (result.status === 'online' && !result.ping_latency_ms && !result.latency_ms) {
        options.onOnlineWithoutLatency()
      }
    } catch (err: any) {
      if (sequence === seq.detailSequence) {
        node.value = null
        logger.error('获取节点详情失败', { error: String(err) })
      }
    } finally {
      if (sequence === seq.detailSequence) loading.value = false
    }
  }

  async function fetchChannels() {
    const id = route.params.id as string
    const serial = nodeSerial.value
    if (!id || !serial) return
    const sequence = ++seq.channelsSequence
    channelsLoading.value = true
    try {
      const res = await channelApi.getList(serial)
      if (sequence !== seq.channelsSequence || route.params.id !== id) return
      channels.value = Array.isArray(res) ? res : (res.items || [])
    } catch (err: any) {
      if (sequence === seq.channelsSequence) {
        channels.value = []
        logger.error('获取通道列表失败', { error: String(err) })
      }
    } finally {
      if (sequence === seq.channelsSequence) channelsLoading.value = false
    }
  }

  async function fetchEvents() {
    const id = route.params.id as string
    if (!id) return
    const sequence = ++seq.eventsSequence
    eventsLoading.value = true
    try {
      const res = await client.get<unknown, { data: NodeEvent[] }>(`/api/v1/nodes/${id}/status-history`, { params: { limit: 50 } })
      if (sequence !== seq.eventsSequence || route.params.id !== id) return
      nodeEvents.value = Array.isArray(res?.data) ? res.data : []
    } catch (err: any) {
      if (sequence === seq.eventsSequence) {
        nodeEvents.value = []
        logger.error('获取节点事件失败', { error: String(err) })
      }
    } finally {
      if (sequence === seq.eventsSequence) eventsLoading.value = false
    }
  }

  // ── 关联设备 ──
  async function fetchDevices() {
    const id = route.params.id as string
    const serial = nodeSerial.value
    if (!id || !serial) return
    const sequence = ++seq.devicesSequence
    devicesLoading.value = true
    try {
      const params = { node_id: serial, page: 1, page_size: 100 }
      await edgeDeviceStore.fetchList(params, true)
      if (sequence !== seq.devicesSequence || route.params.id !== id) return
      devices.value = edgeDeviceStore.getCachedList(params)?.items || []
    } catch (err: any) {
      if (sequence === seq.devicesSequence) {
        devices.value = []
        logger.error('获取关联设备失败', { error: String(err) })
      }
    } finally {
      if (sequence === seq.devicesSequence) devicesLoading.value = false
    }
  }

  // ── OTA 历史 ──
  async function fetchOTAHistory() {
    const id = route.params.id as string
    if (!id) return
    const sequence = ++seq.otaSequence
    otaHistoryLoading.value = true
    try {
      const history = (await nodeApi.getOTAHistory(id)) || []
      if (sequence !== seq.otaSequence || route.params.id !== id) return
      otaHistory.value = history
    } catch (err: any) {
      if (sequence === seq.otaSequence) {
        otaHistory.value = []
        logger.error('获取 OTA 历史失败', { error: String(err) })
      }
    } finally {
      if (sequence === seq.otaSequence) otaHistoryLoading.value = false
    }
  }

  /**
   * 路由切换时的数据重置。
   *
   * 为什么必须在**同一个函数**里递增全部代际并清空：只清数据不递增代际，
   * 上一节点在途的响应回来后会重新写满列表（串台）；只递增不清空，
   * 新节点详情返回前的这一帧会显示上一个节点的数据。两者缺一都会复现缺陷。
   */
  function resetForRouteChange() {
    seq.detailSequence++
    seq.channelsSequence++
    seq.eventsSequence++
    seq.devicesSequence++
    seq.otaSequence++
    node.value = null
    channels.value = []
    nodeEvents.value = []
    devices.value = []
    otaHistory.value = []
  }

  function startClock() {
    sessionTimer = setInterval(() => { nowTick.value = Date.now() }, 1000)
  }

  /**
   * 节点状态更新（node_status 无延迟字段，仅状态/uptime）。
   * 延迟结果不在这里处理：它由页头「测延迟」的 5s 超时兜底，见 useNodeHeaderActions。
   */
  function startStatusSubscription() {
    const unsubStatus = wsStore.subscribe(WS_EVENT.NODE_STATUS, (message: WebSocketMessage) => {
      if (message.payload?.node_id !== nodeSerial.value) return
      if (node.value && message.payload?.status) {
        node.value = {
          ...node.value,
          status: message.payload.status === 'online' ? 'online' : 'offline',
          uptime_seconds: message.payload.uptime_seconds ?? node.value.uptime_seconds,
        }
      }
    })
    unsubscribeStatus = () => { unsubStatus() }
  }

  /** 卸载：递增全部代际让在途请求的落地变成空操作，并停掉时钟/订阅。 */
  function stopAll() {
    seq.detailSequence++
    seq.channelsSequence++
    seq.eventsSequence++
    seq.devicesSequence++
    seq.otaSequence++
    if (sessionTimer) { clearInterval(sessionTimer); sessionTimer = null }
    if (unsubscribeStatus) { unsubscribeStatus(); unsubscribeStatus = null }
  }

  return {
    seq,
    loading,
    refreshing,
    node,
    channels,
    channelsLoading,
    nodeEvents,
    eventsLoading,
    devices,
    devicesLoading,
    otaHistory,
    otaHistoryLoading,
    nowTick,
    lastRefreshAt,
    nodeSerial,
    nodeOnline,
    nodeOffline,
    pageTitle,
    latencyMs,
    qualityText,
    qualityColor,
    lastOnlineText,
    sessionDuration,
    uptimeText,
    freeHeapText,
    connectionTypeText,
    syncStateLabel,
    configWarnings,
    metricsUpdatedText,
    channelStats,
    occupiedHardwareIds,
    recentEvents,
    eventText,
    fetchDetail,
    fetchChannels,
    fetchEvents,
    fetchDevices,
    fetchOTAHistory,
    resetForRouteChange,
    startClock,
    startStatusSubscription,
    stopAll,
  }
}

export type NodeOverviewData = ReturnType<typeof useNodeOverviewData>
export type { NodeEvent }
