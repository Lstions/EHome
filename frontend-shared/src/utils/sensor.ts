/**
 * Sensor metadata maps — shared across Dashboard, EdgeDeviceDetail, etc.
 * S6 fix: Extracted from Dashboard.vue to eliminate duplication.
 */

/** 传感器中文名映射 */
export const sensorNameMap: Record<string, string> = {
  temperature: '温度',
  pressure: '气压',
  humidity: '湿度',
  wind_direction: '风向',
  wind_speed: '风速',
  light: '光照',
  noise: '噪声',
  co2: 'CO₂',
  pm25: 'PM2.5',
  rainfall: '降雨量',
  uv: '紫外线',
  illuminance: '光照',
  uv_index: '紫外线指数',
  rain_intensity: '雨强',
  rain_accum: '累计雨量',
  voltage: '电压',
  current: '电流',
  power: '功率',
  energy: '电量',
  soc: 'SOC',
  soh: 'SOH',
  frequency: '频率',
  // BMS sensors
  total_voltage: '总电压',
  rsoc: 'SOC',
  remaining_capacity: '剩余容量',
  protection_status: '保护状态',
  fet_status: 'MOS状态',
  temperature_1: '温度1',
  temperature_2: '温度2',
  temperature_3: '温度3',
  cell_voltage_max: '最高单体电压',
  cell_voltage_min: '最低单体电压',
}

/** 传感器单位映射 */
export const sensorUnitMap: Record<string, string> = {
  temperature: '°C',
  pressure: 'hPa',
  humidity: '%',
  wind_direction: '°',
  wind_speed: 'm/s',
  light: 'lux',
  noise: 'dB',
  co2: 'ppm',
  pm25: 'μg/m³',
  rainfall: 'mm',
  uv: 'μW/cm²',
  illuminance: 'lux',
  uv_index: '',
  rain_intensity: 'mm/h',
  rain_accum: 'mm',
  voltage: 'V',
  current: 'A',
  power: 'W',
  energy: 'kWh',
  soc: '%',
  soh: '%',
  frequency: 'Hz',
  // BMS sensors
  total_voltage: 'V',
  rsoc: '%',
  remaining_capacity: 'Ah',
  protection_status: '',
  fet_status: '',
  temperature_1: '°C',
  temperature_2: '°C',
  temperature_3: '°C',
  cell_voltage_max: 'V',
  cell_voltage_min: 'V',
}

/** 预定义传感器类型排序（已知类型按此顺序排列，未知类型排后面） */
export const SENSOR_ORDER = [
  'temperature', 'humidity', 'pressure',
  'wind_direction', 'wind_speed',
  'light', 'noise',
  'co2', 'pm25', 'rainfall', 'uv',
]
