<template>
  <div class="data-panel">
    <PageHeader title="数据面板" />

    <el-card>
      <el-form :inline="true" :model="queryForm">
        <el-form-item label="设备">
          <el-select
            v-model="queryForm.deviceId"
            placeholder="请选择设备"
            clearable
            filterable
            style="width: 200px;"
          >
            <el-option
              v-for="device in deviceList"
              :key="device.id"
              :label="device.name"
              :value="device.id"
            />
          </el-select>
        </el-form-item>
        <el-form-item label="时间范围">
          <el-select
            v-model="queryForm.timeRange"
            placeholder="请选择时间范围"
            style="width: 150px;"
          >
            <el-option label="最近1小时" value="1h" />
            <el-option label="最近24小时" value="24h" />
            <el-option label="最近7天" value="7d" />
            <el-option label="最近30天" value="30d" />
          </el-select>
        </el-form-item>
        <el-form-item>
          <el-button type="primary" @click="fetchData" :loading="loading">查询</el-button>
          <el-tooltip content="请先查询数据" placement="top" :disabled="canExport">
            <span>
              <el-button @click="handleExport" :disabled="!canExport">
                <el-icon><Download /></el-icon>
                导出CSV
              </el-button>
            </span>
          </el-tooltip>
        </el-form-item>
      </el-form>
    </el-card>

    <!-- 统计概览卡片 -->
    <div v-if="queryForm.deviceId && historyData.length > 0" class="data-stats">
      <el-card v-for="stat in dynamicStats" :key="stat.code" class="stat-card" shadow="hover">
        <div class="stat-content">
          <div class="stat-icon">
            <el-icon><DataAnalysis /></el-icon>
          </div>
          <div class="stat-info">
            <p class="stat-label">{{ stat.label }}</p>
            <p class="stat-value">{{ stat.value }}<span v-if="stat.unit" class="stat-unit">{{ stat.unit }}</span></p>
          </div>
        </div>
      </el-card>
      <el-card class="stat-card" shadow="hover">
        <div class="stat-content">
          <div class="stat-icon"><el-icon><DocumentChecked /></el-icon></div>
          <div class="stat-info">
            <p class="stat-label">本次数据点</p>
            <p class="stat-value">{{ total }}</p>
          </div>
        </div>
      </el-card>
      <el-card class="stat-card" shadow="hover">
        <div class="stat-content">
          <div class="stat-icon"><el-icon><Timer /></el-icon></div>
          <div class="stat-info">
            <p class="stat-label">采集覆盖时长</p>
            <p class="stat-value">{{ latestStats.duration }}</p>
          </div>
        </div>
      </el-card>
    </div>

    <!-- 实时数据开关 -->
    <el-card style="margin-top: 20px;" v-if="queryForm.deviceId">
      <template #header>
        <div style="display: flex; justify-content: space-between; align-items: center;">
          <span>实时数据</span>
          <el-switch
            v-model="realtimeEnabled"
            active-text="开启"
            inactive-text="关闭"
            @change="toggleRealtime"
          />
        </div>
      </template>
      <div class="realtime-indicator" v-if="realtimeEnabled">
        <el-tag type="success" size="small">
          <el-icon><Connection /></el-icon>
          实时接收中
        </el-tag>
        <span class="realtime-count">共 {{ realtimeCount }} 条</span>
      </div>
      <el-empty v-else description="点击开关开启实时数据接收" />
    </el-card>

    <!-- 历史趋势图表 -->
    <el-card style="margin-top: 20px;" v-if="queryForm.deviceId && historyData.length > 0">
      <template #header>
        <span>历史数据趋势</span>
      </template>
      <div style="min-height: 350px;">
        <LineChart
          v-if="chartSeries.length > 0"
          :series="chartSeries"
          :realtime="realtimeEnabled"
          title="历史数据趋势"
          height="350px"
        />
        <el-empty v-else description="暂无趋势数据" />
      </div>
    </el-card>

    <!-- 多设备对比 -->
    <el-card style="margin-top: 20px;" v-if="compareMode">
      <template #header>
        <div style="display: flex; justify-content: space-between; align-items: center;">
          <span>多设备对比</span>
          <el-button size="small" @click="compareMode = false">关闭对比</el-button>
        </div>
      </template>
      <el-checkbox-group v-model="compareDevices" :min="2">
        <el-checkbox
          v-for="device in deviceList"
          :key="device.id"
          :value="device.id"
          :label="device.name"
          style="margin-right: 16px;"
        />
      </el-checkbox-group>
      <div v-if="compareDevices.length >= 2" style="margin-top: 12px; display: flex; align-items: center; gap: 12px;">
        <span style="font-size: 14px; color: var(--el-text-color-regular);">对比类别：</span>
        <el-select v-model="queryForm.compareCategory" size="small" style="width: 180px;" :disabled="compareCategories.length === 0">
          <el-option
            v-for="category in compareCategories"
            :key="category.code"
            :label="`${sensorNameMap[category.code] || category.code}${category.unit ? ` (${category.unit})` : ''}`"
            :value="category.code"
          />
        </el-select>
        <span v-if="compareCategories.length === 0" class="compare-category-hint">所选设备没有可安全比较的同单位指标</span>
      </div>
      <div v-if="compareDevices.length >= 2" style="margin-top: 20px; min-height: 300px;">
        <LineChart
          v-if="compareSeries.length > 0"
          :series="compareSeries"
          title="多设备对比"
          height="300px"
        />
      </div>
      <el-empty v-else description="请选择至少2个设备进行对比" />
    </el-card>

    <EmptyState
      v-if="!queryForm.deviceId"
      kind="initial"
      icon="Cpu"
      title="选择设备后查看数据"
      description="选择设备和时间范围后，可查看历史趋势与实时数据。"
      :quick-actions="[
        { label: '管理边缘设备', icon: Cpu, type: 'primary', handler: () => router.push('/edge-device') }
      ]"
    />

    <!-- 历史数据表格（未选择设备时由上方引导空状态接管，整个卡片不渲染） -->
    <el-card style="margin-top: 20px;" v-if="queryForm.deviceId">
      <template #header>
        <div style="display: flex; justify-content: space-between; align-items: center;">
          <span>历史数据</span>
          <el-button
            v-if="!compareMode && deviceList.length > 1"
            size="small"
            @click="compareMode = true"
          >
            多设备对比
          </el-button>
        </div>
      </template>
      <el-skeleton v-if="loading" :rows="5" animated />
      <template v-else>
        <EmptyState
          v-if="!historyData || historyData.length === 0"
          kind="empty"
          icon="DataAnalysis"
          title="该时间范围内暂无数据"
          description="可调整时间范围，或确认设备已完成采集与同步。"
        />
        <el-table v-else :data="historyData" stripe>
          <el-table-column prop="created_at" label="采集时间" width="180">
            <template #default="{ row }">
              <span>{{ formatTime(row.timestamp || row.created_at || row.collected_at) }}</span>
            </template>
          </el-table-column>
          <el-table-column prop="data" label="数据">
            <template #default="{ row }">
              <span>{{ formatData(row.parsed_data || row.data) }}</span>
            </template>
          </el-table-column>
          <el-table-column label="原始数据" width="120" align="center">
            <template #default="{ row }">
              <span v-if="row.raw_data" style="font-size: 12px; color: var(--el-text-color-secondary);">{{ formatRawData(row.raw_data) }}</span>
              <span v-else style="color: var(--el-text-color-placeholder);">-</span>
            </template>
          </el-table-column>
          <el-table-column label="状态" width="100" align="center">
            <template #default="{ row }">
              <el-tag v-if="row.error_code && row.error_code > 0"
                      :type="getErrorInfo(row.error_code).type"
                      size="small">
                {{ getErrorInfo(row.error_code).label }}
              </el-tag>
              <span v-else style="color: var(--el-color-success);">正常</span>
            </template>
          </el-table-column>
        </el-table>

        <el-pagination
          v-model:current-page="currentPage"
          v-model:page-size="pageSize"
          :total="total"
          :page-sizes="[20, 50, 100]"
          layout="total, sizes, prev, pager, next, jumper"
          style="margin-top: 20px; justify-content: flex-end;"
          @current-change="fetchData"
          @size-change="fetchData"
        />
      </template>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, onUnmounted, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { Download, Connection, DataAnalysis, DocumentChecked, Timer, Cpu } from '@element-plus/icons-vue'
import PageHeader from '@/components/common/PageHeader.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import LineChart from '@/components/charts/LineChart.vue'
import { edgeDeviceApi, type EdgeDevice } from '@/api/edgeDevice'
import { useEdgeDeviceStore } from '@/stores/edgeDevice'
import client from '@/api/client'
import { getErrorInfo } from '@/utils/errorCode'
import { useWebSocketStore, type WebSocketMessage } from '@/stores/websocket'
import { WS_EVENT } from '@/events/events'
import { exportCSV, exportJSON } from '@/utils/exportData'
import feedback from '@/utils/feedback'
import { logger } from '@/utils/logger'
import { sensorNameMap, sensorUnitMap } from '@/utils/sensor'

const router = useRouter()
const deviceList = ref<Device[]>([])
const historyData = ref<any[]>([])
const chartSeries = ref<any[]>([])
const loading = ref(false)
const currentPage = ref(1)
const pageSize = ref(20)
const total = ref(0)

// 实时数据
const realtimeEnabled = ref(false)
const realtimeCount = ref(0)
const realtimeData = ref<any[]>([])

// 多设备对比
const compareMode = ref(false)
const compareDevices = ref<number[]>([])
const compareSeries = ref<any[]>([])
interface MeasurementCategory {
  code: string
  unit: string
}
interface MeasurementPoint {
  timestamp?: string
  created_at?: string
  value: number
}
interface MeasurementBatch {
  category: string
  data: MeasurementPoint[]
}
interface RealtimeDataPayload {
  device_id?: number
  collected_at?: string
  data?: Record<string, unknown>
  sensors?: Record<string, unknown>
}
const availableCategories = ref<MeasurementCategory[]>([])
const compareCategories = ref<MeasurementCategory[]>([])

// 统计概览数据
const latestStats = reactive({
  totalPoints: 0,
  duration: '--'
})

const extractNumericValue = (value: unknown): number | null => {
  if (typeof value === 'number') return value
  if (typeof value === 'object' && value !== null && 'value' in value) {
    const numeric = (value as { value?: unknown }).value
    return typeof numeric === 'number' ? numeric : null
  }
  return null
}

const dynamicStats = computed(() => {
  const latest = [...historyData.value]
    .sort((a, b) => String(b.timestamp || b.created_at || b.collected_at || '').localeCompare(String(a.timestamp || a.created_at || a.collected_at || '')))[0]
  const source = latest?.parsed_data || latest?.data || {}
  return availableCategories.value
    .map((category) => {
      const value = extractNumericValue(source[category.code])
      if (value === null) return null
      return {
        code: category.code,
        label: `最新${sensorNameMap[category.code] || category.code}`,
        unit: category.unit || sensorUnitMap[category.code] || '',
        value: Number.isInteger(value) ? value : Number(value.toFixed(2)),
      }
    })
    .filter((stat): stat is { code: string; label: string; unit: string; value: number } => stat !== null)
    .slice(0, 3)
})

const calculateStats = () => {
  const items = historyData.value
  latestStats.totalPoints = total.value
  if (!items || items.length === 0) {
    latestStats.duration = '--'
    return
  }

  const times = items
    .map(item => item.timestamp || item.created_at || item.collected_at)
    .filter(Boolean)
    .map(t => new Date(t).getTime())
    .filter(t => !Number.isNaN(t))
    .sort((a, b) => a - b)
  if (times.length < 2) {
    latestStats.duration = '< 1分钟'
    return
  }

  const diffMin = Math.floor((times[times.length - 1] - times[0]) / 60000)
  if (diffMin < 60) {
    latestStats.duration = `${diffMin}分钟`
  } else if (diffMin < 1440) {
    latestStats.duration = `${Math.floor(diffMin / 60)}小时${diffMin % 60}分钟`
  } else {
    latestStats.duration = `${Math.floor(diffMin / 1440)}天${Math.floor((diffMin % 1440) / 60)}小时`
  }
}

const queryForm = reactive({
  deviceId: null as number | null,
  timeRange: '24h',
  compareCategory: 'temperature' as string
})

const canExport = computed(() => !!queryForm.deviceId && historyData.value.length > 0)

const wsStore = useWebSocketStore()
const edgeDeviceStore = useEdgeDeviceStore()
let unsubscribeData: (() => void) | null = null

const loadDeviceCategories = async () => {
  if (!queryForm.deviceId) {
    availableCategories.value = []
    return
  }
  try {
    const response = await client.get<unknown, MeasurementCategory[]>('/api/v1/unified-data/categories', {
      params: { device_pk: queryForm.deviceId },
    })
    availableCategories.value = Array.isArray(response)
      ? response
        .filter((item): item is MeasurementCategory => typeof item?.code === 'string' && item.code.length > 0)
        .map(item => ({ code: item.code, unit: item.unit || sensorUnitMap[item.code] || '' }))
      : []
  } catch (error) {
    logger.warn('获取设备指标类别失败', { error: String(error) })
    availableCategories.value = []
  }
}

const loadCompareCategories = async () => {
  if (compareDevices.value.length < 2) {
    compareCategories.value = []
    return
  }
  try {
    const categoryLists = await Promise.all(compareDevices.value.map(async (deviceId) => {
      const response = await client.get<unknown, MeasurementCategory[]>('/api/v1/unified-data/categories', { params: { device_pk: deviceId } })
      return Array.isArray(response) ? response : []
    }))
    const [first, ...rest] = categoryLists
    compareCategories.value = (first || []).filter((candidate: MeasurementCategory) =>
      rest.every(list => list.some((item: MeasurementCategory) => item.code === candidate.code && item.unit === candidate.unit)),
    )
    if (!compareCategories.value.some(category => category.code === queryForm.compareCategory)) {
      queryForm.compareCategory = compareCategories.value[0]?.code || ''
    }
  } catch (error) {
    logger.warn('获取可比较指标失败', { error: String(error) })
    compareCategories.value = []
    queryForm.compareCategory = ''
  }
}

const fetchDevices = async () => {
  try {
    const params = { page: 1, page_size: 500 }
    await edgeDeviceStore.fetchList(params)
    deviceList.value = edgeDeviceStore.getCachedList(params)?.items || []
  } catch {
    ElMessage.error('获取设备列表失败')
  }
}

const fetchData = async () => {
  if (!queryForm.deviceId) {
    ElMessage.warning('请先选择设备')
    return
  }

  loading.value = true
  try {
    const endTime = new Date()
    const startTime = new Date()

    switch (queryForm.timeRange) {
      case '1h':
        startTime.setHours(startTime.getHours() - 1)
        break
      case '24h':
        startTime.setHours(startTime.getHours() - 24)
        break
      case '7d':
        startTime.setDate(startTime.getDate() - 7)
        break
      case '30d':
        startTime.setDate(startTime.getDate() - 30)
        break
    }

    const response = await edgeDeviceApi.getHistoryData(queryForm.deviceId, {
      start_time: startTime.toISOString(),
      end_time: endTime.toISOString(),
      page: currentPage.value,
      page_size: pageSize.value
    })

    historyData.value = response.items || []
    total.value = response.total || 0

    await loadDeviceCategories()
    calculateStats()

    // 构建图表数据：从 unified_data API 获取解析后的数值数据
    buildChartSeries()
  } catch {
    ElMessage.error('获取历史数据失败')
  } finally {
    loading.value = false
  }
}

const buildChartSeries = async () => {
  if (historyData.value.length === 0) {
    chartSeries.value = []
    return
  }

  // 尝试从 unified_data API 获取解析后的数值数据
  try {
    const endTime = new Date()
    const startTime = new Date()
    switch (queryForm.timeRange) {
      case '1h':
        startTime.setHours(startTime.getHours() - 1)
        break
      case '24h':
        startTime.setHours(startTime.getHours() - 24)
        break
      case '7d':
        startTime.setDate(startTime.getDate() - 7)
        break
      case '30d':
        startTime.setDate(startTime.getDate() - 30)
        break
    }

    const categoryNames = availableCategories.value.map(category => category.code)
    if (categoryNames.length === 0) {
      chartSeries.value = []
      return
    }

    const series: any[] = []

    // 批量请求：1 个请求替代 19 个并行请求，服务端降采样 max_points=500
    const batchParams = {
      device_pk: queryForm.deviceId,
      categories: categoryNames.join(','),
      start_time: startTime.toISOString(),
      end_time: endTime.toISOString(),
      max_points: 500
    }

    try {
      const batchRes = await client.get<unknown, MeasurementBatch[]>('/api/v1/unified-data/historical-batch', {
        params: batchParams
      })
      // 批量 API 返回格式: [{category: "temperature", data: [{...}]}, ...]
      if (Array.isArray(batchRes)) {
        for (const result of batchRes) {
          const cat = result.category
          if (!cat) continue
          const items = (result.data || []).filter((item) => {
            const t = item.timestamp || item.created_at
            return t && !t.startsWith('0001-01-01')
          })
          if (items.length > 0) {
            const catName = sensorNameMap[cat] || cat
            const catUnit = sensorUnitMap[cat] || ''
            series.push({
              name: catName,
              unit: catUnit,
              category: cat,
              data: items.map((item) => ({
                time: item.timestamp || item.created_at,
                value: item.value
              }))
            })
          }
        }
      }

      // 如果批量 API 未返回任何数据，尝试 fallback 逐个请求
      if (series.length === 0) {
        throw new Error('batch API returned no data, falling back')
      }
    } catch {
      // Fallback: 批量 API 失败时逐个请求 + max_points=500
      const fallbackPromises = categoryNames.map(cat =>
        client.get<unknown, MeasurementPoint[]>('/api/v1/unified-data/historical', {
          params: {
            device_pk: queryForm.deviceId,
            category: cat,
            start_time: startTime.toISOString(),
            end_time: endTime.toISOString(),
            max_points: 500
          }
        }).then(res => ({ cat, data: Array.isArray(res) ? res : [] }))
          .catch(() => ({ cat, data: [] as MeasurementPoint[] }))
      )

      const fallbackResults = await Promise.all(fallbackPromises)
      for (const r of fallbackResults) {
        if (r.data && r.data.length > 0) {
          const filteredData = r.data
            .filter((item) => {
              const t = item.timestamp || item.created_at
              return t && !t.startsWith('0001-01-01')
            })
          if (filteredData.length > 0) {
            const catName = sensorNameMap[r.cat] || r.cat
            const catUnit = sensorUnitMap[r.cat] || ''
            series.push({
              name: catName,
              unit: catUnit,
              category: r.cat,
              data: filteredData.map((item) => ({
                time: item.timestamp || item.created_at,
                value: item.value
              }))
            })
          }
        }
      }
    }

    chartSeries.value = series
  } catch (error) {
    logger.error('获取趋势数据失败', { error: String(error) })
    // Fallback: 尝试从 device_data.data 提取数值字段
    const firstItem = historyData.value[0]
    const data = firstItem.data || {}
    const numericKeys = Object.keys(data).filter(key => key !== 'raw_data' && typeof data[key] === 'number')

    if (numericKeys.length > 0) {
      chartSeries.value = numericKeys.map(key => ({
        name: key,
        unit: '',
        category: key,
        data: historyData.value
          .filter((item) => {
            const t = item.timestamp || item.collected_at || item.created_at
            return t && !t.startsWith('0001-01-01')
          })
          .map(item => ({ time: item.timestamp || item.collected_at || item.created_at, value: item.data?.[key] ?? 0 }))
      }))
    } else {
      chartSeries.value = []
    }
  }
}

// 实时数据处理
// 性能优化：防抖 calculateStats，避免每条WS消息都触发重计算
let statsDirty = false
let debounceTimer: ReturnType<typeof setTimeout> | null = null
const DEBOUNCE_MS = 2000  // 2秒内多条消息只触发一次重算

const flushDebounced = () => {
  if (statsDirty) {
    statsDirty = false
    calculateStats()
  }
}

const scheduleDebounced = () => {
  if (debounceTimer) clearTimeout(debounceTimer)
  debounceTimer = setTimeout(flushDebounced, DEBOUNCE_MS)
}

const handleDataUpdate = (message: WebSocketMessage) => {
  const payload = message.payload as RealtimeDataPayload | undefined
  if (!payload || payload.device_id !== queryForm.deviceId) return

  realtimeCount.value++
  const newItem = {
    collected_at: payload.collected_at || new Date().toISOString(),
    data: payload.data || {},
    error_code: 0
  }

  // 添加到实时数据列表
  realtimeData.value.unshift(newItem)
  if (realtimeData.value.length > 50) {
    realtimeData.value = realtimeData.value.slice(0, 50)
  }

  // 同时更新历史数据表格（添加到顶部）
  historyData.value.unshift(newItem)
  if (historyData.value.length > pageSize.value) {
    historyData.value = historyData.value.slice(0, pageSize.value)
  }
  total.value++

  // 标记stats为脏（统计计算），但不标记chartDirty
  statsDirty = true
  scheduleDebounced()

  // 增量更新图表 — 直接append到现有series
  appendRealtimeData(payload)
}

// 新增函数: 增量更新图表series
const appendRealtimeData = (payload: RealtimeDataPayload) => {
  if (chartSeries.value.length === 0) return
  const data = payload.data || payload.sensors
  if (!data || typeof data !== 'object') return
  const time = payload.collected_at || new Date().toISOString()

  let updated = false
  for (const series of chartSeries.value) {
    const key = series.category
    if (key && data[key] !== undefined && typeof data[key] === 'number') {
      series.data.push({ time, value: data[key] })
      // 限制长度
      if (series.data.length > 500) {
        series.data = series.data.slice(-500)
      }
      updated = true
    }
  }
  // 触发响应式更新 — 重新赋值引用
  if (updated) {
    chartSeries.value = [...chartSeries.value]
  }
}

const toggleRealtime = (enabled: boolean) => {
  if (enabled) {
    // 订阅实时数据
    unsubscribeData = wsStore.subscribe(WS_EVENT.DATA_UPDATE, handleDataUpdate)
    realtimeCount.value = 0
    realtimeData.value = []
    ElMessage.success('已开启实时数据接收')
  } else {
    if (unsubscribeData) {
      unsubscribeData()
      unsubscribeData = null
    }
    ElMessage.info('已关闭实时数据接收')
  }
}

// 导出 CSV
const handleExport = () => {
  if (!queryForm.deviceId) return

  const device = deviceList.value.find(d => d.id === queryForm.deviceId)
  const deviceName = device?.name || queryForm.deviceId

  const rows = [['时间', '数据(JSON)', '错误码']]
  for (const item of historyData.value) {
    rows.push([
      formatTime(item.collected_at),
      JSON.stringify(item.data || {}),
      String(item.error_code || 0)
    ])
  }

  const csvContent = rows.map(row => row.map(cell => `"${cell}"`).join(',')).join('\n')
  const blob = new Blob(['\ufeff' + csvContent], { type: 'text/csv;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = `${deviceName}_${queryForm.timeRange}.csv`
  link.click()
  URL.revokeObjectURL(url)

  ElMessage.success('导出成功')
}

// 获取多设备对比数据
const fetchCompareData = async () => {
  if (compareDevices.value.length < 2) return

  compareSeries.value = []
  const endTime = new Date()
  const startTime = new Date(endTime.getTime() - 24 * 60 * 60 * 1000) // 固定 24h

  try {
    const promises = compareDevices.value.map(async (deviceId) => {
      const response = await client.get<unknown, MeasurementPoint[]>(`/api/v1/unified-data/historical`, {
        params: {
          device_pk: deviceId,
          category: queryForm.compareCategory,
          start_time: startTime.toISOString(),
          end_time: endTime.toISOString(),
          max_points: 500
        }
      })
      return {
        deviceId,
        deviceName: deviceList.value.find(d => d.id === deviceId)?.name || String(deviceId),
        data: Array.isArray(response) ? response : []
      }
    })

    const results = await Promise.all(promises)

    compareSeries.value = results.map(r => ({
      name: r.deviceName,
      unit: '',
      data: r.data
        .filter((item) => {
          const t = item.timestamp || item.created_at
          return t && !t.startsWith('0001-01-01')
        })
        .map((item) => ({
          time: item.timestamp || item.created_at,
          value: item.value
        }))
    }))
  } catch (error) {
    logger.error('获取对比数据失败', { error: String(error) })
  }
}

const formatTime = (time: string) => {
  return time ? new Date(time).toLocaleString('zh-CN') : '-'
}

const formatData = (data: Record<string, unknown>) => {
  if (!data) return '-'

  // Filter out raw_data from display (shown in separate column)
  const entries = Object.entries(data).filter(([key]) => key !== 'raw_data')
  if (entries.length === 0) return '-'

  return entries
    .map(([key, value]) => {
      // 支持 {value, unit} 格式
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

const formatRawData = (rawData: string | Record<string, any>) => {
  if (!rawData) return '-'
  // raw_data may be a hex string from data field, or base64 from raw_data field
  if (typeof rawData === 'string') {
    // Detect hex pattern (all lowercase hex chars)
    if (/^[0-9a-f]+$/.test(rawData)) {
      const bytes = rawData.length / 2
      return `${bytes}B hex`
    }
    // base64 or other
    return rawData.length > 20 ? rawData.substring(0, 20) + '...' : rawData
  }
  return JSON.stringify(rawData)
}

watch(compareDevices, async () => {
  if (compareMode.value && compareDevices.value.length >= 2) {
    await loadCompareCategories()
    if (queryForm.compareCategory) await fetchCompareData()
  }
})

watch(() => queryForm.compareCategory, () => {
  if (compareMode.value && compareDevices.value.length >= 2) {
    fetchCompareData()
  }
})

onMounted(() => {
  fetchDevices()
})

onUnmounted(() => {
  if (unsubscribeData) {
    unsubscribeData()
  }
  if (debounceTimer) {
    clearTimeout(debounceTimer)
    debounceTimer = null
  }
})
</script>

<style scoped>
.data-panel {
  padding: 0;
}

.data-stats {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(190px, 1fr));
  gap: 16px;
  margin-top: 20px;
}

:deep(.el-pagination) {
  display: flex;
  justify-content: flex-end;
}

.realtime-indicator {
  display: flex;
  align-items: center;
  gap: 12px;
}

.realtime-count {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.stat-card {
  cursor: default;
  transition: transform 0.3s, box-shadow 0.3s;
}

.stat-card:hover {
  transform: translateY(-2px);
  box-shadow: var(--shadow-md);
}

.stat-content {
  display: flex;
  align-items: center;
  gap: 12px;
}

.stat-icon {
  width: 48px;
  height: 48px;
  border-radius: 10px;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  background: var(--el-color-primary-light-9);
  color: var(--el-color-primary);
}

.stat-info {
  flex: 1;
  min-width: 0;
}

.stat-label {
  margin: 0 0 4px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.stat-value {
  margin: 0;
  font-size: 22px;
  font-weight: 600;
  color: var(--el-text-color-primary);
}

.stat-unit {
  font-size: 13px;
  font-weight: 400;
  color: var(--el-text-color-secondary);
  margin-left: 2px;
}

.compare-category-hint {
  color: var(--el-text-color-secondary);
  font-size: 12px;
}

@media (max-width: 768px) {
  .data-stats {
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 12px;
  }

  :deep(.el-form--inline) {
    display: flex;
    flex-direction: column;
  }

  :deep(.el-form--inline .el-form-item) {
    margin-right: 0;
  }

  :deep(.el-form--inline .el-form-item__content),
  :deep(.el-form--inline .el-select) {
    width: 100%;
  }

  .realtime-indicator {
    flex-wrap: wrap;
  }
}
</style>