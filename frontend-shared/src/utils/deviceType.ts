/**
 * Device type metadata — single source of truth.
 *
 * Replaces the 5+ hardcoded deviceTypeMap/deviceTypes/deviceTypeOptions
 * scattered across EdgeDeviceList, EdgeDeviceDetail, NodeDetail,
 * PeripheralAssignForm, DeviceConfigList.
 */

import type { Component } from 'vue'
import {
  Odometer, Lightning, Cloudy, Sunny, DataAnalysis,
} from '@element-plus/icons-vue'

export interface DeviceTypeOption {
  value: string
  label: string
  icon: Component
}

/** Canonical device type list (used by filters, forms, labels) */
export const deviceTypeOptions: DeviceTypeOption[] = [
  { value: 'temp_humidity', label: '温湿度传感器', icon: Odometer },
  { value: 'wind_speed', label: '风速传感器', icon: Lightning },
  { value: 'wind_direction', label: '风向传感器', icon: Cloudy },
  { value: 'rain', label: '雨量传感器', icon: Cloudy },
  { value: 'light', label: '光照传感器', icon: Sunny },
  { value: 'battery', label: '电池保护板', icon: Lightning },
  { value: 'jiabaida_bms', label: 'BMS 电池管理系统', icon: Lightning },
  { value: 'inverter', label: '光伏逆变器', icon: DataAnalysis },
  { value: 'bmp280', label: 'BMP280温压传感器', icon: Odometer },
  { value: 'sht40', label: 'SHT40温湿度传感器', icon: Odometer },
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

/** Get Chinese label for a device type, falls back to raw type */
export function getDeviceTypeLabel(type: string): string {
  // Fallback labels for legacy GPIO/PWM device types (moved to PeripheralControl)
  const fallbackLabels: Record<string, string> = {
    'gpio.digital': 'GPIO 控制',
    'gpio.pwm': 'PWM 输出',
  }
  return labelMap.get(type) || fallbackLabels[type] || type
}

/** Get icon component for a device type, falls back to DataAnalysis */
export function getDeviceTypeIcon(type: string): Component {
  return iconMap.get(type) || DataAnalysis
}
