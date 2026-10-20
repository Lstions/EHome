<!--
  温度探头「刻度条」—— BMS 移动端改版 demo 专用（src/dev）。

  为什么用图而不是三行文字：三路温度在手机上本来只占 3 行，问题不在行数，
  而在「26.5 / 27.8 / 28.1 三个数读不出它们离告警有多远」。
  横向刻度条把 0–60℃ 的物理量程画出来，45℃ 告警线一标，
  「现在很凉」这件事不需要算就知道 —— 这是文字给不了的信息。

  容器感知：窄容器里 y 轴标签与数值标签都缩到 9px 并右对齐进 grid 内，
  不做固定像素宽度，所以不会溢出。
-->
<template>
  <div ref="chartRef" class="temp-gauge" :style="{ height: height }"></div>
</template>

<script setup lang="ts">
import { ref, onMounted, onUnmounted, watch } from 'vue'
import * as echarts from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { BarChart as BarChartSeries } from 'echarts/charts'
import { GridComponent, TooltipComponent, MarkLineComponent } from 'echarts/components'
import { getThemeColors, getThemeSurfaceColors } from '@/utils/theme'

echarts.use([CanvasRenderer, BarChartSeries, GridComponent, TooltipComponent, MarkLineComponent])

const props = withDefaults(defineProps<{
  labels: string[]
  values: number[]
  height?: string
  /** 刻度条满量程（℃） */
  scaleMax?: number
  warn?: number
  danger?: number
  /**
   * 显示量程下界。默认 0（物理量程，"离告警还有多远"一眼可见）；
   * 但 26~28℃ 在 0~60 量程里只有 45% 长，三根条几乎一样长 ——
   * 若要突出"三路之间的差异"（如方案 A 的紧凑卡），传 20 做放大显示，
   * 并在调用方文案里写明量程，避免误读。
   */
  scaleMin?: number
}>(), {
  height: '104px',
  scaleMax: 60,
  warn: 45,
  danger: 60,
  scaleMin: 0,
})

const chartRef = ref<HTMLElement>()
let chartInstance: echarts.ECharts | null = null
let resizeObserver: ResizeObserver | null = null
let themeObserver: MutationObserver | null = null

function colorOf(v: number, colors: ReturnType<typeof getThemeColors>) {
  if (v >= props.danger) return colors.danger
  if (v >= props.warn) return colors.warning
  return colors.success
}

function buildOption() {
  const colors = getThemeColors()
  const surface = getThemeSurfaceColors()

  return {
    animation: false,
    tooltip: {
      trigger: 'axis' as const,
      axisPointer: { type: 'shadow' as const },
      confine: true,
      backgroundColor: surface.overlay,
      borderColor: surface.border,
      textStyle: { color: surface.text, fontSize: 12 },
      formatter: (params: unknown) => {
        const arr = params as Array<{ name: string; value: number }>
        const p = arr[0]
        return `${p?.name ?? ''}<br/>温度 <b>${p?.value?.toFixed(1) ?? '—'} ℃</b>`
      },
    },
    // 左侧留出探头名，右侧留出数值标签；grid 用 containLabel 自适应，不写死像素
    grid: { left: 0, right: 44, top: 4, bottom: 2, containLabel: true },
    xAxis: {
      type: 'value' as const,
      min: props.scaleMin,
      max: props.scaleMax,
      axisLine: { show: false },
      axisTick: { show: false },
      // 量程放大时必须给出底/顶刻度，否则读者无法判断长度含义（误读风险 > 省下的空间）
      axisLabel: props.scaleMin > 0
        ? { color: surface.regular, fontSize: 10, formatter: (v: number) => String(v) }
        : { show: false },
      splitLine: { show: false },
    },
    yAxis: {
      type: 'category' as const,
      data: props.labels,
      inverse: true,
      axisLine: { show: false },
      axisTick: { show: false },
      axisLabel: { color: surface.regular, fontSize: 11 },
    },
    series: [{
      type: 'bar',
      data: props.values.map((v) => ({ value: v, itemStyle: { color: colorOf(v, colors) } })),
      barWidth: 8,
      itemStyle: { borderRadius: 4 },
      // 每根条右侧直接给数值：手机上不点也能读，tooltip 只是补充
      label: {
        show: true,
        position: 'right',
        fontSize: 11,
        color: surface.text,
        formatter: (p: { value: number }) => `${p.value.toFixed(1)}℃`,
      },
      markLine: {
        silent: true,
        symbol: 'none',
        // 量程放大到告警线以下时，告警线不在可见范围内，不画（画在图外的线是假信息）
        data: props.warn >= props.scaleMin && props.warn <= props.scaleMax ? [{ xAxis: props.warn }] : [],
        lineStyle: { color: colors.warning, type: 'dashed', width: 1, opacity: 0.7 },
        label: { show: false },
      },
    }],
  }
}

function render() {
  chartInstance?.setOption(buildOption(), { notMerge: true })
}

onMounted(() => {
  if (!chartRef.value) return
  chartInstance = echarts.init(chartRef.value)
  render()
  resizeObserver = new ResizeObserver(() => {
    chartInstance?.resize()
    render()
  })
  resizeObserver.observe(chartRef.value)
  themeObserver = new MutationObserver(render)
  themeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['class', 'data-theme'] })
})

watch(() => [props.labels, props.values], render, { deep: true })

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
.temp-gauge { width: 100%; }
</style>
