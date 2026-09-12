import client, { type ApiEnvelope } from './client'

export interface Firmware {
  id: number
  version: string
  filename: string
  checksum: string
  size_bytes: number
  url: string
  changelog?: string
  target_model?: string
  min_from_version?: string
  stable?: boolean
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
    // 后端统一 envelope: {code, data: Firmware[], message}
    const response = await client.get<unknown, ApiEnvelope<Firmware[]>>('/api/v1/firmwares', { params })
    const list = response.data ?? []
    return { total: list.length, list }
  },

  async upload(formData: FormData): Promise<Firmware> {
    // 后端统一 envelope: {code, data: Firmware, message}
    const response = await client.post<unknown, ApiEnvelope<Firmware>>('/api/v1/firmwares/upload', formData)
    return response.data
  },

  async update(id: number, data: { version?: string; changelog?: string }): Promise<void> {
    await client.put(`/api/v1/firmwares/${id}`, data)
  },

  async delete(id: number): Promise<void> {
    await client.delete(`/api/v1/firmwares/${id}`)
  },

  getDownloadUrl(filename: string): string {
    const baseURL = client.defaults.baseURL || ''
    const token = localStorage.getItem('token') || sessionStorage.getItem('token')
    return `${baseURL}/api/v1/firmwares/${encodeURIComponent(filename)}/download?token=${token}`
  }
}
