import client from './client'

// 厂商相关类型
export interface Vendor {
  id: number
  code: string
  name: string
  description: string
  website: string
  enabled: boolean
  created_at: string
  updated_at: string
}

export interface CreateVendorRequest {
  code: string
  name: string
  description?: string
  website?: string
}

export interface UpdateVendorRequest {
  name?: string
  description?: string
  website?: string
  enabled?: boolean
}

// 设备型号相关类型
export interface DeviceModel {
  id: number
  vendor_id: number
  code: string
  name: string
  category: string
  protocol: string
  description: string
  parser_plugin: string
  config_schema: Record<string, unknown>
  enabled: boolean
  created_at: string
  updated_at: string
  vendor?: Vendor
  data_definitions?: DataDefinition[]
}

export interface DataDefinition {
  id: number
  device_model_id: number
  category: string
  name: string
  field: string
  unit: string
  precision: number
  min_value?: number
  max_value?: number
  description: string
}

export interface CreateDeviceModelRequest {
  vendor_id: number
  code: string
  name: string
  category: string
  protocol: string
  description?: string
  parser_plugin?: string
  config_schema?: Record<string, any>
  data_definitions?: DataDefinitionInput[]
}

export interface DataDefinitionInput {
  category: string
  name: string
  field: string
  unit?: string
  precision?: number
  min_value?: number
  max_value?: number
  description?: string
}

export interface UpdateDeviceModelRequest {
  name?: string
  description?: string
  parser_plugin?: string
  config_schema?: Record<string, any>
  enabled?: boolean
}

export interface DeviceCategory {
  code: string
  name: string
  description: string
  data_types: string[]
}

// 厂商API
export const vendorApi = {
  // 获取厂商列表
  list(params?: { page?: number; page_size?: number; enabled?: boolean }) {
    return client.get<{ data: Vendor[]; total: number }>('/vendors', { params })
  },

  // 获取所有启用的厂商
  listAll() {
    return client.get<{ data: Vendor[] }>('/vendors', { params: { page_size: 100, enabled: true } })
  },

  // 获取厂商详情
  get(id: number) {
    return client.get<{ data: Vendor }>(`/vendors/${id}`)
  },

  // 创建厂商
  create(data: CreateVendorRequest) {
    return client.post<{ data: Vendor }>('/vendors', data)
  },

  // 更新厂商
  update(id: number, data: UpdateVendorRequest) {
    return client.put<{ data: Vendor }>(`/vendors/${id}`, data)
  },

  // 删除厂商
  delete(id: number) {
    return client.delete(`/vendors/${id}`)
  },
}

// 设备型号API
export const deviceModelApi = {
  // 获取设备型号列表
  list(params?: { page?: number; page_size?: number; vendor_id?: number; category?: string; enabled?: boolean }) {
    return client.get<{ data: DeviceModel[]; total: number }>('/device-models', { params })
  },

  // 获取设备型号详情
  get(id: number) {
    return client.get<{ data: DeviceModel }>(`/device-models/${id}`)
  },

  // 创建设备型号
  create(data: CreateDeviceModelRequest) {
    return client.post<{ data: DeviceModel }>('/device-models', data)
  },

  // 更新设备型号
  update(id: number, data: UpdateDeviceModelRequest) {
    return client.put<{ data: DeviceModel }>(`/device-models/${id}`, data)
  },

  // 删除设备型号
  delete(id: number) {
    return client.delete(`/device-models/${id}`)
  },

  // 获取数据定义
  getDefinitions(id: number) {
    return client.get<{ data: DataDefinition[] }>(`/device-models/${id}/definitions`)
  },

  // 更新数据定义
  updateDefinitions(id: number, definitions: DataDefinitionInput[]) {
    return client.put(`/device-models/${id}/definitions`, { definitions })
  },
}

// 设备类别API
export const deviceCategoryApi = {
  // 获取设备类别列表
  list() {
    return client.get<{ data: DeviceCategory[] }>('/device-categories')
  },
}
