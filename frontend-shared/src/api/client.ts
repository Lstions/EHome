import axios, { type AxiosInstance, type AxiosError, type AxiosResponse } from 'axios'
import { clearSessionCaches } from '@/utils/sessionCache'
import { loginPath } from '@/utils/basePath'

interface ErrorEnvelope {
  code?: number | string
  message?: string
  error_code?: string
}

/**
 * 后端统一响应 envelope。axios 响应拦截器返回整个 envelope（response.data），
 * 因此各 API 方法以 <unknown, ApiEnvelope<T>> 双泛型取值，只读 envelope.data。
 */
export interface ApiEnvelope<T> {
  code: number
  message: string
  data: T
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
// baseURL：优先 VITE_API_BASE_URL（完整覆盖）；否则跟随 VITE_BASE_PATH 前缀（反代子路径部署）
const apiBaseURL = import.meta.env.VITE_API_BASE_URL
  ? import.meta.env.VITE_API_BASE_URL
  : (import.meta.env.VITE_BASE_PATH ? import.meta.env.VITE_BASE_PATH.replace(/\/+$/, '') : '')
const apiClient: AxiosInstance = axios.create({
  baseURL: apiBaseURL,
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
    const requestURL = error.config?.url || ''
    const isLoginAttempt = requestURL.endsWith('/api/v1/auth/login')
    const hasStoredSession = !!(localStorage.getItem('token') || sessionStorage.getItem('token'))
    if (error.response?.status === 401 && hasStoredSession && !isLoginAttempt) {
      // Only an authenticated request can prove that the current session is
      // invalid. A 401 from the login endpoint is an expected credential
      // failure; reloading /login would erase its inline error and can leave
      // the application blank while the hard navigation is in progress.
      clearSessionCaches()
      localStorage.removeItem('token')
      sessionStorage.removeItem('token')
      // 硬跳转必须带原因。否则用户只会看到"又莫名其妙被踢回登录页"，无法区分
      // 是会话过期（记住我到期 / 服务端吊销 / 改过密码）还是别的问题 ——
      // 这正是「记住我"没生效"」最常见的真实原因：勾了，但那是 24h 的旧 token，
      // 24 小时后就过期了，而页面从未解释过这一点。
      // 登录页据 reason=session_expired 渲染明确文案。
      if (window.location.pathname !== loginPath()) {
        // 纯字符串拼接而不是 new URL(...)：单元测试会替换 window.location（只保留
        // pathname/assign），此时 location.origin 可能不存在，new URL 会抛错。
        const target = loginPath()
        const separator = target.includes('?') ? '&' : '?'
        window.location.assign(target + separator + 'reason=session_expired')
      }
    }
    const errorData = error.response?.data as ErrorEnvelope | undefined
    return Promise.reject(new ApiError(errorData?.message || error.message, error.response, errorData?.error_code))
  }
)

export default apiClient
