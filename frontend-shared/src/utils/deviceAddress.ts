/**
 * 设备地址判定 — 与后端 deviceaction.ParseHardwareAddress 单一口径对齐。
 *
 * ⚠️ 为什么必须与后端"逐字一致"而不是各写一份：
 *   2026-09-20 生产故障的根因就是**两层语义被无声混用** ——
 *   创建向导把 Channel.hardware_id（总线标识，实测值 "UART1"）原样复制成
 *   EdgeDevice.hardware_id（设备地址，应为 1-254 的 Modbus 从站号）。
 *   写入时无人校验，于是每次派发都在后端 ParseHardwareAddress 被拒：
 *     hardware_id "UART1" must be an address from 1 to 254
 *   而 UI 侧操作一直显示 QUEUED，约 120 秒后才变 FAILED
 *   （"deadline expired before dispatch"），真实原因完全不可见。
 *
 * 因此本文件不是"又一份正则"，而是**前端唯一的地址口径入口**：
 *   · 后端真源：backend/internal/deviceaction/definition.go 的 ParseHardwareAddress
 *   · 一致性证据：frontend-shared/src/utils/__tests__/DeviceAddressContractParity.spec.ts
 *     用同一张表驱动两侧判定，并从后端源码解析出真源做**双向集合断言**；
 *     一旦任一侧口径漂移，门禁当场变红。
 *
 * 与后端逐行对齐的语义（含边界）：
 *   ""      -> 合法，默认地址 1（后端把空/0 当历史默认值）
 *   "   "   -> 合法，trim 后为空 → 默认地址 1
 *   "0"     -> 合法，默认地址 1
 *   "1"     -> 合法
 *   "254"   -> 合法（0xFE 是最高合法从站号）
 *   "255"   -> 非法（超界）
 *   "0x01"  -> 合法
 *   "0xFE"  -> 合法
 *   "0xFF"  -> 非法
 *   "0x00"  -> 非法（解析为 0，低于下界）
 *   "0x1"   -> 合法（一位十六进制也可）
 *   "0Xfe"  -> 合法（0X 前缀）
 *   "007"   -> 合法（十进制前导零）
 *   "00"    -> 非法（解析为 0）
 *   " 1 "   -> 合法（先 trim）
 *   "UART1" -> 非法（本次故障值：总线名不是地址）
 *   "abc" / "-1" / "+1" / "1.5" / "1e2" / "0b1" / "1_0" -> 非法
 */

/** 后端认定的合法地址下界（Modbus 从站号 1 起）。 */
export const DEVICE_ADDRESS_MIN = 1
/** 后端认定的合法地址上界（0xFE；255/0xFF 保留不可用）。 */
export const DEVICE_ADDRESS_MAX = 254

/** 空值/0 在后端被当作"历史默认地址 1"，而不是错误。 */
export const DEVICE_ADDRESS_DEFAULT = 1

export interface DeviceAddressVerdict {
  /** 是否合法（含空/0 这种"默认地址"形态）。 */
  valid: boolean
  /**
   * 解析出的地址（1-254）。未提供地址时为空/0 的输入都归一为默认地址 1。
   * 非法时为 null。
   */
  address: number | null
  /**
   * 该值是否"确实指定了一个地址"。
   * false 表示空/0 —— 后端会退回默认地址 1，不构成非法。
   */
  explicit: boolean
}

/**
 * 判定一个 EdgeDevice.hardware_id 是否是合法设备地址。
 *
 * @param value 通道/设备上存的值；undefined 代表"未提供"
 */
export function parseDeviceAddress(value: string | null | undefined): DeviceAddressVerdict {
  const trimmed = (value ?? '').trim()
  if (trimmed === '' || trimmed === '0') {
    return { valid: true, address: DEVICE_ADDRESS_DEFAULT, explicit: false }
  }
  let base = 10
  let digits = trimmed
  if (trimmed.startsWith('0x') || trimmed.startsWith('0X')) {
    base = 16
    digits = trimmed.slice(2)
  }
  // 与后端 strconv.ParseUint(text, base, 8) 对齐：只接受 ASCII 数字，
  // 不接受符号、小数点、指数、下划线、进制前缀（0b）。
  if (digits === '' || !isAllDigitsForBase(digits, base)) {
    return { valid: false, address: null, explicit: true }
  }
  const parsed = parseInt(digits, base)
  if (!Number.isFinite(parsed) || parsed < DEVICE_ADDRESS_MIN || parsed > DEVICE_ADDRESS_MAX) {
    return { valid: false, address: null, explicit: true }
  }
  return { valid: true, address: parsed, explicit: true }
}

/** 便捷判定：是否为合法设备地址（空/0 视为合法默认值）。 */
export function isValidDeviceAddress(value: string | null | undefined): boolean {
  return parseDeviceAddress(value).valid
}

/**
 * 通道上的 hardware_id 是否是"设备地址"而不是"总线标识"。
 *
 * 这是 R1 的判定入口：true 才允许把它直接当 EdgeDevice.hardware_id 提交；
 * false（如 "UART1"/"I2C0"）必须让用户显式填写设备地址。
 *
 * 注意：空/0 返回 true（后端会归一为默认地址 1），但此时**并不代表**
 * 通道里存了一个有意义的地址 —— 需要"该字段必须由用户填写"的场景请用
 * parseDeviceAddress(...).explicit 而不是本函数。
 */
export function channelHardwareIdIsDeviceAddress(value: string | null | undefined): boolean {
  return isValidDeviceAddress(value)
}

function isAllDigitsForBase(text: string, base: number): boolean {
  for (const char of text) {
    const code = char.charCodeAt(0)
    if (code >= 48 && code <= 57) continue // 0-9
    if (base === 16 && code >= 97 && code <= 102) continue // a-f
    if (base === 16 && code >= 65 && code <= 70) continue // A-F
    return false
  }
  return true
}

/**
 * 把用户输入归一成后端可接受的形式：trim。
 *
 * 不改变数值语义（" 1 " → "1"，十进制/十六进制原样保留），也不把
 * "0x01" 改写成 "1" —— 后端两种形态都接受，保持用户所见即所存。
 */
export function normalizeDeviceAddressInput(value: string | null | undefined): string {
  return (value ?? '').trim()
}

/** 面向用户的地址格式提示（与后端错误文案同一措辞口径）。 */
export const DEVICE_ADDRESS_HINT = '设备地址（1-254 的十进制或 0xNN，如 1 / 0x01）'

/** 面向用户的非法地址错误文案（含"这不是总线名"的明确指引）。 */
export const DEVICE_ADDRESS_ERROR = '设备地址必须是 1-254 的十进制或 0xNN 形式；UART1/I2C0 这类是总线名称，不是设备地址'
