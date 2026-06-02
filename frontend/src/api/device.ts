import client from './client'

// Backend Device model (source of truth)
interface BackendDevice {
  id: number
  name: string
  type: string
  parser_id: string
  channel_id: number
  status: string
  created_at: string
  updated_at: string
}

// Frontend display model (adapted)
export interface Device {
  id: number
  collector_id: number
  name: string
  device_type: string
  protocol: string
  hardware_type: string
  hardware_id: string
  config: Record<string, any> | null
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

// Adapt backend Device to display model
function toDisplayModel(d: BackendDevice): Device {
  return {
    id: d.id,
    collector_id: 0, // Backend doesn't return this directly on list
    name: d.name,
    device_type: d.type,
    protocol: d.parser_id || 'N/A',
    hardware_type: 'N/A',
    hardware_id: 'N/A',
    config: null,
    status: d.status,
    last_data: null,
    last_data_time: null,
    created_at: d.created_at,
  }
}

export const deviceApi = {
  async getList(_params?: DeviceListParams): Promise<Device[]> {
    // Backend returns bare array: [Device, ...]
    const response = await client.get<unknown, BackendDevice[]>('/api/v1/devices')
    const list = Array.isArray(response) ? response : (response as any).data || []
    return list.map(toDisplayModel)
  },

  async getDetail(id: number): Promise<Device> {
    // Backend doesn't have GET /devices/:id; get from list
    const list = await deviceApi.getList()
    const d = list.find(d => d.id === id)
    if (!d) throw new Error('Device not found')
    return d
  },

  async create(data: CreateDeviceParams): Promise<{id: number}> {
    // Backend: POST /api/v1/devices
    const response = await client.post<unknown, BackendDevice>('/api/v1/devices', {
      name: data.name,
      type: data.device_type,
      parser_id: data.protocol,
      channel_id: data.channel_id,
      status: data.status || 'active',
    })
    const d = (response as any).data || response
    return { id: (d as BackendDevice).id }
  },

  async update(_id: number, _data: Partial<Device>): Promise<void> {
    // Backend doesn't have PUT /devices/:id yet
  },

  async delete(_id: number): Promise<void> {
    // Backend doesn't have DELETE /devices/:id yet
  },

  async getLatestData(id: number): Promise<any> {
    // Backend: GET /api/v1/devices/:id/sensor-data
    const response = await client.get<unknown, any>(`/api/v1/devices/${id}/sensor-data`)
    return (response as any).data || response
  },

  async getHistoryData(id: number, params: {
    sensor?: string
    hours?: number
  }): Promise<any> {
    // Backend: GET /api/v1/devices/:id/history?sensor=xxx&hours=24
    const response = await client.get<unknown, any>(`/api/v1/devices/${id}/history`, { params })
    return (response as any).data || response
  },

  async executeOperation(_id: number, _operation: string, _params?: Record<string, any>): Promise<any> {
    // Backend doesn't have this endpoint yet
    throw new Error('Not implemented')
  },

  async getOperationHistory(_id: number, _limit: number = 50): Promise<any[]> {
    // Backend doesn't have this endpoint yet
    return []
  }
}
