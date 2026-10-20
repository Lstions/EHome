<!--
  温度趋势卡（diag #8）—— 生产实现。

  ── 只渲染内容 ──────────────────────────────────────────────────────────────
  外壳 / 折叠头（标题+摘要）由父页统一渲染；本组件只出内容区。
  内容区高度目标 ≤217px（不含父页折叠头）：
    工具行 28 + 折线 160 + 说明 16 + 间距 12 ≈ 216px

  ── 为什么这张图有价值（不是把瞬时值换个画法）─────────────────────────────
  页面上的「温度探头」只给 3 个瞬时数字。要判断"某一探头是否单独偏高
  （局部发热/探头故障）"必须看时间维：3 路一起升 = 环境升温；
  只有 1 路升 = 该测点异常。这是截面数据回答不了的问题。

  ── 45℃ 告警线怎么画 ────────────────────────────────────────────────────────
  复用项目 LineChart 组件（它注册的 ECharts 模块里**没有 MarkLineComponent**，
  所以不能传 markLine）。这里用 CSS 绝对定位虚线 + 图外文字说明：
    · 显式给 yAxisMin=20 / yAxisMax=50，让量程固定、虚线位置可计算；
    · 位置按 LineChart 的 grid 约定换算（top 取 ECharts 默认 60，底部留 X 轴标签位）；
    · 同时用文字给出精确结论（"当前最高 28.1℃，距告警 16.9℃"），
      即使虚线有 1~2px 视觉偏差，读者也不会误判。
-->
<template>
  <div class="diag-body">
    <div class="diag-toolbar">
      <span class="diag-tip" v-if="maxTemp !== null">最高 {{ maxTemp.toFixed(1) }} ℃</span>
      <TimeRangeSelector
        v-model="timeRange"
        v-model:custom-range="customTimeRange"
        class="range-ctl"
        @change="onRangeChange"
      />
    </div>

    <!-- 固定 160px 的画布区：loading/empty/error 都占同高，父页高度不抖 -->
    <div class="diag-plot" :style="{ '--plot-h': PLOT_H + 'px' }">
      <div v-if="!online" class="state-line">
        <el-icon :size="14"><WarningFilled /></el-icon>
        <span>设备离线，温度历史暂不可用</span>
      </div>
      <div v-else-if="loading && !loaded" class="state-line is-muted">正在加载温度趋势…</div>
      <div v-else-if="errorMsg" class="state-line is-error" role="alert">
        <el-icon :size="14"><CircleCloseFilled /></el-icon>
        <span class="err-text">温度趋势加载失败：{{ errorMsg }}</span>
        <el-button link size="small" type="primary" @click="load">重试</el-button>
      </div>
      <div v-else-if="!hasData" class="state-line is-muted">暂无温度历史数据</div>

      <template v-else>
        <LineChart
          :series="chartSeries"
          :y-axis-min="Y_MIN"
          :y-axis-max="Y_MAX"
          :show-area="false"
          height="160px"
        />
        <!-- 45℃ 告警线（CSS 定位：LineChart 未注册 MarkLine 模块） -->
        <span class="warn-line" :style="{ top: warnLineTop + 'px' }" aria-hidden="true"></span>
        <span class="warn-tag" :style="{ top: warnLineTop - 7 + 'px' }">45℃</span>
      </template>
    </div>

    <p class="diag-foot">
      <template v-if="maxTemp !== null">
        虚线为 45℃ 告警线 · 当前最高 {{ maxTemp.toFixed(1) }}℃（距告警 {{ (TEMP_WARN - maxTemp).toFixed(1) }}℃）
      </template>
      <template v-else>判定口径：高于 45℃ 视为偏高</template>
    </p>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { CircleCloseFilled, WarningFilled } from '@element-plus/icons-vue'
import LineChart from '@/components/charts/LineChart.vue'
import TimeRangeSelector from '@/components/charts/TimeRangeSelector.vue'
import { useTimeRange } from '@/composables/useTimeRange'
import { useDiagHistory } from './useDiagHistory'

const props = defineProps<{
  deviceId: number | string
  online: boolean
}>()

/** 折叠态摘要上抛给父页的折叠头（§3.2） */
const emit = defineEmits<{ (e: 'summary', text: string): void }>()

/** 图高（与 CSS 的 --plot-h 保持一致，虚线定位要用它换算） */
const PLOT_H = 160
/** 显式量程：固定后 45℃ 线的位置可计算，也让 26~30℃ 的变化不被压平 */
const Y_MIN = 20
const Y_MAX = 50
const TEMP_WARN = 45
/**
 * 45℃ 线在画布上的 y 坐标：
 *   LineChart 的 grid 未指定 top ⇒ ECharts 默认 60px（其 legend 在 top:30 占了上方）；
 *   底部 containLabel 后 X 轴时刻标签约 22px。
 * 换算：y = plotTop + (1 - (v - min)/(max - min)) * plotHeight
 * 若日后 LineChart 改了 grid，此处需同步（虚线只是近似指示，下方文字给精确结论）。
 */
const GRID_TOP = 60
const GRID_BOTTOM = 22
const warnLineTop = computed(() => {
  const plotHeight = Math.max(PLOT_H - GRID_TOP - GRID_BOTTOM, 1)
  const ratio = 1 - (TEMP_WARN - Y_MIN) / (Y_MAX - Y_MIN)
  return GRID_TOP + ratio * plotHeight
})

const { timeRange, customTimeRange, getTimeRange, handleTimeRangeChange } = useTimeRange(() => load())

const { loading, loaded, fetchRange } = useDiagHistory({
  nameMap: { temperature_1: '探头 1', temperature_2: '探头 2', temperature_3: '探头 3' },
  unitMap: { temperature_1: '℃', temperature_2: '℃', temperature_3: '℃' },
})

const series = ref<Array<{ name: string; unit?: string; data: Array<{ time: string; value: number }> }>>([])
const hasData = ref(false)
const errorMsg = ref('')

const chartSeries = computed(() => series.value)
/** 当前最高温：从已取到的点位推导（不另发请求） */
const maxTemp = computed<number | null>(() => {
  let max: number | null = null
  for (const s of series.value) {
    for (const p of s.data) {
      if (typeof p.value === 'number' && Number.isFinite(p.value)) {
        if (max === null || p.value > max) max = p.value
      }
    }
  }
  return max
})

/** 折叠头摘要：「当前 xx℃」是这一块最该被先看到的一个数字 */
const summaryText = computed(() => {
  if (errorMsg.value) return ''
  if (loading.value) return '加载中…'
  if (!hasData.value) return '暂无温度历史数据'
  return maxTemp.value === null ? '' : `当前 ${maxTemp.value.toFixed(1)}℃`
})
watch(summaryText, (t) => emit('summary', t), { immediate: true })

function onRangeChange() {
  handleTimeRangeChange()
}

async function load() {
  errorMsg.value = ''
  if (!props.online) {
    series.value = []
    hasData.value = false
    return
  }
  const [start, end] = getTimeRange()
  const { series: result, error } = await fetchRange(
    props.deviceId,
    ['temperature_1', 'temperature_2', 'temperature_3'],
    start,
    end,
  )
  if (error) {
    errorMsg.value = error
    series.value = []
    hasData.value = false
    return
  }
  // 只保留真有点位的序列：空序列画出来是无意义的空图
  const mapped = result
    .filter((s) => s.data.length > 0)
    .map((s) => ({
      name: s.name,
      unit: s.unit || '℃',
      data: s.data.map((p) => ({ time: String(p.time), value: p.value })),
    }))
  series.value = mapped
  hasData.value = mapped.length > 0
}

onMounted(load)
watch(() => [props.deviceId, props.online], load)
</script>

<style scoped>
.diag-body {
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 0;
}

.diag-toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  min-height: 28px;
  flex-wrap: wrap;
}
.diag-tip {
  font-size: 12px;
  color: var(--el-text-color-regular);
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}
.range-ctl { margin-left: auto; min-width: 0; }

/* 固定高度画布区 + 相对定位承载告警线 */
.diag-plot {
  position: relative;
  height: var(--plot-h, 160px);
  min-width: 0;
}
.state-line {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  height: 100%;
  font-size: 12px;
  line-height: 1.5;
  color: var(--el-text-color-regular);
  text-align: center;
}
.state-line.is-muted { color: var(--el-text-color-secondary); }
.state-line.is-error { color: var(--el-color-danger); align-items: flex-start; padding-top: 8px; }
.err-text { flex: 1; min-width: 0; overflow-wrap: anywhere; text-align: left; }

/* 45℃ 告警虚线：绝对定位，覆盖在折线之上但不拦事件 */
.warn-line {
  position: absolute;
  /* top 由模板内联 style 提供（warnLineTop 计算值） */
  left: 3%;
  right: 12%;
  height: 0;
  border-top: 1px dashed var(--el-color-warning);
  pointer-events: none;
  opacity: 0.75;
}
.warn-tag {
  position: absolute;
  right: 2px;
  font-size: 9px;
  line-height: 1;
  color: var(--el-color-warning);
  background: var(--card-bg);
  padding: 1px 2px;
  pointer-events: none;
  white-space: nowrap;
}

.diag-foot {
  margin: 0;
  font-size: 11px;
  line-height: 1.4;
  color: var(--el-text-color-secondary);
  overflow-wrap: anywhere;
}
</style>
