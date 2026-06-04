import client from './client'

interface ApiResponse<T> {
  code: number
  message: string
  data: T
}

export interface Collector {
  id: number
  name: string
  device_id: string
  model: string
  firmware_version: string
  status: 'online' | 'offline'
  connection_type: string
  connection_quality: number
  latency_ms: number
  last_online_time: string
  online_duration: number
  capabilities: Record<string, any>
  config: Record<string, any>
  created_at: string
  // v2.1 同步机制字段
  protocol_version?: string
  config_sync_state?: 'in_sync' | 'syncing' | 'lag' | 'error' | 'unknown'
  config_epoch?: number
  last_manifest_id?: string
  last_sync_at?: string
  last_sync_id?: string
}

export interface CollectorListResponse {
  total: number
  page: number
  page_size: number
  items: Collector[]
}

export interface CollectorListParams {
  status?: string
  page?: number
  page_size?: number
}

export interface OTARecord {
  id: number
  collector_id: number
  firmware_id: number
  from_version: string
  to_version: string
  status: string
  progress: number
  error_message?: string
  created_at: string
  completed_at?: string
}

// 外设相关类型定义
export interface PeripheralInfo {
  id: string
  status: 'available' | 'configured' | 'error'
  mode?: string
  baudrate?: number
  assigned_device_id?: string
  assigned_device_type?: string
  assigned_device_name?: string
  config?: Record<string, any>
  unassigning?: boolean // 前端状态
}

export interface HardwareConfig {
  uart?: PeripheralInfo[]
  i2c?: PeripheralInfo[]
  spi?: PeripheralInfo[]
}

// 总线资源配置
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
  template_id?: number // 配置模板ID（可选）
  config?: Record<string, any>
}

export interface Device {
  id: number
  collector_id: number
  device_id: string
  name: string
  device_type: string
  protocol: string
  hardware_type: string
  hardware_id: string
  config: Record<string, any> | null
  status: string
  created_at: string
  updated_at: string
}

export const collectorApi = {
  async getList(params?: CollectorListParams): Promise<CollectorListResponse> {
    const response = await client.get<unknown, ApiResponse<CollectorListResponse>>('/api/v1/collectors', { params })
    return response.data
  },

  async getDetail(id: number): Promise<Collector> {
    const response = await client.get<unknown, ApiResponse<Collector>>(`/api/v1/collectors/${id}`)
    return response.data
  },

  async delete(id: number): Promise<void> {
    await client.delete(`/api/v1/collectors/${id}`)
  },

  async getConfig(id: number): Promise<Record<string, any>> {
    const response = await client.get<unknown, ApiResponse<Record<string, any>>>(`/api/v1/collectors/${id}/config`)
    return response.data
  },

  async updateConfig(id: number, config: Record<string, any>): Promise<void> {
    await client.put(`/api/v1/collectors/${id}/config`, config)
  },

  async syncConfig(id: number): Promise<void> {
    await client.post(`/api/v1/collectors/${id}/config/sync`)
  },

  async startOTA(id: number, firmwareId: number, force: boolean = false): Promise<{ota_record_id: number, status: string}> {
    const response = await client.post<unknown, ApiResponse<{ota_record_id: number, status: string}>>(
      `/api/v1/ota/start`,
      { collector_id: id, firmware_id: firmwareId, force }
    )
    return response.data
  },

  async getOTAProgress(_id: number, recordId: number): Promise<OTARecord> {
    const response = await client.get<unknown, ApiResponse<OTARecord>>(
      `/api/v1/ota/progress/${recordId}`
    )
    return response.data
  },

  async getOTAHistory(id: number): Promise<OTARecord[]> {
    const response = await client.get<unknown, ApiResponse<OTARecord[]>>(`/api/v1/ota/history/${id}`)
    return response.data
  },

  async cancelOTA(_id: number, recordId: number): Promise<void> {
    await client.post(`/api/v1/ota/cancel/${recordId}`)
  },

  // 硬件配置管理
  async getHardwareConfig(id: number): Promise<Record<string, any>> {
    const response = await client.get<unknown, ApiResponse<Record<string, any>>>(`/api/v1/collectors/${id}/hardware/config`)
    return response.data
  },

  async updateHardwareConfig(id: number, hardware: Record<string, any>): Promise<void> {
    await client.put(`/api/v1/collectors/${id}/hardware/config`, { hardware })
  },

  // 硬件资源能力（新架构）
  async getCapabilities(id: number): Promise<Capabilities> {
    const response = await client.get<unknown, ApiResponse<Capabilities>>(
      `/api/v1/collectors/${id}/capabilities`
    )
    return response.data
  },

  // 向采集器下发 QueryResources，触发 ReportResources 上报并更新 DB
  async queryResources(id: number): Promise<{ request_id: string }> {
    const response = await client.post<unknown, ApiResponse<{ request_id: string }>>(
      `/api/v1/collectors/${id}/query-resources`
    )
    return response.data
  },

  // 扫描 I2C 总线设备
  async scanI2C(id: number, hardwareId: string): Promise<{ devices: string[] }> {
    const response = await client.post<unknown, ApiResponse<{ devices: string[] }>>(
      `/api/v1/collectors/${id}/bus/i2c/scan`,
      { hardware_id: hardwareId }
    )
    return response.data
  },

  async ping(id: number): Promise<{ timestamp_us: string }> {
    const response = await client.post<unknown, ApiResponse<{ timestamp_us: string }>>(
      `/api/v1/collectors/${id}/ping`
    )
    return response.data
  },
}
