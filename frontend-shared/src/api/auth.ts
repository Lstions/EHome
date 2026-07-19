import client from './client'

export interface LoginRequest {
  username: string
  password: string
  rememberMe?: boolean
}

export interface AccountInfo {
  id: number
  username: string
  email: string
  enabled?: boolean
}

export interface LoginResponse {
  token: string
  user: AccountInfo
}

export interface InitializeRequest {
  credential: string
  username: string
  password: string
  email?: string
}

export interface InitializeResponse {
  id: number
  username: string
}

export type AuthState = 'uninitialized' | 'initialized' | 'migration_required' | 'disabled'

export const authApi = {
  async initialization(): Promise<{ state: AuthState }> {
    const response = await client.get<unknown, any>('/api/v1/auth/initialization')
    return (response as any).data as { state: AuthState }
  },

  async initialize(data: InitializeRequest): Promise<InitializeResponse> {
    const response = await client.post<unknown, any>('/api/v1/auth/initialize', data)
    return (response as any).data as InitializeResponse
  },

  async login(data: LoginRequest): Promise<LoginResponse> {
    const response = await client.post<unknown, any>('/api/v1/auth/login', data)
    return (response as any).data as LoginResponse
  },

  async logout(): Promise<void> {
    await client.post('/api/v1/auth/logout', {})
  },

  async account(): Promise<AccountInfo> {
    const response = await client.get<unknown, any>('/api/v1/account')
    return (response as any).data as AccountInfo
  },

  async changePassword(data: { old_password: string; new_password: string }): Promise<void> {
    await client.post('/api/v1/account/password', data)
  },

  async reauthenticate(password: string): Promise<void> {
    await client.post('/api/v1/account/reauthenticate', { password })
  },

  getToken(): string {
    return localStorage.getItem('token') || sessionStorage.getItem('token') || ''
  },
}
