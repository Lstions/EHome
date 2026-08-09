<template>
  <div ref="chartRef" class="cell-voltage-chart" :style="{ height: height }"></div>
</template>

<script setup lang="ts">
import { ref, onMounted, onUnmounted, watch } from 'vue'
import * as echarts from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { BarChart as BarChartSeries } from 'echarts/charts'
import { GridComponent, TooltipComponent, TitleComponent, MarkLineComponent } from 'echarts/components'
import { getThemeColors, getThemeSurfaceColors } from '@/utils/theme'
import { computeAdaptiveYAxisRange } from '@/utils/chartRange'

echarts.use([CanvasRenderer, BarChartSeries, GridComponent, TooltipComponent, TitleComponent, MarkLineComponent])

const props = withDefaults(defineProps<{
  voltages: number[]
  cellCount?: number
  height?: string
}>(), {
  cellCount: 16,
  height: '280px'
})

const chartRef = ref<HTMLElement>()
let chartInstance: echarts.ECharts | null = null
let resizeObserver: ResizeObserver | null = null
let themeObserver: MutationObserver | null = null

function getBarColors(values: number[]): string[] {
  const colors = getThemeColors()
  return values.map(v => {
    if (v < 3.0) return colors.danger
    if (v > 3.6) return colors.warning
    return colors.success
  })
}

function buildChartOption() {
  const colors = getThemeColors()
  const surface = getThemeSurfaceColors()
  const labels = Array.from({ length: props.cellCount }, (_, i) => `#${i + 1}`)
  const values = props.voltages.length > 0
    ? props.voltages.slice(0, props.cellCount)
    : new Array(props.cellCount).fill(0)

  const validValues = values.filter(v => typeof v === 'number' && v > 0)
  const { min, max } = validValues.length === 0
    ? { min: 2.5, max: 4.0 }
    : computeAdaptiveYAxisRange(validValues, { minBound: 2.0, maxBound: 4.5 })

  return {
    animation: false,
    tooltip: {
      trigger: 'axis' as const,
      confine: true,
      position: (point: number[]) => [point[0] + 10, point[1]],
      backgroundColor: surface.overlay,
      borderColor: surface.border,
      textStyle: { color: surface.text },
      formatter: (params: any) => {
        const p = Array.isArray(params) ? params[0] : params
        return `${p.name}<br/>电压: <b>${typeof p.value === 'number' ? p.value.toFixed(3) : '0.000'}V</b>`
      }
    },
    grid: { left: '3%', right: '4%', bottom: '3%', top: '10%', containLabel: true },
    xAxis: {
      type: 'category' as const,
      data: labels,
      axisLine: { lineStyle: { color: surface.border } },
      axisLabel: { color: surface.regular, fontSize: 11 }
    },
    yAxis: {
      type: 'value' as const,
      name: '电压(V)',
      min,
      max,
      axisLine: { lineStyle: { color: surface.border } },
      splitLine: { lineStyle: { color: surface.split } },
      axisLabel: { color: surface.regular, formatter: (v: number) => v.toFixed(3) },
      nameTextStyle: { color: surface.regular }
    },
    series: [{
      type: 'bar',
      data: values.map(v => ({
        value: v,
        itemStyle: { color: v === 0 ? colors.info : getBarColors([v])[0] }
      })),
      barWidth: '60%',
      label: {
        show: true,
        position: 'top',
        formatter: (p: any) => p.value > 0 ? p.value.toFixed(3) : '',
        fontSize: 10,
        color: surface.text
      },
      markLine: {
        silent: true,
        data: [
          { yAxis: 3.0, lineStyle: { color: colors.danger, type: 'dashed' }, label: { formatter: '下限', color: surface.regular } },
          { yAxis: 3.6, lineStyle: { color: colors.warning, type: 'dashed' }, label: { formatter: '上限', color: surface.regular } },
        ]
      }
    }]
  }
}

function initChart() {
  if (!chartRef.value) return
  chartInstance = echarts.init(chartRef.value)
  chartInstance.setOption(buildChartOption())
}

function updateChart() {
  if (!chartInstance) return
  chartInstance.setOption(buildChartOption(), { notMerge: true })
}

watch(() => props.voltages, () => updateChart(), { deep: true })

onMounted(() => {
  initChart()
  if (chartRef.value) {
    resizeObserver = new ResizeObserver(() => chartInstance?.resize())
    resizeObserver.observe(chartRef.value)
    themeObserver = new MutationObserver(updateChart)
    themeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['class', 'data-theme'] })
  }
})

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
.cell-voltage-chart { width: 100%; }
</style>
