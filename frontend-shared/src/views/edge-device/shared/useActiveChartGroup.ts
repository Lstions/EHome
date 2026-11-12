import { computed, ref, watch, type Ref } from 'vue'
import type { SeriesData } from '@/types/chart'
import { buildChartSubGroups, groupHasData, type ChartSubGroupResult } from './historyChartGroups'

/**
 * 「分组 tab + 单图」的选中态组合式。
 *
 * 边界依据（2026-11-12 纯搬移拆分，行为零变化）：
 *   · 搬入的是**与 DOM 无关**的分组投影与选中状态机：chartSeries → chartSubGroups、
 *     默认选中「第一个有数据的分组」、用户手选后不被数据到达覆盖（groupTouched）、
 *     切换只改 activeGroupTitle（不重新发请求）。
 *   · 没搬走的：
 *       - tab 的 DOM 结构 / role=tablist / aria-selected 仍由原组件模板承担：
 *         HistoryChartSectionTabbed.spec.ts 直接断言原文件文本含这些标记与
 *         `@click="onGroupSelect(g.title)"`，因此本组合式导出的 onGroupSelect 保持同名；
 *       - 图高 / 右轴预留留在原组件：ChartPlotAlignment.spec.ts 断言原文件含
 *         'LINE_CHART_MAX_Y_AXIS = 4' 与 maxReserveRightAxes（页面级契约锚点）。
 */
export interface ActiveChartGroupSource {
  deviceType: string
}

export function useActiveChartGroup(source: ActiveChartGroupSource, chartSeries: Ref<SeriesData[]>) {
  const chartSubGroups = computed<ChartSubGroupResult[]>(() =>
    buildChartSubGroups(source.deviceType, chartSeries.value),
  )

  /* ---- 窄屏分组下拉 ----
   * 默认选中「第一个有数据的分组」；无数据时回退第一个分组。
   * 用户手动切换后不再被数据到达覆盖（groupTouched），但若所选分组在数据刷新后
   * 已不存在（例如切到 1h 后该量全无数据），仍回退到第一个有数据的分组，避免下拉显示空值。
   * 切换只改 activeGroupTitle，不调用 fetchHistoryData ⇒ 不产生新请求。 */
  const activeGroupTitle = ref('')
  const groupTouched = ref(false)

  /*
   * 点击 tab 切换分组（2026-10-21：取代窄屏下拉的 onGroupChange）。
   * 只改 activeGroupTitle —— 不调用 fetchHistoryData，切换零请求、零延迟。
   */
  function onGroupSelect(title: string) {
    if (title === activeGroupTitle.value) return
    activeGroupTitle.value = title
    groupTouched.value = true
  }

  const activeGroup = computed<ChartSubGroupResult | undefined>(
    () => chartSubGroups.value.find(g => g.title === activeGroupTitle.value) ?? chartSubGroups.value[0],
  )
  const mobileSeriesCount = computed(() => activeGroup.value?.series.length ?? 0)

  /** 当前分组实际绘制的点数（测试/探针用：>0 才证明横轴真有数据，不是空图） */
  const mobilePointCount = computed(
    () => (activeGroup.value?.series ?? []).reduce((n, s) => n + s.data.length, 0),
  )

  watch(
    () => chartSubGroups.value.map(g => `${g.title}:${groupHasData(g) ? 1 : 0}`).join(','),
    () => {
      const groups = chartSubGroups.value
      if (groups.length === 0) { activeGroupTitle.value = ''; return }
      if (groupTouched.value && groups.some(g => g.title === activeGroupTitle.value)) return
      const firstWithData = groups.find(g => groupHasData(g))
      activeGroupTitle.value = firstWithData ? firstWithData.title : groups[0].title
    },
    { immediate: true },
  )

  return { chartSubGroups, activeGroupTitle, activeGroup, onGroupSelect, mobileSeriesCount, mobilePointCount }
}
