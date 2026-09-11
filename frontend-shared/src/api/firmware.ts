import client from './client'

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
    const response = await client.get('/api/v1/firmwares', { params })
    // Interceptor returns {code, data, message} → response.data = the array
    const list = (response as any).data as Firmware[] ?? []
    return { total: list.length, list }
  },

  async upload(formData: FormData): Promise<Firmware> {
    const response = await client.post('/api/v1/firmwares/upload', formData)
    // 后端 POST /api/v1/firmwares/upload（handler_ota.go:199 `c.JSON(http.StatusCreated, fw)`）
    // 返回的是**裸 Firmware 对象**，不是 envelope —— 只取 `.data` 会恒为 undefined。
    // 与 periph.ts 的 `?.data || resp` 同款双向兼容（envelope / 裸对象都能取到）。
    const raw = response as any
    return (raw?.data ?? raw) as Firmware
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
