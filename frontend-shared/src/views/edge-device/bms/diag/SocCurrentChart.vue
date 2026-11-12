<!--
  SOC 与电流双区图（diag #9）—— 一张 ECharts 实例、两个 grid、共享一根时间轴。

  ── 为什么和温度卡不同，要自己写一张图 ──────────────────────────────────────
  SOC（%）与电流（A）量纲差两个数量级，LineChart 会把它们分到左右两根 Y 轴上：
  两根轴各自自适应，于是「SOC 掉了 10%」和「电流 −5A」在同一高度上看起来一样陡，
  这正是要避免的假信息。这里改成**上下两个独立绘图区**：
    · 上区：SOC 面积图，单 Y 轴 %，量程按数据自适应；
    · 下区：电流折线，单 Y 轴 A，零线用 markLine 实线；
    · 两区共享同一根时间轴（X 刻度只画在下区），上下对齐才有可比性。

  ── 几何（196px 画布，契约 §C / 实测目标）──────────────────────────────────
    上区 SOC：top 12、height 96
    间隔：16
    下区 电流：top 124、height 44
    底部 28px 留给 X 轴刻度（只在下区画）
  写死像素 + containLabel:false（与 LineChart 常规档同思路）：grid rect 因此可被
  探针量到确定的 96 / 44，而不是随标签宽度浮动。

  ── 为什么电流要拆成正/负两条 series ───────────────────────────────────────
  一次充电/放电的方向是这张图要回答的核心问题，单一颜色表达不了。
  拆成两条 + 互相 null 占位（并在过零点补一个插值点，让两块面积在零线处闭合），
  正段走 --color-success、负段走 --color-danger，面积以 0 为原点。

  ── 工程约束（踩过的坑）────────────────────────────────────────────────────
    · 只注册用到的模块（tree-shaking）：core + Canvas + Line + Grid/Tooltip +
      MarkLine（零线）+ AxisPointer（axisPointer.link 双区联动，缺了会静默失效）；
    · 颜色全部走 CSS 变量，并监听 documentElement 的 class/data-theme 重建 option；
    · **不要把 computed ref / 响应式数组直接塞进 option** —— ECharts 6 会深度 clone
      这份 option，遇到 Vue 的 reactive proxy 会递归到爆栈。这里一律先 .map() 成
      普通数组/数字再进 option。
-->
<template>
  <div ref="chartRef" class="soc-current-chart" :style="{ height: height }"></div>
</template>

<script setup lang="ts">
import { onMounted, onUnmounted, ref, watch } from 'vue'
import * as echarts from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { LineChart as LineChartSeries } from 'echarts/charts'
import {
  GridComponent,
  TooltipComponent,
  MarkLineComponent,
  AxisPointerComponent,
} from 'echarts/components'
import type { EChartsCoreOption } from 'echarts/core'

// 缺一个都会「静默失效」：markLine 不画零线、axisPointer.link 不联动双区。
echarts.use([
  CanvasRenderer,
  LineChartSeries,
  GridComponent,
  TooltipComponent,
  MarkLineComponent,
  AxisPointerComponent,
])

interface ChartPoint {
  time: string | number
  value: number
}

const props = withDefaults(defineProps<{
  /** SOC 点位（%），空数组表示这半区没有数据 */
  soc: ChartPoint[]
  /** 电流点位（A），空数组表示这半区没有数据 */
  current: ChartPoint[]
  /** 画布高度；几何按 196px 标定，超过 196 的余量全部留给 X 轴刻度 */
  height?: string
  /**
   * X 轴最大刻度间隔（毫秒）。页面级时间窗可切 1h / 24h / 7d，
   * 固定 6h 在 7d 窗会给出 ~29 个刻度（虽有 hideOverlap 兜底，但不该靠兜底）。
   * 由卡片按窗口传：1h→15min，24h→6h，7d→24h。
   */
  xMaxIntervalMs?: number
}>(), {
  height: '196px',
})

/** 时间序列点：[时间戳(ms), 值] */
type Pair = [number, number]

/* ── 主题：全部取自 CSS 变量（与 LineChart.vue 的 cssToken 同写法）────────── */
const cssToken = (name: string, fallback: string) =>
  getComputedStyle(document.documentElement).getPropertyValue(name).trim() || fallback

const readTheme = () => ({
  text: cssToken('--text-color-primary', '#303133'),
  regular: cssToken('--text-color-regular', '#606266'),
  border: cssToken('--border-color', '#e8eaec'),
  split: cssToken('--border-color-light', '#ebeef5'),
  overlay: cssToken('--bg-color-overlay', '#ffffff'),
  primary: cssToken('--color-primary', '#409eff'),
  success: cssToken('--color-success', '#67c23a'),
  danger: cssToken('--color-danger', '#f56c6c'),
})

/**
 * 给 CSS 变量里的颜色加透明度。
 * 为什么不用 `color + '33'`（LineChart 的写法）：那只在色值是 6 位十六进制时成立，
 * 一旦主题把变量改成 rgb()（或 element-plus 的 var() 链），拼出来就是非法颜色，
 * ECharts 会把面积画成黑色/透明 —— 是「换主题才坏」的隐性回归。
 */
function withAlpha(color: string, alpha: number): string {
  const c = color.trim()
  let r = NaN
  let g = NaN
  let b = NaN
  const hex = /^#([0-9a-f]{3}|[0-9a-f]{6})$/i.exec(c)
  if (hex) {
    const hexBody = hex[1].length === 3
      ? hex[1].split('').map((ch) => ch + ch).join('')
      : hex[1]
    r = Number.parseInt(hexBody.slice(0, 2), 16)
    g = Number.parseInt(hexBody.slice(2, 4), 16)
    b = Number.parseInt(hexBody.slice(4, 6), 16)
  } else {
    const rgb = /^rgba?\(([^)]+)\)$/i.exec(c)
    if (!rgb) return c
    const parts = rgb[1].split(',').map((s) => Number.parseFloat(s.trim()))
    if (parts.length < 3) return c
    r = parts[0]
    g = parts[1]
    b = parts[2]
  }
  if (![r, g, b].every((n) => Number.isFinite(n))) return c
  return `rgba(${r}, ${g}, ${b}, ${alpha})`
}

/* ── 几何：196px 画布 → 96 / 间隔 16 / 44 ─────────────────────────────────── */
/** X 轴最大刻度间隔（轴级 maxInterval，毫秒）：24h 窗按 6 小时一档 */
const X_MAX_INTERVAL_MS = 6 * 3600 * 1000

const BASE_HEIGHT = 196
const SOC_HEIGHT = 96
const CURRENT_HEIGHT = 44
const GRID_GAP = 16
const TOP_PAD = 12

const parseHeightPx = (height: string): number => {
  const px = Number.parseFloat(height)
  return Number.isFinite(px) && px > 0 ? px : BASE_HEIGHT
}

/**
 * 两区几何。基线（196px）下就是 12 / 96 / 16 / 44，供探针实测核对。
 * 高度低于基线时按比例压缩两个区（保持 96:44 的视觉权重），
 * 高于基线时多出来的高度全部留在底部给 X 轴刻度 —— 不改变两区高度。
 */
function gridGeometry(heightPx: number) {
  const deficit = Math.max(0, BASE_HEIGHT - heightPx)
  const fixed = SOC_HEIGHT + CURRENT_HEIGHT + GRID_GAP
  const scale = deficit > 0 ? Math.max(0.5, (fixed - deficit) / fixed) : 1
  const round2 = (n: number) => Math.round(n * 100) / 100
  const socHeight = round2(SOC_HEIGHT * scale)
  const currentHeight = round2(CURRENT_HEIGHT * scale)
  const gap = round2(GRID_GAP * scale)
  return {
    socTop: TOP_PAD,
    socHeight,
    currentTop: round2(TOP_PAD + socHeight + gap),
    currentHeight,
  }
}

/* ── 数据整形（全部产出普通数组，绝不把响应式对象塞进 option）────────────── */
function toPairs(list: ChartPoint[] | undefined): Pair[] {
  const out: Pair[] = []
  for (const point of list ?? []) {
    if (!point) continue
    const ts = new Date(point.time).getTime()
    const value = Number(point.value)
    if (!Number.isFinite(ts) || !Number.isFinite(value)) continue
    out.push([ts, value])
  }
  return out.sort((a, b) => a[0] - b[0])
}

/** 正/负拆分：每个时间点各自保留自己那一侧，另一侧留 null 占位。 */
function splitBySign(points: Pair[]): {
  positive: Array<[number, number | null]>
  negative: Array<[number, number | null]>
} {
  const positive: Array<[number, number | null]> = []
  const negative: Array<[number, number | null]> = []
  for (let i = 0; i < points.length; i += 1) {
    const [ts, value] = points[i]
    positive.push([ts, value >= 0 ? value : null])
    negative.push([ts, value <= 0 ? value : null])
    const next = points[i + 1]
    if (!next) continue
    const [nextTs, nextValue] = next
    // 过零点插值：不补这一个点，两块面积会在零线附近裂开一条缝，
    // 读不出「电流在哪一刻换向」——而那正是这张图最该说清楚的事。
    if ((value > 0 && nextValue < 0) || (value < 0 && nextValue > 0)) {
      const ratio = value / (value - nextValue)
      const crossTs = ts + (nextTs - ts) * ratio
      positive.push([crossTs, 0])
      negative.push([crossTs, 0])
    }
  }
  return { positive, negative }
}

/** 刻度数字：整数不带小数，非整数保留 1 位（契约 §A6 同口径） */
const trimNumber = (value: number): string =>
  Number.isInteger(value) ? String(value) : String(Math.round(value * 10) / 10)

/**
 * 上区 SOC 量程（契约 §C 原式）：min = max(0, floor((dataMin - 5) / 10) * 10)，max = 100。
 * 再钉 3 个刻度：interval = (100 - min) / 2 ⇒ 刻度恒为「min / 中间 / 100」。
 * min 上限取 90：数据全部贴近 100 时（如 99），否则 (min,100] 会退化成 2 个刻度甚至重叠。
 */
function socAxisRange(points: Pair[]): { min: number; max: number; interval: number } {
  let dataMin = Number.POSITIVE_INFINITY
  for (const [, value] of points) {
    if (value < dataMin) dataMin = value
  }
  if (!Number.isFinite(dataMin)) return { min: 0, max: 100, interval: 50 }
  const rawMin = Math.max(0, Math.floor((dataMin - 5) / 10) * 10)
  const min = Math.min(rawMin, 90)
  return { min, max: 100, interval: (100 - min) / 2 }
}

const NICE_MULTIPLIERS = [1, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10]
/** 把绝对值向上取到「好看」的刻度基数（1/1.5/2/2.5/3/4/5/6/8/10 × 10^n） */
function niceCeil(value: number): number {
  const abs = Math.abs(value)
  if (!Number.isFinite(abs) || abs < 1e-9) return 0
  const magnitude = Math.pow(10, Math.floor(Math.log10(abs)))
  const normalized = abs / magnitude
  const multiplier =
    NICE_MULTIPLIERS.find((m) => normalized <= m + 1e-9) ?? 10
  return Number((multiplier * magnitude).toPrecision(12))
}

/**
 * 下区电流量程。零线必须落在量程内（markLine 才看得见），且固定 3 个刻度：
 *   · 既有充电又有放电：以 0 为轴心对称（−m / 0 / +m）—— 正负两块面积同尺度，才比得出「进多还是出多」；
 *   · 只有单侧：0 … m（充电）或 −m … 0（放电），中间刻度 m/2。
 */
function currentAxisRange(points: Pair[]): { min: number; max: number; interval: number } {
  if (!points.length) return { min: -10, max: 10, interval: 10 }
  let low = 0
  let high = 0
  for (const [, value] of points) {
    if (value < low) low = value
    if (value > high) high = value
  }
  if (low < 0 && high > 0) {
    const m = niceCeil(Math.max(-low, high)) || 1
    return { min: -m, max: m, interval: m }
  }
  if (high > 0) {
    const m = niceCeil(high) || 1
    return { min: 0, max: m, interval: m / 2 }
  }
  const m = niceCeil(-low) || 1
  return { min: -m, max: 0, interval: m / 2 }
}

/** 所有点位的时间跨度（两区取并集：共享 X 轴必须同一时间窗，否则上下对不齐） */
function timeExtent(...groups: Pair[][]): [number, number] | null {
  let min = Number.POSITIVE_INFINITY
  let max = Number.NEGATIVE_INFINITY
  for (const group of groups) {
    for (const [ts] of group) {
      if (ts < min) min = ts
      if (ts > max) max = ts
    }
  }
  return Number.isFinite(min) && Number.isFinite(max) && max > min ? [min, max] : null
}

const pad2 = (n: number) => String(n).padStart(2, '0')
const formatTimeOfDay = (ts: number): string => {
  const d = new Date(ts)
  return `${pad2(d.getHours())}:${pad2(d.getMinutes())}`
}
const formatDateTime = (ts: number): string => {
  const d = new Date(ts)
  return `${pad2(d.getMonth() + 1)}/${pad2(d.getDate())} ${pad2(d.getHours())}:${pad2(d.getMinutes())}`
}
/** 电流读数口径：带符号的两位小数（−5.39A / +2.60A），指向「电在进还是在出」 */
const formatCurrent = (value: number): string =>
  `${value < 0 ? '\u2212' : '+'}${Math.abs(value).toFixed(2)}A`

/** 二分找最近时间点：tooltip 在任一区悬停时，另一区的读数也要能一起显示 */
function nearestValue(points: Pair[], ts: number): number | null {
  if (!points.length) return null
  let low = 0
  let high = points.length - 1
  while (low < high) {
    const mid = (low + high) >> 1
    if (points[mid][0] < ts) low = mid + 1
    else high = mid
  }
  let best = points[low]
  const prev = points[low - 1]
  if (prev && Math.abs(prev[0] - ts) < Math.abs(best[0] - ts)) best = prev
  return best ? best[1] : null
}

const dot = (color: string) =>
  `<span style="display:inline-block;width:7px;height:7px;border-radius:50%;background:${color};margin-right:6px;vertical-align:1px"></span>`

/* ── 组件实例 ─────────────────────────────────────────────────────────────── */
const chartRef = ref<HTMLElement>()
let chartInstance: echarts.ECharts | null = null
let resizeObserver: ResizeObserver | null = null
let themeObserver: MutationObserver | null = null
/** 上一次渲染时的「窄屏」判定：跨过阈值要换 animation，需要重建 option */
let lastNarrow: boolean | null = null

const isNarrow = (width: number): boolean => width > 0 && width < 520

const buildOption = (): EChartsCoreOption => {
  const theme = readTheme()
  const width = chartRef.value?.clientWidth ?? 0
  const narrow = isNarrow(width)
  const geom = gridGeometry(parseHeightPx(props.height))

  const socPairs = toPairs(props.soc)
  const currentPairs = toPairs(props.current)
  const socRange = socAxisRange(socPairs)
  const currentRange = currentAxisRange(currentPairs)
  const split = splitBySign(currentPairs)
  const extent = timeExtent(socPairs, currentPairs)
  const daySpan = extent ? extent[1] - extent[0] : 0
  const tickFormatter = (ts: number) =>
    daySpan > 2 * 86400000 ? formatDateTime(ts) : formatTimeOfDay(ts)

  const axisPointerLabel = { fontSize: 10, color: theme.overlay, backgroundColor: theme.regular }
  const timeAxisBase = {
    type: 'time' as const,
    // 轴级字段（毫秒，不是 axisLabel 里的标签级配置）：24h 窗最密 6 小时一个刻度。
    // 不设它时 ECharts 按 splitNumber 自行挑间隔，1h 窗会挑出 5 分钟级、7d 窗会稀到 2 天，
    // 窄屏上要么挤要么空。标签重叠由 axisLabel.hideOverlap 兜底。
    maxInterval: props.xMaxIntervalMs ?? X_MAX_INTERVAL_MS,
    axisTick: { show: false },
    splitLine: { show: false },
    axisLine: { lineStyle: { color: theme.border } },
    // 两个 grid 各自一根时间轴：显式钉同一对 min/max，
    // 否则两半区按各自数据范围缩放，同一条竖线在上下两区指向不同时刻（假对齐）。
    ...(extent ? { min: extent[0], max: extent[1] } : {}),
  }

  const series: Record<string, unknown>[] = []
  if (socPairs.length > 0) {
    series.push({
      name: '剩余电量',
      type: 'line' as const,
      xAxisIndex: 0,
      yAxisIndex: 0,
      data: socPairs,
      smooth: 0.25,
      showSymbol: false,
      sampling: 'lttb' as const,
      connectNulls: false,
      lineStyle: { width: 1.6, color: theme.primary },
      itemStyle: { color: theme.primary },
      z: 3,
      areaStyle: {
        // 主色渐变（契约 §C）：上浓下淡，不抢零线/刻度的注意力
        color: new echarts.graphic.LinearGradient(0, 0, 0, 1, [
          { offset: 0, color: withAlpha(theme.primary, 0.32) },
          { offset: 1, color: withAlpha(theme.primary, 0.03) },
        ]),
      },
    })
  }

  if (currentPairs.length > 0) {
    let hasPositive = false
    let hasNegative = false
    for (const [, value] of currentPairs) {
      if (value > 0) hasPositive = true
      if (value < 0) hasNegative = true
    }
    const zeroLine = {
      silent: true,
      symbol: 'none' as const,
      animation: false,
      lineStyle: { color: theme.border, width: 1, type: 'solid' as const },
      label: { show: false },
      data: [{ yAxis: 0 }],
    }
    const positiveSeries: Record<string, unknown> = {
      name: '电流',
      type: 'line' as const,
      xAxisIndex: 1,
      yAxisIndex: 1,
      data: split.positive,
      smooth: false,
      showSymbol: false,
      connectNulls: false,
      lineStyle: { width: 1.2, color: theme.success },
      itemStyle: { color: theme.success },
      areaStyle: { origin: 0, color: withAlpha(theme.success, 0.24) },
    }
    const negativeSeries: Record<string, unknown> = {
      name: '电流',
      type: 'line' as const,
      xAxisIndex: 1,
      yAxisIndex: 1,
      data: split.negative,
      smooth: false,
      showSymbol: false,
      connectNulls: false,
      lineStyle: { width: 1.2, color: theme.danger },
      itemStyle: { color: theme.danger },
      areaStyle: { origin: 0, color: withAlpha(theme.danger, 0.28) },
    }
    // 零线挂在**有数据**的那条 series 上：全放电时正段是整列 null，
    // 挂在它上面 markLine 不会渲染（静默丢零线）。
    if (hasPositive || !hasNegative) positiveSeries.markLine = zeroLine
    else negativeSeries.markLine = zeroLine
    series.push(positiveSeries, negativeSeries)
  }

  return {
    // 移动端关闭动画：切时间窗时重绘 500 个点，动画只会让人等（契约 §C）
    ...(narrow ? { animation: false } : {}),
    textStyle: { color: theme.text },
    // 双区联动：鼠标在任一区，竖线同时出现在两区
    axisPointer: { link: [{ xAxisIndex: 'all' }] },
    tooltip: {
      trigger: 'axis',
      confine: true,
      backgroundColor: theme.overlay,
      borderColor: theme.border,
      textStyle: { color: theme.text, fontSize: 12 },
      axisPointer: { type: 'line', lineStyle: { color: theme.border } },
      formatter: (params: any) => {
        const first = Array.isArray(params) ? params[0] : params
        const rawValue = first?.value
        const ts = Array.isArray(rawValue) ? Number(rawValue[0]) : Number(first?.axisValue)
        if (!Number.isFinite(ts)) return ''
        const socValue = nearestValue(socPairs, ts)
        const currentValue = nearestValue(currentPairs, ts)
        const rows: string[] = []
        if (socValue !== null) {
          rows.push(`${dot(theme.primary)}剩余电量 <b>${socValue.toFixed(1)}%</b>`)
        }
        if (currentValue !== null) {
          rows.push(`${dot(currentValue >= 0 ? theme.success : theme.danger)}电流 <b>${formatCurrent(currentValue)}</b>`)
        }
        if (!rows.length) return ''
        return `<div style="margin-bottom:2px;font-weight:500">${formatDateTime(ts)}</div>${rows.join('<br/>')}`
      },
    },
    grid: [
      {
        left: 36,
        right: 10,
        top: geom.socTop,
        height: geom.socHeight,
        containLabel: false,
      },
      {
        left: 36,
        right: 10,
        top: geom.currentTop,
        height: geom.currentHeight,
        containLabel: false,
      },
    ],
    xAxis: [
      {
        ...timeAxisBase,
        gridIndex: 0,
        // 上区不画刻度：X 轴只在下区出现一次（共享轴），避免两组时间标签上下打架。
        // hideOverlap 仍要写：门禁读的是 xAxis[0]，开关缺失会被判成「没开」。
        axisLabel: { show: false, hideOverlap: true },
        axisLine: { show: false },
        axisPointer: { label: { show: false } },
      },
      {
        ...timeAxisBase,
        gridIndex: 1,
        splitNumber: 4,
        axisLabel: {
          show: true,
          color: theme.regular,
          fontSize: 10,
          margin: 6,
          hideOverlap: true,
          formatter: (value: number) => tickFormatter(value),
        },
        axisPointer: { label: axisPointerLabel },
      },
    ],
    yAxis: [
      {
        type: 'value' as const,
        gridIndex: 0,
        min: socRange.min,
        max: socRange.max,
        interval: socRange.interval,
        axisLine: { show: false },
        axisTick: { show: false },
        splitLine: { lineStyle: { color: theme.split, type: [3, 3] } },
        axisLabel: {
          color: theme.regular,
          fontSize: 10,
          margin: 6,
          formatter: (value: number) => `${trimNumber(value)}%`,
        },
      },
      {
        type: 'value' as const,
        gridIndex: 1,
        min: currentRange.min,
        max: currentRange.max,
        interval: currentRange.interval,
        axisLine: { show: false },
        axisTick: { show: false },
        // 下区不画横向网格：零线已经由 markLine 实线承担，再叠虚线会分不清哪条是 0
        splitLine: { show: false },
        axisLabel: {
          color: theme.regular,
          fontSize: 10,
          margin: 6,
          formatter: (value: number) => `${trimNumber(value)}A`,
        },
      },
    ],
    series,
  }
}

const applyOption = () => {
  if (!chartInstance) return
  const width = chartRef.value?.clientWidth ?? 0
  lastNarrow = isNarrow(width)
  // notMerge：两区/两条电流 series 的条数会随数据变化（全充电时正段有值、负段全 null），
  // merge 模式会把上一轮的 series 留在图上。
  chartInstance.setOption(buildOption(), { notMerge: true })
}

const initChart = () => {
  if (!chartRef.value) return
  chartInstance = echarts.init(chartRef.value)
  applyOption()
  // 首帧后宽度可能仍是 0（折叠块刚展开/父容器还在布局），ResizeObserver 会补一次 resize；
  // 但 option 里的 animation 档位取决于宽度，所以宽度跨过阈值时要重建 option。
  resizeObserver = new ResizeObserver(() => {
    if (!chartInstance) return
    chartInstance.resize()
    const width = chartRef.value?.clientWidth ?? 0
    if (lastNarrow !== isNarrow(width)) applyOption()
  })
  resizeObserver.observe(chartRef.value)
  themeObserver = new MutationObserver(applyOption)
  themeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['class', 'data-theme'] })
}

watch(
  () => [props.soc, props.current, props.height],
  applyOption,
  { deep: true },
)

onMounted(initChart)

onUnmounted(() => {
  resizeObserver?.disconnect()
  resizeObserver = null
  themeObserver?.disconnect()
  themeObserver = null
  chartInstance?.dispose()
  chartInstance = null
})

/* ── 探针（测试/取证用：从 DOM 或截图都量不出 grid rect，只能走 ECharts 内部模型）── */
interface RectLike { x: number; y: number; width: number; height: number }

const gridRect = (index: number): RectLike | null => {
  if (!chartInstance) return null
  const internals = chartInstance as unknown as {
    getModel?: () => {
      getComponent: (name: string, index?: number) => {
        coordinateSystem?: { getRect?: () => RectLike }
      }
    }
  }
  const rect = internals.getModel?.()?.getComponent('grid', index)?.coordinateSystem?.getRect?.()
  return rect ? { x: rect.x, y: rect.y, width: rect.width, height: rect.height } : null
}

/**
 * 两区合并的外接矩形（门禁/探针口径，与 LineChart.getPlotRect 对齐）：
 *   x = min(两区 x)，y = 上区 y，width = 最右 - x，height = 下区底边 - 上区顶边。
 * 基线几何下应为 { x: 36, y: 12, width: 249, height: 156 }（96 + 16 + 44）。
 */
const mergedRect = (): RectLike | null => {
  const upper = gridRect(0)
  const lower = gridRect(1)
  if (!upper) return lower
  if (!lower) return upper
  const x = Math.min(upper.x, lower.x)
  return {
    x,
    y: upper.y,
    width: Math.max(upper.x + upper.width, lower.x + lower.width) - x,
    height: lower.y + lower.height - upper.y,
  }
}

defineExpose({
  getChartInstance: () => chartInstance,
  /** 两个绘图区的实测 rect（顺序：上 SOC、下 电流） */
  getGridRects: (): { soc: RectLike | null; current: RectLike | null } => ({
    soc: gridRect(0),
    current: gridRect(1),
  }),
  /** 两区合并 rect（门禁读这个；兼容只认单绘图区的探针） */
  getPlotRect: (): RectLike | null => mergedRect(),
  /** 排版档：本组件只为「紧凑 + 出血」这一档设计，故恒返回该档（与 LineChart 的 getTopLayout 配套） */
  getLayoutMode: (): { compact: boolean; bleed: boolean } => ({ compact: true, bleed: true }),
})
</script>

<style scoped>
.soc-current-chart {
  width: 100%;
  min-width: 0;
}
</style>
