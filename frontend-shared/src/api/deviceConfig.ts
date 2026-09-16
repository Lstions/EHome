import client from './client'

interface ApiResponse<T> {
  code: number
  message: string
  data: T
}

export interface OperationParam {
  name: string
  type: 'uint8' | 'uint16' | 'int8' | 'int16' | 'int32' | 'uint32' | 'float' | 'enum' | 'string' | 'bool'
  label?: string
  min?: number
  max?: number
  step?: number
  default?: number | string
  options?: Array<{ value: number | string; label: string }>
}

export interface OperationDef {
  label: string
  type: 'read' | 'write'
  params?: OperationParam[]
  description?: string
  confirm?: boolean
}

export interface DeviceConfig {
  id: number
  name: string
  description: string
  device_type: string
  parser_id?: string
  protocol?: 'modbus' | 'stream' | 'custom' | ''
  hardware_type: 'uart' | 'i2c' | 'spi' | 'adc'
  config: Record<string, any>
  operations?: Record<string, OperationDef>
  is_default: boolean
  status: string
  created_at: string
  updated_at: string
}

export interface DeviceConfigListResponse {
  /**
   * 当前页切片。
   *
   * 2026-09-15 方言收敛：后端曾返回 `{list,total,...}`，是全仓 12 个分页端点里
   * **唯一**的 `list` 方言（其余 11 个都是 `items`）。已统一为 `items`；
   * 这里**不再保留 `list?` 兜底** —— 留兜底等于允许多套形状并存，
   * 下次有人改回 `list` 时前端会静默兼容、没人发现。
   * 后端契约由 `backend/internal/api/handler_device_dialect_test.go` 守住。
   */
  items: DeviceConfig[]
  /** 过滤后的**全量**条数（不是当前页条数）；分页器用它算总页数 */
  total: number
  page: number
  page_size: number
}

export interface DeviceConfigListParams {
  device_type?: string
  hardware_type?: string
  page?: number
  page_size?: number
}

export interface CreateDeviceConfigParams {
  name: string
  description?: string
  device_type: string
  protocol?: 'modbus' | 'stream' | 'custom' | ''
  hardware_type: 'uart' | 'i2c' | 'spi' | 'adc'
  config: Record<string, any>
  operations?: Record<string, OperationDef>
  is_default?: boolean
}

export interface UpdateDeviceConfigParams {
  // 后端 PUT /device-configs/:id 是"以当前行为底做 JSON 合并"的局部更新：只有 name 强制要求，
  // 其余字段缺失即保持原值。因此这里不能声明成必填，否则调用方只能伪造整份 payload。
  name: string
  description?: string
  device_type?: string
  protocol?: 'modbus' | 'stream' | 'custom' | ''
  hardware_type?: 'uart' | 'i2c' | 'spi' | 'adc'
  config?: Record<string, any>
  operations?: Record<string, OperationDef>
  is_default?: boolean
  status?: string
}

export const deviceConfigApi = {
  // 获取配置模板列表
  async getList(params?: DeviceConfigListParams): Promise<DeviceConfigListResponse> {
    const response = await client.get<unknown, ApiResponse<DeviceConfigListResponse>>('/api/v1/device-configs', { params })
    return response.data
  },

  // 获取配置模板详情
  async getDetail(id: number): Promise<DeviceConfig> {
    const response = await client.get<unknown, ApiResponse<DeviceConfig>>(`/api/v1/device-configs/${id}`)
    return response.data
  },

  // 创建配置模板
  async create(params: CreateDeviceConfigParams): Promise<DeviceConfig> {
    const response = await client.post<unknown, ApiResponse<DeviceConfig>>('/api/v1/device-configs', params)
    return response.data
  },

  // 更新配置模板
  async update(id: number, params: UpdateDeviceConfigParams): Promise<DeviceConfig> {
    const response = await client.put<unknown, ApiResponse<DeviceConfig>>(`/api/v1/device-configs/${id}`, params)
    return response.data
  },

  // 删除配置模板
  async delete(id: number): Promise<void> {
    await client.delete(`/api/v1/device-configs/${id}`)
  },

  // 设置为默认模板
  async setDefault(id: number): Promise<void> {
    await client.post(`/api/v1/device-configs/${id}/default`)
  },

  // 获取设备类型的默认配置
  async getDefault(deviceType: string): Promise<DeviceConfig | null> {
    try {
      const response = await client.get<unknown, ApiResponse<DeviceConfig | null>>(
        `/api/v1/device-configs/default/${deviceType}`
      )
      return response.data
    } catch {
      return null
    }
  },

  // 根据设备类型获取模板列表
  async getByDeviceType(deviceType: string): Promise<DeviceConfig[]> {
    const response = await client.get<unknown, ApiResponse<DeviceConfigListResponse>>(
      '/api/v1/device-configs',
      { params: { device_type: deviceType, page_size: 100 } }
    )
    return response.data.items
  }
}
