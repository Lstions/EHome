<template>
  <div class="notification-deliveries-page">
    <PageHeader
      title="投递审计"
      subtitle="每次投递尝试一行记录：pending 是「尝试已开始、结论未落库」的中间态，不等同于已送达。"
    />

    <!-- 错误提示条：store.error 是唯一错误源（store 不弹窗，页面负责展示 + 重试入口） -->
    <el-alert
      v-if="store.error"
      class="nd-error-alert"
      type="error"
      :closable="false"
      show-icon
      data-test="nd-error"
    >
      <span class="nd-error-text">{{ store.error }}</span>
      <el-button link type="primary" size="small" data-test="nd-retry" @click="retryFetch">重试</el-button>
    </el-alert>

    <section class="card">
      <!-- 过滤条：两个条件都是**发给后端**的查询参数，且有服务端索引。
           禁用态跟随 loading：过滤请求在途时再点一次下拉只会制造乱序响应
           （后发的请求先回来 ⇒ 表格与下拉不一致）。 -->
      <div class="nd-filters">
        <label class="nd-filter">
          <span class="nd-filter-label">通道</span>
          <el-select
            v-model="channelId"
            class="nd-filter-control"
            placeholder="全部通道"
            clearable
            :disabled="store.loading"
            aria-label="按通道过滤"
            data-test="nd-filter-channel"
            @change="onFilterChange"
          >
            <el-option
              v-for="c in channels"
              :key="c.id"
              :label="`#${c.id} ${c.name || '(未命名)'}`"
              :value="c.id"
            />
          </el-select>
        </label>
        <label class="nd-filter">
          <span class="nd-filter-label">状态</span>
          <el-select
            v-model="stateFilter"
            class="nd-filter-control"
            placeholder="全部状态"
            clearable
            :disabled="store.loading"
            aria-label="按状态过滤"
            data-test="nd-filter-state"
            @change="onFilterChange"
          >
            <el-option v-for="s in STATE_OPTIONS" :key="s.value" :label="s.label" :value="s.value" />
          </el-select>
        </label>
        <span v-if="!channelsLoaded" class="nd-filter-note" data-test="nd-channels-note">
          通道名列表未能加载（过滤仍可用通道 ID）；这不影响审计记录本身的展示。
        </span>
      </div>

      <!-- 移动端宽表：横向滚动 + 滑动提示（theme.css .mobile-table-wrapper）。
           本表 8 列，360px 视口下装不下；没有 wrapper 时 el-table 自身 overflow:hidden，
           右侧列不可达也无任何横滑提示（F8）。 -->
      <div class="mobile-table-wrapper">
        <div class="mobile-table-hint">← 左右滑动查看完整表格 →</div>
      <el-table v-loading="store.loading" :data="store.items" stripe data-test="nd-table">
        <el-table-column prop="id" label="ID" width="80" />
        <el-table-column label="通知" width="90">
          <template #default="{ row }">#{{ asDelivery(row).notification_id }}</template>
        </el-table-column>
        <el-table-column label="通道" min-width="150" show-overflow-tooltip>
          <template #default="{ row }">
            <!-- 通道可能已被删除（删除通道**保留**审计行），所以只显示 id + 尽力显示的名字。 -->
            <span>{{ channelText(asDelivery(row).channel_id) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="120">
          <template #default="{ row }">
            <!-- 文字 + 颜色双重表达：颜色单独承载语义对色觉障碍用户不可读（规范 §3.4） -->
            <el-tag size="small" :type="stateTagType(asDelivery(row).state)" :title="stateHint(asDelivery(row).state)">
              {{ stateText(asDelivery(row).state) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="尝试" width="70">
          <template #default="{ row }">第 {{ asDelivery(row).attempt_no }} 次</template>
        </el-table-column>
        <el-table-column label="结果" min-width="260" show-overflow-tooltip>
          <template #default="{ row }">
            <!-- 失败行必须能看出**为什么**失败：HTTP 状态码 + 后端脱敏后的 error_message。
                 这里**不拼**任何 URL/密钥 —— 审计行里本来就没有凭据，error_message
                 落库前已过后端 RedactText。 -->
            <template v-if="asDelivery(row).state === 'failed'">
              <span class="nd-status-code mono">{{ statusCodeText(asDelivery(row).status_code) }}</span>
              <span class="nd-error-message">{{ asDelivery(row).error_message || '（后端未给出原因）' }}</span>
            </template>
            <span v-else-if="asDelivery(row).state === 'pending'" class="muted">
              尝试已开始，结论尚未落库（等待出站结果或进程重启前的残留行）
            </span>
            <span v-else class="muted">出站成功{{ asDelivery(row).status_code ? `（HTTP ${asDelivery(row).status_code}）` : '' }}</span>
          </template>
        </el-table-column>
        <el-table-column label="耗时" width="100">
          <template #default="{ row }">{{ durationText(asDelivery(row).duration_ms) }}</template>
        </el-table-column>
        <el-table-column label="时间" width="170">
          <template #default="{ row }">{{ formatTime(asDelivery(row).created_at) }}</template>
        </el-table-column>
      </el-table>
      </div>

      <EmptyState
        v-if="!store.loading && store.items.length === 0"
        kind="initial"
        icon="Bell"
        data-test="nd-empty"
        :title="hasFilter ? '没有符合条件的投递记录' : '暂无投递记录'"
        :description="hasFilter
          ? '当前过滤条件下没有投递尝试。可放宽过滤条件（通道/状态）后再看。'
          : '通知通道发出测试消息或触发告警后，每次投递尝试都会在这里留下一行审计记录。'"
      />

      <!-- 真分页：翻页/改页长都重新请求后端（page/page_size 原样发出），禁止本地对 items 切片。
           v-if 用 total > 0 而非 items.length：当前页为空时用户仍要看到「共 N 条」并翻回来。 -->
      <div v-if="store.total > 0" class="nd-pagination">
        <el-pagination
          v-model:current-page="currentPage"
          v-model:page-size="pageSize"
          :total="store.total"
          :page-sizes="PAGE_SIZES"
          layout="total, sizes, prev, pager, next, jumper"
          data-test="nd-pagination"
          @current-change="() => loadList()"
          @size-change="onPageSizeChange"
        />
      </div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import PageHeader from '@/components/common/PageHeader.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import { notificationChannelApi, type NotificationChannel, type NotificationDelivery, type NotificationDeliveryState } from '@/api/notificationChannel'
import { useNotificationDeliveryStore } from '@/stores/notificationDelivery'

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

/** 状态下拉项：与后端 validDeliveryStates 的三个取值一一对应（不给第四个"全部"，用"清空"表达）。 */
const STATE_OPTIONS: Array<{ value: NotificationDeliveryState; label: string }> = [
  { value: 'pending', label: '进行中（结论未落库）' },
  { value: 'delivered', label: '已送达' },
  { value: 'failed', label: '失败' },
]

/** el-table 作用域槽的 row 未从 EP 包根导出，此处做一次具名类型收窄（非 any）。 */
const asDelivery = (row: unknown) => row as NotificationDelivery

const store = useNotificationDeliveryStore()

// ── 过滤状态 ──
/** 空值（undefined）= 不过滤。**不发空串** —— 后端对非空 state 做白名单校验，空串会直接 400。 */
const channelId = ref<number | undefined>(undefined)
const stateFilter = ref<NotificationDeliveryState | undefined>(undefined)
const hasFilter = computed(() => channelId.value !== undefined || stateFilter.value !== undefined)

/**
 * 通道下拉的数据源。**只用于把 channel_id 显示成人看得懂的名字**：
 * 审计行的真值是 channel_id，所以通道列表加载失败时过滤与列表**照常可用**
 * （降级为只显示 #id），不把它做成页面级错误 —— 那是本页的非关键路径。
 */
const channels = ref<NotificationChannel[]>([])
const channelsLoaded = ref(false)

// ── 分页状态（真分页） ──
const currentPage = ref(1)
const pageSize = ref(DEFAULT_PAGE_SIZE)

/**
 * 组装查询参数：只放**有值**的键。
 *
 * 这里不做"补 undefined / 空串"的兜底：axios 会把 undefined 键整个丢掉，空串却会真的发出去
 * （后端收到 state= 会当成非法值回 400）。故显式判空后再赋值，语义一目了然。
 */
function buildParams() {
  const params: { channel_id?: number; state?: NotificationDeliveryState; page: number; page_size: number } = {
    page: currentPage.value,
    page_size: pageSize.value,
  }
  if (channelId.value !== undefined) params.channel_id = channelId.value
  if (stateFilter.value !== undefined) params.state = stateFilter.value
  return params
}

/**
 * 拉取当前页。翻页**不得**重置页码（否则永远停在第 1 页），
 * 故页码只在这里读取，重置只发生在 onFilterChange / onPageSizeChange / retryFetch。
 */
async function loadList() {
  try {
    await store.fetchList(buildParams())
  } catch {
    /* store.error 已记录，错误提示条负责展示 */
  }
}

/**
 * 过滤条件变化：**必须**重置 page=1（规范 §3.2.6 MUST / §5「筛选变化重置页码」）。
 *
 * 为什么不是"保持不变再请求"：第 3 页 + 新过滤条件 ⇒ 后端在新结果集上取 offset=40，
 * 新的结果集往往不足 40 条 ⇒ 返回空页，用户以为"没有匹配记录"。
 * 页码留在 3、total 变成 5 条，分页器还会显示"第 3 页 / 共 1 页"这种自相矛盾的状态。
 */
function onFilterChange() {
  currentPage.value = 1
  void loadList()
}

/** 每页条数变化：旧页码在新页长下可能越界，归 1 后重新查询。 */
function onPageSizeChange() {
  currentPage.value = 1
  void loadList()
}

function retryFetch() {
  store.clearError()
  void loadList()
}

/**
 * 通道名映射（只用于展示）。审计行按 id DESC 排序，通道可能已被删除
 * （DELETE /notification-channels/:id 明确保留审计行），故找不到名字时回落到 #id。
 */
function channelText(id: number): string {
  const hit = channels.value.find((c) => c.id === id)
  return hit ? `#${id} ${hit.name || '(未命名)'}` : `#${id}`
}

// ── 展示纯函数（文字 + 颜色双重表达） ──
/**
 * 状态文案。**pending 的措辞是本页最关键的一处**：它是"尝试已开始、结论未落库"的
 * 中间态（设计 §5 状态机：先写 pending 行再去出站，拿到结论后改写 delivered/failed）。
 * 写成"投递中"或"已发送"都会把"请求成功"说成"送达成功" —— 本仓明令禁止的假绿。
 */
function stateText(state: NotificationDeliveryState): string {
  switch (state) {
    case 'pending': return '进行中'
    case 'delivered': return '已送达'
    case 'failed': return '失败'
    default: return '—'
  }
}
function stateHint(state: NotificationDeliveryState): string {
  switch (state) {
    case 'pending': return '尝试已开始，结论尚未落库：可能仍在出站，也可能是进程在写入结论前退出的残留行'
    case 'delivered': return '出站成功且结论已落库'
    case 'failed': return '该次尝试失败；最后一次失败即该通道投递的终态'
    default: return ''
  }
}
function stateTagType(state: NotificationDeliveryState): 'info' | 'success' | 'danger' {
  switch (state) {
    case 'pending': return 'info'
    case 'delivered': return 'success'
    case 'failed': return 'danger'
    default: return 'info'
  }
}
/** 失败行的 HTTP 状态码：0 表示**没走到拿到响应那一步**（连不上/DNS/SSRF 拒绝/超时），不是"HTTP 0"。 */
function statusCodeText(code: number): string {
  return code > 0 ? `HTTP ${code}` : '无响应'
}
function durationText(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return '—'
  return ms < 1000 ? `${ms} ms` : `${(ms / 1000).toFixed(2)} s`
}
function formatTime(t: string | null | undefined): string {
  if (!t) return '—'
  const ts = new Date(t).getTime()
  return Number.isFinite(ts) ? new Date(t).toLocaleString() : '—'
}

/**
 * 拉通道名（非关键路径）：失败只把 channelsLoaded 置 false，**不写 store.error**。
 * 若把它当页面级错误，用户会在"审计记录好好的、只是名字没查到"时看到一个红色错误条。
 */
async function loadChannels() {
  try {
    const res = await notificationChannelApi.list({ page: 1, page_size: 200 })
    channels.value = Array.isArray(res?.items) ? res.items : []
    channelsLoaded.value = true
  } catch {
    channelsLoaded.value = false
  }
}

onMounted(() => {
  void loadList()
  void loadChannels()
})
</script>

<style scoped>
.notification-deliveries-page {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.card {
  background: var(--el-bg-color);
  border-radius: 8px;
  padding: 16px;
}
.nd-error-alert {
  border-radius: 8px;
}
.nd-error-text {
  margin-right: 8px;
}
/* 过滤条：窄屏换行，控件最小宽度保证下拉内容可读 */
.nd-filters {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 12px;
  margin-bottom: 12px;
}
.nd-filter {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
.nd-filter-label {
  font-size: 13px;
  color: var(--el-text-color-secondary);
  white-space: nowrap;
}
.nd-filter-control {
  width: 220px;
}
.nd-filter-note {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
/* 分页条：与表格同卡，右对齐；内层 flex-wrap 修复窄屏整体被推出容器（同 DataSourceList 范式）。 */
.nd-pagination {
  display: flex;
  justify-content: flex-end;
  margin-top: 12px;
  flex-wrap: wrap;
}
.nd-pagination :deep(.el-pagination) {
  flex-wrap: wrap;
}
.nd-status-code {
  display: inline-block;
  min-width: 68px;
  color: var(--el-color-danger);
}
.nd-error-message {
  color: var(--el-color-danger);
  word-break: break-all;
}
.mono {
  font-family: monospace;
}
.muted {
  color: var(--el-text-color-secondary);
}
</style>
