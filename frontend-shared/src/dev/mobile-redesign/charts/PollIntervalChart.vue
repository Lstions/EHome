<!--
  轮询指令「间隔条形图」—— BMS 移动端改版 demo 专用（src/dev）。

  它解决的问题不是「好看」，而是**轮询配置在手机上根本看不出全局**：
  5 条指令、每条一个毫秒数，用户要自己把 1000ms / 5000ms / 60000ms 换算成
  「谁刷得快」。条形图把 5 条排在同一时间轴上，快慢一目了然。

  禁用（interval = 0）不画条，只在轴左侧留一个「已禁用」空标记 ——
  这正好把「1/3 名额已用」这件事可视化出来，而不是让用户去数开关。

  容器感知：grid 自适应 + 右侧数值标签固定留 46px；不做固定宽度。
-->
<template>
  <div ref="chartRef" class="poll-bars" :style="{ height: height }"></div>
</template>

<script setup lang="ts">
import { ref, onMounted, onUnmounted, watch } from 'vue'
import * as echarts from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { BarChart as BarChartSeries } from 'echarts/charts'
import { GridComponent, TooltipComponent } from 'echarts/components'
import { getThemeColors, getThemeSurfaceColors } from '@/utils/theme'

echarts.use([CanvasRenderer, BarChartSeries, GridComponent, TooltipComponent])

export interface PollBarItem {
  name: string
  /** 间隔毫秒，0 = 禁用 */
  intervalMs: number
}

const props = withDefaults(defineProps<{
  items: PollBarItem[]
  height?: string
}>(), {
  height: '132px',
})

const chartRef = ref<HTMLElement>()
let chartInstance: echarts.ECharts | null = null
let resizeObserver: ResizeObserver | null = null
let themeObserver: MutationObserver | null = null

/** 毫秒 → 秒，用于轴上刻度（单位只在轴标题出现一次） */
function toSeconds(ms: number): number {
  return ms / 1000
}

function buildOption() {
  const colors = getThemeColors()
  const surface = getThemeSurfaceColors()
  const names = props.items.map((i) => i.name)
  const secs = props.items.map((i) => toSeconds(i.intervalMs))
  const enabledMax = Math.max(...secs, 1)
  // 轴上界留 15% 余量，避免最长条顶到标签
  const axisMax = Math.ceil(enabledMax * 1.15)

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
        const arr = params as Array<{ name: string; value: number; dataIndex: number }>
        const p = arr[0]
        const item = props.items[p?.dataIndex ?? 0]
        if (!item || item.intervalMs <= 0) return `${item?.name ?? ''}<br/>未启用轮询`
        return `${item.name}<br/>间隔 <b>${secs[p.dataIndex]} 秒</b>`
      },
    },
    grid: { left: 0, right: 8, top: 6, bottom: 0, containLabel: true },
    xAxis: {
      type: 'value' as const,
      min: 0,
      max: axisMax,
      // 单位只在这里出现一次；行内不再重复
      name: '秒',
      nameTextStyle: { color: surface.regular, fontSize: 10, align: 'right' },
      axisLine: { show: false },
      axisTick: { show: false },
      axisLabel: { color: surface.regular, fontSize: 10 },
      splitLine: { lineStyle: { color: surface.split } },
    },
    yAxis: {
      type: 'category' as const,
      data: names,
      inverse: true,
      axisLine: { show: false },
      axisTick: { show: false },
      axisLabel: { color: surface.regular, fontSize: 10, width: 96, overflow: 'truncate' },
    },
    series: [{
      type: 'bar',
      barWidth: 9,
      // 禁用项画成 0 长度，但给一个浅色占位条，让"这一行存在但没启用"可读
      data: props.items.map((i) => (i.intervalMs > 0
        ? { value: toSeconds(i.intervalMs), itemStyle: { color: colors.primary } }
        : { value: axisMax * 0.012, itemStyle: { color: surface.split } })),
      itemStyle: { borderRadius: 4 },
      label: {
        show: true,
        position: 'right',
        fontSize: 10,
        color: surface.text,
        formatter: (p: { dataIndex: number }) => {
          const it = props.items[p.dataIndex]
          if (!it || it.intervalMs <= 0) return '未启用'
          return `${toSeconds(it.intervalMs)}s`
        },
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

watch(() => props.items, render, { deep: true })

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
.poll-bars { width: 100%; }
</style>
