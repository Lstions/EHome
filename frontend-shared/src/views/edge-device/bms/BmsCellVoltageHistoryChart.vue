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
    allSeries.value = cellCategories
      .map((cat, i) => ({
        name: `#${i + 1}`,
        unit: 'V',
        category: cat,
        cellNumber: i + 1,
        data: (results[i] as any[]).map(item => ({
          time: item.created_at || item.timestamp,
          value: item.value
        }))
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
