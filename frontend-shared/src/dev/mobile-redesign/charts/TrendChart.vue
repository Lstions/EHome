<!--
  多序列趋势图（统计类图表，BMS 移动端改版 demo 专用）—— src/dev。

  ── 它回答的问题（现有页面答不了）─────────────────────────────────────────
  BMS 页只有「电芯电压」一个图，全是**瞬时截面**。运维真正会问的是趋势：
    · 3 路温度是在一起往上爬，还是某一路单独偏高（= 探头/局部发热问题）？
    · SOC 在掉，但掉的速度对不对？充电时电流有没有真的灌进去？
  截面数据回答不了这两问，必须给时间维。这是**新信息**，不是把旧图换个样式。

  ── 容器感知（规范要求，复用项目既有降级思路）────────────────────────────
  单点可用宽度 = 容器宽 / 点数：
    · ≥ 26px：显示全部 X 轴刻度
    · < 26px：按 ceil(26/点宽) 的步长抽稀刻度（而不是让 echarts 自己猜，
      它会在窄容器里保留过多标签导致重叠）
  图例在 < 300px 时移到图外（由调用方渲染），避免侵占绘图区。

  ── 手机字号 ──────────────────────────────────────────────────────────────
  轴标签 10px、tooltip 12px、图例 11px —— 全部 ≥10px（Lead 的硬约束）。
-->
<template>
  <div ref="chartRef" class="trend-chart" :style="{ height: height }"></div>
</template>

<script setup lang="ts">
import { computed, ref, onMounted, onUnmounted, watch } from 'vue'
import * as echarts from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { LineChart as LineChartSeries } from 'echarts/charts'
import { GridComponent, TooltipComponent, LegendComponent } from 'echarts/components'
import { getThemeColors, getThemeSurfaceColors } from '@/utils/theme'

echarts.use([CanvasRenderer, LineChartSeries, GridComponent, TooltipComponent, LegendComponent])

export interface TrendSeries {
  name: string
  /** 语义色档位：图里不用裸色值，与页面其它状态色同一套语义 */
  tone: 'primary' | 'success' | 'warning' | 'danger'
  data: number[]
  /**
   * 量纲轴：不同单位的序列共处一图时必须分开读数轴（规范 §4.5.3：
   * 数据比较只允许相同语义且相同单位）。把 ℃ 与 % 画在同一根 Y 轴上，
   * 读者会误以为两条线可以直接比较高低 —— 那是假信息。
   */
  axis?: 'left' | 'right'
  /** 该序列自己的单位（双轴时每个轴各有一个，只能各写一次） */
  unit?: string
}

const props = withDefaults(defineProps<{
  categories: string[]
  series: TrendSeries[]
  /** Y 轴单位（只显示一次，不给每个数据点重复） */
  unit?: string
  yMin?: number
  yMax?: number
  height?: string
  /** 图例交给调用方在图外渲染时置 false */
  showLegend?: boolean
  decimals?: number
  /** 右轴量程（仅双轴时生效） */
  rightMin?: number
  rightMax?: number
}>(), {
  unit: '',
  height: '128px',
  showLegend: true,
  decimals: 1,
  rightMin: undefined,
  rightMax: undefined,
})

const chartRef = ref<HTMLElement>()
let chartInstance: echarts.ECharts | null = null
let resizeObserver: ResizeObserver | null = null
let themeObserver: MutationObserver | null = null
const chartWidth = ref(0)

/** 是否有序列声明了右轴（≥1 条 axis:'right' 即启用双轴） */
const hasDualAxis = computed(() => props.series.some((s) => s.axis === 'right'))
/** 左轴单位：取未声明 right 的第一条序列的 unit，回退到整体 unit */
const leftUnit = computed(() => {
  const left = props.series.find((s) => s.axis !== 'right' && s.unit)
  return left?.unit ?? props.unit
})
const rightUnit = computed(() => props.series.find((s) => s.axis === 'right' && s.unit)?.unit ?? '')

/** 单刻度最小可用宽度（px）。低于它按步长抽稀 —— 与 BmsCellVoltageChart 的降级同源 */
const MIN_LABEL_SPACING = 26

function buildOption() {
  const colors = getThemeColors()
  const surface = getThemeSurfaceColors()
  const count = props.categories.length
  const pointWidth = count > 0 ? chartWidth.value / count : 0
  const labelStep = pointWidth >= MIN_LABEL_SPACING
    ? 0
    : Math.max(1, Math.ceil(MIN_LABEL_SPACING / Math.max(pointWidth, 1)))

  const toneColor: Record<TrendSeries['tone'], string> = {
    primary: colors.primary,
    success: colors.success,
    warning: colors.warning,
    danger: colors.danger,
  }

  return {
    animation: false,
    tooltip: {
      trigger: 'axis' as const,
      confine: true,
      backgroundColor: surface.overlay,
      borderColor: surface.border,
      textStyle: { color: surface.text, fontSize: 12 },
      axisPointer: { lineStyle: { color: surface.border } },
      formatter: (params: unknown) => {
        const arr = params as Array<{ seriesName: string; value: number; axisValue: string }>
        if (!arr.length) return ''
        const head = `<div style="margin-bottom:2px">${arr[0].axisValue}</div>`
        const rows = arr.map((p) => {
          const v = typeof p.value === 'number' ? p.value.toFixed(props.decimals) : '—'
          return `${p.seriesName}：<b>${v}${props.unit}</b>`
        }).join('<br/>')
        return head + rows
      },
    },
    legend: {
      show: props.showLegend,
      top: 0,
      right: 0,
      itemWidth: 8,
      itemHeight: 8,
      itemGap: 10,
      textStyle: { color: surface.regular, fontSize: 10 },
      icon: 'roundRect',
    },
    grid: {
      left: 0,
      right: 6,
      top: props.showLegend ? 22 : 6,
      bottom: 0,
      containLabel: true,
    },
    xAxis: {
      type: 'category' as const,
      data: props.categories,
      boundaryGap: false,
      axisLine: { lineStyle: { color: surface.border } },
      axisTick: { show: false },
      axisLabel: {
        color: surface.regular,
        fontSize: 10,
        // 容器感知抽稀：步长为 0 时全显
        interval: labelStep === 0 ? 'auto' : labelStep,
        hideOverlap: true,
      },
    },
    // 双轴：hasDualAxis 时左轴承载 axis:'left'（默认），右轴承载 axis:'right'。
    // 单轴时只有一根，保持原样（不多画一根空轴占宽度）。
    yAxis: hasDualAxis.value
      ? [
        {
          type: 'value' as const,
          min: props.yMin,
          max: props.yMax,
          // ⚠ computed 在 <script setup> 的普通函数里**不会自动解包**：
          // 直接写 leftUnit 会把 ComputedRef 对象塞进 option，echarts 内部 clone
          // 时沿着它的 _value/dep 引用图无限递归 ⇒ RangeError: Maximum call stack
          // （实测该 bug 会让整个页面被 ErrorBoundary 换成"重试"页）。必须取 .value。
          name: leftUnit.value,
          nameTextStyle: { color: surface.regular, fontSize: 10, align: 'left' },
          splitNumber: 3,
          axisLine: { show: false },
          axisTick: { show: false },
          splitLine: { lineStyle: { color: surface.split } },
          axisLabel: {
            color: surface.regular,
            fontSize: 10,
            formatter: (v: number) => v.toFixed(props.decimals >= 2 ? 1 : 0),
          },
        },
        {
          type: 'value' as const,
          // 右轴量程由调用方通过 rightMin/rightMax 指定；未给时让 echarts 自适应
          min: props.rightMin,
          max: props.rightMax,
          name: rightUnit.value,
          nameTextStyle: { color: surface.regular, fontSize: 10, align: 'right' },
          splitNumber: 3,
          axisLine: { show: false },
          axisTick: { show: false },
          // 右轴不画分隔线：两套网格线交错会让图看起来像"散点噪声"
          splitLine: { show: false },
          axisLabel: {
            color: surface.regular,
            fontSize: 10,
            formatter: (v: number) => v.toFixed(props.decimals >= 2 ? 1 : 0),
          },
        },
      ]
      : {
        type: 'value' as const,
        min: props.yMin,
        max: props.yMax,
        // 单位只出现在轴名一次
        name: props.unit,
        nameTextStyle: { color: surface.regular, fontSize: 10, align: 'left' },
        splitNumber: 3,
        axisLine: { show: false },
        axisTick: { show: false },
        splitLine: { lineStyle: { color: surface.split } },
        axisLabel: {
          color: surface.regular,
          fontSize: 10,
          formatter: (v: number) => v.toFixed(props.decimals >= 2 ? 1 : 0),
        },
      },
    series: props.series.map((s) => ({
      name: s.name,
      type: 'line' as const,
      // 双轴时按声明分配到右轴；单轴时忽略该字段
      yAxisIndex: hasDualAxis.value && s.axis === 'right' ? 1 : 0,
      data: s.data,
      smooth: false,
      showSymbol: false,
      symbol: 'circle',
      symbolSize: 4,
      lineStyle: { width: 1.6, color: toneColor[s.tone] },
      itemStyle: { color: toneColor[s.tone] },
      // 面积只在单序列时才有意义（多序列叠加会糊成一团）
      areaStyle: props.series.length === 1
        ? { color: toneColor[s.tone], opacity: 0.12 }
        : undefined,
    })),
  }
}

function render() {
  chartInstance?.setOption(buildOption(), { notMerge: true })
}

onMounted(() => {
  if (!chartRef.value) return
  chartWidth.value = chartRef.value.clientWidth
  chartInstance = echarts.init(chartRef.value)
  render()
  resizeObserver = new ResizeObserver(() => {
    if (!chartRef.value || !chartInstance) return
    chartWidth.value = chartRef.value.clientWidth
    chartInstance.resize()
    render()
  })
  resizeObserver.observe(chartRef.value)
  themeObserver = new MutationObserver(render)
  themeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['class', 'data-theme'] })
})

watch(() => [props.categories, props.series], render, { deep: true })

onUnmounted(() => {
  resizeObserver?.disconnect()
  resizeObserver = null
  themeObserver?.disconnect()
  themeObserver = null
  chartInstance?.dispose()
  chartInstance = null
})
</script>

<style scoped>
.trend-chart { width: 100%; }
</style>
