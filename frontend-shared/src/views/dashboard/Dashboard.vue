<template>
  <div class="dashboard">
    <PageHeader title="仪表盘" />

    <template v-if="loading">
      <div class="dashboard-stats">
        <SkeletonCard v-for="i in 4" :key="i" variant="stat" :icon-size="48" animated />
      </div>
    </template>
    <template v-else>
      <div class="dashboard-stats">
        <el-card shadow="hover" class="stat-card" role="link" tabindex="0" aria-label="查看采集器总数" @click="router.push('/node')" @keydown.enter.prevent="router.push('/node')" @keydown.space.prevent="router.push('/node')">
          <div class="stat-content">
            <div class="stat-icon" style="color: var(--el-color-primary);">
              <el-icon :size="32"><Connection /></el-icon>
            </div>
            <div class="stat-info">
              <p class="stat-value">{{ overview.nodes?.total || 0 }}</p>
              <p class="stat-label">采集器总数</p>
            </div>
          </div>
        </el-card>

        <el-card shadow="hover" class="stat-card" role="link" tabindex="0" aria-label="查看在线采集器" @click="router.push('/node?status=online')" @keydown.enter.prevent="router.push('/node?status=online')" @keydown.space.prevent="router.push('/node?status=online')">
          <div class="stat-content">
            <div class="stat-icon" style="color: var(--el-color-success);">
              <el-icon :size="32"><CircleCheck /></el-icon>
            </div>
            <div class="stat-info">
              <p class="stat-value">{{ overview.nodes?.online || 0 }}</p>
              <p class="stat-label">在线采集器</p>
            </div>
          </div>
        </el-card>

        <el-card shadow="hover" class="stat-card" role="link" tabindex="0" aria-label="查看设备总数" @click="router.push('/edge-device')" @keydown.enter.prevent="router.push('/edge-device')" @keydown.space.prevent="router.push('/edge-device')">
          <div class="stat-content">
            <div class="stat-icon" style="color: var(--el-color-warning);">
              <el-icon :size="32"><Cpu /></el-icon>
            </div>
            <div class="stat-info">
              <p class="stat-value">{{ overview.edge_devices?.total || 0 }}</p>
              <p class="stat-label">设备总数</p>
            </div>
          </div>
        </el-card>

        <el-card shadow="hover" class="stat-card" role="link" tabindex="0" aria-label="查看在线设备" @click="router.push('/edge-device?status=online')" @keydown.enter.prevent="router.push('/edge-device?status=online')" @keydown.space.prevent="router.push('/edge-device?status=online')">
          <div class="stat-content">
            <div class="stat-icon" style="color: var(--el-color-success);">
              <el-icon :size="32"><CircleCheck /></el-icon>
            </div>
            <div class="stat-info">
              <p class="stat-value">{{ overview.edge_devices?.online || 0 }}</p>
              <p class="stat-label">在线设备</p>
            </div>
          </div>
        </el-card>
      </div>
    </template>

    <!-- 告警 / 异常摘要 -->
    <el-row v-if="overview" :gutter="20" style="margin-top: 20px;">
      <el-col :span="24">
        <el-card shadow="hover" class="alert-summary">
          <template #header>
            <div style="display: flex; align-items: center; gap: 8px;">
              <el-icon :color="hasAlerts ? 'var(--el-color-warning)' : 'var(--el-color-success)'" :size="20">
                <component :is="hasAlerts ? WarningFilled : CircleCheck" />
              </el-icon>
              <span>异常摘要</span>
              <el-tag size="small" :type="hasAlerts ? 'warning' : 'success'">{{ hasAlerts ? '需关注' : '运行正常' }}</el-tag>
            </div>
          </template>
          <div v-if="hasAlerts" class="alert-list">
            <div v-if="offlineCollectors > 0" class="alert-item" @click="router.push('/node?status=offline')">
              <el-icon color="var(--el-color-danger)" :size="28"><Connection /></el-icon>
              <div>
                <div class="alert-value">{{ offlineCollectors }}</div>
                <div class="alert-label">离线采集器</div>
              </div>
            </div>
            <div v-if="offlineDevices > 0" class="alert-item" @click="router.push('/edge-device?status=offline')">
              <el-icon color="var(--el-color-danger)" :size="28"><Cpu /></el-icon>
              <div>
                <div class="alert-value">{{ offlineDevices }}</div>
                <div class="alert-label">离线设备</div>
              </div>
            </div>
            <div v-if="dataErrorCount > 0" class="alert-item" @click="router.push('/data')">
              <el-icon color="var(--el-color-warning)" :size="28"><WarningFilled /></el-icon>
              <div>
                <div class="alert-value">{{ dataErrorCount }}</div>
                <div class="alert-label">采集错误（近 1h）</div>
              </div>
            </div>
          </div>
          <div v-else class="alert-ok">
            <el-icon color="var(--el-color-success)" :size="20"><CircleCheck /></el-icon>
            <span>采集器与设备均在线，暂无采集错误。</span>
          </div>
        </el-card>
      </el-col>
    </el-row>

    <!-- 24h 趋势图表 -->
    <el-row :gutter="20" style="margin-top: 20px;">
      <el-col :span="24">
        <el-card shadow="hover">
          <template #header>
            <div class="trend-card-header">
              <span>{{ trendRangeLabel }}趋势</span>
              <div class="trend-controls">
                <el-radio-group v-model="trendRange" size="small" @change="fetchTrendData">
                  <el-radio-button value="1h">1小时</el-radio-button>
                  <el-radio-button value="24h">24小时</el-radio-button>
                  <el-radio-button value="7d">7天</el-radio-button>
                </el-radio-group>
                <el-select v-model="trendCategory" size="small" class="trend-category-select">
                  <el-option
                    v-for="cat in availableTrendCategories"
                    :key="cat.value"
                    :label="cat.label"
                    :value="cat.value"
                  />
                </el-select>
              </div>
            </div>
          </template>
          <el-skeleton v-if="trendLoading" :rows="4" animated />
          <div v-else style="min-height: 240px;">
            <LineChart
              v-if="trendSeries.length > 0"
              :series="trendSeries"
              :title="`${trendCategoryName}趋势`"
              height="300px"
            />
            <EmptyState
              v-else
              icon="TrendCharts"
              size="small"
              title="暂无趋势数据"
              description="需要设备数据才能显示趋势图表"
              :quick-actions="[
                { label: '查看设备', icon: Cpu, type: 'primary', handler: () => router.push('/edge-device') }
              ]"
            />
          </div>
        </el-card>
      </el-col>
    </el-row>

    <!-- 真实节点状态变化 -->
    <el-row :gutter="20" style="margin-top: 20px;">
      <el-col :span="24">
        <el-card shadow="hover">
          <template #header>
            <div style="display: flex; justify-content: space-between; align-items: center;">
              <span>节点状态变化</span>
              <span class="status-history-hint">最近 {{ statusHistory.length }} 条真实状态事件</span>
            </div>
          </template>
          <el-timeline v-if="statusHistory.length > 0">
            <el-timeline-item
              v-for="event in statusHistory"
              :key="event.id"
              :timestamp="formatTime(event.created_at)"
              :type="event.new_status === 'online' ? 'success' : 'warning'"
            >
              <strong>{{ event.node_name || event.node_id }}</strong>
              <el-tag size="small" :type="event.new_status === 'online' ? 'success' : 'info'" style="margin-left: 8px;">
                {{ event.new_status === 'online' ? '在线' : '离线' }}
              </el-tag>
            </el-timeline-item>
          </el-timeline>
          <EmptyState
            v-else
            icon="Connection"
            size="small"
            title="暂无状态变化记录"
            description="节点首次上线、恢复或离线后，状态变化会显示在这里。"
          />
        </el-card>
      </el-col>
    </el-row>

    <!-- 最新数据表格（同时显示原始设备数据和解析后数据） -->
    <el-row :gutter="20" style="margin-top: 20px;">
      <el-col :span="24">
        <el-card shadow="hover">
          <template #header>
            <div style="display: flex; justify-content: space-between; align-items: center;">
              <span>最新数据</span>
              <el-button size="small" @click="handleRefresh" :loading="refreshing">
                <el-icon><Refresh /></el-icon>
                刷新
              </el-button>
            </div>
          </template>
          <el-skeleton v-if="loading" :rows="5" animated />
          <el-table v-else-if="(overview.latest_data || []).length > 0" :data="overview.latest_data" stripe>
            <el-table-column prop="device_name" label="设备名称" width="150">
              <template #default="{ row }">
                <router-link :to="`/edge-device/${row.device_id}`" class="device-link">
                  {{ row.device_name }}
                </router-link>
              </template>
            </el-table-column>
            <el-table-column prop="collector_name" label="所属采集器" width="150" />
            <!-- 解析后数据 -->
            <el-table-column label="解析数据" width="280">
              <template #default="{ row }">
                <span>{{ formatData(row.data) }}</span>
              </template>
            </el-table-column>
            <!-- 原始数据 -->
            <el-table-column label="原始数据" width="200">
              <template #default="{ row }">
                <span class="raw-data">{{ formatRawData(row.raw_data) }}</span>
              </template>
            </el-table-column>
            <el-table-column prop="collected_at" label="采集时间">
              <template #default="{ row }">
                <span>{{ formatTime(row.collected_at) }}</span>
              </template>
            </el-table-column>
            <el-table-column label="状态" width="100">
              <template #default="{ row }">
                <el-tag size="small" type="success">在线</el-tag>
              </template>
            </el-table-column>
          </el-table>
          <EmptyState
            v-else
            icon="FolderOpened"
            title="暂无数据"
            description="添加采集器和设备后，数据将在此处显示"
            :quick-actions="[
              { label: '查看采集器', icon: Connection, type: 'primary', handler: () => router.push('/node') },
              { label: '查看设备', icon: Cpu, handler: () => router.push('/edge-device') }
            ]"
          />
        </el-card>
      </el-col>
    </el-row>
  </div>
</template>

<script setup lang="ts">
import { defineAsyncComponent, ref, onMounted, onUnmounted, computed, watch } from 'vue'
import { useRouter } from 'vue-router'
import { Cpu, CircleCheck, Refresh, Connection, TrendCharts, WarningFilled } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import PageHeader from '@/components/common/PageHeader.vue'
import SkeletonCard from '@/components/common/SkeletonCard.vue'
import EmptyState from '@/components/common/EmptyState.vue'
// 异步拆分：LineChart（echarts 核心）渲染耗时且仅趋势区使用，
// 独立 chunk 延迟加载，避免阻塞仪表盘首屏。
const LineChart = defineAsyncComponent(() => import('@/components/charts/LineChart.vue'))
import { dataApi, type Overview } from '@/api/data'
import client from '@/api/client'
import { useWebSocketStore, type WebSocketMessage } from '@/stores/websocket'
import { WS_EVENT } from '@/events/events'
import { logger } from '@/utils/logger'
import { sensorNameMap, sensorUnitMap, SENSOR_ORDER } from '@/utils/sensor'

const router = useRouter()

const overview = ref<Overview>({
  nodes: { total: 0, online: 0, offline: 0 },
  edge_devices: { total: 0, online: 0, offline: 0 },
  latest_data: []
})

const loading = ref(true)
const refreshing = ref(false)
const wsStore = useWebSocketStore()

// 趋势图相关
const trendLoading = ref(false)
const trendCategory = ref('temperature')
const trendSeries = ref<any[]>([])
const statusHistory = ref<Array<{ id: number; node_id: string; node_name?: string; new_status: string; created_at: string }>>([])
const trendRange = ref<'1h' | '24h' | '7d'>('24h')
const trendRangeLabel = computed(() => {
  const map: Record<string, string> = { '1h': '最近 1 小时', '24h': '最近 24 小时', '7d': '最近 7 天' }
  return map[trendRange.value] || ''
})
const trendCategoryName = computed(() => {
  return sensorNameMap[trendCategory.value] || trendCategory.value
})

// 传感器映射已提取到 utils/sensor.ts (S6 fix)

// 缓存已见过的传感器类型集合，只增不减，避免 WS 数据更新导致选项忽多忽少
const seenCategoryKeys = ref<Set<string>>(new Set())

// 监听 latest_data 变化，累积已见过的传感器类型
watch(() => overview.value.latest_data, (newData) => {
  for (const item of (newData || [])) {
    if (item.data && typeof item.data === 'object') {
      for (const key of Object.keys(item.data)) {
        const val = item.data[key]
        if (typeof val === 'number' || (typeof val === 'object' && val !== null && 'value' in val)) {
          seenCategoryKeys.value.add(key)
        }
      }
    }
  }
}, { deep: true, immediate: true })

// 从缓存集合生成可用传感器类型列表；始终包含当前选中项，保证下拉显示中文标签而非字段 key
const availableTrendCategories = computed(() => {
  const knownOrder = SENSOR_ORDER
  const keys = [...seenCategoryKeys.value]
  if (trendCategory.value && !keys.includes(trendCategory.value)) {
    keys.push(trendCategory.value)
  }
  return keys.sort((a, b) => {
    const ia = knownOrder.indexOf(a)
    const ib = knownOrder.indexOf(b)
    if (ia !== -1 && ib !== -1) return ia - ib
    if (ia !== -1) return -1
    if (ib !== -1) return 1
    return a.localeCompare(b)
  }).map(key => ({
    value: key,
    label: sensorNameMap[key] || key
  }))
})


// 告警摘要计算属性
const offlineCollectors = computed(() => Math.max(0, (overview.value.nodes?.total || 0) - (overview.value.nodes?.online || 0)))
const offlineDevices = computed(() => Math.max(0, (overview.value.edge_devices?.total || 0) - (overview.value.edge_devices?.online || 0)))
const dataErrorCount = computed(() => {
  return (overview.value.latest_data || []).filter(d => (d.error_code && d.error_code > 0)).length
})
const hasAlerts = computed(() => offlineCollectors.value > 0 || offlineDevices.value > 0 || dataErrorCount.value > 0)

let unsubscribeStatus: (() => void) | null = null
let unsubscribeData: (() => void) | null = null
let trendRefreshTimer: ReturnType<typeof setInterval> | null = null

// 获取趋势数据
const fetchTrendData = async () => {
  trendLoading.value = true
  try {
    const endTime = new Date()
    const rangeMs: Record<string, number> = {
      '1h': 60 * 60 * 1000,
      '24h': 24 * 60 * 60 * 1000,
      '7d': 7 * 24 * 60 * 60 * 1000,
    }
    const startTime = new Date(endTime.getTime() - (rangeMs[trendRange.value] || rangeMs['24h']))

    const unitMap = sensorUnitMap
    const nameMap = sensorNameMap

    // 从概览数据中获取设备列表，逐个查询
    const deviceIds = overview.value.latest_data?.map(d => d.device_id).filter(Boolean) || []
    if (deviceIds.length === 0) {
      logger.warn('没有可用的设备数据，无法加载趋势图')
      trendSeries.value = []
      return
    }

    const series: any[] = []
    const promises = deviceIds.slice(0, 5).map(deviceId =>
      client.get<unknown, any>('/api/v1/unified-data/historical', {
        params: {
          device_pk: deviceId,
          category: trendCategory.value,
          start_time: startTime.toISOString(),
          end_time: endTime.toISOString(),
          max_points: 500
        }
      }).then(res => ({
        deviceId,
        deviceName: overview.value.latest_data?.find(d => d.device_id === deviceId)?.device_name || `设备${deviceId}`,
        data: Array.isArray(res) ? res : (res.data || [])
      }))
    )

    const results = await Promise.all(promises)
    for (const r of results) {
      if (r.data && r.data.length > 0) {
        series.push({
          name: r.deviceName,
          unit: unitMap[trendCategory.value] || '',
          data: r.data
            .filter((item: any) => {
              const t = item.timestamp || item.created_at
              return t && !t.startsWith('0001-01-01')
            })
            .map((item: any) => ({
              time: item.timestamp || item.created_at,
              value: item.value
            }))
            .sort((a: any, b: any) => a.time.localeCompare(b.time))
        })
      }
    }

    trendSeries.value = series
  } catch (error) {
    logger.error('获取趋势数据失败', { error: String(error) })
    trendSeries.value = []
  } finally {
    trendLoading.value = false
  }
}


const fetchStatusHistory = async () => {
  try {
    const response = await client.get<unknown, any>('/api/v1/nodes/status-history', { params: { limit: 20 } })
    const events = response?.data || response || []
    statusHistory.value = Array.isArray(events) ? events : []
  } catch (error) {
    logger.warn('获取节点状态历史失败', { error: String(error) })
    statusHistory.value = []
  }
}

// 处理状态更新 — 防抖：多个状态变更在1秒内只触发一次fetchOverview
let overviewDirty = false
let overviewDebounceTimer: ReturnType<typeof setTimeout> | null = null
const scheduleOverviewRefresh = () => {
  overviewDirty = true
  if (overviewDebounceTimer) clearTimeout(overviewDebounceTimer)
  overviewDebounceTimer = setTimeout(() => {
    if (overviewDirty) {
      overviewDirty = false
      fetchOverview(true)
    }
  }, 1000)
}

const handleStatusUpdate = (message: WebSocketMessage) => {
  logger.debug('状态更新', { payload: message.payload })

  if (message.payload?.collector_id || message.payload?.device_id || message.payload?.node_id) {
    scheduleOverviewRefresh()
    fetchStatusHistory()
  }
}

// 处理数据更新 — 不直接修改 latest_data，触发防抖刷新避免表格跳动
const handleDataUpdate = (message: WebSocketMessage) => {
  logger.debug('数据更新', { payload: message.payload })
  scheduleOverviewRefresh()
  // 有数据更新时也刷新趋势图(复用防抖)
  scheduleTrendRefresh()
}

// 趋势图防抖刷新 — WS数据到达时触发，2s防抖避免频繁刷新
let trendDirty = false
let trendDebounceTimer: ReturnType<typeof setTimeout> | null = null
const scheduleTrendRefresh = () => {
  trendDirty = true
  if (trendDebounceTimer) clearTimeout(trendDebounceTimer)
  trendDebounceTimer = setTimeout(() => {
    if (trendDirty && !trendLoading.value) {
      trendDirty = false
      fetchTrendData()
    }
  }, 2000)
}

const fetchOverview = async (silent = false) => {
  if (!silent) loading.value = true
  try {
    const data = await dataApi.getOverview()
    overview.value = data
  } catch (error: any) {
    if (!silent) ElMessage.error('获取概览数据失败')
  } finally {
    if (!silent) loading.value = false
  }
}

const handleRefresh = async () => {
  refreshing.value = true
  try {
    // Trend queries derive their device list from overview.latest_data, so refresh
    // the overview first to avoid issuing a trend request with stale devices.
    await fetchOverview()
    await Promise.all([fetchTrendData(), fetchStatusHistory()])
    ElMessage.success('数据已刷新')
  } catch (error) {
    ElMessage.error('刷新失败')
  } finally {
    refreshing.value = false
  }
}

const formatData = (data: Record<string, any>) => {
  if (!data) return '-'

  return Object.entries(data)
    .map(([key, value]) => {
      if (typeof value === 'object' && value !== null && 'value' in value) {
        const val = value.value
        const unit = value.unit || ''
        if (typeof val === 'number') {
          return `${key}: ${val.toFixed(2)} ${unit}`.trim()
        }
        return `${key}: ${val} ${unit}`.trim()
      }
      if (typeof value === 'number') {
        return `${key}: ${value.toFixed(2)}`
      }
      return `${key}: ${value}`
    })
    .join(', ')
}

const formatRawData = (rawData: any) => {
  if (!rawData) return '-'
  if (typeof rawData === 'string') {
    // hex-encoded raw bytes from backend
    if (/^[0-9a-f]+$/i.test(rawData)) {
      const truncated = rawData.length > 64 ? rawData.slice(0, 64) + '…' : rawData
      return truncated
    }
    try {
      const parsed = JSON.parse(rawData)
      return JSON.stringify(parsed).slice(0, 80)
    } catch {
      return rawData.slice(0, 80)
    }
  }
  return JSON.stringify(rawData).slice(0, 80)
}

const formatTime = (time: string | null | undefined) => {
  if (!time || time === '0001-01-01T00:00:00Z' || time === '1970-01-01T00:00:00Z') return '-'
  const date = new Date(time)
  if (isNaN(date.getTime()) || date.getFullYear() <= 1970) return '-'
  return date.toLocaleString('zh-CN')
}

watch(trendCategory, () => {
  fetchTrendData()
})

// M11 fix: Extract category keys as a flat string for stable watch comparison.
// The original availableTrendCategories computed returns new object references on every
// evaluation, causing the watch to fire on every overview update even when values are identical.
const availableCategoryKeys = computed(() => {
  return availableTrendCategories.value.map(c => c.value).join(',')
})

watch(availableCategoryKeys, (newKeys, oldKeys) => {
  const cats = availableTrendCategories.value
  if (cats.length > 0 && !cats.some(c => c.value === trendCategory.value)) {
    trendCategory.value = cats[0].value
  } else if (cats.length === 0) {
    trendSeries.value = []
  }
})

onMounted(async () => {
  await fetchOverview()
  await Promise.all([fetchTrendData(), fetchStatusHistory()])

  unsubscribeStatus = wsStore.subscribe(WS_EVENT.NODE_STATUS, handleStatusUpdate)
  unsubscribeData = wsStore.subscribe(WS_EVENT.DATA_UPDATE, handleDataUpdate)

  // 30秒自动刷新趋势图
  trendRefreshTimer = setInterval(() => {
    if (trendSeries.value.length > 0 && !trendLoading.value) {
      fetchTrendData()
    }
  }, 30000)
})

onUnmounted(() => {
  if (unsubscribeStatus) unsubscribeStatus()
  if (unsubscribeData) unsubscribeData()
  if (overviewDebounceTimer) {
    clearTimeout(overviewDebounceTimer)
    overviewDebounceTimer = null
  }
  if (trendDebounceTimer) {
    clearTimeout(trendDebounceTimer)
    trendDebounceTimer = null
  }
  if (trendRefreshTimer) {
    clearInterval(trendRefreshTimer)
    trendRefreshTimer = null
  }
})
</script>

<style scoped>
.dashboard {
  padding: 0;
}

.dashboard-stats {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 20px;
}

.stat-card {
  cursor: pointer;
  transition: transform 0.3s, box-shadow 0.3s;
}

.stat-card:hover {
  transform: translateY(-4px);
  box-shadow: var(--shadow-lg);
}
.stat-card:focus-visible {
  outline: 3px solid var(--el-color-primary);
  outline-offset: 2px;
}

.stat-content {
  display: flex;
  align-items: center;
  gap: 16px;
}

.stat-icon {
  width: 60px;
  height: 60px;
  border-radius: 8px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #fff;
}

.stat-info {
  flex: 1;
}

.stat-label {
  margin: 8px 0 0;
  font-size: 14px;
  color: var(--el-text-color-secondary);
  /* 中文标签防止逐字断行竖排 */
  word-break: keep-all;
  overflow-wrap: break-word;
}

.stat-value {
  margin: 0;
  font-size: 28px;
  font-weight: 600;
  color: var(--el-text-color-primary);
}

.status-history-hint {
  color: var(--el-text-color-secondary);
  font-size: 12px;
}

.device-link {
  color: var(--el-color-primary);
  text-decoration: none;
}

.device-link:hover {
  text-decoration: underline;
}

.raw-data {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  font-family: monospace;
}

.trend-card-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}

.trend-controls {
  display: flex;
  gap: 8px;
  align-items: center;
}

.trend-category-select {
  width: 140px;
}

.alert-list {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
}

.alert-ok {
  display: flex;
  align-items: center;
  gap: 8px;
  color: var(--el-text-color-secondary);
  font-size: 14px;
}

.alert-item {
  flex: 1 1 220px;
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 16px;
  background: var(--bg-color-secondary);
  border-radius: 8px;
  cursor: pointer;
  transition: all 0.2s;
}
.alert-item:hover {
  background: var(--el-color-danger-light-9);
  transform: translateX(2px);
}
.alert-value {
  font-size: 22px;
  font-weight: 600;
  color: var(--el-color-danger);
  line-height: 1.2;
}
.alert-label {
  font-size: 12px;
  color: var(--text-color-secondary);
  margin-top: 2px;
  /* 防止移动端窄宽下中文逐字竖排 */
  word-break: keep-all;
  overflow-wrap: break-word;
}

/* 移动端：单行 4 列纵向紧凑小卡，压低统计卡占高让位给内容区 */
@media (max-width: 768px) {
  .dashboard-stats {
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: 8px;
  }

  .stat-content {
    flex-direction: column;
    align-items: center;
    text-align: center;
    gap: 4px;
  }

  .stat-icon {
    width: 22px;
    height: 22px;
    border-radius: 6px;
    flex-shrink: 0;
  }

  .stat-icon :deep(.el-icon) {
    font-size: 13px;
  }

  .stat-info {
    min-width: 0;
    width: 100%;
  }

  .stat-label {
    font-size: 10px;
    line-height: 1.3;
    margin-top: 1px;
    max-height: 2.6em;
    overflow: hidden;
    word-break: keep-all;
    overflow-wrap: break-word;
  }

  .stat-value {
    font-size: 16px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    max-width: 100%;
  }

  :deep(.stat-card) {
    margin-bottom: 0;
  }

  :deep(.stat-card .el-card__body) {
    padding: 8px 4px;
  }

  /* 分段控件与指标下拉各自整行，避免"7天"折行、边框残缺 */
  .trend-controls {
    width: 100%;
    flex-direction: column;
    align-items: stretch;
  }

  .trend-controls :deep(.el-radio-group) {
    display: flex;
    width: 100%;
  }

  .trend-controls :deep(.el-radio-button) {
    flex: 1;
  }

  .trend-controls :deep(.el-radio-button__inner) {
    width: 100%;
    padding-left: 8px;
    padding-right: 8px;
    text-align: center;
  }

  .trend-category-select {
    width: 100%;
  }
}
</style>
