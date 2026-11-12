/**
 * 通道表单的共享模型（总线类型 / 表单形状 / 能力资源条目）。
 *
 * 边界依据：这些定义同时被「表单组合式函数」「提交载荷编码」和「页面模板」使用。
 * 单独成模块是为了避免 composable ↔ 工具模块互相 import 造成反向依赖。
 * UART 默认波特率不在这里：它是页面级提交契约（源码门禁也要求出现在原组件），
 * 由 ChannelManager.vue 定义后同时传给表单初始化与提交载荷编码。
 */

/** 前端建模的总线类型（与后端 Channel.hardware_type 的小写形态对齐）。 */
export const HW_TYPES = ['uart', 'i2c', 'spi', 'adc'] as const
export type HardwareBusType = (typeof HW_TYPES)[number]

/** 运行时收窄：后端可能回大写或未知类型，统一转小写，非法值回退 i2c（原默认值）。 */
export const toBusType = (value: unknown): HardwareBusType => {
  const lower = typeof value === 'string' ? value.toLowerCase() : ''
  return (HW_TYPES as readonly string[]).includes(lower) ? (lower as HardwareBusType) : 'i2c'
}

/** 能力上报里的硬件资源条目（字段随总线类型变化，只声明实际读取的字段）。 */
export interface HardwareResource {
  id: string | number
  name?: string
  pins?: Array<{ pin?: number; label?: string }>
  default_tx_pin?: number
  default_rx_pin?: number
  default_sda_pin?: number
  default_scl_pin?: number
  default_cs_pin?: number
  default_mosi?: number
  default_miso?: number
  default_sclk?: number
  max_freq_hz?: number
  [key: string]: unknown
}

/** 通道表单的可变状态（由 useChannelManager 创建并持有，页面与参数子组件共享同一 reactive 对象）。 */
export interface ChannelFormState {
  name: string
  hardware_type: HardwareBusType
  hardware_id: string
  address: string
  enabled: boolean
  config: Record<string, any>
}
