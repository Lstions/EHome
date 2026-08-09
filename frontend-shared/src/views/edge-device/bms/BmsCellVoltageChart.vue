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

/** 柱顶标签降级阈值：单柱可用宽度(px)低于该值时，仅保留关键节标签（越界/最高/最低） */
const LABEL_DEGRADE_THRESHOLD = 36

/** 当前图表容器宽度（由 ResizeObserver 维护，驱动容器感知标签降级） */
const chartWidth = ref(0)

/**
 * 容器感知标签降级判定：
 * - 单柱可用宽度 ≥ 阈值：全量显示标签（v > 0）
 * - 单柱可用宽度 < 阈值：仅对越界节（<3.0 或 >3.6，与 getBarColors 阈值一致）与
 *   最高/最低节返回标签，其余返回 ''（全量精确数值仍可通过 axis tooltip 查看）
 */
function shouldShowCellLabel(value: number, cellWidthPx: number, maxValue: number, minValue: number): boolean {
  if (cellWidthPx >= LABEL_DEGRADE_THRESHOLD) return value > 0
  if (value <= 0) return false
  if (value < 3.0 || value > 3.6) return true
  return value === maxValue || value === minValue
}

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

  // 最高/最低节（降级模式下仍保留标签的关键信息）
  const maxValue = validValues.length > 0 ? Math.max(...validValues) : 0
  const minValue = validValues.length > 0 ? Math.min(...validValues) : 0
  // 单柱可用宽度 = 容器宽度 / 电芯数，驱动标签降级（容器感知，非写死 media query）
  const cellWidthPx = props.cellCount > 0 ? chartWidth.value / props.cellCount : 0

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
        formatter: (p: any) => {
          const v = p.value
          return shouldShowCellLabel(v, cellWidthPx, maxValue, minValue) ? v.toFixed(3) : ''
        },
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
  // 先记录初始容器宽度，保证首帧渲染即采用正确的标签降级策略
  if (chartRef.value) chartWidth.value = chartRef.value.clientWidth
  initChart()
  if (chartRef.value) {
    // 容器尺寸变化时同步重绘：更新宽度后 setOption 刷新（标签降级随宽度自适应）
    resizeObserver = new ResizeObserver(() => {
      if (!chartRef.value) return
      chartWidth.value = chartRef.value.clientWidth
      chartInstance?.resize()
      updateChart()
    })
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
