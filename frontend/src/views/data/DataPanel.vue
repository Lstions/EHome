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
            <div class="stat-icon" style="background: linear-gradient(135deg, #f56c6c, #e6a23c);">
              <span class="stat-icon-text">🌡️</span>
            </div>
            <div class="stat-info">
              <p class="stat-label">{{ latestStats.primaryLabel }}</p>
              <p class="stat-value">{{ latestStats.primary?.toFixed(2) ?? '--' }}<span v-if="latestStats.primary" class="stat-unit">{{ latestStats.primaryUnit }}</span></p>
            </div>
          </div>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card class="stat-card" shadow="hover">
          <div class="stat-content">
            <div class="stat-icon" style="background: linear-gradient(135deg, #409eff, #67c23a);">
              <span class="stat-icon-text">🌊</span>
            </div>
            <div class="stat-info">
              <p class="stat-label">{{ latestStats.secondaryLabel }}</p>
              <p class="stat-value">{{ latestStats.secondary?.toFixed(2) ?? '--' }}<span v-if="latestStats.secondary" class="stat-unit">{{ latestStats.secondaryUnit }}</span></p>
            </div>
          </div>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card class="stat-card" shadow="hover">
          <div class="stat-content">
            <div class="stat-icon" style="background: linear-gradient(135deg, #e6a23c, #ffc100);">
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
            <div class="stat-icon" style="background: linear-gradient(135deg, #67c23a, #85ce61);">
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
        <span style="font-size: 14px; color: #606266;">对比类别：</span>
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
              <span>{{ formatData(row) }}</span>
            </template>
          </el-table-column>
          <el-table-column label="原始数据" width="120" align="center">
            <template #default="{ row }">
              <span v-if="row.raw_data" style="font-size: 12px; color: #909399;">{{ formatRawData(row.raw_data) }}</span>
              <span v-else style="color: #c0c4cc;">-</span>
            </template>
          </el-table-column>
          <el-table-column label="状态" width="100" align="center">
            <template #default="{ row }">
              <el-tag v-if="row.error_code && row.error_code > 0"
                      :type="getErrorInfo(row.error_code).type"
                      size="small">
                {{ getErrorInfo(row.error_code).label }}
              </el-tag>
              <span v-else style="color: #67c23a;">正常</span>
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
import { deviceApi, type Device } from '@/api/device'
import client from '@/api/client'
import { getErrorInfo } from '@/utils/errorCode'
import { useWebSocketStore, type WebSocketMessage } from '@/stores/websocket'
import { logger } from '@/utils/logger'

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
  primary: null as number | null,
  primaryLabel: '--',
  primaryUnit: '',
  secondary: null as number | null,
  secondaryLabel: '--',
  secondaryUnit: '',
  totalPoints: 0,
  duration: '--'
})

const calculateStats = () => {
  const items = historyData.value
  if (!items || items.length === 0) {
    latestStats.primary = null
    latestStats.primaryLabel = '--'
    latestStats.primaryUnit = ''
    latestStats.secondary = null
    latestStats.secondaryLabel = '--'
    latestStats.secondaryUnit = ''
    latestStats.totalPoints = 0
    latestStats.duration = '--'
    return
  }

  // Extract first two distinct sensor types from UnifiedData
  const sensorMap = new Map<string, { value: number; unit: string; time: string }>()
  for (const item of items) {
    if (item.sensor_name && item.value !== undefined) {
      const existing = sensorMap.get(item.sensor_name)
      const itemTime = item.timestamp || item.created_at || ''
      if (!existing || itemTime > existing.time) {
        sensorMap.set(item.sensor_name, {
          value: typeof item.value === 'number' ? item.value : parseFloat(item.value) || 0,
          unit: item.unit || '',
          time: itemTime
        })
      }
    }
  }

  const sensors = Array.from(sensorMap.entries())
  if (sensors.length >= 1) {
    const [name, data] = sensors[0]
    latestStats.primary = data.value
    latestStats.primaryLabel = name
    latestStats.primaryUnit = data.unit
  }
  if (sensors.length >= 2) {
    const [name, data] = sensors[1]
    latestStats.secondary = data.value
    latestStats.secondaryLabel = name
    latestStats.secondaryUnit = data.unit
  }

  latestStats.totalPoints = items.length

  // Calculate duration
  const times = items.map(i => new Date(i.timestamp || i.created_at || 0).getTime()).filter(t => t > 0)
  if (times.length >= 2) {
    const durationMs = Math.max(...times) - Math.min(...times)
    const hours = Math.floor(durationMs / 3600000)
    const minutes = Math.floor((durationMs % 3600000) / 60000)
    latestStats.duration = hours > 0 ? `${hours}小时${minutes}分钟` : `${minutes}分钟`
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
let unsubscribeData: (() => void) | null = null

const fetchDevices = async () => {
  try {
    const response = await deviceApi.getList({ page_size: 500 })
    deviceList.value = Array.isArray(response) ? response : []
  } catch (error: any) {
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

    const response = await deviceApi.getHistoryData(queryForm.deviceId, {
      start_time: startTime.toISOString(),
      end_time: endTime.toISOString(),
      page: currentPage.value,
      page_size: pageSize.value
    })

    historyData.value = Array.isArray(response) ? response : []
    total.value = historyData.value.length

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

    const categories = ['temperature', 'pressure', 'humidity', 'wind_speed', 'illuminance']
    const unitMap: Record<string, string> = {
      temperature: '°C',
      pressure: 'hPa',
      humidity: '%',
      wind_speed: 'm/s',
      illuminance: 'lux'
    }
    const nameMap: Record<string, string> = {
      temperature: '温度',
      pressure: '气压',
      humidity: '湿度',
      wind_speed: '风速',
      illuminance: '光照'
    }

    const series: any[] = []
    const promises = categories.map(cat =>
      client.get<unknown, any>('/api/v1/unified-data/historical', {
        params: {
          device_pk: queryForm.deviceId,
          category: cat,
          start_time: startTime.toISOString(),
          end_time: endTime.toISOString()
        }
      }).then(res => ({ cat, data: res.data || [] }))
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
          series.push({
            name: nameMap[r.cat] || r.cat,
            unit: unitMap[r.cat] || '',
            data: filteredData.map((item: any) => ({
              time: item.timestamp || item.created_at,
              value: item.value
            }))
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
        data: historyData.value
          .filter((item: any) => {
            const t = item.timestamp || item.collected_at || item.created_at
            return t && !t.startsWith('0001-01-01')
          })
          .map(item => ({
            time: item.timestamp || item.collected_at || item.created_at,
            value: item.data?.[key] ?? 0
          }))
      }))
    } else {
      chartSeries.value = []
    }
  }
}

// 实时数据处理
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

  // 更新统计和图表
  calculateStats()
  buildChartSeries()
}

const toggleRealtime = (enabled: boolean) => {
  if (enabled) {
    // 订阅实时数据
    unsubscribeData = wsStore.subscribe('data', handleDataUpdate)
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

const formatData = (row: Record<string, any>) => {
  if (!row) return '-'

  // UnifiedData format: sensor_name + value + unit
  if (row.sensor_name && row.value !== undefined) {
    const unit = row.unit || ''
    const val = typeof row.value === 'number' ? row.value.toFixed(2) : row.value
    return `${row.sensor_name}: ${val} ${unit}`.trim()
  }

  // Fallback: parsed_data or data object
  const data = row.parsed_data || row.data
  if (!data) return '-'
  if (typeof data === 'string') return data

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
  color: #909399;
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
  color: #909399;
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
  color: #909399;
  margin-left: 2px;
}
</style>