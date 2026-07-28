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
          <template #suffix>
            <div class="stat-action"><el-icon><Plus /></el-icon></div>
          </template>
        </StatCard>

        <StatCard label="本页在线" icon-color="var(--el-color-success)" @click="handleStatClick('online')">
          <template #icon><el-icon><CircleCheck /></el-icon></template>
          <template #value><CountUp :value="stats.online" class="stat-value" /></template>
          <template #suffix>
            <div class="stat-trend up"><el-icon><TrendCharts /></el-icon>{{ stats.onlineRate }}%</div>
          </template>
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
        
        <el-select v-model="statusFilter" placeholder="状态筛选" clearable @change="handleFilter" style="min-width: 120px;">
          <template #prefix>
            <el-icon><Filter /></el-icon>
          </template>
          <el-option label="全部" value="" />
          <el-option label="在线" value="online" />
          <el-option label="离线" value="offline" />
        </el-select>
        
        <el-select v-model="modelFilter" placeholder="型号筛选" clearable @change="handleFilter" style="min-width: 120px;">
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
      <el-tag v-if="searchKeyword" closable @close="searchKeyword = ''; handleFilter()">关键词：{{ searchKeyword }}</el-tag>
      <el-tag v-if="statusFilter" closable @close="statusFilter = ''; handleFilter()">状态：{{ statusFilter === 'online' ? '在线' : '离线' }}</el-tag>
      <el-tag v-if="modelFilter" closable @close="modelFilter = ''; handleFilter()">型号：{{ modelFilter }}</el-tag>
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
        @click="goToDetail(node.node_id)"
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
            <el-tag size="small">{{ row.firmware_version || '-' }}</el-tag>
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
        
        <el-table-column label="操作" width="180" fixed="right">
          <template #default="{ row }">
            <el-button size="small" @click.stop="goToDetail(row.node_id)">详情</el-button>
            <el-button size="small" type="danger" text @click.stop="handleDelete(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>

      <div class="pagination-wrapper">
        <el-pagination
          v-model:current-page="currentPage"
          v-model:page-size="pageSize"
          :total="total"
          :page-sizes="[10, 20, 50]"
          layout="total, sizes, prev, pager, next, jumper"
          @current-change="() => fetchNodes()"
          @size-change="() => fetchNodes()"
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
import { ref, reactive, computed, onMounted, onUnmounted } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { 
  Connection, CircleCheck, CircleClose, Warning, Cpu, Search, 
  Filter, Grid, List, Refresh, Setting, Upload, Delete, TrendCharts,
  Plus
} from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useNodeStore } from '@/stores/node'
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

const getListParams = () => ({ page: currentPage.value, page_size: pageSize.value })
const initialCache = nodeStore.getCachedList(getListParams())
const hasInitialCache = !!initialCache
const loading = ref(!hasInitialCache)
const refreshing = ref(false)
const nodes = ref<any[]>(initialCache?.items || [])

const hasActiveFilters = computed(() => Boolean(searchKeyword.value || statusFilter.value || modelFilter.value))

// 统计数据
const stats = reactive({
  total: 0,
  online: 0,
  offline: 0,
  warning: 0,
  onlineRate: 0
})

// 型号选项
const modelOptions = computed(() => {
  const models = new Set(nodes.value.map(c => c.model).filter(Boolean))
  return Array.from(models)
})

// 过滤后的采集器
const filteredNodes = computed(() => {
  let result = nodes.value
  
  if (searchKeyword.value) {
    const kw = searchKeyword.value.toLowerCase()
    result = result.filter(c => 
      c.name?.toLowerCase().includes(kw) || 
      c.model?.toLowerCase().includes(kw)
    )
  }
  
  if (statusFilter.value) {
    result = result.filter(c => c.status === statusFilter.value)
  }
  
  if (modelFilter.value) {
    result = result.filter(c => c.model === modelFilter.value)
  }
  
  return result
})

// 更新统计
const updateStats = () => {
  const list = nodes.value
  stats.total = list.length
  stats.online = list.filter(c => c.status === 'online').length
  stats.offline = list.filter(c => c.status === 'offline').length
  stats.warning = list.filter(c => c.status === 'warning').length
  stats.onlineRate = stats.total > 0 ? Math.round((stats.online / stats.total) * 100) : 0
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
    if (sequence === listRequestSequence) ElMessage.error('获取节点列表失败')
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
const handleSearch = () => {
  // 实时搜索，防抖
}

const handleFilter = () => {
  currentPage.value = 1
}

const clearFilters = () => {
  searchKeyword.value = ''
  statusFilter.value = ''
  modelFilter.value = ''
  currentPage.value = 1
}

const handleStatClick = (status: string) => {
  if (status === 'all') {
    statusFilter.value = ''
  } else {
    statusFilter.value = status
  }
}

// 跳转详情
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
    await ElMessageBox.confirm(
      `确定要删除节点 "${row.name}" 吗？此操作不可恢复。`,
      '警告',
      { confirmButtonText: '删除', cancelButtonText: '取消', type: 'warning' }
    )
    
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
      ElMessage.error('删除失败')
    }
  }
}

// 工具函数
const formatRelativeTime = (time: string) => {
  if (!time) return '-'
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
  fetchNodes()
  
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

.stat-trend {
  display: flex;
  align-items: center;
  gap: 4px;
  font-size: 12px;
  padding: 4px 8px;
  border-radius: 4px;
}

.stat-trend.up {
  background: var(--el-color-success-light-9);
  color: var(--el-color-success);
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

.stat-card .stat-action {
  opacity: 0;
  transition: opacity 0.2s;
  color: var(--el-color-primary);
}

.stat-card:hover .stat-action {
  opacity: 1;
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

/* 响应式：中屏 2 列；移动端保持 2 列（避免单列占高过大挤出内容，基线缺陷③） */
@media (max-width: 1200px) {
  .stats-row {
    grid-template-columns: repeat(2, 1fr);
  }
}

@media (max-width: 768px) {
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

@media (max-width: 480px) {
  .stats-row { gap: 10px; }
}
</style>
