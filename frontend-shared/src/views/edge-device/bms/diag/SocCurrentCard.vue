<!--
  SOC 与电流趋势卡（diag #9）—— 生产实现。

  ── 只渲染内容 ──────────────────────────────────────────────────────────────
  外壳 / 折叠头由父页渲染。内容区高度目标：
    画布 196 + 读数条 26 + 结论句 16 + 两处 6px 间距 ≈ 250px

  ── 为什么把 SOC 和电流放在一张图里 ────────────────────────────────────────
  两个量回答的是**同一个问题**：「电是进还是出、掉得快不快」。
  SOC 在掉的同时电流为正（充电）就说明 SOC 曲线有问题；
  只看 SOC 看不出是"没充电"还是"充电但充不进去"。
  所以这里不再把两者塞进同一根 Y 轴（% 与 A 量纲差两个数量级，共轴后陡缓不可比），
  而是交给 SocCurrentChart 画成上下两个绘图区、共享同一根时间轴。

  ── 时间窗改由父页下发（契约 §C / §D）───────────────────────────────────────
  ⚠ 本卡**不再**自带 TimeRangeSelector / useTimeRange：页级时间窗由父页
    BmsDetailPage.vue 吸顶复用 TimeRangeSelector 实现（契约 §D，父页侧由另一队友落地），
    再通过 `:time-range` / `:custom-range` 传下来；本卡 watch 到变化就重新取数。
    父子契约：
      · timeRange?: string —— '1h' | '24h' | '7d' | 'custom'（缺省 24h）
      · customRange?: [string, string] | null —— 与 useTimeRange 同类型，仅 'custom' 时使用
    换算统一走 @/composables/useTimeRange 的 resolveTimeRange（与温度卡同一份实现，
    避免两张卡各写一套窗口算法后漂移）。
    为什么必须收掉卡内控件：折叠态里的小控件点不到，且同页两个时间窗会各说各话。
    父页尚未传参时本卡退回 24h（保持单独挂载/旧页面仍可用）。
-->
<template>
  <div class="diag-body">
    <!-- 固定 196px 画布区：loading/empty/error/离线 都占同高，父页高度不抖 -->
    <div class="diag-plot" :style="{ '--plot-h': PLOT_H + 'px' }">
      <div v-if="!online" class="state-line">
        <el-icon :size="14"><WarningFilled /></el-icon>
        <span>设备离线，历史数据暂不可用</span>
      </div>
      <div v-else-if="loading && !loaded" class="state-line is-muted">正在加载 SOC 与电流…</div>
      <div v-else-if="errorMsg" class="state-line is-error" role="alert">
        <el-icon :size="14"><CircleCloseFilled /></el-icon>
        <span class="err-text">SOC / 电流加载失败：{{ errorMsg }}</span>
        <el-button link size="small" type="primary" @click="load">重试</el-button>
      </div>
      <div v-else-if="!hasData" class="state-line is-muted">暂无 SOC / 电流历史数据</div>
      <SocCurrentChart
        v-else
        ref="chartComp"
        :soc="socPoints"
        :current="currentPoints"
        :height="PLOT_H + 'px'"
        :x-max-interval-ms="xMaxIntervalMs"
      />
    </div>

    <!--
      HTML 读数条（替代图内 legend）：26px 高、11px 字、非交互。
      为什么放图外：393px 宽的画布里图例会吃掉绘图区高度/宽度，
      而这两个数字本身就是用户最想一眼看到的东西，干脆做成读数条。
      色点用 CSS 变量，和两条 series 同色（SOC 主色 / 电流正绿负红）。
    -->
    <div class="diag-readout" aria-label="当前读数">
      <span v-if="latestSoc !== null" class="ro-item">
        <i class="ro-dot" :style="{ '--dot': 'var(--color-primary)' }" aria-hidden="true"></i>
        <span class="ro-label">剩余电量</span>
        <b class="ro-value">{{ latestSoc.toFixed(1) }}%</b>
      </span>
      <span v-if="latestCurrent !== null" class="ro-item">
        <i
          class="ro-dot"
          :style="{ '--dot': latestCurrent >= 0 ? 'var(--color-success)' : 'var(--color-danger)' }"
          aria-hidden="true"
        ></i>
        <span class="ro-label">电流</span>
        <b class="ro-value">{{ currentText }}</b>
      </span>
      <span v-if="latestSoc === null && latestCurrent === null" class="ro-empty">暂无读数</span>
    </div>

    <!-- 结论句：把「电流方向 + SOC 变化」读成一句人话；数据不足时给中性口径 -->
    <p class="diag-foot">{{ conclusion }}</p>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { CircleCloseFilled, WarningFilled } from '@element-plus/icons-vue'
import { resolveTimeRange } from '@/composables/useTimeRange'
import SocCurrentChart from './SocCurrentChart.vue'
import { useDiagHistory } from './useDiagHistory'

const props = defineProps<{
  deviceId: number | string
  online: boolean
  /**
   * 时间窗上移到页面级（2026-11-12）：两张趋势卡共享同一个窗口。
   * 声明成可选是为了「父页尚未接入」的过渡期仍能编译/运行；接入后父页必须传。
   * 类型与 useTimeRange 保持一致（customRange 是 [string, string]，不是 Date）。
   */
  timeRange?: string
  customRange?: [string, string] | null
}>()

/** 当前时间档位（缺省 24h，与 useTimeRange 的默认值一致） */
const rangeMode = computed(() => props.timeRange || '24h')

/** 折叠态摘要上抛给父页的折叠头（§3.2）—— 契约不变 */
const emit = defineEmits<{ (e: 'summary', text: string): void }>()

/** 画布高度（与 SocCurrentChart 的两区几何 96 / 16 / 44 配套） */
const PLOT_H = 196

/** X 轴刻度间隔随窗口自适应（1h→15min，24h→6h，7d→1 天）：7d 固定 6h 会给出 ~29 个刻度 */
const xMaxIntervalMs = computed(() => {
  switch (rangeMode.value) {
    case '1h': return 15 * 60 * 1000
    case '7d': return 24 * 3600 * 1000
    default: return 6 * 3600 * 1000
  }
})

interface RectLike { x: number; y: number; width: number; height: number }
/** 图表 exposed 句柄（门禁/探针通过 getCharts() 读到它，再量 grid rect） */
interface ChartHandle {
  getChartInstance: () => unknown
  getPlotRect: () => RectLike | null
  getGridRects: () => { soc: RectLike | null; current: RectLike | null }
  getLayoutMode: () => { compact: boolean; bleed: boolean }
}
const chartComp = ref<ChartHandle | null>(null)
/** 判「在充电/在放电」的最小电流：噪声（±0.1A 级）不算方向 */
const CHARGE_EPS = 0.15

const { loading, loaded, fetchRange } = useDiagHistory({
  nameMap: { rsoc: '剩余电量', current: '电流' },
  unitMap: { rsoc: '%', current: 'A' },
})

interface ChartPoint {
  time: string
  value: number
}

const socPoints = ref<ChartPoint[]>([])
const currentPoints = ref<ChartPoint[]>([])
const hasData = ref(false)
const errorMsg = ref('')

/** 竞态号：切时间窗时旧请求回来得晚，不能把上一窗口的数据盖到新窗口上 */
let loadSeq = 0

const sortPoints = (points: Array<{ time: string; value: number }> | undefined): ChartPoint[] => {
  const out: ChartPoint[] = []
  for (const point of points ?? []) {
    if (!point) continue
    const value = Number(point.value)
    if (!Number.isFinite(value)) continue
    out.push({ time: String(point.time), value })
  }
  return out.sort((a, b) => new Date(a.time).getTime() - new Date(b.time).getTime())
}

const lastValue = (points: ChartPoint[]): number | null => {
  for (let i = points.length - 1; i >= 0; i -= 1) {
    const value = points[i].value
    if (Number.isFinite(value)) return value
  }
  return null
}

const latestSoc = computed(() => lastValue(socPoints.value))
const latestCurrent = computed(() => lastValue(currentPoints.value))

/** 电流读数口径：带符号两位小数（−5.39A / +2.60A）—— 符号本身就是「电在进还是在出」 */
const currentText = computed(() => {
  const value = latestCurrent.value
  if (value === null) return ''
  return `${value < 0 ? '\u2212' : '+'}${Math.abs(value).toFixed(2)}A`
})

/** 充/放统计：正负采样占比 + SOC 净变化（都从已取到的点位推导，不另发请求） */
const flowStats = computed<{ positiveRatio: number; negativeRatio: number; socDelta: number } | null>(() => {
  const currentList = currentPoints.value
  const socList = socPoints.value
  // 少于这些点说明这段时间基本没上报，任何「充电/放电」结论都是编的
  if (currentList.length < 4 || socList.length < 2) return null
  let positive = 0
  let negative = 0
  for (const point of currentList) {
    if (point.value > CHARGE_EPS) positive += 1
    else if (point.value < -CHARGE_EPS) negative += 1
  }
  const samples = positive + negative
  if (samples === 0) return null
  const socDelta = socList[socList.length - 1].value - socList[0].value
  return {
    positiveRatio: positive / samples,
    negativeRatio: negative / samples,
    socDelta,
  }
})

/**
 * 结论句（契约 §C：如「充电段电流为正时 SOC 回升 12%」，数据不足给中性文案）。
 *
 * 只做**可被数据推翻**的陈述：
 *   · 以充电为主 且 SOC 升 → 正常充电；
 *   · 以充电为主 却 SOC 降 → 明确说「不符」（这是这张卡最该报警的情形）；
 *   · 单侧样本不足 → 不判定，只给口径。
 */
const conclusion = computed(() => {
  if (!props.online) return '设备离线：无法判断电流方向与 SOC 变化的关系'
  if (errorMsg.value) return '加载失败：暂时无法判断充放电与 SOC 的关系'
  if (loading.value && !loaded.value) return '正在读取 SOC 与电流…'
  if (!hasData.value) return '暂无数据：无法判断充放电与 SOC 的关系'
  const stats = flowStats.value
  if (!stats) return '数据点不足，暂不判定：电流为正=充电、为负=放电'
  const magnitude = Math.abs(stats.socDelta).toFixed(1)
  const changed = Math.abs(stats.socDelta) >= 0.5
  if (stats.positiveRatio >= 0.6) {
    if (changed && stats.socDelta > 0) return `充电段电流为正时 SOC 回升 ${magnitude}%`
    if (changed) return `充电段电流为正，SOC 却下降 ${magnitude}%（采样可能滞后）`
    return '充电段电流为正，SOC 基本持平'
  }
  if (stats.negativeRatio >= 0.6) {
    if (changed && stats.socDelta < 0) return `放电段电流为负时 SOC 下降 ${magnitude}%`
    if (changed) return `放电段电流为负，SOC 却回升 ${magnitude}%（可能有外部补电）`
    return '放电段电流为负，SOC 基本持平'
  }
  const percent = Math.round(stats.positiveRatio * 100)
  const net = !changed ? '基本持平' : stats.socDelta > 0 ? `净升 ${magnitude}%` : `净降 ${magnitude}%`
  return `充放电交替（正电流占 ${percent}%），SOC ${net}`
})

/** 折叠头摘要：「SOC 7.0%」—— 电量是这一块最该一眼看到的值（口径与改前一致） */
const summaryText = computed(() => {
  if (errorMsg.value) return ''
  if (loading.value) return '加载中…'
  if (!hasData.value) return '暂无 SOC 数据'
  return latestSoc.value === null ? '' : `SOC ${latestSoc.value.toFixed(1)}%`
})
watch(summaryText, (text) => emit('summary', text), { immediate: true })

async function load() {
  const seq = ++loadSeq
  errorMsg.value = ''
  if (!props.online) {
    socPoints.value = []
    currentPoints.value = []
    hasData.value = false
    return
  }
  // 窗口换算与温度卡同源（useTimeRange.resolveTimeRange）；缺省/非法输入由它退回 24h
  const [start, end] = resolveTimeRange(rangeMode.value, props.customRange ?? null)
  const { series: result, error } = await fetchRange(props.deviceId, ['rsoc', 'current'], start, end)
  if (seq !== loadSeq) return
  if (error) {
    errorMsg.value = error
    socPoints.value = []
    currentPoints.value = []
    hasData.value = false
    return
  }
  const socHit = result.find((s) => s.category === 'rsoc')
  const currentHit = result.find((s) => s.category === 'current')
  socPoints.value = sortPoints(socHit?.data)
  currentPoints.value = sortPoints(currentHit?.data)
  hasData.value = socPoints.value.length > 0 || currentPoints.value.length > 0
}

onMounted(load)

// 时间窗 / 自定义区间 / 设备 / 在线态任一变化 → 重新取数（时间窗不再由卡内控件驱动）
watch(() => [props.deviceId, props.online, props.timeRange, props.customRange], load)

/**
 * 门禁/探针入口：DOM 里的 .soc-current-chart 是 ECharts 画布宿主（由子组件渲染），
 * 这里再把子组件的 exposed 句柄交出去，门禁就不必依赖 __vueParentComponent 的内部字段。
 * 图表未渲染（空/错/离线/加载态）时返回空数组。
 */
defineExpose({
  getCharts: (): ChartHandle[] => (chartComp.value ? [chartComp.value] : []),
})
</script>

<style scoped>
.diag-body {
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 0;
}

/*
  仅窄屏的「全宽出血」：BmsDetailPage 在 ≤768px 把 .diag-card 变成全宽条（左右内距 0），
  于是文字块（读数条 / 结论句）各自补 12px，画布用负 margin 抵消这 12px 贴到视口边缘。
  桌面（>768px）卡片仍有 20px 内距，这段不生效 —— 桌面像素基线不变。
*/
@media (max-width: 768px) {
  .diag-body { padding: 0 var(--diag-text-pad, 12px); }
  .diag-plot { margin: 0 calc(-1 * var(--diag-text-pad, 12px)); }
}

/* 固定高度画布区：三态占位与图表同高（196px） */
.diag-plot {
  position: relative;
  height: var(--plot-h, 196px);
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

/*
  读数条：26px 高 / 11px 字 / 非交互。
  pointer-events:none 是刻意的 —— 它只是读出，不该抢走画布上的 hover（tooltip）。
*/
.diag-readout {
  display: flex;
  align-items: center;
  gap: 14px;
  height: 26px;
  min-width: 0;
  font-size: 11px;
  line-height: 1;
  color: var(--el-text-color-regular);
  white-space: nowrap;
  overflow: hidden;
  pointer-events: none;
  user-select: none;
}
.ro-item {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  min-width: 0;
}
.ro-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  /* --dot 由模板内联给出（SOC 主色 / 电流正绿负红），都是 CSS 变量 */
  background: var(--dot, var(--color-primary));
  flex: 0 0 auto;
}
.ro-label { color: var(--el-text-color-secondary); }
.ro-value {
  font-weight: 600;
  color: var(--el-text-color-primary);
  font-variant-numeric: tabular-nums;
}
.ro-empty { color: var(--el-text-color-secondary); }

.diag-foot {
  margin: 0;
  font-size: 11px;
  line-height: 1.4;
  color: var(--el-text-color-secondary);
  overflow-wrap: anywhere;
}
</style>
