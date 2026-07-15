import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useUserStore } from '../user'

const { mockClearSessionCaches } = vi.hoisted(() => ({
  mockClearSessionCaches: vi.fn(),
}))

vi.mock('@/utils/sessionCache', () => ({ clearSessionCaches: mockClearSessionCaches }))

// Mock auth API
vi.mock('@/api/auth', () => ({
  authApi: {
    login: vi.fn(),
    logout: vi.fn(),
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
        user: { id: 1, username: 'admin', email: 'a@x.com' },
      } as any)
      const store = useUserStore()
      await store.login('admin', 'password123', false)
      expect(store.isLoggedIn).toBe(true)
      expect(store.token).toBe('jwt-abc')
      expect(store.userInfo?.username).toBe('admin')
      expect(sessionStorage.getItem('token')).toBe('jwt-abc')
    })

    it('rememberMe=true 时存到 localStorage', async () => {
      vi.mocked(authApi.login).mockResolvedValue({
        token: 'jwt-persist',
        user: { id: 2, username: 'u', email: 'u@x.com' },
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
    it('先撤销服务端会话，再清空所有状态和存储', async () => {
      vi.mocked(authApi.logout).mockResolvedValue()
      localStorage.setItem('token', 'x')
      sessionStorage.setItem('token', 'y')
      setActivePinia(createPinia())
      const store = useUserStore()
      await store.logout()
      expect(authApi.logout).toHaveBeenCalledOnce()
      expect(store.token).toBe('')
      expect(store.userInfo).toBeNull()
      expect(store.isLoggedIn).toBe(false)
      expect(localStorage.getItem('token')).toBeNull()
      expect(sessionStorage.getItem('token')).toBeNull()
      expect(mockClearSessionCaches).toHaveBeenCalledOnce()
    })

    it('服务端撤销失败时仍清理本地会话', async () => {
      vi.mocked(authApi.logout).mockRejectedValue(new Error('network'))
      localStorage.setItem('token', 'x')
      const store = useUserStore()
      await store.logout()
      expect(store.isLoggedIn).toBe(false)
      expect(localStorage.getItem('token')).toBeNull()
    })
  })
})
