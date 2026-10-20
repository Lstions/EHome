<template>
  <div ref="chartRef" class="cell-voltage-chart" :style="{ height: height }"></div>
</template>

<script setup lang="ts">
import { computed, ref, onMounted, onUnmounted, watch } from 'vue'
import * as echarts from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { BarChart as BarChartSeries } from 'echarts/charts'
import { GridComponent, TooltipComponent, TitleComponent, MarkLineComponent } from 'echarts/components'
// ⚠ 按需引入的 ECharts **必须显式注册 LabelLayout**，否则 series.labelLayout（紧凑态
// hideOverlap 防相邻柱压字）会被静默忽略 —— 2026-10-20 探针实测：未注册时 #4/#5
// 两个相邻极值标签仍叠成「3.499501」，注册后自动隐藏冲突标签。
import { LabelLayout } from 'echarts/features'
import { getThemeColors, getThemeSurfaceColors } from '@/utils/theme'
import { computeAdaptiveYAxisRange } from '@/utils/chartRange'

echarts.use([CanvasRenderer, BarChartSeries, GridComponent, TooltipComponent, TitleComponent, MarkLineComponent, LabelLayout])

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
/** 当前图表容器高度（由 ResizeObserver 维护）：父页窄屏把图区固定 112px，矮容器需切紧凑布局 */
const chartHeight = ref(0)

/**
 * 紧凑模式判定（**容器感知**，不新增 window 监听 —— 既有契约
 * BmsMobileResponsive.spec 要求「re-render 在 ResizeObserver 内」，组件源码不得出现 window 监听）：
 *   · 容器宽 < 768px ：手机宽度（393px 视口下容器实测 ~345px）
 *   · 容器高 < 180px ：父页在窄屏把图区固定到 112px（.cell-chart-host）
 * 桌面 1440px 的容器约 1400×300，两个条件都不满足 ⇒ 走原桌面配置（逐像素不变）。
 */
const isCompact = computed(() =>
  chartWidth.value > 0
  && (chartWidth.value < 768 || (chartHeight.value > 0 && chartHeight.value < 180)),
)

/**
 * 容器感知标签降级判定：
 * - 单柱可用宽度 ≥ 阈值：全量显示标签（v > 0）
 * - 单柱可用宽度 < 阈值：仅对越界节（<3.0 或 >3.6，与 getBarColors 阈值一致）与
 *   最高/最低节返回标签，其余返回 ''（全量精确数值仍可通过 axis tooltip 查看）
 * - 最高/最低按**首个下标**判定：并列极值只标注第一个，否则相邻柱标签会压字
 */
function shouldShowCellLabel(
  value: number,
  cellWidthPx: number,
  index: number,
  maxIndex: number,
  minIndex: number,
): boolean {
  if (cellWidthPx >= LABEL_DEGRADE_THRESHOLD) return value > 0
  if (value <= 0) return false
  if (value < 3.0 || value > 3.6) return true
  // 2026-10-20 修：旧实现把「数值等于最值」当作标注条件，
  // **并列最值会同时命中**，于是相邻柱的标签互相压字（实测 #4 与 #11 同为 3.227、
  // #8 与 #16 同为 3.234，移动端标签直接叠在一起）。
  // 改为按"首个最值下标"判定：并列时只标注第一个，其余交给 tooltip ——
  // 用户仍能一眼看到极值，但不会出现重叠。
  return index === maxIndex || index === minIndex
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
  // 并列极值只取**第一个**出现的位置：标签降级时只给它标注，避免相邻柱压字
  const maxIndex = values.findIndex(v => v === maxValue && v > 0)
  const minIndex = values.findIndex(v => v === minValue && v > 0)
  // 单柱可用宽度 = 容器宽度 / 电芯数，驱动标签降级（容器感知，非写死 media query）
  const cellWidthPx = props.cellCount > 0 ? chartWidth.value / props.cellCount : 0
  // 紧凑布局由容器尺寸推导（见 isCompact）：窄屏 393px / 矮容器 112px 为 true，桌面 1440×300 为 false
  const compact = isCompact.value

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
    // 紧凑态（393×112）把四边留白从百分比压到像素级，把高度让给柱体；桌面保持原值
    grid: compact
      ? { left: 2, right: 2, top: 16, bottom: 2, containLabel: true }
      : { left: '3%', right: '4%', bottom: '3%', top: '10%', containLabel: true },
    xAxis: {
      type: 'category' as const,
      data: labels,
      axisLine: { lineStyle: { color: surface.border } },
      // 紧凑态：16 个「#N」落在约 20px/柱的宽度里必然互相压字，只保留首/尾刻度；
      // 极端值由柱顶标签 + 卡头「最高/最低/压差」承担，精确值仍走 axis tooltip
      ...(compact ? { axisTick: { show: false } } : {}),
      axisLabel: compact
        ? {
            color: surface.regular,
            fontSize: 9,
            interval: 0,
            formatter: (v: string, i: number) => (i === 0 || i === labels.length - 1 ? v : ''),
          }
        : { color: surface.regular, fontSize: 11 }
    },
    yAxis: {
      type: 'value' as const,
      // 紧凑态去轴名（112px 高时轴名会顶掉柱顶标签的可用空间）
      name: compact ? '' : '电压(V)',
      min,
      max,
      axisLine: { lineStyle: { color: surface.border } },
      splitLine: { lineStyle: { color: surface.split } },
      axisLabel: compact
        ? { color: surface.regular, fontSize: 9, formatter: (v: number) => v.toFixed(3) }
        : { color: surface.regular, formatter: (v: number) => v.toFixed(3) },
      nameTextStyle: { color: surface.regular },
      // 紧凑态固定 3 个 tick（min / 中值 / max）：112px 高的图里更多刻度线只是噪点
      ...(compact ? { splitNumber: 2, interval: (max - min) / 2 } : {}),
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
          // p.dataIndex 是当前柱在 series.data 中的下标（与 values 同序）
          return shouldShowCellLabel(v, cellWidthPx, p.dataIndex, maxIndex, minIndex)
            ? v.toFixed(3)
            : ''
        },
        fontSize: compact ? 9 : 10,
        color: surface.text
      },
      // 紧凑态兜底：最高/最低常落在相邻柱（实测 #4 最低、#5 最高），两个标签横向必重叠；
      // hideOverlap 让 ECharts 隐藏冲突标签而不是压字。降级判定仍由 shouldShowCellLabel 决定
      // （函数签名与 36px 阈值不变），被隐藏的精确值在 tooltip 与卡头摘要里照常可读。
      ...(compact ? { labelLayout: { hideOverlap: true } } : {}),
      markLine: {
        silent: true,
        data: [
          {
            yAxis: 3.0,
            lineStyle: { color: colors.danger, type: 'dashed' },
            // 紧凑态收起「下限/上限」文字（112px 高会与柱顶标签抢位），虚线仍在；
            // 阈值文本由卡头图例「偏低 < 3.000V / 偏高 > 3.600V」承担
            label: compact ? { show: false } : { formatter: '下限', color: surface.regular },
          },
          {
            yAxis: 3.6,
            lineStyle: { color: colors.warning, type: 'dashed' },
            label: compact ? { show: false } : { formatter: '上限', color: surface.regular },
          },
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
  // 先记录初始容器尺寸，保证首帧渲染即采用正确的标签降级 / 紧凑布局
  if (chartRef.value) {
    chartWidth.value = chartRef.value.clientWidth
    chartHeight.value = chartRef.value.clientHeight
  }
  initChart()
  if (chartRef.value) {
    // 容器尺寸变化时同步重绘：更新宽高后 setOption 刷新（标签降级与紧凑布局随容器自适应）
    resizeObserver = new ResizeObserver(() => {
      if (!chartRef.value) return
      chartWidth.value = chartRef.value.clientWidth
      chartHeight.value = chartRef.value.clientHeight
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
