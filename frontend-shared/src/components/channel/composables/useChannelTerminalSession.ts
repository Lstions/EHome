import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { feedback, extractErrorMessage } from '@/utils/feedback'
import { channelApi, type Channel } from '@/api/channel'
import { useWebSocketStore, type WebSocketMessage } from '@/stores/websocket'
import { WS_EVENT } from '@/events/events'
import { logger } from '@/utils/logger'
import { getHardwareLabel } from '@/utils/hardwareTag'
import { asciiToHex, formatHexInput, isValidHex } from '../terminalCodec'
import type { LogEntry } from './useChannelTerminalLogs'

/**
 * 通道终端「会话」组合式：通道清单 / 选中 / 代际隔离 / 发送 / 命令历史 / WebSocket 订阅。
 *
 * 边界依据（2026-11-12 纯搬移拆分，行为零变化）：
 *   · 搬入：自取通道清单（self-fetch）与 initialChannelId 预选状态机、collector/node 变化
 *     时的代际失效 + 全量复位、发送链路（WS 优先 / HTTP 回退 + 代际校验）、命令历史 ↑↓、
 *     CHANNEL_DATA / CHANNEL_WRITE_ERROR 两个订阅、卸载解绑。
 *   · 没搬：
 *       - 两个 .panel-log 模板 ref 只能在组件里持有（注入日志组合式用于「追加后滚底」）；
 *       - displayMode（日志展示档）留在原组件；DOM 与 scoped CSS 全部留在原组件
 *         （ChannelTerminal.spec.ts 编译原文件的 scoped 样式做 .terminal-actions 级联断言）。
 *   · ⚠ 门禁：ChannelTerminalRoute.spec.ts 的读取路径已机械改为「ChannelTerminal.vue +
 *     本文件」拼接。channelRequestGeneration++ / props.collectorId !== collectorId /
 *     props.nodeDeviceId !== nodeDeviceId / watch(() => [props.collectorId, props.nodeDeviceId] /
 *     selectedChannelId.value = undefined / localChannels.value = [] / clearLog() /
 *     generation !== channelRequestGeneration / sending.value = false 仍逐字出现在拼接文本里，
 *     该文件里的断言一字未改。
 */
export interface ChannelTerminalProps {
  collectorId: number | string
  nodeDeviceId?: string
  channels?: Channel[]
  initialChannelId?: number
}

export interface TerminalSessionOptions {
  addLog: (entry: LogEntry) => void
  clearLog: () => void
}

export function useChannelTerminalSession(props: ChannelTerminalProps, options: TerminalSessionOptions) {
  const { addLog, clearLog } = options

  // --- State ---
  const selectedChannelId = ref<number | undefined>()
  // 初始预选只执行一次：之后用户手动切换不被 channels/initialChannelId 变化覆盖
  let hasAppliedInitial = false
  const inputData = ref('')
  const inputDataAscii = ref('')
  const inputMode = ref<'hex' | 'ascii'>('hex')
  const sending = ref(false)
  const readSize = ref<number>(0)

  const showReadSize = computed(() => {
    // 归一化（同波特率 tag 的根因）：后端存大写 'SPI'/'I2C'，
    // 小写字面量直接比较在真实数据下恒 false ⇒ 读长度控件永不显示。
    const type = String(selectedChannel.value?.hardware_type || '').toLowerCase()
    return type === 'spi' || type === 'i2c'
  })

  const localChannels = ref<Channel[]>([])
  let channelRequestGeneration = 0

  // Command history
  const commandHistory = ref<string[]>([])
  const historyIndex = ref(-1)

  // Dedup: track last optimistic TX to avoid WebSocket echo in RX panel
  const lastOptimisticTx = ref<{ data: string; time: number } | null>(null)

  // WebSocket
  const wsStore = useWebSocketStore()
  let unsubscribeChannelData: (() => void) | null = null

  // --- Computed ---
  const allChannels = computed(() => props.channels?.length ? props.channels : localChannels.value)

  const selectedChannel = computed(() => {
    if (!selectedChannelId.value) return null
    return allChannels.value.find(ch => ch.id === selectedChannelId.value) || null
  })

  const currentBaud = computed(() => {
    const ch = selectedChannel.value
    if (!ch) return 0
    const bc = (ch as any).bus_config
    if (!bc || typeof bc !== 'string' || bc.length < 12) return 0
    try {
      const hex = bc.replace(/\s/g, '')
      if (hex.length >= 12) return parseInt(hex.substring(4, 12), 16)
    } catch {}
    return 0
  })

  interface ChannelGroup { type: string; label: string; channels: Channel[] }

  const channelGroups = computed<ChannelGroup[]>(() => {
    const groups: Map<string, Channel[]> = new Map()
    for (const ch of allChannels.value) {
      // 分组键按归一小写：后端存大写 'UART'、历史数据存小写 'uart'，
      // 不归一会把同一总线拆成两个下拉分组。
      const type = String(ch.hardware_type || '').trim().toLowerCase() || 'other'
      if (!groups.has(type)) groups.set(type, [])
      groups.get(type)!.push(ch)
    }
    // label 走共享源：改前此处私有 map 给 uart 单独造了「串口」写法，与共享源的「UART」不一致。
    return Array.from(groups.entries()).map(([type, channels]) => ({ type, label: getHardwareLabel(type), channels }))
  })

  // --- Helpers ---
  const getChannelLabel = (ch: Channel) => {
    // hardware_id 已是 'UART0' 这类总线标识；缺失时才退回共享标签，避免渲染成「UART UART」。
    const hwId = (ch as any).hardware_id || getHardwareLabel(ch.hardware_type) || '?'
    if (ch.name) return `${ch.name} (ID:${ch.id})`
    return `${hwId} (ID:${ch.id})`
  }

  const now = () => {
    const d = new Date()
    return `${d.getHours().toString().padStart(2, '0')}:${d.getMinutes().toString().padStart(2, '0')}:${d.getSeconds().toString().padStart(2, '0')}.${d.getMilliseconds().toString().padStart(3, '0')}`
  }

  // --- Send ---
  const sendData = async () => {
    if (!selectedChannelId.value) return
    const collectorId = props.collectorId
    const nodeDeviceId = props.nodeDeviceId
    const generation = channelRequestGeneration

    let hexData: string
    if (inputMode.value === 'ascii') {
      if (!inputDataAscii.value.trim()) return
      hexData = asciiToHex(inputDataAscii.value)
    } else {
      if (!inputData.value.trim()) return
      hexData = formatHexInput(inputData.value)
      if (!isValidHex(hexData)) {
        ElMessage.warning('无效的 HEX 数据（需偶数长度）')
        return
      }
    }

    const channel = selectedChannel.value
    if (!channel || !channel.node_id) {
      feedback.error('无法获取通道的节点 ID')
      return
    }
    const deviceId = String(channel.node_id)
    const channelId = selectedChannelId.value

    // Save to command history
    const historyEntry = inputMode.value === 'ascii' ? `[ASCII] ${inputDataAscii.value}` : inputData.value
    commandHistory.value.push(historyEntry)
    if (commandHistory.value.length > 50) commandHistory.value.shift()
    historyIndex.value = -1

    addLog({ type: 'send', direction: 'TX', time: now(), data: hexData, source: 'manual', channelId: selectedChannelId.value })

    // Track for RX dedup (WebSocket echoes own TX)
    lastOptimisticTx.value = { data: hexData, time: Date.now() }

    if (wsStore.connected) {
      if (generation !== channelRequestGeneration || props.collectorId !== collectorId || props.nodeDeviceId !== nodeDeviceId) return
      wsStore.send({
        type: 'send',
        payload: {
          device_id: deviceId,
          channel_id: channelId,
          data_hex: hexData,
          ...(showReadSize.value && readSize.value > 0 && { read_size: readSize.value }),
        }
      })
      ElMessage.success(`已发送 ${hexData.length / 2} 字节`)
      if (inputMode.value === 'ascii') inputDataAscii.value = ''
      else inputData.value = ''
    } else {
      sending.value = true
      try {
        const result = await channelApi.terminalWrite(channelId, deviceId, hexData, showReadSize.value ? readSize.value : undefined)
        if (generation !== channelRequestGeneration || props.collectorId !== collectorId || props.nodeDeviceId !== nodeDeviceId) return
        if (result.success) ElMessage.success(`已发送 ${hexData.length / 2} 字节`)
        else {
          feedback.error('发送失败')
          addLog({ type: 'error', direction: 'TX', time: now(), data: '操作失败', source: 'manual' })
        }
      } catch (error: any) {
        if (generation !== channelRequestGeneration || props.collectorId !== collectorId || props.nodeDeviceId !== nodeDeviceId) return
        const errMsg = extractErrorMessage(error, '未知错误')
        feedback.handleError(error, '发送失败')
        addLog({ type: 'error', direction: 'TX', time: now(), data: errMsg, source: 'manual' })
      } finally {
        if (generation === channelRequestGeneration && props.collectorId === collectorId && props.nodeDeviceId === nodeDeviceId) sending.value = false
      }
    }
  }

  // el-input 的 @keydown 声明为 (evt: Event | KeyboardEvent) => any，入口收窄为 KeyboardEvent。
  const handleKeydown = (evt: Event | KeyboardEvent) => {
    const e = evt instanceof KeyboardEvent ? evt : null
    if (!e) return
    if (e.key === 'ArrowUp') {
      e.preventDefault()
      if (historyIndex.value < commandHistory.value.length - 1) {
        historyIndex.value++
        inputData.value = commandHistory.value[commandHistory.value.length - 1 - historyIndex.value]
      }
    } else if (e.key === 'ArrowDown') {
      e.preventDefault()
      if (historyIndex.value > 0) {
        historyIndex.value--
        inputData.value = commandHistory.value[commandHistory.value.length - 1 - historyIndex.value]
      } else {
        historyIndex.value = -1
        inputData.value = ''
      }
    }
  }

  const onChannelChange = () => {
    clearLog()
    inputData.value = ''
    inputDataAscii.value = ''
    readSize.value = 0
  }

  // --- Initial channel pre-selection ---
  const applyInitialChannel = () => {
    // 仅在 initialChannelId 设置了且尚未预选成功时生效
    if (hasAppliedInitial || props.initialChannelId === undefined) return
    if (allChannels.value.some(ch => ch.id === props.initialChannelId)) {
      hasAppliedInitial = true
      selectedChannelId.value = props.initialChannelId
    }
  }

  const applyInitialChannelChange = () => {
    // initialChannelId 显式变化：父组件主动意图，有效则跟随更新；
    // 无效（undefined/不存在）时不动作（不清空用户当前选择）
    const id = props.initialChannelId
    if (id === undefined) return
    if (allChannels.value.some(ch => ch.id === id)) {
      hasAppliedInitial = true   // 保持状态机一致：预选已发生，channels watch 不再重复 apply
      selectedChannelId.value = id
    }
  }

  // --- Channel loading ---
  const loadChannels = async () => {
    if (props.channels?.length) return
    const generation = ++channelRequestGeneration
    const collectorId = props.collectorId
    const nodeDeviceId = props.nodeDeviceId
    try {
      const queryId = nodeDeviceId || collectorId
      const result = await channelApi.getList(queryId as any)
      if (generation !== channelRequestGeneration || props.collectorId !== collectorId || props.nodeDeviceId !== nodeDeviceId) return
      localChannels.value = Array.isArray(result) ? result : (result.items || [])
      // self-fetch 模式下 props.channels 恒为空/未变，watch 不会触发；
      // 这里补一次预选，覆盖"异步 channels 到达"路径（hasAppliedInitial 保证只生效一次）
      applyInitialChannel()
    } catch (error: any) {
      logger.error('加载通道列表失败', { error: String(error) })
    }
  }

  // --- WebSocket ---
  const setupWebSocket = () => {
    if (!wsStore.connected) wsStore.connect()

    unsubscribeChannelData = wsStore.subscribe(WS_EVENT.CHANNEL_DATA, (message: WebSocketMessage) => {
      const payload = message.payload as any
      if (!payload) return
      if (!selectedChannelId.value || payload.channel_id !== selectedChannelId.value) return

      const data = payload.raw_hex

      // Dedup: if this RX matches our last sent TX within 200ms, skip it
      if (data && lastOptimisticTx.value) {
        const dedup = lastOptimisticTx.value
        if (dedup.data === data && Date.now() - dedup.time < 200) {
          lastOptimisticTx.value = null
          return  // echo suppressed
        }
      }

      const source: 'interactive' | 'scheduled' = payload.request_id ? 'interactive' : 'scheduled'

      if (data) {
        addLog({ type: 'recv', direction: 'RX', time: now(), data, source, channelId: payload.channel_id, errorCode: payload.error_code })
      } else if (payload.request_id) {
        const errInfo = payload.error_code ? ` [ERR:${payload.error_code}]` : ''
        addLog({ type: payload.error_code ? 'error' : 'recv', direction: 'RX', time: now(), data: payload.error_code ? `无响应${errInfo}` : '✓ 确认', source: 'interactive', channelId: payload.channel_id, errorCode: payload.error_code })
      }
    })

    const unsubWriteError = wsStore.subscribe(WS_EVENT.CHANNEL_WRITE_ERROR, (message: WebSocketMessage) => {
      const payload = message.payload as any
      if (!payload) return
      if (!selectedChannelId.value || payload.channel_id !== selectedChannelId.value) return
      addLog({ type: 'error', direction: 'TX', time: now(), data: payload.error || '写入请求失败', source: 'manual' })
    })

    const origUnsub = unsubscribeChannelData
    unsubscribeChannelData = () => { origUnsub?.(); unsubWriteError() }
  }

  // --- Lifecycle ---
  onMounted(() => { loadChannels(); setupWebSocket(); applyInitialChannel() })
  onUnmounted(() => {
    channelRequestGeneration++
    sending.value = false
    if (unsubscribeChannelData) { unsubscribeChannelData(); unsubscribeChannelData = null }
  })
  watch(() => [props.collectorId, props.nodeDeviceId] as const, () => {
    channelRequestGeneration++
    selectedChannelId.value = undefined
    inputData.value = ''
    inputDataAscii.value = ''
    readSize.value = 0
    localChannels.value = []
    clearLog()
    lastOptimisticTx.value = null
    sending.value = false
    void loadChannels()
  })
  watch(() => props.initialChannelId, () => {
    applyInitialChannelChange()
  })
  watch(() => props.channels, (channels) => {
    if (channels?.length) {
      channelRequestGeneration++
      localChannels.value = []
    }
    // 初始预选：channels 异步到达后首次出现该 id 时选中（hasAppliedInitial 保证只一次）
    applyInitialChannel()
    if (selectedChannelId.value && !channels?.some(channel => channel.id === selectedChannelId.value)) {
      selectedChannelId.value = undefined
    }
  })

  return {
    selectedChannelId,
    selectedChannel,
    currentBaud,
    channelGroups,
    showReadSize,
    inputData,
    inputDataAscii,
    inputMode,
    readSize,
    sending,
    getChannelLabel,
    onChannelChange,
    sendData,
    handleKeydown,
  }
}
