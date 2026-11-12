/**
 * 数据面板「行展示」纯函数 —— 历史表格 / 移动端摘要卡片 / 详情弹层共用的展示口径。
 *
 * 为什么搬到独立模块（2026-11-12 拆分，原 DataPanel.vue 内同名函数原样搬出）：
 *   · 这三段展示原本在同一份模板里，"指标名 / 单位 / 数值格式 / 时间文案"被三处复用；
 *     拆成子组件后函数不会跟着模板走，不抽出来就只能每个子组件复制一份，三份必然漂移
 *     （历史缺陷正是"表格与统计卡各读各的字段"）；
 *   · 取值仍只走 ./rowData.ts 的 parseRowData（唯一入口），本模块只决定"怎么显示"，
 *     **不得**补任何默认值：取不到就是「—」，不能落成 0（规范 §3.2.5）。
 */
import { UNKNOWN } from '@/utils/format'
import { sensorNameMap, sensorUnitMap } from '@/utils/sensor'
import { parseRowData } from './rowData'
import type { MeasurementCategory } from './types'

// ── 移动端趋势图：单指标 + 紧凑高度 ──
// P1（B-data 审计）：393 的 303px 画布里 4 根 Y 轴 + 20 条图例不可读（轴标签重叠、
// 图例被折叠成 1/20）；移动端改为单指标 + height 220（LineChart 的紧凑模式阈值 ≤220）。
//
// 这张表同时是「移动端历史数据摘要卡片」的优先级（拆分前 SUMMARY_METRIC_PRIORITY =
// CHART_METRIC_PRIORITY，同一个常量）。放在本模块是因为两者都是"行/序列怎么展示"的口径；
// 趋势图 composable 从这里引入，而不是各写一份 —— 否则"同一张表"会漂成两条真源。
export const CHART_METRIC_PRIORITY = [
  'total_voltage', 'current', 'rsoc', 'remaining_capacity', 'temperature_1',
  'power', 'cell_voltage_max', 'cell_voltage_min',
]

// ── 移动端历史数据「摘要 3~4 个关键指标 + 查看完整数据」──
// P0（B-data 审计）：393 下「数据」列 80px 装 638 字符 ⇒ 单行 2041px、整页 42689px。
// 摘要只挑业务优先级高的指标；完整 K/V 全量（不 ellipsis 丢数据）放进详情弹层。
const SUMMARY_METRIC_PRIORITY = CHART_METRIC_PRIORITY

/** 采集时间的展示口径（历史表格「采集时间」列与 CSV 导出共用）。 */
export function formatTime(time: string): string {
  return time ? new Date(time).toLocaleString('zh-CN') : UNKNOWN
}

/** 单行的时间文案：timestamp → created_at → collected_at 三段兜底，全空即「—」。 */
export function rowTimeText(row: Record<string, any> | null): string {
  if (!row) return UNKNOWN
  const time = row.timestamp || row.created_at || row.collected_at
  return time ? formatTime(time) : UNKNOWN
}

/** 表格「数据」列的一条 K/V 串（formatData 原名，原样搬出）。 */
export function formatData(data: Record<string, unknown> | null | undefined): string {
  // typeof + 键数双保险：null / undefined / 空对象 / 空数组 / 字符串一律「—」。
  // formatData 是显示层最后一道闸：即便上游把 values 传成空对象（"没有可信数值"），
  // 也会落回「—」而不是渲染成 "0" 或空白。
  if (!data || typeof data !== 'object' || Array.isArray(data)) return UNKNOWN

  const entries = Object.entries(data)
  if (entries.length === 0) return UNKNOWN

  return entries
    .map(([key, value]) => {
      // 支持 {value, unit} 格式
      if (typeof value === 'object' && value !== null && 'value' in value) {
        const entry = value as { value: unknown; unit?: unknown }
        const val = entry.value
        const unit = typeof entry.unit === 'string' ? entry.unit : ''
        if (typeof val === 'number') {
          return `${key}: ${val.toFixed(2)} ${unit}`.trim()
        }
        return `${key}: ${val} ${unit}`.trim()
      }
      if (typeof value === 'number') {
        return `${key}: ${value.toFixed(2)}`
      }
      return `${key}: ${value}`
    })
    .join(', ')
}

/**
 * 指标单位：设备可用类别的单位优先，其次全局字典。
 *
 * 拆分前直接读页面的 availableCategories；拆成子组件后由调用方把类别传进来
 * （同一份数组，取值口径不变）。
 */
function metricUnit(code: string, categories: MeasurementCategory[]): string {
  const category = categories.find(item => item.code === code)
  return category?.unit || sensorUnitMap[code] || ''
}

function formatMetricValue(value: number): string {
  return Number.isInteger(value) ? String(value) : value.toFixed(2)
}

export interface RowSummaryMetric { key: string; label: string; value: string; unit: string }

/** 移动端摘要：按业务优先级取前 4 个**纯数值**指标（字符串读数不进摘要，同趋势图）。 */
export function summarizeRow(row: Record<string, any>, categories: MeasurementCategory[]): RowSummaryMetric[] {
  const numbers = parseRowData(row).numbers
  if (!numbers) return []
  const rank = (code: string) => {
    const index = SUMMARY_METRIC_PRIORITY.indexOf(code)
    return index < 0 ? SUMMARY_METRIC_PRIORITY.length : index
  }
  return Object.keys(numbers)
    .sort((a, b) => rank(a) - rank(b))
    .slice(0, 4)
    .map(code => ({
      key: code,
      label: sensorNameMap[code] || code,
      value: formatMetricValue(numbers[code]),
      unit: metricUnit(code, categories),
    }))
}

export interface RowDetailEntry { key: string; label: string; value: string; unit: string }

/** 详情弹层：该行的**全部**展示值（含字符串读数），不 ellipsis、不丢字段。 */
export function rowDetailEntries(row: Record<string, any> | null, categories: MeasurementCategory[]): RowDetailEntry[] {
  const values = row ? parseRowData(row).values : null
  if (!values) return []
  return Object.entries(values).map(([key, raw]) => ({
    key,
    label: sensorNameMap[key] || key,
    value: String(raw ?? UNKNOWN),
    unit: metricUnit(key, categories),
  }))
}
