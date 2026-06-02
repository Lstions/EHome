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
        <div class="stat-card" @click="handleStatClick('all')">
          <div class="stat-icon" style="color: #409eff;">
            <el-icon><Connection /></el-icon>
          </div>
          <div class="stat-content">
            <CountUp :value="stats.total" class="stat-value" />
            <span class="stat-label">采集器总数</span>
          </div>
          <div class="stat-action">
            <el-icon><Plus /></el-icon>
          </div>
        </div>
        
        <div class="stat-card online" @click="handleStatClick('online')">
          <div class="stat-icon" style="color: #67c23a;">
            <el-icon><CircleCheck /></el-icon>
          </div>
          <div class="stat-content">
            <CountUp :value="stats.online" class="stat-value" />
            <span class="stat-label">在线</span>
          </div>
          <div class="stat-trend up">
            <el-icon><TrendCharts /></el-icon>
            {{ stats.onlineRate }}%
          </div>
        </div>
        
        <div class="stat-card offline" @click="handleStatClick('offline')">
          <div class="stat-icon" style="color: #f56c6c;">
            <el-icon><CircleClose /></el-icon>
          </div>
          <div class="stat-content">
            <CountUp :value="stats.offline" class="stat-value" />
            <span class="stat-label">离线</span>
          </div>
        </div>
        
        <div class="stat-card warning">
          <div class="stat-icon" style="color: #e6a23c;">
            <el-icon><Warning /></el-icon>
          </div>
          <div class="stat-content">
            <CountUp :value="stats.warning" class="stat-value" />
            <span class="stat-label">告警</span>
          </div>
        </div>
      </div>

    <!-- 工具栏 -->
    <div class="toolbar">
      <div class="toolbar-left">
        <el-input
          v-model="searchKeyword"
          placeholder="搜索采集器名称、型号..."
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
          <el-button :type="viewMode === 'grid' ? 'primary' : 'default'" @click="viewMode = 'grid'">
            <el-icon><Grid /></el-icon>
          </el-button>
          <el-button :type="viewMode === 'list' ? 'primary' : 'default'" @click="viewMode = 'list'">
            <el-icon><List /></el-icon>
          </el-button>
        </el-button-group>
        
        <el-button type="primary" @click="router.push('/collectors?action=add')">
          <el-icon><Plus /></el-icon>
          添加采集器
        </el-button>
        <el-button type="primary" @click="refreshData">
          <el-icon><Refresh /></el-icon>
          刷新
        </el-button>
      </div>
    </div>

    <!-- 卡片视图 -->
    <div v-if="viewMode === 'grid'" class="collector-grid">
      <el-card 
        v-for="collector in filteredCollectors" 
        :key="collector.id" 
        class="collector-card"
        :class="{ offline: collector.status === 'offline' }"
        shadow="hover"
        @click="goToDetail(collector.id)"
      >
        <div class="card-header">
          <div class="collector-info">
            <div class="collector-icon" :class="collector.status">
              <el-icon :size="24"><Cpu /></el-icon>
            </div>
            <div class="collector-meta">
              <h3>{{ collector.name }}</h3>
              <span class="model">{{ collector.model || '未知型号' }}</span>
            </div>
          </div>
          <div class="status-tag" :class="collector.status">
            <span class="status-dot"></span>
            {{ collector.status === 'online' ? '在线' : '离线' }}
          </div>
        </div>
        
        <div class="card-body">
          <div class="info-row">
            <span class="label">设备ID</span>
            <span class="value">{{ collector.device_id }}</span>
          </div>
          <div class="info-row">
            <span class="label">固件版本</span>
            <el-tag size="small">{{ collector.firmware_version || '未知' }}</el-tag>
          </div>
          <div class="info-row">
            <span class="label">连接质量</span>
            <div class="quality-bar" v-if="collector.status === 'online'">
              <el-progress 
                :percentage="collector.connection_quality || 0" 
                :stroke-width="6"
                :color="getQualityColor(collector.connection_quality)"
                :show-text="false"
              />
              <span class="quality-value">{{ collector.connection_quality || 0 }}%</span>
            </div>
            <span v-else class="value">-</span>
          </div>
          <div class="info-row">
            <span class="label">最后在线</span>
            <span class="value time">{{ formatRelativeTime(collector.last_online_time) }}</span>
          </div>
        </div>
        
        <div class="card-footer">
          <el-button size="small" text @click.stop="handleQuickAction('config', collector)">
            <el-icon><Setting /></el-icon>
            配置
          </el-button>
          <el-button size="small" text @click.stop="handleQuickAction('ota', collector)">
            <el-icon><Upload /></el-icon>
            升级
          </el-button>
          <el-button size="small" text type="danger" @click.stop="handleDelete(collector)">
            <el-icon><Delete /></el-icon>
            删除
          </el-button>
        </div>
      </el-card>
    </div>

    <!-- 列表视图 -->
    <el-card v-else class="collector-table-card">
      <el-table 
        :data="filteredCollectors" 
        v-loading="loading"
        stripe
        @row-click="(row) => goToDetail(row.id)"
        row-class-name="collector-row"
      >
        <el-table-column label="采集器" min-width="200">
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
        
        <el-table-column prop="device_id" label="设备ID" width="100" />
        
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
        
        <el-table-column label="最后在线" width="160">
          <template #default="{ row }">
            <span class="time">{{ formatRelativeTime(row.last_online_time) }}</span>
          </template>
        </el-table-column>
        
        <el-table-column label="操作" width="180" fixed="right">
          <template #default="{ row }">
            <el-button size="small" @click.stop="goToDetail(row.id)">详情</el-button>
            <el-button size="small" type="danger" plain @click.stop="handleDelete(row)">删除</el-button>
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
          @current-change="fetchCollectors"
          @size-change="fetchCollectors"
        />
      </div>
    </el-card>

    <!-- 空状态 -->
    <EmptyState
      v-if="!loading && filteredCollectors.length === 0"
      icon="Connection"
      title="暂无采集器"
      description="开始添加第一个采集器来监控您的设备"
      :quick-actions="[
        { label: '添加采集器', icon: Plus, type: 'primary', handler: () => ElMessage.info('跳转添加页面') }
      ]"
    />

    </template>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import { 
  Connection, CircleCheck, CircleClose, Warning, Cpu, Search, 
  Filter, Grid, List, Refresh, Setting, Upload, Delete, TrendCharts,
  Plus
} from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useCollectorStore } from '@/stores/collector'
import { useWebSocketStore, type WebSocketMessage } from '@/stores/websocket'
import SkeletonCard from '@/components/common/SkeletonCard.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import CountUp from '@/components/common/CountUp.vue'

const router = useRouter()
const collectorStore = useCollectorStore()
const wsStore = useWebSocketStore()

// 状态
const loading = ref(false)
const collectors = ref<any[]>([])
const currentPage = ref(1)
const pageSize = ref(20)
const total = ref(0)
const viewMode = ref<'grid' | 'list'>('grid')
const searchKeyword = ref('')
const statusFilter = ref('')
const modelFilter = ref('')

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
  const models = new Set(collectors.value.map(c => c.model).filter(Boolean))
  return Array.from(models)
})

// 过滤后的采集器
const filteredCollectors = computed(() => {
  let result = collectors.value
  
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
  const list = collectors.value
  stats.total = list.length
  stats.online = list.filter(c => c.status === 'online').length
  stats.offline = list.filter(c => c.status === 'offline').length
  stats.warning = list.filter(c => c.status === 'warning').length
  stats.onlineRate = stats.total > 0 ? Math.round((stats.online / stats.total) * 100) : 0
}

// 获取采集器列表
const fetchCollectors = async () => {
  loading.value = true
  try {
    await collectorStore.fetchCollectors({
      page: currentPage.value,
      page_size: pageSize.value
    })
    collectors.value = collectorStore.collectors
    total.value = collectorStore.total
    updateStats()
  } catch (error: any) {
    ElMessage.error('获取采集器列表失败')
  } finally {
    loading.value = false
  }
}

// 刷新数据
const refreshData = () => {
  fetchCollectors()
  ElMessage.success('数据已刷新')
}

// 搜索和筛选
const handleSearch = () => {
  // 实时搜索，防抖
}

const handleFilter = () => {
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
const goToDetail = (id: number) => {
  router.push(`/collectors/${id}`)
}

// 快捷操作
const handleQuickAction = (action: string, collector: any) => {
  if (action === 'config') {
    router.push(`/collectors/${collector.id}?tab=config`)
  } else if (action === 'ota') {
    router.push(`/firmware?collector=${collector.id}`)
  }
}

// 删除
const handleDelete = async (row: any) => {
  try {
    await ElMessageBox.confirm(
      `确定要删除采集器 "${row.name}" 吗？此操作不可恢复。`,
      '警告',
      { confirmButtonText: '删除', cancelButtonText: '取消', type: 'warning' }
    )
    
    await collectorStore.deleteCollector(row.id)
    ElMessage.success('删除成功')
    await fetchCollectors()
  } catch (error: any) {
    if (error !== 'cancel') {
      ElMessage.error('删除失败')
    }
  }
}

// 工具函数
const getQualityColor = (quality: number) => {
  if (quality >= 80) return '#67c23a'
  if (quality >= 50) return '#e6a23c'
  return '#f56c6c'
}

const getLatencyColor = (ms: number) => {
  if (ms < 50) return '#67c23a'
  if (ms < 200) return '#e6a23c'
  return '#f56c6c'
}

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
  fetchCollectors()
  
  // 订阅状态更新
  unsubscribe = wsStore.subscribe('status', (message: WebSocketMessage) => {
    if (message.payload?.collector_id) {
      fetchCollectors()
    }
  })
})

onUnmounted(() => {
  if (unsubscribe) unsubscribe()
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
  background: #fff;
  border-radius: 12px;
  padding: 20px;
  display: flex;
  align-items: center;
  gap: 16px;
  cursor: pointer;
  transition: all 0.3s;
  border: 1px solid #e8eaec;
}

.stat-card:hover {
  transform: translateY(-2px);
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.08);
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

.stat-card.total .stat-icon { color: #409eff; }
.stat-card.online .stat-icon { color: #67c23a; }
.stat-card.offline .stat-icon { color: #f56c6c; }
.stat-card.warning .stat-icon { color: #e6a23c; }

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
  color: #303133;
  line-height: 1.2;
}

.stat-label {
  font-size: 13px;
  color: #909399;
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
  background: #f0f9eb;
  color: #67c23a;
}

/* 工具栏 */
.toolbar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  background: #fff;
  padding: 16px 20px;
  border-radius: 12px;
  border: 1px solid #e8eaec;
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

/* 卡片网格 */
.collector-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
  gap: 16px;
}

.collector-card {
  cursor: pointer;
  transition: all 0.3s;
  border: 1px solid #e8eaec;
}

.collector-card:hover {
  transform: translateY(-4px);
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.12);
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

.collector-icon.online { background: linear-gradient(135deg, #67c23a 0%, #85ce61 100%); }
.collector-icon.offline { background: linear-gradient(135deg, #909399 0%, #b1b3b8 100%); }

.collector-meta h3 {
  margin: 0;
  font-size: 16px;
  color: #303133;
}

.collector-meta .model {
  font-size: 12px;
  color: #909399;
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
  background: #f0f9eb;
  color: #67c23a;
}

.status-tag.offline {
  background: #f4f4f5;
  color: #909399;
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
  border-top: 1px solid #f5f7fa;
  border-bottom: 1px solid #f5f7fa;
}

.info-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.info-row .label {
  font-size: 13px;
  color: #909399;
}

.info-row .value {
  font-size: 13px;
  color: #606266;
}

.info-row .value.time {
  color: #909399;
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
  color: #606266;
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
  color: #303133;
}

.table-collector-info .model {
  font-size: 12px;
  color: #909399;
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

.status-cell.online .status-dot { background: #67c23a; }
.status-cell.offline .status-dot { background: #909399; }

.pagination-wrapper {
  margin-top: 16px;
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
  color: #409eff;
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

/* 响应式 */
@media (max-width: 1200px) {
  .stats-row {
    grid-template-columns: repeat(2, 1fr);
  }
}

@media (max-width: 768px) {
  .stats-row {
    grid-template-columns: 1fr;
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
