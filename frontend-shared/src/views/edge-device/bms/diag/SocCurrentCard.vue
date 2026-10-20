<!--
  SOC 与电流趋势卡（diag #9）—— 生产实现。

  ── 只渲染内容 ──────────────────────────────────────────────────────────────
  外壳 / 折叠头由父页渲染。内容区高度目标 ≤189px：
    工具行 28 + 折线 150 + 间距 6 ≈ 184px

  ── 为什么把 SOC 和电流放在一张图里 ────────────────────────────────────────
  两个量回答的是**同一个问题**：「电是进还是出、掉得快不快」。
  SOC 在掉的同时电流为正（充电）就说明 SOC 曲线有问题；
  只看 SOC 看不出是"没充电"还是"充电但充不进去"。

  ── 量级/量纲处理（关键）───────────────────────────────────────────────────
  SOC 是 %（0~100），电流是 A（-5~5）。LineChart 内部的 buildYAxisList 只在
  「单位相同 **且** 量级比 <10」时才共轴，单位不同会自动分左右轴。
  这里显式给每条序列带 unit（'%' / 'A'），因此：
    · SOC 走左轴（%），电流走右轴（A）
    · 不会出现"把 7% 和 2.6A 画在同一根轴上比高低"的假信息
-->
<template>
  <div class="diag-body">
    <div class="diag-toolbar">
      <span class="diag-tip" v-if="latestSoc !== null">SOC {{ latestSoc.toFixed(1) }}%</span>
      <span class="diag-tip is-charge" v-if="latestCurrent !== null">
        {{ latestCurrent >= 0 ? '充电' : '放电' }} {{ Math.abs(latestCurrent).toFixed(2) }} A
      </span>
      <TimeRangeSelector
        v-model="timeRange"
        v-model:custom-range="customTimeRange"
        class="range-ctl"
        @change="onRangeChange"
      />
    </div>

    <div class="diag-plot" :style="{ height: PLOT_H + 'px' }">
      <div v-if="!online" class="state-line">
        <el-icon :size="14"><WarningFilled /></el-icon>
        <span>设备离线，历史数据暂不可用</span>
      </div>
      <div v-else-if="loading && !loaded" class="state-line is-muted">正在加载 SOC 与电流…</div>
      <div v-else-if="errorMsg" class="state-line is-error">
        <el-icon :size="14"><CircleCloseFilled /></el-icon>
        <span class="err-text">{{ errorMsg }}</span>
        <el-button link size="small" type="primary" @click="load">重试</el-button>
      </div>
      <div v-else-if="!hasData" class="state-line is-muted">暂无 SOC / 电流历史数据</div>
      <LineChart
        v-else
        :series="chartSeries"
        :show-area="false"
        height="150px"
      />
    </div>
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

const PLOT_H = 150

const { timeRange, customTimeRange, getTimeRange, handleTimeRangeChange } = useTimeRange(() => load())

const { loading, loaded, fetchRange } = useDiagHistory({
  nameMap: { rsoc: '剩余电量', current: '电流' },
  unitMap: { rsoc: '%', current: 'A' },
})

interface ChartSeriesItem {
  name: string
  unit?: string
  data: Array<{ time: string; value: number }>
}

const series = ref<ChartSeriesItem[]>([])
const hasData = ref(false)
const errorMsg = ref('')

const chartSeries = computed(() => series.value)

function lastValueOf(name: string): number | null {
  const hit = series.value.find((s) => s.name === name)
  if (!hit || hit.data.length === 0) return null
  const last = hit.data[hit.data.length - 1]
  return Number.isFinite(last.value) ? last.value : null
}
const latestSoc = computed(() => lastValueOf('剩余电量'))
const latestCurrent = computed(() => lastValueOf('电流'))

/** 折叠头摘要：「SOC 7.0%」—— 电量是这一块最该一眼看到的值 */
const summaryText = computed(() => {
  if (errorMsg.value) return ''
  if (!hasData.value) return '暂无 SOC 数据'
  return latestSoc.value === null ? '' : `SOC ${latestSoc.value.toFixed(1)}%`
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
  const { series: result, error } = await fetchRange(props.deviceId, ['rsoc', 'current'], start, end)
  if (error) {
    errorMsg.value = error
    series.value = []
    hasData.value = false
    return
  }
  const mapped = result
    .filter((s) => s.data.length > 0)
    .map((s) => ({
      name: s.name,
      // 单位必须带上：LineChart 靠它决定是否分轴（'%' vs 'A' 不同 ⇒ 左右双轴）
      unit: s.unit || (s.category === 'rsoc' ? '%' : s.category === 'current' ? 'A' : ''),
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
.diag-tip.is-charge { color: var(--el-color-success); }
.range-ctl { margin-left: auto; min-width: 0; }

.diag-plot {
  position: relative;
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
</style>
