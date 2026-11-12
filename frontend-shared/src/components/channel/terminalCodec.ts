/**
 * 通道终端的字节编解码 / 展示格式化纯函数。
 *
 * 边界依据（2026-11-12 纯搬移拆分，行为零变化）：
 *   · 搬入的是只做「字符串 ↔ 十六进制 ↔ ASCII」换算的纯函数，不读组件状态、不碰 DOM。
 *   · 函数体逐字未改（含 formatHexInput 先删空白再删 0x 的顺序 —— 反过来会改变结果）。
 *   · 没搬：displayMode 档位（展示偏好）留在 ChannelTerminal.vue；「是否展开」的集合与
 *     日志追加/暂停在 composables/useChannelTerminalLogs.ts；会话与发送在
 *     composables/useChannelTerminalSession.ts。
 */

/** 输入框里的 HEX：去空白、去 0x 前缀、统一大写 */
export const formatHexInput = (input: string): string => input.replace(/\s+/g, '').replace(/0x/gi, '').toUpperCase()

/** 合法 HEX：仅十六进制字符且长度为偶数 */
export const isValidHex = (str: string): boolean => /^[0-9A-Fa-f]*$/.test(str) && str.length % 2 === 0

/** ASCII 明文 → HEX（每字符两个十六进制位，大写） */
export const asciiToHex = (ascii: string): string => {
  let hex = ''
  for (let i = 0; i < ascii.length; i++) hex += ascii.charCodeAt(i).toString(16).padStart(2, '0').toUpperCase()
  return hex
}

/** HEX → ASCII；不可打印位以 '.' 代替 */
export const hexToAscii = (hex: string): string => {
  let result = ''
  for (let i = 0; i < hex.length; i += 2) {
    const code = parseInt(hex.substring(i, i + 2), 16)
    result += (code >= 32 && code <= 126) ? String.fromCharCode(code) : '.'
  }
  return result
}

/** 字节数 = 去空白后的十六进制字符数 / 2 */
export const getByteCount = (data: string): number => {
  if (!data || typeof data !== 'string') return 0
  return data.replace(/\s/g, '').length / 2
}

/**
 * 单条日志的展示文本。
 * 超过 32 字节且未展开时只显示前 32 字节（与拆分前一致，含 ASCII 档同样按截断后的字节解码）。
 */
export const formatTerminalData = (data: string | any, expanded: boolean, displayMode: 'hex' | 'ascii'): string => {
  if (!data) return ''
  if (typeof data !== 'string') return String(data)
  const hex = data.replace(/\s/g, '')
  const byteCount = hex.length / 2
  if (byteCount > 32 && !expanded) {
    const truncated = hex.substring(0, 32)
    return displayMode === 'ascii' ? hexToAscii(truncated) : truncated.replace(/(.{2})/g, '$1 ').trim()
  }
  return displayMode === 'ascii' ? hexToAscii(hex) : hex.replace(/(.{2})/g, '$1 ').trim()
}
