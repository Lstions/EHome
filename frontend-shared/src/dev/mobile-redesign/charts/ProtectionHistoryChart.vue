<!--
  保护触发统计（统计类图表，BMS 移动端改版 demo 专用）—— src/dev。

  ── 它回答的问题（现有页面答不了）─────────────────────────────────────────
  「保护状态」只显示**当前**位掩码（本例 0x00 = 12 项全绿）。但设备上报过
  「读取保护历史次数」，即 12 类保护的累计触发次数，却没有任何页面展示它。
  后果：一台"现在正常"的 BMS 可能上周放电过流触发过 3 次 —— 只看当前位掩码
  永远发现不了。这是**新信息**，不是把旧数据换个画法。

  ── 两版的实测教训（都写进注释防回退）────────────────────────────────────
  v1 把 12 个类别全画成行：
    · 156px 高度里 12 行 ⇒ 每行 13px，ECharts 自动**隐藏了一半类别标签**
      （实测只剩 6 个标签，另外 6 个静默消失 —— 读者不知道还有什么被跳过了）；
    · 0 值条的数值标签 position:'right' 落在 x≈0，**压在 y 轴类别标签上**
      （截图中「单体过压」右侧叠了一个 0）。
  v2（当前）：把"有触发"与"没触发"分开表达 ——
    · 只有 count > 0 的项画条形（本例 3 项），每行 22px，标签必定完整；
    · 其余项合并成**一行**说明「其余 9 项未触发」；
    · 12 项里 9 项恒为 0，用 9 行去说"它们是 0"是纯冗余 —— 这与"保护状态
      12 行文本"是同一个信息设计错误，只是换了个图形。

  ── 容器感知 ──────────────────────────────────────────────────────────────
  y 轴标签宽度随容器截断（overflow:'truncate'），数值标签固定右侧 30px 留白；
  行高按条目数动态算（每个 22px），不给固定高度 —— 3 项时不出现大片空白。
-->
<template>
  <div class="protect-hist">
    <!-- 无任何历史触发：一行结论，不画空图 -->
    <div v-if="!hitItems.length" class="hist-empty">
      <el-icon :size="14"><CircleCheckFilled /></el-icon>
      <span>{{ windowText }} 12 类保护均无触发记录</span>
    </div>

    <template v-else>
      <div ref="chartRef" class="hist-chart" :style="{ height: chartHeight }"></div>
      <!-- 未触发项合并成一行：把"我看过其余 9 项"这件事说清楚，而不占用 9 行 -->
      <p class="hist-zero">
        <span class="zero-dot" aria-hidden="true"></span>
        其余 {{ zeroCount }} 项{{ windowText }}未触发
      </p>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, onMounted, onUnmounted, watch, nextTick } from 'vue'
import { CircleCheckFilled } from '@element-plus/icons-vue'
import * as echarts from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { BarChart as BarChartSeries } from 'echarts/charts'
import { GridComponent, TooltipComponent } from 'echarts/components'
import { getThemeColors, getThemeSurfaceColors } from '@/utils/theme'

echarts.use([CanvasRenderer, BarChartSeries, GridComponent, TooltipComponent])

export interface ProtectCount {
  label: string
  count: number
}

const props = withDefaults(defineProps<{
  items: ProtectCount[]
  /** 统计窗口文案，用于结论与 tooltip（如「近 30 天」） */
  windowText?: string
}>(), {
  windowText: '近 30 天',
})

const chartRef = ref<HTMLElement>()
let chartInstance: echarts.ECharts | null = null
let resizeObserver: ResizeObserver | null = null
let themeObserver: MutationObserver | null = null

/** 有触发的项（按次数降序 —— 最该被先看到的排最上） */
const hitItems = computed(() =>
  props.items.filter((i) => i.count > 0).sort((a, b) => b.count - a.count))
const zeroCount = computed(() => props.items.length - hitItems.value.length)

/** 行高 22px：11px 字号 + 上下留白，保证 10px 的类目标签不重叠 */
const ROW_PX = 22
const chartHeight = computed(() => Math.max(hitItems.value.length * ROW_PX + 18, 64) + 'px')

function buildOption() {
  const colors = getThemeColors()
  const surface = getThemeSurfaceColors()
  const items = hitItems.value
  const counts = items.map((i) => i.count)
  const maxCount = Math.max(...counts, 1)
  // 上界至少为 1，但留 25% 余量让最长条的数值标签不顶到边
  const axisMax = Math.max(1, Math.ceil(maxCount * 1.25))

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
        const arr = params as Array<{ dataIndex: number }>
        const it = items[arr[0]?.dataIndex ?? 0]
        return it ? `${it.label}<br/>${props.windowText}触发 <b>${it.count} 次</b>` : ''
      },
    },
    grid: { left: 2, right: 30, top: 2, bottom: 2, containLabel: true },
    xAxis: {
      type: 'value' as const,
      min: 0,
      max: axisMax,
      // 次数是整数：刻度不出现 0.5
      minInterval: 1,
      axisLine: { show: false },
      axisTick: { show: false },
      axisLabel: { color: surface.regular, fontSize: 10 },
      splitLine: { lineStyle: { color: surface.split } },
    },
    yAxis: {
      type: 'category' as const,
      data: items.map((i) => i.label),
      inverse: true,
      // 强制每行都出标签：条目已按"只留有触发的"筛过，行数少，不会再挤
      axisLabel: {
        color: surface.regular,
        fontSize: 11,
        interval: 0,
        width: 84,
        overflow: 'truncate',
      },
      axisLine: { show: false },
      axisTick: { show: false },
    },
    series: [{
      type: 'bar',
      barWidth: 8,
      data: counts.map((c) => ({ value: c, itemStyle: { color: colors.warning } })),
      itemStyle: { borderRadius: 4 },
      label: {
        show: true,
        position: 'right',
        fontSize: 11,
        color: surface.text,
        formatter: (p: { value: number }) => String(p.value),
      },
    }],
  }
}

function render() {
  if (!chartInstance) return
  chartInstance.setOption(buildOption(), { notMerge: true })
  chartInstance.resize()
}

function mount() {
  if (!chartRef.value || chartInstance) return
  chartInstance = echarts.init(chartRef.value)
  render()
  resizeObserver = new ResizeObserver(() => {
    chartInstance?.resize()
    render()
  })
  resizeObserver.observe(chartRef.value)
}

onMounted(() => {
  themeObserver = new MutationObserver(render)
  themeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['class', 'data-theme'] })
  // hitItems 为空时图表容器是 v-if 掉的，需等 DOM 出来再挂载
  if (hitItems.value.length) mount()
})

watch(hitItems, async () => {
  if (!hitItems.value.length) {
    chartInstance?.dispose()
    chartInstance = null
    return
  }
  await nextTick()
  if (!chartInstance) mount()
  else render()
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
.protect-hist { display: flex; flex-direction: column; gap: 6px; }
.hist-chart { width: 100%; }

.hist-empty {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  line-height: 1.5;
  color: var(--el-color-success);
}

/* 未触发项的合并说明：中性灰，不与"有触发"的 warning 色抢注意力 */
.hist-zero {
  display: flex;
  align-items: center;
  gap: 6px;
  margin: 0;
  font-size: 11px;
  line-height: 1.5;
  color: var(--el-text-color-secondary);
}
.zero-dot {
  flex-shrink: 0;
  width: 8px;
  height: 8px;
  border-radius: 2px;
  background: var(--el-fill-color);
  border: 1px solid var(--el-border-color);
}
</style>
