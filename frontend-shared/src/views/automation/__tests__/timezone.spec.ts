import { describe, it, expect, afterEach, vi } from 'vitest'
import {
  browserTimeZone, buildTimeZoneOptions, isValidTimeZone, listTimeZones, timeZoneLabel,
} from '../timezone'

/**
 * time_window 时区词表（2026-11-18）。
 *
 * 这些用例钉的是**能力检测与降级**：Intl.supportedValuesOf 不存在 / 抛异常时，
 * 表单必须仍能渲染出一个可用的时区下拉（而不是白屏）—— 这是本模块最容易在
 * 旧内核 / jsdom / 被裁剪的 WebView 上回归的一条路径。
 */

/** 在「没有 supportedValuesOf」的环境下执行；无论断言成败都恢复原属性。 */
function withoutSupportedValuesOf<T>(run: () => T, mode: 'missing' | 'throwing' = 'missing'): T {
  const descriptor = Object.getOwnPropertyDescriptor(Intl, 'supportedValuesOf')
  Object.defineProperty(Intl, 'supportedValuesOf', {
    configurable: true,
    writable: true,
    value: mode === 'missing' ? undefined : () => { throw new RangeError('unsupported') },
  })
  try {
    return run()
  } finally {
    if (descriptor) Object.defineProperty(Intl, 'supportedValuesOf', descriptor)
    else Reflect.deleteProperty(Intl, 'supportedValuesOf')
  }
}

afterEach(() => { vi.restoreAllMocks() })

describe('时区词表', () => {
  it('优先使用 Intl.supportedValuesOf（不是手写十几个时区）', () => {
    const zones = listTimeZones()
    expect(zones.length).toBeGreaterThan(100)
    expect(zones).toContain('Asia/Shanghai')
    // 枚举表本身不含 UTC（Node 24 实测），必须手动补上：它是「服务器/容器时区」的选择项
    expect(zones).toContain('UTC')
  })

  it('没有 supportedValuesOf 时回退到常见时区列表，而不是抛错', () => {
    const zones = withoutSupportedValuesOf(() => listTimeZones())
    expect(zones.length).toBeGreaterThan(10)
    expect(zones).toContain('UTC')
    expect(zones).toContain('Asia/Shanghai')
    expect(zones).toContain('Europe/London')
  })

  it('supportedValuesOf 抛异常（早期 Safari / polyfill）时同样回退', () => {
    const zones = withoutSupportedValuesOf(() => listTimeZones(), 'throwing')
    expect(zones).toContain('Asia/Shanghai')
  })

  it('时区标签带当前 UTC 偏移，帮助不熟悉 IANA 命名的用户定位', () => {
    // 上海无夏令时，偏移恒为 +08:00（与运行机器时区无关）
    expect(timeZoneLabel('Asia/Shanghai')).toBe('Asia/Shanghai（UTC+08:00）')
    // UTC 自身不再重复写一遍 UTC+00:00
    expect(timeZoneLabel('UTC')).toBe('UTC')
  })

  it('当前选中值即使不在词表里也作为选项追加（存量数据不能变成下拉里找不到的选中项）', () => {
    const options = buildTimeZoneOptions('Legacy/Nowhere')
    expect(options.map(option => option.value)).toContain('Legacy/Nowhere')
    // 已在词表里的值不重复追加
    const shanghai = options.filter(option => option.value === 'Asia/Shanghai')
    expect(shanghai).toHaveLength(1)
  })
})

describe('浏览器时区', () => {
  it('返回一个 Intl 能解析的 IANA 时区', () => {
    const zone = browserTimeZone()
    expect(zone.trim()).not.toBe('')
    expect(isValidTimeZone(zone)).toBe(true)
  })

  it('读不到浏览器时区（被裁剪的 Intl）时退回 UTC，不阻止用户配置规则', () => {
    vi.spyOn(Intl, 'DateTimeFormat').mockImplementation(() => {
      throw new Error('Intl.DateTimeFormat is not available')
    })
    expect(browserTimeZone()).toBe('UTC')
  })
})

describe('时区合法性', () => {
  it('合法 IANA 名通过', () => {
    expect(isValidTimeZone('Asia/Shanghai')).toBe(true)
    expect(isValidTimeZone('UTC')).toBe(true)
    expect(isValidTimeZone('  America/New_York  ')).toBe(true)
  })

  it('拼错 / 空白 / 空串一律判非法（保存前拦住，避免规则永不按预期生效）', () => {
    expect(isValidTimeZone('Asia/Shangai')).toBe(false)
    expect(isValidTimeZone('')).toBe(false)
    expect(isValidTimeZone('   ')).toBe(false)
  })

  it('固定偏移写法（+08:00）判非法：Intl 能解析，但后端 time.LoadLocation 不认', () => {
    // 反证：Node 24 的 Intl.DateTimeFormat 接受 ES2024 偏移标识符，若只靠它做校验，
    // 用户手改的 "+08:00" 会通过前端校验、在保存时被后端 fail-closed 拒绝。
    expect(() => new Intl.DateTimeFormat('en-US', { timeZone: '+08:00' })).not.toThrow()
    expect(isValidTimeZone('+08:00')).toBe(false)
  })
})
