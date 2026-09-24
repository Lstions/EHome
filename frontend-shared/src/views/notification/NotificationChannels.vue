<template>
  <div class="notification-channels-page">
    <PageHeader
      title="外发通知通道"
      subtitle="把告警外发到企业微信 / Webhook / OneBot；密钥只写不读，列表仅显示末 4 位。"
    >
      <template #extra>
        <!-- G7：测试过一次后常驻的审计入口（通知气泡会消失，用户不该为此重找菜单）。 -->
        <el-button
          v-if="lastTestedChannelId !== null"
          data-test="nc-goto-deliveries"
          @click="goToDeliveries"
        >查看通道 #{{ lastTestedChannelId }} 的投递审计</el-button>
        <el-button type="primary" :icon="Plus" data-test="nc-create" @click="openCreate">新建通道</el-button>
      </template>
    </PageHeader>

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
        <!-- 操作列：三个行内动作。测试/编辑/删除都可能改"哪一行"，统一用 actingId 串行化
             （per-row 忙态而不是表级 loading：表级遮罩会让用户看不出点的是哪一行）。
             :fixed 按窄屏取消 —— 330px 的固定列在 360px 视口会整列盖住数据列（F26 实测）。 -->
        <el-table-column label="操作" width="330" :fixed="isMobile ? false : 'right'">
          <template #default="{ row }">
            <el-button
              link
              type="primary"
              size="small"
              data-test="nc-test"
              :loading="actingId === asChannel(row).id"
              :disabled="actingId === asChannel(row).id"
              @click="onTest(asChannel(row))"
            >测试</el-button>
            <el-button
              link
              type="primary"
              size="small"
              data-test="nc-edit"
              :disabled="actingId === asChannel(row).id"
              @click="openEdit(asChannel(row))"
            >编辑</el-button>
            <el-button
              link
              type="danger"
              size="small"
              data-test="nc-delete"
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
        :quick-actions="[{ label: '新建通道', type: 'primary', handler: openCreate }]"
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

    <!-- 新建 / 编辑共用同一个对话框（设计 §8）：表单、预设模板与 secret 三态全在子组件里 -->
    <NotificationChannelFormDialog
      v-model:visible="dialogVisible"
      :channel="editingChannel"
      :submitting="submitting"
      @submit="onSubmitForm"
    />
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElNotification } from 'element-plus'
import { Plus } from '@element-plus/icons-vue'
import feedback from '@/utils/feedback'
import PageHeader from '@/components/common/PageHeader.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import NotificationChannelFormDialog from '@/components/notification/NotificationChannelFormDialog.vue'
import { useResponsive } from '@/composables/useResponsive'
import { useNotificationChannelStore } from '@/stores/notificationChannel'
import type {
  NotificationChannel,
  NotificationChannelCreatePayload,
  NotificationChannelType,
  NotificationChannelUpdatePayload,
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

/**
 * 窄屏取消固定操作列（F26 合同的同款范式）：固定列绘制顺序恒在普通列之上，
 * 330px 的固定列在 360px 视口会整列盖住数据列与行内控件。
 */
const { isMobile } = useResponsive()

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
/**
 * 行内动作的"当前行"标记。**同时**充当互斥锁：任一行动作进行中，所有行的按钮都禁用
 * （编辑对话框开着的时候还能删同一行，是并发的静默不一致来源）。
 */
const actingId = ref<number | null>(null)

// ── G7：测试投递后的审计入口 ──
const router = useRouter()
/** 最近一次测试的通道与目标路由（模板里提供常驻入口，不依赖通知是否还在屏幕上）。 */
const lastTestedChannelId = ref<number | null>(null)
const lastTestedTarget = ref<{ name: string; query: Record<string, string> } | null>(null)
function goToDeliveries() {
  if (lastTestedTarget.value) void router.push(lastTestedTarget.value)
}

// ── 新建 / 编辑对话框 ──
const dialogVisible = ref(false)
const submitting = ref(false)
/** null = 新建；非 null = 正在编辑的通道快照 */
const editingChannel = ref<NotificationChannel | null>(null)

function openCreate() {
  editingChannel.value = null
  dialogVisible.value = true
}

function openEdit(channel: NotificationChannel) {
  editingChannel.value = channel
  dialogVisible.value = true
}

/**
 * 提交表单：新建走 createChannel，编辑走 updateChannel。
 *
 * payload 的 secret 键语义由子组件保证（见 NotificationChannelFormDialog.buildPayload）：
 * 这里**只做透传**，绝不"顺手补一个 secret"—— 页面上拿得到的东西只有 secret_hint（末 4 位），
 * 把它补进请求体就是用 4 位假密钥覆盖真密钥。
 */
async function onSubmitForm(payload: NotificationChannelCreatePayload | NotificationChannelUpdatePayload) {
  const editing = editingChannel.value
  submitting.value = true
  try {
    if (editing) {
      await store.updateChannel(editing.id, payload as NotificationChannelUpdatePayload)
      feedback.success('通道已更新')
    } else {
      await store.createChannel(payload as NotificationChannelCreatePayload)
      // 新建改变 total：回后端取真值（同删除路径），否则分页器停在旧总数上
      currentPage.value = 1
      await loadList()
      feedback.success('通道已创建')
    }
    dialogVisible.value = false
    editingChannel.value = null
  } catch (err) {
    feedback.handleErrorWithContext(err, editing ? '更新通道失败' : '创建通道失败')
  } finally {
    submitting.value = false
  }
}

/**
 * 测试按钮 → POST /:id/test。
 *
 * 文案必须是"已发出"而不是"投递成功"：后端是**异步投递**，HTTP 200 只代表
 * 测试消息进了投递队列（响应 state 恒为 pending），出站结果稍后才落到投递审计。
 * 说成"投递成功"就是本仓明令禁止的假绿。
 */
async function onTest(channel: NotificationChannel) {
  actingId.value = channel.id
  try {
    const res = await store.testChannel(channel.id)
    const channelId = res?.channel_id ?? channel.id
    // G7：改前只弹一句「实际投递结果见投递审计」，用户得自己找菜单跳过去再手动筛通道
    // （4-7 步）。此处让提示**带一个直达入口**，并把该通道带上，落在已筛好的审计页。
    //
    // 注意 deliveries_url 是**后端 API 路径**（`handler_notification_channel.go:499`：
    // `/api/v1/notification-deliveries?channel_id=N`），不是前端路由——不能直接 push，
    // 否则会命中前端路由表外的地址。故只借用它的 channel_id 语义，路由用 name。
    const target = { name: 'NotificationDeliveries', query: { channel_id: String(channelId) } }
    ElNotification({
      type: 'success',
      duration: 8000,
      title: '测试消息已发出',
      message: `通道 #${channelId}：HTTP 200 仅表示进入投递队列，实际结果见投递审计。`,
      onClick: () => { void router.push(target) },
    })
    lastTestedChannelId.value = channelId
    lastTestedTarget.value = target
  } catch (err) {
    feedback.handleErrorWithContext(err, '测试消息发送失败')
  } finally {
    actingId.value = null
  }
}

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
