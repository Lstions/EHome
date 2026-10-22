<template>
  <div ref="chartRef" class="line-chart" :style="{ height: height }"></div>
</template>

<script lang="ts">
/**
 * 图例 / 绘图区顶部布局 —— 纯函数，可单测（2026-10-21 用户反馈「图例压住绘图区与 Y 轴名」）。
 *
 * 背景（逆变器「输出」组：6 条序列 5 种单位）：图例固定 top=30、行高约 20px，plain 图例在容器
 * 宽度用尽时折行；grid.top 原为常量 56（只够 1 行图例 + 轴名），折成 2 行后图例底边 ≈70 > 56
 * ⇒ 图例压在绘图区与 Y 轴名上（实测底边 ≈66px）。这里把「图例行数 → grid.top」收敛成纯函数：
 * 行数按「图例标记 + 文字宽度 + itemGap」对可用宽度贪心折行，grid.top = top + 行数×行高 + 间隙，
 * 且不低于原常量 TOP_RESERVED_PX（单行时与改前逐像素一致）。
 *
 * 放独立 <script> 块：<script setup> 不能 export，而该规则必须有单测（见 ChartPlotAlignment.spec.ts）。
 */

/** 非紧凑图上边距基线（px）：单行图例 + Y 轴名刚好不压不切；改前即此值。 */
export const TOP_RESERVED_PX = 56
/** 紧凑图阈值：该档不写 grid.top（ECharts 默认 60，其它组件按 60 换算告警线）。 */
export const COMPACT_MAX_HEIGHT_PX = 220
/** ECharts 默认 grid.top（紧凑图不覆盖，仅供测试/调用方参考）。 */
export const COMPACT_DEFAULT_GRID_TOP_PX = 60
/** 常规图图例参数（与 applyChartOption 传给 legend 的取值同源）。 */
export const LEGEND_TOP_PX = 30
export const LEGEND_ROW_HEIGHT_PX = 20
export const LEGEND_ITEM_GAP_PX = 10
/** 图例标记占位（ECharts 默认 itemWidth=25 + 标记与文字间距，按 30 估算）。 */
export const LEGEND_MARKER_PX = 30
/** 图例底边与绘图区的最小间隙（px）。 */
export const LEGEND_GRID_GAP_PX = 6
/** 图例文字字号（与 legend.textStyle 同源）。 */
export const LEGEND_FONT_SIZE_PX = 12
export const LEGEND_FONT_SIZE_COMPACT_PX = 10
/** 图例容器左右内边距之和（ECharts legend padding 默认 5x2，留 12 作余量）。 */
const LEGEND_HORIZONTAL_PADDING_PX = 12
/**
 * 宽度估算安全系数：字体度量与逐字符估算有 ±5~10% 偏差；宁多算一行，不可少算（少算=图例压图）。
 * 1.12 的标定依据：逆变器「输出」组 6 条序列 5 种单位在 844px 画布上**实测折成 2 行**
 * （图例底边 ≈66px 压住绘图区），本估算必须复现该折行；取 1.08 时估出 818px < 832px
 * 可用宽会误判 1 行（假绿），1.12 约 839px ⇒ 2 行，与实测一致并留字距余量。
 */
const WIDTH_SAFETY_FACTOR = 1.12

/** 该高度是否走紧凑图分支（与组件内 isCompactChart 同判据）。 */
export function isCompactHeight(height?: string | number): boolean {
  const h = typeof height === "string" ? Number.parseInt(height, 10) : Number(height)
  // 220 与 COMPACT_MAX_HEIGHT_PX 同值；此处保留字面量是因为既有门禁
  // （LineChartCompact.spec.ts）按 `h <= 220` 断言该判据存在，避免常量改名即失守。
  // 二者一致性由 ChartPlotAlignment.spec.ts 的边界用例（220 紧凑 / 221 常规）钉住。
  return Number.isFinite(h) && (h as number) > 0 && (h as number) <= 220
}

/** 图例文字宽度估算（px）：全角字符按字号宽、半角按 0.6 字号宽。仅用于判定折行。 */
export function estimateLegendTextWidth(text: string, fontSize = LEGEND_FONT_SIZE_PX): number {
  let width = 0
  for (const ch of text) {
    const code = ch.codePointAt(0) ?? 0
    const fullWidth = code >= 0x2e80 || (code >= 0xff00 && code <= 0xffef)
    width += fullWidth ? fontSize : fontSize * 0.6
  }
  return width
}

export interface ChartTopLayoutInput {
  /** 画布宽度（px）；取不到（未挂载/布局前）传 0：退回单行假设。 */
  canvasWidth: number
  /** 图例文字（已带单位，如「输出电压 (V)」）。 */
  items: string[]
  /** 组件高度（"260px" / 260），用于判定紧凑图。 */
  height?: string | number
  /**
   * 显式紧凑开关（2026-10-22）。
   * 缺省（undefined）→ 仍按 height ≤ COMPACT_MAX_HEIGHT_PX 判定（既有契约，逐像素不变）；
   * true → 强制紧凑（轴名不画、图例贴顶、hideOverlap、最多 2 根 Y 轴）；
   * false → 强制常规排版（即使高度很小）。
   * 为什么需要：移动端图表高度抬到 320px（>220）以压低「控件块/图高」比例，
   * 但窄屏仍需要紧凑排版才不糊 —— 像素高度与排版档是两个正交概念。
   */
  compact?: boolean
}

export interface ChartTopLayout {
  compact: boolean
  /** 图例容器 top（紧凑图 6 / 常规图 30，与 legend.top 一致） */
  legendTop: number
  /** 估算出的图例行数（>=1） */
  legendRows: number
  /** 每条图例项估算宽度（px），便于测试核对折行依据 */
  legendItemWidths: number[]
  /** 绘图区上边距：常规图 = max(TOP_RESERVED_PX, top + rows×行高 + 间隙)；紧凑图 = 60（不写入 option） */
  gridTop: number
}

/**
 * 计算图例/绘图区顶部布局。
 *
 * 折行口径：按图例项宽度贪心铺满一行，放不下就换行（与 ECharts plain legend 一致）。
 * grid.top：图例底边（legendTop + rows×rowHeight）再留 LEGEND_GRID_GAP_PX，且不低于 TOP_RESERVED_PX
 * ⇒ 单行图例输出 56，与改前逐像素一致。
 */
export function computeChartTopLayout(input: ChartTopLayoutInput): ChartTopLayout {
  // 显式 compact 优先；缺省时保持改前契约（按高度判定）
  const compact = input.compact ?? isCompactHeight(input.height)
  const legendTop = compact ? 6 : LEGEND_TOP_PX
  const rowHeight = compact ? 16 : LEGEND_ROW_HEIGHT_PX
  const itemGap = compact ? 8 : LEGEND_ITEM_GAP_PX
  const fontSize = compact ? LEGEND_FONT_SIZE_COMPACT_PX : LEGEND_FONT_SIZE_PX
  const markerWidth = compact ? 15 : LEGEND_MARKER_PX
  const items = (input.items || []).filter(text => typeof text === "string" && text.length > 0)
  const legendItemWidths = items.map(text => Math.round((markerWidth + estimateLegendTextWidth(text, fontSize) * WIDTH_SAFETY_FACTOR + itemGap) * 100) / 100)

  const canvasWidth = Number.isFinite(input.canvasWidth) ? Math.max(0, input.canvasWidth) : 0
  const available = canvasWidth - LEGEND_HORIZONTAL_PADDING_PX
  // 画布宽度未知（未挂载 / happy-dom）：无法判定折行，退回单行（= 改前行为），不擅自抬高 plot。
  let legendRows = 1
  if (canvasWidth > 0 && legendItemWidths.length > 0) {
    legendRows = 1
    let used = 0
    for (const width of legendItemWidths) {
      if (used === 0) {
        used = width
      } else if (used + width <= Math.max(available, width)) {
        used += width
      } else {
        legendRows += 1
        used = width
      }
    }
  }

  const gridTop = compact
    ? COMPACT_DEFAULT_GRID_TOP_PX
    : Math.max(TOP_RESERVED_PX, legendTop + legendRows * rowHeight + LEGEND_GRID_GAP_PX)

  return { compact, legendTop, legendRows, legendItemWidths, gridTop }
}

export interface YAxisNameInput {
  compact: boolean
  /** 轴下标（0 = 左轴） */
  index: number
  /** 本轴单位 */
  unit: string
  /** 前面各轴的单位（用于「同单位只显示一次」） */
  priorUnits: string[]
  /** 总轴数（含左轴）。右轴数 = totalAxes - 1 */
  totalAxes: number
}

/**
 * 该 Y 轴是否显示轴名。
 *
 * 规则：
 *   · 紧凑图：一律不显示（轴名会压住贴顶图例）；
 *   · 右轴数 >= 2（总轴数 >= 3）：所有轴名清空 —— nameLocation:end 的多个轴名会在顶部互相重叠
 *     （实测「输出」组 (V)(Hz)(VA)(W)(%) 挤成一团），单位由图例项承担；
 *   · 右轴数 <= 1 保留原行为：左轴显示，同单位只在第一根轴显示。
 */
export function shouldShowYAxisName(input: YAxisNameInput): boolean {
  if (input.compact) return false
  if (input.totalAxes >= 3) return false
  if (input.index > 0 && input.priorUnits.includes(input.unit)) return false
  return true
}
</script>

<script setup lang="ts">
import { ref, onMounted, onUnmounted, watch } from 'vue'
import * as echarts from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { LineChart as LineChartSeries } from 'echarts/charts'
import {
  TitleComponent,
  TooltipComponent,
  LegendComponent,
  GridComponent
} from 'echarts/components'
import type { EChartsCoreOption } from 'echarts/core'

// Register required ECharts components for tree-shaking mode
echarts.use([
  CanvasRenderer,
  LineChartSeries,
  TitleComponent,
  TooltipComponent,
  LegendComponent,
  GridComponent
])

interface ChartData {
  time: string
  value: number
}

interface SeriesConfig {
  name: string
  data: ChartData[]
  unit?: string
}

const props = withDefaults(defineProps<{
  data?: ChartData[]
  title?: string
  height?: string
  smooth?: boolean
  series?: SeriesConfig[]
  yAxisMin?: number
  yAxisMax?: number
  showArea?: boolean
  realtime?: boolean
  /**
   * 为右侧 Y 轴**预留**的轴数（父级传入组内最大值）。
   *
   * 为什么需要它：右边距原本按本图自己的右轴数计算，于是同一页里
   * 「1 根右轴」和「3 根右轴」的分组拿到的右边距不同（15% vs 29%），
   * 绘图区右边界不一致 —— 用户看到的就是「图表长度参差不齐」（2026-10-21 反馈）。
   * 父级（HistoryChartSection）算出组内最大右轴数传下来，组内所有图预留同样宽度。
   */
  reserveRightAxes?: number
  /**
   * 显式紧凑排版开关（缺省 undefined = 按 height ≤220px 判定，保持既有契约）：
   *   · true  → 强制紧凑（不画 Y 轴名 / 图例贴顶 / X 轴 hideOverlap / 最多 2 根 Y 轴）；
   *   · false → 强制常规排版（即使高度很小）。
   * 用途：移动端图高抬到 320px 后 >220 会退出紧凑分支，而窄屏仍需紧凑渲染；
   * 高度与排版档必须能分别指定（2026-10-22，规范 §4.4 控件尺寸档位）。
   */
  compact?: boolean
}>(), {
  data: () => [],
  title: '',
  height: '400px',
  smooth: true,
  series: () => [],
  showArea: true,
  realtime: false,
  reserveRightAxes: 0
})

const chartRef = ref<HTMLElement>()
let chartInstance: echarts.ECharts | null = null
/** 最近一次顶部布局（图例/grid 上边距）—— 供测试/探针读取真实排版档位（legend 是否压绘图区）。 */
let lastTopLayout: ReturnType<typeof computeChartTopLayout> | null = null
let resizeObserver: ResizeObserver | null = null
let themeObserver: MutationObserver | null = null

const cssToken = (name: string, fallback: string) =>
  getComputedStyle(document.documentElement).getPropertyValue(name).trim() || fallback

const getThemePalette = () => [
  cssToken('--color-primary', '#409eff'),
  cssToken('--color-danger', '#f56c6c'),
  cssToken('--color-warning', '#e6a23c'),
  cssToken('--color-success', '#67c23a'),
  cssToken('--color-info', '#909399'),
  cssToken('--color-adc', '#9c27b0'),
  cssToken('--terminal-accent', '#06b6d4'),
  cssToken('--terminal-warning', '#f97316')
]

const getChartTheme = () => ({
  text: cssToken('--text-color-primary', '#303133'),
  regular: cssToken('--text-color-regular', '#606266'),
  border: cssToken('--border-color', '#e8eaec'),
  split: cssToken('--border-color-light', '#ebeef5'),
  overlay: cssToken('--bg-color-overlay', '#ffffff'),
  palette: getThemePalette()
})

/**
 * 同一单位、但量级相差 ≥ 该倍数的序列**不能**再共用一根 Y 轴。
 *
 * 2026-10-02 生产实测（BMS 电压图）：总电压 50.5V 与最高/最低单体电压 3.18V 同为 "V"，
 * 按单位分组后被放进同一根 Y 轴，轴上界被 50.5 撑到 ~60，3.18V 的两条曲线被压成
 * 贴在 X 轴上的直线 —— 数据在、图上看不见。这正是"分组正确但量纲不可比"的失效。
 *
 * 判据用「量级比」而不是「单位」：单位相同不代表可以共轴。
 */
const MAGNITUDE_SPLIT_RATIO = 10
/** 轴数上限：超过后按"量级最接近"合并，避免 grid 被挤爆、图不可读。
 * 紧凑图（窄屏小图，高度 ≤220px）再收紧到 2 根：实测 319px 宽 + 4 根轴
 * （每根 offset 60px + containLabel）会把绘图区压到 20~30px，曲线完全读不出。
 * 合并后次要量纲改由 tooltip / 图例读数，绘图区宽度回到可用区间。 */
/** 上边距基线 TOP_RESERVED_PX 已移到独立 <script> 块（供图表布局纯函数与单测共用）。
 *  取值依据：图例 top:30、行高约 20px（底 ≈50），轴名画在绘图区顶端之上；56px 实测不压不切。 */
/**
 * 常规图左右下边距（px）。与 TOP_RESERVED_PX 同一目的：让每张图的 plot 区严格同宽同高。
 *
 * 取值依据（1440 实测）：
 *   · LEFT 64：Y 轴刻度最宽约 5 字符（「300.0」≈40px）+ 轴名，留 64 不切标签；
 *   · RIGHT 基准 24 + 每根右轴 104（offset 60 递进 + 刻度约 44）；
 *   · BOTTOM 46：X 轴时间刻度单行（「MM/DD HH:mm」≈62×14px）画在轴下方。
 * ⚠ 这几个常量只在 containLabel:false 下成立 —— 换回 true 时它们只是「最小边距」，
 *   标签宽度仍会参与收缩，同页会重新变得参差不齐。
 */
const LEFT_RESERVED_PX = 64
const RIGHT_BASE_PX = 24
const RIGHT_AXIS_PX = 64
/**
 * 右边距占画布宽度的上限（避免窄桌面 769~1000px 被固定像素边距吃光绘图区）。
 * 实测 769px 视口下画布只有约 453px：若仍按 24+3×64=216 预留，
 * 绘图区只剩 173px；夹到 34% 后回到 235px，曲线可读。
 * 上限只影响「能否放下全部右轴标签」，不影响同页对齐 —— 组内画布宽度相同，
 * 夹取结果也相同。
 */
const RIGHT_MAX_RATIO = 0.34
const BOTTOM_RESERVED_PX = 46
const MAX_Y_AXIS = 4
const MAX_Y_AXIS_COMPACT = 2

/** 该序列的量级参考值（绝对值最大者）。空/全 null 返回 0，表示"无从比较"。 */
const seriesMagnitude = (s: SeriesConfig): number => {
  let magnitude = 0
  for (const point of s.data) {
    const abs = Math.abs(point.value as number)
    if (Number.isFinite(abs) && abs > magnitude) magnitude = abs
  }
  return magnitude
}

/** 两个量级是否可比（任一为 0 视为未知，不加惩罚地允许共轴）。 */
const magnitudeComparable = (a: number, b: number): boolean => {
  if (a === 0 || b === 0) return true
  return Math.max(a, b) / Math.min(a, b) < MAGNITUDE_SPLIT_RATIO
}

/**
 * Build yAxis list for multi-series mode.
 * Series share one Y-axis only when BOTH unit and magnitude are comparable;
 * otherwise they get their own Y-axis (offset to the right).
 */
const buildYAxisList = (theme = getChartTheme()) => {
  if (!props.series || props.series.length <= 1) return undefined
  const axes: { unit: string; magnitude: number }[] = []
  const seriesToYAxis: number[] = []
  props.series.forEach(s => {
    const unit = s.unit || ''
    const magnitude = seriesMagnitude(s)
    const maxAxis = isCompactChart.value ? MAX_Y_AXIS_COMPACT : MAX_Y_AXIS
    let idx = axes.findIndex(a => a.unit === unit && magnitudeComparable(a.magnitude, magnitude))
    if (idx < 0 && axes.length >= maxAxis) {
      // 触顶：并入量级最接近的那根轴，保持图可读（宁可共轴也不新增轴）。
      let best = 0
      let bestDistance = Infinity
      axes.forEach((a, i) => {
        const d = a.magnitude === 0 || magnitude === 0 ? 0 : Math.abs(Math.log10(a.magnitude) - Math.log10(magnitude))
        if (d < bestDistance) { bestDistance = d; best = i }
      })
      idx = best
    }
    if (idx < 0) {
      axes.push({ unit, magnitude })
      idx = axes.length - 1
    } else if (magnitude > axes[idx].magnitude) {
      axes[idx].magnitude = magnitude
    }
    seriesToYAxis.push(idx)
  })
  // 自定义范围是"整张图一个范围"的旧契约，只在单轴时成立；
  // 多轴时若把它套到每根轴上，量级不同的轴会得到错误的上下界。
  const hasCustomRange = props.yAxisMin !== undefined && props.yAxisMax !== undefined && axes.length === 1
  return {
    yAxisList: axes.map((axis, i) => ({
      type: 'value' as const,
      // 紧凑图（窄屏小图，如 160px）不画轴名：ECharts 的 yAxis.name 默认 nameLocation:'end'，
      // 画在轴顶端 ≈ 图例那一行，实测会与图例文字重叠成「#1 (V)(V)」。单位并不丢，
      // 图例项已带「#1 (V)」「探头 1 (℃)」后缀。
      // 同单位只在第一根轴显示轴名：实测「发电量」组有 3 根 kWh 轴，
      // 三处 (kWh) 在顶部挤在一起并压住刻度，且信息完全重复。
      // 单位并未丢失 —— 图例项已带 (kWh) 后缀。
      // 轴名规则：
      //   · 紧凑图不画（轴名会压住贴顶图例）；
      //   · 同单位只在第一根轴显示（原有行为，保留字面判据以防回归）；
      //   · **右轴数 >= 2（总轴数 >= 3）时所有轴名清空** —— 多个 nameLocation:'end' 的轴名
      //     会在顶部互相重叠（实测「输出」组 (V)(Hz)(VA)(W)(%) 挤成一团），单位由图例项承担。
      //     该条规则收敛到纯函数 shouldShowYAxisName（可单测，见 ChartPlotAlignment.spec.ts）。
      name: isCompactChart.value || (i > 0 && axes.slice(0, i).some((a) => a.unit === axis.unit))
        || !shouldShowYAxisName({
          compact: isCompactChart.value,
          index: i,
          unit: axis.unit,
          priorUnits: axes.slice(0, i).map((a) => a.unit),
          totalAxes: axes.length,
        })
        ? ''
        : axis.unit ? `(${axis.unit})` : '数值',
      position: i === 0 ? 'left' as 'left' : 'right' as 'right',
      // 紧凑图最多 2 根轴（左 1 右 1），不再需要叠加偏移；常规图沿用 60px 递进
      offset: i > 1 ? (i - 1) * (isCompactChart.value ? 30 : 60) : 0,
      min: hasCustomRange ? props.yAxisMin : undefined,
      max: hasCustomRange ? props.yAxisMax : undefined,
      axisLine: { lineStyle: { color: theme.border } },
      splitLine: { show: i === 0, lineStyle: { color: theme.split } },
      axisLabel: {
        color: theme.regular,
        // 紧凑图轴标签小一号，给绘图区留宽度
        fontSize: isCompactChart.value ? 10 : undefined,
        formatter: (v: number) => hasCustomRange ? v.toFixed(2) : v.toFixed(1),
      },
      nameTextStyle: { color: theme.regular }
    })),
    seriesToYAxis
  }
}

const buildSeries = (seriesToYAxis?: number[], palette = getThemePalette()) => {
  // Multi-series mode: use props.series — data as [timestamp, value] pairs
  if (props.series && props.series.length > 0) {
    return props.series.map((s, i) => ({
      name: s.name + (s.unit ? ` (${s.unit})` : ''),
      type: 'line' as const,
      data: s.data.map(item => [new Date(item.time).getTime(), item.value]),
      smooth: props.smooth,
      sampling: 'lttb' as const,
      yAxisIndex: seriesToYAxis ? seriesToYAxis[i] : i,
      lineStyle: { width: 2, color: palette[i % palette.length] },
      itemStyle: { color: palette[i % palette.length] },
      showSymbol: s.data.length > 50 ? false : true,
      ...(props.showArea ? {
        areaStyle: {
          color: new echarts.graphic.LinearGradient(0, 0, 0, 1, [
            { offset: 0, color: palette[i % palette.length] + '33' },
            { offset: 1, color: palette[i % palette.length] + '0a' }
          ])
        }
      } : {})
    }))
  }

  // Single-series mode: use props.data
  return [{
    name: '数值',
    type: 'line' as const,
    data: props.data.map(item => [new Date(item.time).getTime(), item.value]),
    smooth: props.smooth,
    lineStyle: { width: 2, color: palette[0] },
    itemStyle: { color: palette[0] },
    ...(props.showArea ? {
      areaStyle: {
        color: new echarts.graphic.LinearGradient(0, 0, 0, 1, [
          { offset: 0, color: palette[0] + '33' },
          { offset: 1, color: palette[0] + '0a' }
        ])
      }
    } : {})
  }]
}

/**
 * Time-based xAxis config.
 * Uses type: 'time' so series with different timestamps align correctly.
 */
/**
 * 常规图右边距（px）：按预留的右轴数计算，再夹到画布宽度的 RIGHT_MAX_RATIO。
 * 抽成函数是为了让「窄桌面夹取」的规则只有一处、可被单测直接调用。
 */
const normalRightMargin = (rightAxes: number): number => {
  const raw = RIGHT_BASE_PX + rightAxes * RIGHT_AXIS_PX
  const canvasWidth = chartRef.value?.clientWidth ?? 0
  if (canvasWidth <= 0) return raw
  return Math.round(Math.min(raw, Math.max(RIGHT_BASE_PX * 2, canvasWidth * RIGHT_MAX_RATIO)))
}

const getXAxisConfig = (theme = getChartTheme()) => {
  return {
    type: 'time' as const,
    axisLine: { lineStyle: { color: theme.border } },
    splitLine: { lineStyle: { color: theme.split } },
    axisLabel: {
      color: theme.regular,
      fontSize: 11,
      // 窄屏小图（295px）上 6 个时间刻度会挤成「20:0000:0004:0008:00…」（实测重叠），
      // 交给 ECharts 丢弃重叠标签；大图无重叠时该开关不改变任何东西。
      hideOverlap: isCompactChart.value,
      formatter: (value: number) => {
        const d = new Date(value)
        const now = new Date()
        const diffDays = Math.abs(now.getTime() - value) / 86400000
        if (diffDays > 2) {
          // 7d range: MM/DD HH:mm
          return `${(d.getMonth() + 1).toString().padStart(2, '0')}/${d.getDate().toString().padStart(2, '0')} ${d.getHours().toString().padStart(2, '0')}:${d.getMinutes().toString().padStart(2, '0')}`
        } else {
          // 24h and 1h: HH:mm
          return `${d.getHours().toString().padStart(2, '0')}:${d.getMinutes().toString().padStart(2, '0')}`
        }
      }
    }
  }
}

const formatTooltipTime = (ts: string | number) => {
  const d = new Date(ts)
  return `${(d.getMonth() + 1).toString().padStart(2, '0')}/${d.getDate().toString().padStart(2, '0')} ${d.getHours().toString().padStart(2, '0')}:${d.getMinutes().toString().padStart(2, '0')}:${d.getSeconds().toString().padStart(2, '0')}`
}

/**
 * 是否「紧凑图」：显式 compact prop 优先；未传时仍按高度 ≤220px 判定（既有契约）。
 * 只影响排版：轴名要不要画（默认会压住图例）、图例/grid 的上边距、
 * X 轴标签 hideOverlap、Y 轴上限（紧凑最多 2 根）。
 * 桌面大图（280/300/400px）不传 compact ⇒ 走原分支，像素不变。
 */
const isCompactChart = computed(() => props.compact ?? isCompactHeight(props.height))

const applyChartOption = () => {
  if (!chartInstance) return
  const theme = getChartTheme()
  const multiSeries = props.series && props.series.length > 1
  const yAxisInfo = buildYAxisList(theme)
  // 每根右侧轴都要留出刻度位；固定 8% 在多轴时会把轴标签挤出画布。
  const rightAxisCount = yAxisInfo ? Math.max(0, yAxisInfo.yAxisList.length - 1) : 0
  const yAxisConfig = yAxisInfo?.yAxisList || {
    type: 'value' as const,
    min: props.yAxisMin,
    max: props.yAxisMax,
    axisLine: { lineStyle: { color: theme.border } },
    splitLine: { lineStyle: { color: theme.split } },
    axisLabel: { color: theme.regular, formatter: (value: number) => value.toFixed(2) }
  }
  /*
   * 图例/绘图区顶部布局（2026-10-21 用户反馈「图例折 2 行后压住绘图区与 Y 轴名」）：
   * 行数按「每条图例文字宽度（已带单位）+ 标记 + itemGap」对画布宽度贪心折行估算，
   * grid.top = 图例 top + 行数×行高 + 间隙，且不低于基线 TOP_RESERVED_PX
   * ⇒ 单行时数值仍为 56（改前像素不变），2 行时抬到 76、3 行 96，图例底边永远在绘图区之上。
   * 画布宽度取 chartRef.clientWidth；取不到（挂载首帧/无布局环境）时退回单行，不擅自抬高 plot。
   */
  const legendItems = (props.series ?? []).map(s => s.name + (s.unit ? ` (${s.unit})` : ''))
  const topLayout = computeChartTopLayout({
    canvasWidth: chartRef.value?.clientWidth ?? 0,
    items: legendItems,
    height: props.height,
    compact: props.compact,
  })
  lastTopLayout = topLayout
  const option: EChartsCoreOption = {
    animation: props.realtime ? false : undefined,
    color: theme.palette,
    title: { text: props.title, left: 'center', textStyle: { color: theme.text } },
    tooltip: {
      trigger: 'axis',
      // Confine tooltip inside the chart area to avoid clipping by ancestor containers.
      confine: true,
      // Prefer following the pointer horizontally so the tooltip stays near the crosshair
      // while ECharts keeps it within the chart bounds.
      position: (point: number[]) => [point[0] + 10, point[1]],
      backgroundColor: theme.overlay,
      borderColor: theme.border,
      textStyle: { color: theme.text },
      formatter: (params: any) => {
        if (!Array.isArray(params) || params.length === 0) return ''
        const ts = params[0].axisValueLabel || params[0].name
        const timeStr = formatTooltipTime(ts)
        // Sort by value descending so the most significant items are always visible first,
        // especially when the tooltip is clipped by the chart top edge.
        const sorted = [...params].sort((a: any, b: any) => {
          const av = Array.isArray(a.value) ? a.value[1] : a.value
          const bv = Array.isArray(b.value) ? b.value[1] : b.value
          return (typeof bv === 'number' ? bv : 0) - (typeof av === 'number' ? av : 0)
        })
        const total = sorted.length
        // Dynamic two-column layout: up to 12 items per column, max 2 columns.
        // This keeps the tooltip compact and avoids clipping on mobile.
        const maxPerColumn = 12
        const columns = total > maxPerColumn ? 2 : 1
        const pageSize = columns * maxPerColumn
        const visible = sorted.slice(0, pageSize)
        const gridMinWidth = columns === 1 ? 'auto' : '220px'
        let html = `<div style="white-space:normal;">`
        html += `<div style="margin-bottom:4px;font-weight:500;">${timeStr}</div>`
        html += `<div style="display:grid;grid-template-columns:repeat(${columns}, 1fr);gap:4px 16px;min-width:${gridMinWidth};">`
        visible.forEach((item: any) => {
          const val = Array.isArray(item.value) ? item.value[1] : item.value
          html += `<div style="white-space:nowrap;overflow:hidden;text-overflow:ellipsis;">${item.marker} ${item.seriesName}: <b>${typeof val === 'number' ? val.toFixed(3) : val}</b></div>`
        })
        html += '</div>'
        if (total > pageSize) {
          html += `<div style="color:${theme.regular};font-size:12px;margin-top:4px;">... 还有 ${total - pageSize} 项</div>`
        }
        html += '</div>'
        return html
      }
    },
    legend: multiSeries
      ? {
        // 紧凑图没有轴名，图例可以贴顶；常规图保持 30（给多轴轴名留位置）。
        // 与 computeChartTopLayout 的 legendTop 同源（该函数返回 6 / LEGEND_TOP_PX=30，
        // 由 ChartPlotAlignment.spec.ts 断言两者一致，防止这里与布局计算漂移）。
        top: isCompactChart.value ? 6 : 30,
        /*
         * 图例溢出策略（2026-10-20 生产实测后定稿）：
         *   319px 宽的紧凑图里 5~7 条序列，`type:'scroll'` 只显示 2~3 条 + 「◀ 1/3 ▶」翻页器 ——
         *   用户根本不知道被折叠的序列是什么，等于**默认隐藏数据**（违反「不拿信息换对齐」）。
         *   `type:'plain'` 会在容器宽度用尽时自动换行成 2~3 行，所有序列名始终可见。
         *   紧凑图 grid.top 固定 60px，图例 top 6 + 2 行(各 16px) = 38 < 60，不会压到绘图区；
         *   条目尺寸同步收小，保证 2 行内放得下 7 条。
         */
        /*
         * 图例一律 plain（换行不翻页）。2026-10-21 真机实测：
         *   · 桌面 type:'scroll' 在「发电量」这类多轴图上会**把图例画到图表底部、
         *     压在 x 轴刻度上**，而且只显示 1 项 —— 用户看到的是一个巨大的
         *     「总发电量 (kWh)」压在曲线上（实测 3 个时点稳定复现，任何一次
         *     setOption 后才恢复，属首次布局错误，不可靠）。
         *   · 另外 scroll 会翻页隐藏序列，违反「不拿信息换对齐」（2026-10-20 已定）。
         * plain 在多时自动换行，实测渲染位置正确。
         */
        type: 'plain' as const,
        /*
         * bottom: 'auto' 是必须的（2026-10-21 定性）：ECharts 6.1.0 的 legend 默认值里带
         * `bottom: tokens.size.m`（见 LegendModel.defaultOption），只指定 `top` 时图例被
         * 纳入 top..bottom 之间做垂直布局，实测直接画到**画布顶部之外**（文字 bbox y ≈ -9，
         * 只露出下边缘，用户看到的就是一个巨大的序列名压在 x 轴上）。
         * 隔离实验：同一份 option 在空白容器里复现；只把 bottom 设为 'auto'（或 0）即恢复，
         * 删掉整个 legend、或去掉 grid 都恢复 —— 说明是 legend 自身布局，不是容器/时序问题。
         */
        bottom: 'auto' as const,
        itemGap: isCompactChart.value ? 8 : 10,
        /*
         * ⚠ 不要把 itemWidth/itemHeight/fontSize 写成 `undefined`（2026-10-21 定性第二处）：
         * 显式 undefined 会被 ECharts 6 当成「已提供」参与图例行高计算，配合默认 bottom
         * 会把图例摆到画布外 —— 隔离实验里同一份 option 只要 JSON 序列化（丢掉 undefined 键）
         * 就能正常渲染，而直接传对象就复现错位。这里改成「按需展开」：紧凑图才补这几个键。
         */
        ...(isCompactChart.value
          ? { itemWidth: 10, itemHeight: 8, textStyle: { color: theme.regular, fontSize: 10 } }
          : { textStyle: { color: theme.regular } }),
      }
      : undefined,
    // ⚠ 这里**不覆盖** grid.top：紧凑图的既有默认值（60px）被其它组件当常量用
    // （如 TempTrendCard 用 CSS 绝对定位画 45℃ 告警线，按 gridTop=60 换算 y 坐标）。
    // 本次只解决「轴名压图例」，不动 plot 区域几何。
    /*
     * 绘图区几何必须**跨分组一致**（2026-10-21 用户反馈「图表长度参差不齐」）：
     *
     * ① 右边距：原本按本图自己的右轴数算（1 轴 15% / 3 轴 29%），于是同页不同分组的
     *    绘图区右边界各不相同 —— 「输出」的 3 根右轴把图压窄 14%，曲线右端点与
     *    「PV输入」不在同一竖线。改为 max(本图右轴数, 父级预留数)，组内预留同样宽度。
     * ② 上边距：原本不设 top、交给 ECharts 自动布局，而自动 top 会随「轴名个数 +
     *    图例行数」变化 —— 实测同屏单轴图 plot 高 139px、三轴图 122px。
     *    改为显式常量 TOP_RESERVED_PX。
     * ⚠ 只**条件展开**写入 top：显式 top:undefined 在 ECharts 6 会被当成「已提供」
     *    参与布局（与下方 legend.bottom 同源陷阱）。
     */
    grid: isCompactChart.value
      ? {
        // 紧凑图（窄屏小图）沿用百分比 + containLabel：容器只有 150px 高，
        // 必须让 ECharts 自己按标签宽度收放，写死像素反而会切标签。
        left: '3%',
        right: multiSeries ? (rightAxisCount > 0 ? `${8 + rightAxisCount * 7}%` : '8%') : '4%',
        bottom: '3%',
        containLabel: true,
      }
      : {
        /*
         * 常规图（桌面 260px）：**写死像素边距 + containLabel:false**，
         * 让每张图的 plot rect 严格相等 —— 这是「图表长度参差不齐」的正解。
         *
         * 为什么不能继续用 containLabel:true：它会先按百分比起算，再按**本图标签实际宽度**
         * 往里收，于是「300.0」（5 字符）和「54.9」的分组得到不同的左边距，
         * 右轴 1 根与 3 根的分组得到不同的右边距 —— 实测同一屏 6 张图 plot 宽
         * 615 / 688 / 555 / 628 / 723 / 622px，用户看到的就是右端点不在同一竖线。
         * 写死边距后宽度只由 reserveRightAxes（组内最大值）决定 ⇒ 组内完全一致。
         */
        // 单行图例时即 top: TOP_RESERVED_PX（56，改前像素基线）；多行图例时按实测行数抬升，
        // 保证图例底边（legendTop + rows×20）永远在绘图区之上（2 行 → 76、3 行 → 96）。
        top: topLayout.gridTop,
        left: LEFT_RESERVED_PX,
        // 每多一根右轴，ECharts 按 offset 60px 递进摆放，轴标签再占约 45px
        right: normalRightMargin(Math.max(rightAxisCount, props.reserveRightAxes ?? 0)),
        bottom: BOTTOM_RESERVED_PX,
        containLabel: false,
      },
    xAxis: getXAxisConfig(theme),
    yAxis: yAxisConfig,
    series: buildSeries(yAxisInfo?.seriesToYAxis, theme.palette)
  }
  chartInstance.setOption(option, { notMerge: true })
}

const initChart = () => {
  if (!chartRef.value) return
  chartInstance = echarts.init(chartRef.value)
  applyChartOption()
  /*
   * 首帧后再应用一次。
   *
   * 2026-10-21 真机实测（/edge-device/15「发电量」图）：**首次** applyChartOption 之后，
   * ECharts 的图例会被画到画布顶部之外（实测文字 bbox y ≈ -9，只露出下边缘一条），
   * 而任何一次后续 setOption 都能把它放回正确位置 —— 说明是首次布局时容器度量
   * 尚未稳定（图表在分批渲染/折叠块里挂载，父级高度在首个 rAF 前还在变）。
   * 这里在下一帧 resize + 重新 apply，保证「首帧渲染」与「交互后」视觉一致。
   * 只多一次 setOption，无额外请求、无视觉闪烁（同一帧内完成）。
   */

  resizeObserver = new ResizeObserver(() => chartInstance?.resize())
  resizeObserver.observe(chartRef.value)
  themeObserver = new MutationObserver(applyChartOption)
  themeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['class', 'data-theme'] })
}

// Watch for data changes
// realtime mode: shallow watch (reference change only) — appendRealtimeData does chartSeries.value = [...]
// normal mode: deep watch to detect nested data array mutations
watch(
  // height / compact 也必须进 watch：窄屏切换（横竖屏/窗口缩放）时高度与排版档一起变，
  // 不重算 option 会保留旧分支（轴名/图例位置错档）。reserveRightAxes 保持在最后：
  // ChartPlotAlignment.spec 按 /props\.reserveRightAxes\]/ 锁这条 watch。
  () => [props.data, props.series, props.title, props.yAxisMin, props.yAxisMax, props.height, props.compact, props.reserveRightAxes],
  applyChartOption,
  { deep: true },
)

onUnmounted(() => {
  resizeObserver?.disconnect()
  resizeObserver = null
  themeObserver?.disconnect()
  themeObserver = null
  chartInstance?.dispose()
  chartInstance = null
})

onMounted(() => {
  initChart()
})

/**
 * 供测试/探针读取真实绘图区几何（2026-10-21）。
 *
 * 为什么需要：用户反馈「图表长度参差不齐」，而**容器高度是相等的（都 260px）**，
 * 差异藏在 ECharts 内部的 grid（plot 区）上 —— 从 DOM 或截图都没法可靠测出来
 * （曲线/面积填充会干扰像素扫描，实测不可用）。这里暴露真实 rect，
 * 让对齐可以被**断言**，而不是靠肉眼比划。
 */
defineExpose({
  getChartInstance: () => chartInstance,
  /**
   * 顶部布局（2026-10-22）：{ compact, legendTop, legendRows, gridTop }。
   * 探针用它证明「紧凑图 legend 底边 < grid 顶边（图例不压绘图区）」，
   * 与 getPlotRect 配套即可在不做像素比对的情况下量化排版。
   */
  getTopLayout: () => lastTopLayout,
  getPlotRect: (): { x: number; y: number; width: number; height: number } | null => {
    if (!chartInstance) return null
    // ECharts 未把 getModel 放进公开类型，但运行时可用来读 grid 布局；
    // 这里只在测试/探针路径使用，取不到就返回 null（不影响生产渲染）。
    const internals = chartInstance as unknown as {
      getModel?: () => { getComponent: (n: string) => { coordinateSystem?: { getRect?: () => { x: number; y: number; width: number; height: number } } } }
    }
    const grid = internals.getModel?.()?.getComponent('grid')
    const rect = grid?.coordinateSystem?.getRect?.()
    return rect ? { x: rect.x, y: rect.y, width: rect.width, height: rect.height } : null
  },
})
</script>

<style scoped>
.line-chart {
  width: 100%;
}
</style>
