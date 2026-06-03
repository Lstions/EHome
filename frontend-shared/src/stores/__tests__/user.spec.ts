import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useUserStore } from '../user'

// Mock auth API
vi.mock('@/api/auth', () => ({
  authApi: {
    login: vi.fn(),
  },
}))

import { authApi } from '@/api/auth'

describe('user store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    localStorage.clear()
    sessionStorage.clear()
    vi.clearAllMocks()
  })

  afterEach(() => {
    localStorage.clear()
    sessionStorage.clear()
  })

  describe('初始 state', () => {
    it('默认未登录', () => {
      const store = useUserStore()
      expect(store.isLoggedIn).toBe(false)
      expect(store.userInfo).toBeNull()
      expect(store.token).toBe('')
    })

    it('从 localStorage 恢复登录态', () => {
      localStorage.setItem('token', 'restored-token')
      setActivePinia(createPinia())
      const store = useUserStore()
      expect(store.isLoggedIn).toBe(true)
      expect(store.token).toBe('restored-token')
    })
  })

  describe('login action', () => {
    it('登录成功后保存 userInfo + token', async () => {
      vi.mocked(authApi.login).mockResolvedValue({
        token: 'jwt-abc',
        user: { id: 1, username: 'admin', email: 'a@x.com', role: 'admin' },
      } as any)
      const store = useUserStore()
      await store.login('admin', 'password123', false)
      expect(store.isLoggedIn).toBe(true)
      expect(store.token).toBe('jwt-abc')
      expect(store.userInfo?.username).toBe('admin')
      expect(store.userInfo?.role).toBe('admin')
      expect(sessionStorage.getItem('token')).toBe('jwt-abc')
    })

    it('rememberMe=true 时存到 localStorage', async () => {
      vi.mocked(authApi.login).mockResolvedValue({
        token: 'jwt-persist',
        user: { id: 2, username: 'u', email: 'u@x.com', role: 'viewer' },
      } as any)
      const store = useUserStore()
      await store.login('u', 'pass1234', true)
      expect(localStorage.getItem('token')).toBe('jwt-persist')
      expect(sessionStorage.getItem('token')).toBeNull()
    })

    it('登录失败抛出错误', async () => {
      vi.mocked(authApi.login).mockRejectedValue(new Error('401'))
      const store = useUserStore()
      await expect(store.login('u', 'wrong', false)).rejects.toThrow('401')
      expect(store.isLoggedIn).toBe(false)
    })
  })

  describe('logout action', () => {
    it('清空所有状态和存储', () => {
      localStorage.setItem('token', 'x')
      sessionStorage.setItem('token', 'y')
      setActivePinia(createPinia())
      const store = useUserStore()
      store.logout()
      expect(store.token).toBe('')
      expect(store.userInfo).toBeNull()
      expect(store.isLoggedIn).toBe(false)
      expect(localStorage.getItem('token')).toBeNull()
      expect(sessionStorage.getItem('token')).toBeNull()
    })
  })

  describe('角色 getter', () => {
    it('isAdmin 仅在 role=admin 时为 true', () => {
      const store = useUserStore()
      store.userInfo = { id: 1, username: 'u', email: 'u@x.com', role: 'admin' }
      expect(store.isAdmin).toBe(true)
      store.userInfo.role = 'operator'
      expect(store.isAdmin).toBe(false)
    })

    it('isOperator 在 admin 和 operator 时为 true', () => {
      const store = useUserStore()
      store.userInfo = { id: 1, username: 'u', email: 'u@x.com', role: 'admin' }
      expect(store.isOperator).toBe(true)
      store.userInfo.role = 'operator'
      expect(store.isOperator).toBe(true)
      store.userInfo.role = 'viewer'
      expect(store.isOperator).toBe(false)
    })

    it('hasRole 含更高级角色 (admin 同时拥有 operator/viewer 权限)', () => {
      const store = useUserStore()
      store.userInfo = { id: 1, username: 'u', email: 'u@x.com', role: 'admin' }
      expect(store.hasRole('admin')).toBe(true)
      expect(store.hasRole('operator')).toBe(true)
      expect(store.hasRole('viewer')).toBe(true)

      store.userInfo.role = 'operator'
      expect(store.hasRole('admin')).toBe(false)
      expect(store.hasRole('operator')).toBe(true)
      expect(store.hasRole('viewer')).toBe(true)

      store.userInfo.role = 'viewer'
      expect(store.hasRole('admin')).toBe(false)
      expect(store.hasRole('operator')).toBe(false)
      expect(store.hasRole('viewer')).toBe(true)
    })

    it('未登录时所有 hasRole 返回 false', () => {
      const store = useUserStore()
      expect(store.hasRole('admin')).toBe(false)
      expect(store.hasRole('viewer')).toBe(false)
    })
  })
})
