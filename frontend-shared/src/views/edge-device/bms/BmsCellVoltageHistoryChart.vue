<template>
  <el-card shadow="hover" style="margin-top: 20px;">
    <template #header>
      <div style="display: flex; justify-content: space-between; align-items: center;">
        <span>电芯电压历史趋势</span>
        <div style="display: flex; align-items: center; gap: 8px;">
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
            style="width: 340px;"
            @change="fetchCellVoltageHistory"
          />
        </div>
      </div>
    </template>

    <el-skeleton v-if="loading" :rows="6" animated />

    <template v-else>
      <!-- Cell selection checkboxes -->
      <div class="cell-selector" v-if="allSeries.length > 0">
        <el-checkbox-group v-model="selectedCells" size="small">
          <el-checkbox
            v-for="i in props.cellCount"
            :key="i"
            :value="i"
            :label="`#${i}`"
            border
            size="small"
            style="margin: 2px;"
          />
        </el-checkbox-group>
        <div style="margin-left: 8px; display: inline-flex; gap: 4px;">
          <el-button size="small" text @click="selectAll">全选</el-button>
          <el-button size="small" text @click="invertSelection">反选</el-button>
        </div>
      </div>

      <!-- Line chart -->
      <LineChart
        v-if="filteredSeries.length > 0"
        :series="filteredSeries"
        :y-axis-min="yAxisRange.min"
        :y-axis-max="yAxisRange.max"
        height="320px"
      />

      <!-- Empty state -->
      <el-empty
        v-if="!loading && allSeries.length === 0"
        description="暂无电芯电压历史数据"
        :image-size="60"
      />
    </template>
  </el-card>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import LineChart from '@/components/charts/LineChart.vue'
import client from '@/api/client'
import { downsampleMultiSeries } from '@/utils/downsample'

const props = withDefaults(defineProps<{
  deviceId: number
  cellCount?: number
}>(), {
  cellCount: 16
})

interface HistoryDataPoint { time: string; value: number }
interface SeriesData { name: string; data: HistoryDataPoint[]; unit?: string; category?: string; cellNumber: number }

const loading = ref(true)
const allSeries = ref<SeriesData[]>([])
const selectedCells = ref<number[]>([])
const timeRange = ref('24h')
const customTimeRange = ref<[string, string] | null>(null)

const cellCategories = Array.from(
  { length: props.cellCount },
  (_, i) => `cell_voltage_${i + 1}`
)

const getTimeRange = (): [Date, Date] => {
  const now = new Date()
  let startTime: Date
  switch (timeRange.value) {
    case '1h': startTime = new Date(now.getTime() - 3600000); break
    case '24h': startTime = new Date(now.getTime() - 86400000); break
    case '7d': startTime = new Date(now.getTime() - 7 * 86400000); break
    case 'custom':
      if (customTimeRange.value) {
        return [new Date(customTimeRange.value[0]), new Date(customTimeRange.value[1])]
      }
      startTime = new Date(now.getTime() - 86400000); break
    default: startTime = new Date(now.getTime() - 86400000)
  }
  return [startTime, now]
}

const filteredSeries = computed<SeriesData[]>(() => {
  if (selectedCells.value.length === 0) return []
  return allSeries.value.filter(s => selectedCells.value.includes(s.cellNumber))
})

/**
 * Compute adaptive Y-axis range from actual cell voltage history data.
 * Uses data min/max with padding so small voltage differences are visible.
 * Clamps to safe bounds so the chart never looks absurd with bad data.
 */
const yAxisRange = computed<{ min: number; max: number }>(() => {
  const allValues = filteredSeries.value.flatMap(s => s.data.map(d => d.value)).filter(v => typeof v === 'number' && v > 0)
  if (allValues.length === 0) return { min: 2.5, max: 4.0 }

  const dataMin = allValues.reduce((a, b) => a < b ? a : b, Infinity)
  const dataMax = allValues.reduce((a, b) => a > b ? a : b, -Infinity)
  const span = dataMax - dataMin

  // If all values are identical, create a small window around the value
  const padding = span < 0.05 ? 0.05 : span * 0.3
  let min = dataMin - padding
  let max = dataMax + padding

  // Clamp to safe bounds
  if (min < 2.0) min = 2.0
  if (max > 4.5) max = 4.5

  // Ensure minimum visible range
  if (max - min < 0.1) {
    const center = (min + max) / 2
    min = center - 0.05
    max = center + 0.05
  }

  // Round to nice values
  min = Math.floor(min * 100) / 100
  max = Math.ceil(max * 100) / 100

  return { min, max }
})

const fetchCellVoltageHistory = async () => {
  if (!props.deviceId) return
  const [startTime, endTime] = getTimeRange()
  loading.value = true
  try {
    const results = await Promise.all(
      cellCategories.map(cat =>
        client.get<unknown, any>('/api/v1/unified-data/historical', {
          params: {
            device_pk: props.deviceId,
            category: cat,
            start_time: startTime.toISOString(),
            end_time: endTime.toISOString()
          }
        }).then(res => res || []).catch(() => [])
      )
    )

    // Map raw API results to {time, value} arrays
    const rawSeries = cellCategories.map((cat, i) =>
      (results[i] as any[]).map(item => ({
        time: item.created_at || item.timestamp,
        value: item.value
      }))
    )

    // Downsample all series together so they share the same timestamps.
    // This ensures the tooltip at any time point shows all cells, not just
    // the ones that happened to retain a point at that timestamp.
    // Series with different lengths fall back to independent downsampling.
    const hasData = rawSeries.filter(s => s.length > 0)
    const downsampled = hasData.length > 0
      ? downsampleMultiSeries(hasData)
      : []

    // Rebuild allSeries preserving original cell numbers
    const downsampledMap = new Map<number, typeof rawSeries[number]>()
    let dsIdx = 0
    for (let i = 0; i < cellCategories.length; i++) {
      if (rawSeries[i].length > 0) {
        downsampledMap.set(i + 1, downsampled[dsIdx] || [])
        dsIdx++
      }
    }

    allSeries.value = cellCategories
      .map((cat, i) => ({
        name: `#${i + 1}`,
        unit: 'V',
        category: cat,
        cellNumber: i + 1,
        data: downsampledMap.get(i + 1) || []
      }))
      .filter(s => s.data.length > 0)
    selectedCells.value = allSeries.value.map(s => s.cellNumber)
  } catch {
    ElMessage.error('获取电芯电压历史数据失败')
  } finally {
    loading.value = false
  }
}

const handleTimeRangeChange = () => {
  if (timeRange.value !== 'custom') {
    fetchCellVoltageHistory()
  }
}

const selectAll = () => {
  selectedCells.value = allSeries.value.map(s => s.cellNumber)
}

const invertSelection = () => {
  const all = allSeries.value.map(s => s.cellNumber)
  selectedCells.value = all.filter(i => !selectedCells.value.includes(i))
}

defineExpose({ fetchCellVoltageHistory })

onMounted(() => fetchCellVoltageHistory())
</script>

<style scoped>
.cell-selector {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  margin-bottom: 12px;
}
</style>
