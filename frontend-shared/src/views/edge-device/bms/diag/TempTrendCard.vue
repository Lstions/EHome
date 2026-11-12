<template>
  <div class="diag-body">
    <!-- 固定画布区：loading/empty/error/离线 都占同高，父页高度不抖（H5 契约） -->
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
        <!--
          全宽出血档（2026-11-12）：
            · legend-position="none" —— 图例移出画布，改由下方 HTML 读数条承担（同时是当前值）；
            · density="compact" —— 不再依赖「高度 ≤220 自动判定」（那条判据历史上被 Vue 的
              Boolean 铸型短路，导致 160px 小图跑桌面几何）；
            · mark-line —— 45℃ 告警线由 ECharts 画，位置永远与刻度对齐（原来是 CSS 绝对定位，
              按 gridTop=60 / bottom=22 换算，实测偏高 ≈9px）。
        -->
        <LineChart
          :series="chartSeries"
          :y-axis-min="Y_MIN"
          :y-axis-max="Y_MAX"
          :show-area="false"
          :height="PLOT_H + 'px'"
          density="compact"
          legend-position="none"
          :y-tick-count="3"
          :x-max-interval-ms="tickIntervalMs"
          :mark-line="warnMarkLine"
        />
      </template>
    </div>

    <!-- 读数条：色点 + 探头名 + 当前值（26px，非交互 —— 不占用 44px 触摸目标预算） -->
    <div v-if="hasData && !errorMsg && online" class="chips" aria-label="各探头当前温度">
      <span v-for="(s, i) in series" :key="s.name" class="chip">
        <i class="chip-dot" :style="{ background: seriesColor(i) }"></i>
        <span class="chip-name">{{ s.name }}</span>
        <b class="chip-value">{{ lastValue(s) }}</b>
      </span>
    </div>

    <p class="diag-foot">
      <template v-if="maxTemp !== null">
        最高 {{ maxTemp.toFixed(1) }}℃（{{ hottestName }}），距 <b>45℃</b> 告警 {{ (TEMP_WARN - maxTemp).toFixed(1) }}℃
        <template v-if="series.length > 1">· 三路同步升降＝环境温度，单路独升＝该测点异常</template>
      </template>
      <template v-else>判定口径：高于 45℃ 视为偏高</template>
    </p>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { CircleCloseFilled, WarningFilled } from '@element-plus/icons-vue'
import LineChart from '@/components/charts/LineChart.vue'
import { resolveTimeRange } from '@/composables/useTimeRange'
import { useDiagHistory } from './useDiagHistory'

const props = defineProps<{
  deviceId: number | string
  online: boolean
  /**
   * 时间窗上移到页面级（2026-11-12）：两张趋势卡共享同一个窗口。
   * 类型与 useTimeRange 保持一致（customRange 是 [string, string]）。
   */
  timeRange?: string
  customRange?: [string, string] | null
}>()

/** 折叠态摘要上抛给父页的折叠头（§3.2） */
const emit = defineEmits<{ (e: 'summary', text: string): void }>()

/** 画布高（px）：全宽出血后这里只给「4 条网格 + 一行时刻」留位置 */
const PLOT_H = 180
/** 显式量程：固定后 45℃ 告警线的位置由 ECharts 自己算，也让 26~31℃ 的变化不被压平 */
const Y_MIN = 20
const Y_MAX = 50
const TEMP_WARN = 45

const rangeMode = computed(() => props.timeRange || '24h')
/** 24h → 6h 一个刻度；7d → 1 天；1h → 15min（轴级 maxInterval，单位毫秒） */
const tickIntervalMs = computed(() => {
  switch (rangeMode.value) {
    case '1h': return 15 * 60 * 1000
    case '7d': return 24 * 3600 * 1000
    default: return 6 * 3600 * 1000
  }
})

/**
 * Canvas 里不能直接用 CSS 变量（ECharts 把颜色写进 canvas 的 fillStyle，
 * 浏览器不会解析 var(...)）—— 必须用 getComputedStyle 取真实色值。
 * 主题切换时重取（H6：画布颜色必须跟着主题走）。
 */
const warnColor = ref('#e6a23c')
const readThemeColor = () => {
  if (typeof window === 'undefined') return
  const v = getComputedStyle(document.documentElement).getPropertyValue('--color-warning').trim()
  warnColor.value = v || getComputedStyle(document.documentElement).getPropertyValue('--el-color-warning').trim() || '#e6a23c'
}

/** 告警线：颜色取主题色，暗/亮主题都跟着走 */
const warnMarkLine = computed(() => ({
  silent: true,
  symbol: 'none',
  lineStyle: { color: warnColor.value, type: 'dashed', width: 1, opacity: 0.85 },
  label: {
    position: 'insideEndTop',
    formatter: '45℃ 告警',
    fontSize: 10,
    color: warnColor.value,
  },
  data: [{ yAxis: TEMP_WARN }],
}))

/** 读数条色点必须与 LineChart 的调色板同序（primary / danger / warning），否则「色点对不上线」 */
const SERIES_COLORS = ['#409eff', '#f56c6c', '#e6a23c']
const seriesColor = (i: number): string => {
  const names = ['--color-primary', '--color-danger', '--color-warning']
  if (typeof window === 'undefined') return SERIES_COLORS[i % 3]
  const v = getComputedStyle(document.documentElement).getPropertyValue(names[i % 3]).trim()
  return v || SERIES_COLORS[i % 3]
}

let themeObserver: MutationObserver | null = null
onMounted(() => {
  readThemeColor()
  themeObserver = new MutationObserver(readThemeColor)
  themeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['class', 'data-theme'] })
})
onUnmounted(() => {
  themeObserver?.disconnect()
  themeObserver = null
})

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

/** 最高温所在探头（结论句要说清「哪一路」，用户才知道是不是单点异常） */
const hottestName = computed(() => {
  let name = ''
  let max = -Infinity
  for (const s of series.value) {
    const last = s.data.length ? s.data[s.data.length - 1].value : -Infinity
    if (typeof last === 'number' && last > max) { max = last; name = s.name }
  }
  return name
})

const lastValue = (s: { data: Array<{ value: number }> }) => {
  const last = s.data.length ? s.data[s.data.length - 1].value : null
  return typeof last === 'number' ? `${last.toFixed(1)}℃` : '—'
}

/** 折叠头摘要：「当前 xx℃」是这一块最该被先看到的一个数字 */
const summaryText = computed(() => {
  if (errorMsg.value) return ''
  if (loading.value) return '加载中…'
  if (!hasData.value) return '暂无温度历史数据'
  return maxTemp.value === null ? '' : `当前 ${maxTemp.value.toFixed(1)}℃`
})
watch(summaryText, (t) => emit('summary', t), { immediate: true })

async function load() {
  errorMsg.value = ''
  if (!props.online) {
    series.value = []
    hasData.value = false
    return
  }
  const [start, end] = resolveTimeRange(rangeMode.value, props.customRange ?? null)
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
watch(() => [props.deviceId, props.online, props.timeRange, props.customRange], load)
</script>

<style scoped>
.diag-body {
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 0;
}
.diag-plot {
  position: relative;
  height: var(--plot-h, 180px);
  min-width: 0;
}

/*
  仅窄屏的「全宽出血」：BmsDetailPage 把 .diag-card 变成全宽条（左右内距 0），
  于是卡片内的文字块要自己补回 12px，画布则用负 margin 抵消这 12px 贴到视口边缘。
  桌面（>768px）卡片仍有 20px 内距，这段规则不生效 —— 桌面像素基线不变。
*/
@media (max-width: 768px) {
  .diag-body { padding: 0 var(--diag-text-pad, 12px); }
  .diag-plot { margin: 0 calc(-1 * var(--diag-text-pad, 12px)); }
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

/* 读数条：图例 + 当前值合体，26px（低于 10px 字号是硬下限，这里 11px） */
.chips { display: flex; flex-wrap: wrap; gap: 5px; margin-top: 2px; }
.chip {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  min-height: 26px;
  padding: 0 8px;
  border: 1px solid var(--border-color, var(--el-border-color));
  border-radius: 13px;
  background: var(--bg-color-overlay, var(--el-bg-color-overlay));
  font-size: 11px;
  line-height: 1;
  color: var(--text-color-regular, var(--el-text-color-regular));
  white-space: nowrap;
}
.chip-dot { width: 8px; height: 8px; border-radius: 50%; flex: none; }
.chip-value {
  color: var(--text-color-primary, var(--el-text-color-primary));
  font-weight: 600;
  font-size: 12px;
  font-variant-numeric: tabular-nums;
}

.diag-foot {
  margin: 0;
  font-size: 11px;
  line-height: 1.45;
  color: var(--el-text-color-secondary);
  overflow-wrap: anywhere;
}
</style>
