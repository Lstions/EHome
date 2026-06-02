import client from './client'

interface ApiResponse<T> {
  code: number
  message: string
  data: T
}

export interface Firmware {
  id: number
  name: string
  version: string
  model: string
  file_size: number
  file_md5: string
  changelog: string
  status: string
  created_at: string
}

export interface FirmwareListParams {
  model?: string
  status?: string
  page?: number
  page_size?: number
}

export const firmwareApi = {
  async getList(params?: FirmwareListParams): Promise<{total: number, list: Firmware[]}> {
    const response = await client.get<unknown, ApiResponse<{total: number, list: Firmware[]}>>('/api/v1/firmwares', { params })
    return response.data
  },

  async upload(formData: FormData): Promise<Firmware> {
    const response = await client.post<unknown, ApiResponse<Firmware>>('/api/v1/firmwares/upload', formData, {
      headers: { 'Content-Type': 'multipart/form-data' }
    })
    return response.data
  },

  async update(id: number, data: { name?: string; changelog?: string }): Promise<void> {
    await client.put(`/api/v1/firmwares/${id}`, data)
  },

  async delete(id: number): Promise<void> {
    await client.delete(`/api/v1/firmwares/${id}`)
  },

  getDownloadUrl(id: number): string {
    const baseURL = client.defaults.baseURL || ''
    const token = localStorage.getItem('token') || sessionStorage.getItem('token')
    return `${baseURL}/api/v1/firmwares/${id}/download?token=${token}`
  }
}
