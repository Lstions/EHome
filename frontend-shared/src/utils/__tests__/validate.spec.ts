import { describe, it, expect } from 'vitest'
import {
  validateUsername,
  validatePassword,
  validateDeviceName,
  validateIP,
  validatePort,
  validateEmail,
  validateModbusAddress,
  validateBaudrate,
} from '../validate'

// ── validateUsername ────────────────────────────

describe('validateUsername', () => {
  it('accepts non-empty string', () => {
    expect(validateUsername('admin')).toBe(true)
    expect(validateUsername('user1')).toBe(true)
  })

  it('rejects empty string', () => {
    expect(validateUsername('')).toBe(false)
    expect(validateUsername('   ')).toBe(false)
  })

  it('rejects non-string types', () => {
    expect(validateUsername(null)).toBe(false)
    expect(validateUsername(undefined)).toBe(false)
    expect(validateUsername(123)).toBe(false)
  })
})

// ── validatePassword ────────────────────────────

describe('validatePassword', () => {
  it('accepts password >= 8 chars', () => {
    expect(validatePassword('12345678')).toBe(true)
    expect(validatePassword('password')).toBe(true)
  })

  it('rejects password < 8 chars', () => {
    expect(validatePassword('abc')).toBe(false)
    expect(validatePassword('1234567')).toBe(false)
    expect(validatePassword('')).toBe(false)
  })

  it('rejects non-string types', () => {
    expect(validatePassword(null)).toBe(false)
    expect(validatePassword(123456)).toBe(false)
  })
})

// ── validateDeviceName ──────────────────────────

describe('validateDeviceName', () => {
  it('accepts non-empty string', () => {
    expect(validateDeviceName('BMP280-Sensor')).toBe(true)
    expect(validateDeviceName('Node-01')).toBe(true)
  })

  it('rejects empty/whitespace-only', () => {
    expect(validateDeviceName('')).toBe(false)
    expect(validateDeviceName('   ')).toBe(false)
  })

  it('rejects non-string', () => {
    expect(validateDeviceName(null)).toBe(false)
    expect(validateDeviceName(42)).toBe(false)
  })
})

// ── validateIP ──────────────────────────────────

describe('validateIP', () => {
  it('accepts valid IPv4 addresses', () => {
    expect(validateIP('192.168.1.1')).toBe(true)
    expect(validateIP('10.0.0.1')).toBe(true)
    expect(validateIP('255.255.255.255')).toBe(true)
    expect(validateIP('0.0.0.0')).toBe(true)
    expect(validateIP('127.0.0.1')).toBe(true)
  })

  it('rejects invalid IPv4 addresses', () => {
    expect(validateIP('256.1.1.1')).toBe(false)
    expect(validateIP('192.168.1')).toBe(false)
    expect(validateIP('192.168.1.1.1')).toBe(false)
    expect(validateIP('abc.def.ghi.jkl')).toBe(false)
    expect(validateIP('')).toBe(false)
  })

  it('rejects non-string', () => {
    expect(validateIP(null)).toBe(false)
    expect(validateIP(123)).toBe(false)
  })
})

// ── validatePort ────────────────────────────────

describe('validatePort', () => {
  it('accepts valid port numbers', () => {
    expect(validatePort(1)).toBe(true)
    expect(validatePort(8080)).toBe(true)
    expect(validatePort(65535)).toBe(true)
  })

  it('rejects invalid port numbers', () => {
    expect(validatePort(0)).toBe(false)
    expect(validatePort(-1)).toBe(false)
    expect(validatePort(65536)).toBe(false)
  })

  it('rejects non-number types', () => {
    expect(validatePort('8080')).toBe(false)
    expect(validatePort(null)).toBe(false)
    expect(validatePort(undefined)).toBe(false)
  })
})

// ── validateEmail ───────────────────────────────

describe('validateEmail', () => {
  it('accepts valid email addresses', () => {
    expect(validateEmail('user@example.com')).toBe(true)
    expect(validateEmail('test.user@domain.org')).toBe(true)
    expect(validateEmail('a@b.co')).toBe(true)
  })

  it('rejects invalid email addresses', () => {
    expect(validateEmail('not-an-email')).toBe(false)
    expect(validateEmail('@domain.com')).toBe(false)
    expect(validateEmail('user@')).toBe(false)
    expect(validateEmail('user@domain')).toBe(false)
    expect(validateEmail('')).toBe(false)
  })

  it('rejects non-string', () => {
    expect(validateEmail(null)).toBe(false)
    expect(validateEmail(123)).toBe(false)
  })
})

// ── validateModbusAddress ───────────────────────

describe('validateModbusAddress', () => {
  it('accepts valid Modbus addresses (1-247)', () => {
    expect(validateModbusAddress(1)).toBe(true)
    expect(validateModbusAddress(247)).toBe(true)
    expect(validateModbusAddress(100)).toBe(true)
  })

  it('rejects out-of-range addresses', () => {
    expect(validateModbusAddress(0)).toBe(false)
    expect(validateModbusAddress(248)).toBe(false)
    expect(validateModbusAddress(300)).toBe(false)
    expect(validateModbusAddress(-1)).toBe(false)
  })

  it('rejects non-integers', () => {
    expect(validateModbusAddress(1.5)).toBe(false)
    expect(validateModbusAddress('1')).toBe(false)
    expect(validateModbusAddress(null)).toBe(false)
  })
})

// ── validateBaudrate ────────────────────────────

describe('validateBaudrate', () => {
  it('accepts standard baud rates', () => {
    expect(validateBaudrate(9600)).toBe(true)
    expect(validateBaudrate(115200)).toBe(true)
    expect(validateBaudrate(300)).toBe(true)
    expect(validateBaudrate(4800)).toBe(true)
  })

  it('rejects non-standard rates', () => {
    expect(validateBaudrate(5000)).toBe(false)
    expect(validateBaudrate(100000)).toBe(false)
    expect(validateBaudrate(0)).toBe(false)
  })

  it('rejects non-number types', () => {
    expect(validateBaudrate('9600')).toBe(false)
    expect(validateBaudrate(null)).toBe(false)
  })
})
