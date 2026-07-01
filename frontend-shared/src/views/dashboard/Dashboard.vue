<template>
  <div class="dashboard">
    <PageHeader title="仪表盘" />

    <template v-if="loading">
      <el-row :gutter="20">
        <el-col :span="6" v-for="i in 4" :key="i">
          <SkeletonCard variant="stat" :icon-size="48" animated />
        </el-col>
      </el-row>
    </template>
    <template v-else>
      <el-row :gutter="20">
        <el-col :span="6">
          <el-card shadow="hover" class="stat-card" @click="router.push('/node')">
            <div class="stat-content">
              <div class="stat-icon" style="color: var(--el-color-primary);">
                <el-icon :size="32"><Connection /></el-icon>
              </div>
              <div class="stat-info">
                <p class="stat-label">采集器总数</p>
                <p class="stat-value">{{ overview.nodes?.total || 0 }}</p>
              </div>
            </div>
          </el-card>
        </el-col>

        <el-col :span="6">
          <el-card shadow="hover" class="stat-card" @click="router.push('/node?status=online')">
            <div class="stat-content">
              <div class="stat-icon" style="color: var(--el-color-success);">
                <el-icon :size="32"><CircleCheck /></el-icon>
              </div>
              <div class="stat-info">
                <p class="stat-label">在线采集器</p>
                <p class="stat-value">{{ overview.nodes?.online || 0 }}</p>
              </div>
            </div>
          </el-card>
        </el-col>

        <el-col :span="6">
          <el-card shadow="hover" class="stat-card" @click="router.push('/edge-device')">
            <div class="stat-content">
              <div class="stat-icon" style="color: var(--el-color-warning);">
                <el-icon :size="32"><Cpu /></el-icon>
              </div>
              <div class="stat-info">
                <p class="stat-label">设备总数</p>
                <p class="stat-value">{{ overview.edge_devices?.total || 0 }}</p>
              </div>
            </div>
          </el-card>
        </el-col>

        <el-col :span="6">
          <el-card shadow="hover" class="stat-card" @click="router.push('/edge-device?status=online')">
            <div class="stat-content">
              <div class="stat-icon" style="color: var(--el-color-success);">
                <el-icon :size="32"><CircleCheck /></el-icon>
              </div>
              <div class="stat-info">
                <p class="stat-label">在线设备</p>
                <p class="stat-value">{{ overview.edge_devices?.online || 0 }}</p>
              </div>
            </div>
          </el-card>
        </el-col>
      </el-row>
    </template>

    <!-- 告警 / 异常摘要 -->
    <el-row v-if="overview && (offlineDevices > 0 || offlineCollectors > 0 || dataErrorCount > 0)" :gutter="20" style="margin-top: 20px;">
      <el-col :span="24">
        <el-card shadow="hover" class="alert-summary">
          <template #header>
            <div style="display: flex; align-items: center; gap: 8px;">
              <el-icon color="var(--el-color-warning)" :size="20"><WarningFilled /></el-icon>
              <span>异常摘要</span>
              <el-tag size="small" type="warning">需关注</el-tag>
            </div>
          </template>
          <el-row :gutter="16">
            <el-col v-if="offlineCollectors > 0" :span="8">
              <div class="alert-item" @click="router.push('/node?status=offline')">
                <el-icon color="var(--el-color-danger)" :size="28"><Connection /></el-icon>
                <div>
                  <div class="alert-value">{{ offlineCollectors }}</div>
                  <div class="alert-label">离线采集器</div>
                </div>
              </div>
            </el-col>
            <el-col v-if="offlineDevices > 0" :span="8">
              <div class="alert-item" @click="router.push('/edge-device?status=offline')">
                <el-icon color="var(--el-color-danger)" :size="28"><Cpu /></el-icon>
                <div>
                  <div class="alert-value">{{ offlineDevices }}</div>
                  <div class="alert-label">离线设备</div>
                </div>
              </div>
            </el-col>
            <el-col v-if="dataErrorCount > 0" :span="8">
              <div class="alert-item" @click="router.push('/data')">
                <el-icon color="var(--el-color-warning)" :size="28"><WarningFilled /></el-icon>
                <div>
                  <div class="alert-value">{{ dataErrorCount }}</div>
                  <div class="alert-label">采集错误（近 1h）</div>
                </div>
              </div>
            </el-col>
          </el-row>
        </el-card>
      </el-col>
    </el-row>

    <!-- 24h 趋势图表 -->
    <el-row :gutter="20" style="margin-top: 20px;">
      <el-col :span="24">
        <el-card shadow="hover">
          <template #header>
            <div style="display: flex; justify-content: space-between; align-items: center; flex-wrap: wrap; gap: 8px;">
              <span>{{ trendRangeLabel }}趋势</span>
              <div style="display: flex; gap: 8px; align-items: center;">
                <el-radio-group v-model="trendRange" size="small" @change="fetchTrendData">
                  <el-radio-button value="1h">1小时</el-radio-button>
                  <el-radio-button value="24h">24小时</el-radio-button>
                  <el-radio-button value="7d">7天</el-radio-button>
                </el-radio-group>
                <el-select v-model="trendCategory" size="small" style="width: 140px;">
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
          <div v-else style="min-height: 300px;">
            <LineChart
              v-if="trendSeries.length > 0"
              :series="trendSeries"
              :title="`${trendCategoryName}趋势`"
              height="300px"
            />
            <EmptyState
              v-else
              icon="TrendCharts"
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

    <!-- 设备状态时间线 -->
    <el-row :gutter="20" style="margin-top: 20px;">
      <el-col :span="24">
        <el-card shadow="hover">
          <template #header>
            <div style="display: flex; justify-content: space-between; align-items: center;">
              <span>采集器/设备状态变化</span>
              <div style="display: flex; align-items: center; gap: 8px;">
                <el-tag size="small" type="warning">模拟数据</el-tag>
                <el-tag size="small" type="info">最近 24 小时</el-tag>
              </div>
            </div>
          </template>
          <el-timeline v-if="statusTimeline.length > 0">
            <el-timeline-item
              v-for="item in statusTimeline"
              :key="item.id"
              :timestamp="formatTime(item.time)"
              :type="item.type"
              :hollow="item.type === 'warning'"
            >
              <p style="margin: 0; font-size: 14px;">
                <strong>{{ item.name }}</strong>
                <el-tag size="small" :type="item.status === 'online' ? 'success' : 'info'" style="margin-left: 8px;">
                  {{ item.status === 'online' ? '在线' : '离线' }}
                </el-tag>
              </p>
              <p v-if="item.type" style="margin: 4px 0 0; font-size: 12px; color: var(--el-text-color-secondary);">
                {{ item.collectorName ? `采集器: ${item.collectorName}` : '' }}
              </p>
            </el-timeline-item>
          </el-timeline>
          <el-empty v-else description="暂无状态变化记录" />
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
import { ref, onMounted, onUnmounted, computed, watch } from 'vue'
import { useRouter } from 'vue-router'
import { Cpu, CircleCheck, Refresh, Connection, TrendCharts, WarningFilled } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import PageHeader from '@/components/common/PageHeader.vue'
import SkeletonCard from '@/components/common/SkeletonCard.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import LineChart from '@/components/charts/LineChart.vue'
import { dataApi, type Overview } from '@/api/data'
import client from '@/api/client'
import { useWebSocketStore, type WebSocketMessage } from '@/stores/websocket'
import { WS_EVENT } from '@/events/events'
import { logger } from '@/utils/logger'
import { sensorNameMap, sensorUnitMap, SENSOR_ORDER } from '@/utils/sensor'
import { downsampleData } from '@/utils/downsample'

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

// 从缓存集合生成可用传感器类型列表
const availableTrendCategories = computed(() => {
  const knownOrder = SENSOR_ORDER
  return [...seenCategoryKeys.value].sort((a, b) => {
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

// 状态时间线
const statusTimeline = ref<any[]>([])

// 告警摘要计算属性
const offlineCollectors = computed(() => Math.max(0, (overview.value.nodes?.total || 0) - (overview.value.nodes?.online || 0)))
const offlineDevices = computed(() => Math.max(0, (overview.value.edge_devices?.total || 0) - (overview.value.edge_devices?.online || 0)))
const dataErrorCount = computed(() => {
  return (overview.value.latest_data || []).filter(d => (d.error_code && d.error_code > 0)).length
})

let unsubscribeStatus: (() => void) | null = null
let unsubscribeData: (() => void) | null = null

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
          end_time: endTime.toISOString()
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
          data: downsampleData(
            r.data
              .filter((item: any) => {
                const t = item.timestamp || item.created_at
                return t && !t.startsWith('0001-01-01')
              })
              .map((item: any) => ({
                time: item.timestamp || item.created_at,
                value: item.value
              }))
              .sort((a: any, b: any) => a.time.localeCompare(b.time))
          )
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

// 获取状态变化时间线
const fetchStatusTimeline = async () => {
  try {
    // 从 unified_data 的设备列表获取基本信息构建简单时间线
    // 实际应该从日志表获取，这里用概览数据模拟
    const devices = overview.value.latest_data || []
    const timeline: any[] = []
    const now = Date.now()

    for (const item of devices.slice(0, 10)) {
      timeline.push({
        id: item.device_id,
        name: item.device_name,
        collectorName: item.collector_name,
        status: 'online',
        time: item.collected_at || new Date(now).toISOString(),
        type: 'success'
      })
    }
    statusTimeline.value = timeline
  } catch (error) {
    statusTimeline.value = []
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

  if (message.payload?.collector_id || message.payload?.device_id) {
    scheduleOverviewRefresh()
  }
}

// 处理数据更新 — 不直接修改 latest_data，触发防抖刷新避免表格跳动
const handleDataUpdate = (message: WebSocketMessage) => {
  logger.debug('数据更新', { payload: message.payload })
  scheduleOverviewRefresh()
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
    await fetchOverview()
    await fetchTrendData()
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
  await fetchTrendData()
  await fetchStatusTimeline()

  unsubscribeStatus = wsStore.subscribe(WS_EVENT.NODE_STATUS, handleStatusUpdate)
  unsubscribeData = wsStore.subscribe(WS_EVENT.DATA_UPDATE, handleDataUpdate)
})

onUnmounted(() => {
  if (unsubscribeStatus) unsubscribeStatus()
  if (unsubscribeData) unsubscribeData()
  if (overviewDebounceTimer) {
    clearTimeout(overviewDebounceTimer)
    overviewDebounceTimer = null
  }
})
</script>

<style scoped>
.dashboard {
  padding: 0;
}

.stat-card {
  cursor: pointer;
  transition: transform 0.3s, box-shadow 0.3s;
}

.stat-card:hover {
  transform: translateY(-4px);
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.15);
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
  margin: 0 0 8px;
  font-size: 14px;
  color: var(--el-text-color-secondary);
}

.stat-value {
  margin: 0;
  font-size: 28px;
  font-weight: 600;
  color: var(--el-text-color-primary);
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

.alert-item {
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
}

@media (max-width: 768px) {
  :deep(.stat-card) {
    margin-bottom: 8px;
  }
  :deep(.alert-item) {
    margin-bottom: 8px;
  }
}
</style>