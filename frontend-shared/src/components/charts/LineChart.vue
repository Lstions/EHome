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
const SERIES_COLORS = ['var(--el-color-primary)', 'var(--el-color-danger)', 'var(--el-color-warning)', 'var(--el-color-success)', 'var(--el-text-color-secondary)']

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
  const source = (props.series && props.series.length > 0 && props.series[0].data.length > 0)
    ? props.series[0].data
    : props.data
  if (source.length === 0) return []

  const trendRange = props.series?.[0]?.data?.length
    ? (() => {
        const first = new Date(source[0].time)
        const last = new Date(source[source.length - 1].time)
        return (last.getTime() - first.getTime()) / 86400000
      })()
    : 0

  return source.map(item => {
    const d = new Date(item.time)
    if (trendRange > 2) {
      // 7d: MM/DD HH:mm
      return `${(d.getMonth() + 1).toString().padStart(2, '0')}/${d.getDate().toString().padStart(2, '0')} ${d.getHours().toString().padStart(2, '0')}:${d.getMinutes().toString().padStart(2, '0')}`
    } else {
      // 24h and 1h: HH:mm (seconds not useful on chart axis)
      return `${d.getHours().toString().padStart(2, '0')}:${d.getMinutes().toString().padStart(2, '0')}`
    }
  })
}

const getXAxisConfig = () => {
  const source = (props.series && props.series.length > 0 && props.series[0].data.length > 0)
    ? props.series[0].data
    : props.data
  const count = source.length
  const labels = getXAxisData()

  // Auto-calculate interval to show ~8-12 labels max
  const maxLabels = 10
  const interval = count > maxLabels ? Math.floor(count / maxLabels) : 0

  // Determine rotation based on data density
  const rotate = count > 50 ? 30 : count > 20 ? 15 : 0

  return {
    type: 'category' as const,
    data: labels,
    axisLabel: {
      rotate,
      interval,
      fontSize: 11
    },
    axisTick: { alignWithLabel: true }
  }
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
    xAxis: getXAxisConfig(),
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
    xAxis: getXAxisConfig(),
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
