<template>
  <el-card style="margin-top: 20px;" shadow="hover">
    <template #header>
      <div style="display: flex; justify-content: space-between; align-items: center;">
        <span>历史数据</span>
        <div class="header-controls">
          <TimeRangeSelector
            v-model="timeRange"
            v-model:custom-range="customTimeRange"
            @change="onTimeRangeChange"
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
      <!--
        2026-10-20 修（实测：BMS「运行趋势」卡在**全部子图无数据**时仍高达 831px）：
        旧模板对每个分组都渲染一个 height=260px 的 LineChart，即便该组 series 为空；
        5 个空组 = 1300px 的空白图表区，把页面拉得极长且看不出哪里有问题。
        现在：只在**该组确实有数据点**时才渲染图表，否则渲染一行紧凑的空态文案。
      -->
      <div v-if="chartSubGroups.length > 0" class="chart-grid">
        <div
          v-for="group in chartSubGroups"
          :key="group.title"
          class="chart-sub-section"
        >
          <p class="chart-sub-title">{{ group.title }}</p>
          <LineChart
            v-if="groupHasData(group)"
            :series="group.series"
            :title="''"
            height="260px"
            :yAxisMin="group.yAxisMin"
            :yAxisMax="group.yAxisMax"
          />
          <p v-else class="chart-sub-empty">该分组在当前时间范围内无数据</p>
        </div>
      </div>
      <LineChart
        v-else-if="historyData.length > 0"
        :data="historyData"
        :title="`${deviceTypeText}数据趋势`"
        height="400px"
      />
      <!--
        空态必须说明"查了什么范围"，否则用户无法判断是设备没上报、还是时间范围选错。
        2026-10-20 实测：逆变器历史卡曾长期只显示"暂无历史数据"，而库里该设备
        24h 内有 2000+ 个数据点 —— 根因是设备类型判定漏了厂商前缀（已修），
        但空态本身不给任何线索，让排查方向完全错误。这里补上范围与可能原因。
      -->
      <el-empty v-else :image-size="60">
        <template #description>
          <div class="empty-desc">
            <p class="empty-title">当前时间范围内暂无历史数据</p>
            <p class="empty-hint">已查询：{{ timeRangeLabel }}（{{ queryScopeHint }}）</p>
            <p class="empty-hint">可能原因：设备刚接入尚未上报、该类型数据未被采集，或所选时间范围无数据。</p>
          </div>
        </template>
      </el-empty>
    </template>
  </el-card>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { feedback } from '@/utils/feedback'
import { ElMessage } from 'element-plus'
import { Download } from '@element-plus/icons-vue'
import LineChart from '@/components/charts/LineChart.vue'
import TimeRangeSelector from '@/components/charts/TimeRangeSelector.vue'
import { edgeDeviceApi } from '@/api/edgeDevice'
import { sensorNameMap, sensorUnitMap, SENSOR_ORDER } from '@/utils/sensor'
import { computeAdaptiveYAxisRange } from '@/utils/chartRange'
import { useTimeRange } from '@/composables/useTimeRange'
import { useHistoryData } from '@/composables/useHistoryData'
import type { HistoryDataPoint, SeriesData } from '@/types/chart'
import { logger } from '@/utils/logger'

const props = defineProps<{
  deviceId: number
  deviceType: string
  deviceTypeText: string
}>()

const historyData = ref<HistoryDataPoint[]>([])
const chartSeries = ref<SeriesData[]>([])

/** 状态量category — 不适合折线图，从图表中移除 */
const statusCategories = ['protection_status', 'fet_status']

const { loading: chartLoading, fetch: historyFetch } = useHistoryData({
  nameMap: sensorNameMap,
  unitMap: sensorUnitMap,
  excludeCategories: statusCategories,
  errorPrefix: '获取历史数据失败',
  showError: false, // caller handles errors for device_data fallback
  // 历史 API 单独放宽到 30s（client.ts 全局仍 10s）；不改其它行为（契约 §2.7）
  timeoutMs: 30000,
})

const { timeRange, customTimeRange, getTimeRange, handleTimeRangeChange } = useTimeRange(fetchHistoryData)

/**
 * Unified handler for TimeRangeSelector @change events.
 * The selector emits 'change' for both radio-group (preset) and date-picker (custom) changes.
 * handleTimeRangeChange (from composable) only fires for preset ranges;
 * for 'custom' we trigger fetchHistoryData directly when the date picker changes.
 */
const onTimeRangeChange = () => {
  handleTimeRangeChange()
  // handleTimeRangeChange skips 'custom'; trigger fetch for custom date changes
  if (timeRange.value === 'custom') fetchHistoryData()
}

/** BMS设备使用的unified-data category名称 */
const bmsCategories = [
  'total_voltage', 'current', 'rsoc', 'remaining_capacity',
  'protection_status', 'fet_status',
  'temperature_1', 'temperature_2', 'temperature_3',
  'cell_voltage_max', 'cell_voltage_min',
]

/** 逆变器设备使用的unified-data category名称 */
const inverterCategories = [
  // PV
  'pv1_voltage', 'pv1_current', 'pv1_power',
  'pv2_voltage', 'pv2_current', 'pv2_power',
  // Grid
  'grid_voltage', 'grid_frequency',
  // Output
  'output_voltage', 'output_frequency',
  'output_apparent_power', 'output_active_power', 'output_load_percent',
  // Battery
  'battery_voltage', 'battery_capacity',
  'battery_charge_current', 'battery_discharge_current', 'bus_voltage',
  // Temperature
  'pv_temp', 'inverter_temp', 'boost_temp', 'transformer_temp',
  'max_temp', 'pv2_temp', 'dc_rectifier_temp',
  // Energy
  'daily_energy', 'monthly_energy', 'yearly_energy', 'total_energy',
  // BMS
  'bms_soc', 'bms_charge_current', 'bms_discharge_current',
  'bms_charge_voltage_limit', 'bms_discharge_voltage_limit',
  'bms_charge_current_limit', 'bms_temp',
]

/**
 * BMS按量纲分组的子图表定义。
 * 每组共享同一Y轴量纲，避免多Y轴混叠。
 * 状态量(protection_status, fet_status)不放入折线图。
 */
interface ChartSubGroup {
  title: string
  categories: string[]
}
/**
 * BMS 按量纲分组。
 *
 * 2026-10-02 修正：原先把 total_voltage(~50V) 与 cell_voltage_max/min(~3.2V)
 * 放进同一个「电压 (V)」组。两者单位相同但量级差 15 倍，共用一根 Y 轴会把
 * 单体电压压成贴底直线（生产实测图：50V 曲线正常、两条 3.2V 曲线几乎不可见）。
 * 拆成两组后各自独立缩放，两条曲线都完整可读。
 */
const bmsChartGroups: ChartSubGroup[] = [
  { title: '总电压 (V)', categories: ['total_voltage'] },
  { title: '单体电压 (V)', categories: ['cell_voltage_max', 'cell_voltage_min'] },
  { title: '电流 (A)', categories: ['current'] },
  { title: '电池状态', categories: ['rsoc', 'remaining_capacity'] },
  { title: '温度 (°C)', categories: ['temperature_1', 'temperature_2', 'temperature_3'] },
]

/**
 * 逆变器按预定义量纲分组。
 *
 * 只需按"物理量族"分组即可 —— 组内的单位差异（V/A/W/Hz/%）由 LineChart 的
 * 多 Y 轴机制解决，量级差异由它的量级分轴判据解决。不要在这里按单个单位
 * 拆成十几张图，否则页面会被拉得极长。
 */
const inverterChartGroups: ChartSubGroup[] = [
  { title: 'PV输入', categories: ['pv1_voltage', 'pv1_current', 'pv1_power', 'pv2_voltage', 'pv2_current', 'pv2_power'] },
  { title: '电网', categories: ['grid_voltage', 'grid_frequency'] },
  { title: '输出', categories: ['output_voltage', 'output_frequency', 'output_apparent_power', 'output_active_power', 'output_load_percent'] },
  { title: '电池', categories: ['battery_voltage', 'battery_capacity', 'battery_charge_current', 'battery_discharge_current', 'bus_voltage'] },
  { title: '温度 (°C)', categories: ['pv_temp', 'inverter_temp', 'boost_temp', 'transformer_temp', 'max_temp', 'pv2_temp', 'dc_rectifier_temp'] },
  { title: '发电量 (kWh)', categories: ['daily_energy', 'monthly_energy', 'yearly_energy', 'total_energy'] },
  { title: 'BMS', categories: ['bms_soc', 'bms_charge_current', 'bms_discharge_current', 'bms_charge_voltage_limit', 'bms_discharge_voltage_limit', 'bms_charge_current_limit', 'bms_temp'] },
]

/** 判断是否为BMS设备类型 */
const isBmsDevice = (deviceType: string): boolean => {
  const t = deviceType.toLowerCase()
  // 同样兼容厂商前缀（jiabaida_/daly_ 等）与历史裸名；battery 是 BMS 页的别名
  return t === 'bms' || t === 'battery' || t.endsWith('_bms') || t.includes('bms')
}

/**
 * 判断是否为逆变器设备类型。
 *
 * ⚠ 2026-10-20 修：原实现只认字面量 'inverter'，而生产驱动的注册名是
 * `techfine_inverter`（见 EdgeDeviceDetailRouter 的 componentMap 注释 —— 那里
 * 已经因为同一个坑修过一次）。于是 true 的逆变器落进下方"通用设备"分支，
 * 用 SENSOR_ORDER 等通用 category 去查 unified-data，而逆变器实际写入的分类是
 * pv1_voltage / grid_voltage / battery_voltage 等 ⇒ 一条都查不到 ⇒
 * 页面永远显示「暂无历史数据」，尽管库里 24h 有 2000+ 个点。
 * 改成前缀/包含匹配，兼容厂商前缀（techfine_/growatt_ 等）与历史裸名。
 */
const isInverterDevice = (deviceType: string): boolean => {
  const t = deviceType.toLowerCase()
  return t === 'inverter' || t.endsWith('_inverter') || t.includes('inverter')
}

interface ChartSubGroupResult {
  title: string
  series: SeriesData[]
  yAxisMin?: number
  yAxisMax?: number
}

/**
 * 将chartSeries按量纲分组为子图表。
 * BMS设备使用预定义分组；非BMS设备按unit自动分组。
 * 仅在单组时计算自适应Y轴范围并传入LineChart；
 * 多组时不传（让echarts auto），因为LineChart的yAxisMin/yAxisMax是全局共享的。
 */
const chartSubGroups = computed<ChartSubGroupResult[]>(() => {
  if (chartSeries.value.length === 0) return []

  let groups: { title: string; series: SeriesData[] }[]

  if (isBmsDevice(props.deviceType)) {
    // BMS: 按预定义量纲分组
    groups = bmsChartGroups
      .map(group => {
        const series = chartSeries.value.filter(s =>
          s.category && group.categories.includes(s.category)
        )
        return { title: group.title, series }
      })
      .filter(g => g.series.length > 0)
  } else if (isInverterDevice(props.deviceType)) {
    // Inverter: 按预定义量纲分组
    groups = inverterChartGroups
      .map(group => {
        const series = chartSeries.value.filter(s =>
          s.category && group.categories.includes(s.category)
        )
        return { title: group.title, series }
      })
      .filter(g => g.series.length > 0)
  } else {
    // 非BMS: 按unit自动分组
    const unitGroups = new Map<string, SeriesData[]>()
    for (const s of chartSeries.value) {
      const unit = s.unit || ''
      if (!unitGroups.has(unit)) unitGroups.set(unit, [])
      unitGroups.get(unit)!.push(s)
    }
    groups = Array.from(unitGroups.entries()).map(([unit, series]) => ({
      title: unit ? `(${unit})` : '数据趋势',
      series,
    }))
  }

  // 自适应 Y 轴范围只在「该组内所有序列量级可比」时才传。
  //
  // 2026-10-02 生产实测：BMS 电压组同时含总电压(~50V)与单体电压(~3.2V)。
  // 传一个基于**全组最小值/最大值**算出的范围，等于强行把两条曲线压进同一根轴，
  // 3.2V 会被压成贴底的直线。此时必须交回 LineChart 按量级分轴。
  const adaptive = (series: SeriesData[]) => {
    if (series.length === 0) return {}
    const magnitudes = series.map(s => {
      let m = 0
      for (const d of s.data) {
        const abs = Math.abs(d.value)
        if (Number.isFinite(abs) && abs > m) m = abs
      }
      return m
    }).filter(m => m > 0)
    if (magnitudes.length > 1) {
      const max = Math.max(...magnitudes)
      const min = Math.min(...magnitudes)
      if (max / min >= 10) return {} // 量级不可比 ⇒ 不指定范围，让 LineChart 分轴
    }
    const allValues = series.flatMap(s => s.data.map(d => d.value))
    const { min, max } = computeAdaptiveYAxisRange(allValues)
    return { yAxisMin: min, yAxisMax: max }
  }

  return groups.map(g => ({ ...g, ...adaptive(g.series) }))
})

/** 该分组是否确实有数据点（没有就不渲染 260px 图表，避免大片空白） */
function groupHasData(group: ChartSubGroupResult): boolean {
  return group.series.some(s => Array.isArray(s.data) && s.data.length > 0)
}

/** 空态展示用的时间范围文案（与 TimeRangeSelector 的档位一一对应） */
const timeRangeLabel = computed(() => {
  const map: Record<string, string> = {
    '1h': '最近 1 小时', '24h': '最近 24 小时', '7d': '最近 7 天',
  }
  if (timeRange.value === 'custom') {
    const [s, e] = getTimeRange()
    return s.toLocaleString('zh-CN') + ' ~ ' + e.toLocaleString('zh-CN')
  }
  return map[timeRange.value] || timeRange.value
})

/** 空态展示用的查询范围说明（告诉用户查了哪些物理量，便于判断是没采到还是选错范围） */
const queryScopeHint = computed(() => {
  const t = props.deviceType
  if (isBmsDevice(t)) return 'BMS 电压 / 电流 / SOC / 温度 / 单体电压'
  if (isInverterDevice(t)) return '逆变器 PV / 电网 / 输出 / 电池 / 温度 / 发电量'
  return '通用传感器分类'
})

async function fetchHistoryData() {
  if (!props.deviceId) return
  const [startTime, endTime] = getTimeRange()
  const deviceType = props.deviceType

  chartLoading.value = true
  try {
    // Unified data devices: query specific categories via batch API
    if (['bmp280', 'sht40', 'temp_humidity'].includes(deviceType)) {
      const categories = deviceType === 'bmp280' ? ['temperature', 'pressure'] : ['temperature', 'humidity']
      const result = await historyFetch(props.deviceId, categories, startTime, endTime)
      chartSeries.value = result
      historyData.value = []
    } else {
      // Generic: try all known categories via unified-data batch API
      const knownCategories = isBmsDevice(deviceType)
        ? bmsCategories
        : isInverterDevice(deviceType)
          ? inverterCategories
          : [...SENSOR_ORDER, 'illuminance', 'uv_index', 'rain_intensity', 'rain_accum',
            'voltage', 'current', 'power', 'energy', 'soc', 'soh', 'frequency',
            'cell_voltage', 'cell_temp', 'mos_status', 'protection_status']

      const result = await historyFetch(props.deviceId, knownCategories, startTime, endTime)

      if (result.length > 0) {
        chartSeries.value = result
        historyData.value = []
      } else {
        // Fallback: device_data table
        const response = await edgeDeviceApi.getHistoryData(props.deviceId, {
          start_time: startTime.toISOString(), end_time: endTime.toISOString(), page: 1, page_size: 1000
        })
        chartSeries.value = []
        /*
         * ⚠ 2026-10-20 实测崩溃：`response` 可能是 **null**（后端该端点在无数据/
         * 参数异常时返回 data:null），旧代码直接 response.items ⇒
         * "TypeError: Cannot read properties of null (reading 'items')"，
         * 被 catch 后弹「获取历史数据失败」，用户看到的是报错而不是空态。
         * 这里先把响应收敛成数组，缺失即空态（空态是正常业务状态，不是错误）。
         */
        const items = (response && Array.isArray(response.items)) ? response.items : []
        if (items.length > 0) {
          const firstItem = items[0]
          const data = firstItem.data || {}
          // 过滤掉状态量字段
          const numericKeys = Object.keys(data)
            .filter(key => key !== 'raw_data' && typeof data[key] === 'number')
            .filter(key => !statusCategories.includes(key))
          if (numericKeys.length > 1) {
            chartSeries.value = numericKeys.map(key => ({
              name: sensorNameMap[key] || key, unit: sensorUnitMap[key] || '', category: key,
              data: items.map((item: any) => ({ time: item.created_at || item.collected_at, value: item.data[key] ?? 0 }))
            }))
            historyData.value = []
          } else {
            const valueKey = numericKeys[0] || Object.keys(data).find(key => typeof data[key] === 'number' && !statusCategories.includes(key))
            historyData.value = valueKey
              ? items.map((item: any) => ({ time: item.created_at || item.collected_at, value: item.data[valueKey] }))
              : []
          }
        } else {
          historyData.value = []
        }
      }
    }
  } catch (error: any) {
    logger.error('获取历史数据失败', { error: String(error) })
    feedback.handleError(error, '获取历史数据失败')
  } finally {
    chartLoading.value = false
  }
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
/* 空态说明：给出「查了什么范围 + 可能原因」，而不是只说「暂无」 */
.chart-sub-empty {
  margin: 0;
  padding: 18px 0;
  text-align: center;
  font-size: 12px;
  color: var(--el-text-color-placeholder);
  background: var(--el-fill-color-lighter);
  border-radius: 6px;
}
.empty-desc { text-align: center; line-height: 1.7; }
.empty-title { margin: 0; font-size: 14px; color: var(--el-text-color-regular); }
.empty-hint { margin: 4px 0 0; font-size: 12px; color: var(--el-text-color-secondary); }
.header-controls { display: flex; align-items: center; }
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
