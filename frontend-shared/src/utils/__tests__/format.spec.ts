import { describe, it, expect } from 'vitest'
import { formatTime, formatFileSize, formatNumber, formatObjectData, bytesToHex, formatPower, debounce, throttle } from '../format'

// ── formatTime ──────────────────────────────────

describe('formatTime', () => {
  it('returns "-" for null/undefined/empty', () => {
    expect(formatTime(null)).toBe('-')
    expect(formatTime(undefined)).toBe('-')
    expect(formatTime('')).toBe('-')
  })

  it('returns "-" for epoch zero dates', () => {
    expect(formatTime('0001-01-01T00:00:00Z')).toBe('-')
    expect(formatTime('1970-01-01T00:00:00Z')).toBe('-')
  })

  it('returns "-" for invalid date string', () => {
    expect(formatTime('not-a-date')).toBe('-')
  })

  it('formats valid ISO date string', () => {
    const result = formatTime('2024-06-15T10:30:00Z')
    expect(result).not.toBe('-')
    expect(result).toContain('2024')
  })

  it('formats Date object', () => {
    const result = formatTime(new Date('2024-06-15T10:30:00Z'))
    expect(result).not.toBe('-')
    expect(result).toContain('2024')
  })
})

// ── formatFileSize ──────────────────────────────

describe('formatFileSize', () => {
  it('returns "0 B" for 0', () => {
    expect(formatFileSize(0)).toBe('0 B')
  })

  it('formats bytes', () => {
    expect(formatFileSize(512)).toBe('512 B')
  })

  it('formats KB', () => {
    expect(formatFileSize(1024)).toBe('1 KB')
    expect(formatFileSize(1536)).toBe('1.5 KB')
  })

  it('formats MB', () => {
    expect(formatFileSize(1048576)).toBe('1 MB')
  })

  it('formats GB', () => {
    expect(formatFileSize(1073741824)).toBe('1 GB')
  })
})

// ── formatNumber ────────────────────────────────

describe('formatNumber', () => {
  it('formats with default 2 decimals', () => {
    expect(formatNumber(3.14159)).toBe('3.14')
  })

  it('formats with custom decimals', () => {
    expect(formatNumber(3.14159, 4)).toBe('3.1416')
    expect(formatNumber(3.14159, 0)).toBe('3')
  })
})

// ── formatObjectData ────────────────────────────

describe('formatObjectData', () => {
  it('returns "-" for null', () => {
    expect(formatObjectData(null as any)).toBe('-')
  })

  it('formats simple object', () => {
    const result = formatObjectData({ temp: 25.5, humidity: 60 })
    expect(result).toContain('temp: 25.5')
    expect(result).toContain('humidity: 60')
  })
})

// ── bytesToHex ──────────────────────────────────

describe('bytesToHex', () => {
  it('returns "(空)" for empty/null', () => {
    expect(bytesToHex([])).toBe('(空)')
    expect(bytesToHex(null)).toBe('(空)')
    expect(bytesToHex(undefined)).toBe('(空)')
  })

  it('formats bytes with default space separator', () => {
    expect(bytesToHex([0x7B, 0x22, 0x74])).toBe('7B 22 74')
  })

  it('formats bytes with custom separator', () => {
    expect(bytesToHex([0xFF, 0xAA], ':')).toBe('FF:AA')
  })

  it('pads single-digit hex', () => {
    expect(bytesToHex([0x1, 0xA])).toBe('01 0A')
  })
})

// ── formatPower ─────────────────────────────────

describe('formatPower', () => {
  it('returns "0W" for 0', () => {
    expect(formatPower(0)).toBe('0W')
  })

  it('formats watts < 1000', () => {
    expect(formatPower(500)).toBe('500W')
    expect(formatPower(999)).toBe('999W')
  })

  it('formats kW for >= 1000', () => {
    expect(formatPower(1000)).toBe('1.00kW')
    expect(formatPower(1500)).toBe('1.50kW')
  })

  it('handles negative power', () => {
    expect(formatPower(-500)).toBe('-500W')
    expect(formatPower(-1500)).toBe('-1.50kW')
  })
})

// ── debounce ────────────────────────────────────

describe('debounce', () => {
  it('calls function after wait period', async () => {
    let called = 0
    const fn = debounce(() => { called++ }, 50)
    fn()
    expect(called).toBe(0)
    await new Promise(r => setTimeout(r, 60))
    expect(called).toBe(1)
  })

  it('only calls once for rapid successive calls', async () => {
    let called = 0
    const fn = debounce(() => { called++ }, 50)
    fn()
    fn()
    fn()
    await new Promise(r => setTimeout(r, 60))
    expect(called).toBe(1)
  })
})

// ── throttle ────────────────────────────────────

describe('throttle', () => {
  it('calls function immediately on first call', () => {
    let called = 0
    const fn = throttle(() => { called++ }, 50)
    fn()
    expect(called).toBe(1)
  })

  it('suppresses calls within throttle period', () => {
    let called = 0
    const fn = throttle(() => { called++ }, 100)
    fn() // called=1
    fn() // suppressed
    fn() // suppressed
    expect(called).toBe(1)
  })
})
