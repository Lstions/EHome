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
  list: DeviceConfig[]
  items?: DeviceConfig[]  // 兼容不同响应格式
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
  name: string
  description?: string
  device_type: string
  protocol?: 'modbus' | 'stream' | 'custom' | ''
  hardware_type: 'uart' | 'i2c' | 'spi' | 'adc'
  config: Record<string, any>
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
    return response.data.list
  }
}
