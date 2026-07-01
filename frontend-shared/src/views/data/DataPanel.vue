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
          <el-button @click="handleExport" :disabled="!queryForm.deviceId || !historyData || historyData.length === 0">
            <el-icon><Download /></el-icon>
            导出CSV
          </el-button>
        </el-form-item>
      </el-form>
    </el-card>

    <!-- 统计概览卡片 -->
    <el-row :gutter="16" style="margin-top: 20px;" v-if="queryForm.deviceId && historyData.length > 0">
      <el-col :span="6">
        <el-card class="stat-card" shadow="hover">
          <div class="stat-content">
            <div class="stat-icon" style="background: linear-gradient(135deg, var(--el-color-danger), var(--el-color-warning));">
              <span class="stat-icon-text">🌡️</span>
            </div>
            <div class="stat-info">
              <p class="stat-label">最新温度</p>
              <p class="stat-value">{{ latestStats.temperature ?? '--' }}<span v-if="latestStats.temperature" class="stat-unit">°C</span></p>
            </div>
          </div>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card class="stat-card" shadow="hover">
          <div class="stat-content">
            <div class="stat-icon" style="background: linear-gradient(135deg, var(--el-color-primary), var(--el-color-success));">
              <span class="stat-icon-text">🌊</span>
            </div>
            <div class="stat-info">
              <p class="stat-label">最新气压</p>
              <p class="stat-value">{{ latestStats.pressure ?? '--' }}<span v-if="latestStats.pressure" class="stat-unit">hPa</span></p>
            </div>
          </div>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card class="stat-card" shadow="hover">
          <div class="stat-content">
            <div class="stat-icon" style="background: linear-gradient(135deg, var(--el-color-warning), #ffc100);">
              <span class="stat-icon-text">📊</span>
            </div>
            <div class="stat-info">
              <p class="stat-label">数据点总数</p>
              <p class="stat-value">{{ latestStats.totalPoints }}</p>
            </div>
          </div>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card class="stat-card" shadow="hover">
          <div class="stat-content">
            <div class="stat-icon" style="background: linear-gradient(135deg, var(--el-color-success), #85ce61);">
              <span class="stat-icon-text">⏱️</span>
            </div>
            <div class="stat-info">
              <p class="stat-label">采集时长</p>
              <p class="stat-value">{{ latestStats.duration }}</p>
            </div>
          </div>
        </el-card>
      </el-col>
    </el-row>

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
        <el-select v-model="queryForm.compareCategory" size="small" style="width: 140px;">
          <el-option label="温度" value="temperature" />
          <el-option label="气压" value="pressure" />
          <el-option label="湿度" value="humidity" />
        </el-select>
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

    <!-- 历史数据表格 -->
    <el-card style="margin-top: 20px;">
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
        <el-empty v-if="!historyData || historyData.length === 0" description="暂无数据" />
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
import { ref, reactive, onMounted, onUnmounted, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { Download, Connection } from '@element-plus/icons-vue'
import PageHeader from '@/components/common/PageHeader.vue'
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
import { downsampleData } from '@/utils/downsample'

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

// 统计概览数据
const latestStats = reactive({
  temperature: null as number | null,
  pressure: null as number | null,
  totalPoints: 0,
  duration: '--'
})

const calculateStats = () => {
  const items = historyData.value
  if (!items || items.length === 0) {
    latestStats.temperature = null
    latestStats.pressure = null
    latestStats.totalPoints = 0
    latestStats.duration = '--'
    return
  }

  // 统计最新温度和气压（遍历所有数据提取）
  let latestTemp: number | null = null
  let latestPressure: number | null = null
  let latestTempTime = ''
  let latestPressureTime = ''

  for (const item of items) {
    const data = item.parsed_data || item.data
    if (!data) continue
    // Helper to extract numeric value
    const extractNumeric = (val: any): number | null => {
      if (typeof val === 'number') return val
      if (typeof val === 'object' && val !== null && 'value' in val) {
        return typeof val.value === 'number' ? val.value : null
      }
      return null
    }

    const itemTime = item.timestamp || item.created_at || item.collected_at || ''
    // Temperature search: check common keys
    const tempKeys = ['temperature', 'temp', 'Temperature', 'Temp']
    for (const key of tempKeys) {
      if (data[key] !== undefined) {
        const val = extractNumeric(data[key])
        if (val !== null && (!latestTemp || itemTime > latestTempTime)) {
          latestTemp = val
          latestTempTime = itemTime
        }
        break
      }
    }

    // Pressure search
    const pressKeys = ['pressure', 'Pressure', 'press', 'Press']
    for (const key of pressKeys) {
      if (data[key] !== undefined) {
        const val = extractNumeric(data[key])
        // Pressure in Pa -> convert to hPa
        const adjusted = val !== null && val > 10000 ? val / 100 : val
        if (val !== null && (!latestPressure || itemTime > latestPressureTime)) {
          latestPressure = adjusted !== null ? Math.round(adjusted * 10) / 10 : null
          latestPressureTime = itemTime
        }
        break
      }
    }
  }

  latestStats.temperature = latestTemp
  latestStats.pressure = latestPressure
  latestStats.totalPoints = total.value

  // 计算采集时长
  if (items.length >= 2) {
    const times = items
      .map(item => item.timestamp || item.created_at || item.collected_at)
      .filter(Boolean)
      .map(t => new Date(t).getTime())
      .filter(t => !isNaN(t))
      .sort((a, b) => a - b)
    if (times.length >= 2) {
      const diffMs = times[times.length - 1] - times[0]
      const diffMin = Math.floor(diffMs / 60000)
      if (diffMin < 60) {
        latestStats.duration = `${diffMin}分钟`
      } else if (diffMin < 1440) {
        latestStats.duration = `${Math.floor(diffMin / 60)}小时${diffMin % 60}分钟`
      } else {
        latestStats.duration = `${Math.floor(diffMin / 1440)}天${Math.floor((diffMin % 1440) / 60)}小时`
      }
    } else {
      latestStats.duration = '< 1分钟'
    }
  } else if (items.length === 1) {
    latestStats.duration = '< 1分钟'
  } else {
    latestStats.duration = '--'
  }
}

const queryForm = reactive({
  deviceId: null as number | null,
  timeRange: '24h',
  compareCategory: 'temperature' as string
})

const wsStore = useWebSocketStore()
const edgeDeviceStore = useEdgeDeviceStore()
let unsubscribeData: (() => void) | null = null

const fetchDevices = async () => {
  try {
    await edgeDeviceStore.fetchList({ page_size: 500 })
    deviceList.value = edgeDeviceStore.list
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

    // 计算统计概览
    calculateStats()

    // 构建图表数据：从 unified_data API 获取解析后的数值数据
    buildChartSeries()
  } catch (error: any) {
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

    // Fetch categories dynamically from the API instead of hardcoding
    let categoryNames: string[] = []
    try {
      const catResponse = await client.get<unknown, any>('/api/v1/unified-data/categories')
      const cats = catResponse?.data || catResponse || []
      if (Array.isArray(cats)) {
        categoryNames = cats.map((c: any) => c.code || c.name || c)
      }
    } catch { /* ignore */ }

    // Fallback to known categories if API returns nothing
    if (categoryNames.length === 0) {
      categoryNames = ['temperature', 'pressure', 'humidity', 'wind_speed', 'wind_direction',
        'illuminance', 'uv_index', 'rain_intensity', 'rain_accum',
        'voltage', 'current', 'power', 'energy', 'soc', 'soh', 'frequency',
        'cell_voltage', 'cell_temp', 'mos_status']
    }

    const series: any[] = []
    const promises = categoryNames.map(cat =>
      client.get<unknown, any>('/api/v1/unified-data/historical', {
        params: {
          device_pk: queryForm.deviceId,
          category: cat,
          start_time: startTime.toISOString(),
          end_time: endTime.toISOString()
        }
      }).then(res => ({ cat, data: res.data || [] }))
        .catch(() => ({ cat, data: [] as any[] }))
    )

    const results = await Promise.all(promises)
    for (const r of results) {
      if (r.data && r.data.length > 0) {
        const filteredData = r.data
          .filter((item: any) => {
            const t = item.timestamp || item.created_at
            return t && !t.startsWith('0001-01-01')
          })
        if (filteredData.length > 0) {
          // Use category from API response or fall back to sensor maps
          const catName = sensorNameMap[r.cat] || r.cat
          const catUnit = sensorUnitMap[r.cat] || ''
          series.push({
            name: catName,
            unit: catUnit,
            data: downsampleData(
              filteredData.map((item: any) => ({
                time: item.timestamp || item.created_at,
                value: item.value
              }))
            )
          })
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
        data: downsampleData(
          historyData.value
            .filter((item: any) => {
              const t = item.timestamp || item.collected_at || item.created_at
              return t && !t.startsWith('0001-01-01')
            })
            .map(item => ({ time: item.timestamp || item.collected_at || item.created_at, value: item.data?.[key] ?? 0 }))
        )
      }))
    } else {
      chartSeries.value = []
    }
  }
}

// 实时数据处理
// 性能优化：防抖 calculateStats 和 buildChartSeries，避免每条WS消息都触发重计算
let statsDirty = false
let chartDirty = false
let debounceTimer: ReturnType<typeof setTimeout> | null = null
const DEBOUNCE_MS = 2000  // 2秒内多条消息只触发一次重算

const flushDebounced = () => {
  if (statsDirty) {
    statsDirty = false
    calculateStats()
  }
  if (chartDirty) {
    chartDirty = false
    buildChartSeries()
  }
}

const scheduleDebounced = () => {
  if (debounceTimer) clearTimeout(debounceTimer)
  debounceTimer = setTimeout(flushDebounced, DEBOUNCE_MS)
}

const handleDataUpdate = (message: WebSocketMessage) => {
  const payload = message.payload
  if (!payload || payload.device_id !== queryForm.deviceId) return

  realtimeCount.value++
  const newItem = {
    collected_at: (payload as any).collected_at || new Date().toISOString(),
    data: (payload as any).data || {},
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

  // 标记为脏，延迟批量重算（避免高频WS消息每条都触发20+个HTTP请求）
  statsDirty = true
  chartDirty = true
  scheduleDebounced()
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
      const response = await client.get<unknown, any>(`/api/v1/unified-data/historical`, {
        params: {
          device_pk: deviceId,
          category: queryForm.compareCategory,
          start_time: startTime.toISOString(),
          end_time: endTime.toISOString()
        }
      })
      return {
        deviceId,
        deviceName: deviceList.value.find(d => d.id === deviceId)?.name || String(deviceId),
        data: response.data || []
      }
    })

    const results = await Promise.all(promises)

    compareSeries.value = results.map(r => ({
      name: r.deviceName,
      unit: '',
      data: r.data
        .filter((item: any) => {
          const t = item.timestamp || item.created_at
          return t && !t.startsWith('0001-01-01')
        })
        .map((item: any) => ({
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

const formatData = (data: Record<string, any>) => {
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

watch(compareDevices, () => {
  if (compareMode.value && compareDevices.value.length >= 2) {
    fetchCompareData()
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
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.12);
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
}

.stat-icon-text {
  font-size: 20px;
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
  color: #303133;
}

.stat-unit {
  font-size: 13px;
  font-weight: 400;
  color: var(--el-text-color-secondary);
  margin-left: 2px;
}
</style>