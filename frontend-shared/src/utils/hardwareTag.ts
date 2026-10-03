/**
 * 硬件总线类型 → 展示标签 / Element Plus tag type 的统一映射。
 * 单一来源：消除 EdgeDeviceList / ChannelList / ChannelTerminal 等页面各写一套 map，
 * 以及同一实体曾出现的 UART / 串行 / 串口 (UART) 等多套写法。
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

/**
 * 总线类型归一：后端实测存**大写**（'UART'），历史数据/驱动声明混有小写（'uart'）。
 * 调用方各自 trim/toUpperCase 必然分叉，故归一集中在此，函数只认归一键。
 */
function canonicalizeHardwareType(type: string): string {
  return String(type ?? '').trim().toLowerCase()
}

export function getHardwareTagType(type: string): HardwareTagType {
  return HARDWARE_TAG_MAP[canonicalizeHardwareType(type)] ?? 'info'
}

const HARDWARE_LABEL_MAP: Record<string, string> = {
  uart: 'UART',
  i2c: 'I2C',
  spi: 'SPI',
  gpio: 'GPIO',
  adc: 'ADC',
  pwm: 'PWM',
}

/**
 * 面向用户的英文标签：统一大写（入参 'UART'/'uart' 均可，归一在内部完成）。
 * 未知类型返回归一后的大写原文而非空串 —— 界面宁可显示未建模的总线名，也不留白。
 */
export function getHardwareLabel(type: string): string {
  const key = canonicalizeHardwareType(type)
  return HARDWARE_LABEL_MAP[key] ?? key.toUpperCase()
}

/**
 * 面向用户的中文名，与 getHardwareLabel **一一对应**。
 * 只在此处定义：页面需要中文时 import 它，不得各自再写 map（否则又是多套写法）。
 */
const HARDWARE_TITLE_MAP: Record<string, string> = {
  uart: '串行',
  i2c: 'I²C',
  spi: 'SPI',
  gpio: '数字IO',
  adc: '模拟',
  pwm: 'PWM',
}

export function getHardwareTitle(type: string): string {
  const key = canonicalizeHardwareType(type)
  // 未知类型回退英文标签，保证与 getHardwareLabel 同口径、非空。
  return HARDWARE_TITLE_MAP[key] ?? getHardwareLabel(type)
}

/**
 * hardware_id 是否已经自带总线名（'UART0' / 'I2C0' / 'SPI0_CS0'）。
 * 判据：以字母开头 = 自描述（总线名 + 序号/片选），可直接显示；
 *       以数字开头 = 纯资源序号或设备地址（'1'、'0x76'），才需要补总线前缀。
 * 之所以不按「是否包含某总线名」判断：那要求调用方再维护一份总线名清单，与共享源重复；
 * 而前缀重复正是旧写法渲染出 "UART UART0" 的成因。
 */
export function hardwareIdIncludesBusName(hardwareId: string): boolean {
  return /^[A-Za-z]/.test(String(hardwareId ?? '').trim())
}

/**
 * 通道硬件标识展示：hardware_id 自带总线名时原样返回（不重复前缀），
 * 纯数字/地址时补共享总线标签；hardware_id 为空时只回退总线标签。
 */
export function getHardwareDisplay(type: string, hardwareId: string): string {
  const hwId = String(hardwareId ?? '').trim()
  if (!hwId) return getHardwareLabel(type)
  if (hardwareIdIncludesBusName(hwId)) return hwId
  const label = getHardwareLabel(type)
  return label ? `${label} ${hwId}` : hwId
}
