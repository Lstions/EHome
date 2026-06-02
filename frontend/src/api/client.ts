import axios, { type AxiosInstance, type AxiosError } from 'axios'

// 创建 Axios 实例
const apiClient: AxiosInstance = axios.create({
  baseURL: import.meta.env.VITE_API_BASE_URL || '/api',
  timeout: 10000,
  headers: {
    'Content-Type': 'application/json'
  }
})

// 请求拦截器 - 添加JWT Token
apiClient.interceptors.request.use(
  (config) => {
    const token = localStorage.getItem('token') || sessionStorage.getItem('token')
    if (token) {
      config.headers.Authorization = `Bearer ${token}`
    }
    return config
  },
  (error) => {
    return Promise.reject(error)
  }
)

// 响应拦截器 - 统一错误处理
apiClient.interceptors.response.use(
  (response) => {
    const data = response.data as { code?: number; message?: string; data?: unknown }
    // 检查业务状态码
    if (data.code && data.code !== 200) {
      return Promise.reject(new Error(data.message || '请求失败'))
    }
    return response.data
  },
  (error: AxiosError) => {
    if (error.response?.status === 401) {
      // Token过期，清除并跳转登录
      localStorage.removeItem('token')
      sessionStorage.removeItem('token')
      window.location.href = '/login'
    }
    const errorData = error.response?.data as { message?: string } | undefined
    return Promise.reject(new Error(errorData?.message || error.message))
  }
)

export default apiClient
