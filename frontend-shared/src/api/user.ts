/**
 * 用户管理 API
 *
 * 角色枚举必须与后端保持一致:
 *   - admin    管理员
 *   - operator 操作员
 *   - viewer   观察者
 */
import client from './client'
import type { UserRole } from '@/stores/user'

export interface UserListItem {
  id: number
  username: string
  email: string
  role: UserRole
  enabled: boolean
  created_at: string
  last_login_at?: string
}

export interface UserListParams {
  page?: number
  page_size?: number
  keyword?: string
  role?: UserRole
}

export interface UserListResponse {
  list: UserListItem[]
  total: number
  page: number
  page_size: number
}

export interface CreateUserParams {
  username: string
  password: string
  email?: string
  role: UserRole
}

export interface UpdateUserParams {
  email?: string
  role?: UserRole
  enabled?: boolean
}

export interface ChangePasswordParams {
  old_password: string
  new_password: string
}

export const userApi = {
  list: (params: UserListParams = {}) => {
    return client.get<UserListResponse>('/api/v1/users', { params })
  },
  get: (id: number) => {
    return client.get<UserListItem>(`/api/v1/users/${id}`)
  },
  create: (data: CreateUserParams) => {
    return client.post<UserListItem>('/api/v1/users', data)
  },
  update: (id: number, data: UpdateUserParams) => {
    return client.put<UserListItem>(`/api/v1/users/${id}`, data)
  },
  delete: (id: number) => {
    return client.delete(`/api/v1/users/${id}`)
  },
  changePassword: (data: ChangePasswordParams) => {
    return client.post('/api/v1/users/me/change-password', data)
  },
  /** 管理员重置他人密码 */
  resetPassword: (id: number, new_password: string) => {
    return client.post(`/api/v1/users/${id}/reset-password`, { new_password })
  },
}

export default userApi
