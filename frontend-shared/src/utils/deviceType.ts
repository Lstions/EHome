/**
 * Device type metadata — single source of truth.
 *
 * Replaces the 5+ hardcoded deviceTypeMap/deviceTypes/deviceTypeOptions
 * scattered across EdgeDeviceList, EdgeDeviceDetail, NodeDetail,
 * PeripheralAssignForm, DeviceConfigList.
 */

import type { Component } from 'vue'
import {
  Odometer, Lightning, Cloudy, Sunny, DataAnalysis, Open, SetUp,
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
  { value: 'gpio.digital', label: 'GPIO 控制', icon: Open },
  { value: 'gpio.pwm', label: 'PWM 输出', icon: SetUp },
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
  return labelMap.get(type) || type
}

/** Get icon component for a device type, falls back to DataAnalysis */
export function getDeviceTypeIcon(type: string): Component {
  return iconMap.get(type) || DataAnalysis
}
