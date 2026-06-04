import client from './client'

interface ApiResponse<T> {
  code: number
  message: string
  data: T
}

export interface EdgeDevice {
  id: number
  node_id: number
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
    const response = await client.get<unknown, ApiResponse<{total: number, items: EdgeDevice[]}>>('/api/v1/edge-devices', { params })
    return response.data
  },

  async getDetail(id: number): Promise<EdgeDevice> {
    const response = await client.get<unknown, ApiResponse<EdgeDevice>>(`/api/v1/edge-devices/${id}`)
    return response.data
  },

  async create(data: CreateEdgeDeviceParams): Promise<{id: number}> {
    const response = await client.post<unknown, ApiResponse<{id: number}>>('/api/v1/edge-devices', data)
    return response.data
  },

  async update(id: number, data: Partial<EdgeDevice>): Promise<void> {
    await client.put(`/api/v1/edge-devices/${id}`, data)
  },

  async delete(id: number): Promise<void> {
    await client.delete(`/api/v1/edge-devices/${id}`)
  },

  async getLatestData(id: number): Promise<any> {
    const response = await client.get<unknown, ApiResponse<any>>(`/api/v1/edge-devices/${id}/latest-data`)
    return response.data
  },

  async getHistoryData(id: number, params: {
    start_time: string
    end_time: string
    page?: number
    page_size?: number
  }): Promise<any> {
    const response = await client.get<unknown, ApiResponse<any>>(`/api/v1/edge-devices/${id}/data`, { params })
    return response.data
  },

  async executeOperation(id: number, operation: string, params?: Record<string, any>): Promise<any> {
    const response = await client.post<unknown, ApiResponse<any>>(`/api/v1/edge-devices/${id}/operations`, {
      operation,
      params
    })
    return response.data
  },

  async getOperationHistory(id: number, limit: number = 50): Promise<any[]> {
    const response = await client.get<unknown, ApiResponse<any[]>>(
      `/api/v1/edge-devices/${id}/operations/history`,
      { params: { limit } }
    )
    return response.data
  }
}
