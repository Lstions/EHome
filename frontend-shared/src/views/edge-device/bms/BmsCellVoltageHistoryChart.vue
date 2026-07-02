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
    // 批量请求：1 个请求替代 16 个并行请求，服务端降采样 max_points=500
    const rawSeries: HistoryDataPoint[][] = new Array(cellCategories.length).fill(null).map(() => [])
    try {
      const batchRes = await client.get<unknown, any>('/api/v1/unified-data/historical-batch', {
        params: {
          device_pk: props.deviceId,
          categories: cellCategories.join(','),
          start_time: startTime.toISOString(),
          end_time: endTime.toISOString(),
          max_points: 500
        }
      })
      const batchData = batchRes?.data || batchRes || []
      if (Array.isArray(batchData)) {
        for (const result of batchData) {
          const cat = result.category
          if (!cat) continue
          const idx = cellCategories.indexOf(cat)
          if (idx === -1) continue
          rawSeries[idx] = (result.data || []).map((item: any) => ({
            time: item.created_at || item.timestamp,
            value: item.value
          }))
        }
      }
    } catch {
      // Fallback: 逐个请求 + max_points=500
      const fallbackResults = await Promise.all(
        cellCategories.map(cat =>
          client.get<unknown, any>('/api/v1/unified-data/historical', {
            params: {
              device_pk: props.deviceId,
              category: cat,
              start_time: startTime.toISOString(),
              end_time: endTime.toISOString(),
              max_points: 500
            }
          }).then(res => ({ cat, data: (res.data || res || []) as any[] })).catch(() => ({ cat, data: [] as any[] }))
        )
      )
      for (const r of fallbackResults) {
        const idx = cellCategories.indexOf(r.cat)
        if (idx !== -1) {
          rawSeries[idx] = r.data.map((item: any) => ({ time: item.created_at || item.timestamp, value: item.value }))
        }
      }
    }

    // 服务端已降采样，直接使用原始数据
    allSeries.value = cellCategories
      .map((cat, i) => ({
        name: `#${i + 1}`,
        unit: 'V',
        category: cat,
        cellNumber: i + 1,
        data: rawSeries[i] || []
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
