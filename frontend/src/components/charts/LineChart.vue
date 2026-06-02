<template>
  <div ref="chartRef" class="line-chart" :style="{ height: height }"></div>
</template>

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
import type { EChartsOption } from 'echarts/core'

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
}>(), {
  data: () => [],
  title: '',
  height: '400px',
  smooth: true,
  series: () => []
})

const chartRef = ref<HTMLElement>()
let chartInstance: echarts.ECharts | null = null

// Default color palette
const SERIES_COLORS = ['#409eff', '#f56c6c', '#e6a23c', '#67c23a', '#909399']

const buildSeries = () => {
  // Multi-series mode: use props.series
  if (props.series && props.series.length > 0) {
    const firstSeries = props.series[0]
    return props.series.map((s, i) => ({
      name: s.name + (s.unit ? ` (${s.unit})` : ''),
      type: 'line' as const,
      data: s.data.map(item => item.value),
      smooth: props.smooth,
      yAxisIndex: i,
      lineStyle: { width: 2, color: SERIES_COLORS[i % SERIES_COLORS.length] },
      itemStyle: { color: SERIES_COLORS[i % SERIES_COLORS.length] },
      areaStyle: {
        color: new echarts.graphic.LinearGradient(0, 0, 0, 1, [
          { offset: 0, color: SERIES_COLORS[i % SERIES_COLORS.length] + '33' },
          { offset: 1, color: SERIES_COLORS[i % SERIES_COLORS.length] + '0a' }
        ])
      }
    }))
  }

  // Single-series mode: use props.data
  return [{
    name: '数值',
    type: 'line' as const,
    data: props.data.map(item => item.value),
    smooth: props.smooth,
    lineStyle: { width: 2, color: SERIES_COLORS[0] },
    itemStyle: { color: SERIES_COLORS[0] },
    areaStyle: {
      color: new echarts.graphic.LinearGradient(0, 0, 0, 1, [
        { offset: 0, color: SERIES_COLORS[0] + '33' },
        { offset: 1, color: SERIES_COLORS[0] + '0a' }
      ])
    }
  }]
}

const getXAxisData = () => {
  if (props.series && props.series.length > 0 && props.series[0].data.length > 0) {
    return props.series[0].data.map(item => {
      const date = new Date(item.time)
      return `${date.getHours()}:${date.getMinutes().toString().padStart(2, '0')}`
    })
  }
  return props.data.map(item => {
    const date = new Date(item.time)
    return `${date.getHours()}:${date.getMinutes().toString().padStart(2, '0')}`
  })
}

const initChart = () => {
  if (!chartRef.value) return

  chartInstance = echarts.init(chartRef.value)

  const multiSeries = props.series && props.series.length > 1
  const yAxisConfig = multiSeries
    ? props.series!.map((s, i) => ({
        type: 'value' as const,
        name: s.name + (s.unit ? ` (${s.unit})` : ''),
        position: i === 0 ? 'left' : 'right' as 'right',
        offset: i > 1 ? (i - 1) * 60 : 0,
        axisLabel: { formatter: (v: number) => v.toFixed(1) }
      }))
    : undefined

  const option: EChartsOption = {
    title: { text: props.title, left: 'center' },
    tooltip: {
      trigger: 'axis',
      formatter: (params: any) => {
        if (!Array.isArray(params) || params.length === 0) return ''
        const time = params[0].name
        let html = `${time}<br/>`
        params.forEach((p: any) => {
          html += `${p.marker} ${p.seriesName}: <b>${typeof p.value === 'number' ? p.value.toFixed(2) : p.value}</b><br/>`
        })
        return html
      }
    },
    legend: multiSeries ? { top: 30 } : undefined,
    grid: { left: '3%', right: multiSeries ? '8%' : '4%', bottom: '3%', containLabel: true },
    xAxis: {
      type: 'category',
      data: getXAxisData(),
      axisLabel: { rotate: 45, interval: 0 }
    },
    yAxis: yAxisConfig || {
      type: 'value',
      axisLabel: { formatter: (value: number) => value.toFixed(2) }
    },
    series: buildSeries()
  }

  chartInstance.setOption(option)

  const resizeObserver = new ResizeObserver(() => chartInstance?.resize())
  resizeObserver.observe(chartRef.value)
}

// Watch for data changes
watch(() => [props.data, props.series], () => {
  if (!chartInstance) return

  const multiSeries = props.series && props.series.length > 1
  const yAxisConfig = multiSeries
    ? props.series!.map((s, i) => ({
        type: 'value' as const,
        name: s.name + (s.unit ? ` (${s.unit})` : ''),
        position: i === 0 ? 'left' : 'right' as 'right',
        offset: i > 1 ? (i - 1) * 60 : 0,
        axisLabel: { formatter: (v: number) => v.toFixed(1) }
      }))
    : {
        type: 'value' as const,
        axisLabel: { formatter: (value: number) => value.toFixed(2) }
      }

  chartInstance.setOption({
    legend: multiSeries ? { top: 30 } : undefined,
    grid: { left: '3%', right: multiSeries ? '8%' : '4%', bottom: '3%', containLabel: true },
    xAxis: { type: 'category', data: getXAxisData(), axisLabel: { rotate: 45, interval: 0 } },
    yAxis: yAxisConfig,
    series: buildSeries()
  })
}, { deep: true })

onUnmounted(() => {
  chartInstance?.dispose()
  chartInstance = null
})

onMounted(() => {
  initChart()
})
</script>

<style scoped>
.line-chart {
  width: 100%;
}
</style>
