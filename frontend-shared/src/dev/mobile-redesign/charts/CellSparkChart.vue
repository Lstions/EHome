<!--
  电芯电压「紧凑分布条」—— BMS 移动端改版 demo 专用（src/dev，DEV 门禁路由）。

  为什么需要它：生产页 BmsCellVoltageChart 在 393px 下是 300px 高，
  带 Y 轴名、图例、参考线、脚注说明，一屏放不下一块 + 旁边的温度；
  但它真正要回答的问题只有两个：「有没有掉队的串」「压差多大」。

  本图只保留回答这两个问题所需的视觉：16 根柱 + 自适应 Y 量程 + 最高/最低标注。
  精确数字（最高/最低/压差）不靠图中字号，而是由调用方在**图外**用正文文本给出
  —— 手机上 10px 刻度值等于没有，不如让 HTML 正文承担文字通道。

  容器感知：单柱宽度 < 阈值时不画柱顶数字（与 BmsCellVoltageChart 同一降级思路，
  阈值常量一致），避免 16 根柱的标签互相压字。
-->
<template>
  <div ref="chartRef" class="cell-spark" :style="{ height: height }"></div>
</template>

<script setup lang="ts">
import { ref, onMounted, onUnmounted, watch } from 'vue'
import * as echarts from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { BarChart as BarChartSeries } from 'echarts/charts'
import { GridComponent, TooltipComponent, MarkLineComponent } from 'echarts/components'
import { getThemeColors, getThemeSurfaceColors } from '@/utils/theme'
import { computeAdaptiveYAxisRange } from '@/utils/chartRange'

echarts.use([CanvasRenderer, BarChartSeries, GridComponent, TooltipComponent, MarkLineComponent])

const props = withDefaults(defineProps<{
  values: number[]
  /** 柱顶数字的最小单柱宽度（px）；低于它只标最高/最低，其余交给 tooltip */
  labelMinCellWidth?: number
  height?: string
  /** 正常色；越界柱分别用 warning / danger（与项目既有阈值口径一致） */
  lowThreshold?: number
  highThreshold?: number
}>(), {
  labelMinCellWidth: 34,
  height: '72px',
  lowThreshold: 3.0,
  highThreshold: 3.6,
})

const chartRef = ref<HTMLElement>()
let chartInstance: echarts.ECharts | null = null
let resizeObserver: ResizeObserver | null = null
let themeObserver: MutationObserver | null = null
const chartWidth = ref(0)

function buildOption() {
  const colors = getThemeColors()
  const surface = getThemeSurfaceColors()
  const values = props.values
  const count = values.length
  const labels = values.map((_, i) => `#${i + 1}`)

  const valid = values.filter((v) => Number.isFinite(v) && v > 0)
  const { min, max } = valid.length === 0
    ? { min: 2.8, max: 4.2 }
    : computeAdaptiveYAxisRange(valid, { minBound: 2.0, maxBound: 4.5, decimals: 2 })

  const maxValue = valid.length ? Math.max(...valid) : 0
  const minValue = valid.length ? Math.min(...valid) : 0
  // 并列极值只取第一个下标：标签降级时相邻柱不会压字
  const maxIndex = values.findIndex((v) => v === maxValue && v > 0)
  const minIndex = values.findIndex((v) => v === minValue && v > 0)
  const cellWidthPx = count > 0 ? chartWidth.value / count : 0
  const showAllLabels = cellWidthPx >= props.labelMinCellWidth

  const colorOf = (v: number) => {
    if (!Number.isFinite(v) || v <= 0) return colors.info
    if (v < props.lowThreshold) return colors.danger
    if (v > props.highThreshold) return colors.warning
    // 正常柱用 success 的浅一档：16 根实心深绿在暗色下过重，浅底更耐看
    return colors.success
  }

  return {
    animation: false,
    // 图内不留标题/图例/轴名：这些信息在图外的正文里，重复渲染只是再占 40px
    tooltip: {
      trigger: 'axis' as const,
      confine: true,
      position: (p: number[]) => [p[0] + 10, p[1]],
      backgroundColor: surface.overlay,
      borderColor: surface.border,
      textStyle: { color: surface.text, fontSize: 12 },
      formatter: (params: unknown) => {
        const arr = params as Array<{ name: string; value: number }>
        const p = arr[0]
        const v = typeof p?.value === 'number' ? p.value.toFixed(3) : '—'
        return `${p?.name ?? ''}<br/>单体电压 <b>${v} V</b>`
      },
    },
    // containLabel + 极窄 left：Y 轴刻度值只需要"能验证量级"，不需要宽栏位
    grid: { left: 2, right: 2, top: 14, bottom: 2, containLabel: true },
    xAxis: {
      type: 'category' as const,
      data: labels,
      axisLine: { lineStyle: { color: surface.border } },
      axisTick: { show: false },
      // 16 个标签在 340px 里必然重叠：只保留首/尾/极值四处，其余为空串
      axisLabel: {
        color: surface.regular,
        fontSize: 9,
        interval: 0,
        formatter: (_v: string, i: number) =>
          i === 0 || i === count - 1 || i === maxIndex || i === minIndex ? _v : '',
      },
    },
    yAxis: {
      type: 'value' as const,
      min,
      max,
      splitNumber: 2,
      axisLine: { show: false },
      axisTick: { show: false },
      splitLine: { lineStyle: { color: surface.split } },
      axisLabel: { color: surface.regular, fontSize: 9, formatter: (v: number) => v.toFixed(2) },
    },
    series: [{
      type: 'bar',
      data: values.map((v) => ({ value: v, itemStyle: { color: colorOf(v) } })),
      barWidth: '62%',
      label: {
        show: true,
        position: 'top',
        fontSize: 9,
        color: surface.text,
        formatter: (p: { value: number; dataIndex: number }) => {
          if (!(p.value > 0)) return ''
          if (showAllLabels) return p.value.toFixed(3)
          return p.dataIndex === maxIndex || p.dataIndex === minIndex ? p.value.toFixed(3) : ''
        },
      },
      markLine: {
        silent: true,
        symbol: 'none',
        data: [{ yAxis: 3.0, lineStyle: { color: colors.danger, type: 'dashed', width: 1, opacity: 0.5 } }],
        label: { show: false },
      },
    }],
  }
}

function render() {
  if (!chartInstance) return
  chartInstance.setOption(buildOption(), { notMerge: true })
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
  // 亮暗主题切换必须重取 token（Canvas 拿不到 CSS 变量）
  themeObserver = new MutationObserver(render)
  themeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['class', 'data-theme'] })
})

watch(() => props.values, render, { deep: true })

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
.cell-spark { width: 100%; }
</style>
