<template>
  <div class="log-panel">
    <!-- 控制栏 -->
    <div class="log-controls">
      <div class="control-group">
        <span class="control-label">日志流</span>
        <el-switch v-model="streamEnabled" @change="onStreamToggle" :loading="streamLoading" />
      </div>
      <div class="control-group">
        <span class="control-label">级别</span>
        <el-select v-model="logLevel" @change="onLevelChange" :disabled="!streamEnabled" size="small" style="width: 120px;">
          <el-option label="ERROR" :value="0" />
          <el-option label="WARN" :value="1" />
          <el-option label="INFO" :value="2" />
          <el-option label="DEBUG" :value="3" />
          <el-option label="VERBOSE" :value="4" />
        </el-select>
      </div>
      <div class="control-group">
        <span class="control-label">持久化</span>
        <el-switch v-model="persistEnabled" @change="onPersistToggle" :loading="persistLoading" />
      </div>
    </div>

    <!-- 联动提示 -->
    <el-alert v-if="persistEnabled && !streamEnabled" type="warning" :closable="false" style="margin-bottom: 12px;">
      持久化已开启但日志流未开启，ESP32 不会产生日志。请同时开启日志流。
    </el-alert>

    <!-- 关闭状态 -->
    <el-empty v-if="!streamEnabled && !persistEnabled" description="日志功能已关闭，点击开关启用" />

    <!-- 实时日志区 -->
    <div v-if="streamEnabled" class="log-realtime">
      <div class="log-toolbar">
        <span class="toolbar-title">实时日志</span>
        <el-button size="small" @click="paused = !paused">
          <el-icon><VideoPause v-if="!paused" /><VideoPlay v-else /></el-icon>
          {{ paused ? '继续' : '暂停' }}
        </el-button>
        <el-button size="small" @click="realtimeLogs = []">清屏</el-button>
        <el-input v-model="searchKeyword" size="small" placeholder="搜索..." style="width: 180px;" clearable />
      </div>
      <div class="log-terminal" ref="terminalRef">
        <div
          v-for="(line, idx) in filteredRealtimeLogs"
          :key="idx"
          class="log-line"
          :class="levelClass(line.level)"
        >
          <span class="log-time">{{ formatTime(line.ts) }}</span>
          <span class="log-level">{{ levelText(line.level) }}</span>
          <span class="log-tag">{{ line.tag }}</span>
          <span class="log-msg">{{ line.msg }}</span>
        </div>
        <div v-if="filteredRealtimeLogs.length === 0" class="log-empty">等待日志...</div>
      </div>
    </div>

    <!-- 历史查询区 -->
    <div v-if="persistEnabled" class="log-history" style="margin-top: 16px;">
      <div class="log-toolbar">
        <span class="toolbar-title">历史日志</span>
        <el-button size="small" @click="queryLogs" :loading="queryLoading">查询</el-button>
        <el-button size="small" @click="deleteAllLogs" type="danger" plain>全部清空</el-button>
      </div>
      <div class="query-filters">
        <el-select v-model="queryLevel" size="small" placeholder="级别" clearable style="width: 100px;">
          <el-option label="ERROR" :value="0" />
          <el-option label="WARN" :value="1" />
          <el-option label="INFO" :value="2" />
          <el-option label="DEBUG" :value="3" />
          <el-option label="VERBOSE" :value="4" />
        </el-select>
        <el-input v-model="queryTag" size="small" placeholder="Tag" style="width: 120px;" clearable />
        <el-input v-model="queryKeyword" size="small" placeholder="关键词" style="width: 180px;" clearable />
      </div>
      <el-table v-if="historyLogs.length > 0" :data="historyLogs" stripe size="small" style="margin-top: 8px;">
        <el-table-column label="时间" width="160">
          <template #default="{ row }">{{ formatTime(row.ts) }}</template>
        </el-table-column>
        <el-table-column label="级别" width="80">
          <template #default="{ row }">
            <el-tag :type="levelTagType(row.level)" size="small">{{ levelText(row.level) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="tag" label="Tag" width="120" />
        <el-table-column prop="message" label="消息" show-overflow-tooltip />
      </el-table>
      <el-empty v-else description="暂无历史日志" />
      <el-pagination
        v-if="historyTotal > querySize"
        v-model:current-page="queryPage"
        :page-size="querySize"
        :total="historyTotal"
        layout="prev, pager, next"
        @current-change="queryLogs"
        style="margin-top: 8px;"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch, nextTick } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { VideoPause, VideoPlay } from '@element-plus/icons-vue'
import { nodeApi } from '@/api/node'
import { WS_EVENT } from '@/events/events'
import { useWebSocketStore, type WebSocketMessage } from '@/stores/websocket'

interface Props {
  collectorId: string | number
  nodeDeviceId?: string
}

const props = defineProps<Props>()
const wsStore = useWebSocketStore()

// Config state
const streamEnabled = ref(false)
const persistEnabled = ref(false)
const logLevel = ref(2)
const streamLoading = ref(false)
const persistLoading = ref(false)

// Realtime state
const realtimeLogs = ref<any[]>([])
const paused = ref(false)
const searchKeyword = ref('')
const terminalRef = ref<HTMLElement>()

// History state
const historyLogs = ref<any[]>([])
const historyTotal = ref(0)
const queryPage = ref(1)
const querySize = ref(100)
const queryLevel = ref<number | undefined>(undefined)
const queryTag = ref('')
const queryKeyword = ref('')
const queryLoading = ref(false)

const MAX_REALTIME_LINES = 500

const filteredRealtimeLogs = computed(() => {
  if (!searchKeyword.value) return realtimeLogs.value
  const kw = searchKeyword.value.toLowerCase()
  return realtimeLogs.value.filter(l =>
    l.tag?.toLowerCase().includes(kw) || l.msg?.toLowerCase().includes(kw)
  )
})

// WebSocket
let unsubWs: (() => void) | null = null

function onWsMessage(msg: WebSocketMessage) {
  const data = (msg as any).data || msg
  if (data.node_id !== props.nodeDeviceId) return
  if (paused.value) return

  const lines = data.lines || []
  for (const line of lines) {
    realtimeLogs.value.push(line)
  }
  // Trim
  if (realtimeLogs.value.length > MAX_REALTIME_LINES) {
    realtimeLogs.value = realtimeLogs.value.slice(-MAX_REALTIME_LINES)
  }
  // Auto scroll
  nextTick(() => {
    if (terminalRef.value) {
      terminalRef.value.scrollTop = terminalRef.value.scrollHeight
    }
  })
}

// Load config
async function loadConfig() {
  try {
    const cfg = await nodeApi.getLogConfig(props.collectorId)
    streamEnabled.value = cfg.stream_enabled
    persistEnabled.value = cfg.persist_enabled
    logLevel.value = cfg.level
  } catch (e: any) {
    // Config not yet loaded — silent
  }
}

async function onStreamToggle(val: boolean) {
  streamLoading.value = true
  try {
    await nodeApi.updateLogConfig(props.collectorId, { stream_enabled: val })
    ElMessage.success(val ? '日志流已开启' : '日志流已关闭')
  } catch (e: any) {
    streamEnabled.value = !val
    ElMessage.error('操作失败: ' + (e.message || ''))
  } finally {
    streamLoading.value = false
  }
}

async function onLevelChange(val: number) {
  try {
    await nodeApi.updateLogConfig(props.collectorId, { level: val })
    ElMessage.success('日志级别已更新')
  } catch (e: any) {
    ElMessage.error('操作失败: ' + (e.message || ''))
  }
}

async function onPersistToggle(val: boolean) {
  persistLoading.value = true
  try {
    await nodeApi.updateLogPersist(props.collectorId, val)
    ElMessage.success(val ? '持久化已开启' : '持久化已关闭')
    if (val) {
      await queryLogs()
    }
  } catch (e: any) {
    persistEnabled.value = !val
    ElMessage.error('操作失败: ' + (e.message || ''))
  } finally {
    persistLoading.value = false
  }
}

async function queryLogs() {
  queryLoading.value = true
  try {
    const result = await nodeApi.getNodeLogs(props.collectorId, {
      page: queryPage.value,
      size: querySize.value,
      level: queryLevel.value,
      tag: queryTag.value || undefined,
      q: queryKeyword.value || undefined,
    })
    historyLogs.value = result.logs || []
    historyTotal.value = result.total || 0
  } catch (e: any) {
    ElMessage.error('查询失败: ' + (e.message || ''))
  } finally {
    queryLoading.value = false
  }
}

async function deleteAllLogs() {
  try {
    await ElMessageBox.confirm('确认删除该节点所有历史日志？', '警告', { type: 'warning' })
    const result = await nodeApi.deleteNodeLogs(props.collectorId)
    ElMessage.success(`已删除 ${result.deleted} 条日志`)
    historyLogs.value = []
    historyTotal.value = 0
  } catch (e: any) {
    if (e !== 'cancel') ElMessage.error('删除失败: ' + (e.message || ''))
  }
}

// Helpers
function levelText(level: number): string {
  return ['ERROR', 'WARN', 'INFO', 'DEBUG', 'VERBOSE'][level] || 'UNKNOWN'
}

function levelClass(level: number): string {
  return ['log-error', 'log-warn', 'log-info', 'log-debug', 'log-verbose'][level] || 'log-info'
}

function levelTagType(level: number): string {
  return ['danger', 'warning', '', 'info', 'info'][level] || ''
}

function formatTime(ts: number): string {
  // ESP32 ts is in microseconds
  const ms = Math.floor(ts / 1000)
  const d = new Date(ms)
  return d.toLocaleTimeString('zh-CN', { hour12: false }) + '.' + String(d.getMilliseconds()).padStart(3, '0')
}

onMounted(() => {
  loadConfig()
  unsubWs = wsStore.subscribe(WS_EVENT.NODE_LOG, onWsMessage)
})

onUnmounted(() => {
  if (unsubWs) {
    unsubWs()
    unsubWs = null
  }
})
</script>

<style scoped>
.log-panel {
  padding: 0;
}

.log-controls {
  display: flex;
  align-items: center;
  gap: 24px;
  margin-bottom: 16px;
}

.control-group {
  display: flex;
  align-items: center;
  gap: 8px;
}

.control-label {
  font-size: 14px;
  font-weight: 500;
  color: var(--el-text-color-primary);
}

.log-toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 8px;
}

.toolbar-title {
  font-size: 14px;
  font-weight: 600;
  margin-right: auto;
}

.log-terminal {
  background: #1e1e1e;
  border-radius: 8px;
  padding: 12px;
  max-height: 400px;
  overflow-y: auto;
  font-family: 'JetBrains Mono', 'Cascadia Code', 'Courier New', Consolas, monospace;
  font-size: 12px;
  line-height: 1.6;
}

.log-line {
  display: flex;
  gap: 8px;
  white-space: nowrap;
}

.log-time {
  color: #888;
  flex-shrink: 0;
}

.log-level {
  font-weight: 600;
  flex-shrink: 0;
  width: 60px;
}

.log-tag {
  color: #569cd6;
  flex-shrink: 0;
  min-width: 80px;
}

.log-msg {
  color: #d4d4d4;
  overflow: hidden;
  text-overflow: ellipsis;
}

.log-error .log-level { color: #f48771; }
.log-error .log-msg { color: #f48771; }
.log-warn .log-level { color: #cca700; }
.log-warn .log-msg { color: #cca700; }
.log-info .log-level { color: #4ec9b0; }
.log-debug .log-level { color: #888; }
.log-debug .log-msg { color: #aaa; }
.log-verbose .log-level { color: #666; }
.log-verbose .log-msg { color: #888; }

.log-empty {
  color: #666;
  text-align: center;
  padding: 20px;
}

.query-filters {
  display: flex;
  gap: 8px;
  margin-bottom: 8px;
}

.log-history {
  border-top: 1px solid var(--el-border-color-light);
  padding-top: 12px;
}
</style>
