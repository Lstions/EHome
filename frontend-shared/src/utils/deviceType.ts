/**
 * Device type metadata — single source of truth.
 *
 * Replaces the 5+ hardcoded deviceTypeMap/deviceTypes/deviceTypeOptions
 * scattered across EdgeDeviceList, EdgeDeviceDetail, NodeDetail,
 * DeviceConfigList.
 */

import type { Component } from 'vue'
import {
  Cloudy, PartlyCloudy, WindPower, Compass, Drizzling,
  Sunny, Cpu, Switch, Box,
} from '@element-plus/icons-vue'

export interface DeviceTypeOption {
  value: string
  label: string
  icon: Component
  /**
   * 图标语义色调，对应 CSS 的 --icon-* 前景 token（见 theme.css）。
   * 只描述"这个类型属于什么语义"，不写死颜色 —— 亮/暗取值由主题给出。
   */
  tone?: IconTone
}

/** 图标语义色调。取值与 theme.css 的 --icon-* 一一对应。 */
export type IconTone = 'primary' | 'success' | 'warning' | 'danger' | 'info'

/**
 * Canonical device type list (used by filters, forms, labels)
 *
 * 图标按**含义**挑，不再按键名凑数（2026-10-17 全站统一）：
 * 改前是温湿度=仪表盘(Odometer)、风速=闪电(Lightning)、BMS=闪电、
 * 逆变器=图表(DataAnalysis) —— 都文不对题，用户看不出这是什么设备。
 */
export const deviceTypeOptions: DeviceTypeOption[] = [
  { value: 'temp_humidity', label: '温湿度传感器', icon: Cloudy, tone: 'info' },
  { value: 'wind_speed', label: '风速传感器', icon: WindPower, tone: 'info' },
  { value: 'wind_direction', label: '风向传感器', icon: Compass, tone: 'info' },
  { value: 'rain', label: '雨量传感器', icon: Drizzling, tone: 'warning' },
  { value: 'light', label: '光照传感器', icon: Sunny, tone: 'warning' },
  { value: 'battery', label: '电池保护板', icon: Cpu, tone: 'success' },
  { value: 'jiabaida_bms', label: 'BMS 电池管理系统', icon: Cpu, tone: 'success' },
  { value: 'inverter', label: '光伏逆变器', icon: Switch, tone: 'primary' },
  { value: 'bmp280', label: 'BMP280温压传感器', icon: PartlyCloudy, tone: 'info' },
  { value: 'sht40', label: 'SHT40温湿度传感器', icon: Cloudy, tone: 'info' },
  // GPIO/PWM 已移至独立外设控制组件，保留 fallback 标签用于已存在的记录
]

/** Quick lookup: device_type → Chinese label */
const labelMap = new Map<string, string>(
  deviceTypeOptions.map(o => [o.value, o.label])
)

/** Quick lookup: device_type → icon component */
const iconMap = new Map<string, Component>(
  deviceTypeOptions.map(o => [o.value, o.icon])
)

/** Quick lookup: device_type → icon tone */
const toneMap = new Map<string, IconTone>(
  deviceTypeOptions.flatMap(o => (o.tone ? [[o.value, o.tone] as [string, IconTone]] : []))
)

/**
 * 按 device_type 前缀/子串兜底的色调，覆盖不在 canonical 清单里的型号
 * （如 jiabaida_bms_v2、wind_speed_2）。顺序敏感：先具体后笼统。
 */
const TONE_PATTERNS: [RegExp, IconTone][] = [
  [/bms|battery/i, 'success'],
  [/inverter/i, 'primary'],
  [/light|rain/i, 'warning'],
  [/wind|temp|humidity|pressure/i, 'info'],
]

/** Get Chinese label for a device type, falls back to raw type */
export function getDeviceTypeLabel(type: string): string {
  // Fallback labels for legacy GPIO/PWM device types (moved to PeripheralControl)
  const fallbackLabels: Record<string, string> = {
    'gpio.digital': 'GPIO 控制',
    'gpio.pwm': 'PWM 输出',
  }
  return labelMap.get(type) || fallbackLabels[type] || type
}

/** Get icon component for a device type, falls back to Box (未知设备) */
export function getDeviceTypeIcon(type: string): Component {
  if (iconMap.has(type)) return iconMap.get(type) as Component
  // 未知型号按语义前缀给图标，而不是一律 DataAnalysis（"图表"与设备无关）
  if (/wind/i.test(type)) return WindPower
  if (/direction|compass/i.test(type)) return Compass
  if (/rain/i.test(type)) return Drizzling
  if (/light|sun/i.test(type)) return Sunny
  if (/temp|humidity|pressure/i.test(type)) return Cloudy
  if (/bms|battery/i.test(type)) return Cpu
  if (/inverter/i.test(type)) return Switch
  return Box
}

/**
 * Get icon tone for a device type.
 *
 * 颜色值不在这里 —— 只返回语义档位，由 CSS 的 --icon-* token 映射到具体颜色，
 * 这样亮/暗两套主题共用同一份映射（暗色下 primary 必须换成更亮的蓝，见 theme.css）。
 */
export function getDeviceTypeTone(type: string): IconTone {
  const direct = toneMap.get(type)
  if (direct) return direct
  for (const [re, tone] of TONE_PATTERNS) {
    if (re.test(type)) return tone
  }
  return 'primary'
}
