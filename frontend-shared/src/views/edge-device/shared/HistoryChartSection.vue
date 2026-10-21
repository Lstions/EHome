<template>
  <el-card style="margin-top: 20px;" shadow="hover">
    <template #header>
      <!-- hist-header 仅承载窄屏 @media 规则（桌面无任何基础样式 ⇒ 1440 像素不变） -->
      <div class="hist-header">
        <span class="hist-title">历史数据</span>
        <div class="header-controls">
          <TimeRangeSelector
            v-model="timeRange"
            v-model:custom-range="customTimeRange"
            @change="onTimeRangeChange"
          />
          <el-button
            type="primary"
            class="hist-export"
            @click="handleExportCSV"
            :disabled="historyData.length === 0 && chartSeries.length === 0"
          >
            <el-icon :size="16"><Download /></el-icon>
            导出CSV
          </el-button>
        </div>
      </div>
    </template>

    <el-skeleton v-if="chartLoading" :rows="isMobileViewport ? 3 : 8" animated />
    <template v-else>
      <!--
        2026-10-20 修（实测：BMS「运行趋势」卡在**全部子图无数据**时仍高达 831px）：
        旧模板对每个分组都渲染一个 height=260px 的 LineChart，即便该组 series 为空；
        5 个空组 = 1300px 的空白图表区，把页面拉得极长且看不出哪里有问题。
        现在：只在**该组确实有数据点**时才渲染图表，否则渲染一行紧凑的空态文案。
      -->
      <!--
        2026-10-20 再修（393 生产实测：本卡 2441px / 内部 7 个 277×260 画布 / 图例翻页「◀ 1/5 ▶」/
        X 轴刻度挤成乱码 / 轴名「(V)(Hz)(W)」压图例）：
        窄屏（≤768px）改「**单图 + 分组下拉**」—— 下拉选分组、只渲染选中组的 1 张图，
        图高 150px ⇒ 自动进入 LineChart 的 isCompactChart 分支（不画轴名、图例贴顶、axisLabel.hideOverlap），
        正好治上述三个症状；分组切换只换渲染（数据已在 chartSeries 内存里），**不重新发请求**。
        桌面（>768px）保持 7 子图平铺、height=260px，结构与改前完全一致（逐像素契约）。
      -->
      <!--
        2026-10-21 用户要求：「逆变器图表下拉框改为 tab 点击切换图表显示，交互效率更高」。
        改前：桌面把 7 个分组**纵向平铺**（7 × 260px ≈ 1900px，要滚三屏；也正是
        「图表长度参差不齐」的根源 —— 同屏对比时绘图区必须逐像素相等）；
        窄屏用一个 el-select 下拉（换组要两步：展开 + 选择）。
        改后：桌面与窄屏**统一**为「一行分组 tab + 单张图」——
          · 一次点击切换分组，全部 7 个分组始终可见（窄屏折成两行，不做横向滚动隐藏）；
          · 同屏只渲染 1 个 echarts 实例（不再为 7 张图各建实例，窄屏省掉 6 份绘制成本）；
          · 切换只改 activeGroupTitle，**不重新发请求**（数据已在 chartSeries 内存里）。
      -->
      <div
        v-if="chartSubGroups.length > 0"
        class="hist-tabbed"
        :data-group="activeGroupTitle"
        :data-series-count="mobileSeriesCount"
        :data-point-count="mobilePointCount"
      >
        <div class="hist-tabs" role="tablist" aria-label="切换历史数据分组">
          <button
            v-for="g in chartSubGroups"
            :key="g.title"
            type="button"
            role="tab"
            class="hist-tab"
            :class="{ 'is-active': g.title === activeGroupTitle }"
            :aria-selected="g.title === activeGroupTitle"
            @click="onGroupSelect(g.title)"
          >{{ g.title }}</button>
        </div>
        <div class="hist-chart">
          <LineChart
            v-if="activeGroup && groupHasData(activeGroup)"
            :series="activeGroup.series"
            :title="''"
            :height="chartHeightPx"
            :yAxisMin="activeGroup.yAxisMin"
            :yAxisMax="activeGroup.yAxisMax"
            :reserve-right-axes="maxReserveRightAxes"
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
            <!-- 离线优先：设备离线时「换时间范围」是无效排查方向，必须先把原因说准。 -->
            <p v-if="props.online === 'offline'" class="empty-hint is-offline">
              设备当前离线，历史数据需由设备上报后才有 —— 请先确认节点与通道在线，恢复后数据会自动补齐。
            </p>
            <p v-else class="empty-hint">可能原因：设备刚接入尚未上报、该类型数据未被采集，或所选时间范围无数据。</p>
          </div>
        </template>
      </el-empty>
    </template>
  </el-card>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
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
  /**
   * 设备在线状态：离线设备的「暂无历史数据」根本原因通常不是时间范围，
   * 而是设备没上报。2026-10-21 独立复核指出空态文案未区分离线，
   * 会把用户引向「换时间范围」这种无效排查。
   *
   * 用三态字符串而不是 boolean：Vue 对 Boolean 类型 prop 在缺省时会 cast 成 false，
   * 直传 boolean 时「未知」与「离线」无法区分（实测未传 online 也走进离线分支，
   * 让 14 个既有空态测试全部误红）。
   */
  online?: 'online' | 'offline' | 'unknown'
}>()

/* ---- 视口判定（≤768px 走窄屏「单图 + 分组下拉」）----
 * 为什么用 JS 判定而不是纯 CSS：窄屏要的是**结构**变化（7 张图 → 1 张图 + 下拉）。
 * CSS 只能隐藏不能减少节点；被 display:none 藏起来的 6 张图仍会创建 echarts 实例、
 * 仍在读屏与 Tab 顺序里，属于可访问性与性能双重缺陷。
 * 无 window.matchMedia 的环境（SSR / 部分测试）按桌面处理，保证既有桌面契约稳定。 */
const MOBILE_MAX = 768
const isMobileViewport = ref(
  typeof window !== 'undefined' && typeof window.matchMedia === 'function'
    ? window.matchMedia(`(max-width: ${MOBILE_MAX}px)`).matches
    : false,
)
let mobileMql: MediaQueryList | null = null
const onMobileChange = (e: MediaQueryListEvent) => { isMobileViewport.value = e.matches }

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
  /*
   * 温度组**不含 pv2_temp / dc_rectifier_temp**（2026-10-21 生产数据实测）。
   *
   * 证据（近 3 小时 362 个采样点，unified-data historical-batch）：
   *   pv2_temp：min=30 max=100 mean=90.52，**只有 3 个取值 30 / 50 / 100**；
   *   dc_rectifier_temp：与它完全同步（同一组 30/50/100）；
   *   两者在 04:01~05:01 之间反复跳变；
   *   而其余 5 路温度是正常连续区间（pv 38~42、inverter 36~43、boost 31~38、
   *   transformer 32~39、max 38~43）。
   * 结论：这两个字段在该机型上不是温度（更像状态/枚举位，或驱动 HTEMP 位序
   * 对该固件不成立）。当 °C 画出来会出现一条 100℃ 假尖峰，并把真实 30~43℃
   * 的曲线压扁 —— 用户看到的就是「逆变器温度有问题」。
   *
   * 处理：前端先不展示这两路（数据仍在库里，可随时恢复）；驱动侧位序需厂商
   * 文档确认后再改名/改位。
   */
  { title: '温度 (°C)', categories: ['pv_temp', 'inverter_temp', 'boost_temp', 'transformer_temp', 'max_temp'] },
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

/* ---- 窄屏分组下拉 ----
 * 默认选中「第一个有数据的分组」；无数据时回退第一个分组。
 * 用户手动切换后不再被数据到达覆盖（groupTouched），但若所选分组在数据刷新后
 * 已不存在（例如切到 1h 后该量全无数据），仍回退到第一个有数据的分组，避免下拉显示空值。
 * 切换只改 activeGroupTitle，不调用 fetchHistoryData ⇒ 不产生新请求。 */
const activeGroupTitle = ref('')
const groupTouched = ref(false)

/*
 * 点击 tab 切换分组（2026-10-21：取代窄屏 el-select 的 onGroupChange）。
 * 只改 activeGroupTitle —— 不调用 fetchHistoryData，切换零请求、零延迟。
 */
function onGroupSelect(title: string) {
  if (title === activeGroupTitle.value) return
  activeGroupTitle.value = title
  groupTouched.value = true
}

const activeGroup = computed<ChartSubGroupResult | undefined>(
  () => chartSubGroups.value.find(g => g.title === activeGroupTitle.value) ?? chartSubGroups.value[0],
)
const mobileSeriesCount = computed(() => activeGroup.value?.series.length ?? 0)
/**
 * 单图高度（px 字符串，直接传给 LineChart 的 height）。
 *   · 桌面 320：平铺时代每张 260px，现在同屏只有一张，给到 320 更易读；
 *   · 窄屏 200：必须 ≤220 才会进入 LineChart 的 isCompactChart 分支
 *     （不画轴名/图例贴顶/axisLabel.hideOverlap）—— 窄屏正是靠这三个机制才不糊。
 */
const chartHeightPx = computed(() => (isMobileViewport.value ? '200px' : '320px'))

/** 当前分组实际绘制的点数（测试/探针用：>0 才证明横轴真有数据，不是空图） */
const mobilePointCount = computed(
  () => (activeGroup.value?.series ?? []).reduce((n, s) => n + s.data.length, 0),
)

/**
 * 当前分组需要预留的右轴数（2026-10-21，随 tab 化改造调整）。
 *
 * 历史：平铺 7 张图时这里取「所有组里最大的右轴数」，让每组预留同样右边距 ——
 * 目的是让同屏各图 plot 宽严格相等（治「图表长度参差不齐」）。
 * 现在 tab 一次只显示**一张**图，跨图对齐不再有意义；改为只按当前分组自己的
 * 不同单位数估算（与 LineChart 的分轴规则同口径），简单分组（如电网 2 个量）
 * 不再白白空出两根右轴的位置。
 *
 * 上限与 LineChart 的 MAX_Y_AXIS=4 对齐（4 根轴 ⇒ 最多 3 根右轴）。
 * 变量名保持 maxReserveRightAxes 不变（既有 ChartPlotAlignment 契约按此匹配）。
 */
const LINE_CHART_MAX_Y_AXIS = 4
const maxReserveRightAxes = computed(() => {
  const group = activeGroup.value
  if (!group) return 0
  const units = new Set(group.series.map(s => s.unit || ''))
  return Math.max(0, Math.min(units.size, LINE_CHART_MAX_Y_AXIS) - 1)
})

watch(
  () => chartSubGroups.value.map(g => `${g.title}:${groupHasData(g) ? 1 : 0}`).join(','),
  () => {
    const groups = chartSubGroups.value
    if (groups.length === 0) { activeGroupTitle.value = ''; return }
    if (groupTouched.value && groups.some(g => g.title === activeGroupTitle.value)) return
    const firstWithData = groups.find(g => groupHasData(g))
    activeGroupTitle.value = firstWithData ? firstWithData.title : groups[0].title
  },
  { immediate: true },
)

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

onMounted(() => {
  // 监听视口变化：横竖屏切换 / 桌面窗口缩放时在「7 子图平铺」与「单图 + 分组下拉」之间切换
  if (typeof window !== 'undefined' && typeof window.matchMedia === 'function') {
    mobileMql = window.matchMedia(`(max-width: ${MOBILE_MAX}px)`)
    mobileMql.addEventListener('change', onMobileChange)
    isMobileViewport.value = mobileMql.matches
  }
  void fetchHistoryData()
})

onUnmounted(() => {
  // 必须解绑：不解绑会在路由切换后留下指向已销毁组件的监听（内存泄漏）
  mobileMql?.removeEventListener('change', onMobileChange)
  mobileMql = null
})
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

/* 卡头：标题左、控件右；放不下时换行（控件尺寸不跟着缩，避免「按钮忽大忽小」） */
.hist-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px 12px;
  flex-wrap: wrap;
}
.hist-title { font-size: 16px; font-weight: 500; color: var(--el-text-color-primary); }
.header-controls { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }

/*
  分组 tab（2026-10-21 新增，替代「桌面 7 图平铺 / 窄屏 el-select」）。
  为什么不用 el-tabs：7 个分组在 769~900px 的卡片里会溢出，el-tabs 溢出时用
  「◀ ▶ 翻页器」把选项藏起来 —— 等于默认隐藏分组；这里是 flex-wrap，**永远全部可见**。
  交互：role=tablist/tab + aria-selected（读屏可感知当前分组），点击即切换。
*/
.hist-tabs {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-bottom: 12px;
}
.hist-tab {
  appearance: none;
  -webkit-appearance: none;
  margin: 0;
  padding: 0 12px;
  height: 30px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 6px;
  background: var(--el-fill-color-blank);
  color: var(--el-text-color-regular);
  font-family: inherit;
  font-size: 13px;
  line-height: 1;
  cursor: pointer;
  transition: color .15s, background-color .15s, border-color .15s;
}
.hist-tab:hover {
  color: var(--el-color-primary);
  border-color: var(--el-color-primary-light-5);
}
.hist-tab.is-active {
  /* 白字落在 primary 实心填充上：必须用主题 token（暗色下是深字），
     判据见 src/styles/__tests__/FillContrastContract.spec.ts 的对比度合同 */
  color: var(--text-on-fill);
  background: var(--el-color-primary);
  border-color: var(--el-color-primary);
  font-weight: 500;
}
.hist-tab:focus-visible { outline: 2px solid var(--el-color-primary-light-3); outline-offset: 1px; }
.hist-chart { width: 100%; }

/* ---- 窄屏（≤768px）：卡内距收紧 + tab 折行（不横向滚动、不隐藏任何分组） ---- */
@media (max-width: 768px) {
  .hist-title { font-size: 15px; }
  .hist-tabs { gap: 4px; margin-bottom: 8px; }
  .hist-tab { height: 32px; padding: 0 10px; font-size: 12px; }
  /* 空态占满单图高度，切换分组时不抖卡高 */
  .hist-chart .chart-sub-empty {
    height: 200px;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 0 12px;
    box-sizing: border-box;
  }
  :deep(.el-card__header) { padding: 12px; }
  :deep(.el-card__body) { padding: 12px; }
}
</style>
