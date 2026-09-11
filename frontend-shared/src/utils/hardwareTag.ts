/**
 * 硬件总线类型 → Element Plus tag type 统一映射。
 * 单一来源，消除 EdgeDeviceList / ChannelList 等页面的重复定义和颜色不一致。
 */
import type { TagType } from './tagType'

export type HardwareTagType = TagType

const HARDWARE_TAG_MAP: Record<string, HardwareTagType> = {
  uart: 'primary',
  i2c: 'success',
  spi: 'warning',
  gpio: 'info',
  adc: 'danger',
  pwm: 'info',
}

export function getHardwareTagType(type: string): HardwareTagType {
  return HARDWARE_TAG_MAP[type] ?? 'info'
}

const HARDWARE_LABEL_MAP: Record<string, string> = {
  uart: 'UART',
  i2c: 'I2C',
  spi: 'SPI',
  gpio: 'GPIO',
  adc: 'ADC',
  pwm: 'PWM',
}

export function getHardwareLabel(type: string): string {
  return HARDWARE_LABEL_MAP[type] ?? type.toUpperCase()
}
