import client from './client'

export interface Firmware {
  id: number
  version: string
  checksum: string
  size_bytes: number
  url: string
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
    const response = await client.get<unknown, any>('/api/v1/firmwares', { params })
    // Backend returns bare array [{...}], not {code, data, message} envelope
    // Interceptor returns response.data (parsed JSON body), so response IS the array
    if (Array.isArray(response)) {
      return { total: response.length, list: response as Firmware[] }
    }
    // Handle envelope format if backend changes
    if (response?.data?.list) {
      return { total: response.data.total ?? response.data.list.length, list: response.data.list }
    }
    if (response?.data && Array.isArray(response.data)) {
      return { total: response.data.length, list: response.data }
    }
    return { total: 0, list: [] }
  },

  async upload(formData: FormData): Promise<Firmware> {
    const response = await client.post<unknown, any>('/api/v1/firmwares/upload', formData, {
      headers: { 'Content-Type': 'multipart/form-data' }
    })
    // POST returns bare object (the created firmware)
    if (response?.id !== undefined) return response as Firmware
    if (response?.data) return response.data as Firmware
    return response as unknown as Firmware
  },

  async update(id: number, data: { version?: string; changelog?: string }): Promise<void> {
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
