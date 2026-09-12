import client, { type ApiEnvelope } from './client'

export interface LoginRequest {
  username: string
  password: string
  rememberMe?: boolean
}

export interface InitializeRequest {
  credential: string
  username: string
  password: string
  email?: string
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

export type AuthState = 'uninitialized' | 'initialized' | 'migration_required' | 'disabled'

export const authApi = {
  async initialization(): Promise<{ state: AuthState }> {
    const response = await client.get<unknown, ApiEnvelope<{ state: AuthState }>>('/api/v1/auth/initialization')
    return response.data
  },

  async login(data: LoginRequest): Promise<LoginResponse> {
    const response = await client.post<unknown, ApiEnvelope<LoginResponse>>('/api/v1/auth/login', data)
    return response.data
  },

  async initialize(data: InitializeRequest): Promise<void> {
    await client.post('/api/v1/auth/initialize', data)
  },

  async logout(): Promise<void> {
    await client.post('/api/v1/auth/logout', {})
  },

  async account(): Promise<AccountInfo> {
    const response = await client.get<unknown, ApiEnvelope<AccountInfo>>('/api/v1/account')
    return response.data
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
