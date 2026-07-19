import axios, { type AxiosInstance, type AxiosError, type AxiosResponse } from 'axios'
import { clearSessionCaches } from '@/utils/sessionCache'

interface ErrorEnvelope {
  code?: number | string
  message?: string
  error_code?: string
}

export class ApiError extends Error {
  readonly response?: AxiosResponse
  readonly status?: number
  readonly errorCode?: string

  constructor(message: string, response?: AxiosResponse, errorCode?: string) {
    super(message)
    this.name = 'ApiError'
    this.response = response
    this.status = response?.status
    this.errorCode = errorCode
  }
}

export function isApiErrorCode(error: unknown, errorCode: string): boolean {
  return error instanceof ApiError && error.errorCode === errorCode
}

// 创建 Axios 实例
const apiClient: AxiosInstance = axios.create({
  baseURL: import.meta.env.VITE_API_BASE_URL || '',
  timeout: 10000,
  // 不设置默认 Content-Type，让 axios 根据请求体自动处理
  // (FormData 需要浏览器自动生成 multipart/form-data + boundary)
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
    const data = response.data as ErrorEnvelope & { data?: unknown }
    // 检查业务状态码：2xx 放行，4xx/5xx 拦截为业务错误
    if (typeof data.code === 'number' && data.code >= 400) {
      return Promise.reject(new ApiError(data.message || '请求失败', response, data.error_code))
    }
    return response.data
  },
  (error: AxiosError) => {
    if (error.response?.status === 401) {
      // Token过期，清除并跳转登录
      clearSessionCaches()
      localStorage.removeItem('token')
      sessionStorage.removeItem('token')
      window.location.href = '/login'
    }
    const errorData = error.response?.data as ErrorEnvelope | undefined
    return Promise.reject(new ApiError(errorData?.message || error.message, error.response, errorData?.error_code))
  }
)

export default apiClient
