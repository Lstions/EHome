/**
 * 登录失败锁定
 *
 * - 默认连续 5 次失败锁定 5 分钟
 * - 锁定期间登录按钮禁用，提示剩余秒数
 * - 锁定时间到自动清零
 * - 锁定信息仅存 localStorage（同一浏览器）
 */

const STORAGE_KEY = 'login_lockout'
const ATTEMPTS_KEY = 'login_attempts'

export interface LockoutState {
  /** 锁定到期时间（毫秒时间戳） */
  lockedUntil: number
  /** 触发本次锁定的连续失败次数 */
  attempts: number
  /** 锁定原因 */
  reason: 'too_many_attempts'
}

export interface AttemptState {
  count: number
  firstAt: number
}

const MAX_ATTEMPTS = Number(import.meta.env.VITE_LOGIN_MAX_ATTEMPTS) || 5
const LOCKOUT_SECONDS = Number(import.meta.env.VITE_LOGIN_LOCKOUT_SECONDS) || 300

export function getLockout(): LockoutState | null {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return null
    const state: LockoutState = JSON.parse(raw)
    if (!state.lockedUntil) return null
    if (Date.now() >= state.lockedUntil) {
      // 已过期
      localStorage.removeItem(STORAGE_KEY)
      localStorage.removeItem(ATTEMPTS_KEY)
      return null
    }
    return state
  } catch {
    return null
  }
}

export function getRemainingLockSeconds(): number {
  const state = getLockout()
  if (!state) return 0
  return Math.max(0, Math.ceil((state.lockedUntil - Date.now()) / 1000))
}

export function getAttempts(): number {
  try {
    const raw = localStorage.getItem(ATTEMPTS_KEY)
    if (!raw) return 0
    const state: AttemptState = JSON.parse(raw)
    return state.count || 0
  } catch {
    return 0
  }
}

export function recordFailedAttempt(): { attempts: number; locked: boolean; remainingSeconds: number } {
  const prev = getAttempts()
  const next = prev + 1
  const now = Date.now()

  const state: AttemptState = { count: next, firstAt: prev === 0 ? now : getFirstAt() }
  localStorage.setItem(ATTEMPTS_KEY, JSON.stringify(state))

  if (next >= MAX_ATTEMPTS) {
    const lockout: LockoutState = {
      lockedUntil: now + LOCKOUT_SECONDS * 1000,
      attempts: next,
      reason: 'too_many_attempts',
    }
    localStorage.setItem(STORAGE_KEY, JSON.stringify(lockout))
    return { attempts: next, locked: true, remainingSeconds: LOCKOUT_SECONDS }
  }
  return { attempts: next, locked: false, remainingSeconds: 0 }
}

export function clearAttempts(): void {
  localStorage.removeItem(ATTEMPTS_KEY)
  localStorage.removeItem(STORAGE_KEY)
}

/** 应用启动时调用：清掉过期锁定 */
export function initLoginLockout(): void {
  getLockout() // 触发清理
}

function getFirstAt(): number {
  try {
    const raw = localStorage.getItem(ATTEMPTS_KEY)
    if (!raw) return Date.now()
    return JSON.parse(raw).firstAt || Date.now()
  } catch {
    return Date.now()
  }
}
