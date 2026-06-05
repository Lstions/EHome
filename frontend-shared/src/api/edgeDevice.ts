import client from './client'

export interface EdgeDevice {
  id: number
  node_id: number
  node?: { id: number; name: string }
  name: string
  device_type: string
  protocol: string
  hardware_type: string
  hardware_id: string
  config: Record<string, any>
  status: string
  last_data: Record<string, number> | null
  last_data_time: string | null
  created_at: string
}

export interface EdgeDeviceListParams {
  node_id?: number
  device_type?: string
  status?: string
  page?: number
  page_size?: number
}

export interface CreateEdgeDeviceParams extends Partial<EdgeDevice> {
  channel_id?: number
}

// ============================================================
// API (edgeDeviceApi)
// ============================================================

export const edgeDeviceApi = {
  async getList(params?: EdgeDeviceListParams): Promise<{total: number, items: EdgeDevice[]}> {
    const response = await client.get<unknown, any>('/api/v1/edge-devices', { params })
    // Backend returns bare array [{...}], not {code, data, message} envelope
    // Interceptor returns response.data (parsed JSON body), so response IS the array
    if (Array.isArray(response)) {
      return { total: response.length, items: response as EdgeDevice[] }
    }
    // Handle envelope format if backend changes
    if (response?.data?.items) {
      return { total: response.data.total ?? response.data.items.length, items: response.data.items }
    }
    if (response?.data && Array.isArray(response.data)) {
      return { total: response.data.length, items: response.data }
    }
    return { total: 0, items: [] }
  },

  async getDetail(id: number): Promise<EdgeDevice> {
    const response = await client.get<unknown, any>(`/api/v1/edge-devices/${id}`)
    // GET /edge-devices/:id returns envelope {code, data, message}
    if (response?.data && typeof response.data === 'object') {
      return response.data as EdgeDevice
    }
    return response as unknown as EdgeDevice
  },

  async create(data: CreateEdgeDeviceParams): Promise<{id: number}> {
    const response = await client.post<unknown, any>('/api/v1/edge-devices', data)
    // POST returns bare object (the created device)
    if (response?.id !== undefined) {
      return { id: response.id }
    }
    if (response?.data?.id !== undefined) {
      return { id: response.data.id }
    }
    return response as unknown as {id: number}
  },

  async update(id: number, data: Partial<EdgeDevice>): Promise<void> {
    await client.put(`/api/v1/edge-devices/${id}`, data)
  },

  async delete(id: number): Promise<void> {
    await client.delete(`/api/v1/edge-devices/${id}`)
  },

  async getLatestData(id: number): Promise<any> {
    const response = await client.get<unknown, any>(`/api/v1/edge-devices/${id}/latest-data`)
    return response?.data !== undefined ? response.data : response
  },

  async getHistoryData(id: number, params: {
    start_time: string
    end_time: string
    page?: number
    page_size?: number
  }): Promise<any> {
    const response = await client.get<unknown, any>(`/api/v1/edge-devices/${id}/data`, { params })
    return response?.data !== undefined ? response.data : response
  },

  async executeOperation(id: number, operation: string, params?: Record<string, any>): Promise<any> {
    const response = await client.post<unknown, any>(`/api/v1/edge-devices/${id}/operations`, {
      operation,
      params
    })
    return response?.data !== undefined ? response.data : response
  },

  async getOperationHistory(id: number, limit: number = 50): Promise<any[]> {
    const response = await client.get<unknown, any>(
      `/api/v1/edge-devices/${id}/operations/history`,
      { params: { limit } }
    )
    if (Array.isArray(response)) return response
    if (response?.data) return response.data as any[]
    return []
  }
}
