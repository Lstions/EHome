<template>
  <div class="channel-terminal">
    <!-- 通道选择 + 控制栏 -->
    <div class="terminal-controls">
      <el-select
        v-model="selectedChannelId"
        placeholder="选择通道"
        size="small"
        style="width: 220px;"
        filterable
        @change="onChannelChange"
      >
        <el-option-group
          v-for="group in channelGroups"
          :key="group.type"
          :label="group.label"
        >
          <el-option
            v-for="ch in group.channels"
            :key="ch.id"
            :label="getChannelLabel(ch)"
            :value="ch.id"
          />
        </el-option-group>
      </el-select>

      <el-tag v-if="selectedChannel" :type="getTagType(selectedChannel.hardware_type)" size="small">
        {{ selectedChannel.hardware_type?.toUpperCase() }}
      </el-tag>

      <el-tag v-if="selectedChannel && selectedChannel.address" type="info" size="small">
        {{ selectedChannel.address }}
      </el-tag>

      <template v-if="selectedChannel && selectedChannel.hardware_type === 'uart'">
        <el-tag type="info" size="small">{{ currentBaud }} baud</el-tag>
      </template>

      <div style="flex: 1;"></div>

      <el-radio-group v-model="displayMode" size="small">
        <el-radio-button value="hex">HEX</el-radio-button>
        <el-radio-button value="ascii">ASCII</el-radio-button>
      </el-radio-group>

      <el-button size="small" :aria-label="isPaused ? '继续终端日志' : '暂停终端日志'" @click="togglePause" :type="isPaused ? 'warning' : 'default'">
        {{ isPaused ? '▶ 继续' : '⏸ 暂停' }}
      </el-button>

      <el-button size="small" aria-label="导出终端日志" @click="exportLog" :disabled="logEntries.length === 0" title="导出日志">
        ↓ 导出
      </el-button>

      <el-button size="small" aria-label="清空终端日志" @click="clearLog" :disabled="logEntries.length === 0">
        清空
      </el-button>
    </div>

    <!-- 双面板日志区域 -->
    <div class="terminal-dual-panel">
      <!-- 下行面板 (TX) -->
      <div class="terminal-panel tx-panel" :class="{ 'panel-paused': isPaused }">
        <div class="panel-header">
          <span class="panel-label">▼ 下行 (TX)</span>
          <span class="panel-count">{{ txEntries.length }}</span>
        </div>
        <div class="panel-log" ref="txLogContainer">
          <div v-if="txEntries.length === 0" class="panel-empty">无下行数据</div>
          <template v-for="(entry, index) in txEntries" :key="'tx-' + index">
            <div
              class="panel-entry"
              :class="[entry.type, { 'entry-expandable': getByteCount(entry.data) > 32 }]"
              @click="toggleExpand('tx', index)"
            >
              <span class="entry-dot dot-tx"></span>
              <span class="entry-time">{{ entry.time }}</span>
              <span class="entry-length">[{{ getByteCount(entry.data) }}B]</span>
              <span class="entry-data">{{ formatData(entry.data, 'tx', index) }}</span>
              <span v-if="getByteCount(entry.data) > 32 && !isExpanded('tx', index)" class="entry-ellipsis">...[点击展开]</span>
            </div>
          </template>
        </div>
      </div>

      <!-- 上行面板 (RX) -->
      <div class="terminal-panel rx-panel" :class="{ 'panel-paused': isPaused }">
        <div class="panel-header">
          <span class="panel-label">▲ 上行 (RX)</span>
          <span class="panel-count">{{ rxEntries.length }}</span>
        </div>
        <div class="panel-log" ref="rxLogContainer">
          <div v-if="rxEntries.length === 0" class="panel-empty">无上行数据</div>
          <template v-for="(entry, index) in rxEntries" :key="'rx-' + index">
            <div
              class="panel-entry"
              :class="[entry.type, { 'entry-expandable': getByteCount(entry.data) > 32 }]"
              @click="toggleExpand('rx', index)"
            >
              <span class="entry-dot" :class="entry.source === 'interactive' ? 'dot-interactive' : 'dot-rx'"></span>
              <span class="entry-time">{{ entry.time }}</span>
              <span class="entry-length">[{{ getByteCount(entry.data) }}B]</span>
              <span class="entry-data">{{ formatData(entry.data, 'rx', index) }}</span>
              <span v-if="getByteCount(entry.data) > 32 && !isExpanded('rx', index)" class="entry-ellipsis">...[点击展开]</span>
              <span v-if="entry.source === 'interactive'" class="entry-meta interactive">[交互]</span>
              <span v-if="entry.errorCode" class="entry-meta error">[ERR:{{ entry.errorCode }}]</span>
            </div>
          </template>
        </div>
      </div>
    </div>

    <!-- 发送区域 -->
    <el-card class="terminal-send-card" shadow="never">
      <div class="terminal-input">
        <el-radio-group v-model="inputMode" size="small" class="input-mode-toggle">
          <el-radio-button value="hex">HEX</el-radio-button>
          <el-radio-button value="ascii">ASCII</el-radio-button>
        </el-radio-group>

        <el-input
          v-if="inputMode === 'hex'"
          v-model="inputData"
          placeholder="输入 hex 数据（如 01 03 00 00 00 02 C4 0B）"
          size="small"
          @keyup.enter="sendData"
          @keydown="handleKeydown"
          :disabled="!selectedChannelId || sending"
          clearable
          style="flex: 1;"
          class="hex-input"
        >
          <template #prefix>
            <span class="tx-prefix">TX&gt;</span>
          </template>
        </el-input>

        <el-input
          v-else
          v-model="inputDataAscii"
          placeholder="输入 ASCII 明文（如 AT+RST）"
          size="small"
          @keyup.enter="sendData"
          @keydown="handleKeydown"
          :disabled="!selectedChannelId || sending"
          clearable
          style="flex: 1;"
          class="ascii-input"
        >
          <template #prefix>
            <span class="tx-prefix">TX&gt;</span>
          </template>
        </el-input>

        <template v-if="showReadSize">
          <el-input-number
            v-model="readSize"
            :min="0" :max="256"
            :step="1"
            size="small"
            style="width: 120px;"
            controls-position="right"
            placeholder="RX 字节数"
          />
          <span class="read-size-hint" v-if="readSize === 0">仅写</span>
          <span class="read-size-hint" v-else>读 {{ readSize }}B</span>
        </template>

        <el-button
          type="primary"
          size="small"
          @click="sendData"
          :loading="sending"
          :disabled="!selectedChannelId || (!inputData.trim() && !inputDataAscii.trim())"
        >
          发送
        </el-button>
      </div>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, nextTick, onMounted, onUnmounted, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { channelApi, type Channel } from '@/api/channel'
import { useWebSocketStore, type WebSocketMessage } from '@/stores/websocket'
import { WS_EVENT } from '@/events/events'
import { logger } from '@/utils/logger'

interface Props {
  collectorId: number | string
  nodeDeviceId?: string
  channels?: Channel[]
}

const props = defineProps<Props>()

// --- State ---
const selectedChannelId = ref<number | undefined>()
const inputData = ref('')
const inputDataAscii = ref('')
const inputMode = ref<'hex' | 'ascii'>('hex')
const displayMode = ref<'hex' | 'ascii'>('hex')
const sending = ref(false)
const txLogContainer = ref<HTMLDivElement>()
const rxLogContainer = ref<HTMLDivElement>()
const isPaused = ref(false)
const expandedEntries = ref(new Set<string>())
const readSize = ref<number>(0)

const showReadSize = computed(() => {
  const type = selectedChannel.value?.hardware_type
  return type === 'spi' || type === 'i2c'
})

// GPIO 方向相关代码已移至 PeripheralControl 组件

interface LogEntry {
  type: 'send' | 'recv' | 'error' | 'info'
  direction: 'TX' | 'RX'
  time: string
  data: string
  source: 'scheduled' | 'interactive' | 'manual'
  channelId?: number
  errorCode?: number
}

const logEntries = ref<LogEntry[]>([])
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
  const typeLabels: Record<string, string> = {
    uart: '串口 (UART)', i2c: 'I2C', spi: 'SPI', adc: 'ADC'
  }
  for (const ch of allChannels.value) {
    const type = ch.hardware_type || 'other'
    if (!groups.has(type)) groups.set(type, [])
    groups.get(type)!.push(ch)
  }
  return Array.from(groups.entries()).map(([type, channels]) => ({ type, label: typeLabels[type] || type, channels }))
})

const txEntries = computed(() => logEntries.value.filter(e => e.direction === 'TX'))
const rxEntries = computed(() => logEntries.value.filter(e => e.direction === 'RX'))

// --- Helpers ---
const getTagType = (type: string) => {
  const types: Record<string, string> = { adc: 'success', i2c: 'warning', spi: 'danger', uart: '' }
  return types[type] || ''
}

const getChannelLabel = (ch: Channel) => {
  const hwId = (ch as any).hardware_id || ch.hardware_type?.toUpperCase() || '?'
  if (ch.name) return `${ch.name} (ID:${ch.id})`
  return `${hwId} (ID:${ch.id})`
}

const now = () => {
  const d = new Date()
  return `${d.getHours().toString().padStart(2, '0')}:${d.getMinutes().toString().padStart(2, '0')}:${d.getSeconds().toString().padStart(2, '0')}.${d.getMilliseconds().toString().padStart(3, '0')}`
}

let entryCounter = 0
const addLog = (entry: LogEntry) => {
  if (isPaused.value) return
  entryCounter++
  logEntries.value.push(entry)
  if (logEntries.value.length > 1000) logEntries.value = logEntries.value.slice(-800)
  nextTick(() => {
    if (entry.direction === 'TX' && txLogContainer.value) txLogContainer.value.scrollTop = txLogContainer.value.scrollHeight
    if (entry.direction === 'RX' && rxLogContainer.value) rxLogContainer.value.scrollTop = rxLogContainer.value.scrollHeight
  })
}

const clearLog = () => {
  logEntries.value = []
  expandedEntries.value.clear()
}

const togglePause = () => { isPaused.value = !isPaused.value }

const formatHexInput = (input: string): string => input.replace(/\s+/g, '').replace(/0x/gi, '').toUpperCase()
const isValidHex = (str: string): boolean => /^[0-9A-Fa-f]*$/.test(str) && str.length % 2 === 0

const asciiToHex = (ascii: string): string => {
  let hex = ''
  for (let i = 0; i < ascii.length; i++) hex += ascii.charCodeAt(i).toString(16).padStart(2, '0').toUpperCase()
  return hex
}

const hexToAscii = (hex: string): string => {
  let result = ''
  for (let i = 0; i < hex.length; i += 2) {
    const code = parseInt(hex.substring(i, i + 2), 16)
    result += (code >= 32 && code <= 126) ? String.fromCharCode(code) : '.'
  }
  return result
}

const getByteCount = (data: string): number => {
  if (!data || typeof data !== 'string') return 0
  return data.replace(/\s/g, '').length / 2
}

const isExpanded = (panel: string, index: number): boolean => expandedEntries.value.has(`${panel}-${index}`)

const toggleExpand = (panel: string, index: number) => {
  const key = `${panel}-${index}`
  if (expandedEntries.value.has(key)) expandedEntries.value.delete(key)
  else expandedEntries.value.add(key)
  expandedEntries.value = new Set(expandedEntries.value)
}

const formatData = (data: string | any, panel: string, index: number): string => {
  if (!data) return ''
  if (typeof data !== 'string') return String(data)
  const hex = data.replace(/\s/g, '')
  const byteCount = hex.length / 2
  const expanded = isExpanded(panel, index)
  if (byteCount > 32 && !expanded) {
    const truncated = hex.substring(0, 32)
    return displayMode.value === 'ascii' ? hexToAscii(truncated) : truncated.replace(/(.{2})/g, '$1 ').trim()
  }
  return displayMode.value === 'ascii' ? hexToAscii(hex) : hex.replace(/(.{2})/g, '$1 ').trim()
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
    ElMessage.error('无法获取通道的节点 ID')
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
        ElMessage.error('发送失败')
        addLog({ type: 'error', direction: 'TX', time: now(), data: '操作失败', source: 'manual' })
      }
    } catch (error: any) {
      if (generation !== channelRequestGeneration || props.collectorId !== collectorId || props.nodeDeviceId !== nodeDeviceId) return
      const errMsg = error?.response?.data?.message || error?.message || '未知错误'
      ElMessage.error(`发送失败: ${errMsg}`)
      addLog({ type: 'error', direction: 'TX', time: now(), data: errMsg, source: 'manual' })
    } finally {
      if (generation === channelRequestGeneration && props.collectorId === collectorId && props.nodeDeviceId === nodeDeviceId) sending.value = false
    }
  }
}

// GPIO 快捷操作已移至 PeripheralControl 组件

const handleKeydown = (e: KeyboardEvent) => {
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
  logEntries.value = []
  expandedEntries.value.clear()
  inputData.value = ''
  inputDataAscii.value = ''
  readSize.value = 0
}

// --- Export ---
const exportLog = () => {
  const lines = logEntries.value.map(e => {
    const dir = e.direction === 'TX' ? 'TX▼' : 'RX▲'
    const src = e.source === 'interactive' ? '[交互]' : e.source === 'manual' ? '[手动]' : ''
    const err = e.errorCode ? `[ERR:${e.errorCode}]` : ''
    const hex = e.data.replace(/(.{2})/g, '$1 ').trim()
    return `[${e.time}] ${dir} [${getByteCount(e.data)}B] ${hex} ${src}${err}`
  })
  const content = lines.join('\n')
  const blob = new Blob([content], { type: 'text/plain' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  const ts = new Date().toISOString().replace(/[:.]/g, '-').substring(0, 19)
  a.download = `terminal_${selectedChannelId.value || 'all'}_${ts}.txt`
  a.click()
  URL.revokeObjectURL(url)
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
onMounted(() => { loadChannels(); setupWebSocket() })
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
watch(() => props.channels, (channels) => {
  if (channels?.length) {
    channelRequestGeneration++
    localChannels.value = []
  }
  if (selectedChannelId.value && !channels?.some(channel => channel.id === selectedChannelId.value)) {
    selectedChannelId.value = undefined
  }
})
</script>

<style scoped>
.channel-terminal { display: flex; flex-direction: column; gap: 12px; }
.terminal-controls { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.terminal-dual-panel { display: flex; gap: 8px; height: 400px; }
.terminal-panel { flex: 1; min-width: 0; display: flex; flex-direction: column; border: 1px solid var(--el-border-color-lighter); border-radius: var(--radius-sm); overflow: hidden; }
.panel-header { display: flex; align-items: center; justify-content: space-between; padding: 6px 10px; border-bottom: 1px solid var(--el-border-color-lighter); font-size: 12px; font-weight: 500; }
.tx-panel .panel-header { background: var(--el-color-primary-light-9); color: var(--el-color-primary); }
.rx-panel .panel-header { background: var(--el-color-success-light-9); color: var(--el-color-success); }
.panel-paused .panel-header { animation: pause-blink 1.5s ease-in-out infinite; }
@keyframes pause-blink { 0%, 100% { opacity: 1; } 50% { opacity: 0.5; } }
.panel-label { color: inherit; }
.panel-count { font-size: 11px; opacity: 0.7; }
.panel-log { flex: 1; min-height: 0; overflow-y: auto; padding: 4px 0; font-family: 'JetBrains Mono', 'Fira Code', 'Consolas', monospace; font-size: 12px; line-height: 1.6; }
.panel-empty { color: var(--el-text-color-placeholder); text-align: center; padding: 20px; font-size: 12px; }
.panel-entry { display: flex; align-items: baseline; padding: 1px 8px; gap: 6px; min-height: 22px; min-width: 0; }
.panel-entry:hover { background: var(--el-fill-color-lighter); }
.panel-entry.error { color: var(--el-color-danger); }
.panel-entry.info { color: var(--el-color-success); }
.panel-entry.entry-expandable { cursor: pointer; }
.entry-dot { width: 6px; height: 6px; border-radius: 50%; flex-shrink: 0; margin-top: 6px; }
.dot-tx { background: var(--el-color-primary); }
.dot-rx { background: var(--el-color-success); }
.dot-interactive { background: var(--el-color-warning); }
.entry-time { color: var(--el-text-color-secondary); font-size: 11px; flex-shrink: 0; }
.entry-length { color: var(--el-text-color-secondary); font-size: 10px; font-family: 'JetBrains Mono', 'Fira Code', 'Consolas', monospace; flex-shrink: 0; opacity: 0.7; }
.entry-data { min-width: 0; white-space: pre-wrap; word-break: break-all; }
.entry-ellipsis { color: var(--el-color-primary); font-size: 10px; cursor: pointer; flex-shrink: 0; }
.entry-meta { font-size: 10px; opacity: 0.8; flex-shrink: 0; }
.entry-meta.interactive { color: var(--el-color-warning); }
.entry-meta.error { color: var(--el-color-danger); }
.terminal-send-card { margin: 0; }
.terminal-input { display: flex; align-items: center; gap: 8px; }
.input-mode-toggle { flex-shrink: 0; }
.tx-prefix { color: var(--el-color-primary); font-weight: 600; font-family: 'JetBrains Mono', 'Fira Code', 'Consolas', monospace; font-size: 12px; margin-right: 4px; }
.read-size-hint { font-size: 11px; color: var(--el-text-color-secondary); white-space: nowrap; }

@media (max-width: 768px) {
  .terminal-controls > :first-child {
    width: 100% !important;
  }

  .terminal-controls > div[style*="flex"] {
    display: none;
  }

  .terminal-dual-panel {
    flex-direction: column;
    height: auto;
  }

  .terminal-panel {
    height: 260px;
    flex: none;
  }

  .terminal-input {
    align-items: stretch;
    flex-wrap: wrap;
  }

  .input-mode-toggle,
  .terminal-input :deep(.el-input),
  .terminal-input :deep(.el-input-number) {
    flex: 1 1 100%;
    width: 100% !important;
  }

  .terminal-input :deep(.el-button) {
    flex: 1 1 auto;
  }
}
</style>
