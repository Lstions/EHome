import client from './client'
import type { OperationDef } from './deviceConfig'

// M10 fix: Export DeviceStatus type for use across components
// Extended to support health status: active, warning, error, disabled
export type DeviceStatus = 'active' | 'online' | 'offline' | 'warning' | 'error' | 'disabled' | 'pending' | 'initializing' | 'unknown'

export interface ExecuteOperationResponse {
  code: number
  data: {
    status: string
    operation: string
    value?: number
    unit?: string
    data_hex?: string
  }
  message: string
}

export interface EdgeDevice {
  id: number
  node_id: number | string
  node?: { id: number | string; name: string }
  name: string
  device_type: string
  protocol: string
  hardware_type: string
  hardware_id: string
  config: Record<string, any>
  status: DeviceStatus
  last_data: Record<string, number> | null
  last_data_time: string | null
  last_error_code?: number
  created_at: string
  device_config?: { id?: number; protocol?: string; config?: Record<string, any> | string; operations?: Record<string, OperationDef> }
}

export interface EdgeDeviceListParams {
  node_id?: number | string
  device_type?: string
  status?: string
  page?: number
  page_size?: number
}

export interface CreateEdgeDeviceParams extends Partial<EdgeDevice> {
  channel_id?: number
}

// ============================================================
// Normalize backend fields → frontend EdgeDevice interface
// ============================================================

// RawEdgeDevice represents the raw API response from the backend
// (M8 fix: replaces `any` with proper interface for type safety)
interface RawEdgeDevice {
  id: number
  node_id?: number | string
  node?: { id?: number | string; name?: string }
  name?: string
  type?: string
  device_type?: string
  protocol?: string
  device_config?: { id?: number; protocol?: string; config?: Record<string, any> | string; operations?: Record<string, OperationDef> }
  hardware_type?: string
  channel?: { hardware_type?: string; hardware_id?: string }
  hardware_id?: string
  config?: Record<string, any>
  status?: string
  last_data?: Record<string, number> | null
  last_data_at?: string | null
  last_data_time?: string | null
  error_code?: number
  last_error_code?: number
  created_at?: string
}

// M9 fix: Explicit status mapping from backend values to frontend display values
// Preserves health status values (active, warning, error, disabled) instead of collapsing to online/offline
const STATUS_MAP: Record<string, DeviceStatus> = {
  active: 'active',
  online: 'online',
  offline: 'offline',
  warning: 'warning',
  error: 'error',
  disabled: 'disabled',
  pending: 'pending',
  initializing: 'initializing',
  unknown: 'unknown',
}

function mapStatus(rawStatus?: string): DeviceStatus {
  if (!rawStatus) return 'offline'
  return STATUS_MAP[rawStatus] ?? 'unknown' // unmapped statuses become 'unknown' instead of unsafe cast
}

const normalize = (d: RawEdgeDevice): EdgeDevice => ({
  id: d.id,
  node_id: d.node_id ?? 0,
  node: d.node && d.node.name ? { id: (d.node.id ?? d.node_id ?? 0) as string | number, name: d.node.name } : undefined,
  name: d.name || '',
  device_type: d.type || d.device_type || '',
  protocol: d.protocol || d.device_config?.protocol || '',
  hardware_type: d.hardware_type || d.channel?.hardware_type || '',
  hardware_id: d.hardware_id || d.channel?.hardware_id || '',
  config: d.config || {},
  status: mapStatus(d.status),
  last_data: d.last_data || null,
  last_data_time: d.last_data_at || d.last_data_time || null,
  last_error_code: d.error_code ?? d.last_error_code ?? undefined,
  created_at: d.created_at || '',
  device_config: d.device_config
})

// ============================================================
// API (edgeDeviceApi)
// ============================================================

export const edgeDeviceApi = {
  async getList(params?: EdgeDeviceListParams): Promise<{total: number, items: EdgeDevice[]}> {
    const response = await client.get<unknown, any>('/api/v1/edge-devices', { params })
    // Backend returns bare array [{...}], not {code, data, message} envelope
    // Interceptor returns response.data (parsed JSON body), so response IS the array
    if (Array.isArray(response)) {
      return { total: response.length, items: response.map(normalize) }
    }
    // Handle envelope format if backend changes
    if (response?.data?.items) {
      return { total: response.data.total ?? response.data.items.length, items: response.data.items.map(normalize) }
    }
    if (response?.data && Array.isArray(response.data)) {
      return { total: response.data.length, items: response.data.map(normalize) }
    }
    return { total: 0, items: [] }
  },

  async getDetail(id: number): Promise<EdgeDevice> {
    const response = await client.get<unknown, any>(`/api/v1/edge-devices/${id}`)
    // GET /edge-devices/:id returns envelope {code, data, message}
    if (response?.data && typeof response.data === 'object') {
      return normalize(response.data)
    }
    return normalize(response)
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
    return response.data || response
  },

  async getHistoryData(id: number, params: {
    start_time: string
    end_time: string
    page?: number
    page_size?: number
  }): Promise<any> {
    const response = await client.get<unknown, any>(`/api/v1/edge-devices/${id}/data`, { params })
    return response.data || response
  },

  async executeOperation(id: number, operation: string, params?: Record<string, any>): Promise<ExecuteOperationResponse> {
    const response = await client.post<unknown, any>(`/api/v1/edge-devices/${id}/execute`, {
      operation,
      params: params || {}
    })
    return response.data || response
  },

  async getOperationHistory(id: number, limit: number = 50): Promise<any[]> {
    const response = await client.get<unknown, any>(
      `/api/v1/edge-devices/${id}/operations/history`,
      { params: { limit } }
    )
    if (Array.isArray(response)) return response
    if (response?.data) return response.data as any[]
    return []
  },

  async changeAddress(id: number, newAddress: number): Promise<any> {
    const response = await client.post<unknown, any>(`/api/v1/edge-devices/${id}/change-address`, {
      new_address: newAddress
    })
    return response.data || response
  }
}
