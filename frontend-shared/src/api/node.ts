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
  node_id: string
  name: string
  model: string
  firmware_version: string
  status: 'online' | 'offline'
  connection_type: string
  connection_quality: number
  latency_ms: number
  ping_latency_ms: number
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
  node_id: number
  firmware_id: number
  from_version: string
  to_version: string
  status: string
  progress: number
  error_message?: string
  created_at: string
  completed_at?: string
}

/** Persisted ESP32 log record returned by the node history API. */
export interface NodeLogEntry {
  id: number
  node_id: string
  level: number
  /** ESP uptime in microseconds; not a wall-clock timestamp. */
  ts: number
  tag: string
  message: string
  /** Server receipt time in RFC3339 format. */
  created_at: string
}

/**
 * Node log API query. The current backend accepts one numeric level only.
 * Wall-clock values may be Unix milliseconds or RFC3339 strings.
 */
export interface NodeLogQuery {
  from?: number | string
  to?: number | string
  level?: number
  tag?: string
  q?: string
  page?: number
  size?: number
}

export interface NodeLogPage {
  total: number
  page: number
  size: number
  logs: NodeLogEntry[]
}

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

// ============================================================
// DMA 相关类型定义
// ============================================================

export interface DmaChannelInfo {
  dma_id: number
  name: string
  dma_type: number
  capabilities: number
  max_burst: number
  state: number       // 0=free, 1=allocated, 2=disabled
  bound_to: string
  compatible_bus: number
}

export interface DmaChannelConfig {
  dma_id: number
  enabled: boolean
  bind_to: string
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
// API (nodeApi)
// ============================================================

export const nodeApi = {
  async getList(params?: NodeListParams): Promise<NodeListResponse> {
    const response = await client.get('/api/v1/nodes', { params })
    // Client interceptor returns response.data (parsed JSON body)
    // Backend v2.2 returns bare array [{...}], not an envelope
    const raw = response as any
    if (Array.isArray(raw)) {
      // Backend returns bare array [{...}], wrap it
      return {
        total: raw.length,
        page: params?.page || 1,
        page_size: params?.page_size || raw.length,
        items: raw as unknown as Node[]
      }
    }
    // If backend returns proper envelope format {code, data: {items, total, ...}, message}
    if (raw?.data) {
      const inner = raw.data
      if (inner.items) {
        return inner as NodeListResponse
      }
      // data is the list directly
      if (Array.isArray(inner)) {
        return {
          total: inner.length,
          page: params?.page || 1,
          page_size: params?.page_size || inner.length,
          items: inner as unknown as Node[]
        }
      }
    }
    // Fallback
    return { total: 0, page: 1, page_size: 20, items: [] }
  },

  async getDetail(id: number | string): Promise<Node> {
    const response = await client.get(`/api/v1/nodes/${id}`)
    // Backend may return bare object or envelope
    const raw = response as any
    if (raw?.data && typeof raw.data === 'object' && !Array.isArray(raw.data)) {
      return raw.data as Node
    }
    return raw as unknown as Node
  },

  async update(id: number | string, data: { name?: string }): Promise<void> {
    await client.put(`/api/v1/nodes/${id}`, data)
  },

  async delete(id: number | string): Promise<void> {
    await client.delete(`/api/v1/nodes/${id}`)
  },

  async getConfig(id: number | string): Promise<Record<string, any>> {
    const response = await client.get<unknown, ApiResponse<Record<string, any>>>(`/api/v1/nodes/${id}/config`)
    return response.data
  },

  async updateConfig(id: number | string, config: Record<string, any>): Promise<void> {
    await client.put(`/api/v1/nodes/${id}/config`, config)
  },

  async syncConfig(id: number | string): Promise<void> {
    await client.post(`/api/v1/nodes/${id}/config/sync`)
  },

  async startOTA(id: number | string, firmwareId: number, force: boolean = false): Promise<{ota_record_id: number, status: string}> {
    const response = await client.post<unknown, ApiResponse<{ota_record_id: number, status: string}>>(
      `/api/v1/ota/start`,
      { node_id: id, firmware_id: firmwareId, force }
    )
    return response.data
  },

  async getOTAProgress(_id: number | string, recordId: number): Promise<OTARecord> {
    const response = await client.get<unknown, ApiResponse<OTARecord>>(
      `/api/v1/ota/progress/${recordId}`
    )
    return response.data
  },

  async getOTAHistory(id: number | string): Promise<OTARecord[]> {
    const response = await client.get<unknown, ApiResponse<OTARecord[]>>(`/api/v1/ota/history/${id}`)
    return response.data
  },

  async cancelOTA(_id: number | string, recordId: number): Promise<void> {
    await client.post(`/api/v1/ota/cancel/${recordId}`)
  },

  // 硬件配置管理
  async getHardwareConfig(id: number | string): Promise<Record<string, any>> {
    const response = await client.get<unknown, ApiResponse<Record<string, any>>>(`/api/v1/nodes/${id}/hardware/config`)
    return response.data
  },

  async updateHardwareConfig(id: number | string, hardware: Record<string, any>): Promise<void> {
    await client.put(`/api/v1/nodes/${id}/hardware/config`, { hardware })
  },

  // 硬件资源能力（新架构）
  async getCapabilities(id: number | string): Promise<Capabilities> {
    const response = await client.get<unknown, ApiResponse<Capabilities>>(
      `/api/v1/nodes/${id}/capabilities`
    )
    return response.data
  },

  // 向节点下发 QueryResources，触发 ReportResources 上报并更新 DB
  async queryResources(id: number | string): Promise<{ request_id: string }> {
    const response = await client.post<unknown, ApiResponse<{ request_id: string }>>(
      `/api/v1/nodes/${id}/query-resources`
    )
    return response.data
  },

  // 扫描 I2C 总线设备
  async scanI2C(id: number | string, hardwareId: string): Promise<{ devices: string[] }> {
    const response = await client.post<unknown, ApiResponse<{ devices: string[] }>>(
      `/api/v1/nodes/${id}/bus/i2c/scan`,
      { hardware_id: hardwareId }
    )
    return response.data
  },

  async ping(id: number | string): Promise<{ timestamp_us: string }> {
    const response = await client.post<unknown, ApiResponse<{ timestamp_us: string }>>(
      `/api/v1/nodes/${id}/ping`
    )
    return response.data
  },

  // DMA 通道管理
  async getDmaChannels(id: number | string): Promise<DmaChannelInfo[]> {
    const response = await client.get<unknown, ApiResponse<{ dma_channels: DmaChannelInfo[] }>>(
      `/api/v1/nodes/${id}/dma-channels`
    )
    return response.data?.dma_channels || []
  },

  async updateDmaConfig(id: number | string, configs: DmaChannelConfig[]): Promise<void> {
    await client.put(`/api/v1/nodes/${id}/dma-config`, configs)
  },

  // v2.5: Log stream API
  async getLogConfig(id: number | string): Promise<{ stream_enabled: boolean; level: number; persist_enabled: boolean }> {
    return client.get<unknown, { stream_enabled: boolean; level: number; persist_enabled: boolean }>(`/api/v1/nodes/${id}/log-config`)
  },

  async updateLogConfig(id: number | string, data: { stream_enabled?: boolean; level?: number }): Promise<void> {
    await client.put(`/api/v1/nodes/${id}/log-config`, data)
  },

  async updateLogPersist(id: number | string, enabled: boolean): Promise<void> {
    await client.put(`/api/v1/nodes/${id}/log-persist`, { enabled })
  },

  async getNodeLogs(id: number | string, params: NodeLogQuery = {}): Promise<NodeLogPage> {
    return client.get<unknown, NodeLogPage>(`/api/v1/nodes/${id}/logs`, { params })
  },

  async deleteNodeLogs(id: number | string, before?: number | string): Promise<{ deleted: number }> {
    const params = before !== undefined ? { before } : {}
    return client.delete<unknown, { deleted: number }>(`/api/v1/nodes/${id}/logs`, { params })
  },
}
