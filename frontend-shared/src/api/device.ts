import client from './client'

interface ApiResponse<T> {
  code: number
  message: string
  data: T
}

export interface Device {
  id: number
  collector_id: number
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

export interface DeviceListParams {
  collector_id?: number
  device_type?: string
  status?: string
  page?: number
  page_size?: number
}

export interface CreateDeviceParams extends Partial<Device> {
  channel_id?: number
}

export const deviceApi = {
  async getList(params?: DeviceListParams): Promise<{total: number, items: Device[]}> {
    const response = await client.get<unknown, ApiResponse<{total: number, items: Device[]}>>('/api/v1/devices', { params })
    return response.data
  },

  async getDetail(id: number): Promise<Device> {
    const response = await client.get<unknown, ApiResponse<Device>>(`/api/v1/devices/${id}`)
    return response.data
  },

  async create(data: CreateDeviceParams): Promise<{id: number}> {
    const response = await client.post<unknown, ApiResponse<{id: number}>>('/api/v1/devices', data)
    return response.data
  },

  async update(id: number, data: Partial<Device>): Promise<void> {
    await client.put(`/api/v1/devices/${id}`, data)
  },

  async delete(id: number): Promise<void> {
    await client.delete(`/api/v1/devices/${id}`)
  },

  async getLatestData(id: number): Promise<any> {
    const response = await client.get<unknown, ApiResponse<any>>(`/api/v1/devices/${id}/latest-data`)
    return response.data
  },

  async getHistoryData(id: number, params: {
    start_time: string
    end_time: string
    page?: number
    page_size?: number
  }): Promise<any> {
    const response = await client.get<unknown, ApiResponse<any>>(`/api/v1/devices/${id}/data`, { params })
    return response.data
  },

  async executeOperation(id: number, operation: string, params?: Record<string, any>): Promise<any> {
    const response = await client.post<unknown, ApiResponse<any>>(`/api/v1/devices/${id}/operations`, {
      operation,
      params
    })
    return response.data
  },

  async getOperationHistory(id: number, limit: number = 50): Promise<any[]> {
    const response = await client.get<unknown, ApiResponse<any[]>>(
      `/api/v1/devices/${id}/operations/history`,
      { params: { limit } }
    )
    return response.data
  }
}
