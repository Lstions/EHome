/**
 * 边缘设备卡片「最新读数」语义化（2026-10-17 由评审 demo 转正）。
 *
 * 背景：列表卡片原先直接打印后端字段名 —— 用户截图里那行
 * `cell_count: 16.00 | cell_overvolt...` 既读不懂，也违反项目
 * 「中文优先、不出现内部键名」的既定要求。
 *
 * 本模块按设备类型挑 3 个**最有信息量**的指标，翻成中文并补单位；
 * 字段名/单位复用 utils/sensor.ts 的单一事实源（sensorNameMap / sensorUnitMap），
 * 只对本项目新增的 BMS 字段做少量补充，避免第二份映射漂移。
 */

import { sensorNameMap, sensorUnitMap } from './sensor'
import type { EdgeDevice } from '@/api/edgeDevice'

/** 不是"读数"的元数据字段，不进卡片 */
const HIDDEN_KEYS = new Set([
  'timestamp', 'device_id', 'device_type', 'channel_id', 'raw_data',
])

/** sensorNameMap 未覆盖、但卡片会用到的补充标签 */
const EXTRA_LABELS: Record<string, string> = {
  cell_count: '电芯串数',
  cycle_count: '循环次数',
  nominal_capacity: '标称容量',
  remaining_capacity: '剩余容量',
  software_version: '固件版本',
  balancing: '均衡状态',
}

/** 补充单位（sensorUnitMap 未覆盖的） */
const EXTRA_UNITS: Record<string, string> = {
  cell_count: '串',
  cycle_count: '次',
  nominal_capacity: 'Ah',
  remaining_capacity: 'Ah',
}

/** 各设备类型的关键指标优先级：越靠前越先展示 */
const BMS_PRIORITY = [
  'rsoc', 'soc', 'total_voltage', 'current',
  'cell_voltage_max', 'cell_voltage_min', 'remaining_capacity',
]
const INVERTER_PRIORITY = [
  'output_active_power', 'output_load_percent', 'grid_voltage',
  'battery_voltage', 'battery_capacity', 'daily_energy', 'max_temp',
]
const GENERIC_PRIORITY = [
  'temperature', 'humidity', 'pressure', 'wind_speed', 'light', 'pm25', 'co2', 'rainfall',
]

/** 按设备类型取优先级表；未知类型走通用表 */
export function readingPriorityFor(deviceType: string): string[] {
  const t = (deviceType || '').toLowerCase()
  if (t.indexOf('bms') >= 0 || t === 'battery') return BMS_PRIORITY
  if (t === 'inverter') return INVERTER_PRIORITY
  return GENERIC_PRIORITY
}

/** 字段名 → 中文标签。支持 cell_voltage_N / temp_N 这类带序号字段。 */
export function readingLabel(key: string): string {
  if (sensorNameMap[key]) return sensorNameMap[key]
  const cell = /^cell_voltage_(\d+)$/.exec(key) || /^cell_v_(\d+)$/.exec(key)
  if (cell) return '单体' + cell[1] + '电压'
  const temp = /^temp_(\d+)$/.exec(key) || /^temperature_(\d+)$/.exec(key)
  if (temp) return '探头' + temp[1] + '温度'
  return EXTRA_LABELS[key] || key
}

/** 字段名 → 单位 */
export function readingUnit(key: string): string {
  if (sensorUnitMap[key]) return sensorUnitMap[key]
  if (/^cell_voltage_/.test(key) || /^cell_v_/.test(key)) return 'V'
  if (/^temp_/.test(key) || /^temperature_/.test(key)) return '°C'
  return EXTRA_UNITS[key] || ''
}

/**
 * 数值按字段语义定小数位。
 * 为什么不能统一 toFixed(1)：单体电压 3.250V 只留一位会变成 3.3（丢掉诊断价值），
 * 而串数 16 带小数又很怪。
 */
export function formatReadingValue(key: string, v: unknown): string {
  const n = Number(v)
  if (!Number.isFinite(n)) return String(v)
  if (/^cell_voltage_/.test(key) || /^cell_v_/.test(key)) return n.toFixed(3)
  if (key === 'total_voltage') return n.toFixed(2)
  if (key === 'rsoc' || key === 'soc' || key === 'battery_capacity' || key === 'output_load_percent') return n.toFixed(1)
  if (key === 'current' || key === 'battery_charge_current' || key === 'battery_discharge_current') return n.toFixed(2)
  if (Math.abs(n) >= 100) return n.toFixed(0)
  return n.toFixed(1)
}

export interface DeviceReading {
  key: string
  label: string
  value: string
  unit: string
}

/**
 * 挑出卡片要展示的关键读数。
 * 优先按类型优先级取，不足则用剩余字段补齐（保证任何设备都有内容可显示）；
 * 无数据返回空数组，由调用方渲染「等待首条采集数据」。
 */
export function pickDeviceReadings(
  device: Pick<EdgeDevice, 'device_type' | 'last_data'>,
  limit = 3,
): DeviceReading[] {
  const data = device.last_data
  if (!data || typeof data !== 'object') return []
  const keys = Object.keys(data).filter((k) => !HIDDEN_KEYS.has(k))
  if (keys.length === 0) return []
  const priority = readingPriorityFor(device.device_type)
  const ordered = priority
    .filter((k) => keys.indexOf(k) >= 0)
    .concat(keys.filter((k) => priority.indexOf(k) < 0))
  return ordered.slice(0, limit).map((k) => ({
    key: k,
    label: readingLabel(k),
    value: formatReadingValue(k, (data as Record<string, unknown>)[k]),
    unit: readingUnit(k),
  }))
}
