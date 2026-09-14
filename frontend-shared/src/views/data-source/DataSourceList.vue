<template>
  <div class="data-source-page">
    <PageHeader title="数据源" subtitle="为逻辑设备的数据类别声明主备来源，按健康度自动切换权威来源">
      <template #extra>
        <el-button type="primary" :icon="Plus" data-test="create-source" @click="openCreate">新建数据源</el-button>
      </template>
    </PageHeader>

    <!-- 概览指标条 -->
    <div class="stats-row">
      <StatCard label="总来源" icon-color="var(--el-color-primary)" data-test="ds-stat-total">
        <template #icon><el-icon><Connection /></el-icon></template>
        <template #value><span class="stat-value">{{ metric(store.total) }}</span></template>
      </StatCard>
      <StatCard label="权威" icon-color="var(--el-color-success)" data-test="ds-stat-active">
        <template #icon><el-icon><CircleCheck /></el-icon></template>
        <template #value><span class="stat-value">{{ metric(store.activeCount) }}</span></template>
      </StatCard>
      <StatCard label="待命" icon-color="var(--el-color-info)" data-test="ds-stat-standby">
        <template #icon><el-icon><Clock /></el-icon></template>
        <template #value><span class="stat-value">{{ metric(store.standbyCount) }}</span></template>
      </StatCard>
      <StatCard label="熔断" icon-color="var(--el-color-danger)" data-test="ds-stat-error">
        <template #icon><el-icon><WarningFilled /></el-icon></template>
        <template #value><span class="stat-value">{{ metric(store.errorCount) }}</span></template>
      </StatCard>
    </div>

    <!-- 错误态：store 捕获的加载/操作错误，可重试 -->
    <el-alert
      v-if="store.error"
      type="error"
      :closable="false"
      show-icon
      class="ds-error-alert"
      data-test="ds-error"
    >
      <template #title>
        <span class="ds-error-text">加载数据源失败：{{ store.error }}</span>
        <el-button link type="primary" size="small" data-test="ds-retry" @click="retryFetch">重试</el-button>
      </template>
    </el-alert>

    <!-- 筛选区 -->
    <el-card class="filter-card" shadow="never">
      <div class="filter-bar">
        <el-select
          v-if="deviceOptions.length"
          v-model="filterDeviceId"
          placeholder="逻辑设备"
          clearable
          filterable
          class="filter-item"
          data-test="filter-device"
          @change="applyFilters"
        >
          <el-option v-for="d in deviceOptions" :key="d.id" :label="d.name || `逻辑设备 #${d.id}`" :value="d.id" />
        </el-select>
        <el-input-number
          v-else
          v-model="filterDeviceId"
          :min="1"
          :controls="false"
          placeholder="逻辑设备 ID"
          class="filter-item"
          data-test="filter-device-id"
          @change="applyFilters"
        />
        <el-input
          v-model="filterCategory"
          placeholder="数据类别"
          clearable
          class="filter-item"
          data-test="filter-category"
          @keyup.enter="applyFilters"
        />
        <el-select
          v-model="filterStatus"
          placeholder="状态"
          clearable
          class="filter-item"
          data-test="filter-status"
          @change="applyFilters"
        >
          <el-option label="权威" value="active" />
          <el-option label="待命" value="standby" />
          <el-option label="熔断" value="error" />
          <el-option label="已停用" value="disabled" />
        </el-select>
        <el-button type="primary" data-test="search-btn" @click="applyFilters">查询</el-button>
        <el-button data-test="reset-filters" @click="resetFilters">重置筛选</el-button>
      </div>
    </el-card>

    <!-- 来源表格 -->
    <section class="card">
      <!-- 移动端宽表：横向滚动 + 滑动提示（theme.css .mobile-table-wrapper）。
           本表 9 列合计 1270px，且「操作」是 330px 的固定列 —— 390px 视口下占表格盒
           106.5%，整列压住「名称」列（F26）。窄屏已取消该列的 fixed，见下表列定义。 -->
      <div class="mobile-table-wrapper">
        <div class="mobile-table-hint">← 左右滑动查看完整表格 →</div>
      <el-table :data="store.items" v-loading="store.loading" data-test="ds-table">
        <el-table-column label="逻辑设备" min-width="100">
          <template #default="{ row }">
            <span class="mono">{{ asSource(row).device_id }}</span>
          </template>
        </el-table-column>
        <el-table-column label="类别" min-width="120">
          <template #default="{ row }">
            <span class="mono">{{ asSource(row).category }}</span>
          </template>
        </el-table-column>
        <el-table-column label="来源边缘设备" min-width="120">
          <template #default="{ row }">
            <span class="mono">{{ asSource(row).edge_device_id }}</span>
          </template>
        </el-table-column>
        <el-table-column label="名称" min-width="150" show-overflow-tooltip>
          <template #default="{ row }">
            <el-button link type="primary" size="small" data-test="ds-detail" @click="openDetail(asSource(row))">
              {{ asSource(row).name || `来源 #${asSource(row).id}` }}
            </el-button>
          </template>
        </el-table-column>
        <el-table-column label="优先级" width="90">
          <template #default="{ row }">{{ asSource(row).priority }}</template>
        </el-table-column>
        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <el-tag :type="statusTagType(asSource(row).status)" size="small" data-test="ds-status">
              {{ statusText(asSource(row).status) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="连续失败" width="110">
          <template #default="{ row }">
            <span class="mono">{{ asSource(row).fail_count }}/{{ asSource(row).max_fail_count }}</span>
          </template>
        </el-table-column>
        <el-table-column label="最后成功" min-width="150">
          <template #default="{ row }">{{ relativeTime(asSource(row).last_success) }}</template>
        </el-table-column>
        <!-- F26 裁决 D2：窄屏取消固定列（原 fixed="right"）。
             实测（360/390px，48 点 elementFromPoint 网格）：330px 的粘性列大于表格盒
             （占比 117.9%/106.5%），整列压在「名称」列上 —— 行内 5 个操作按钮与
             「名称」链接命中自身 0/48、命中固定列 48/48；桌面 1440px 命中 48/48 正常。
             故只在窄屏让位：宽度 330px 与列内容一字不改 ⇒ 桌面（≥769px）视觉密度
             逐像素不变（§4.2.3 紧凑运维密度）。
             为什么不照 EdgeDeviceList 收窄成 96px 图标列：实测本表 5 个 36px 热区
             放不进 96px，会折成 5 行、桌面行高 40px → 183px，直接改变桌面密度；
             即使收窄到单行所需的 208px，窄屏仍占表格盒 67.1%（照样遮挡），
             且桌面列宽也被改动 —— 两条都不满足本任务的硬性约束。 -->
        <el-table-column label="操作" width="330" :fixed="isMobile ? false : 'right'">
          <template #default="{ row }">
            <el-button
              link
              type="primary"
              size="small"
              data-test="ds-activate"
              :loading="actingId === asSource(row).id"
              :disabled="asSource(row).status === 'active' || actingId === asSource(row).id"
              :title="asSource(row).status === 'active' ? '该来源已是权威，无需切换' : ''"
              @click="onActivate(asSource(row))"
            >切换为权威</el-button>
            <el-button
              link
              type="warning"
              size="small"
              data-test="ds-deactivate"
              :loading="actingId === asSource(row).id"
              :disabled="asSource(row).status === 'disabled' || actingId === asSource(row).id"
              :title="asSource(row).status === 'disabled' ? '该来源已停用' : ''"
              @click="onDeactivate(asSource(row))"
            >停用</el-button>
            <el-button
              link
              size="small"
              data-test="ds-reset"
              :loading="actingId === asSource(row).id"
              :disabled="asSource(row).status !== 'error' || actingId === asSource(row).id"
              :title="asSource(row).status !== 'error' ? '仅熔断来源可重置' : ''"
              @click="onReset(asSource(row))"
            >重置</el-button>
            <el-button link type="primary" size="small" data-test="ds-edit" @click="openEdit(asSource(row))">编辑</el-button>
            <el-button
              link
              type="danger"
              size="small"
              data-test="ds-delete"
              :loading="actingId === asSource(row).id"
              :disabled="actingId === asSource(row).id"
              @click="onDelete(asSource(row))"
            >删除</el-button>
          </template>
        </el-table-column>
      </el-table>
      </div>

      <EmptyState
        v-if="!store.loading && store.items.length === 0"
        kind="initial"
        icon="Link"
        data-test="ds-empty"
        title="暂无数据源"
        description="创建数据源后，可按健康度自动切换权威来源。"
        :quick-actions="[{ label: '新建数据源', type: 'primary', handler: openCreate }]"
      />
    </section>

    <!-- 新建 / 编辑对话框 -->
    <el-dialog
      v-model="dialogVisible"
      :title="editingId === null ? '新建数据源' : '编辑数据源'"
      width="560px"
      data-test="ds-dialog"
    >
      <el-form :model="form" label-width="120px" data-test="ds-form">
        <el-form-item label="逻辑设备 ID" required>
          <el-input-number
            v-model="form.device_id"
            :min="1"
            :controls="false"
            :disabled="editingId !== null"
            data-test="field-device-id"
          />
        </el-form-item>
        <el-form-item label="数据类别" required>
          <el-input
            v-model="form.category"
            placeholder="如：temperature"
            :disabled="editingId !== null"
            data-test="field-category"
          />
        </el-form-item>
        <el-form-item label="来源边缘设备 ID" required>
          <el-input-number
            v-model="form.edge_device_id"
            :min="1"
            :controls="false"
            :disabled="editingId !== null"
            data-test="field-edge-device-id"
          />
        </el-form-item>
        <el-form-item label="名称">
          <el-input v-model="form.name" placeholder="如：客厅温度主来源" data-test="field-name" />
        </el-form-item>
        <el-form-item label="描述">
          <el-input v-model="form.description" type="textarea" :rows="2" data-test="field-description" />
        </el-form-item>
        <el-form-item label="优先级">
          <el-input-number v-model="form.priority" :controls="false" data-test="field-priority" />
        </el-form-item>
        <el-form-item label="声明为主">
          <el-switch v-model="form.is_primary" data-test="field-is-primary" />
        </el-form-item>
        <el-form-item label="熔断阈值" required>
          <el-input-number v-model="form.max_fail_count" :min="1" :max="20" data-test="field-max-fail-count" />
          <span class="hint">连续失败达到该值后熔断（1..20）</span>
        </el-form-item>
        <el-form-item label="配置 (JSON)">
          <el-input v-model="form.config" type="textarea" :rows="3" data-test="field-config" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" data-test="save-source" @click="onSave">保存</el-button>
      </template>
    </el-dialog>

    <!-- 详情抽屉：尺寸走 DETAIL_DRAWER_SIZE（见 script），窄视口下不溢出 -->
    <el-drawer v-model="detailVisible" title="数据源详情" :size="DETAIL_DRAWER_SIZE" data-test="ds-drawer">
      <div v-if="detailSource" class="detail-body">
        <el-descriptions :column="1" border>
          <el-descriptions-item label="ID">{{ detailSource.id }}</el-descriptions-item>
          <el-descriptions-item label="逻辑设备">{{ detailSource.device_id }}</el-descriptions-item>
          <el-descriptions-item label="数据类别">{{ detailSource.category }}</el-descriptions-item>
          <el-descriptions-item label="来源边缘设备">{{ detailSource.edge_device_id }}</el-descriptions-item>
          <el-descriptions-item label="来源类型">{{ detailSource.source_type }}</el-descriptions-item>
          <el-descriptions-item label="名称">{{ detailSource.name || '—' }}</el-descriptions-item>
          <el-descriptions-item label="描述">{{ detailSource.description || '—' }}</el-descriptions-item>
          <el-descriptions-item label="优先级">{{ detailSource.priority }}</el-descriptions-item>
          <el-descriptions-item label="声明为主">{{ detailSource.is_primary ? '是' : '否' }}</el-descriptions-item>
          <el-descriptions-item label="状态">
            <el-tag :type="statusTagType(detailSource.status)" size="small">{{ statusText(detailSource.status) }}</el-tag>
          </el-descriptions-item>
          <el-descriptions-item label="连续失败">{{ detailSource.fail_count }}/{{ detailSource.max_fail_count }}</el-descriptions-item>
          <el-descriptions-item label="最后成功">{{ formatTime(detailSource.last_success) }}</el-descriptions-item>
          <el-descriptions-item label="最后失败">{{ formatTime(detailSource.last_failure) }}</el-descriptions-item>
          <el-descriptions-item label="配置">{{ detailSource.config || '—' }}</el-descriptions-item>
          <el-descriptions-item label="创建时间">{{ formatTime(detailSource.created_at) }}</el-descriptions-item>
          <el-descriptions-item label="更新时间">{{ formatTime(detailSource.updated_at) }}</el-descriptions-item>
        </el-descriptions>

        <h4 class="section-title">健康记录</h4>
        <el-timeline v-loading="store.healthLoading" data-test="ds-health-timeline">
          <el-timeline-item v-for="h in store.health" :key="h.id" :timestamp="formatTime(h.created_at)">
            <div class="timeline-content">
              <el-tag size="small" :type="h.status === 'failure' ? 'danger' : 'warning'">
                {{ h.status === 'failure' ? '失败' : '状态迁移' }}
              </el-tag>
              <span class="timeline-message">{{ h.message || '—' }}</span>
              <span class="timeline-meta">响应 {{ h.response_time }} ms</span>
            </div>
          </el-timeline-item>
          <el-timeline-item v-if="!store.healthLoading && store.health.length === 0" timestamp="">暂无健康记录</el-timeline-item>
        </el-timeline>

        <h4 class="section-title">切换日志</h4>
        <el-timeline v-loading="store.failoverLoading" data-test="ds-failover-timeline">
          <el-timeline-item v-for="log in store.failoverLogs" :key="log.id" :timestamp="formatTime(log.created_at)">
            <div class="timeline-content">
              <span class="mono">{{ log.category }}</span>
              <span class="mono">#{{ log.from_source_id }} → #{{ log.to_source_id }}</span>
              <span>原因：{{ reasonText(log.reason) }}</span>
              <span>触发：{{ triggerText(log.trigger) }}</span>
            </div>
          </el-timeline-item>
          <el-timeline-item v-if="!store.failoverLoading && store.failoverLogs.length === 0" timestamp="">暂无切换日志</el-timeline-item>
        </el-timeline>
      </div>
    </el-drawer>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import feedback from '@/utils/feedback'
import { Plus, Connection, CircleCheck, Clock, WarningFilled } from '@element-plus/icons-vue'
import PageHeader from '@/components/common/PageHeader.vue'
import StatCard from '@/components/common/StatCard.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import { useResponsive } from '@/composables/useResponsive'
import { useDataSourceStore } from '@/stores/dataSource'
import { logicalDeviceApi, type LogicalDeviceItem } from '@/api/logicalDevice'
import type {
  DataSource,
  DataSourceStatus,
  DataSourceListParams,
  CreateDataSourceRequest,
  UpdateDataSourceRequest,
  FailoverReason,
  FailoverTrigger,
} from '@/api/dataSource'

// isMobile（<768px）用于窄屏取消操作列的固定（F26），断点与 theme.css 一致。
const { width: viewportWidth, isMobile } = useResponsive()

/**
 * 详情抽屉宽度：桌面保持 560px，窄视口按 92vw 收敛（与全局 .el-dialog 的 92vw 兜底同一比例），
 * 保证 360px 视口下抽屉左边缘仍 >= 0、内部 el-descriptions 的 label 不被推出可视区。
 * 为什么在组件侧算而不是写 CSS 兜底：el-drawer 默认 teleport 到 body，组件的 scoped <style>
 * 命中不了它（规范 §4.3.2.3）；而 Element Plus 把 size 直接写进 drawer 根节点的 inline style
 * （useResizable → addUnit(props.size)），全局 .el-drawer max-width 兜底又会波及所有抽屉。
 * 用 useResponsive 的共享视口宽度而非 useMediaQuery：抽屉宽度随视口连续收敛，断点处不突变。
 */
const DETAIL_DRAWER_SIZE = computed(() => `${Math.min(560, Math.round(viewportWidth.value * 0.92))}px`)

/** el-table 作用域槽的 row 未从 EP 包根导出，此处做一次具名类型的边界收窄（非 any）。 */
const asSource = (row: unknown) => row as DataSource

const store = useDataSourceStore()

// ── 状态映射纯函数（文字 + 颜色双重表达） ──
function statusText(status: DataSourceStatus): string {
  switch (status) {
    case 'active': return '权威'
    case 'standby': return '待命'
    case 'error': return '熔断'
    case 'disabled': return '已停用'
  }
}
function statusTagType(status: DataSourceStatus): 'success' | 'info' | 'danger' {
  switch (status) {
    case 'active': return 'success'
    case 'standby': return 'info'
    case 'error': return 'danger'
    case 'disabled': return 'info'
  }
}
function reasonText(reason: FailoverReason): string {
  switch (reason) {
    case 'auto': return '自动切换'
    case 'manual': return '手动切换'
    case 'manual_deactivate': return '手动停用'
  }
}
function triggerText(trigger: FailoverTrigger): string {
  switch (trigger) {
    case 'device_offline': return '设备离线'
    case 'stale_data': return '数据停滞'
    default: return '—'
  }
}
function formatTime(t: string | null | undefined): string {
  if (!t) return '—'
  const ts = new Date(t).getTime()
  return Number.isFinite(ts) ? new Date(t).toLocaleString() : '—'
}
function relativeTime(t: string | null | undefined): string {
  if (!t) return '—'
  const ts = new Date(t).getTime()
  if (!Number.isFinite(ts)) return '—'
  const diff = Date.now() - ts
  if (diff < 0) return '刚刚'
  const sec = Math.floor(diff / 1000)
  if (sec < 60) return `${sec} 秒前`
  const min = Math.floor(sec / 60)
  if (min < 60) return `${min} 分钟前`
  const hour = Math.floor(min / 60)
  if (hour < 24) return `${hour} 小时前`
  return `${Math.floor(hour / 24)} 天前`
}
function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

// ── 列表加载与指标 ──
const loaded = ref(false)
const metricsReady = computed(() => loaded.value && !store.error)
function metric(value: number): string | number {
  return metricsReady.value ? value : '—'
}

function buildParams(): DataSourceListParams {
  const params: DataSourceListParams = {}
  if (filterDeviceId.value != null) {
    params.device_id = Number(filterDeviceId.value)
  }
  if (filterCategory.value.trim()) params.category = filterCategory.value.trim()
  if (filterStatus.value) params.status = filterStatus.value as DataSourceStatus
  return params
}

async function loadList() {
  try {
    await store.fetchList(buildParams())
  } catch {
    /* store.error 已记录，错误提示条负责展示 */
  } finally {
    loaded.value = true
  }
}
function applyFilters() {
  store.clearError()
  void loadList()
}
function retryFetch() {
  store.clearError()
  void loadList()
}
function resetFilters() {
  filterDeviceId.value = undefined
  filterCategory.value = ''
  filterStatus.value = ''
  applyFilters()
}

// ── 筛选条件 ──
const filterDeviceId = ref<number | undefined>(undefined)
const filterCategory = ref('')
const filterStatus = ref<'' | DataSourceStatus>('')

// ── 逻辑设备下拉（真实 api；取不到时降级为设备 ID 数字输入） ──
const deviceOptions = ref<LogicalDeviceItem[]>([])
async function loadDevices() {
  try {
    const res = await logicalDeviceApi.list()
    deviceOptions.value = Array.isArray(res?.items) ? res.items : []
  } catch {
    deviceOptions.value = []
  }
}

// ── 行操作 ──
const actingId = ref<number | null>(null)

async function onActivate(source: DataSource) {
  actingId.value = source.id
  try {
    await store.activateSource(source.id)
    ElMessage.success('已切换为权威来源')
  } catch (err) {
    ElMessage.error(errorMessage(err))
  } finally {
    actingId.value = null
  }
}

async function onDeactivate(source: DataSource) {
  if (source.status === 'active') {
    // 停用权威来源会改变组内接手方：按破坏性动作处理（danger 确认按钮）。
    const confirmed = await feedback.confirmDanger(
      `停用权威来源「${source.name || `#${source.id}`}」？组内候选将自动接替。`,
      { title: '确认停用', confirmText: '停用', cancelText: '取消' },
    )
    if (!confirmed) return
  }
  actingId.value = source.id
  try {
    await store.deactivateSource(source.id)
    ElMessage.success('已停用')
  } catch (err) {
    ElMessage.error(errorMessage(err))
  } finally {
    actingId.value = null
  }
}

async function onReset(source: DataSource) {
  actingId.value = source.id
  try {
    await store.resetSource(source.id)
    ElMessage.success('已重置为待命')
  } catch (err) {
    ElMessage.error(errorMessage(err))
  } finally {
    actingId.value = null
  }
}

async function onDelete(source: DataSource) {
  // 删除数据源不可恢复：确认文案含对象身份，确认按钮为 danger。
  const confirmed = await feedback.confirmDanger(
    `删除数据源「${source.name || `#${source.id}`}」？此操作不可恢复。`,
    { title: '确认删除', confirmText: '删除', cancelText: '取消' },
  )
  if (!confirmed) return

  actingId.value = source.id
  try {
    await store.removeSource(source.id)
    ElMessage.success('已删除')
  } catch (err) {
    ElMessage.error(errorMessage(err))
  } finally {
    actingId.value = null
  }
}

// ── 新建 / 编辑对话框 ──
const dialogVisible = ref(false)
const saving = ref(false)
const editingId = ref<number | null>(null)
const emptyForm = () => ({
  device_id: 0,
  category: '',
  edge_device_id: 0,
  name: '',
  description: '',
  priority: 0,
  is_primary: false,
  max_fail_count: 3,
  config: '',
})
const form = reactive(emptyForm())

function openCreate() {
  editingId.value = null
  Object.assign(form, emptyForm())
  dialogVisible.value = true
}
function openEdit(source: DataSource) {
  editingId.value = source.id
  Object.assign(form, {
    device_id: source.device_id,
    category: source.category,
    edge_device_id: source.edge_device_id,
    name: source.name,
    description: source.description,
    priority: source.priority,
    is_primary: source.is_primary,
    max_fail_count: source.max_fail_count,
    config: source.config,
  })
  dialogVisible.value = true
}

function validate(): string | null {
  if (!Number.isInteger(form.device_id) || form.device_id < 1) return '逻辑设备 ID 必须为正整数'
  if (!form.category.trim()) return '请填写数据类别'
  if (!Number.isInteger(form.edge_device_id) || form.edge_device_id < 1) return '来源边缘设备 ID 必须为正整数'
  if (!Number.isInteger(form.max_fail_count) || form.max_fail_count < 1 || form.max_fail_count > 20) {
    return '熔断阈值必须在 1..20 之间'
  }
  return null
}

/** 新建：提交契约要求的必填三字段 + 可选字段 */
function buildCreatePayload(): CreateDataSourceRequest {
  return {
    device_id: Number(form.device_id),
    category: form.category.trim(),
    edge_device_id: Number(form.edge_device_id),
    source_type: 'edge_device',
    name: form.name.trim(),
    description: form.description.trim(),
    priority: form.priority,
    is_primary: form.is_primary,
    max_fail_count: form.max_fail_count,
    config: form.config,
  }
}

/** 编辑：仅提交 UpdateDataSourceRequest 白名单字段，禁止提交 device_id/category/edge_device_id */
function buildUpdatePayload(): UpdateDataSourceRequest {
  return {
    name: form.name.trim(),
    description: form.description.trim(),
    priority: form.priority,
    is_primary: form.is_primary,
    max_fail_count: form.max_fail_count,
    config: form.config,
  }
}

async function onSave() {
  const err = validate()
  if (err) {
    ElMessage.warning(err)
    return
  }
  saving.value = true
  try {
    if (editingId.value !== null) {
      await store.updateSource(editingId.value, buildUpdatePayload())
      ElMessage.success('数据源已更新')
    } else {
      await store.createSource(buildCreatePayload())
      ElMessage.success('数据源已创建')
    }
    dialogVisible.value = false
  } catch (e) {
    ElMessage.error(errorMessage(e))
  } finally {
    saving.value = false
  }
}

// ── 详情抽屉 ──
const detailVisible = ref(false)
const detailSource = ref<DataSource | null>(null)
async function openDetail(source: DataSource) {
  detailSource.value = source
  detailVisible.value = true
  try {
    await Promise.all([
      store.fetchHealth(source.id, 50),
      store.fetchFailoverLogs(source.device_id, { limit: 20 }),
    ])
  } catch (err) {
    ElMessage.error(errorMessage(err))
  }
}

onMounted(() => {
  void loadList()
  void loadDevices()
})
</script>

<style scoped>
.data-source-page {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.stats-row {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 16px;
}
.filter-card {
  border-radius: 8px;
}
.filter-bar {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
  align-items: center;
}
.filter-item {
  width: 180px;
}
.card {
  background: var(--el-bg-color);
  border-radius: 8px;
  padding: 16px;
}
.ds-error-alert {
  border-radius: 8px;
}
.ds-error-text {
  margin-right: 8px;
}
.section-title {
  margin: 20px 0 12px;
  font-size: 14px;
  font-weight: 600;
  color: var(--el-text-color-primary);
}
.timeline-content {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
  color: var(--el-text-color-regular);
}
.timeline-message {
  color: var(--el-text-color-primary);
}
.timeline-meta {
  color: var(--el-text-color-secondary);
  font-size: 12px;
}
.mono {
  font-family: monospace;
}
.hint {
  margin-left: 8px;
  color: var(--el-text-color-secondary);
  font-size: 12px;
}

@media (max-width: 768px) {
  .stats-row {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
  .filter-item {
    width: 100%;
  }
}
</style>
