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
    const response = await client.get('/api/v1/firmwares', { params })
    // Interceptor returns {code, data, message} → response.data = the array
    const list = (response as any).data as Firmware[] ?? []
    return { total: list.length, list }
  },

  async upload(formData: FormData): Promise<Firmware> {
    const response = await client.post('/api/v1/firmwares/upload', formData, {
      headers: { 'Content-Type': 'multipart/form-data' }
    })
    return (response as any).data
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
