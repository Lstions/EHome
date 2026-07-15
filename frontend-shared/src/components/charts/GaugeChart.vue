<template>
  <div ref="chartRef" class="gauge-chart" :style="{ width: width, height: height }"></div>
</template>

<script setup lang="ts">
import { ref, onMounted, onUnmounted, watch } from 'vue'
import * as echarts from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { GaugeChart as GaugeChartSeries } from 'echarts/charts'
import { TitleComponent, TooltipComponent } from 'echarts/components'
import type { EChartsOption } from 'echarts/core'

echarts.use([CanvasRenderer, GaugeChartSeries, TitleComponent, TooltipComponent])

const props = withDefaults(defineProps<{
  value: number
  min?: number
  max?: number
  unit?: string
  title?: string
  width?: string
  height?: string
}>(), { min: 0, max: 100, unit: '', title: '', width: '200px', height: '200px' })

const chartRef = ref<HTMLElement>()
let chartInstance: echarts.ECharts | null = null
let resizeObserver: ResizeObserver | null = null
let themeObserver: MutationObserver | null = null

const token = (name: string, fallback: string) =>
  getComputedStyle(document.documentElement).getPropertyValue(name).trim() || fallback

const applyOption = () => {
  if (!chartInstance) return
  const range = props.max - props.min
  const percent = range === 0 ? 0 : ((props.value - props.min) / range) * 100
  const accent = percent > 80 ? token('--color-danger', '#f56c6c')
    : percent > 60 ? token('--color-warning', '#e6a23c')
      : token('--color-primary', '#409eff')
  const text = token('--text-color-primary', '#303133')
  const track = token('--border-color-light', '#ebeef5')
  const option: EChartsOption = {
    series: [{
      type: 'gauge', min: props.min, max: props.max,
      startAngle: 225, endAngle: -45, radius: '75%', center: ['50%', '60%'],
      axisLine: { lineStyle: { width: 10, color: [[1, track]] } },
      progress: { show: true, roundCap: true, width: 10, itemStyle: { color: accent, shadowColor: token('--card-shadow', 'rgba(0,0,0,.2)'), shadowBlur: 8 } },
      pointer: { itemStyle: { color: accent } },
      axisTick: { lineStyle: { color: track } },
      splitLine: { lineStyle: { color: track } },
      axisLabel: { color: token('--text-color-secondary', '#909399') },
      data: [{
        value: props.value,
        name: props.title || '数值',
        title: { offsetCenter: [0, '50%'], color: text, fontSize: 14 },
        detail: { formatter: (value: number) => `${value.toFixed(2)}${props.unit}`, color: text, fontSize: 20, offsetCenter: [0, '65%'] }
      }]
    }]
  }
  chartInstance.setOption(option, { notMerge: true })
}

watch(() => [props.value, props.min, props.max, props.unit, props.title], applyOption)

onMounted(() => {
  if (!chartRef.value) return
  chartInstance = echarts.init(chartRef.value)
  applyOption()
  resizeObserver = new ResizeObserver(() => chartInstance?.resize())
  resizeObserver.observe(chartRef.value)
  themeObserver = new MutationObserver(applyOption)
  themeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['class', 'data-theme'] })
})

onUnmounted(() => {
  resizeObserver?.disconnect()
  themeObserver?.disconnect()
  chartInstance?.dispose()
  resizeObserver = null
  themeObserver = null
  chartInstance = null
})
</script>

<style scoped>
.gauge-chart {
}
</style>
