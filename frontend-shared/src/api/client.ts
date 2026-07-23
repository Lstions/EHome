import axios, { type AxiosInstance, type AxiosError } from 'axios'
import { clearSessionCaches } from '@/utils/sessionCache'

export class ApiError extends Error {
  constructor(
    message: string,
    public readonly status?: number,
    public readonly code?: number | string,
    public readonly retryAfterSeconds?: number,
  ) {
    super(message)
    this.name = 'ApiError'
  }
}

const hasAuthorizationHeader = (error: AxiosError): boolean => {
  const headers = error.config?.headers as Record<string, unknown> | undefined
  return Boolean(headers?.Authorization || headers?.authorization)
}

const isLoginRequest = (error: AxiosError): boolean =>
  error.config?.url?.includes('/api/v1/auth/login') === true

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
    const data = response.data as { code?: number; message?: string; data?: unknown }
    // 检查业务状态码：2xx 放行，4xx/5xx 拦截为业务错误
    if (data.code && data.code >= 400) {
      return Promise.reject(new ApiError(data.message || '请求失败', undefined, data.code))
    }
    return response.data
  },
  (error: AxiosError) => {
    // 登录接口返回 401 表示凭据错误，不能按“已有会话失效”处理，否则
    // 登录失败页面会被重定向，用户看不到真正的失败反馈。
    if (error.response?.status === 401 && !isLoginRequest(error) && hasAuthorizationHeader(error)) {
      // Token过期，清除并跳转登录
      clearSessionCaches()
      localStorage.removeItem('token')
      sessionStorage.removeItem('token')
      window.location.href = '/login'
    }
    const errorData = error.response?.data as { code?: number | string; message?: string } | undefined
    const retryAfterHeader = error.response?.headers?.['retry-after']
      ?? (error.response?.headers as any)?.get?.('retry-after')
    const retryAfterSeconds = Number(retryAfterHeader)
    return Promise.reject(new ApiError(
      errorData?.message || error.message,
      error.response?.status,
      errorData?.code,
      Number.isFinite(retryAfterSeconds) && retryAfterSeconds > 0 ? retryAfterSeconds : undefined,
    ))
  }
)

export default apiClient
