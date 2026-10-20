<template>
  <section class="charts-row">
    <!-- 图1 电芯电压历史趋势：业务逻辑参考旧页 BmsCellVoltageHistoryChart -->
    <div class="card chart-card">
      <div class="card-head">
        <span class="card-title">电芯电压历史趋势</span>
        <span class="card-hint">{{ cellHint }}</span>
        <!-- 窄屏：16 个勾选框收进弹层（触发器常显「已选 N/16」）。
             桌面端 v-if 为 false ⇒ 原结构完全不变（16 个 checkbox 仍平排）。 -->
        <el-popover
          v-if="isMobileViewport && cellAll.length > 0"
          placement="bottom-end"
          :width="300"
          trigger="click"
          popper-class="bms-cell-picker-popover"
        >
          <template #reference>
            <!-- 触摸目标：theme.css 的 `.el-button--small:not(...)` 规则（0,4,0）会把小按钮压到 36px，
                 组件 scoped 规则（0,2,0）赢不了 ⇒ 这里用内联 44px（该触发器只在窄屏渲染，桌面无影响） -->
            <el-button class="cell-picker-trigger" size="small" style="min-height: 44px" aria-label="选择要显示的电芯">
              {{ cellPickLabel }}
            </el-button>
          </template>
          <div class="cell-picker-pop">
            <p class="cell-picker-pop-title">选择电芯（{{ cellPickLabel }}）</p>
            <el-checkbox-group v-model="cellPicked" size="small" class="cell-picker-pop-group">
              <el-checkbox v-for="i in cellCount" :key="i" :value="i" :label="'#' + i" border size="small" />
            </el-checkbox-group>
            <div class="cell-picker-pop-actions">
              <el-button size="small" text style="min-height: 44px" @click="pickAll">全选</el-button>
              <el-button size="small" text style="min-height: 44px" @click="invertPick">反选</el-button>
            </div>
          </div>
        </el-popover>
        <span class="head-right">
          <TimeRangeSelector v-model="cellRange" v-model:custom-range="cellCustom" @change="onCellRange" />
        </span>
      </div>
      <div v-if="cellLoading" class="chart-state">正在加载历史数据…</div>
      <div v-else-if="!online" class="chart-state">设备离线，历史数据暂不可用</div>
      <!-- 失败可见（契约 §2.3 / 根因 R4）：请求失败且无数据时给错误态 + 重试，
           不能静默退化成「暂无数据」——那会让用户误判「设备真没数据」 -->
      <div v-else-if="cellError" class="chart-state is-error" role="alert">
        <span>历史数据加载失败，请稍后重试</span>
        <el-button link size="small" type="primary" @click="fetchCells">重试</el-button>
      </div>
      <div v-else-if="cellAll.length === 0" class="chart-state">暂无电芯电压历史数据</div>
      <template v-else>
        <!-- 桌面：16 个勾选框平排（契约：1440 下结构与改前一致） -->
        <div v-if="!isMobileViewport" class="cell-picker">
          <el-checkbox-group v-model="cellPicked" size="small">
            <el-checkbox v-for="i in cellCount" :key="i" :value="i" :label="'#' + i" border size="small" />
          </el-checkbox-group>
          <el-button size="small" text @click="pickAll">全选</el-button>
          <el-button size="small" text @click="invertPick">反选</el-button>
        </div>
        <div class="chart-box">
          <LineChart
            :series="cellShown"
            :y-axis-min="cellY.min"
            :y-axis-max="cellY.max"
            :show-area="false"
            :height="isMobileViewport ? '160px' : '280px'"
          />
        </div>
      </template>
    </div>

    <!-- 图2 运行趋势：按量纲分组（参考旧页 HistoryChartSection 的 bmsChartGroups）。
         总电压(~53V) 与单体电压(~3.3V) 同为 V 但量级差 16 倍，共用一根 Y 轴会把单体压成贴底直线，
         故拆成独立子图；状态量 protection_status / fet_status 不适合折线，沿用旧页判断不纳入。
         窄屏：5 子图平铺改为「单图 + 量纲切换器」（el-select），桌面仍保留 5 子图平铺。 -->
    <div class="card chart-card">
      <div class="card-head">
        <span class="card-title">运行趋势</span>
        <span class="card-hint">按量纲分组（总电压 / 单体电压 / 电流 / 电池状态 / 温度），状态量未纳入</span>
        <span class="head-right">
          <el-button v-if="metricError" size="small" text type="primary" class="retry-btn" @click="fetchMetrics">重试</el-button>
          <TimeRangeSelector v-model="metricRange" v-model:custom-range="metricCustom" @change="onMetricRange" />
        </span>
      </div>
      <!-- 桌面契约：恒定 5 个分组槽位，组数不随数据到达而变，页面高度稳定
           （本页依赖 overflow-anchor:none 防跳动） -->
      <div v-if="!isMobileViewport" class="trend-grid">
        <div v-for="g in trendGroups" :key="g.title" class="trend-sub">
          <p class="trend-sub-title">{{ g.title }}</p>
          <div class="trend-sub-body">
            <div v-if="metricLoading" class="trend-sub-state is-loading">加载中…</div>
            <div v-else-if="!online" class="trend-sub-state">设备离线</div>
            <div v-else-if="metricError" class="trend-sub-state is-error" role="alert">历史数据加载失败，请稍后重试</div>
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
      <!-- 窄屏：单图 + 量纲切换器。容器固定高度 ⇒ loading/empty/offline 也不撑变卡片高度 -->
      <div
        v-else
        class="trend-mobile"
        :data-dim="activeDim"
        :data-series-count="mobileSeriesCount"
        :data-point-count="mobilePointCount"
      >
        <div class="trend-dim-bar">
          <el-select
            v-model="activeDim"
            class="trend-dim-select"
            size="small"
            popper-class="bms-trend-dim-dropdown"
            aria-label="切换运行趋势量纲"
            @change="onDimChange"
          >
            <el-option v-for="d in trendGroupDefs" :key="d.key" :value="d.key" :label="d.label" />
          </el-select>
        </div>
        <div class="trend-mobile-chart">
          <div v-if="metricLoading" class="trend-sub-state is-loading">加载中…</div>
          <div v-else-if="!online" class="trend-sub-state">设备离线</div>
          <div v-else-if="metricError" class="trend-sub-state is-error" role="alert">历史数据加载失败，请稍后重试</div>
          <LineChart
            v-else-if="mobileGroup && mobileGroup.series.length"
            :series="mobileGroup.series"
            :y-axis-min="mobileGroup.yAxisMin"
            :y-axis-max="mobileGroup.yAxisMax"
            :show-area="false"
            height="196px"
          />
          <div v-else class="trend-sub-state">暂无该项历史数据</div>
        </div>
      </div>
    </div>
  </section>
</template>
<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import LineChart from '@/components/charts/LineChart.vue'
import TimeRangeSelector from '@/components/charts/TimeRangeSelector.vue'
import { useHistoryData } from '@/composables/useHistoryData'
import { withHistorySlot } from '@/composables/useHistoryQueue'
import { useTimeRange } from '@/composables/useTimeRange'
import { computeAdaptiveYAxisRange } from '@/utils/chartRange'
import type { SeriesData } from '@/types/chart'

const props = withDefaults(defineProps<{ deviceId: number; cellCount?: number; online?: boolean }>(), { cellCount: 16, online: true })

/* ---- 视口判定（≤768px 走移动端布局）----
 * 为什么用 JS 判定而不是纯 CSS：窄屏要**结构**变化（5 子图 → 单图 + 量纲切换器、
 * 16 checkbox → 弹层），CSS 只能隐藏不能减少节点；被 display:none 藏起来的 5 张图
 * 仍会创建 echarts 实例、仍会进读屏与 Tab 顺序，属于可访问性与性能双重缺陷。
 * SSR/测试环境没有 window.matchMedia 时按桌面处理，保证既有桌面契约测试稳定。 */
const MOBILE_MAX = 768
const isMobileViewport = ref(
  typeof window !== 'undefined' && typeof window.matchMedia === 'function'
    ? window.matchMedia(`(max-width: ${MOBILE_MAX}px)`).matches
    : false,
)
let mobileMql: MediaQueryList | null = null
const onMobileChange = (e: MediaQueryListEvent) => { isMobileViewport.value = e.matches }

/* ---- 图1 电芯电压历史（逻辑同旧页 BmsCellVoltageHistoryChart） ---- */
const cellCats = computed(() => Array.from({ length: props.cellCount }, (_, i) => 'cell_voltage_' + (i + 1)))
// 历史查询超时放宽到 30s：24h×16 类本就很重，全局 10s 会把正常慢请求误判为失败并触发降级风暴；
// 只覆盖历史 API，client.ts 全局 10s 不动（契约 §2.3）
const { loading: cellLoading, error: cellError, fetch: cellFetch } = useHistoryData({
  errorPrefix: '获取电芯电压历史数据失败',
  timeoutMs: 30000,
})
const cellAll = ref<SeriesData[]>([])
const cellPicked = ref<number[]>([])
/** 图1 的取消器：切时间范围/重复触发/卸载时 abort 旧请求（旧响应不得覆盖新响应） */
let cellController: AbortController | null = null

async function fetchCells() {
  if (!props.deviceId) return
  cellController?.abort()
  const controller = new AbortController()
  cellController = controller
  const [start, end] = getCellRange()
  // 与诊断卡共用页面级历史请求闸门（并发 ≤3），不再和另外 5 个重查询一起打满后端
  const res = await withHistorySlot(() =>
    cellFetch(props.deviceId, cellCats.value, start, end, { signal: controller.signal }),
  )
  // 已被更新的请求取代（或组件已卸载）：丢弃本次结果
  if (controller.signal.aborted) return
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
/** 窄屏弹层触发器文案：不带「串」后缀，控制触发器宽度（≤44px 高，文案单行不截断）。 */
const cellPickLabel = computed(() => '已选 ' + cellShown.value.length + '/' + cellAll.value.length)

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
 * key/label 供窄屏量纲切换器使用；title 供桌面子图标题使用（两者不可混用，否则桌面标题会变）。
 */
interface TrendDimDef { key: string; label: string; title: string; categories: string[] }
const trendGroupDefs: TrendDimDef[] = [
  { key: 'total_voltage', label: '总电压', title: '总电压 (V)', categories: ['total_voltage'] },
  { key: 'cell_voltage', label: '单体电压', title: '单体电压 (V)', categories: ['cell_voltage_max', 'cell_voltage_min'] },
  { key: 'current', label: '电流', title: '电流 (A)', categories: ['current'] },
  { key: 'status', label: '电池状态', title: '电池状态', categories: ['rsoc', 'remaining_capacity'] },
  { key: 'temperature', label: '温度', title: '温度 (℃)', categories: ['temperature_1', 'temperature_2', 'temperature_3'] },
]

const { loading: metricLoading, error: metricError, fetch: metricFetch } = useHistoryData({
  nameMap: metricNameMap,
  unitMap: metricUnitMap,
  errorPrefix: '获取运行趋势数据失败',
  timeoutMs: 30000,
})
const metricAll = ref<SeriesData[]>([])
/** 图2 的取消器（与图1 各自独立：一张图切范围不应取消另一张的请求） */
let metricController: AbortController | null = null

async function fetchMetrics() {
  if (!props.deviceId) return
  metricController?.abort()
  const controller = new AbortController()
  metricController = controller
  const [start, end] = getMetricRange()
  const res = await withHistorySlot(() =>
    metricFetch(props.deviceId, metricDefs.map((m) => m.cat), start, end, { signal: controller.signal }),
  )
  if (controller.signal.aborted) return
  metricAll.value = res.map((s) => ({ name: s.name, unit: s.unit, category: s.category, data: s.data }))
}
const { timeRange: metricRange, customTimeRange: metricCustom, getTimeRange: getMetricRange, handleTimeRangeChange: metricRangeChanged } = useTimeRange(fetchMetrics)
function onMetricRange() {
  metricRangeChanged()
  if (metricRange.value === 'custom' && metricCustom.value) void fetchMetrics()
}

interface TrendGroupView { key: string; label: string; title: string; series: SeriesData[]; yAxisMin?: number; yAxisMax?: number }

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
    return { key: g.key, label: g.label, title: g.title, series, ...adaptiveRange(series) }
  }),
)

/* ---- 窄屏量纲切换器 ----
 * 默认选中「第一个有数据的分组」；用户手动切换后不再被数据到达覆盖（dimTouched）。
 * 数据仍一次拉取全部 5 组，切换不重新请求。 */
const activeDim = ref(trendGroupDefs[0].key)
const dimTouched = ref(false)
function onDimChange() { dimTouched.value = true }
watch(
  () => trendGroups.value.map((g) => g.series.length).join(','),
  () => {
    if (dimTouched.value) return
    const firstWithData = trendGroups.value.find((g) => g.series.length > 0)
    activeDim.value = firstWithData ? firstWithData.key : trendGroupDefs[0].key
  },
  { immediate: true },
)
const mobileGroup = computed<TrendGroupView | undefined>(
  () => trendGroups.value.find((g) => g.key === activeDim.value) ?? trendGroups.value[0],
)
const mobileSeriesCount = computed(() => mobileGroup.value?.series.length ?? 0)
/** 当前量纲实际绘制的点数（探针/测试用：>0 才证明横轴真有数据，不是空图） */
const mobilePointCount = computed(
  () => (mobileGroup.value?.series ?? []).reduce((n, s) => n + s.data.length, 0),
)

onMounted(() => {
  // 监听视口变化：横竖屏切换/桌面窗口缩放时在「5 子图平铺」与「单图 + 切换器」间切换
  if (typeof window !== 'undefined' && typeof window.matchMedia === 'function') {
    mobileMql = window.matchMedia(`(max-width: ${MOBILE_MAX}px)`)
    mobileMql.addEventListener('change', onMobileChange)
    isMobileViewport.value = mobileMql.matches
  }
  void fetchCells()
  void fetchMetrics()
})

onUnmounted(() => {
  // 必须解绑：不解绑会在路由切换后留下指向已销毁组件的监听（内存泄漏）
  mobileMql?.removeEventListener('change', onMobileChange)
  mobileMql = null
  // 卸载后不再需要响应：abort 在途请求，避免它们继续占用页面级并发名额
  cellController?.abort()
  metricController?.abort()
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
/* 失败态：与「暂无数据」必须一眼可分（颜色 + 重试按钮），高度不变避免页面跳动 */
.chart-state.is-error {
  flex-direction: column;
  gap: 8px;
  color: var(--el-color-danger);
  text-align: center;
  padding: 0 16px;
  box-sizing: border-box;
}
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
/*
  空态高度（2026-10-20 实测修正）：
  旧规则固定 height:200px，本意是与图表等高、数据到达时不跳动。
  但实测：5 个分组**全部无数据**时，卡片仍占 5×200px ≈ 1000px 纯空白
  （BMS 页「运行趋势」卡实测 831px，其中约 1000px 是空态占位），
  页面被拉得极长，且用户看不出"是没数据还是没加载"。

  取舍：单空态改紧凑 48px；但**加载中**仍保持 200px ——
  加载位置是用户已预期要等的地方，保留占位避免数据到达时二次跳动。
  数据从无到有那一次的跳动发生在首屏加载期，用户对页面变化有预期。
*/
.trend-sub-state {
  min-height: 48px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--el-text-color-secondary);
  font-size: 12px;
}
.trend-sub-state.is-loading { min-height: 200px; }
/* 失败态用 danger 色，和「暂无该项历史数据」的灰色空态区分开（文案也不同） */
.trend-sub-state.is-error { color: var(--el-color-danger); }

/* ---- 窄屏独有：电芯选择弹层触发器 + 运行趋势单图 + 量纲切换器 ---- */
.cell-picker-trigger { flex: 0 0 auto; }
.cell-picker-pop { display: flex; flex-direction: column; gap: 8px; }
.cell-picker-pop-title { margin: 0; font-size: 12px; color: var(--el-text-color-secondary); }
.cell-picker-pop-group { display: flex; flex-wrap: wrap; gap: 8px; }
.cell-picker-pop-actions { display: flex; justify-content: flex-end; gap: 4px; }
/* 弹层内容根节点带本组件 scope id（teleport 不丢 scoped），但弹层外层 popper 不带 ⇒ 宽度约束走 :global */
:global(.bms-cell-picker-popover) { max-width: calc(100vw - 24px); }
:global(.bms-cell-picker-popover .el-checkbox) { margin-right: 0; }
.trend-dim-bar { display: flex; }
.trend-dim-select { width: 100%; }
/* 量纲切换器的选项文案必须完整可读（不 ellipsis 截断）：
   Element Plus 的 .el-select__selected-item / .el-select-dropdown__item 默认 text-overflow: ellipsis，
   即使当前 5 个量纲名都很短（≤4 字）也会被截断扫描判为「可能丢字」，故显式收回。 */
.trend-dim-select :deep(.el-select__selected-item) { text-overflow: clip; }
:global(.bms-trend-dim-dropdown .el-select-dropdown__item) { text-overflow: clip; }
:global(.bms-trend-dim-dropdown) { max-width: calc(100vw - 24px); }
.trend-mobile-chart { height: 196px; }
.trend-mobile-chart .trend-sub-state { min-height: 0; height: 100%; }

@media (max-width: 1100px) {
  .trend-grid { grid-template-columns: minmax(0, 1fr); }
}
@media (max-width: 768px) {
  /* 与父页 .card 的窄屏内距一致；上下收到 12px 为「单图 + 切换器」腾高度 */
  .chart-card { padding: 12px; }
  .card-head { margin-bottom: 4px; }
  /* 窄屏标题行空间让给量纲切换器/电芯选择触发器：
     静态说明文字（按量纲分组说明、已选 N/16 串）信息已分别由切换器选项与触发器承载，
     不是数据本身 —— 数值/保护名称等业务数据一律不隐藏。 */
  .card-hint { display: none; }
  .chart-box { height: 160px; }
  .chart-state { height: 160px; }
  .trend-dim-bar { margin-bottom: 4px; }
  .trend-dim-select :deep(.el-select__wrapper) { min-height: 44px; font-size: 14px; }
  /* 触发器/弹层勾选框：窄屏触摸目标 ≥44px（高密度弹层勾选框允许 36px，间距补足） */
  .cell-picker-trigger { min-height: 44px; }
  .cell-picker-pop :deep(.el-checkbox) { min-height: 36px; }
  .cell-picker-pop :deep(.el-checkbox__label) { font-size: 13px; }
  .head-right :deep(.el-radio-button__inner) { min-height: 44px; padding: 0 12px; }
}
@media (pointer: coarse) {
  /* 粗指针（触屏笔记本在 1440px 也走这一支）：恢复 44px 触摸目标 */
  .head-right :deep(.el-radio-button__inner) { min-height: 44px; padding: 0 12px; }
}
</style>
