<template>
  <section class="charts-row">
    <!-- 图1 电芯电压历史趋势：业务逻辑参考旧页 BmsCellVoltageHistoryChart -->
    <div class="card chart-card">
      <div class="card-head">
        <span class="card-title">电芯电压历史趋势</span>
        <span class="card-hint">{{ cellHint }}</span>
        <span class="head-right">
          <TimeRangeSelector v-model="cellRange" v-model:custom-range="cellCustom" @change="onCellRange" />
        </span>
      </div>
      <div v-if="cellLoading" class="chart-state">正在加载历史数据…</div>
      <div v-else-if="!online" class="chart-state">设备离线，历史数据暂不可用</div>
      <div v-else-if="cellAll.length === 0" class="chart-state">暂无电芯电压历史数据</div>
      <template v-else>
        <div class="cell-picker">
          <el-checkbox-group v-model="cellPicked" size="small">
            <el-checkbox v-for="i in cellCount" :key="i" :value="i" :label="'#' + i" border size="small" />
          </el-checkbox-group>
          <el-button size="small" text @click="pickAll">全选</el-button>
          <el-button size="small" text @click="invertPick">反选</el-button>
        </div>
        <div class="chart-box">
          <LineChart :series="cellShown" :y-axis-min="cellY.min" :y-axis-max="cellY.max" :show-area="false" height="280px" />
        </div>
      </template>
    </div>

    <!-- 图2 运行趋势：按量纲分组（参考旧页 HistoryChartSection 的 bmsChartGroups）。
         总电压(~53V) 与单体电压(~3.3V) 同为 V 但量级差 16 倍，共用一根 Y 轴会把单体压成贴底直线，
         故拆成独立子图；状态量 protection_status / fet_status 不适合折线，沿用旧页判断不纳入。 -->
    <div class="card chart-card">
      <div class="card-head">
        <span class="card-title">运行趋势</span>
        <span class="card-hint">按量纲分组（总电压 / 单体电压 / 电流 / 电池状态 / 温度），状态量未纳入</span>
        <span class="head-right">
          <TimeRangeSelector v-model="metricRange" v-model:custom-range="metricCustom" @change="onMetricRange" />
        </span>
      </div>
      <!-- 恒定 5 个分组槽位：组数不随数据到达而变，页面高度稳定（本页依赖 overflow-anchor:none 防跳动） -->
      <div class="trend-grid">
        <div v-for="g in trendGroups" :key="g.title" class="trend-sub">
          <p class="trend-sub-title">{{ g.title }}</p>
          <div class="trend-sub-body">
            <div v-if="metricLoading" class="trend-sub-state">加载中…</div>
            <div v-else-if="!online" class="trend-sub-state">设备离线</div>
            <LineChart
              v-else-if="g.series.length"
              :series="g.series"
              :y-axis-min="g.yAxisMin"
              :y-axis-max="g.yAxisMax"
              :show-area="false"
              height="200px"
            />
            <div v-else class="trend-sub-state">暂无该项历史数据</div>
          </div>
        </div>
      </div>
    </div>
  </section>
</template>
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import LineChart from '@/components/charts/LineChart.vue'
import TimeRangeSelector from '@/components/charts/TimeRangeSelector.vue'
import { useHistoryData } from '@/composables/useHistoryData'
import { useTimeRange } from '@/composables/useTimeRange'
import { computeAdaptiveYAxisRange } from '@/utils/chartRange'
import type { SeriesData } from '@/types/chart'

const props = withDefaults(defineProps<{ deviceId: number; cellCount?: number; online?: boolean }>(), { cellCount: 16, online: true })

/* ---- 图1 电芯电压历史（逻辑同旧页 BmsCellVoltageHistoryChart） ---- */
const cellCats = computed(() => Array.from({ length: props.cellCount }, (_, i) => 'cell_voltage_' + (i + 1)))
const { loading: cellLoading, fetch: cellFetch } = useHistoryData({ errorPrefix: '获取电芯电压历史数据失败' })
const cellAll = ref<SeriesData[]>([])
const cellPicked = ref<number[]>([])

async function fetchCells() {
  if (!props.deviceId) return
  const [start, end] = getCellRange()
  const res = await cellFetch(props.deviceId, cellCats.value, start, end)
  cellAll.value = cellCats.value
    .map((cat, i) => {
      const hit = res.find((x) => x.category === cat)
      return { name: '#' + (i + 1), unit: 'V', category: cat, cellNumber: i + 1, data: hit?.data ?? [] }
    })
    .filter((s) => s.data.length > 0)
  if (cellPicked.value.length === 0) pickAll()
}
const { timeRange: cellRange, customTimeRange: cellCustom, getTimeRange: getCellRange, handleTimeRangeChange: cellRangeChanged } = useTimeRange(fetchCells)
function onCellRange() {
  cellRangeChanged()
  if (cellRange.value === 'custom' && cellCustom.value) void fetchCells()
}

const cellShown = computed<SeriesData[]>(() => {
  if (cellPicked.value.length === 0) return []
  return cellAll.value.filter((s) => cellPicked.value.includes(s.cellNumber as number))
})
function pickAll() {
  cellPicked.value = cellAll.value.map((s) => s.cellNumber as number)
}
function invertPick() {
  const all = cellAll.value.map((s) => s.cellNumber as number)
  cellPicked.value = all.filter((i) => !cellPicked.value.includes(i))
}
const cellY = computed(() => {
  const vals = cellShown.value.flatMap((s) => s.data.map((d) => d.value)).filter((v) => typeof v === 'number' && v > 0)
  if (vals.length === 0) return { min: 2.5, max: 4.0 }
  return computeAdaptiveYAxisRange(vals, { minBound: 2.0, maxBound: 4.5 })
})
const cellHint = computed(() => '已选 ' + cellShown.value.length + '/' + cellAll.value.length + ' 串')

/* ---- 图2 运行趋势：按量纲分组（逻辑同旧页 HistoryChartSection 的 bmsChartGroups） ----
 * 状态量 protection_status / fet_status 不适合折线，沿用旧页判断，不纳入。 */
const metricDefs = [
  { cat: 'total_voltage', label: '总电压', unit: 'V' },
  { cat: 'current', label: '电流', unit: 'A' },
  { cat: 'rsoc', label: 'SOC', unit: '%' },
  { cat: 'remaining_capacity', label: '剩余容量', unit: 'Ah' },
  { cat: 'temperature_1', label: '温度1', unit: '℃' },
  { cat: 'temperature_2', label: '温度2', unit: '℃' },
  { cat: 'temperature_3', label: '温度3', unit: '℃' },
  { cat: 'cell_voltage_max', label: '最高单体', unit: 'V' },
  { cat: 'cell_voltage_min', label: '最低单体', unit: 'V' },
]
const metricNameMap = Object.fromEntries(metricDefs.map((m) => [m.cat, m.label]))
const metricUnitMap = Object.fromEntries(metricDefs.map((m) => [m.cat, m.unit]))

/**
 * 量纲分组 —— 与旧页 HistoryChartSection.bmsChartGroups 一一对应。
 * 同组共享一根 Y 轴；跨组不共轴，避免 53V 把 3.3V 压成贴底直线。
 */
const trendGroupDefs: Array<{ title: string; categories: string[] }> = [
  { title: '总电压 (V)', categories: ['total_voltage'] },
  { title: '单体电压 (V)', categories: ['cell_voltage_max', 'cell_voltage_min'] },
  { title: '电流 (A)', categories: ['current'] },
  { title: '电池状态', categories: ['rsoc', 'remaining_capacity'] },
  { title: '温度 (℃)', categories: ['temperature_1', 'temperature_2', 'temperature_3'] },
]

const { loading: metricLoading, fetch: metricFetch } = useHistoryData({
  nameMap: metricNameMap,
  unitMap: metricUnitMap,
  errorPrefix: '获取运行趋势数据失败',
})
const metricAll = ref<SeriesData[]>([])

async function fetchMetrics() {
  if (!props.deviceId) return
  const [start, end] = getMetricRange()
  const res = await metricFetch(props.deviceId, metricDefs.map((m) => m.cat), start, end)
  metricAll.value = res.map((s) => ({ name: s.name, unit: s.unit, category: s.category, data: s.data }))
}
const { timeRange: metricRange, customTimeRange: metricCustom, getTimeRange: getMetricRange, handleTimeRangeChange: metricRangeChanged } = useTimeRange(fetchMetrics)
function onMetricRange() {
  metricRangeChanged()
  if (metricRange.value === 'custom' && metricCustom.value) void fetchMetrics()
}

interface TrendGroupView { title: string; series: SeriesData[]; yAxisMin?: number; yAxisMax?: number }

/**
 * 自适应 Y 轴范围只在「组内各序列量级可比」时才给。
 * 判据同旧页：量级比 ≥10 ⇒ 不指定范围，交给 LineChart 的量级分轴机制。
 */
function adaptiveRange(series: SeriesData[]): { yAxisMin?: number; yAxisMax?: number } {
  if (series.length === 0) return {}
  const magnitudes = series
    .map((s) => s.data.reduce((m, d) => {
      const abs = Math.abs(d.value)
      return Number.isFinite(abs) && abs > m ? abs : m
    }, 0))
    .filter((m) => m > 0)
  if (magnitudes.length > 1) {
    const max = Math.max(...magnitudes)
    const min = Math.min(...magnitudes)
    if (min > 0 && max / min >= 10) return {}
  }
  const all = series.flatMap((s) => s.data.map((d) => d.value)).filter((v) => Number.isFinite(v))
  if (all.length === 0) return {}
  const { min, max } = computeAdaptiveYAxisRange(all)
  return { yAxisMin: min, yAxisMax: max }
}

const trendGroups = computed<TrendGroupView[]>(() =>
  trendGroupDefs.map((g) => {
    const series = metricAll.value.filter((s) => s.category && g.categories.includes(s.category))
    return { title: g.title, series, ...adaptiveRange(series) }
  }),
)

onMounted(() => {
  void fetchCells()
  void fetchMetrics()
})

defineExpose({ fetchCells, fetchMetrics })
</script>
<style scoped>
/* ⚠ 父页的 .row / .card / .card-head 都是 **scoped**，子组件（本文件）拿不到这些规则，
   所以这里必须复刻同一组数值；否则节间距、卡片内距会与页面其它组件漂移。 */
.charts-row {
  display: flex;
  flex-wrap: wrap;
  gap: 16px;
  /* 与父页 .row 一致：靠下边距形成 16px 节间距。
     旧写法 margin-top:20px 且无下边距 —— 上间距比别处大、末尾又漏掉间距。 */
  margin-bottom: 16px;
  align-items: flex-start;
}
.chart-card {
  flex: 1 1 100%;
  min-width: 0;
  background: var(--card-bg);
  border: 1px solid var(--card-border);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-sm);
  padding: 16px 20px;
  box-sizing: border-box;
}
.card-head { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; margin-bottom: 10px; }
.card-title { font-weight: 600; font-size: 14px; color: var(--el-text-color-primary); }
.card-hint { font-size: 12px; color: var(--el-text-color-secondary); }
.head-right { margin-left: auto; display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
/* 时间范围分段控件（TimeRangeSelector）对齐卡片右上角其它 el-button size=small。
   全局 theme.css 给 `.el-radio-button__inner { min-height:36px }`（触摸目标），副作用：
   ① min-height 把盒子撑高、文字却仍贴上沿（看起来没居中）；② 36px 比其它右上角 small 按钮(24px)大一号。
   这里在**本组件作用域内**收回 small 档并用 flex 垂直居中——不改全局 theme.css、不改共享组件；
   窄屏 / 粗指针仍升到 44px，与页面其它控件一致。 */
.head-right :deep(.el-radio-button__inner) {
  min-height: 24px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 0 11px;
  font-size: 12px;
  line-height: 1;
}
/* 固定高度：内容变化不得撑变页面高度（本页依赖 overflow-anchor:none 防跳动） */
.chart-box { height: 280px; }
.chart-state { height: 280px; display: flex; align-items: center; justify-content: center; color: var(--el-text-color-secondary); font-size: 13px; }
.cell-picker { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; margin-bottom: 10px; }

.trend-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 8px; }
/* 子图容器与页面「受控操作分组」(.op-group) 同一语言：浅描边 + 8px 圆角 + 灰底标题条。
   不用 fill-color-blank（白底）+ 12px 内距 —— 那是旧页 HistoryChartSection 的写法，
   与本页其它内层块不是一套。 */
.trend-sub {
  min-width: 0;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: var(--radius-sm);
  overflow: hidden;
}
.trend-sub-title {
  margin: 0;
  padding: 8px 12px;
  font-size: 13px;
  font-weight: 600;
  color: var(--el-text-color-primary);
  background: var(--el-fill-color-lighter);
}
.trend-sub-body { padding: 8px 12px 4px; }
.trend-sub-state { height: 200px; display: flex; align-items: center; justify-content: center; color: var(--el-text-color-secondary); font-size: 12px; }

@media (max-width: 1100px) {
  .trend-grid { grid-template-columns: minmax(0, 1fr); }
}
@media (max-width: 768px) {
  /* 与父页 .card 的窄屏内距一致 */
  .chart-card { padding: 14px 12px; }
  .head-right :deep(.el-radio-button__inner) { min-height: 44px; padding: 0 12px; }
}
@media (pointer: coarse) {
  /* 粗指针（触屏笔记本在 1440px 也走这一支）：恢复 44px 触摸目标 */
  .head-right :deep(.el-radio-button__inner) { min-height: 44px; padding: 0 12px; }
}
</style>
