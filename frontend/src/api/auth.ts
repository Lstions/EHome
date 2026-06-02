import client from './client'

// Backend LoginResponse (source of truth): {token: string, user: {id, username, role}}
export interface LoginRequest {
  username: string
  password: string
}

export interface LoginResponse {
  token: string
  user: {
    id: number
    username: string
    role: string
  }
}

export const authApi = {
  async login(data: LoginRequest): Promise<LoginResponse> {
    // Backend returns bare {token, user} (not wrapped in ApiResponse)
    // client interceptor already unwraps response.data
    const response = await client.post<unknown, LoginResponse>('/api/v1/auth/login', data)
    // If the response is the bare object from the backend
    if (response && typeof response === 'object') {
      // Check if it's wrapped in ApiResponse {code, message, data} or bare
      const r = response as any
      if (r.code !== undefined && r.data !== undefined) {
        return r.data as LoginResponse
      }
      return response as LoginResponse
    }
    return response as LoginResponse
  },

  async logout(): Promise<void> {
    // Backend doesn't have a logout endpoint yet; just clear local state
  },

  getToken(): string {
    return localStorage.getItem('token') || sessionStorage.getItem('token') || ''
  }
}
