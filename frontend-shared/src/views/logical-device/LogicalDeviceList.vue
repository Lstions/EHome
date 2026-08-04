<template>
  <div class="logical-device-page">
    <!-- 工具栏 -->
    <el-card class="toolbar-card" shadow="hover">
      <div class="filter-bar">
        <div class="filter-left">
          <el-input
            v-model="searchKeyword"
            placeholder="搜索逻辑设备名称..."
            prefix-icon="Search"
            clearable
            class="search-input"
            aria-label="搜索逻辑设备"
          />
          <el-select v-model="typeFilter" placeholder="设备类型" clearable class="filter-select" aria-label="按设备类型筛选">
            <el-option v-for="t in deviceTypeOptions" :key="t.value" :label="t.label" :value="t.value" />
          </el-select>
        </div>
        <div class="filter-right">
          <el-button
            type="primary"
            :disabled="!canMerge"
            :title="mergeDisabledHint"
            @click="openPreview"
          >
            <el-icon><Connection /></el-icon>
            合并所选（{{ selection.length }}）
          </el-button>
          <el-button :loading="loading" @click="fetchList">
            <el-icon><Refresh /></el-icon>
            刷新
          </el-button>
        </div>
      </div>
      <div class="merge-hint" v-if="selection.length > 0 && !canMerge">
        <el-icon><InfoFilled /></el-icon>
        <span>{{ mergeDisabledHint }}</span>
      </div>
    </el-card>

    <!-- 列表 (instance_count 含已删实例, §1.1 Unscoped 聚合) -->
    <div class="mobile-table-wrapper">
      <div class="mobile-table-hint">← 左右滑动查看完整表格 →</div>
      <el-table
        v-loading="loading"
        :data="filteredItems"
        @selection-change="handleSelectionChange"
        row-key="id"
        style="width: 100%"
      >
        <el-table-column type="selection" width="48" :selectable="isSelectable" />
        <el-table-column prop="name" label="名称" min-width="160">
          <template #default="{ row }">
            <span class="ld-name">{{ row.name }}</span>
            <el-tag v-if="row.merge_status === 'pending'" type="warning" size="small" class="ld-tag">合并中</el-tag>
            <el-tag v-else-if="row.merge_status === 'done'" type="info" size="small" class="ld-tag">已合并 → #{{ row.merged_into }}</el-tag>
            <el-tag v-if="row.purge_requested" type="danger" size="small" class="ld-tag">待清除数据</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="device_type" label="类型" width="120">
          <template #default="{ row }">{{ getDeviceTypeLabel(row.device_type) }}</template>
        </el-table-column>
        <el-table-column prop="instance_count" label="实例数（含已删）" width="140" align="center" />
        <el-table-column label="数据量估算" width="120" align="right">
          <template #default="{ row }">
            <span v-if="row.row_estimate != null">{{ formatRows(row.row_estimate) }}</span>
            <span v-else class="muted">—</span>
          </template>
        </el-table-column>
        <el-table-column label="最后数据时间" width="170">
          <template #default="{ row }">
            <span v-if="row.last_data_at">{{ formatTime(row.last_data_at) }}</span>
            <span v-else class="muted">无数据</span>
          </template>
        </el-table-column>
        <el-table-column label="保留天数" width="120" align="center">
          <template #default="{ row }">
            <el-tag size="small" effect="plain">{{ row.retention_days }} 天</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="100" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" size="small" @click="openEdit(row)">编辑</el-button>
          </template>
        </el-table-column>
        <template #empty>
          <div class="empty-state-enhanced">
            <el-empty description="暂无逻辑设备" :image-size="80">
              <template #description>
                <p class="empty-title">暂无逻辑设备</p>
                <p class="empty-desc">逻辑设备用于聚合边缘设备数据，实现统一视图和数据保留策略管理。您还没有边缘设备，请先创建边缘设备。</p>
              </template>
              <el-button type="primary" @click="goToEdgeDevice">
                <el-icon><Plus /></el-icon>
                去创建边缘设备
              </el-button>
            </el-empty>
          </div>
        </template>
      </el-table>
    </div>

    <!-- 合并预览弹窗 (§3.4: 时间轴对比/重叠提示/数据量合计/target_retention_days, 二次确认) -->
    <el-dialog
      v-model="previewVisible"
      title="合并预览"
      width="720px"
      align-center
      class="merge-preview-dialog"
    >
      <div v-loading="previewLoading">
        <div v-if="preview" class="preview-body">
          <el-alert
            v-if="hasOverlap"
            type="warning"
            :closable="false"
            show-icon
            title="时间范围存在重叠"
            description="重叠时间段内的同时间点数据将按保形去重保留 MAX(id) 行（非平均值），原始 device_id 血缘保留。"
            class="preview-alert"
          />
          <!-- 时间轴对比 -->
          <div class="timeline-section">
            <div class="timeline-header">
              <span>{{ formatTime(globalRange.min) }}</span>
              <span>{{ formatTime(globalRange.max) }}</span>
            </div>
            <div v-for="src in preview.sources" :key="src.id" class="timeline-row">
              <div class="timeline-label" :title="src.name">{{ src.name }}</div>
              <div class="timeline-track">
                <div
                  class="timeline-bar"
                  :class="{ overlap: src.overlap_with_others }"
                  :style="timelineBarStyle(src)"
                  :aria-label="`${src.name} 数据时间范围: ${src.first_data_at ? formatTime(src.first_data_at) : '无'} 至 ${src.last_data_at ? formatTime(src.last_data_at) : '无'}${src.overlap_with_others ? ', 与其他源存在重叠' : ''}`"
                  role="img"
                ></div>
              </div>
              <div class="timeline-meta">
                <span v-if="src.row_estimate != null">约 {{ formatRows(src.row_estimate) }} 行</span>
                <span v-else class="muted">估算不可用</span>
              </div>
            </div>
          </div>
          <!-- 每源明细 -->
          <el-table :data="preview.sources" size="small" class="preview-table">
            <el-table-column prop="name" label="源逻辑设备" min-width="140" />
            <el-table-column label="首条数据" width="160">
              <template #default="{ row }">{{ row.first_data_at ? formatTime(row.first_data_at) : '无数据' }}</template>
            </el-table-column>
            <el-table-column label="末条数据" width="160">
              <template #default="{ row }">{{ row.last_data_at ? formatTime(row.last_data_at) : '无数据' }}</template>
            </el-table-column>
            <el-table-column label="数据量" width="110" align="right">
              <template #default="{ row }">{{ row.row_estimate != null ? formatRows(row.row_estimate) : '—' }}</template>
            </el-table-column>
            <el-table-column label="重叠" width="70" align="center">
              <template #default="{ row }">
                <el-tag v-if="row.overlap_with_others" type="warning" size="small">重叠</el-tag>
                <span v-else class="muted">—</span>
              </template>
            </el-table-column>
          </el-table>
          <div class="preview-summary">
            <div class="summary-item">
              <span class="summary-label">数据量合计（估算）</span>
              <span class="summary-value">{{ totalRows != null ? `约 ${formatRows(totalRows)} 行` : '—' }}</span>
            </div>
            <div class="summary-item">
              <span class="summary-label">新目标保留天数</span>
              <span class="summary-value">{{ preview.target_retention_days }} 天</span>
              <span class="summary-note">合并后可在管理页调整</span>
            </div>
          </div>
          <el-form class="target-name-form" label-width="100px">
            <el-form-item label="目标名称" required>
              <el-input v-model="targetName" placeholder="合并后的逻辑设备名称" maxlength="128" data-testid="merge-target-name" />
            </el-form-item>
          </el-form>
        </div>
      </div>
      <template #footer>
        <el-button @click="previewVisible = false">取消</el-button>
        <el-button
          type="primary"
          :loading="merging"
          :disabled="!targetName.trim() || previewLoading"
          data-testid="merge-confirm"
          @click="confirmMerge"
        >
          确认合并（{{ selection.length }} 个源）
        </el-button>
      </template>
    </el-dialog>

    <!-- 409 冲突逐条呈现 (§3.4 D-1, 带实例跳转) -->
    <el-dialog v-model="conflictVisible" title="合并被拒绝" width="640px" align-center>
      <el-alert type="error" :closable="false" :title="conflictMessage" class="preview-alert" />
      <div v-for="(conflict, idx) in conflicts" :key="idx" class="conflict-item">
        <el-icon class="conflict-icon"><WarningFilled /></el-icon>
        <div class="conflict-body">
          <div class="conflict-title">
            逻辑设备「{{ conflict.logical_name }}」（#{{ conflict.logical_device_id }}）：{{ conflictReasonText(conflict.reason) }}
          </div>
          <div v-if="conflict.instance_id" class="conflict-detail">
            存活实例：{{ conflict.instance_name || `#${conflict.instance_id}` }}
            <span v-if="conflict.node_name">（节点：{{ conflict.node_name }}）</span>
            <el-button link type="primary" size="small" data-testid="conflict-jump" @click="jumpToInstance(conflict.instance_id!)">
              查看实例 →
            </el-button>
          </div>
        </div>
      </div>
      <template #footer>
        <el-button type="primary" @click="conflictVisible = false">知道了</el-button>
      </template>
    </el-dialog>

    <!-- 搬迁进度 (merge-jobs 轮询) -->
    <el-dialog v-model="progressVisible" title="数据搬迁进度" width="560px" align-center>
      <div v-for="job in jobs" :key="job.id" class="job-item">
        <div class="job-header">
          <span class="job-name">源 #{{ job.source_logical_id }} → 目标 #{{ job.target_logical_id }}</span>
          <el-tag :type="jobStatusType(job.status)" size="small">{{ jobStatusText(job.status) }}</el-tag>
        </div>
        <el-progress
          :percentage="jobPercent(job)"
          :status="job.status === 'done' ? 'success' : job.status === 'failed' ? 'exception' : undefined"
        />
        <div class="job-meta">
          已搬迁 {{ formatRows(job.migrated_rows) }} 行
          <span v-if="job.total_estimate > 0"> / 估算 {{ formatRows(job.total_estimate) }} 行</span>
          <span v-if="job.retry_count > 0" class="job-retry">（重试 {{ job.retry_count }} 次）</span>
        </div>
      </div>
      <template #footer>
        <el-button @click="closeProgress">关闭</el-button>
      </template>
    </el-dialog>

    <!-- 编辑 (name / retention_days; identity_key 只读 §1.1) -->
    <el-dialog v-model="editVisible" title="编辑逻辑设备" width="480px" align-center>
      <el-form v-if="editing" label-width="100px">
        <el-form-item label="名称">
          <el-input v-model="editForm.name" maxlength="128" data-testid="edit-name" />
        </el-form-item>
        <el-form-item label="保留天数">
          <el-input-number
            v-model="editForm.retention_days"
            :min="1"
            :max="3650"
            data-testid="edit-retention"
          />
          <span class="form-hint">到期后数据将被分批硬删除，删除后不可恢复</span>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="editVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" data-testid="edit-save" @click="saveEdit">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onBeforeUnmount, watch } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { ElMessage } from 'element-plus'
import { Connection, Refresh, InfoFilled, WarningFilled, Plus } from '@element-plus/icons-vue'
import { deviceTypeOptions, getDeviceTypeLabel } from '@/utils/deviceType'
import {
  logicalDeviceApi,
  extractMergeConflicts,
  type LogicalDeviceItem,
  type MergePreviewResponse,
  type MergePreviewSource,
  type MergeJob,
  type MergeConflict,
} from '@/api/logicalDevice'
import { useDebouncedSearch } from '@/composables/useDebouncedSearch'

const router = useRouter()
const route = useRoute()

// 空状态 CTA：跳转到边缘设备页面创建
const goToEdgeDevice = () => {
  router.push('/edge-device')
}

const loading = ref(false)
const items = ref<LogicalDeviceItem[]>([])

// 搜索 debounce — 减少不必要的 filter 计算
const {
  searchKeyword,
  filteredItems: searchFilteredItems,
} = useDebouncedSearch(items, {
  searchFields: (i) => [i.name || ''],
})

const typeFilter = ref('')
const selection = ref<LogicalDeviceItem[]>([])

// ─── 列表 ───
const fetchList = async () => {
  loading.value = true
  try {
    const res = await logicalDeviceApi.list()
    items.value = res.items
  } catch (error: any) {
    ElMessage.error('加载逻辑设备列表失败: ' + (error?.message || '未知错误'))
  } finally {
    loading.value = false
  }
}

const filteredItems = computed(() => {
  let list = searchFilteredItems.value
  if (typeFilter.value) list = list.filter(i => i.device_type === typeFilter.value)
  return list
})

// ─── 合并门控 (§3.4: 2+ 个同 device_type; 已合并/合并中/purge 的不可选) ───
const isSelectable = (row: LogicalDeviceItem) =>
  !row.merged_into && !row.merge_status && !row.purge_requested

const handleSelectionChange = (rows: LogicalDeviceItem[]) => {
  selection.value = rows
}

const canMerge = computed(() => {
  if (selection.value.length < 2) return false
  const firstType = selection.value[0].device_type
  return selection.value.every(s => s.device_type === firstType)
})

const mergeDisabledHint = computed(() => {
  if (selection.value.length === 0) return '勾选逻辑设备后可合并'
  if (selection.value.length === 1) return '至少选择 2 个逻辑设备'
  return '所选逻辑设备必须属于同一设备类型'
})

// ─── 预览 ───
const previewVisible = ref(false)
const previewLoading = ref(false)
const preview = ref<MergePreviewResponse | null>(null)
const targetName = ref('')
const merging = ref(false)

const hasOverlap = computed(() => !!preview.value?.sources.some(s => s.overlap_with_others))

const totalRows = computed(() => {
  if (!preview.value) return null
  let total = 0
  for (const s of preview.value.sources) {
    if (s.row_estimate == null) return null
    total += s.row_estimate
  }
  return total
})

const globalRange = computed(() => {
  const times: number[] = []
  for (const s of preview.value?.sources || []) {
    if (s.first_data_at) times.push(new Date(s.first_data_at).getTime())
    if (s.last_data_at) times.push(new Date(s.last_data_at).getTime())
  }
  if (times.length === 0) return { min: 0, max: 0 }
  return { min: Math.min(...times), max: Math.max(...times) }
})

// 时间轴条: 相对全局 [min,max] 定位; 无数据源不画条。
const timelineBarStyle = (src: MergePreviewSource) => {
  const { min, max } = globalRange.value
  if (!src.first_data_at || !src.last_data_at || max <= min) {
    return { left: '0%', width: '0%' }
  }
  const first = new Date(src.first_data_at).getTime()
  const last = new Date(src.last_data_at).getTime()
  const left = ((first - min) / (max - min)) * 100
  const width = Math.max(((last - first) / (max - min)) * 100, 1.5)
  return { left: `${left}%`, width: `${width}%` }
}

const openPreview = async () => {
  const ids = selection.value.map(s => s.id)
  targetName.value = selection.value.map(s => s.name).join(' + ')
  previewVisible.value = true
  previewLoading.value = true
  preview.value = null
  try {
    preview.value = await logicalDeviceApi.mergePreview(ids)
  } catch (error: any) {
    ElMessage.error('加载合并预览失败: ' + (error?.message || '未知错误'))
    previewVisible.value = false
  } finally {
    previewLoading.value = false
  }
}

// ─── 合并提交 + 409 conflicts ───
const conflictVisible = ref(false)
const conflictMessage = ref('')
const conflicts = ref<MergeConflict[]>([])

const conflictReasonText = (reason: string) => {
  switch (reason) {
    case 'alive_instance': return '仍有存活实例，请先删除该实例（历史数据保留）再发起合并'
    case 'purge_requested': return '已标记删除数据，不能参与合并'
    case 'already_merging': return '已在其他合并中或已合并'
    default: return reason
  }
}

const jumpToInstance = (instanceId: number) => {
  conflictVisible.value = false
  previewVisible.value = false
  router.push(`/edge-device/${instanceId}`)
}

const confirmMerge = async () => {
  if (!preview.value) return
  merging.value = true
  try {
    const result = await logicalDeviceApi.merge(targetName.value.trim(), selection.value.map(s => s.id))
    previewVisible.value = false
    ElMessage.success('合并已发起，后台正在搬迁数据')
    startProgressPolling(result.job_ids)
  } catch (error: any) {
    const extracted = extractMergeConflicts(error)
    if (extracted.length > 0 || error?.status === 409) {
      conflicts.value = extracted
      conflictMessage.value = error?.message || '合并校验未通过'
      conflictVisible.value = true
    } else {
      ElMessage.error('发起合并失败: ' + (error?.message || '未知错误'))
    }
  } finally {
    merging.value = false
  }
}

// ─── 进度轮询 (GET /logical-devices/merge-jobs/:id) ───
const progressVisible = ref(false)
const jobs = ref<MergeJob[]>([])
let pollTimer: ReturnType<typeof setInterval> | null = null

const jobPercent = (job: MergeJob) => {
  if (job.status === 'done') return 100
  if (job.total_estimate <= 0) return job.status === 'failed' ? 100 : 0
  return Math.min(Math.round((job.migrated_rows / job.total_estimate) * 100), 99)
}

const jobStatusType = (status: string) =>
  status === 'done' ? 'success' : status === 'failed' ? 'danger' : status === 'running' ? 'primary' : 'info'

const jobStatusText = (status: string) =>
  status === 'done' ? '完成' : status === 'failed' ? '失败' : status === 'running' ? '搬迁中' : '排队中'

const pollJobs = async () => {
  const settled: MergeJob[] = []
  for (const job of jobs.value) {
    if (job.status === 'done' || job.status === 'failed') {
      settled.push(job)
      continue
    }
    try {
      const fresh = await logicalDeviceApi.mergeJob(job.id)
      settled.push(fresh)
    } catch {
      settled.push(job)
    }
  }
  jobs.value = settled
  if (settled.every(j => j.status === 'done' || j.status === 'failed')) {
    stopPolling()
    const failed = settled.filter(j => j.status === 'failed').length
    if (failed > 0) {
      ElMessage.warning(`搬迁完成，其中 ${failed} 个任务失败（可查看通知）`)
    } else {
      ElMessage.success('数据搬迁全部完成')
    }
    await fetchList()
  }
}

const startProgressPolling = (jobIds: number[]) => {
  jobs.value = jobIds.map(id => ({
    id,
    source_logical_id: 0,
    target_logical_id: 0,
    status: 'pending' as const,
    migrated_rows: 0,
    total_estimate: 0,
    watermark_id: 0,
    watermark_phase: 'unified_data',
    retry_count: 0,
    created_at: '',
    updated_at: '',
    finished_at: null,
  }))
  progressVisible.value = true
  pollJobs()
  stopPolling()
  pollTimer = setInterval(pollJobs, 2000)
}

const stopPolling = () => {
  if (pollTimer) {
    clearInterval(pollTimer)
    pollTimer = null
  }
}

const closeProgress = () => {
  progressVisible.value = false
  stopPolling()
  fetchList()
}

onBeforeUnmount(stopPolling)

// ─── 编辑 ───
const editVisible = ref(false)
const saving = ref(false)
const editing = ref<LogicalDeviceItem | null>(null)
const editForm = ref({ name: '', retention_days: 365 })

const openEdit = (row: LogicalDeviceItem) => {
  editing.value = row
  editForm.value = { name: row.name, retention_days: row.retention_days }
  editVisible.value = true
}

const saveEdit = async () => {
  if (!editing.value) return
  if (!editForm.value.name.trim()) {
    ElMessage.warning('名称不能为空')
    return
  }
  saving.value = true
  try {
    const updated = await logicalDeviceApi.update(editing.value.id, {
      name: editForm.value.name.trim(),
      retention_days: editForm.value.retention_days,
    })
    const idx = items.value.findIndex(i => i.id === updated.id)
    if (idx >= 0) items.value[idx] = updated
    editVisible.value = false
    ElMessage.success('已保存')
  } catch (error: any) {
    ElMessage.error('保存失败: ' + (error?.message || '未知错误'))
  } finally {
    saving.value = false
  }
}

// ─── retention 通知深链 (§六 D-2: /logical-device?retention=<id> 直接打开编辑) ───
const handleRetentionDeepLink = async () => {
  const raw = route.query.retention
  if (raw == null) return
  const id = Number(raw)
  if (!Number.isFinite(id) || id <= 0) return
  if (items.value.length === 0) await fetchList()
  const target = items.value.find(i => i.id === id)
  if (target) openEdit(target)
}

watch(() => route.query.retention, (val) => {
  if (val != null) handleRetentionDeepLink()
})

// ─── 格式化 ───
const formatRows = (n: number) => {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1000) return `${(n / 1000).toFixed(1)}k`
  return String(n)
}

const formatTime = (iso: string | number | null) => {
  if (!iso) return '—'
  const d = typeof iso === 'number' ? new Date(iso) : new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  const pad = (x: number) => String(x).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

onMounted(async () => {
  await fetchList()
  handleRetentionDeepLink()
})
</script>

<style scoped>
.logical-device-page {
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.toolbar-card :deep(.el-card__body) {
  padding: 16px;
}

.filter-bar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}

.filter-left {
  display: flex;
  gap: 12px;
  flex-wrap: wrap;
  align-items: center;
}

.filter-right {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}

.search-input {
  width: 220px;
}

.filter-select {
  width: 150px;
}

.merge-hint {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: 10px;
  color: var(--el-color-info);
  font-size: 13px;
}

.ld-name {
  font-weight: 600;
  margin-right: 6px;
}

.ld-tag {
  margin-left: 4px;
}

.muted {
  color: var(--el-text-color-placeholder);
}

/* 合并预览 */
.preview-body {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.preview-alert {
  margin-bottom: 4px;
}

.timeline-section {
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 8px;
  padding: 12px;
}

.timeline-header {
  display: flex;
  justify-content: space-between;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  margin-bottom: 8px;
}

.timeline-row {
  display: grid;
  grid-template-columns: 140px 1fr 110px;
  gap: 10px;
  align-items: center;
  margin-bottom: 8px;
}

.timeline-label {
  font-size: 13px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.timeline-track {
  position: relative;
  height: 14px;
  background: var(--el-fill-color-light);
  border-radius: 7px;
  overflow: hidden;
}

.timeline-bar {
  position: absolute;
  top: 2px;
  bottom: 2px;
  border-radius: 5px;
  background: linear-gradient(90deg, var(--el-color-primary), var(--el-color-primary-light-3));
  min-width: 4px;
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.1);
  transition: all 0.3s ease;
}

.timeline-bar.overlap {
  background: linear-gradient(90deg, var(--el-color-warning), var(--el-color-warning-light-3));
  box-shadow: 0 1px 4px rgba(230, 162, 60, 0.3);
}

.timeline-bar:hover {
  transform: scaleY(1.2);
  box-shadow: 0 2px 6px rgba(0, 0, 0, 0.2);
}

.timeline-meta {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  text-align: right;
}

.preview-table {
  width: 100%;
}

.preview-summary {
  display: flex;
  gap: 32px;
  flex-wrap: wrap;
  padding: 12px;
  background: var(--el-fill-color-lighter);
  border-radius: 8px;
}

.summary-item {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.summary-label {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.summary-value {
  font-size: 16px;
  font-weight: 600;
}

.summary-note {
  font-size: 12px;
  color: var(--el-text-color-placeholder);
}

.target-name-form {
  margin-top: 4px;
}

/* 409 conflicts */
.conflict-item {
  display: flex;
  gap: 10px;
  padding: 12px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 8px;
  margin-top: 10px;
}

.conflict-icon {
  color: var(--el-color-danger);
  font-size: 18px;
  margin-top: 2px;
}

.conflict-title {
  font-size: 14px;
  font-weight: 500;
}

.conflict-detail {
  font-size: 13px;
  color: var(--el-text-color-secondary);
  margin-top: 4px;
}

/* 进度 */
.job-item {
  padding: 12px 0;
  border-bottom: 1px solid var(--el-border-color-extra-light);
}

.job-item:last-child {
  border-bottom: none;
}

.job-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 6px;
}

.job-name {
  font-size: 14px;
}

.job-meta {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  margin-top: 4px;
}

.job-retry {
  color: var(--el-color-warning);
}

.form-hint {
  margin-left: 10px;
  font-size: 12px;
  color: var(--el-text-color-placeholder);
}

/* 空状态增强 */
.empty-state-enhanced {
  padding: 20px 0;
}

.empty-state-enhanced :deep(.el-empty__description) {
  margin-top: 8px;
}

.empty-title {
  font-size: 14px;
  color: var(--el-text-color-primary);
  margin: 0 0 4px;
}

.empty-desc {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  margin: 0 0 16px;
  max-width: 320px;
  line-height: 1.5;
}

.empty-state-enhanced :deep(.el-empty__bottom) {
  margin-top: 16px;
}

@media (max-width: 768px) {
  .filter-bar {
    flex-direction: column;
    align-items: stretch;
  }

  .filter-left,
  .filter-right {
    width: 100%;
    justify-content: flex-start;
  }

  .search-input {
    width: 100%;
  }

  .timeline-row {
    grid-template-columns: 90px 1fr 80px;
  }

  .preview-summary {
    gap: 16px;
  }
}
</style>
