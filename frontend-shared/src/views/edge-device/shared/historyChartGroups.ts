import type { SeriesData } from '@/types/chart'
import { computeAdaptiveYAxisRange } from '@/utils/chartRange'
import { SENSOR_ORDER } from '@/utils/sensor'

/**
 * 历史图表「设备类型 → 查询分类 / 量纲分组」的纯函数域。
 *
 * 边界依据（2026-11-12 纯搬移拆分，行为零变化）：
 *   · 搬入本文件的都是**无组件状态**的映射与投影：分类清单、预定义分组、
 *     chartSeries → 子图分组（含自适应 Y 轴判据）、空态范围文案。它们只吃入参，
 *     不读 ref、不发请求、不碰 DOM。
 *   · 没有搬走的：
 *       - 模板与 scoped 样式：源码门禁 HistoryChartSectionTabbed.spec.ts /
 *         ChartCardToolbarSizing.spec.ts 断言**原文件**含 role="tablist" / .hist-tab /
 *         .hist-export 的 --control-* token / CHART_HEIGHT_PX，属页面级契约锚点；
 *       - 视口判定与卸载解绑（行为测试直接观察 window.matchMedia 的 removeEventListener）；
 *       - tab 选中态（activeGroupTitle / onGroupSelect）：见 useActiveChartGroup.ts。
 */

/** 状态量category — 不适合折线图，从图表中移除 */
export const statusCategories = ['protection_status', 'fet_status']

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

/** 通用设备的兜底 category 清单（unified-data batch） */
const genericCategories = [
  ...SENSOR_ORDER, 'illuminance', 'uv_index', 'rain_intensity', 'rain_accum',
  'voltage', 'current', 'power', 'energy', 'soc', 'soh', 'frequency',
  'cell_voltage', 'cell_temp', 'mos_status', 'protection_status',
]

/**
 * 该设备类型应查询的 category 清单。
 * 顺序与拆分前逐字一致（batch 按 categories 顺序返回，顺序变化会影响图例次序）。
 */
export function knownCategoriesFor(deviceType: string): string[] {
  if (isBmsDevice(deviceType)) return bmsCategories
  if (isInverterDevice(deviceType)) return inverterCategories
  return genericCategories
}

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
export const isBmsDevice = (deviceType: string): boolean => {
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
export const isInverterDevice = (deviceType: string): boolean => {
  const t = deviceType.toLowerCase()
  return t === 'inverter' || t.endsWith('_inverter') || t.includes('inverter')
}

export interface ChartSubGroupResult {
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
export function buildChartSubGroups(deviceType: string, chartSeries: SeriesData[]): ChartSubGroupResult[] {
  if (chartSeries.length === 0) return []

  let groups: { title: string; series: SeriesData[] }[]

  if (isBmsDevice(deviceType)) {
    // BMS: 按预定义量纲分组
    groups = bmsChartGroups
      .map(group => {
        const series = chartSeries.filter(s =>
          s.category && group.categories.includes(s.category)
        )
        return { title: group.title, series }
      })
      .filter(g => g.series.length > 0)
  } else if (isInverterDevice(deviceType)) {
    // Inverter: 按预定义量纲分组
    groups = inverterChartGroups
      .map(group => {
        const series = chartSeries.filter(s =>
          s.category && group.categories.includes(s.category)
        )
        return { title: group.title, series }
      })
      .filter(g => g.series.length > 0)
  } else {
    // 非BMS: 按unit自动分组
    const unitGroups = new Map<string, SeriesData[]>()
    for (const s of chartSeries) {
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
}

/** 该分组是否确实有数据点（没有就不渲染 260px 图表，避免大片空白） */
export function groupHasData(group: ChartSubGroupResult): boolean {
  return group.series.some(s => Array.isArray(s.data) && s.data.length > 0)
}

/** 空态展示用的查询范围说明（告诉用户查了哪些物理量，便于判断是没采到还是选错范围） */
export function describeQueryScope(deviceType: string): string {
  if (isBmsDevice(deviceType)) return 'BMS 电压 / 电流 / SOC / 温度 / 单体电压'
  if (isInverterDevice(deviceType)) return '逆变器 PV / 电网 / 输出 / 电池 / 温度 / 发电量'
  return '通用传感器分类'
}
