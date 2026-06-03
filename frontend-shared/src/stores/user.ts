import { defineStore } from 'pinia'
import { authApi } from '@/api/auth'
import { clearAttempts, recordFailedAttempt } from '@/utils/loginLockout'

// Helper to get/set token with storage preference
const getStoredToken = (): string => {
  return localStorage.getItem('token') || sessionStorage.getItem('token') || ''
}

export type UserRole = 'admin' | 'operator' | 'viewer'

export interface UserInfo {
  id: number
  username: string
  email: string
  role: UserRole
}

/** 菜单项所需的最小角色等级（越大越受限） */
const ROLE_LEVEL: Record<UserRole, number> = {
  admin: 0,
  operator: 1,
  viewer: 2,
}

export const useUserStore = defineStore('user', {
  state: () => ({
    token: getStoredToken(),
    userInfo: null as UserInfo | null,
    isLoggedIn: !!getStoredToken(),
  }),

  getters: {
    role: (state): UserRole => state.userInfo?.role ?? 'viewer',
    isAdmin: (state): boolean => state.userInfo?.role === 'admin',
    isOperator: (state): boolean => (state.userInfo?.role ?? '') === 'operator' || (state.userInfo?.role ?? '') === 'admin',
    isViewer: (state): boolean => state.userInfo?.role === 'viewer',
    /** 角色等级数字，越小权限越高 */
    roleLevel: (state): number => ROLE_LEVEL[state.userInfo?.role ?? 'viewer'],
    /**
     * 判断当前用户是否拥有指定角色（含更高级角色）
     * 例: hasRole('operator') → admin 和 operator 都返回 true
     * 未登录时一律返回 false
     */
    hasRole(): (role: UserRole) => boolean {
      return (role: UserRole) => {
        if (!this.userInfo) return false
        return this.roleLevel <= ROLE_LEVEL[role]
      }
    },
  },

  actions: {
    async login(username: string, password: string, rememberMe: boolean = false) {
      const response = await authApi.login({ username, password })
      this.token = response.token
      this.userInfo = response.user as UserInfo
      this.isLoggedIn = true
      if (rememberMe) {
        localStorage.setItem('token', response.token)
      } else {
        sessionStorage.setItem('token', response.token)
      }
      // 登录成功，清零失败计数
      clearAttempts()
    },

    /** 记录一次登录失败 */
    recordLoginFailure() {
      return recordFailedAttempt()
    },

    logout() {
      this.token = ''
      this.userInfo = null
      this.isLoggedIn = false
      localStorage.removeItem('token')
      sessionStorage.removeItem('token')
    },
  },
})
