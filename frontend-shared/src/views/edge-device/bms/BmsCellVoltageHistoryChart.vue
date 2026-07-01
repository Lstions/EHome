<template>
  <el-card shadow="hover" style="margin-top: 20px;">
    <template #header>
      <div style="display: flex; justify-content: space-between; align-items: center;">
        <span>电芯电压历史趋势</span>
        <TimeRangeSelector
          v-model="timeRange"
          v-model:custom-range="customTimeRange"
          @change="onTimeRangeChange"
        />
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
        :show-area="false"
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
import TimeRangeSelector from '@/components/charts/TimeRangeSelector.vue'
import client from '@/api/client'
import { downsampleMultiSeries } from '@/utils/downsample'
import { computeAdaptiveYAxisRange } from '@/utils/chartRange'
import { useTimeRange } from '@/composables/useTimeRange'
import type { HistoryDataPoint, SeriesData } from '@/types/chart'

const props = withDefaults(defineProps<{
  deviceId: number
  cellCount?: number
}>(), {
  cellCount: 16
})

const loading = ref(true)
const allSeries = ref<SeriesData[]>([])
const selectedCells = ref<number[]>([])

// useTimeRange provides reactive timeRange/customTimeRange state, getTimeRange(),
// and handleTimeRangeChange (fires onChange only for preset ranges, not 'custom').
// fetchCellVoltageHistory is a hoisted function declaration so it can be referenced
// here before its definition appears in source order.
const { timeRange, customTimeRange, getTimeRange, handleTimeRangeChange } = useTimeRange(fetchCellVoltageHistory)

const cellCategories = Array.from(
  { length: props.cellCount },
  (_, i) => `cell_voltage_${i + 1}`
)

const filteredSeries = computed<SeriesData[]>(() => {
  if (selectedCells.value.length === 0) return []
  return allSeries.value.filter(s => selectedCells.value.includes(s.cellNumber))
})

const yAxisRange = computed<{ min: number; max: number }>(() => {
  const allValues = filteredSeries.value.flatMap(s => s.data.map(d => d.value)).filter(v => typeof v === 'number' && v > 0)
  if (allValues.length === 0) return { min: 2.5, max: 4.0 }
  return computeAdaptiveYAxisRange(allValues, { minBound: 2.0, maxBound: 4.5 })
})

/**
 * Wrapper for TimeRangeSelector @change event.
 * Uses the composable's handleTimeRangeChange for preset ranges,
 * and also fetches when custom dates are selected via the date picker.
 */
const onTimeRangeChange = () => {
  handleTimeRangeChange()
  if (timeRange.value === 'custom' && customTimeRange.value) {
    fetchCellVoltageHistory()
  }
}

async function fetchCellVoltageHistory() {
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
    const rawSeries: HistoryDataPoint[][] = cellCategories.map((cat, i) =>
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
