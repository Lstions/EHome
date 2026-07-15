/**
 * 设备类型映射
 */
export const DEVICE_TYPE_MAP: Record<string, string> = {
  wind_speed: '风速传感器',
  wind_direction: '风向传感器',
  rain: '雨量传感器',
  light: '光照传感器',
  temp_humidity: '温湿度传感器',
  battery: '电池保护板',
  inverter: '光伏逆变器'
}

/**
 * 设备类型选项
 */
export const DEVICE_TYPE_OPTIONS = [
  { label: '风速传感器', value: 'wind_speed' },
  { label: '风向传感器', value: 'wind_direction' },
  { label: '雨量传感器', value: 'rain' },
  { label: '光照传感器', value: 'light' },
  { label: '温湿度传感器', value: 'temp_humidity' },
  { label: '电池保护板', value: 'battery' },
  { label: '光伏逆变器', value: 'inverter' }
]

/**
 * 通信协议映射
 */
export const PROTOCOL_MAP: Record<string, string> = {
  modbus: 'MODBUS',
  stream: '字节流'
}

/**
 * 总线类型映射
 */
export const BUS_TYPE_MAP: Record<string, string> = {
  uart: 'UART',
  i2c: 'I2C',
  spi: 'SPI'
}

/**
 * 状态颜色映射
 */
export const STATUS_COLOR_MAP: Record<string, string> = {
  online: 'var(--color-success)',
  offline: 'var(--color-info)',
  error: 'var(--color-danger)'
}

/**
 * 分页大小选项
 */
export const PAGE_SIZE_OPTIONS = [10, 20, 50, 100]

/**
 * 默认分页大小
 */
export const DEFAULT_PAGE_SIZE = 20
