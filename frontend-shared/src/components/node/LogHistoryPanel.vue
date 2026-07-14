<template>
  <section class="log-history-panel" aria-label="历史日志">
    <div class="history-toolbar">
      <strong class="toolbar-title">历史日志</strong>
      <el-button
        size="small"
        aria-label="查询历史日志"
        :loading="loading"
        @click="applyFilters"
      >
        查询
      </el-button>
      <el-button
        size="small"
        aria-label="导出历史日志"
        :disabled="logs.length === 0"
        @click="exportCurrentPage"
      >
        导出当前结果
      </el-button>
      <el-button
        size="small"
        type="danger"
        plain
        aria-label="清理全部历史日志"
        @click="clearAll"
      >
        全部清理
      </el-button>
    </div>

    <div class="query-filters">
      <el-date-picker
        v-model="draftRange"
        type="datetimerange"
        value-format="x"
        range-separator="至"
        start-placeholder="开始时间"
        end-placeholder="结束时间"
        aria-label="历史日志时间范围"
      />
      <el-select
        v-model="draftLevel"
        size="small"
        clearable
        placeholder="级别"
        aria-label="历史日志级别"
      >
        <el-option label="ERROR" :value="0" />
        <el-option label="WARN" :value="1" />
        <el-option label="INFO" :value="2" />
        <el-option label="DEBUG" :value="3" />
        <el-option label="VERBOSE" :value="4" />
      </el-select>
      <el-input
        v-model="draftTag"
        size="small"
        clearable
        placeholder="Tag"
        aria-label="历史日志 Tag"
        @keyup.enter="applyFilters"
      />
      <el-input
        v-model="draftKeyword"
        size="small"
        clearable
        placeholder="关键词"
        aria-label="历史日志关键词"
        @keyup.enter="applyFilters"
      />
    </div>

    <div class="cleanup-controls">
      <el-date-picker
        v-model="cleanupBefore"
        type="datetime"
        value-format="x"
        placeholder="清理此时间前日志"
        aria-label="清理时间点"
      />
      <el-button
        size="small"
        type="danger"
        plain
        aria-label="清理指定时间前日志"
        :disabled="cleanupBefore == null"
        @click="clearBefore"
      >
        清理指定时间前日志
      </el-button>
    </div>

    <el-table v-if="logs.length > 0" :data="logs" stripe size="small" class="history-table">
      <el-table-column label="时间" min-width="180">
        <template #default="{ row }">{{ formatHistoryTime(row.created_at) }}</template>
      </el-table-column>
      <el-table-column label="级别" width="92">
        <template #default="{ row }">
          <el-tag :type="levelTagType(row.level)" size="small">{{ levelText(row.level) }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="tag" label="Tag" min-width="120" />
      <el-table-column prop="message" label="消息" min-width="280" show-overflow-tooltip />
    </el-table>
    <el-empty v-else description="暂无历史日志" />

    <el-pagination
      v-if="total > pageSize"
      v-model:current-page="page"
      :page-size="pageSize"
      :total="total"
      layout="prev, pager, next"
      aria-label="历史日志分页"
      @current-change="loadLogs"
    />
  </section>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { nodeApi, type NodeLogEntry, type NodeLogQuery } from '@/api/node'
import { exportCSV } from '@/utils/exportData'
import { levelText, levelTagType } from '@/components/node/logTypes'

interface Props {
  collectorId: string | number
}

const props = defineProps<Props>()

const logs = ref<NodeLogEntry[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(100)
const loading = ref(false)

const draftRange = ref<[number, number] | undefined>()
const draftLevel = ref<number | undefined>()
const draftTag = ref('')
const draftKeyword = ref('')
const cleanupBefore = ref<number | null>()
const activeFilters = ref<Omit<NodeLogQuery, 'page' | 'size'>>({})
let requestGeneration = 0

function currentQuery(): NodeLogQuery {
  return {
    ...activeFilters.value,
    page: page.value,
    size: pageSize.value,
  }
}

function applyFilters() {
  activeFilters.value = {
    ...(draftRange.value
      ? { from: draftRange.value[0], to: draftRange.value[1] }
      : {}),
    ...(draftLevel.value !== undefined ? { level: draftLevel.value } : {}),
    ...(draftTag.value.trim() ? { tag: draftTag.value.trim() } : {}),
    ...(draftKeyword.value.trim() ? { q: draftKeyword.value.trim() } : {}),
  }
  page.value = 1
  void loadLogs()
}

async function loadLogs() {
  const generation = ++requestGeneration
  loading.value = true
  try {
    const result = await nodeApi.getNodeLogs(props.collectorId, currentQuery())
    if (generation !== requestGeneration) return
    logs.value = result.logs ?? []
    total.value = result.total ?? 0
  } catch (error: unknown) {
    if (generation === requestGeneration) {
      ElMessage.error(`查询失败: ${errorMessage(error)}`)
    }
  } finally {
    if (generation === requestGeneration) {
      loading.value = false
    }
  }
}

async function clearBefore() {
  if (cleanupBefore.value == null) {
    ElMessage.warning('请先选择清理时间点')
    return
  }
  try {
    await ElMessageBox.confirm(
      `确认清理指定时间前的历史日志？清理时间：${formatHistoryTime(cleanupBefore.value)}`,
      '清理历史日志',
      {
        type: 'warning',
        confirmButtonText: '确认清理',
        cancelButtonText: '取消',
      },
    )
  } catch {
    return
  }

  try {
    const result = await nodeApi.deleteNodeLogs(props.collectorId, cleanupBefore.value)
    ElMessage.success(`已删除 ${result.deleted} 条日志`)
    page.value = 1
    await loadLogs()
  } catch (error: unknown) {
    ElMessage.error(`删除失败: ${errorMessage(error)}`)
  }
}

async function clearAll() {
  try {
    await ElMessageBox.confirm(
      '确认清理该节点全部历史日志？此操作不可恢复。',
      '清理全部历史日志',
      {
        type: 'warning',
        confirmButtonText: '全部清理',
        cancelButtonText: '取消',
      },
    )
  } catch {
    return
  }

  try {
    const result = await nodeApi.deleteNodeLogs(props.collectorId)
    ElMessage.success(`已删除 ${result.deleted} 条日志`)
    page.value = 1
    await loadLogs()
  } catch (error: unknown) {
    ElMessage.error(`删除失败: ${errorMessage(error)}`)
  }
}

function exportCurrentPage() {
  if (logs.value.length === 0) {
    ElMessage.warning('当前查询结果为空')
    return
  }
  exportCSV(
    `node-logs-${props.collectorId}`,
    ['时间', '级别', 'Tag', '消息'],
    logs.value.map((entry) => ({
      时间: entry.created_at,
      级别: levelText(entry.level),
      Tag: entry.tag,
      消息: entry.message,
    })),
  )
}

function formatHistoryTime(value: string | number): string {
  const date = new Date(value)
  return Number.isNaN(date.getTime())
    ? '-'
    : date.toLocaleString('zh-CN', { hour12: false })
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : '未知错误'
}

onMounted(loadLogs)
</script>

<style scoped>
.log-history-panel {
  display: grid;
  gap: 12px;
  min-width: 0;
}

.history-toolbar,
.query-filters,
.cleanup-controls {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}

.toolbar-title {
  margin-right: auto;
  font-size: 14px;
}

.query-filters :deep(.el-date-editor) {
  width: 360px;
}

.query-filters :deep(.el-select) {
  width: 120px;
}

.query-filters :deep(.el-input) {
  width: 180px;
}

.cleanup-controls {
  padding: 10px;
  border: 1px solid var(--el-border-color-light);
  border-radius: var(--radius-md, 6px);
  background: var(--el-fill-color-lighter);
}

.history-table {
  width: 100%;
}

@media (max-width: 768px) {
  .history-toolbar > *,
  .query-filters > *,
  .cleanup-controls > * {
    flex: 1 1 100%;
  }

  .query-filters :deep(.el-date-editor),
  .query-filters :deep(.el-select),
  .query-filters :deep(.el-input),
  .cleanup-controls :deep(.el-date-editor) {
    width: 100%;
  }
}
</style>
