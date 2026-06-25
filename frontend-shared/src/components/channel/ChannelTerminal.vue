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

      <!-- UART 波特率显示 + 修改 -->
      <template v-if="selectedChannel && selectedChannel.hardware_type === 'uart'">
        <el-tag type="info" size="small" class="baud-tag">
          {{ currentBaud }} baud
          <el-icon class="baud-edit-icon" @click="showBaudDialog = true"><Edit /></el-icon>
        </el-tag>
      </template>

      <div style="flex: 1;"></div>

      <!-- 显示模式切换 -->
      <el-radio-group v-model="displayMode" size="small">
        <el-radio-button value="hex">HEX</el-radio-button>
        <el-radio-button value="ascii">ASCII</el-radio-button>
      </el-radio-group>

      <el-button size="small" @click="togglePause" :type="isPaused ? 'warning' : 'default'">
        {{ isPaused ? '▶ 继续' : '⏸ 暂停' }}
      </el-button>

      <el-button size="small" @click="exportLog" :disabled="logEntries.length === 0" title="导出日志">
        ↓ 导出
      </el-button>

      <el-button size="small" @click="clearLog" :disabled="logEntries.length === 0">
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
              <span v-if="entry.meta" class="entry-meta">{{ entry.meta }}</span>
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
              @click="onEntryClick(entry, 'rx', index)"
            >
              <span class="entry-dot" :class="entry.source === 'interactive' ? 'dot-interactive' : 'dot-rx'"></span>
              <span class="entry-time">{{ entry.time }}</span>
              <span class="entry-length">[{{ getByteCount(entry.data) }}B]</span>
              <span class="entry-data">{{ formatData(entry.data, 'rx', index) }}</span>
              <span v-if="getByteCount(entry.data) > 32 && !isExpanded('rx', index)" class="entry-ellipsis">...[点击展开]</span>
              <span v-if="entry.source === 'interactive'" class="entry-meta interactive">[交互]</span>
              <span v-if="entry.errorCode" class="entry-meta error">[ERR:{{ entry.errorCode }}]</span>
              <span v-if="entry.meta" class="entry-meta">{{ entry.meta }}</span>
            </div>
            <!-- Modbus 解析行 -->
            <div v-if="isEntrySelected(entry) && parseModbus(entry.data)" class="panel-entry modbus-parse">
              <span class="entry-dot"></span>
              <span class="parse-detail">{{ parseModbus(entry.data) }}</span>
            </div>
          </template>
        </div>
      </div>
    </div>

    <!-- 发送区域 -->
    <el-card class="terminal-send-card" shadow="never">
      <div class="terminal-input">
        <!-- 输入模式切换 -->
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

        <!-- SPI/I2C read_size 控件 -->
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

    <!-- 波特率修改对话框 -->
    <el-dialog v-model="showBaudDialog" title="修改波特率" width="360px" :close-on-click-modal="false">
      <div style="display: flex; flex-direction: column; gap: 12px;">
        <p style="margin: 0; font-size: 13px; color: var(--el-text-color-secondary);">
          当前波特率: <strong>{{ currentBaud }}</strong>
        </p>
        <el-select v-model="newBaud" placeholder="选择波特率" filterable allow-create style="width: 100%;">
          <el-option v-for="b in baudOptions" :key="b" :label="b + ' baud'" :value="b" />
        </el-select>
        <p style="margin: 0; font-size: 12px; color: var(--el-color-warning);">
          ⚠ 修改波特率后，ESP32将重新配置UART，连接的传感器需支持新波特率
        </p>
      </div>
      <template #footer>
        <el-button @click="showBaudDialog = false">取消</el-button>
        <el-button type="primary" @click="applyBaudChange" :loading="baudChanging" :disabled="!newBaud">确认</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, nextTick, onMounted, onUnmounted, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { Edit } from '@element-plus/icons-vue'
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

// State
const selectedChannelId = ref<number | undefined>()
const inputData = ref('')
const inputDataAscii = ref('')
const inputMode = ref<'hex' | 'ascii'>('hex')
const displayMode = ref<'hex' | 'ascii'>('hex')
const sending = ref(false)
const txLogContainer = ref<HTMLDivElement>()
const rxLogContainer = ref<HTMLDivElement>()
const isPaused = ref(false)

// 帧展开状态
const expandedEntries = ref(new Set<string>())

// Modbus解析选中
const selectedEntryId = ref<string | null>(null)

// 波特率修改
const showBaudDialog = ref(false)
const newBaud = ref<number>(115200)
const baudChanging = ref(false)
const baudOptions = [4800, 9600, 19200, 38400, 57600, 115200, 230400, 460800, 921600]

// SPI/I2C 读取字节数
const readSize = ref<number>(0)
const showReadSize = computed(() => {
  const type = selectedChannel.value?.hardware_type
  return type === 'spi' || type === 'i2c'
})

// Pure data flow: log entry is simple — just direction + data + source
interface LogEntry {
  type: 'send' | 'recv' | 'error' | 'info'
  direction: 'TX' | 'RX'
  time: string
  data: string
  source: 'scheduled' | 'interactive' | 'manual'
  channelId?: number
  errorCode?: number
  meta?: string
  _id?: string  // unique id for selection tracking
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

// 从bus_config解析波特率
const currentBaud = computed(() => {
  const ch = selectedChannel.value
  if (!ch) return 0
  const bc = (ch as any).bus_config
  if (!bc || typeof bc !== 'string' || bc.length < 12) return 0
  try {
    // bus_config hex: byte0=TX, byte1=RX, bytes2-5=baud(4B big-endian)
    const hex = bc.replace(/\s/g, '')
    if (hex.length >= 12) {
      const baudHex = hex.substring(4, 12)
      return parseInt(baudHex, 16)
    }
  } catch {}
  return 0
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
      read_size: c.read_size,
    }))
})

// Methods
const getTagType = (type: string) => {
  const types: Record<string, string> = {
    gpio: 'info', adc: 'success', i2c: 'warning', spi: 'danger', uart: '', pwm: 'primary'
  }
  return types[type] || ''
}

const getChannelLabel = (ch: Channel) => {
  const hwId = (ch as any).hardware_id || ch.hardware_type?.toUpperCase() || '?'
  const id = ch.id
  // 如果有name就用name，否则用hardware_id
  if (ch.name) return `${ch.name} (ID:${id})`
  return `${hwId} (ID:${id})`
}

const now = () => {
  const d = new Date()
  return `${d.getHours().toString().padStart(2, '0')}:${d.getMinutes().toString().padStart(2, '0')}:${d.getSeconds().toString().padStart(2, '0')}.${d.getMilliseconds().toString().padStart(3, '0')}`
}

let entryCounter = 0
const addLog = (entry: LogEntry) => {
  if (isPaused.value) return  // 暂停时丢弃数据

  entry._id = `e-${++entryCounter}`
  logEntries.value.push(entry)
  // 限制最大条目数 (修复裁剪bug: 1000→800而非500→400)
  if (logEntries.value.length > 1000) {
    logEntries.value = logEntries.value.slice(-800)
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
  expandedEntries.value.clear()
  selectedEntryId.value = null
}

const togglePause = () => {
  isPaused.value = !isPaused.value
}

const formatHexInput = (input: string): string => {
  return input.replace(/\s+/g, '').replace(/0x/gi, '').toUpperCase()
}

const isValidHex = (str: string): boolean => {
  return /^[0-9A-Fa-f]*$/.test(str) && str.length % 2 === 0
}

// Convert ASCII string to hex string
const asciiToHex = (ascii: string): string => {
  let hex = ''
  for (let i = 0; i < ascii.length; i++) {
    hex += ascii.charCodeAt(i).toString(16).padStart(2, '0').toUpperCase()
  }
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

const isExpanded = (panel: string, index: number): boolean => {
  return expandedEntries.value.has(`${panel}-${index}`)
}

const toggleExpand = (panel: string, index: number) => {
  const key = `${panel}-${index}`
  if (expandedEntries.value.has(key)) {
    expandedEntries.value.delete(key)
  } else {
    expandedEntries.value.add(key)
  }
  // Trigger reactivity
  expandedEntries.value = new Set(expandedEntries.value)
}

const formatData = (data: string | any, panel: string, index: number): string => {
  if (!data) return ''
  if (typeof data !== 'string') return String(data)
  
  const hex = data.replace(/\s/g, '')
  const byteCount = hex.length / 2
  const expanded = isExpanded(panel, index)
  
  // 长帧折叠: >32字节且未展开时只显示前16字节
  if (byteCount > 32 && !expanded) {
    const truncated = hex.substring(0, 32)
    if (displayMode.value === 'ascii') {
      return hexToAscii(truncated)
    }
    return truncated.replace(/(.{2})/g, '$1 ').trim()
  }
  
  if (displayMode.value === 'ascii') {
    return hexToAscii(hex)
  }
  return hex.replace(/(.{2})/g, '$1 ').trim()
}

// Modbus帧解析
const parseModbus = (data: string): string | null => {
  if (!data || typeof data !== 'string') return null
  const hex = data.replace(/\s/g, '')
  if (hex.length < 4) return null  // 最少: slave+fc+crc(2)
  
  const bytes: number[] = []
  for (let i = 0; i < hex.length; i += 2) {
    bytes.push(parseInt(hex.substring(i, i + 2), 16))
  }
  
  // CRC校验
  let crc = 0xFFFF
  for (let i = 0; i < bytes.length - 2; i++) {
    crc ^= bytes[i]
    for (let j = 0; j < 8; j++) {
      crc = (crc & 1) ? ((crc >> 1) ^ 0xA001) : (crc >> 1)
    }
  }
  const crcOk = (bytes[bytes.length - 2] === (crc & 0xFF)) && 
                (bytes[bytes.length - 1] === ((crc >> 8) & 0xFF))
  
  const slave = bytes[0]
  const fc = bytes[1]
  
  const fcNames: Record<number, string> = {
    1: '读线圈', 2: '读离散输入', 3: '读保持寄存器', 4: '读输入寄存器',
    5: '写单线圈', 6: '写单寄存器', 15: '写多线圈', 16: '写多寄存器'
  }
  
  let detail = `从站:${slave} FC:${String(fc).padStart(2,'0')}(${fcNames[fc] || '未知'})`
  
  // 响应帧 (FC01-04: byte3=字节数)
  if (fc >= 1 && fc <= 4 && bytes.length >= 3) {
    const byteCount = bytes[2]
    detail += ` 字节数:${byteCount}`
    if (fc === 3 || fc === 4) {
      // 寄存器值
      const regCount = byteCount / 2
      if (regCount > 0 && regCount <= 10) {
        const values: number[] = []
        for (let i = 0; i < regCount; i++) {
          values.push((bytes[3 + i*2] << 8) | bytes[3 + i*2 + 1])
        }
        detail += ` 值:[${values.join(',')}]`
      }
    }
  }
  // 请求帧 (FC03/04: reg+count)
  else if ((fc === 3 || fc === 4) && bytes.length >= 6) {
    const startReg = (bytes[2] << 8) | bytes[3]
    const count = (bytes[4] << 8) | bytes[5]
    detail += ` 起始:${startReg} 数量:${count}`
  }
  
  detail += ` CRC:${crcOk ? '✓' : '✗'}`
  return detail
}

const onEntryClick = (entry: LogEntry, panel: string, index: number) => {
  // 先处理展开/折叠
  toggleExpand(panel, index)
  
  // RX条目: toggle Modbus解析显示
  if (entry.direction === 'RX' && entry._id) {
    if (selectedEntryId.value === entry._id) {
      selectedEntryId.value = null
    } else {
      selectedEntryId.value = entry._id
    }
  }
}

const isEntrySelected = (entry: LogEntry): boolean => {
  return entry._id === selectedEntryId.value
}

const sendData = async () => {
  if (!selectedChannelId.value) return

  // Determine hex data based on input mode
  let hexData: string
  if (inputMode.value === 'ascii') {
    if (!inputDataAscii.value.trim()) return
    hexData = asciiToHex(inputDataAscii.value)
  } else {
    if (!inputData.value.trim()) return
    hexData = formatHexInput(inputData.value)
    if (!isValidHex(hexData)) {
      ElMessage.warning('无效的 HEX 数据（需偶数长度，如 F4 或 F4 2E）')
      return
    }
  }

  // Get device_id (node_id) from selected channel
  const channel = selectedChannel.value
  if (!channel || !channel.node_id) {
    ElMessage.error('无法获取通道的节点 ID，请重新选择通道')
    return
  }
  const deviceId = String(channel.node_id)

  // Save to command history
  const historyEntry = inputMode.value === 'ascii' ? `[ASCII] ${inputDataAscii.value}` : inputData.value
  commandHistory.value.push(historyEntry)
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

    // Send via terminal WS protocol: type=send, payload={device_id, channel_id, data_hex, read_size}
    wsStore.send({
      type: 'send',
      payload: {
        device_id: deviceId,
        channel_id: selectedChannelId.value,
        data_hex: hexData,
        ...(showReadSize.value && readSize.value > 0 && { read_size: readSize.value }),
      }
    })
    ElMessage.success(`已发送 ${hexData.length / 2} 字节`)
    if (inputMode.value === 'ascii') {
      inputDataAscii.value = ''
    } else {
      inputData.value = ''
    }
  } else {
    sending.value = true
    try {
      const result = await channelApi.terminalWrite(
        selectedChannelId.value!, deviceId, hexData,
        showReadSize.value ? readSize.value : undefined
      )
      if (result.success) {
        ElMessage.success(`已发送 ${hexData.length / 2} 字节`)
      } else {
        ElMessage.error('发送失败')
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
      ElMessage.error(`发送失败: ${errMsg}`)
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

const sendQuickCommand = async (cmd: { write: string; read_size?: number }) => {
  inputMode.value = 'hex'
  inputData.value = cmd.write
  // SPI/I2C 快捷命令如果配置了 read_size，自动填入
  if (showReadSize.value && cmd.read_size !== undefined) {
    readSize.value = cmd.read_size
  }
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
  expandedEntries.value.clear()
  selectedEntryId.value = null
  inputData.value = ''
  inputDataAscii.value = ''
  readSize.value = 0
}

// 波特率修改
const applyBaudChange = async () => {
  if (!selectedChannelId.value || !newBaud.value) return
  baudChanging.value = true
  try {
    await channelApi.reconfigure(selectedChannelId.value, newBaud.value)
    ElMessage.success(`波特率修改请求已发送: ${newBaud.value}`)
    showBaudDialog.value = false
  } catch (error: any) {
    ElMessage.error(`修改失败: ${error?.message || '未知错误'}`)
  } finally {
    baudChanging.value = false
  }
}

// 导出日志
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

const loadChannels = async () => {
  if (props.channels?.length) return
  channelsLoading.value = true
  try {
    const queryId = props.nodeDeviceId || props.collectorId
    const result = await channelApi.getList(queryId as any)
    localChannels.value = Array.isArray(result) ? result : (result.items || [])
  } catch (error: any) {
    logger.error('加载通道列表失败', { error: String(error) })
  } finally {
    channelsLoading.value = false
  }
}

// WebSocket 订阅 — 纯数据流模型
const setupWebSocket = () => {
  if (!wsStore.connected) {
    wsStore.connect()
  }

  unsubscribeChannelData = wsStore.subscribe(WS_EVENT.CHANNEL_DATA, (message: WebSocketMessage) => {
    const payload = message.payload as any
    if (!payload) return

    // 只处理当前选中通道的数据
    if (!selectedChannelId.value || payload.channel_id !== selectedChannelId.value) return

    const data = payload.raw_hex
    const source: 'interactive' | 'scheduled' = payload.request_id ? 'interactive' : 'scheduled'

    if (data) {
      addLog({
        type: 'recv',
        direction: 'RX',
        time: now(),
        data: data,
        source: source,
        channelId: payload.channel_id,
        errorCode: payload.error_code,
      })
    } else if (payload.request_id) {
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
  })

  // 订阅 channel_write_error
  const unsubWriteError = wsStore.subscribe(WS_EVENT.CHANNEL_WRITE_ERROR, (message: WebSocketMessage) => {
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
  border-bottom: 1px solid var(--el-border-color-lighter);
  font-size: 12px;
  font-weight: 500;
}

.tx-panel .panel-header {
  background: var(--el-color-primary-light-9, #ecf5ff);
  color: var(--el-color-primary);
}

.rx-panel .panel-header {
  background: var(--el-color-success-light-9, #f0f9eb);
  color: var(--el-color-success);
}

.panel-paused .panel-header {
  animation: pause-blink 1.5s ease-in-out infinite;
}

@keyframes pause-blink {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.5; }
}

.panel-label {
  color: inherit;
}

.panel-count {
  font-size: 11px;
  opacity: 0.7;
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

.panel-entry.entry-expandable {
  cursor: pointer;
}

.panel-entry.modbus-parse {
  background: var(--el-fill-color-light);
  padding: 2px 8px 2px 30px;
  font-size: 11px;
  color: var(--el-text-color-secondary);
  min-height: 20px;
}

.parse-detail {
  font-family: 'JetBrains Mono', 'Fira Code', 'Consolas', monospace;
  font-size: 11px;
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

.read-size-hint {
  font-size: 11px;
  color: var(--el-text-color-secondary);
  white-space: nowrap;
}

.entry-time {
  color: var(--el-text-color-secondary);
  font-size: 11px;
  flex-shrink: 0;
}

.entry-length {
  color: var(--el-text-color-secondary);
  font-size: 10px;
  font-family: 'JetBrains Mono', 'Fira Code', 'Consolas', monospace;
  flex-shrink: 0;
  opacity: 0.7;
}

.entry-data {
  word-break: break-all;
}

.entry-ellipsis {
  color: var(--el-text-color-secondary);
  font-size: 11px;
  font-style: italic;
  flex-shrink: 0;
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

.input-mode-toggle {
  flex-shrink: 0;
}

.hex-input :deep(.el-input__inner),
.ascii-input :deep(.el-input__inner) {
  font-family: 'JetBrains Mono', 'Fira Code', 'Consolas', monospace;
  letter-spacing: 0.5px;
}

.tx-prefix {
  color: var(--el-text-color-secondary);
  font-size: 12px;
  font-family: 'JetBrains Mono', 'Fira Code', 'Consolas', monospace;
}

.baud-tag {
  cursor: default;
}

.baud-edit-icon {
  cursor: pointer;
  margin-left: 4px;
  font-size: 12px;
  color: var(--el-color-primary);
}

.baud-edit-icon:hover {
  color: var(--el-color-primary-dark-2);
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
