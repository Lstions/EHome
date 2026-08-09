import { defineStore } from 'pinia'
import { authApi } from '@/api/auth'
import { clearAttempts, recordFailedAttempt } from '@/utils/loginLockout'
import { clearSessionCaches } from '@/utils/sessionCache'

const getStoredToken = (): string => localStorage.getItem('token') || sessionStorage.getItem('token') || ''

const getStoredUser = (): UserInfo | null => {
  try {
    const raw = localStorage.getItem('user') || sessionStorage.getItem('user')
    return raw ? JSON.parse(raw) : null
  } catch {
    return null
  }
}

export interface UserInfo {
  id: number
  username: string
  email: string
  enabled?: boolean
}

export const useUserStore = defineStore('user', {
  state: () => {
    const token = getStoredToken()
    return { token, userInfo: getStoredUser(), isLoggedIn: !!token }
  },
  actions: {
    async login(username: string, password: string, rememberMe = false) {
      const response = await authApi.login({ username, password, rememberMe })
      this.token = response.token
      this.userInfo = response.user as UserInfo
      this.isLoggedIn = true
      const storage = rememberMe ? localStorage : sessionStorage
      storage.setItem('token', response.token)
      storage.setItem('user', JSON.stringify(response.user))
      const other = rememberMe ? sessionStorage : localStorage
      other.removeItem('token')
      other.removeItem('user')
      clearAttempts()
    },
    async refreshAccount() {
      this.userInfo = await authApi.account()
      const storage = localStorage.getItem('token') ? localStorage : sessionStorage
      storage.setItem('user', JSON.stringify(this.userInfo))
    },
    recordLoginFailure() {
      return recordFailedAttempt()
    },
    async logout() {
      try {
        if (this.token) await authApi.logout()
      } catch {
        // Network/server failures must not trap the browser in a stale local session.
      } finally {
        clearSessionCaches()
        this.token = ''
        this.userInfo = null
        this.isLoggedIn = false
        localStorage.removeItem('token')
        sessionStorage.removeItem('token')
        localStorage.removeItem('user')
        sessionStorage.removeItem('user')
      }
    },
  },
})
