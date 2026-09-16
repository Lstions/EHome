<template>
  <div class="collector-page">
<template v-if="loading">
      <div class="stats-row">
        <SkeletonCard v-for="i in 4" :key="i" variant="stat" :icon-size="48" animated />
      </div>
      <div class="skeleton-grid">
        <SkeletonCard v-for="i in 8" :key="i" variant="card" animated />
      </div>
    </template>
    <template v-else>
      <!-- 顶部统计卡片 -->
      <div class="stats-row">
        <StatCard label="本页节点" icon-color="var(--el-color-primary)" @click="handleStatClick('all')">
          <template #icon><el-icon><Connection /></el-icon></template>
          <template #value><CountUp :value="stats.total" class="stat-value" /></template>
        </StatCard>

        <StatCard label="本页在线" icon-color="var(--el-color-success)" @click="handleStatClick('online')">
          <template #icon><el-icon><CircleCheck /></el-icon></template>
          <template #value><CountUp :value="stats.online" class="stat-value" /></template>
        </StatCard>

        <StatCard label="本页离线" icon-color="var(--el-color-danger)" @click="handleStatClick('offline')">
          <template #icon><el-icon><CircleClose /></el-icon></template>
          <template #value><CountUp :value="stats.offline" class="stat-value" /></template>
        </StatCard>

        <StatCard label="本页告警" icon-color="var(--el-color-warning)">
          <template #icon><el-icon><Warning /></el-icon></template>
          <template #value><CountUp :value="stats.warning" class="stat-value" /></template>
        </StatCard>
      </div>

    <!-- 工具栏 -->
    <div class="toolbar">
      <div class="toolbar-left">
        <el-input
          v-model="searchKeyword"
          placeholder="搜索名称/型号"
          prefix-icon="Search"
          clearable
          class="search-input"
          @input="handleSearch"
        />
        
        <el-select v-model="statusFilter" placeholder="状态筛选" clearable style="min-width: 120px;">
          <template #prefix>
            <el-icon><Filter /></el-icon>
          </template>
          <el-option label="全部" value="" />
          <el-option label="在线" value="online" />
          <el-option label="离线" value="offline" />
        </el-select>
        
        <!-- 型号选项来自**当前页**的 model 去重 (服务端无独立 facet 端点)。
             不是全库清单, 因此改选型号后需用搜索框做全库检索。 -->
        <el-select v-model="modelFilter" placeholder="型号筛选" clearable style="min-width: 120px;">
          <el-option v-for="model in modelOptions" :key="model" :label="model" :value="model" />
        </el-select>
      </div>
      
      <div class="toolbar-right">
        <el-button-group>
          <el-button :type="viewMode === 'grid' ? 'primary' : 'default'" aria-label="卡片视图" @click="viewMode = 'grid'">
            <el-icon><Grid /></el-icon>
          </el-button>
          <el-button :type="viewMode === 'list' ? 'primary' : 'default'" aria-label="表格视图" @click="viewMode = 'list'">
            <el-icon><List /></el-icon>
          </el-button>
        </el-button-group>
        
        <el-button type="primary" @click="router.push('/node?action=add')">
          <el-icon><Plus /></el-icon>
          添加节点
        </el-button>
        <el-button type="primary" @click="refreshData" :loading="refreshing">
          <el-icon><Refresh /></el-icon>
          刷新
        </el-button>
      </div>
    </div>

    <div v-if="hasActiveFilters" class="active-filters" aria-label="当前筛选条件">
      <span class="active-filters-label">当前筛选：</span>
      <el-tag v-if="searchKeyword" closable @close="searchKeyword = ''">关键词：{{ searchKeyword }}</el-tag>
      <el-tag v-if="statusFilter" closable @close="statusFilter = ''">状态：{{ statusFilter === 'online' ? '在线' : '离线' }}</el-tag>
      <el-tag v-if="modelFilter" closable @close="modelFilter = ''">型号：{{ modelFilter }}</el-tag>
      <el-button text type="primary" @click="clearFilters">清除全部</el-button>
    </div>

    <!-- 卡片视图 -->
    <div v-if="viewMode === 'grid'" class="collector-grid">
      <el-card 
        v-for="node in filteredNodes" 
        :key="node.id" 
        class="collector-card"
        :class="{ offline: node.status === 'offline' }"
        shadow="hover"
        role="button"
        tabindex="0"
        :aria-label="`查看节点 ${node.name} 详情`"
        @click="goToDetail(node.node_id)"
        @keydown.enter.prevent="goToDetail(node.node_id)"
        @keydown.space.prevent="goToDetail(node.node_id)"
      >
        <div class="card-header">
          <div class="collector-info">
            <div class="collector-icon" :class="node.status">
              <el-icon :size="24"><Cpu /></el-icon>
            </div>
            <div class="collector-meta">
              <h3>{{ node.name }}</h3>
              <span class="model">{{ node.model || '未知型号' }}</span>
            </div>
          </div>
          <div class="status-tag" :class="node.status">
            <span class="status-dot"></span>
            {{ node.status === 'online' ? '在线' : '离线' }}
          </div>
        </div>
        
        <div class="card-body">
          <div class="info-row">
            <span class="label">设备ID</span>
            <span class="value">{{ node.node_id }}</span>
          </div>
          <div class="info-row">
            <span class="label">固件版本</span>
            <el-tag size="small">{{ node.firmware_version || '未知' }}</el-tag>
          </div>
          <div class="info-row">
            <span class="label">连接质量</span>
            <div class="quality-bar" v-if="node.status === 'online'">
              <el-progress 
                :percentage="node.connection_quality || 0" 
                :stroke-width="6"
                :color="getQualityColor(node.connection_quality)"
                :show-text="false"
              />
              <span class="quality-value">{{ node.connection_quality || 0 }}%</span>
            </div>
            <span v-else class="value">-</span>
          </div>
          <div class="info-row">
            <span class="label">上线时间</span>
            <span class="value time">{{ formatRelativeTime(node.last_online_time) }}</span>
          </div>
        </div>
        
        <div class="card-footer">
          <el-button size="small" text @click.stop="handleQuickAction('config', node)">
            <el-icon><Setting /></el-icon>
            配置
          </el-button>
          <el-button size="small" text @click.stop="handleQuickAction('ota', node)">
            <el-icon><Upload /></el-icon>
            升级
          </el-button>
          <el-button size="small" text type="danger" @click.stop="handleDelete(node)">
            <el-icon><Delete /></el-icon>
            删除
          </el-button>
        </div>
      </el-card>
    </div>

    <!-- 列表视图 -->
    <el-card v-else class="collector-table-card" shadow="hover">
      <!-- 移动端宽表：横向滚动 + 滑动提示（theme.css .mobile-table-wrapper） -->
      <div class="mobile-table-wrapper">
        <div class="mobile-table-hint">← 左右滑动查看完整表格 →</div>
      <el-table 
        :data="filteredNodes" 
        v-loading="loading"
        stripe
        @row-click="(row) => goToDetail(row.node_id)"
        row-class-name="collector-row"
      >
        <el-table-column label="节点" min-width="200">
          <template #default="{ row }">
            <div class="table-collector-info">
              <div class="collector-icon" :class="row.status">
                <el-icon><Cpu /></el-icon>
              </div>
              <div>
                <div class="name">{{ row.name }}</div>
                <div class="model">{{ row.model || '未知型号' }}</div>
              </div>
            </div>
          </template>
        </el-table-column>
        
        <el-table-column prop="node_id" label="设备ID" width="100" />
        
        <el-table-column prop="firmware_version" label="固件" width="100">
          <template #default="{ row }">
            <el-tag size="small">{{ row.firmware_version || UNKNOWN }}</el-tag>
          </template>
        </el-table-column>
        
        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <div class="status-cell" :class="row.status">
              <span class="status-dot"></span>
              {{ row.status === 'online' ? '在线' : '离线' }}
            </div>
          </template>
        </el-table-column>
        
        <el-table-column label="连接质量" width="150">
          <template #default="{ row }">
            <div class="quality-bar" v-if="row.status === 'online'">
              <el-progress 
                :percentage="row.connection_quality || 0" 
                :stroke-width="6"
                :color="getQualityColor(row.connection_quality)"
                :show-text="false"
              />
              <span class="quality-value">{{ row.connection_quality || 0 }}%</span>
            </div>
            <span v-else>-</span>
          </template>
        </el-table-column>
        
        <el-table-column label="延迟" width="100">
          <template #default="{ row }">
            <span v-if="row.latency_ms > 0" :style="{ color: getLatencyColor(row.latency_ms) }">
              {{ row.latency_ms }} ms
            </span>
            <span v-else>-</span>
          </template>
        </el-table-column>
        
        <el-table-column label="上线时间" width="160">
          <template #default="{ row }">
            <span class="time">{{ formatRelativeTime(row.last_online_time) }}</span>
          </template>
        </el-table-column>
        
        <!-- 操作列只需容纳"详情/删除"两个 2 字 text 按钮：120px（原 240px 在 390px
             视口下占 62%，@360 达 69%，把固定列变成整屏）。触控热区由 .touch-target 补足。 -->
        <el-table-column label="操作" width="120" fixed="right">
          <template #default="{ row }">
            <el-button size="small" type="primary" text class="touch-target" @click.stop="goToDetail(row.node_id)">详情</el-button>
            <el-button size="small" type="danger" text class="touch-target" :aria-label="`删除 ${row.name}`" @click.stop="handleDelete(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
      </div>

      <div class="pagination-wrapper">
        <el-pagination
          v-model:current-page="currentPage"
          v-model:page-size="pageSize"
          :total="total"
          :page-sizes="[10, 20, 50]"
          layout="total, sizes, prev, pager, next, jumper"
          @current-change="() => fetchNodes()"
          @size-change="handlePageSizeChange"
        />
      </div>
    </el-card>

    <!-- 空状态 -->
    <EmptyState
      v-if="!loading && filteredNodes.length === 0"
      icon="Connection"
      title="暂无节点"
      description="开始添加第一个节点来监控您的设备"
      :quick-actions="[
        { label: '添加节点', icon: Plus, type: 'primary', handler: () => ElMessage.info('跳转添加页面') }
      ]"
    />

    </template>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, watch, onMounted, onUnmounted } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { 
  Connection, CircleCheck, CircleClose, Warning, Cpu, 
  Filter, Grid, List, Refresh, Setting, Upload, Delete,
  Plus
} from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import feedback from '@/utils/feedback'
import { UNKNOWN } from '@/utils/format'
import { useNodeStore } from '@/stores/node'
import type { NodeListParams } from '@/api/node'
import { useWebSocketStore, type WebSocketMessage } from '@/stores/websocket'
import { WS_EVENT } from '@/events/events'
import SkeletonCard from '@/components/common/SkeletonCard.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import CountUp from '@/components/common/CountUp.vue'
import StatCard from '@/components/common/StatCard.vue'
import { getQualityColor, getLatencyColor } from '@/utils/theme'

const router = useRouter()
const route = useRoute()
const nodeStore = useNodeStore()
const wsStore = useWebSocketStore()

// 状态
const currentPage = ref(1)
const pageSize = ref(20)
const total = ref(0)
const viewMode = ref<'grid' | 'list'>('grid')
const searchKeyword = ref('')
const statusFilter = ref('')
const modelFilter = ref('')

const routeSearch = typeof route.query.search === 'string' ? route.query.search : ''
const routeStatus = typeof route.query.status === 'string' ? route.query.status : ''
if (routeSearch) searchKeyword.value = routeSearch
if (routeStatus === 'online' || routeStatus === 'offline') statusFilter.value = routeStatus

const nodes = ref<any[]>([])

// ── 服务端分页 + 服务端筛选 (负债 I-11) ──
// 三个筛选 (search/status/model) 全部下沉到 GET /nodes, 与后端 Count/Find 两侧
// 同口径 —— 检索覆盖全库, 而不是"当前页本地过滤伪装成全局检索" (§3.3.5)。
// status/model 是离散选择, 变化即时下发; search 需要 300ms 防抖 (每次按键都打
// 请求不可接受)。
//
// 为什么这里**不用** useDebouncedSearch: 该 composable 内部自建 searchKeyword ref,
// 而本页已有一个模板绑定的 searchKeyword (路由 query 初始化 + 筛选标签都依赖它)。
// 直接引入会出现**两个互不相干的 ref** —— 输入框写的是页面那个, debouncedKeyword
// 跟随的是 composable 那个(永远为空), 结果是"搜索框能打字、请求里却没有 search",
// 且在单测里表现为"筛选项看起来生效、实际没下发"。故此处按同一语义 (清空立即生效、
// 非空延迟 300ms) 直接对页面自己的 ref 做防抖。
const debouncedSearch = ref('')
let searchDebounceTimer: ReturnType<typeof setTimeout> | null = null
watch(searchKeyword, (val) => {
  if (searchDebounceTimer) clearTimeout(searchDebounceTimer)
  if (!val) {
    // 清空立即生效: 用户点"×"时期待立刻看到全量, 不是 300ms 后。
    debouncedSearch.value = ''
    return
  }
  searchDebounceTimer = setTimeout(() => {
    searchDebounceTimer = null
    debouncedSearch.value = val
  }, 300)
})

const getListParams = (): NodeListParams => {
  const params: NodeListParams = { page: currentPage.value, page_size: pageSize.value }
  if (debouncedSearch.value.trim()) params.search = debouncedSearch.value.trim()
  if (statusFilter.value) params.status = statusFilter.value
  if (modelFilter.value) params.model = modelFilter.value
  return params
}
// 首屏缓存读取必须在 nodes 声明之后 —— 它现在要拿 getListParams() 的结果。
const initialCache = nodeStore.getCachedList(getListParams())
const hasInitialCache = !!initialCache
const loading = ref(!hasInitialCache)
const refreshing = ref(false)
nodes.value = initialCache?.items || []

const hasActiveFilters = computed(() => Boolean(searchKeyword.value || statusFilter.value || modelFilter.value))

// 统计数据
const stats = reactive({
  total: 0,
  online: 0,
  offline: 0,
  warning: 0,
})

// 型号选项
const modelOptions = computed(() => {
  const models = new Set(nodes.value.map(c => c.model).filter(Boolean))
  return Array.from(models)
})

// 表格/卡片直接渲染接口返回的当前页, **不再做本地切片/过滤** (真分页)。
// 改前这里对 nodes.value (当时是全量数组) 做本地 filter, 于是"筛选"只在当前页
// 生效、"翻页"只是本地切片 —— 两者都伪装成了全局行为。筛选已下沉服务端,
// 本地再过滤一次只会把服务端已经筛好的结果二次筛一遍 (且有口径漂移风险:
// 服务端 search 大小写不敏感、model 精确匹配, 本地实现必须与之一致)。
const filteredNodes = computed(() => nodes.value)

// 更新统计
const updateStats = () => {
  const list = nodes.value
  stats.total = list.length
  stats.online = list.filter(c => c.status === 'online').length
  stats.offline = list.filter(c => c.status === 'offline').length
  stats.warning = list.filter(c => c.status === 'warning').length
}

// 获取节点列表
// silent=true: 不显示骨架屏（用于 WS 推送的静默刷新）
let listRequestSequence = 0
const fetchNodes = async (silent = false, force = false, throwOnError = false) => {
  const sequence = ++listRequestSequence
  const params = getListParams()
  const showInitialSkeleton = !silent && !nodeStore.hasCachedList(params)
  if (showInitialSkeleton) loading.value = true
  try {
    await nodeStore.fetchNodes(params, force)
    if (sequence !== listRequestSequence) return
    const cached = nodeStore.getCachedList(params)
    nodes.value = cached?.items || []
    total.value = cached?.total || 0
    updateStats()
  } catch (error: any) {
    if (sequence === listRequestSequence) feedback.handleError(error, '获取节点列表失败')
    if (throwOnError) throw error
  } finally {
    if (showInitialSkeleton && sequence === listRequestSequence) loading.value = false
  }
}

// WS 推送防抖：短时间内多条事件只触发一次静默刷新
let wsRefreshTimer: ReturnType<typeof setTimeout> | null = null
const debouncedSilentRefresh = () => {
  if (wsRefreshTimer) clearTimeout(wsRefreshTimer)
  wsRefreshTimer = setTimeout(() => {
    wsRefreshTimer = null
    fetchNodes(true)
  }, 500)
}

// 刷新数据
const refreshData = async () => {
  refreshing.value = true
  try {
    await fetchNodes(false, true, true)
    ElMessage.success('数据已刷新')
  } catch {
    // fetchNodes already displayed the request error.
  } finally {
    refreshing.value = false
  }
}

// 搜索和筛选
//
// 规范 §3.2.6 MUST: 「对会改变查询范围的输入 (节点、设备、型号、时间范围、筛选)
// 重置或失效其派生状态: 选中项、**分页**、异步结果…」。
// 改前 handleFilter 只把 currentPage 归 1, 但**从不重新请求** —— 页码重置了,
// 数据还是旧的; 服务端分页后"重置页码 + 重新查询"必须成对出现, 否则用户在
// 第 3 页切换筛选会看到第 3 页的旧数据配第 1 页的页码。
const handleSearch = () => {
  // 仅占位: 真正的检索由上面 watch(searchKeyword) 的防抖副本触发 (300ms)。
}

// 每页条数变化: 页码回到第 1 页 (原页在新页长下可能已越界), 再查询。
const handlePageSizeChange = () => {
  currentPage.value = 1
  void fetchNodes()
}

// 清空筛选: 只改 ref, 由下面的 watch 统一重置页码 + 重新查询 ——
// 不在这里再调一次 fetchNodes, 否则 watch 会补发第二个请求 (重复取数)。
// useDebouncedSearch 在清空时**立即**同步 debouncedKeyword (不走 300ms 防抖),
// 因此三个 ref 的变化在同一 tick 内被 Vue 批处理成一次 watch 回调。
const clearFilters = () => {
  searchKeyword.value = ''
  statusFilter.value = ''
  modelFilter.value = ''
}

const handleStatClick = (status: string) => {
  statusFilter.value = status === 'all' ? '' : status
}

// 筛选/搜索变化 → 重置页码后重新查询 (§3.2.6 MUST)。
// 这是"筛选变化"的唯一入口: 模板里的 @change / 标签关闭 / KPI 钻取都只改 ref,
// 由这里统一收口 —— 避免"某条路径忘了重置页码"或"重复发请求"两种偏差。
// 注意改前的缺陷: 旧 handleFilter 把 currentPage 归 1 却**从不重新请求**,
// 于是页码重置了、数据仍是旧的; 服务端分页后重置与重查必须成对出现。
//
// filterReady 门控挂载期初值: 路由 query 初始化的 search/status 已经在
// getListParams() 首次取数时带上, 不需要再触发一次请求。
let filterReady = false
watch([debouncedSearch, statusFilter, modelFilter], () => {
  if (!filterReady) return
  currentPage.value = 1
  void fetchNodes()
})

// 跳转详情（新版节点总览页）
const goToDetail = (nodeId: string) => {
  router.push(`/node/${nodeId}`)
}

// 快捷操作
const handleQuickAction = (action: string, node: any) => {
  if (action === 'config') {
    router.push(`/node/${node.node_id}?tab=config`)
  } else if (action === 'ota') {
    router.push(`/firmware?node=${node.node_id}`)
  }
}

// 删除
const handleDelete = async (row: any) => {
  let deleted = false
  try {
    const confirmed = await feedback.confirmDanger(
      `确定要删除节点 "${row.name}" 吗？此操作不可恢复。`,
      { title: '警告', confirmText: '删除', cancelText: '取消' }
    )
    if (!confirmed) return

    await nodeStore.deleteNode(row.id)
    deleted = true
    nodes.value = nodes.value.filter(node => node.id !== row.id && node.node_id !== String(row.id))
    total.value = Math.max(0, total.value - 1)
    updateStats()
    ElMessage.success('删除成功')
    try {
      await fetchNodes(false, true, true)
    } catch {
      ElMessage.warning('节点已删除，但列表刷新失败，请稍后刷新')
    }
  } catch (error: any) {
    if (error !== 'cancel' && !deleted) {
      feedback.handleErrorWithContext(error, '删除节点失败')
    }
  }
}

// 工具函数
const formatRelativeTime = (time: string) => {
  if (!time) return UNKNOWN
  const now = new Date()
  const date = new Date(time)
  const diff = now.getTime() - date.getTime()
  
  const minutes = Math.floor(diff / 60000)
  const hours = Math.floor(diff / 3600000)
  const days = Math.floor(diff / 86400000)
  
  if (minutes < 1) return '刚刚'
  if (minutes < 60) return `${minutes}分钟前`
  if (hours < 24) return `${hours}小时前`
  if (days < 7) return `${days}天前`
  return date.toLocaleDateString('zh-CN')
}

// WebSocket 订阅
let unsubscribe: (() => void) | null = null

onMounted(() => {
  fetchNodes().then(() => {
    // 首次加载后再武装筛选 watch, 避免挂载期初值触发一次多余请求。
    filterReady = true
  })
  
  // 订阅状态更新：就地更新节点状态，避免全量重拉导致屏闪
  unsubscribe = wsStore.subscribe(WS_EVENT.NODE_STATUS, (message: WebSocketMessage) => {
    const payload = message.payload
    if (!payload?.node_id) return

    // 就地更新：在已有 nodes 数组中找到对应节点并更新状态字段
    const node = nodes.value.find(n => n.node_id === payload.node_id)
    if (node) {
      // 状态未变则不做任何操作
      if (payload.status && node.status !== payload.status) {
        node.status = payload.status
        if (payload.status === 'offline') {
          node.connection_quality = 0
        }
        updateStats()
      }
    } else {
      // 新节点（列表中不存在）：防抖静默拉取
      debouncedSilentRefresh()
    }
  })
})

onUnmounted(() => {
  if (unsubscribe) unsubscribe()
  if (wsRefreshTimer) clearTimeout(wsRefreshTimer)
  if (searchDebounceTimer) clearTimeout(searchDebounceTimer)
})
</script>

<style scoped>
.collector-page {
  display: flex;
  flex-direction: column;
  gap: 20px;
}

/* 统计卡片 */
.stats-row {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 16px;
}

.stat-card {
  background: var(--card-bg);
  border-radius: 12px;
  padding: 20px;
  display: flex;
  align-items: center;
  gap: 16px;
  cursor: pointer;
  transition: all 0.3s;
  border: 1px solid var(--el-border-color);
}

.stat-card:hover {
  transform: translateY(-2px);
  box-shadow: var(--shadow-md);
}

.stat-icon {
  width: 56px;
  height: 56px;
  border-radius: 12px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 28px;
  color: var(--el-text-color-secondary);
}

.stat-card.total .stat-icon { color: var(--el-color-primary); }
.stat-card.online .stat-icon { color: var(--el-color-success); }
.stat-card.offline .stat-icon { color: var(--el-color-danger); }
.stat-card.warning .stat-icon { color: var(--el-color-warning); }

.stat-content {
  align-items: center;
  justify-content: center;
  color: #fff;
  font-size: 20px;
}

.stat-content {
  flex: 1;
}

.stat-value {
  display: block;
  font-size: 28px;
  font-weight: 600;
  color: var(--el-text-color-primary);
  line-height: 1.2;
}

.stat-label {
  font-size: 13px;
  color: var(--el-text-color-secondary);
}


/* 工具栏 */
.toolbar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  background: var(--el-bg-color);
  padding: 16px 20px;
  border-radius: 12px;
  border: 1px solid var(--el-border-color);
}

.toolbar-left {
  display: flex;
  gap: 12px;
}

.search-input {
  width: 280px;
  min-width: 200px;
}

.toolbar-right {
  display: flex;
  gap: 12px;
}

.active-filters {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}

.active-filters-label {
  color: var(--el-text-color-secondary);
  font-size: 13px;
}

/* 卡片网格 */
.collector-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
  gap: 16px;
}

.collector-card {
  cursor: pointer;
  transition: all 0.3s;
  border: 1px solid var(--el-border-color);
}

.collector-card:focus-visible {
  outline: 2px solid var(--el-color-primary);
  outline-offset: 2px;
}

.collector-card:hover {
  transform: translateY(-4px);
  box-shadow: var(--shadow-lg);
}

.collector-card.offline {
  opacity: 0.8;
}

.card-header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  margin-bottom: 16px;
}

.collector-info {
  display: flex;
  gap: 12px;
}

.collector-icon {
  width: 44px;
  height: 44px;
  border-radius: 10px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #fff;
}

.collector-icon.online { background: linear-gradient(135deg, var(--el-color-success) 0%, var(--el-color-success-light-3) 100%); }
.collector-icon.offline { background: linear-gradient(135deg, var(--el-text-color-secondary) 0%, var(--el-text-color-placeholder) 100%); }

.collector-meta h3 {
  margin: 0;
  font-size: 16px;
  color: var(--el-text-color-primary);
}

.collector-meta .model {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.status-tag {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 4px 10px;
  border-radius: 20px;
  font-size: 12px;
}

.status-tag.online {
  background: var(--el-color-success-light-9);
  color: var(--el-color-success);
}

.status-tag.offline {
  background: var(--el-fill-color-light);
  color: var(--el-text-color-secondary);
}

.status-tag .status-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: currentColor;
}

.status-tag.online .status-dot {
  animation: pulse 2s infinite;
}

.card-body {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 16px 0;
  border-top: 1px solid var(--el-fill-color-light);
  border-bottom: 1px solid var(--el-fill-color-light);
}

.info-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.info-row .label {
  font-size: 13px;
  color: var(--el-text-color-secondary);
}

.info-row .value {
  font-size: 13px;
  color: var(--el-text-color-regular);
}

.info-row .value.time {
  color: var(--el-text-color-secondary);
  font-size: 12px;
}

.quality-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: 1;
  max-width: 120px;
}

.quality-bar .el-progress {
  flex: 1;
}

.quality-value {
  font-size: 12px;
  color: var(--el-text-color-regular);
  min-width: 32px;
}

.card-footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  padding-top: 12px;
}

/* 列表视图 */
.collector-table-card :deep(.el-table) {
  border-radius: 8px;
}

.collector-row {
  cursor: pointer;
}

.table-collector-info {
  display: flex;
  align-items: center;
  gap: 12px;
}

.table-collector-info .name {
  font-weight: 500;
  color: var(--el-text-color-primary);
}

.table-collector-info .model {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.status-cell {
  display: flex;
  align-items: center;
  gap: 6px;
}

.status-cell .status-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
}

.status-cell.online .status-dot { background: var(--el-color-success); }
.status-cell.offline .status-dot { background: var(--el-text-color-secondary); }

.pagination-wrapper {
  margin-top: 20px;
  display: flex;
  justify-content: flex-end;
}

/* 骨架屏 */
.skeleton-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
  gap: 16px;
}


/* 动画 */
@keyframes pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.5; }
}

/* 空状态 */
.empty-state {
  padding: 60px 0;
}

/* 响应式：中屏 2 列；移动端单行 4 列紧凑横排（占高约一行，让位给列表） */
@media (max-width: 1200px) {
  .stats-row {
    grid-template-columns: repeat(2, 1fr);
  }
}

@media (max-width: 768px) {
  .stats-row {
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: 8px;
  }

  /* 覆盖桌面 .stat-card 基础块（padding 20px/图标 56px），单行 4 列纵向紧凑小卡 */
  .stat-card {
    flex-direction: column;
    align-items: center;
    text-align: center;
    gap: 4px;
    padding: 8px 4px;
    border-radius: 10px;
  }
  .stat-icon {
    width: 22px;
    height: 22px;
    border-radius: 6px;
    font-size: 13px;
    flex-shrink: 0;
  }
  .stat-content {
    width: 100%;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 1px;
  }
  .stat-value {
    font-size: 16px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    max-width: 100%;
  }
  .stat-label {
    font-size: 10px;
    line-height: 1.3;
    max-height: 2.6em;
    overflow: hidden;
    word-break: keep-all;
    overflow-wrap: break-word;
  }

  .toolbar {
    flex-direction: column;
    gap: 12px;
  }

  .toolbar-left, .toolbar-right {
    width: 100%;
    flex-wrap: wrap;
  }

  .search-input {
    width: 100%;
  }
}

</style>
