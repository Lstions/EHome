import client from './client'

// Backend Collector model (source of truth)
export interface Collector {
  id: number
  device_id: string
  model: string
  firmware_version: string
  status: string
  config_version: string
  config_status: string
  last_seen: string | null
  uptime_seconds: number
  created_at: string
  updated_at: string
  channels?: Channel[]
}

export interface Channel {
  id: number
  collector_id: number
  hardware_type: string
  hardware_id: number
  interval_ms: number
  bus_type: string
  bus_config: string
  template_ids: string
  config: string
  enabled: boolean
  created_at: string
  updated_at: string
  devices?: Device[]
}

export interface Device {
  id: number
  name: string
  type: string
  parser_id: string
  channel_id: number
  status: string
  created_at: string
  updated_at: string
}

// Frontend display types (adapted from backend)
export interface CollectorDisplay {
  id: number
  name: string
  device_id: string
  model: string
  firmware_version: string
  status: 'online' | 'offline'
  config_version: string
  config_status: string
  last_seen: string | null
  uptime_seconds: number
  created_at: string
  channels?: Channel[]
}

export interface CollectorListParams {
  status?: string
  page?: number
  page_size?: number
}

// OTA types matching backend OTATask model
export interface OTATask {
  id: number
  ota_id: string
  collector_id: number
  firmware_id: number
  status: string
  progress: number
  error_msg: string
  created_at: string
  updated_at: string
}

// Peripheral/hardware types (for detail views)
export interface PeripheralInfo {
  id: string
  status: 'available' | 'configured' | 'error'
  mode?: string
  baudrate?: number
  assigned_device_id?: string
  assigned_device_type?: string
  assigned_device_name?: string
  config?: Record<string, any>
  unassigning?: boolean
}

export interface HardwareConfig {
  uart?: PeripheralInfo[]
  i2c?: PeripheralInfo[]
  spi?: PeripheralInfo[]
}

export interface GPIOBusResource {
  id: string
  enabled: boolean
  direction?: 'input' | 'output'
  pull?: 'none' | 'pullup' | 'pulldown'
  config_id?: number | null
  pin?: number
  features?: number
}

export interface ADCBusResource {
  id: string
  enabled: boolean
  attenuation?: '0db' | '2.5db' | '6db' | '11db'
  vref_mv?: number
  config_id?: number | null
  unit?: number
  channel?: number
  bits?: number
}

export interface I2CBusResource {
  id: string
  enabled: boolean
  mode?: 'master' | 'slave'
  freq_hz?: number
  config_id?: number | null
  port?: number
  features?: number
}

export interface SPIBusResource {
  id: string
  enabled: boolean
  mode?: 'master' | 'slave'
  clock_hz?: number
  config_id?: number | null
  port?: number
  features?: number
}

export interface UARTBusResource {
  id: string
  enabled: boolean
  baud_rate?: number
  data_bits?: number
  parity?: 'none' | 'even' | 'odd'
  stop_bits?: number
  config_id?: number | null
  port?: number
  default_tx?: number
  default_rx?: number
}

export interface Capabilities {
  model?: string
  buses?: {
    gpio?: GPIOBusResource[]
    adc?: ADCBusResource[]
    i2c?: I2CBusResource[]
    spi?: SPIBusResource[]
    uart?: UARTBusResource[]
  }
}

export interface PeripheralAssignment {
  peripheral_type: 'uart' | 'i2c' | 'spi'
  peripheral_id: string
  device_type: string
  device_name: string
  protocol: 'modbus' | 'stream'
  template_id?: number
  config?: Record<string, any>
}

// Adapt backend Collector to display model
function toDisplayModel(col: Collector): CollectorDisplay {
  return {
    id: col.id,
    name: col.device_id, // Use device_id as display name since backend has no name field
    device_id: col.device_id,
    model: col.model || 'N/A',
    firmware_version: col.firmware_version || 'N/A',
    status: (col.status === 'online' ? 'online' : 'offline') as 'online' | 'offline',
    config_version: col.config_version,
    config_status: col.config_status,
    last_seen: col.last_seen,
    uptime_seconds: col.uptime_seconds,
    created_at: col.created_at,
    channels: col.channels,
  }
}

export const collectorApi = {
  async getList(_params?: CollectorListParams): Promise<CollectorDisplay[]> {
    // Backend returns bare array: [Collector, ...]
    const response = await client.get<unknown, Collector[]>('/api/v1/collectors')
    const list = Array.isArray(response) ? response : (response as any).data || []
    return list.map(toDisplayModel)
  },

  async getDetail(deviceId: string): Promise<CollectorDisplay> {
    // Backend uses device_id as param, not numeric id
    const response = await client.get<unknown, Collector>(`/api/v1/collectors/${deviceId}`)
    const col = (response as any).data || response
    return toDisplayModel(col as Collector)
  },

  async delete(id: number): Promise<void> {
    await client.delete(`/api/v1/collectors/${id}`)
  },

  async getConfig(deviceId: string): Promise<Record<string, any>> {
    // Backend doesn't have a dedicated config endpoint yet
    // Get collector detail which includes channels
    const response = await client.get<unknown, Collector>(`/api/v1/collectors/${deviceId}`)
    const col = (response as any).data || response
    return (col as Collector).channels || {}
  },

  async updateConfig(_id: number, _config: Record<string, any>): Promise<void> {
    // Backend doesn't have PUT /collectors/:id/config yet
  },

  async syncConfig(_id: number): Promise<void> {
    // Backend doesn't have POST /collectors/:id/config/sync yet
  },

  async startOTA(collectorId: number, firmwareId: number, _force: boolean = false): Promise<{ ota_id: string; status: string }> {
    // Backend: POST /api/v1/ota/tasks with {collector_id, firmware_id}
    const response = await client.post<unknown, OTATask>('/api/v1/ota/tasks', {
      collector_id: collectorId,
      firmware_id: firmwareId,
    })
    const task = (response as any).data || response
    return { ota_id: (task as OTATask).ota_id, status: (task as OTATask).status }
  },

  async getOTAProgress(taskId: number): Promise<OTATask> {
    const response = await client.get<unknown, OTATask>(`/api/v1/ota/tasks/${taskId}`)
    return (response as any).data || response
  },

  async getOTAHistory(_collectorId: number): Promise<OTATask[]> {
    // Backend: GET /api/v1/ota/tasks (returns all tasks, filter client-side)
    const response = await client.get<unknown, OTATask[]>('/api/v1/ota/tasks')
    const list = Array.isArray(response) ? response : (response as any).data || []
    return list
  },

  async cancelOTA(_id: number, _recordId: number): Promise<void> {
    // Backend doesn't have POST /ota/cancel/:id yet
  },

  async getHardwareConfig(_id: number): Promise<Record<string, any>> {
    // Backend doesn't have this endpoint yet
    return {}
  },

  async updateHardwareConfig(_id: number, _hardware: Record<string, any>): Promise<void> {
    // Backend doesn't have this endpoint yet
  },

  async getCapabilities(_id: number): Promise<Capabilities> {
    // Backend doesn't have this endpoint yet
    return {}
  },

  async queryResources(_id: number): Promise<{ request_id: string }> {
    // Backend doesn't have this endpoint yet
    return { request_id: '' }
  },

  async scanI2C(_id: number, _hardwareId: string): Promise<{ devices: string[] }> {
    // Backend doesn't have this endpoint yet
    return { devices: [] }
  },

  async ping(deviceId: string): Promise<{ message: string }> {
    // Backend: POST /api/v1/collectors/:device_id/ping
    const response = await client.post<unknown, { message: string }>(`/api/v1/collectors/${deviceId}/ping`)
    return (response as any).data || response
  },
}
