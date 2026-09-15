<template>
  <div class="notification-channels-page">
    <PageHeader
      title="外发通知通道"
      subtitle="把告警外发到企业微信 / Webhook / OneBot；密钥只写不读，列表仅显示末 4 位。"
    />

    <!-- 错误提示条：store.error 是唯一错误源（store 不弹窗，页面负责展示 + 重试入口） -->
    <el-alert
      v-if="store.error"
      class="nc-error-alert"
      type="error"
      :closable="false"
      show-icon
      data-test="nc-error"
    >
      <span class="nc-error-text">{{ store.error }}</span>
      <el-button link type="primary" size="small" data-test="nc-retry" @click="retryFetch">重试</el-button>
    </el-alert>

    <section class="card">
      <!-- 移动端宽表：横向滚动 + 滑动提示（theme.css .mobile-table-wrapper）。
           本表 9 列合计约 1180px，360px 视口下装不下 —— 没有 wrapper 时 el-table 自身
           overflow:hidden，右侧「操作」列不可达也无任何横滑提示（F8）。 -->
      <div class="mobile-table-wrapper">
        <div class="mobile-table-hint">← 左右滑动查看完整表格 →</div>
      <el-table v-loading="store.loading" :data="store.items" stripe data-test="nc-table">
        <el-table-column prop="id" label="ID" width="72" />
        <el-table-column prop="name" label="名称" min-width="160" show-overflow-tooltip />
        <el-table-column label="类型" width="110">
          <template #default="{ row }">
            <el-tag size="small" type="info">{{ typeText(asChannel(row).type) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="目标地址" min-width="220" show-overflow-tooltip>
          <template #default="{ row }">
            <!-- target_url 已由后端 RedactTargetURL 脱敏（企业微信 key= 不会出现在这里） -->
            <span class="mono">{{ asChannel(row).target_url || '—' }}</span>
          </template>
        </el-table-column>
        <el-table-column label="密钥" width="150">
          <template #default="{ row }">
            <span v-if="asChannel(row).has_secret" class="mono">已设置 (末 4 位 {{ asChannel(row).secret_hint || '****' }})</span>
            <span v-else class="muted">未设置</span>
          </template>
        </el-table-column>
        <el-table-column label="最低级别" width="110">
          <template #default="{ row }">
            <el-tag size="small" :type="levelTagType(asChannel(row).min_level)">{{ levelText(asChannel(row).min_level) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="启用" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="asChannel(row).enabled ? 'success' : 'info'">
              {{ asChannel(row).enabled ? '启用' : '停用' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="更新时间" width="170">
          <template #default="{ row }">{{ formatTime(asChannel(row).updated_at) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="96" fixed="right">
          <template #default="{ row }">
            <el-button
              link
              type="danger"
              size="small"
              data-test="nc-delete"
              :loading="actingId === asChannel(row).id"
              :disabled="actingId === asChannel(row).id"
              @click="onDelete(asChannel(row))"
            >删除</el-button>
          </template>
        </el-table-column>
      </el-table>
      </div>

      <EmptyState
        v-if="!store.loading && store.items.length === 0"
        kind="initial"
        icon="Bell"
        data-test="nc-empty"
        title="暂无通知通道"
        description="新建通道后，告警会按最低级别筛选后外发到对应渠道。"
      />

      <!-- 真分页：翻页/改页长都重新请求后端（page/page_size 原样发出），禁止本地对 items 切片。
           v-if 用 total > 0 而非 items.length：当前页为空时用户仍要看到「共 N 条」并翻回来。 -->
      <div v-if="store.total > 0" class="nc-pagination">
        <el-pagination
          v-model:current-page="currentPage"
          v-model:page-size="pageSize"
          :total="store.total"
          :page-sizes="PAGE_SIZES"
          layout="total, sizes, prev, pager, next, jumper"
          data-test="nc-pagination"
          @current-change="() => loadList()"
          @size-change="onPageSizeChange"
        />
      </div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import feedback from '@/utils/feedback'
import PageHeader from '@/components/common/PageHeader.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import { useNotificationChannelStore } from '@/stores/notificationChannel'
import type {
  NotificationChannel,
  NotificationChannelType,
  NotificationMinLevel,
} from '@/api/notificationChannel'

/**
 * 每页条数默认值：与后端 defaultNotificationPageSize=20 对齐
 * （handler_notification_channel.go:43）。显式发送而不是依赖后端默认 ——
 * 后端默认值是实现细节，一旦调整，前端分页器按 page_size 算的偏移就会错位。
 */
const DEFAULT_PAGE_SIZE = 20
/**
 * 每页条数选项：上限 200 与后端 maxNotificationPageSize 对齐
 * （handler_notification_channel.go:44）。不给后端会静默钳制的值（如 500），
 * 否则分页器以为一页 500 条、后端只回 200 条。
 */
const PAGE_SIZES = [20, 50, 100, 200]

/** el-table 作用域槽的 row 未从 EP 包根导出，此处做一次具名类型收窄（非 any）。 */
const asChannel = (row: unknown) => row as NotificationChannel

const store = useNotificationChannelStore()

// ── 分页状态（真分页） ──
const currentPage = ref(1)
const pageSize = ref(DEFAULT_PAGE_SIZE)

/** 每页条数变化：旧页码在新页长下可能越界，归 1 后重新查询。 */
function onPageSizeChange() {
  currentPage.value = 1
  void loadList()
}

/**
 * 拉取当前页。翻页**不得**重置页码（否则永远停在第 1 页），
 * 故页码只在这里读取，重置只发生在 onPageSizeChange / retryFetch。
 */
async function loadList() {
  try {
    await store.fetchList({ page: currentPage.value, page_size: pageSize.value })
    await fallbackIfPageEmptied()
  } catch {
    /* store.error 已记录，错误提示条负责展示 */
  }
}

/**
 * 空页回退：删除后当前页可能已越界（后端按 offset 返回空数组，total 仍是真总数）。
 * 非第 1 页且拿到空页时回退一页 —— 否则表格空白、看起来像"一条都没有"。
 */
async function fallbackIfPageEmptied() {
  if (currentPage.value <= 1 || store.items.length > 0) return
  currentPage.value -= 1
  try {
    await store.fetchList({ page: currentPage.value, page_size: pageSize.value })
  } catch {
    /* 回退请求失败：store.error 已记录，保留当前页码交给用户手动重试 */
  }
}

function retryFetch() {
  store.clearError()
  void loadList()
}

// ── 行操作 ──
const actingId = ref<number | null>(null)

async function onDelete(channel: NotificationChannel) {
  // 删除不可恢复（投递审计会保留、但通道配置本身没了）：走 confirmDanger，
  // 焦点落在安全侧（取消），避免"回车即删除"（F5 裁决）。
  const confirmed = await feedback.confirmDanger(
    `删除通知通道「${channel.name || `#${channel.id}`}」？此操作不可恢复（历史投递审计会保留）。`,
    { title: '确认删除', confirmText: '删除', cancelText: '取消' },
  )
  if (!confirmed) return

  actingId.value = channel.id
  try {
    await store.removeChannel(channel.id)
    feedback.success('已删除')
    // 删除改变 total 并可能抽空当前页：回后端取真值（loadList 内含空页回退），
    // 否则会出现「表格 19 行、分页器仍说 21 条」的下一个静默不一致。
    await loadList()
  } catch (err) {
    feedback.handleError(err)
  } finally {
    actingId.value = null
  }
}

// ── 展示纯函数（文字 + 颜色双重表达） ──
function typeText(type: NotificationChannelType): string {
  switch (type) {
    case 'webhook': return 'Webhook'
    case 'wecom': return '企业微信'
    case 'onebot': return 'OneBot'
    default: return '—'
  }
}
function levelText(level: NotificationMinLevel): string {
  switch (level) {
    case 'info': return '信息'
    case 'warning': return '警告'
    case 'error': return '错误'
    case 'critical': return '严重'
    default: return '—'
  }
}
function levelTagType(level: NotificationMinLevel): 'info' | 'warning' | 'danger' {
  switch (level) {
    case 'info': return 'info'
    case 'warning': return 'warning'
    case 'error': return 'danger'
    case 'critical': return 'danger'
    default: return 'info'
  }
}
function formatTime(t: string | null | undefined): string {
  if (!t) return '—'
  const ts = new Date(t).getTime()
  return Number.isFinite(ts) ? new Date(t).toLocaleString() : '—'
}

onMounted(() => {
  void loadList()
})
</script>

<style scoped>
.notification-channels-page {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.card {
  background: var(--el-bg-color);
  border-radius: 8px;
  padding: 16px;
}
.nc-error-alert {
  border-radius: 8px;
}
.nc-error-text {
  margin-right: 8px;
}
/* 分页条：与表格同卡，右对齐；内层 flex-wrap 修复窄屏整体被推出容器（同 DataSourceList 范式）。 */
.nc-pagination {
  display: flex;
  justify-content: flex-end;
  margin-top: 12px;
  flex-wrap: wrap;
}
.nc-pagination :deep(.el-pagination) {
  flex-wrap: wrap;
}
.mono {
  font-family: monospace;
}
.muted {
  color: var(--el-text-color-secondary);
}
</style>
