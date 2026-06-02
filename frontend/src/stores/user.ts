import { defineStore } from 'pinia'
import { authApi } from '@/api/auth'

// Helper to get/set token with storage preference
const getStoredToken = (): string => {
  return localStorage.getItem('token') || sessionStorage.getItem('token') || ''
}

export const useUserStore = defineStore('user', {
  state: () => ({
    token: getStoredToken(),
    userInfo: null as {id: number, username: string, email: string, role: string} | null,
    isLoggedIn: !!getStoredToken()
  }),

  actions: {
    async login(username: string, password: string, rememberMe: boolean = false) {
      const response = await authApi.login({ username, password })
      // authApi.login 返回的是 ApiResponse.data，即 LoginResponse
      this.token = response.token
      this.userInfo = response.user
      this.isLoggedIn = true
      if (rememberMe) {
        localStorage.setItem('token', response.token)
      } else {
        sessionStorage.setItem('token', response.token)
      }
      console.log('Login success, userInfo:', this.userInfo, 'rememberMe:', rememberMe)
    },

    logout() {
      this.token = ''
      this.userInfo = null
      this.isLoggedIn = false
      localStorage.removeItem('token')
      sessionStorage.removeItem('token')
    }
  }
})
