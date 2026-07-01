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
  yAxisMin?: number
  yAxisMax?: number
  showArea?: boolean
  realtime?: boolean
}>(), {
  data: () => [],
  title: '',
  height: '400px',
  smooth: true,
  series: () => [],
  showArea: true,
  realtime: false
})

const chartRef = ref<HTMLElement>()
let chartInstance: echarts.ECharts | null = null

// Default color palette
const SERIES_COLORS = ['#409eff', '#f56c6c', '#e6a23c', '#67c23a', '#909399', '#8b5cf6', '#06b6d4', '#f97316']

/**
 * Build yAxis list for multi-series mode.
 * Series with the same unit share one Y-axis (left side).
 * Series with different units get their own Y-axis (offset right).
 */
const buildYAxisList = () => {
  if (!props.series || props.series.length <= 1) return undefined
  const unitGroups: string[] = []
  const seriesToYAxis: number[] = []
  props.series.forEach(s => {
    const unit = s.unit || ''
    const existingIdx = unitGroups.indexOf(unit)
    if (existingIdx >= 0) {
      seriesToYAxis.push(existingIdx)
    } else {
      seriesToYAxis.push(unitGroups.length)
      unitGroups.push(unit)
    }
  })
  const hasCustomRange = props.yAxisMin !== undefined && props.yAxisMax !== undefined
  return {
    yAxisList: unitGroups.map((unit, i) => ({
      type: 'value' as const,
      name: unit ? `(${unit})` : '数值',
      position: i === 0 ? 'left' as 'left' : 'right' as 'right',
      offset: i > 1 ? (i - 1) * 60 : 0,
      min: props.yAxisMin,
      max: props.yAxisMax,
      axisLabel: { formatter: (v: number) => hasCustomRange ? v.toFixed(2) : v.toFixed(1) }
    })),
    seriesToYAxis
  }
}

const buildSeries = (seriesToYAxis?: number[]) => {
  // Multi-series mode: use props.series — data as [timestamp, value] pairs
  if (props.series && props.series.length > 0) {
    return props.series.map((s, i) => ({
      name: s.name + (s.unit ? ` (${s.unit})` : ''),
      type: 'line' as const,
      data: s.data.map(item => [new Date(item.time).getTime(), item.value]),
      smooth: props.smooth,
      sampling: 'lttb' as const,
      yAxisIndex: seriesToYAxis ? seriesToYAxis[i] : i,
      lineStyle: { width: 2, color: SERIES_COLORS[i % SERIES_COLORS.length] },
      itemStyle: { color: SERIES_COLORS[i % SERIES_COLORS.length] },
      showSymbol: s.data.length > 50 ? false : true,
      ...(props.showArea ? {
        areaStyle: {
          color: new echarts.graphic.LinearGradient(0, 0, 0, 1, [
            { offset: 0, color: SERIES_COLORS[i % SERIES_COLORS.length] + '33' },
            { offset: 1, color: SERIES_COLORS[i % SERIES_COLORS.length] + '0a' }
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
    lineStyle: { width: 2, color: SERIES_COLORS[0] },
    itemStyle: { color: SERIES_COLORS[0] },
    ...(props.showArea ? {
      areaStyle: {
        color: new echarts.graphic.LinearGradient(0, 0, 0, 1, [
          { offset: 0, color: SERIES_COLORS[0] + '33' },
          { offset: 1, color: SERIES_COLORS[0] + '0a' }
        ])
      }
    } : {})
  }]
}

/**
 * Time-based xAxis config.
 * Uses type: 'time' so series with different timestamps align correctly.
 */
const getXAxisConfig = () => {
  return {
    type: 'time' as const,
    axisLabel: {
      fontSize: 11,
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

const initChart = () => {
  if (!chartRef.value) return

  chartInstance = echarts.init(chartRef.value)

  const multiSeries = props.series && props.series.length > 1
  const yAxisInfo = buildYAxisList()
  const yAxisConfig = yAxisInfo?.yAxisList

  const option: EChartsOption = {
    animation: props.realtime ? false : undefined,
    title: { text: props.title, left: 'center' },
    tooltip: {
      trigger: 'axis',
      formatter: (params: any) => {
        if (!Array.isArray(params) || params.length === 0) return ''
        const ts = params[0].axisValueLabel || params[0].name
        const d = new Date(ts)
        const timeStr = `${(d.getMonth() + 1).toString().padStart(2, '0')}/${d.getDate().toString().padStart(2, '0')} ${d.getHours().toString().padStart(2, '0')}:${d.getMinutes().toString().padStart(2, '0')}:${d.getSeconds().toString().padStart(2, '0')}`
        let html = `${timeStr}<br/>`
        params.forEach((p: any) => {
          const val = Array.isArray(p.value) ? p.value[1] : p.value
          html += `${p.marker} ${p.seriesName}: <b>${typeof val === 'number' ? val.toFixed(3) : val}</b><br/>`
        })
        return html
      }
    },
    legend: multiSeries ? { top: 30, type: 'scroll' as const } : undefined,
    grid: { left: '3%', right: multiSeries ? '8%' : '4%', bottom: '3%', containLabel: true },
    xAxis: getXAxisConfig(),
    yAxis: yAxisConfig || {
      type: 'value',
      min: props.yAxisMin,
      max: props.yAxisMax,
      axisLabel: { formatter: (value: number) => value.toFixed(2) }
    },
    series: buildSeries(yAxisInfo?.seriesToYAxis)
  }

  chartInstance.setOption(option)

  const resizeObserver = new ResizeObserver(() => chartInstance?.resize())
  resizeObserver.observe(chartRef.value)
}

// Watch for data changes
watch(() => [props.data, props.series], () => {
  if (!chartInstance) return

  const multiSeries = props.series && props.series.length > 1
  const yAxisInfo = buildYAxisList()
  const yAxisConfig = yAxisInfo?.yAxisList || {
    type: 'value' as const,
    min: props.yAxisMin,
    max: props.yAxisMax,
    axisLabel: { formatter: (value: number) => value.toFixed(2) }
  }

  chartInstance.setOption({
    animation: props.realtime ? false : undefined,
    legend: multiSeries ? { top: 30, type: 'scroll' as const } : undefined,
    grid: { left: '3%', right: multiSeries ? '8%' : '4%', bottom: '3%', containLabel: true },
    xAxis: getXAxisConfig(),
    yAxis: yAxisConfig,
    series: buildSeries(yAxisInfo?.seriesToYAxis)
  }, { replaceMerge: ['series'] })
}, { deep: !props.realtime })

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
