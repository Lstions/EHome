import client from './client'

interface ApiResponse<T> {
  code: number
  message: string
  data: T
}

// ============================================================
// v2.2 正式类型
// ============================================================

export interface EdgeDevice {
  id: number
  node_id: number              // v2.2 字段名 (原 collector_id)
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

  // v2.1 兼容字段 (6 个月后删除, v2.3)
  /** @deprecated Use node_id instead */
  collector_id?: number
}

export interface EdgeDeviceListParams {
  node_id?: number             // v2.2 字段名 (原 collector_id)
  device_type?: string
  status?: string
  page?: number
  page_size?: number
}

export interface CreateEdgeDeviceParams extends Partial<EdgeDevice> {
  channel_id?: number
}

// ============================================================
// v2.1 兼容别名 (6 个月后删除, v2.3)
// ============================================================

/** @deprecated Use EdgeDevice instead */
export type Device = EdgeDevice
/** @deprecated Use EdgeDeviceListParams instead */
export type DeviceListParams = EdgeDeviceListParams
/** @deprecated Use CreateEdgeDeviceParams instead */
export type CreateDeviceParams = CreateEdgeDeviceParams

// ============================================================
// v2.2 API (edgeDeviceApi) — 推荐
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

// ============================================================
// v2.1 兼容 API (deviceApi) — 6 个月后删除, v2.3
// 调用 v2.1 /api/v1/devices 路径 (后端双注册保证)
// ============================================================

/** @deprecated Use edgeDeviceApi instead */
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
