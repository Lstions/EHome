import client from './client'

export interface LoginRequest {
  username: string
  password: string
}

export interface LoginResponse {
  token: string
  user: {
    id: number
    username: string
    email: string
    role: string
  }
}

interface ApiResponse<T> {
  code: number
  message: string
  data: T
}

export const authApi = {
  async login(data: LoginRequest): Promise<LoginResponse> {
    const response = await client.post<unknown, ApiResponse<LoginResponse>>('/api/v1/auth/login', data)
    return response.data
  },

  async logout(): Promise<void> {
    await client.post('/api/v1/auth/logout', {})
  },

  getToken(): string {
    return localStorage.getItem('token') || sessionStorage.getItem('token') || ''
  }
}
