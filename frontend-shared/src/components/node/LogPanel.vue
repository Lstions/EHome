<template>
  <div class="log-panel">
    <div class="log-controls">
      <div class="control-group">
        <span class="control-label">日志流</span>
        <el-switch
          v-model="streamEnabled"
          aria-label="日志流开关"
          :loading="streamLoading"
          @change="onStreamToggle"
        />
      </div>
      <div class="control-group">
        <span class="control-label">级别</span>
        <el-select
          v-model="logLevel"
          aria-label="日志级别"
          :disabled="!streamEnabled"
          size="small"
          class="level-select"
          @change="onLevelChange"
        >
          <el-option v-for="opt in LOG_LEVEL_OPTIONS" :key="opt.value" :label="opt.label" :value="opt.value" />
        </el-select>
      </div>
      <div class="control-group">
        <span class="control-label">持久化</span>
        <el-switch
          v-model="persistEnabled"
          aria-label="日志持久化开关"
          :loading="persistLoading"
          @change="onPersistToggle"
        />
      </div>
    </div>

    <el-alert
      v-if="persistEnabled && !streamEnabled"
      type="warning"
      :closable="false"
      class="stream-warning"
    >
      持久化已开启但日志流未开启，ESP32 不会产生日志。请同时开启日志流。
    </el-alert>

    <div class="realtime-section">
      <LogRealtimeViewer
        :logs="realtimeLogs"
        :received-count="realtimeReceivedCount"
        :generation="realtimeGeneration"
        :paused="paused"
        :search-keyword="searchKeyword"
        :search-count-state="realtimeSearchCountState"
        @update:paused="paused = $event"
        @update:search-keyword="searchKeyword = $event"
        @clear="clearRealtimeLogs"
        @export="exportRealtimeLogs"
      />
    </div>

    <div class="history-section">
      <LogHistoryPanel :collector-id="collectorId" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { nodeApi } from '@/api/node'
import LogHistoryPanel from '@/components/node/LogHistoryPanel.vue'
import LogRealtimeViewer from '@/components/node/LogRealtimeViewer.vue'
import { WS_EVENT } from '@/events/events'
import { useWebSocketStore, type WebSocketMessage } from '@/stores/websocket'
import { downloadText, exportCSV } from '@/utils/exportData'
import { logger } from '@/utils/logger'
import { assertSessionGeneration, getSessionGeneration } from '@/utils/sessionCache'
import {
  levelText,
  formatUptime,
  errorMessage,
  LOG_LEVEL_OPTIONS,
  type IncomingRealtimeLogLine,
  type RealtimeLogLine,
  type RealtimeSearchCountState,
} from '@/components/node/logTypes'

interface Props {
  collectorId: string | number
  nodeDeviceId?: string
}

const props = defineProps<Props>()
const wsStore = useWebSocketStore()

const streamEnabled = ref(false)
const persistEnabled = ref(false)
const logLevel = ref(2)
const streamLoading = ref(false)
const persistLoading = ref(false)

const realtimeLogs = ref<RealtimeLogLine[]>([])
const realtimeReceivedCount = ref(0)
const realtimeGeneration = ref(0)
const paused = ref(false)
const searchKeyword = ref('')
const realtimeSearchCountState = ref<RealtimeSearchCountState>({
  epoch: 0,
  baselineId: 0,
  baselineMatchIds: [],
  matchedAfterBaseline: 0,
})

const MAX_REALTIME_LINES = 5000
let nextRealtimeLogId = 1
let unsubWs: (() => void) | null = null
let operationGeneration = 0

const filteredRealtimeLogs = computed(() => {
  const keyword = normalizedSearchKeyword(searchKeyword.value)
  if (!keyword) return realtimeLogs.value
  return realtimeLogs.value.filter(line => realtimeLogMatches(line, keyword))
})

function normalizedSearchKeyword(keyword: string): string {
  return keyword.trim().toLocaleLowerCase()
}

function realtimeLogMatches(line: RealtimeLogLine, keyword: string): boolean {
  return String(line.tag ?? '').toLocaleLowerCase().includes(keyword)
    || String(line.msg ?? '').toLocaleLowerCase().includes(keyword)
    || levelText(line.level).toLocaleLowerCase().includes(keyword)
}

function onWsMessage(message: WebSocketMessage): void {
  const envelope = message as WebSocketMessage & {
    payload?: { node_id?: string; lines?: IncomingRealtimeLogLine[] }
    data?: { node_id?: string; lines?: IncomingRealtimeLogLine[] }
  }
  const data = envelope.payload ?? envelope.data ?? envelope
  if (data.node_id !== props.nodeDeviceId || !Array.isArray(data.lines)) return

  const appendedLogs = data.lines.map(line => ({
    id: nextRealtimeLogId++,
    ts: Number(line.ts ?? 0),
    level: Number(line.level ?? 0),
    tag: String(line.tag ?? ''),
    msg: String(line.msg ?? ''),
  }))
  if (appendedLogs.length === 0) return

  const keyword = normalizedSearchKeyword(searchKeyword.value)
  if (keyword) {
    const matchedInBatch = appendedLogs.reduce(
      (count, line) => count + (realtimeLogMatches(line, keyword) ? 1 : 0),
      0,
    )
    if (matchedInBatch > 0) {
      realtimeSearchCountState.value = {
        ...realtimeSearchCountState.value,
        matchedAfterBaseline: realtimeSearchCountState.value.matchedAfterBaseline + matchedInBatch,
      }
    }
  }

  realtimeReceivedCount.value += appendedLogs.length
  realtimeLogs.value = [...realtimeLogs.value, ...appendedLogs].slice(-MAX_REALTIME_LINES)
}

async function loadConfig(): Promise<void> {
  const collectorId = props.collectorId
  try {
    const config = await nodeApi.getLogConfig(collectorId)
    if (props.collectorId !== collectorId) return
    streamEnabled.value = config.stream_enabled
    persistEnabled.value = config.persist_enabled
    logLevel.value = config.level
  } catch (error: unknown) {
    if (props.collectorId !== collectorId) return
    logger.warn('加载节点日志配置失败', {
      collectorId: String(props.collectorId),
      error: errorMessage(error),
    })
    ElMessage.warning('日志配置加载失败，仍可查看实时与历史日志')
  }
}

async function onStreamToggle(value: boolean): Promise<void> {
  const collectorId = props.collectorId
  const operation = operationGeneration
  const sessionGeneration = getSessionGeneration()
  streamLoading.value = true
  try {
    await nodeApi.updateLogConfig(collectorId, { stream_enabled: value })
    assertSessionGeneration(sessionGeneration)
    if (operation !== operationGeneration || props.collectorId !== collectorId) return
    ElMessage.success(value ? '日志流已开启' : '日志流已关闭')
  } catch (error: unknown) {
    if (operation !== operationGeneration || props.collectorId !== collectorId) return
    streamEnabled.value = !value
    ElMessage.error(`操作失败: ${errorMessage(error)}`)
  } finally {
    if (operation === operationGeneration && props.collectorId === collectorId) streamLoading.value = false
  }
}

async function onLevelChange(value: number): Promise<void> {
  const collectorId = props.collectorId
  const operation = operationGeneration
  const sessionGeneration = getSessionGeneration()
  try {
    await nodeApi.updateLogConfig(collectorId, { level: value })
    assertSessionGeneration(sessionGeneration)
    if (operation !== operationGeneration || props.collectorId !== collectorId) return
    ElMessage.success('日志级别已更新')
  } catch (error: unknown) {
    if (operation !== operationGeneration || props.collectorId !== collectorId) return
    ElMessage.error(`操作失败: ${errorMessage(error)}`)
  }
}

async function onPersistToggle(value: boolean): Promise<void> {
  const collectorId = props.collectorId
  const operation = operationGeneration
  const sessionGeneration = getSessionGeneration()
  persistLoading.value = true
  try {
    await nodeApi.updateLogPersist(collectorId, value)
    assertSessionGeneration(sessionGeneration)
    if (operation !== operationGeneration || props.collectorId !== collectorId) return
    ElMessage.success(value ? '持久化已开启' : '持久化已关闭')
  } catch (error: unknown) {
    if (operation !== operationGeneration || props.collectorId !== collectorId) return
    persistEnabled.value = !value
    ElMessage.error(`操作失败: ${errorMessage(error)}`)
  } finally {
    if (operation === operationGeneration && props.collectorId === collectorId) persistLoading.value = false
  }
}

function clearRealtimeLogs(): void {
  realtimeGeneration.value += 1
  realtimeLogs.value = []
  if (normalizedSearchKeyword(searchKeyword.value)) {
    realtimeSearchCountState.value = {
      epoch: realtimeSearchCountState.value.epoch + 1,
      baselineId: realtimeReceivedCount.value,
      baselineMatchIds: [],
      matchedAfterBaseline: 0,
    }
  }
}

function exportRealtimeLogs(format: 'text' | 'csv'): void {
  const logs = filteredRealtimeLogs.value
  if (logs.length === 0) {
    ElMessage.error('没有可导出的实时日志')
    return
  }

  const filename = `realtime-logs-${props.collectorId}`
  if (format === 'csv') {
    exportCSV(
      filename,
      ['运行时间', '级别', 'Tag', '消息'],
      logs.map(line => ({
        运行时间: formatUptime(line.ts),
        级别: levelText(line.level),
        Tag: line.tag,
        消息: line.msg,
      })),
    )
    return
  }

  const content = logs
    .map(line => `${formatUptime(line.ts)} ${levelText(line.level)} ${line.tag} ${line.msg}`)
    .join('\n')
  downloadText(content, `${filename}.txt`)
}

watch(
  () => normalizedSearchKeyword(searchKeyword.value),
  (keyword, previousKeyword) => {
    if (keyword === previousKeyword) return

    realtimeSearchCountState.value = {
      epoch: realtimeSearchCountState.value.epoch + 1,
      baselineId: realtimeReceivedCount.value,
      baselineMatchIds: keyword
        ? realtimeLogs.value.filter(line => realtimeLogMatches(line, keyword)).map(line => line.id)
        : [],
      matchedAfterBaseline: 0,
    }
  },
  { flush: 'sync' },
)

onMounted(() => {
  void loadConfig()
  unsubWs = wsStore.subscribe(WS_EVENT.NODE_LOG, onWsMessage)
})

watch(() => [props.collectorId, props.nodeDeviceId] as const, ([newCollector, newDevice], [oldCollector, oldDevice]) => {
  if (newCollector === oldCollector && newDevice === oldDevice) return
  operationGeneration++
  streamLoading.value = false
  persistLoading.value = false
  clearRealtimeLogs()
  realtimeReceivedCount.value = 0
  streamEnabled.value = false
  persistEnabled.value = false
  logLevel.value = 2
  void loadConfig()
})

onUnmounted(() => {
  operationGeneration++
  streamLoading.value = false
  persistLoading.value = false
  unsubWs?.()
  unsubWs = null
})
</script>

<style scoped>
.log-panel {
  display: grid;
  gap: 16px;
  min-width: 0;
}

.log-controls,
.control-group {
  display: flex;
  align-items: center;
}

.log-controls {
  flex-wrap: wrap;
  gap: 24px;
}

.control-group {
  gap: 8px;
}

.control-label {
  font-size: 14px;
  font-weight: 600;
  color: var(--el-text-color-primary);
}

.level-select {
  width: 120px;
}

.stream-warning {
  margin: 0;
}

.realtime-section,
.history-section {
  min-width: 0;
}

.history-section {
  padding-top: 16px;
  border-top: 1px solid var(--el-border-color-light);
}

@media (max-width: 768px) {
  .log-controls {
    align-items: stretch;
  }

  .log-controls {
    gap: 12px;
  }

  .control-group {
    flex: 1 1 140px;
    justify-content: space-between;
  }
}
</style>
