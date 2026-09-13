/**
 * 未知 / 空值 / 未配置的统一占位符：em dash「—」（规范 §3.4.5 MUST）。
 *
 * 为什么必须是常量而不是各处写字面量：半角连字符 `'-'` 与减号、路径分隔符、正则字符类、
 * CSS 值与 `0001-01-01` 这类哨兵日期**同形**，散落在模板里既无法用搜索区分，
 * 也无法在评审中证明"所有未知值都被统一"。集中定义后，占位语义只有一处权威实现。
 *
 * 注意：`—` 表示「接口没说 / 未配置」，`0` 表示「确实是 0」，二者不得互相顶替
 * （规范 §3.2.5 不得以本地默认值伪造事实）。
 */
export const UNKNOWN = '—'

/**
 * 格式化时间
 */
export function formatTime(time: string | Date | null | undefined): string {
  if (!time || time === '0001-01-01T00:00:00Z' || time === '1970-01-01T00:00:00Z') return UNKNOWN
  const date = typeof time === 'string' ? new Date(time) : time
  if (isNaN(date.getTime()) || date.getFullYear() <= 1970) return UNKNOWN
  return date.toLocaleString('zh-CN', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit'
  })
}

/**
 * 格式化文件大小
 */
export function formatFileSize(bytes: number): string {
  if (bytes === 0) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i]
}

/**
 * 格式化数字，保留指定小数位
 */
export function formatNumber(num: number, decimals: number = 2): string {
  return num.toFixed(decimals)
}

/**
 * 格式化对象数据为可读字符串
 */
export function formatObjectData(data: Record<string, any>): string {
  if (!data) return UNKNOWN
  return Object.entries(data)
    .map(([key, value]) => `${key}: ${value}`)
    .join(', ')
}

/**
 * 防抖函数
 */
export function debounce<T extends (...args: any[]) => any>(
  func: T,
  wait: number
): (...args: Parameters<T>) => void {
  let timeout: ReturnType<typeof setTimeout> | null = null

  return function (this: any, ...args: Parameters<T>) {
    if (timeout) clearTimeout(timeout)
    timeout = setTimeout(() => {
      func.apply(this, args)
    }, wait)
  }
}

/**
 * 节流函数
 */
export function throttle<T extends (...args: any[]) => any>(
  func: T,
  wait: number
): (...args: Parameters<T>) => void {
  let inThrottle = false

  return function (this: any, ...args: Parameters<T>) {
    if (!inThrottle) {
      func.apply(this, args)
      inThrottle = true
      setTimeout(() => {
        inThrottle = false
      }, wait)
    }
  }
}

/**
 * 将字节数组转换为16进制字符串
 * @param bytes 字节数组或 Uint8Array
 * @param separator 分隔符，默认为空格
 * @returns 16进制字符串，如 "7B 22 74 65 6D 70"
 */
export function bytesToHex(bytes: number[] | Uint8Array | null | undefined, separator: string = ' '): string {
  if (!bytes || bytes.length === 0) return '(空)'

  return Array.from(bytes)
    .map(byte => byte.toString(16).toUpperCase().padStart(2, '0'))
    .join(separator)
}

/**
 * 将对象数据转换为16进制显示字符串
 * @param data 任意数据对象
 * @returns 16进制字符串
 */
export function dataToHexString(data: any): string {
  if (!data) return '(空)'

  try {
    const jsonStr = typeof data === 'string' ? data : JSON.stringify(data)
    const encoder = new TextEncoder()
    const bytes = encoder.encode(jsonStr)
    return bytesToHex(bytes)
  } catch (error) {
    return '(转换失败)'
  }
}

/**
 * 格式化数据为明文显示
 * @param data 数据对象
 * @param deviceType 可选的设备类型，用于智能格式化（如 'bmp280'）
 * @returns 格式化后的字符串
 */
export function formatDataPlainText(data: any, deviceType?: string): string {
  if (!data) return '(空)'

  if (typeof data === 'object') {
    // BMP280 类型设备：只显示有意义的数据字段，添加单位
    if (deviceType === 'bmp280') {
      const bmp280Fields: Record<string, { unit: string, filter?: (v: any) => boolean }> = {
        temperature: { unit: '°C' },
        pressure: { unit: 'hPa' },
      }

      return Object.entries(data)
        .filter(([key]) => {
          // 过滤掉原始字节字段
          if (['calibration', 'measurement', 'raw_bmp280', 'raw'].includes(key)) {
            return false
          }
          // 只显示已知的数据字段
          return key in bmp280Fields || typeof data[key] === 'number'
        })
        .map(([key, value]) => {
          if (typeof value === 'number') {
            const field = bmp280Fields[key]
            if (field) {
              return `${key}: ${value.toFixed(2)} ${field.unit}`
            }
            return `${key}: ${value.toFixed(2)}`
          }
          return `${key}: ${value}`
        })
        .join('\n')
    }

    // 其他设备：显示所有数值字段
    return Object.entries(data)
      .filter(([key]) => {
        // 过滤掉原始字节字段
        if (['calibration', 'measurement', 'raw_bmp280', 'raw'].includes(key)) {
          return false
        }
        return true
      })
      .map(([key, value]) => {
        if (typeof value === 'number') {
          return `${key}: ${value.toFixed(2)}`
        }
        return `${key}: ${value}`
      })
      .join('\n')
  }

  return String(data)
}

/**
 * 格式化数据为指定模式显示
 * @param data 数据对象
 * @param mode 显示模式：'text' 明文，'hex' 16进制
 * @param deviceType 可选的设备类型，用于智能格式化
 * @returns 格式化后的字符串
 */
export function formatDataDisplay(data: any, mode: 'text' | 'hex' = 'text', deviceType?: string): string {
  if (mode === 'hex') {
    return dataToHexString(data)
  }
  return formatDataPlainText(data, deviceType)
}

/**
 * 指标未知态统一占位符。
 *
 * 语义（规范 §3.2.5「不得以本地默认值伪造事实」）：
 *   **0 是「确实是 0」，「—」是「接口没说」**。
 * 所以在「接口是否已成功返回」未知（加载中 / 加载失败 / 字段缺失）时，
 * 必须用「—」表示未知，绝不能用 value || 0 把缺失数据渲染成 0。
 *
 * @param value 接口返回的数值；undefined / null 表示接口未提供该字段
 * @param known 该数值是否已经可信；传 false（加载失败或尚未加载）时一律返回「—」
 * @returns 可信时返回原数值，否则返回「—」
 */
export function metricOrDash(value: number | null | undefined, known = true): number | typeof UNKNOWN {
  return known ? (value ?? UNKNOWN) : UNKNOWN
}

/**
 * Format power value: convert to kW if >= 1000W
 */
export function formatPower(w: number): string {
  if (!w || w === 0) return '0W'
  if (Math.abs(w) >= 1000) return `${(w / 1000).toFixed(2)}kW`
  return `${w.toFixed(0)}W`
}
