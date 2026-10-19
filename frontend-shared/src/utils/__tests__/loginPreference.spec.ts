import { describe, it, expect, beforeEach, vi } from 'vitest'
import {
  isRemembered,
  getRememberedUsername,
  persistLoginPreference,
  clearLoginPreference,
} from '@/utils/loginPreference'

/**
 * 「记住我」偏好持久化契约（2026-10-20）
 *
 * 用户报障原文：「登录页面记住我的功能……一堆问题」。
 * 实测根因：勾选状态与用户名**从未持久化**，刷新即丢 → 勾了等于没勾。
 * 本文件锁死四条语义，任何一条被改都会当场红：
 *   ① 勾选后能读回（跨"刷新"= 跨 localStorage 生命周期）
 *   ② 用户名一并记住（下次预填，用户只需输密码）
 *   ③ 取消勾选必须**连带清掉用户名**（PII 不能残留）
 *   ④ 存储不可用（隐私模式）时不得抛错 —— 记住我绝不能阻塞登录
 */
describe('登录偏好：记住我', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it('默认：未记住、无用户名', () => {
    expect(isRemembered()).toBe(false)
    expect(getRememberedUsername()).toBe('')
  })

  it('勾选后能读回勾选状态与用户名（相当于刷新页面后）', () => {
    persistLoginPreference(true, 'lstions')
    expect(isRemembered()).toBe(true)
    expect(getRememberedUsername()).toBe('lstions')
  })

  it('取消勾选时连带清除用户名（不残留 PII）', () => {
    persistLoginPreference(true, 'lstions')
    expect(getRememberedUsername()).toBe('lstions')

    persistLoginPreference(false)
    expect(isRemembered()).toBe(false)
    expect(getRememberedUsername()).toBe('')
    expect(localStorage.getItem('login_remember_me')).toBeNull()
    expect(localStorage.getItem('login_remembered_username')).toBeNull()
  })

  it('勾选但未传用户名：保留勾选、不改动既有用户名', () => {
    persistLoginPreference(true, 'alice')
    persistLoginPreference(true)
    expect(isRemembered()).toBe(true)
    expect(getRememberedUsername()).toBe('alice')
  })

  it('clearLoginPreference 能单独清除', () => {
    persistLoginPreference(true, 'bob')
    clearLoginPreference()
    expect(isRemembered()).toBe(false)
    expect(getRememberedUsername()).toBe('')
  })

  /**
   * 存储不可用（Safari 无痕 / 配额耗尽 / 被策略禁用）时，读写都必须静默降级。
   *
   * ⚠ 为什么用 vi.stubGlobal 整块替换 localStorage，而不是 spyOn(Storage.prototype)：
   *   vitest 在同一文件内**复用同一个模块实例**，模块内的 `localStorage.setItem`
   *   在首次调用时就被绑定；对原型打桩对后续用例无效（实测会假绿）。
   *   整体替换全局对象才能真实模拟"存储不可用"。
   */
  it('存储不可用（读写都抛错）时不崩溃、不写入', () => {
    localStorage.clear()
    vi.stubGlobal('localStorage', {
      getItem: () => { throw new Error('SecurityError') },
      setItem: () => { throw new Error('QuotaExceededError') },
      removeItem: () => { throw new Error('SecurityError') },
    })
    try {
      expect(() => persistLoginPreference(true, 'x')).not.toThrow()
      expect(() => isRemembered()).not.toThrow()
      expect(() => getRememberedUsername()).not.toThrow()
      expect(() => clearLoginPreference()).not.toThrow()
      expect(isRemembered()).toBe(false)
      expect(getRememberedUsername()).toBe('')
    } finally {
      vi.unstubAllGlobals()
    }
    // 恢复后仍能正常工作（降级不污染真实存储）
    expect(isRemembered()).toBe(false)
    persistLoginPreference(true, 'ok')
    expect(getRememberedUsername()).toBe('ok')
  })
})
