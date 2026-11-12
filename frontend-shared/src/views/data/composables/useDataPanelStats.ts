/**
 * 数据面板「统计概览卡」的派生数据（原 DataPanel.vue 的 dynamicStats / calculateStats 段原样搬出）。
 *
 * 边界依据（为什么整块搬）：
 *   · 两张卡的口径不同，且**必须逐卡标注范围**（规范 §4.3）：dynamicStats 取「本页最新一行」，
 *     latestStats.duration 只遍历「本页 20 条」，而 totalPoints 用服务端筛选后的**全量 total**。
 *     三者是同一次查询的三种分母，拆开就会各自漂移；
 *   · 输入只有三个 ref（本页行 / 全量 total / 指标类别），输出只有两张卡的数据，没有取数副作用。
 *
 * 刻意留在页面的：范围词常量（SCOPE_PAGE / SCOPE_FILTERED）。它是**渲染契约**，
 * 由 DataPanel.spec.ts 的 DOM 断言与 §4.3 门禁锁定，不能藏进 composable 变得不可文本审计。
 */
import { computed, reactive, type Ref } from 'vue'
import { UNKNOWN } from '@/utils/format'
import { sensorNameMap, sensorUnitMap } from '@/utils/sensor'
import { parseRowData } from '../rowData'
import type { CollectedRow } from '../historyRows'
import type { MeasurementCategory } from '../types'

/**
 * 「最新」统计卡的指标优先级：改前取 availableCategories 的**字母序前 3**（BMS 上是
 * cell_voltage_1/10/11，三张卡都是 3.27V，等于没有信息量）。这里按业务优先级排序后取前 3。
 */
const STAT_METRIC_PRIORITY = [
  'total_voltage', 'current', 'rsoc', 'remaining_capacity', 'temperature_1',
  'power', 'cell_voltage_max', 'cell_voltage_min', 'total_power', 'frequency',
]
function statRank(code: string): number {
  const index = STAT_METRIC_PRIORITY.indexOf(code)
  return index < 0 ? STAT_METRIC_PRIORITY.length : index
}

export function useDataPanelStats(
  /** 当前页的历史行（一次采集一行）—— 「最新 X」与「采集覆盖时长」都只由它算出。 */
  historyData: Ref<CollectedRow[]>,
  /** 服务端按设备 + 时间范围筛出的全量采集数 —— 「数据点总数」的唯一来源。 */
  total: Ref<number>,
  /** 设备可用指标类别（单位来源）。 */
  availableCategories: Ref<MeasurementCategory[]>,
) {
  // 统计概览数据
  const latestStats = reactive({
    totalPoints: 0,
    duration: UNKNOWN
  })

  const dynamicStats = computed(() => {
    const latest = [...historyData.value]
      .sort((a, b) => String(b.timestamp || b.created_at || b.collected_at || '').localeCompare(String(a.timestamp || a.created_at || a.collected_at || '')))[0]
    // 与表格列共用 parseRowData：改前读 latest.parsed_data || latest.data，
    // 而接口只给 data_json ⇒ source 恒 {} ⇒ 统计卡恒不渲染（数据非空却一张卡都没有）。
    // 统计卡是**数值指标**视图：用 numbers（纯数值）而不是 values（可能含字符串），
    // 否则 hardware_version="V19" 会被当成 0 计入并污染统计。
    const source = parseRowData(latest).numbers ?? {}
    return [...availableCategories.value]
      // 业务优先级排序（稳定排序：未列出的指标保持后端原顺序），不再按字母序取前 3。
      .sort((a, b) => statRank(a.code) - statRank(b.code))
      .map((category) => {
        const value = source[category.code] ?? null
        if (value === null) return null
        return {
          code: category.code,
          label: `最新${sensorNameMap[category.code] || category.code}`,
          unit: category.unit || sensorUnitMap[category.code] || '',
          value: Number.isInteger(value) ? value : Number(value.toFixed(2)),
        }
      })
      .filter((stat): stat is { code: string; label: string; unit: string; value: number } => stat !== null)
      .slice(0, 3)
  })

  const calculateStats = () => {
    const items = historyData.value
    latestStats.totalPoints = total.value
    if (!items || items.length === 0) {
      // 空数组是「本页没有数据」，不是「时长为 0」；用统一未知占位符而非 '--'
      // （'--' 是第三种占位写法，规范 §3.4.5 要求未知值统一为 '—'）。
      latestStats.duration = UNKNOWN
      return
    }

    const times = items
      .map(item => item.timestamp || item.created_at || item.collected_at)
      .filter((t): t is string => typeof t === 'string' && t !== '')
      .map(t => new Date(t).getTime())
      .filter(t => !Number.isNaN(t))
      .sort((a, b) => a - b)
    if (times.length < 2) {
      latestStats.duration = '< 1分钟'
      return
    }

    const diffMin = Math.floor((times[times.length - 1] - times[0]) / 60000)
    // P2（审计实测：本页 20 条跨 19s 却显示「0分钟」，像没采到数据）：
    // 不足 1 分钟统一走本页已有的「< 1分钟」写法，不显示 0。
    if (diffMin < 1) {
      latestStats.duration = '< 1分钟'
    } else if (diffMin < 60) {
      latestStats.duration = `${diffMin}分钟`
    } else if (diffMin < 1440) {
      latestStats.duration = `${Math.floor(diffMin / 60)}小时${diffMin % 60}分钟`
    } else {
      latestStats.duration = `${Math.floor(diffMin / 1440)}天${Math.floor((diffMin % 1440) / 60)}小时`
    }
  }

  return { latestStats, dynamicStats, calculateStats }
}
