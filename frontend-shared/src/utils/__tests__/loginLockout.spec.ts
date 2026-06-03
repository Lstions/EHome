import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import {
  getLockout,
  getRemainingLockSeconds,
  getAttempts,
  recordFailedAttempt,
  clearAttempts,
  initLoginLockout,
} from '../loginLockout'

describe('loginLockout', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
    localStorage.clear()
  })

  it('初始无锁定记录', () => {
    expect(getLockout()).toBeNull()
    expect(getRemainingLockSeconds()).toBe(0)
    expect(getAttempts()).toBe(0)
  })

  it('第 1-4 次失败不锁定，仅累加 attempts', () => {
    for (let i = 1; i <= 4; i++) {
      const result = recordFailedAttempt()
      expect(result.locked).toBe(false)
      expect(result.attempts).toBe(i)
      expect(result.remainingSeconds).toBe(0)
    }
    expect(getLockout()).toBeNull()
    expect(getAttempts()).toBe(4)
  })

  it('第 5 次失败触发锁定 300 秒', () => {
    for (let i = 1; i <= 4; i++) {
      recordFailedAttempt()
    }
    const result = recordFailedAttempt()
    expect(result.locked).toBe(true)
    expect(result.attempts).toBe(5)
    expect(result.remainingSeconds).toBe(300)
    expect(getLockout()).not.toBeNull()
  })

  it('锁定期间 getRemainingLockSeconds 递减', () => {
    for (let i = 1; i <= 5; i++) recordFailedAttempt()
    const start = getRemainingLockSeconds()
    expect(start).toBe(300)

    vi.advanceTimersByTime(60_000) // 1 分钟
    const after1min = getRemainingLockSeconds()
    expect(after1min).toBe(240)

    vi.advanceTimersByTime(60_000) // 又 1 分钟
    expect(getRemainingLockSeconds()).toBe(180)
  })

  it('锁定时间到自动清理', () => {
    for (let i = 1; i <= 5; i++) recordFailedAttempt()
    expect(getLockout()).not.toBeNull()

    vi.advanceTimersByTime(301_000) // 超过 300 秒
    expect(getLockout()).toBeNull()
    expect(getAttempts()).toBe(0) // 一起清掉
  })

  it('clearAttempts 立即清零', () => {
    recordFailedAttempt()
    recordFailedAttempt()
    expect(getAttempts()).toBe(2)
    clearAttempts()
    expect(getAttempts()).toBe(0)
    expect(getLockout()).toBeNull()
  })

  it('initLoginLockout 启动时清理过期锁定', () => {
    localStorage.setItem('login_lockout', JSON.stringify({ lockedUntil: 1, attempts: 99, reason: 'too_many_attempts' }))
    initLoginLockout()
    expect(getLockout()).toBeNull()
  })

  it('localStorage 损坏时容错', () => {
    localStorage.setItem('login_lockout', 'not-json')
    expect(getLockout()).toBeNull()
    expect(getAttempts()).toBe(0)
  })
})
