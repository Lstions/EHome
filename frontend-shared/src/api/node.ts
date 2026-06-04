import client from './client'

interface ApiResponse<T> {
  code: number
  message: string
  data: T
}

// ============================================================
// v2.2 正式类型
// ============================================================

export interface Node {
  id: number
  node_id: string          // v2.2 字段名 (原 device_id)
  name: string
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
  // v2.2 同步机制字段
  protocol_version?: string
  config_sync_state?: 'in_sync' | 'syncing' | 'lag' | 'error' | 'unknown'
  config_epoch?: number
  last_manifest_id?: string
  last_sync_at?: string
  last_sync_id?: string

  // v2.1 兼容字段 (6 个月后删除, v2.3)
  /** @deprecated Use node_id instead */
  device_id?: string
  /** @deprecated Use node_id instead */
  collector_id?: string
}

export interface NodeListResponse {
  total: number
  page: number
  page_size: number
  items: Node[]
}

export interface NodeListParams {
  status?: string
  page?: number
  page_size?: number
}

export interface OTARecord {
  id: number
  node_id: number          // v2.2 字段名 (原 collector_id)
  firmware_id: number
  from_version: string
  to_version: string
  status: string
  progress: number
  error_message?: string
  created_at: string
  completed_at?: string

  /** @deprecated Use node_id instead */
  collector_id?: number
}

// ============================================================
// v2.1 兼容别名 (6 个月后删除, v2.3)
// ============================================================

/** @deprecated Use Node instead */
export type Collector = Node
/** @deprecated Use NodeListResponse instead */
export type CollectorListResponse = NodeListResponse
/** @deprecated Use NodeListParams instead */
export type CollectorListParams = NodeListParams

// ============================================================
// 外设相关类型定义
// ============================================================

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

// ============================================================
// v2.2 API (nodeApi) — 推荐
// ============================================================

export const nodeApi = {
  async getList(params?: NodeListParams): Promise<NodeListResponse> {
    const response = await client.get<unknown, ApiResponse<NodeListResponse>>('/api/v1/nodes', { params })
    return response.data
  },

  async getDetail(id: number): Promise<Node> {
    const response = await client.get<unknown, ApiResponse<Node>>(`/api/v1/nodes/${id}`)
    return response.data
  },

  async delete(id: number): Promise<void> {
    await client.delete(`/api/v1/nodes/${id}`)
  },

  async getConfig(id: number): Promise<Record<string, any>> {
    const response = await client.get<unknown, ApiResponse<Record<string, any>>>(`/api/v1/nodes/${id}/config`)
    return response.data
  },

  async updateConfig(id: number, config: Record<string, any>): Promise<void> {
    await client.put(`/api/v1/nodes/${id}/config`, config)
  },

  async syncConfig(id: number): Promise<void> {
    await client.post(`/api/v1/nodes/${id}/config/sync`)
  },

  async startOTA(id: number, firmwareId: number, force: boolean = false): Promise<{ota_record_id: number, status: string}> {
    const response = await client.post<unknown, ApiResponse<{ota_record_id: number, status: string}>>(
      `/api/v1/ota/start`,
      { node_id: id, firmware_id: firmwareId, force }
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
    const response = await client.get<unknown, ApiResponse<Record<string, any>>>(`/api/v1/nodes/${id}/hardware/config`)
    return response.data
  },

  async updateHardwareConfig(id: number, hardware: Record<string, any>): Promise<void> {
    await client.put(`/api/v1/nodes/${id}/hardware/config`, { hardware })
  },

  // 硬件资源能力（新架构）
  async getCapabilities(id: number): Promise<Capabilities> {
    const response = await client.get<unknown, ApiResponse<Capabilities>>(
      `/api/v1/nodes/${id}/capabilities`
    )
    return response.data
  },

  // 向节点下发 QueryResources，触发 ReportResources 上报并更新 DB
  async queryResources(id: number): Promise<{ request_id: string }> {
    const response = await client.post<unknown, ApiResponse<{ request_id: string }>>(
      `/api/v1/nodes/${id}/query-resources`
    )
    return response.data
  },

  // 扫描 I2C 总线设备
  async scanI2C(id: number, hardwareId: string): Promise<{ devices: string[] }> {
    const response = await client.post<unknown, ApiResponse<{ devices: string[] }>>(
      `/api/v1/nodes/${id}/bus/i2c/scan`,
      { hardware_id: hardwareId }
    )
    return response.data
  },

  async ping(id: number): Promise<{ timestamp_us: string }> {
    const response = await client.post<unknown, ApiResponse<{ timestamp_us: string }>>(
      `/api/v1/nodes/${id}/ping`
    )
    return response.data
  },
}

// ============================================================
// v2.1 兼容 API (collectorApi) — 6 个月后删除, v2.3
// 调用 v2.2 node 接口, 路径映射由后端双注册保证
// ============================================================

/** @deprecated Use nodeApi instead */
export const collectorApi = {
  async getList(params?: CollectorListParams): Promise<CollectorListResponse> {
    // v2.1 兼容: 调用 /api/v1/collectors (后端双注册, 重定向到 /api/v1/nodes)
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

  async getHardwareConfig(id: number): Promise<Record<string, any>> {
    const response = await client.get<unknown, ApiResponse<Record<string, any>>>(`/api/v1/collectors/${id}/hardware/config`)
    return response.data
  },

  async updateHardwareConfig(id: number, hardware: Record<string, any>): Promise<void> {
    await client.put(`/api/v1/collectors/${id}/hardware/config`, { hardware })
  },

  async getCapabilities(id: number): Promise<Capabilities> {
    const response = await client.get<unknown, ApiResponse<Capabilities>>(
      `/api/v1/collectors/${id}/capabilities`
    )
    return response.data
  },

  async queryResources(id: number): Promise<{ request_id: string }> {
    const response = await client.post<unknown, ApiResponse<{ request_id: string }>>(
      `/api/v1/collectors/${id}/query-resources`
    )
    return response.data
  },

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
