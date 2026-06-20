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

export const authApi = {
  async login(data: LoginRequest): Promise<LoginResponse> {
    // Backend returns envelope: {code, data: {token, user}, message}
    // Interceptor returns response.data = the full envelope,
    // so we must unwrap response.data to get {token, user}
    const response = await client.post<unknown, any>('/api/v1/auth/login', data)
    return (response as any).data as LoginResponse
  },

  async logout(): Promise<void> {
    await client.post('/api/v1/auth/logout', {})
  },

  getToken(): string {
    return localStorage.getItem('token') || sessionStorage.getItem('token') || ''
  }
}
