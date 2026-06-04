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
            :label="`${ch.name || (ch.hardware_type?.toUpperCase() + ' ' + ch.hardware_id)} (ID:${ch.id})`"
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

      <div style="flex: 1;"></div>

      <!-- 显示模式切换 -->
      <el-radio-group v-model="displayMode" size="small">
        <el-radio-button value="hex">HEX</el-radio-button>
        <el-radio-button value="ascii">ASCII</el-radio-button>
      </el-radio-group>

      <el-button size="small" @click="clearLog" :disabled="logEntries.length === 0">
        清空
      </el-button>
    </div>

    <!-- 双面板日志区域 -->
    <div class="terminal-dual-panel">
      <!-- 下行面板 (TX) -->
      <div class="terminal-panel tx-panel">
        <div class="panel-header">
          <span class="panel-label">▼ 下行 (TX)</span>
          <span class="panel-count">{{ txEntries.length }}</span>
        </div>
        <div class="panel-log" ref="txLogContainer">
          <div v-if="txEntries.length === 0" class="panel-empty">无下行数据</div>
          <div
            v-for="(entry, index) in txEntries"
            :key="'tx-' + index"
            class="panel-entry"
            :class="entry.type"
          >
            <span class="entry-dot dot-tx"></span>
            <span class="entry-time">{{ entry.time }}</span>
            <span class="entry-data">{{ formatData(entry.data) }}</span>
            <span v-if="entry.meta" class="entry-meta">{{ entry.meta }}</span>
          </div>
        </div>
      </div>

      <!-- 上行面板 (RX) -->
      <div class="terminal-panel rx-panel">
        <div class="panel-header">
          <span class="panel-label">▲ 上行 (RX)</span>
          <span class="panel-count">{{ rxEntries.length }}</span>
        </div>
        <div class="panel-log" ref="rxLogContainer">
          <div v-if="rxEntries.length === 0" class="panel-empty">无上行数据</div>
          <div
            v-for="(entry, index) in rxEntries"
            :key="'rx-' + index"
            class="panel-entry"
            :class="entry.type"
          >
            <span class="entry-dot" :class="entry.source === 'interactive' ? 'dot-interactive' : 'dot-rx'"></span>
            <span class="entry-time">{{ entry.time }}</span>
            <span class="entry-data">{{ formatData(entry.data) }}</span>
            <span v-if="entry.source === 'interactive'" class="entry-meta interactive">[交互]</span>
            <span v-if="entry.errorCode" class="entry-meta error">[ERR:{{ entry.errorCode }}]</span>
            <span v-if="entry.meta" class="entry-meta">{{ entry.meta }}</span>
          </div>
        </div>
      </div>
    </div>

    <!-- 发送区域 -->
    <el-card class="terminal-send-card" shadow="never">
      <div class="terminal-input">
        <el-input
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

        <el-button
          type="primary"
          size="small"
          @click="sendData"
          :loading="sending"
          :disabled="!selectedChannelId || !inputData.trim()"
        >
          发送
        </el-button>
      </div>
    </el-card>

    <!-- 快捷命令 -->
    <div v-if="quickCommands.length > 0" class="quick-commands">
      <span class="quick-label">快捷命令</span>
      <el-tag
        v-for="(cmd, i) in quickCommands"
        :key="i"
        type="info"
        effect="plain"
        class="quick-cmd-tag"
        @click="sendQuickCommand(cmd)"
        :disable-transitions="true"
      >
        {{ cmd.label }}
      </el-tag>
    </div>
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
  collectorId: number
  channels?: Channel[]
}

const props = defineProps<Props>()

// State
const selectedChannelId = ref<number | undefined>()
const inputData = ref('')
const displayMode = ref<'hex' | 'ascii'>('hex')
const sending = ref(false)
const txLogContainer = ref<HTMLDivElement>()
const rxLogContainer = ref<HTMLDivElement>()

// Pure data flow: log entry is simple — just direction + data + source
interface LogEntry {
  type: 'send' | 'recv' | 'error' | 'info'
  direction: 'TX' | 'RX'
  time: string
  data: string
  source: 'scheduled' | 'interactive' | 'manual'  // scheduled=周期采集, interactive=交互命令响应, manual=用户手动发送
  channelId?: number
  errorCode?: number
  meta?: string
}

const logEntries = ref<LogEntry[]>([])
const localChannels = ref<Channel[]>([])
const channelsLoading = ref(false)

// Command history
const commandHistory = ref<string[]>([])
const historyIndex = ref(-1)

// Track last optimistic TX to deduplicate WS broadcast echo
const lastOptimisticTx = ref<{ data: string; time: number } | null>(null)

// WebSocket store
const wsStore = useWebSocketStore()
let unsubscribeChannelData: (() => void) | null = null

// Computed
const allChannels = computed(() => props.channels?.length ? props.channels : localChannels.value)

const selectedChannel = computed(() => {
  if (!selectedChannelId.value) return null
  return allChannels.value.find(ch => ch.id === selectedChannelId.value) || null
})

interface ChannelGroup {
  type: string
  label: string
  channels: Channel[]
}

const channelGroups = computed<ChannelGroup[]>(() => {
  const groups: Map<string, Channel[]> = new Map()
  const typeLabels: Record<string, string> = {
    uart: '串口 (UART)',
    i2c: 'I2C',
    spi: 'SPI',
    gpio: 'GPIO',
    adc: 'ADC',
    pwm: 'PWM'
  }

  for (const ch of allChannels.value) {
    const type = ch.hardware_type || 'other'
    if (!groups.has(type)) groups.set(type, [])
    groups.get(type)!.push(ch)
  }

  return Array.from(groups.entries()).map(([type, channels]) => ({
    type,
    label: typeLabels[type] || type,
    channels
  }))
})

// 下行条目 (TX)
const txEntries = computed(() => logEntries.value.filter(e => e.direction === 'TX'))

// 上行条目 (RX)
const rxEntries = computed(() => logEntries.value.filter(e => e.direction === 'RX'))

// 快捷命令（从 channel config.commands 读取）
const quickCommands = computed(() => {
  if (!selectedChannel.value) return []
  const cmds = selectedChannel.value.config?.commands
  if (!cmds || !Array.isArray(cmds)) return []
  return cmds
    .filter((c: any) => c.write)
    .map((c: any) => ({
      label: c.key || c.write,
      write: c.write,
    }))
})

// Methods
const getTagType = (type: string) => {
  const types: Record<string, string> = {
    gpio: 'info', adc: 'success', i2c: 'warning', spi: 'danger', uart: '', pwm: 'primary'
  }
  return types[type] || ''
}

const now = () => {
  const d = new Date()
  return `${d.getHours().toString().padStart(2, '0')}:${d.getMinutes().toString().padStart(2, '0')}:${d.getSeconds().toString().padStart(2, '0')}.${d.getMilliseconds().toString().padStart(3, '0')}`
}

const addLog = (entry: LogEntry) => {
  logEntries.value.push(entry)
  // 限制最大条目数
  if (logEntries.value.length > 500) {
    logEntries.value = logEntries.value.slice(-400)
  }
  nextTick(() => {
    if (entry.direction === 'TX' && txLogContainer.value) {
      txLogContainer.value.scrollTop = txLogContainer.value.scrollHeight
    }
    if (entry.direction === 'RX' && rxLogContainer.value) {
      rxLogContainer.value.scrollTop = rxLogContainer.value.scrollHeight
    }
  })
}

const clearLog = () => {
  logEntries.value = []
}

const formatHexInput = (input: string): string => {
  return input.replace(/\s+/g, '').replace(/0x/gi, '').toUpperCase()
}

const isValidHex = (str: string): boolean => {
  return /^[0-9A-Fa-f]*$/.test(str) && str.length % 2 === 0
}

const hexToAscii = (hex: string): string => {
  let result = ''
  for (let i = 0; i < hex.length; i += 2) {
    const code = parseInt(hex.substring(i, i + 2), 16)
    result += (code >= 32 && code <= 126) ? String.fromCharCode(code) : '.'
  }
  return result
}

const formatData = (data: string): string => {
  if (!data) return ''
  if (displayMode.value === 'ascii') {
    return hexToAscii(data)
  }
  return data.replace(/(.{2})/g, '$1 ').trim()
}

const sendData = async () => {
  if (!selectedChannelId.value || !inputData.value.trim()) return

  const hexData = formatHexInput(inputData.value)
  if (!isValidHex(hexData)) {
    ElMessage.warning('无效的 HEX 数据（需偶数长度，如 F4 或 F4 2E）')
    return
  }

  // Save to command history
  commandHistory.value.push(inputData.value)
  if (commandHistory.value.length > 50) commandHistory.value.shift()
  historyIndex.value = -1

  // Immediately log TX (optimistic update)
  addLog({
    type: 'send',
    direction: 'TX',
    time: now(),
    data: hexData,
    source: 'manual',
    channelId: selectedChannelId.value,
  })

  if (wsStore.connected) {
    lastOptimisticTx.value = { data: hexData, time: Date.now() }

    wsStore.send({
      type: 'channel_write',
      payload: {
        channel_id: selectedChannelId.value,
        data: hexData,
      }
    })
    inputData.value = ''
  } else {
    sending.value = true
    try {
      const result = await channelApi.write(selectedChannelId.value!, hexData)
      if (result.success) {
        addLog({
          type: 'info',
          direction: 'TX',
          time: now(),
          data: '✓ 已发送',
          source: 'manual',
        })
      } else {
        addLog({
          type: 'error',
          direction: 'TX',
          time: now(),
          data: '操作失败',
          source: 'manual',
        })
      }
    } catch (error: any) {
      const errMsg = error?.response?.data?.message || error?.message || '未知错误'
      addLog({
        type: 'error',
        direction: 'TX',
        time: now(),
        data: errMsg,
        source: 'manual',
      })
    } finally {
      sending.value = false
    }
  }
}

const sendQuickCommand = async (cmd: { write: string }) => {
  inputData.value = cmd.write
  await sendData()
}

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
  inputData.value = ''
}

const loadChannels = async () => {
  if (props.channels?.length) return
  channelsLoading.value = true
  try {
    const result = await channelApi.getList(props.collectorId)
    localChannels.value = Array.isArray(result) ? result : (result.items || [])
  } catch (error: any) {
    logger.error('加载通道列表失败', { error: String(error) })
  } finally {
    channelsLoading.value = false
  }
}

// WebSocket 订阅 — 纯数据流模型
// 收到 DataReport → 有 request_id = 交互响应，无 request_id = 周期采集
// 收到 channel_write_ack → 服务端确认收到写入请求
// 收到 channel_write_error → 写入请求失败
const setupWebSocket = () => {
  if (!wsStore.connected) {
    wsStore.connect()
  }

  unsubscribeChannelData = wsStore.subscribe(WS_EVENT.CHANNEL_DATA, (message: WebSocketMessage) => {
    const payload = message.payload as any
    if (!payload) return

    // 只处理当前选中通道的数据
    if (!selectedChannelId.value || payload.channel_id !== selectedChannelId.value) return

    switch (payload.direction) {
      case 'recv': {
        // DataReport 上行数据 — 有 request_id 表示交互命令响应，无则周期采集
        const source: 'interactive' | 'scheduled' = payload.request_id ? 'interactive' : 'scheduled'
        // Show data if present, or show ack if request_id exists (even with empty data)
        if (payload.data) {
          addLog({
            type: 'recv',
            direction: 'RX',
            time: now(),
            data: payload.data,
            source: source,
            channelId: payload.channel_id,
            errorCode: payload.error_code,
          })
        } else if (payload.request_id) {
          // WriteCommand ack with no data (e.g. timeout/error)
          const errInfo = payload.error_code ? ` [ERR:${payload.error_code}]` : ''
          addLog({
            type: payload.error_code ? 'error' : 'recv',
            direction: 'RX',
            time: now(),
            data: payload.error_code ? `无响应${errInfo}` : '✓ 确认',
            source: 'interactive',
            channelId: payload.channel_id,
            errorCode: payload.error_code,
          })
        }
        break
      }

      case 'send': {
        // 下行 WriteCommand 回显 — 去重乐观更新
        if (payload.data && lastOptimisticTx.value &&
            payload.data === lastOptimisticTx.value.data &&
            Date.now() - lastOptimisticTx.value.time < 2000) {
          lastOptimisticTx.value = null
          break
        }
        if (payload.data) {
          addLog({
            type: 'send',
            direction: 'TX',
            time: now(),
            data: payload.data,
            source: 'manual',
            meta: '(remote)',
          })
        }
        break
      }

      case 'send_error': {
        addLog({
          type: 'error',
          direction: 'TX',
          time: now(),
          data: payload.error || '写入失败',
          source: 'manual',
        })
        break
      }
    }
  })

  // 订阅 channel_write_error（WS 消息路由错误，如 channel 不存在）
  const unsubWriteError = wsStore.subscribe('channel_write_error', (message: WebSocketMessage) => {
    const payload = message.payload as any
    if (!payload) return
    if (!selectedChannelId.value || payload.channel_id !== selectedChannelId.value) return

    addLog({
      type: 'error',
      direction: 'TX',
      time: now(),
      data: payload.error || '写入请求失败',
      source: 'manual',
    })
  })

  // 保存取消函数，组件卸载时一起清理
  const origUnsub = unsubscribeChannelData
  unsubscribeChannelData = () => {
    origUnsub?.()
    unsubWriteError()
  }
}

// Lifecycle
onMounted(() => {
  loadChannels()
  setupWebSocket()
})

onUnmounted(() => {
  if (unsubscribeChannelData) {
    unsubscribeChannelData()
    unsubscribeChannelData = null
  }
})

watch(() => props.channels, () => {
  if (props.channels?.length && !selectedChannelId.value) {
    // 不自动选择，让用户选择
  }
})
</script>

<style scoped>
.channel-terminal {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.terminal-controls {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.terminal-dual-panel {
  display: flex;
  gap: 8px;
  height: 400px;
}

.terminal-panel {
  flex: 1;
  display: flex;
  flex-direction: column;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 4px;
  overflow: hidden;
}

.panel-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 6px 10px;
  background: var(--el-fill-color-light);
  border-bottom: 1px solid var(--el-border-color-lighter);
  font-size: 12px;
  font-weight: 500;
}

.panel-label {
  color: var(--el-text-color-regular);
}

.panel-count {
  color: var(--el-text-color-secondary);
  font-size: 11px;
}

.panel-log {
  flex: 1;
  overflow-y: auto;
  padding: 4px 0;
  font-family: 'JetBrains Mono', 'Fira Code', 'Consolas', monospace;
  font-size: 12px;
  line-height: 1.6;
}

.panel-empty {
  color: var(--el-text-color-placeholder);
  text-align: center;
  padding: 20px;
  font-size: 12px;
}

.panel-entry {
  display: flex;
  align-items: baseline;
  padding: 1px 8px;
  gap: 6px;
  min-height: 22px;
}

.panel-entry:hover {
  background: var(--el-fill-color-lighter);
}

.panel-entry.error {
  color: var(--el-color-danger);
}

.panel-entry.info {
  color: var(--el-color-success);
}

.entry-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  flex-shrink: 0;
  margin-top: 6px;
}

.dot-tx {
  background: var(--el-color-primary);
}

.dot-rx {
  background: var(--el-color-success);
}

.dot-interactive {
  background: var(--el-color-warning);
}

.entry-time {
  color: var(--el-text-color-secondary);
  font-size: 11px;
  flex-shrink: 0;
}

.entry-data {
  word-break: break-all;
}

.entry-meta {
  color: var(--el-text-color-secondary);
  font-size: 11px;
  flex-shrink: 0;
}

.entry-meta.interactive {
  color: var(--el-color-warning);
}

.entry-meta.error {
  color: var(--el-color-danger);
}

.terminal-send-card {
  :deep(.el-card__body) {
    padding: 8px 12px;
  }
}

.terminal-input {
  display: flex;
  gap: 8px;
  align-items: center;
}

.hex-input :deep(.el-input__inner) {
  font-family: 'JetBrains Mono', 'Fira Code', 'Consolas', monospace;
  letter-spacing: 0.5px;
}

.tx-prefix {
  color: var(--el-text-color-secondary);
  font-size: 12px;
  font-family: 'JetBrains Mono', 'Fira Code', 'Consolas', monospace;
}

.quick-commands {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}

.quick-label {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  flex-shrink: 0;
}

.quick-cmd-tag {
  cursor: pointer;
  font-family: 'JetBrains Mono', 'Fira Code', 'Consolas', monospace;
  font-size: 11px;
}

.quick-cmd-tag:hover {
  color: var(--el-color-primary);
}
</style>
