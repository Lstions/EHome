<template>
  <div ref="chartRef" class="cell-voltage-chart" :style="{ height: height }"></div>
</template>

<script setup lang="ts">
import { ref, onMounted, onUnmounted, watch } from 'vue'
import * as echarts from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { BarChart as BarChartSeries } from 'echarts/charts'
import { GridComponent, TooltipComponent, TitleComponent, MarkLineComponent } from 'echarts/components'
import { THEME_COLORS } from '@/utils/theme'

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

function getBarColors(values: number[]): string[] {
  return values.map(v => {
    if (v < 3.0) return THEME_COLORS.danger    // red - too low
    if (v > 3.6) return THEME_COLORS.warning   // orange - too high
    return THEME_COLORS.success                 // green - normal
  })
}

/**
 * Compute adaptive Y-axis range from actual cell voltage data.
 * - Uses data min/max with padding so small voltage differences are visible.
 * - Clamps to safe bounds so the chart never looks absurd with bad data.
 * - Falls back to a sensible default range when no data.
 */
function computeYAxisRange(values: number[]): { min: number; max: number } {
  const valid = values.filter(v => typeof v === 'number' && v > 0)
  if (valid.length === 0) return { min: 2.5, max: 4.0 }

  const dataMin = Math.min(...valid)
  const dataMax = Math.max(...valid)
  const span = dataMax - dataMin

  // If all cells are identical, create a small window around the value
  const padding = span < 0.05 ? 0.05 : span * 0.3
  let min = dataMin - padding
  let max = dataMax + padding

  // Clamp to safe bounds
  if (min < 2.0) min = 2.0
  if (max > 4.5) max = 4.5

  // Ensure minimum visible range
  if (max - min < 0.1) {
    const center = (min + max) / 2
    min = center - 0.05
    max = center + 0.05
  }

  // Round to nice values
  min = Math.floor(min * 100) / 100
  max = Math.ceil(max * 100) / 100

  return { min, max }
}

function buildChartOption() {
  const labels = Array.from({ length: props.cellCount }, (_, i) => `#${i + 1}`)
  const values = props.voltages.length > 0
    ? props.voltages.slice(0, props.cellCount)
    : new Array(props.cellCount).fill(0)

  const { min, max } = computeYAxisRange(values)

  return {
    tooltip: {
      trigger: 'axis' as const,
      formatter: (params: any) => {
        const p = Array.isArray(params) ? params[0] : params
        return `${p.name}<br/>电压: <b>${typeof p.value === 'number' ? p.value.toFixed(3) : '0.000'}V</b>`
      }
    },
    grid: { left: '3%', right: '4%', bottom: '3%', top: '10%', containLabel: true },
    xAxis: {
      type: 'category' as const,
      data: labels,
      axisLabel: { fontSize: 11 }
    },
    yAxis: {
      type: 'value' as const,
      name: '电压(V)',
      min,
      max,
      axisLabel: { formatter: (v: number) => v.toFixed(3) }
    },
    series: [{
      type: 'bar',
      data: values.map(v => ({
        value: v,
        itemStyle: { color: v === 0 ? THEME_COLORS.info : getBarColors([v])[0] }
      })),
      barWidth: '60%',
      label: {
        show: true,
        position: 'top',
        formatter: (p: any) => p.value > 0 ? p.value.toFixed(3) : '',
        fontSize: 10
      },
      markLine: {
        silent: true,
        data: [
          { yAxis: 3.0, lineStyle: { color: THEME_COLORS.danger, type: 'dashed' }, label: { formatter: '下限' } },
          { yAxis: 3.6, lineStyle: { color: THEME_COLORS.warning, type: 'dashed' }, label: { formatter: '上限' } },
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

function handleResize() {
  chartInstance?.resize()
}

watch(() => props.voltages, () => {
  if (!chartInstance) return
  chartInstance.setOption(buildChartOption())
}, { deep: true })

onMounted(() => {
  initChart()
  window.addEventListener('resize', handleResize)
})

onUnmounted(() => {
  window.removeEventListener('resize', handleResize)
  chartInstance?.dispose()
  chartInstance = null
})
</script>

<style scoped>
.cell-voltage-chart { width: 100%; }
</style>
