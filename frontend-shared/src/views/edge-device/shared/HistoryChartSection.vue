<template>
  <el-card style="margin-top: 20px;" shadow="hover">
    <template #header>
      <!-- hist-header 仅承载窄屏 @media 规则（桌面无任何基础样式 ⇒ 1440 像素不变） -->
      <div class="hist-header">
        <span class="hist-title">历史数据</span>
        <div class="header-controls">
          <TimeRangeSelector
            v-model="timeRange"
            v-model:custom-range="customTimeRange"
            @change="onTimeRangeChange"
          />
          <el-tooltip content="导出 CSV" placement="top">
            <el-button
              type="primary"
              size="small"
              class="hist-export"
              aria-label="导出 CSV"
              title="导出 CSV"
              @click="handleExportCSV"
              :disabled="historyData.length === 0 && chartSeries.length === 0"
            >
              <el-icon :size="16"><Download /></el-icon>
            </el-button>
          </el-tooltip>
        </div>
      </div>
    </template>

    <el-skeleton v-if="chartLoading" :rows="isMobileViewport ? 3 : 8" animated />
    <template v-else>
      <!--
        2026-10-20 修（实测：BMS「运行趋势」卡在**全部子图无数据**时仍高达 831px）：
        旧模板对每个分组都渲染一个 height=260px 的 LineChart，即便该组 series 为空；
        5 个空组 = 1300px 的空白图表区，把页面拉得极长且看不出哪里有问题。
        现在：只在**该组确实有数据点**时才渲染图表，否则渲染一行紧凑的空态文案。
      -->
      <!--
        2026-10-20 再修（393 生产实测：本卡 2441px / 内部 7 个 277×260 画布 / 图例翻页「◀ 1/5 ▶」/
        X 轴刻度挤成乱码 / 轴名「(V)(Hz)(W)」压图例）：
        窄屏（≤768px）改「**单图 + 分组下拉**」—— 下拉选分组、只渲染选中组的 1 张图，
        图高 150px ⇒ 自动进入 LineChart 的 isCompactChart 分支（不画轴名、图例贴顶、axisLabel.hideOverlap），
        正好治上述三个症状；分组切换只换渲染（数据已在 chartSeries 内存里），**不重新发请求**。
        桌面（>768px）保持 7 子图平铺、height=260px，结构与改前完全一致（逐像素契约）。
      -->
      <!--
        2026-10-21 用户要求：「逆变器图表下拉框改为 tab 点击切换图表显示，交互效率更高」。
        改前：桌面把 7 个分组**纵向平铺**（7 × 260px ≈ 1900px，要滚三屏；也正是
        「图表长度参差不齐」的根源 —— 同屏对比时绘图区必须逐像素相等）；
        窄屏用一个 el-select 下拉（换组要两步：展开 + 选择）。
        改后：桌面与窄屏**统一**为「一行分组 tab + 单张图」——
          · 一次点击切换分组，全部 7 个分组始终可见（窄屏折成两行，不做横向滚动隐藏）；
          · 同屏只渲染 1 个 echarts 实例（不再为 7 张图各建实例，窄屏省掉 6 份绘制成本）；
          · 切换只改 activeGroupTitle，**不重新发请求**（数据已在 chartSeries 内存里）。
      -->
      <div
        v-if="chartSubGroups.length > 0"
        class="hist-tabbed"
        :data-group="activeGroupTitle"
        :data-series-count="mobileSeriesCount"
        :data-point-count="mobilePointCount"
        :data-compact="String(isMobileViewport)"
        :data-chart-height="chartHeightPx"
      >
        <div class="hist-tabs" role="tablist" aria-label="切换历史数据分组">
          <button
            v-for="g in chartSubGroups"
            :key="g.title"
            type="button"
            role="tab"
            class="hist-tab"
            :class="{ 'is-active': g.title === activeGroupTitle }"
            :aria-selected="g.title === activeGroupTitle"
            @click="onGroupSelect(g.title)"
          >{{ g.title }}</button>
        </div>
        <div class="hist-chart">
          <LineChart
            v-if="activeGroup && groupHasData(activeGroup)"
            :series="activeGroup.series"
            :title="''"
            :height="chartHeightPx"
            :yAxisMin="activeGroup.yAxisMin"
            :yAxisMax="activeGroup.yAxisMax"
            :reserve-right-axes="maxReserveRightAxes"
            :compact="isMobileViewport"
          />
          <p v-else class="chart-sub-empty">该分组在当前时间范围内无数据</p>
        </div>
      </div>
      <LineChart
        v-else-if="historyData.length > 0"
        :data="historyData"
        :title="`${deviceTypeText}数据趋势`"
        height="400px"
      />
      <!--
        空态必须说明"查了什么范围"，否则用户无法判断是设备没上报、还是时间范围选错。
        2026-10-20 实测：逆变器历史卡曾长期只显示"暂无历史数据"，而库里该设备
        24h 内有 2000+ 个数据点 —— 根因是设备类型判定漏了厂商前缀（已修），
        但空态本身不给任何线索，让排查方向完全错误。这里补上范围与可能原因。
      -->
      <el-empty v-else :image-size="60">
        <template #description>
          <div class="empty-desc">
            <p class="empty-title">当前时间范围内暂无历史数据</p>
            <p class="empty-hint">已查询：{{ timeRangeLabel }}（{{ queryScopeHint }}）</p>
            <!-- 离线优先：设备离线时「换时间范围」是无效排查方向，必须先把原因说准。 -->
            <p v-if="props.online === 'offline'" class="empty-hint is-offline">
              设备当前离线，历史数据需由设备上报后才有 —— 请先确认节点与通道在线，恢复后数据会自动补齐。
            </p>
            <p v-else class="empty-hint">可能原因：设备刚接入尚未上报、该类型数据未被采集，或所选时间范围无数据。</p>
          </div>
        </template>
      </el-empty>
    </template>
  </el-card>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { Download } from '@element-plus/icons-vue'
import LineChart from '@/components/charts/LineChart.vue'
import TimeRangeSelector from '@/components/charts/TimeRangeSelector.vue'
import { groupHasData } from './historyChartGroups'
import { useHistoryChartData } from './useHistoryChartData'
import { useActiveChartGroup } from './useActiveChartGroup'

const props = defineProps<{
  deviceId: number
  deviceType: string
  deviceTypeText: string
  /**
   * 设备在线状态：离线设备的「暂无历史数据」根本原因通常不是时间范围，
   * 而是设备没上报。2026-10-21 独立复核指出空态文案未区分离线，
   * 会把用户引向「换时间范围」这种无效排查。
   *
   * 用三态字符串而不是 boolean：Vue 对 Boolean 类型 prop 在缺省时会 cast 成 false，
   * 直传 boolean 时「未知」与「离线」无法区分（实测未传 online 也走进离线分支，
   * 让 14 个既有空态测试全部误红）。
   */
  online?: 'online' | 'offline' | 'unknown'
}>()

// 取数 / 时间窗 / CSV 下沉到组合式（边界见 useHistoryChartData.ts 文件头）。
const {
  chartLoading, historyData, chartSeries, timeRange, customTimeRange,
  timeRangeLabel, queryScopeHint, fetchHistoryData, handleExportCSV, onTimeRangeChange,
} = useHistoryChartData(props)

// 分组投影与 tab 选中态下沉到组合式（边界见 useActiveChartGroup.ts 文件头）。
const {
  chartSubGroups, activeGroupTitle, activeGroup, onGroupSelect, mobileSeriesCount, mobilePointCount,
} = useActiveChartGroup(props, chartSeries)

/* ---- 视口判定（≤768px 走窄屏「单图 + 分组下拉」）----
 * 为什么用 JS 判定而不是纯 CSS：窄屏要的是**结构**变化（7 张图 → 1 张图 + 下拉）。
 * CSS 只能隐藏不能减少节点；被 display:none 藏起来的 6 张图仍会创建 echarts 实例、
 * 仍在读屏与 Tab 顺序里，属于可访问性与性能双重缺陷。
 * 无 window.matchMedia 的环境（SSR / 部分测试）按桌面处理，保证既有桌面契约稳定。 */
const MOBILE_MAX = 768
const isMobileViewport = ref(
  typeof window !== 'undefined' && typeof window.matchMedia === 'function'
    ? window.matchMedia(`(max-width: ${MOBILE_MAX}px)`).matches
    : false,
)
let mobileMql: MediaQueryList | null = null
const onMobileChange = (e: MediaQueryListEvent) => { isMobileViewport.value = e.matches }

/**
 * 单图高度（px 字符串，直接传给 LineChart 的 height）。
 *
 * 2026-10-22（第七轮真机反馈「按钮大小比例与图表相比大太多了」）：
 *   · 桌面 320px：不变（平铺时代每张 260px，现在同屏只有一张）；
 *   · 窄屏 200 → 320px：窄屏控件块（卡头 + 分组 tab）最少约 166px，图 200px 时
 *     控件块/图高 = 0.90~1.20；图高 320 后降到 0.52 以内（验收要求 ≤0.55）。
 *   · 高度 >220 已超出 LineChart 的「按高度判定紧凑图」阈值，故窄屏**显式**传
 *     :compact="isMobileViewport" —— 紧凑渲染（不画轴名/图例贴顶/hideOverlap/少轴）
 *     与像素高度是两件事，必须能分别指定；桌面不传 compact，行为与改前逐像素一致。
 */
const CHART_HEIGHT_PX = 320
const chartHeightPx = computed(() => `${CHART_HEIGHT_PX}px`)

/**
 * 当前分组需要预留的右轴数（2026-10-21，随 tab 化改造调整）。
 *
 * 历史：平铺 7 张图时这里取「所有组里最大的右轴数」，让每组预留同样右边距 ——
 * 目的是让同屏各图 plot 宽严格相等（治「图表长度参差不齐」）。
 * 现在 tab 一次只显示**一张**图，跨图对齐不再有意义；改为只按当前分组自己的
 * 不同单位数估算（与 LineChart 的分轴规则同口径），简单分组（如电网 2 个量）
 * 不再白白空出两根右轴的位置。
 *
 * 上限与 LineChart 的 MAX_Y_AXIS=4 对齐（4 根轴 ⇒ 最多 3 根右轴）。
 * 变量名保持 maxReserveRightAxes 不变（既有 ChartPlotAlignment 契约按此匹配）。
 */
const LINE_CHART_MAX_Y_AXIS = 4
const maxReserveRightAxes = computed(() => {
  const group = activeGroup.value
  if (!group) return 0
  const units = new Set(group.series.map(s => s.unit || ''))
  return Math.max(0, Math.min(units.size, LINE_CHART_MAX_Y_AXIS) - 1)
})

defineExpose({ fetchHistoryData })

onMounted(() => {
  // 监听视口变化：横竖屏切换 / 桌面窗口缩放时在「7 子图平铺」与「单图 + 分组下拉」之间切换
  if (typeof window !== 'undefined' && typeof window.matchMedia === 'function') {
    mobileMql = window.matchMedia(`(max-width: ${MOBILE_MAX}px)`)
    mobileMql.addEventListener('change', onMobileChange)
    isMobileViewport.value = mobileMql.matches
  }
  void fetchHistoryData()
})

onUnmounted(() => {
  // 必须解绑：不解绑会在路由切换后留下指向已销毁组件的监听（内存泄漏）
  mobileMql?.removeEventListener('change', onMobileChange)
  mobileMql = null
})
</script>

<style scoped>
/* 空态说明：给出「查了什么范围 + 可能原因」，而不是只说「暂无」 */
.chart-sub-empty {
  margin: 0;
  padding: 18px 0;
  text-align: center;
  font-size: 12px;
  color: var(--el-text-color-placeholder);
  background: var(--el-fill-color-lighter);
  border-radius: 6px;
}
.empty-desc { text-align: center; line-height: 1.7; }
.empty-title { margin: 0; font-size: 14px; color: var(--el-text-color-regular); }
.empty-hint { margin: 4px 0 0; font-size: 12px; color: var(--el-text-color-secondary); }
/* 空态说明：给出「查了什么范围 + 可能原因」，而不是只说「暂无」 */
.chart-sub-empty {
  margin: 0;
  padding: 18px 0;
  text-align: center;
  font-size: 12px;
  color: var(--el-text-color-placeholder);
  background: var(--el-fill-color-lighter);
  border-radius: 6px;
}
.empty-desc { text-align: center; line-height: 1.7; }
.empty-title { margin: 0; font-size: 14px; color: var(--el-text-color-regular); }
.empty-hint { margin: 4px 0 0; font-size: 12px; color: var(--el-text-color-secondary); }

/* 卡头：标题左、控件右；放不下时换行（控件尺寸不跟着缩，避免「按钮忽大忽小」） */
.hist-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px 12px;
  flex-wrap: wrap;
}
.hist-title { font-size: 16px; font-weight: 500; color: var(--el-text-color-primary); }
.header-controls { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }

/*
  图表卡工具条档（规范 §4.4「控件尺寸档位」）：
  导出图标按钮与时间档分段、分组 tab 共用 theme.css 的 --control-* token，
  桌面 32x32 / ≤768 或 pointer:coarse 36x36，圆角 6px，内距 0（方形图标按钮宽度=高度）。
  图标按钮必须有三通道名称（aria-label + title + el-tooltip），否则纯图标对读屏/悬停用户不可达。
  ⚠ 用 size="small"：桌面档要 32px，而 theme.css 粗指针块会把「非 small」普通按钮抬到 44px；
    small 变体只被窄屏 36px 档约束（与本档一致），桌面由下面的 scoped 规则钉到 32px。
*/
.hist-export {
  width: var(--control-h-toolbar, 32px);
  height: var(--control-h-toolbar, 32px);
  min-width: var(--control-h-toolbar, 32px);
  min-height: var(--control-h-toolbar, 32px);
  box-sizing: border-box;
  padding: 0;
  border-radius: var(--control-radius-toolbar, 6px);
  /*
    字号也要钉在同一 token 上：App.vue 有一条「≤480px 时 .el-button{font-size:15px}」的
    全局移动端缩放规则（0,1,0），会把工具条按钮从 12px 顶回 15px —— 于是又把
    「同一行控件一档字号」破坏在 320~480px（第七轮独立验证 D6 发现）。
    本规则是 scoped（0,2,0），显式压回工具条档，不依赖全局规则先后顺序。
  */
  font-size: var(--control-font-toolbar, 12px);
}

/*
  分组 tab（2026-10-21 新增，替代「桌面 7 图平铺 / 窄屏 el-select」）。
  为什么不用 el-tabs：7 个分组在 769~900px 的卡片里会溢出，el-tabs 溢出时用
  「◀ ▶ 翻页器」把选项藏起来 —— 等于默认隐藏分组；这里是 flex-wrap，**永远全部可见**。
  交互：role=tablist/tab + aria-selected（读屏可感知当前分组），点击即切换。
*/
.hist-tabs {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-bottom: 12px;
}
.hist-tab {
  appearance: none;
  -webkit-appearance: none;
  margin: 0;
  /* 工具条档：与时间档分段、导出按钮同 token ⇒ 三者高度严格相等（32 / 窄屏 36） */
  padding: 0 10px;
  height: var(--control-h-toolbar, 32px);
  box-sizing: border-box;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: var(--control-radius-toolbar, 6px);
  background: var(--el-fill-color-blank);
  color: var(--el-text-color-regular);
  font-family: inherit;
  font-size: var(--control-font-toolbar, 12px);
  line-height: 1;
  cursor: pointer;
  transition: color .15s, background-color .15s, border-color .15s;
}
.hist-tab:hover {
  color: var(--el-color-primary);
  border-color: var(--el-color-primary-light-5);
}
.hist-tab.is-active {
  /* 白字落在 primary 实心填充上：必须用主题 token（暗色下是深字），
     判据见 src/styles/__tests__/FillContrastContract.spec.ts 的对比度合同 */
  color: var(--text-on-fill);
  background: var(--el-color-primary);
  border-color: var(--el-color-primary);
  font-weight: 500;
}
.hist-tab:focus-visible { outline: 2px solid var(--el-color-primary-light-3); outline-offset: 1px; }
.hist-chart { width: 100%; }

/* ---- 窄屏（≤768px）：卡内距收紧 + tab 折行（不横向滚动、不隐藏任何分组） ---- */
@media (max-width: 768px) {
  .hist-title { font-size: 15px; line-height: 20px; }
  /* 卡头行距收紧：320px 下标题单独一行 + 控件行一行，卡头总高 142 → 约 82px */
  .hist-header { gap: 6px 12px; }
  /*
    卡头控件行**恒为单行**（标题可以单独一行）：时间档四段（约 170px）+ 导出图标按钮（36px）
    + 8px 间距 ≈ 214px，320px 卡内宽约 256px ⇒ 放得下。导出从「文字块」改图标按钮，
    正是为了腾出这 108px —— 改前它被挤到第二行，卡头折 3~4 行。
  */
  .header-controls { flex-wrap: nowrap; gap: 8px; }
  /* 36px 例外档要求相邻控件间距 ≥6px（规范 §4.4 控件尺寸档位；改前 4px 不达标） */
  .hist-tabs { gap: 6px; margin-bottom: 8px; }
  .hist-tab {
    height: var(--control-h-toolbar-touch, 36px);
    padding: 0 10px;
    font-size: var(--control-font-toolbar, 12px);
  }
  /* 导出图标按钮在窄屏也是 36×36（宽高必须同时切到触摸档，否则会变成 32×36 的长方形） */
  .hist-export {
    width: var(--control-h-toolbar-touch, 36px);
    height: var(--control-h-toolbar-touch, 36px);
    min-width: var(--control-h-toolbar-touch, 36px);
    min-height: var(--control-h-toolbar-touch, 36px);
  }
  /* 空态占满单图高度，切换分组时不抖卡高（与 chartHeightPx=320 同步） */
  .hist-chart .chart-sub-empty {
    height: 320px;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 0 12px;
    box-sizing: border-box;
  }
  :deep(.el-card__header) { padding: 10px 12px; }
  :deep(.el-card__body) { padding: 12px; }
}

/* 粗指针（宽视口触屏设备也会命中）：工具条档取 36px，与窄屏一致 */
@media (pointer: coarse) {
  .hist-tab {
    height: var(--control-h-toolbar-touch, 36px);
  }
  .hist-export {
    width: var(--control-h-toolbar-touch, 36px);
    height: var(--control-h-toolbar-touch, 36px);
    min-width: var(--control-h-toolbar-touch, 36px);
    min-height: var(--control-h-toolbar-touch, 36px);
  }
}
</style>
