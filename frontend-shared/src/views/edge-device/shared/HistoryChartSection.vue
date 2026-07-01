<template>
  <el-card style="margin-top: 20px;" shadow="hover">
    <template #header>
      <div style="display: flex; justify-content: space-between; align-items: center;">
        <span>历史数据</span>
        <div class="time-range-selector">
          <el-radio-group v-model="timeRange" size="small" @change="handleTimeRangeChange">
            <el-radio-button value="1h">1小时</el-radio-button>
            <el-radio-button value="24h">24小时</el-radio-button>
            <el-radio-button value="7d">7天</el-radio-button>
            <el-radio-button value="custom">自定义</el-radio-button>
          </el-radio-group>
          <el-date-picker
            v-if="timeRange === 'custom'"
            v-model="customTimeRange"
            type="datetimerange"
            size="small"
            range-separator="至"
            start-placeholder="开始时间"
            end-placeholder="结束时间"
            format="YYYY-MM-DD HH:mm"
            value-format="YYYY-MM-DD HH:mm:ss"
            style="margin-left: 10px; width: 380px;"
            @change="fetchHistoryData"
          />
          <el-button
            type="primary"
            style="margin-left: 10px;"
            @click="handleExportCSV"
            :disabled="historyData.length === 0 && chartSeries.length === 0"
          >
            <el-icon :size="16"><Download /></el-icon>
            导出CSV
          </el-button>
        </div>
      </div>
    </template>

    <el-skeleton v-if="chartLoading" :rows="8" animated />
    <template v-else>
      <div v-if="chartSubGroups.length > 0" class="chart-grid">
        <div
          v-for="group in chartSubGroups"
          :key="group.title"
          class="chart-sub-section"
        >
          <p class="chart-sub-title">{{ group.title }}</p>
          <LineChart
            :series="group.series"
            :title="''"
            height="260px"
          />
        </div>
      </div>
      <LineChart
        v-else-if="historyData.length > 0"
        :data="historyData"
        :title="`${deviceTypeText}数据趋势`"
        height="400px"
      />
      <el-empty v-else description="暂无历史数据" :image-size="60" />
    </template>
  </el-card>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { Download } from '@element-plus/icons-vue'
import LineChart from '@/components/charts/LineChart.vue'
import { edgeDeviceApi } from '@/api/edgeDevice'
import client from '@/api/client'
import { sensorNameMap, sensorUnitMap, SENSOR_ORDER } from '@/utils/sensor'
import { downsampleData } from '@/utils/downsample'
import { logger } from '@/utils/logger'

const props = defineProps<{
  deviceId: number
  deviceType: string
  deviceTypeText: string
}>()

interface HistoryDataPoint { time: string; value: number }
interface SeriesData { name: string; data: HistoryDataPoint[]; unit?: string; category?: string }

const historyData = ref<HistoryDataPoint[]>([])
const chartSeries = ref<SeriesData[]>([])
const chartLoading = ref(true)
const timeRange = ref('24h')
const customTimeRange = ref<[string, string] | null>(null)

/** BMS设备使用的unified-data category名称 */
const bmsCategories = [
  'total_voltage', 'current', 'rsoc', 'remaining_capacity',
  'protection_status', 'fet_status',
  'temperature_1', 'temperature_2', 'temperature_3',
  'cell_voltage_max', 'cell_voltage_min',
]

/** 状态量category — 不适合折线图，从图表中移除 */
const statusCategories = ['protection_status', 'fet_status']

/**
 * BMS按量纲分组的子图表定义。
 * 每组共享同一Y轴量纲，避免多Y轴混叠。
 * 状态量(protection_status, fet_status)不放入折线图。
 */
interface ChartSubGroup {
  title: string
  categories: string[]
}
const bmsChartGroups: ChartSubGroup[] = [
  { title: '电压 (V)', categories: ['total_voltage', 'cell_voltage_max', 'cell_voltage_min'] },
  { title: '电流 (A)', categories: ['current'] },
  { title: '电池状态', categories: ['rsoc', 'remaining_capacity'] },
  { title: '温度 (°C)', categories: ['temperature_1', 'temperature_2', 'temperature_3'] },
]

/** 判断是否为BMS设备类型 */
const isBmsDevice = (deviceType: string): boolean => {
  const bmsTypes = ['jiabaida_bms', 'bms', 'battery']
  return bmsTypes.includes(deviceType.toLowerCase())
}

/**
 * 将chartSeries按量纲分组为子图表。
 * BMS设备使用预定义分组；非BMS设备按unit自动分组。
 */
const chartSubGroups = computed<{ title: string; series: SeriesData[] }[]>(() => {
  if (chartSeries.value.length === 0) return []

  if (isBmsDevice(props.deviceType)) {
    // BMS: 按预定义量纲分组
    return bmsChartGroups
      .map(group => {
        const series = chartSeries.value.filter(s =>
          s.category && group.categories.includes(s.category)
        )
        return { title: group.title, series }
      })
      .filter(g => g.series.length > 0)
  }

  // 非BMS: 按unit自动分组
  const unitGroups = new Map<string, SeriesData[]>()
  for (const s of chartSeries.value) {
    const unit = s.unit || ''
    if (!unitGroups.has(unit)) unitGroups.set(unit, [])
    unitGroups.get(unit)!.push(s)
  }
  return Array.from(unitGroups.entries()).map(([unit, series]) => ({
    title: unit ? `(${unit})` : '数据趋势',
    series,
  }))
})

const getTimeRange = (): [Date, Date] => {
  const now = new Date()
  let startTime: Date
  switch (timeRange.value) {
    case '1h': startTime = new Date(now.getTime() - 3600000); break
    case '24h': startTime = new Date(now.getTime() - 86400000); break
    case '7d': startTime = new Date(now.getTime() - 7 * 86400000); break
    case 'custom':
      if (customTimeRange.value) return [new Date(customTimeRange.value[0]), new Date(customTimeRange.value[1])]
      startTime = new Date(now.getTime() - 86400000); break
    default: startTime = new Date(now.getTime() - 86400000)
  }
  return [startTime, now]
}

const fetchHistoryData = async () => {
  if (!props.deviceId) return
  const [startTime, endTime] = getTimeRange()
  const deviceType = props.deviceType

  chartLoading.value = true
  try {
    // Unified data devices: query specific categories
    if (['bmp280', 'sht40', 'temp_humidity'].includes(deviceType)) {
      const categories = deviceType === 'bmp280' ? ['temperature', 'pressure'] : ['temperature', 'humidity']
      const results = await Promise.all(
        categories.map(cat =>
          client.get<unknown, any>('/api/v1/unified-data/historical', {
            params: { device_pk: props.deviceId, category: cat, start_time: startTime.toISOString(), end_time: endTime.toISOString() }
          }).then(res => res || [])
        )
      )
      chartSeries.value = categories
        .map((cat, i) => ({
          name: sensorNameMap[cat] || cat,
          unit: sensorUnitMap[cat] || '',
          category: cat,
          data: downsampleData(
            (results[i] as any[]).map((item: any) => ({ time: item.created_at || item.timestamp, value: item.value }))
          )
        }))
        .filter(s => s.data.length > 0)
      historyData.value = []
    } else {
      // Generic: try all known categories via unified-data
      // BMS设备使用专用category名称
      const knownCategories = isBmsDevice(deviceType)
        ? bmsCategories
        : [...SENSOR_ORDER, 'illuminance', 'uv_index', 'rain_intensity', 'rain_accum',
            'voltage', 'current', 'power', 'energy', 'soc', 'soh', 'frequency',
            'cell_voltage', 'cell_temp', 'mos_status', 'protection_status']
      const results = await Promise.all(
        knownCategories.map(cat =>
          client.get<unknown, any>('/api/v1/unified-data/historical', {
            params: { device_pk: props.deviceId, category: cat, start_time: startTime.toISOString(), end_time: endTime.toISOString() }
          }).then(res => ({ cat, data: res || [] }))
            .catch(() => ({ cat, data: [] as any[] }))
        )
      )
      // 过滤掉状态量(不适合折线图)和空数据
      const series = results
        .filter(r => r.data && r.data.length > 0)
        .filter(r => !statusCategories.includes(r.cat))
        .map(r => ({
          name: sensorNameMap[r.cat] || r.cat,
          unit: sensorUnitMap[r.cat] || '',
          category: r.cat,
          data: downsampleData(
            r.data.map((item: any) => ({ time: item.created_at || item.timestamp, value: item.value }))
          )
        }))
      if (series.length > 0) {
        chartSeries.value = series
        historyData.value = []
      } else {
        // Fallback: device_data table
        const response = await edgeDeviceApi.getHistoryData(props.deviceId, {
          start_time: startTime.toISOString(), end_time: endTime.toISOString(), page: 1, page_size: 1000
        })
        chartSeries.value = []
        if (response.items && response.items.length > 0) {
          const firstItem = response.items[0]
          const data = firstItem.data || {}
          // 过滤掉状态量字段
          const numericKeys = Object.keys(data)
            .filter(key => key !== 'raw_data' && typeof data[key] === 'number')
            .filter(key => !statusCategories.includes(key))
          if (numericKeys.length > 1) {
            chartSeries.value = numericKeys.map(key => ({
              name: sensorNameMap[key] || key, unit: sensorUnitMap[key] || '', category: key,
              data: downsampleData(
                response.items.map((item: any) => ({ time: item.created_at || item.collected_at, value: item.data[key] ?? 0 }))
              )
            }))
            historyData.value = []
          } else {
            const valueKey = numericKeys[0] || Object.keys(data).find(key => typeof data[key] === 'number' && !statusCategories.includes(key))
            historyData.value = valueKey
              ? downsampleData(
                  response.items.map((item: any) => ({ time: item.created_at || item.collected_at, value: item.data[valueKey] }))
                )
              : []
          }
        } else {
          historyData.value = []
        }
      }
    }
  } catch (error: any) {
    logger.error('获取历史数据失败', { error: String(error) })
    ElMessage.error('获取历史数据失败')
  } finally {
    chartLoading.value = false
  }
}

const handleTimeRangeChange = () => {
  if (timeRange.value !== 'custom') fetchHistoryData()
}

function handleExportCSV() {
  const rows: string[][] = [['时间', '数值']]
  if (chartSeries.value.length > 0) {
    const seriesNames = chartSeries.value.map(s => s.name)
    rows[0] = ['时间', ...seriesNames]
    const timeSet = new Set<number>()
    const seriesMap: Map<string, Map<number, number>> = new Map()
    chartSeries.value.forEach(s => {
      const valMap = new Map<number, number>()
      s.data.forEach((d: any) => {
        const ts = new Date(d.time).getTime()
        timeSet.add(ts)
        valMap.set(ts, d.value)
      })
      seriesMap.set(s.name, valMap)
    })
    const sortedTimes = Array.from(timeSet).sort((a, b) => a - b)
    sortedTimes.forEach(ts => {
      const row = [new Date(ts).toLocaleString('zh-CN')]
      seriesNames.forEach(name => {
        const val = seriesMap.get(name)?.get(ts)
        row.push(val !== undefined ? String(val) : '')
      })
      rows.push(row)
    })
  } else {
    historyData.value.forEach(item => rows.push([new Date(item.time).toLocaleString('zh-CN'), String(item.value)]))
  }
  const csvContent = rows.map(row => row.map(cell => `"${cell}"`).join(',')).join('\n')
  const blob = new Blob(['\ufeff' + csvContent], { type: 'text/csv;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = `${props.deviceTypeText}_history.csv`
  link.click()
  URL.revokeObjectURL(url)
  ElMessage.success('导出成功')
}

defineExpose({ fetchHistoryData })

onMounted(() => fetchHistoryData())
</script>

<style scoped>
.time-range-selector { display: flex; align-items: center; }
.chart-grid { display: flex; flex-direction: column; gap: 16px; }
.chart-sub-section {
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 8px;
  padding: 12px;
  background: var(--el-fill-color-blank);
}
.chart-sub-title {
  margin: 0 0 8px;
  font-size: 14px;
  font-weight: 600;
  color: var(--el-text-color-primary);
}
</style>
