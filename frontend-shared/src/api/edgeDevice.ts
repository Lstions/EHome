import client from './client'
import type { OperationDef } from './deviceConfig'

// M10 fix: Export DeviceStatus type for use across components
// Extended to support health status: active, warning, error, disabled
export type DeviceStatus = 'active' | 'online' | 'offline' | 'warning' | 'error' | 'disabled' | 'pending' | 'initializing' | 'unknown'

export interface EdgeDevice {
  id: number
  node_id: number | string
  channel_id: number
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

// 创建参数 — 精确对齐后端 CreateDTO，不继承 Partial<EdgeDevice>
// 后端 DTO: name(*string), type(*string), node_id(*string), channel_id(*uint), hardware_id(*string)
export interface CreateEdgeDeviceParams {
  name?: string
  type?: string
  node_id?: string
  channel_id?: number
  hardware_id?: string
  enabled?: boolean
  interval_ms?: number
  device_config_id?: number
}

// ============================================================
// Normalize backend fields → frontend EdgeDevice interface
// ============================================================

// RawEdgeDevice represents the raw API response from the backend
// (M8 fix: replaces `any` with proper interface for type safety)
interface RawEdgeDevice {
  id: number
  node_id?: number | string
  channel_id?: number
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

function isObjectRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function isRawEdgeDevice(value: unknown): value is RawEdgeDevice {
  return isObjectRecord(value) && value.id !== undefined && value.id !== null
}

/**
 * Keeps list consumers from receiving null, undefined, sparse, or otherwise
 * unusable entries from an API/cache boundary.
 */
export function compactEdgeDeviceList(items: unknown): EdgeDevice[] {
  if (!Array.isArray(items)) return []
  return items.filter(isRawEdgeDevice) as EdgeDevice[]
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
  channel_id: d.channel_id ?? 0,
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

const normalizeList = (items: unknown[]): EdgeDevice[] =>
  items.filter(isRawEdgeDevice).map(normalize)

// ============================================================
// API (edgeDeviceApi)
// ============================================================

export const edgeDeviceApi = {
  async getList(params?: EdgeDeviceListParams): Promise<{total: number, items: EdgeDevice[]}> {
    const response = await client.get<unknown, any>('/api/v1/edge-devices', { params })
    // Backend returns bare array [{...}], not {code, data, message} envelope
    // Interceptor returns response.data (parsed JSON body), so response IS the array
    if (Array.isArray(response)) {
      return { total: response.length, items: normalizeList(response) }
    }
    // Handle envelope format if backend changes
    if (Array.isArray(response?.data?.items)) {
      return {
        total: response.data.total ?? response.data.items.length,
        items: normalizeList(response.data.items),
      }
    }
    if (response?.data && Array.isArray(response.data)) {
      return { total: response.data.length, items: normalizeList(response.data) }
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

  // 更新参数 — 对齐后端 UpdateDTO
  async update(id: number, data: CreateEdgeDeviceParams): Promise<void> {
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

  async getOperationHistory(id: number, limit: number = 50): Promise<any[]> {
    const response = await client.get<unknown, any>(
      `/api/v1/edge-devices/${id}/operations/history`,
      { params: { limit } }
    )
    if (Array.isArray(response)) return response
    if (response?.data) return response.data as any[]
    return []
  },

  // Driver command templates
  async getDriverCommands(deviceType: string): Promise<CommandTemplate[]> {
    const response = await client.get<unknown, any>(`/api/v1/drivers/${deviceType}/commands`)
    if (Array.isArray(response?.data)) return response.data
    if (Array.isArray(response)) return response
    return []
  },

  // Edge device command intervals
  async getCommandIntervals(edgeDeviceId: number): Promise<CommandTemplateWithInterval[]> {
    const response = await client.get<unknown, any>(`/api/v1/edge-devices/${edgeDeviceId}/commands`)
    if (Array.isArray(response?.data)) return response.data
    if (Array.isArray(response)) return response
    return []
  },

  async updateCommandIntervals(edgeDeviceId: number, intervals: Record<string, number>): Promise<void> {
    await client.put(`/api/v1/edge-devices/${edgeDeviceId}/commands`, { intervals })
  },
}

export interface CommandTemplate {
  id: string
  name: string
  type: string
  cmd_byte: number
  write_data: string
  read_length: number
  delay_ms: number
  interval_ms: number
  schedulable: boolean
  description: string
}

export interface CommandTemplateWithInterval extends CommandTemplate {
  current_interval_ms: number
}
