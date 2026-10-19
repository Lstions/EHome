import { defineStore } from 'pinia'
import { authApi } from '@/api/auth'
import { clearAttempts, recordFailedAttempt } from '@/utils/loginLockout'
import { clearSessionCaches } from '@/utils/sessionCache'
import { persistLoginPreference } from '@/utils/loginPreference'

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
      // token 落哪个 storage 决定「关浏览器后是否免登录」：
      //   rememberMe=true  → localStorage  （持久，下次打开直接进系统）
      //   rememberMe=false → sessionStorage（随标签页关闭而失效）
      // 后端同时按 rememberMe 签发 7d / 24h TTL，两侧语义必须一致。
      const storage = rememberMe ? localStorage : sessionStorage
      storage.setItem('token', response.token)
      storage.setItem('user', JSON.stringify(response.user))
      const other = rememberMe ? sessionStorage : localStorage
      other.removeItem('token')
      other.removeItem('user')
      // 表单意愿（勾选 + 用户名）必须在**登录成功后**才落盘：
      // 登录失败时不该把用户输错的名字记住，也不该在凭据错误时留下"已记住"的假象。
      persistLoginPreference(rememberMe, username)
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
        // ⚠ 刻意**不**清除 login_remember_me / login_remembered_username：
        // 「主动登出」表达的是"我现在不想保持登录"，而不是"以后别再记住我"。
        // 若在这里清掉，用户每次登出后都要重新勾选、重新输用户名 —— 这正是
        // 用户报「记住我根本没记住」的直接原因之一。只有明文取消勾选才清除。
      }
    },
  },
})
