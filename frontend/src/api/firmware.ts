import client from './client'

// Backend Firmware model (source of truth)
export interface Firmware {
  id: number
  version: string
  checksum: string
  size_bytes: number
  url: string
  created_at: string
}

// Firmware display model for UI (adapted from backend)
export interface FirmwareDisplay {
  id: number
  name: string            // ← version (used as display name)
  version: string
  model: string           // N/A (backend doesn't return)
  file_size: number       // ← size_bytes
  file_md5: string        // ← checksum
  changelog: string       // N/A
  status: string          // N/A
  created_at: string
  url: string
}

export interface FirmwareListParams {
  model?: string
  status?: string
  page?: number
  page_size?: number
}

// Adapt backend Firmware to display model
function toDisplayModel(fw: Firmware): FirmwareDisplay {
  return {
    id: fw.id,
    name: fw.version,
    version: fw.version,
    model: 'N/A',
    file_size: fw.size_bytes,
    file_md5: fw.checksum,
    changelog: 'N/A',
    status: 'N/A',
    created_at: fw.created_at,
    url: fw.url,
  }
}

export const firmwareApi = {
  async getList(_params?: FirmwareListParams): Promise<FirmwareDisplay[]> {
    // Backend returns bare array: [{id, version, checksum, size_bytes, url, created_at}]
    const response = await client.get<unknown, Firmware[]>('/api/v1/firmwares')
    // Handle both array and wrapped responses defensively
    const list = Array.isArray(response) ? response : (response as any).data || []
    return list.map(toDisplayModel)
  },

  async upload(formData: FormData): Promise<FirmwareDisplay> {
    // Backend returns bare Firmware object on 201
    const response = await client.post<unknown, Firmware>('/api/v1/firmwares/upload', formData, {
      headers: { 'Content-Type': 'multipart/form-data' }
    })
    const fw = (response as any).data || response
    return toDisplayModel(fw as Firmware)
  },

  async update(_id: number, _data: { name?: string; changelog?: string }): Promise<void> {
    // Backend doesn't have a PUT /firmwares/:id endpoint yet
    // await client.put(`/api/v1/firmwares/${id}`, data)
  },

  async delete(_id: number): Promise<void> {
    // Backend doesn't have a DELETE /firmwares/:id endpoint yet
    // await client.delete(`/api/v1/firmwares/${id}`)
  },

  getDownloadUrl(filename: string): string {
    const baseURL = client.defaults.baseURL || ''
    const token = localStorage.getItem('token') || sessionStorage.getItem('token')
    return `${baseURL}/api/v1/firmwares/${filename}/download?token=${token}`
  }
}
