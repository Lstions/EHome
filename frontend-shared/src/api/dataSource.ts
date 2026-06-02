import client from './client'

// 数据源状态类型
export type DataSourceStatus = 'active' | 'standby' | 'error' | 'disabled'

// 数据源类型
export interface DataSource {
  id: number
  device_id: number
  category: string
  name: string
  description: string
  priority: number
  is_primary: boolean
  status: DataSourceStatus
  fail_count: number
  max_fail_count: number
  last_success?: string
  last_failure?: string
  config?: Record<string, any>
  created_at: string
  updated_at: string
  device?: any
}

export interface CreateDataSourceRequest {
  device_id: number
  category: string
  name: string
  description?: string
  priority?: number
  is_primary?: boolean
  max_fail_count?: number
  config?: Record<string, any>
}

export interface UpdateDataSourceRequest {
  name?: string
  description?: string
  priority?: number
  is_primary?: boolean
  max_fail_count?: number
  status?: DataSourceStatus
  config?: Record<string, any>
}

// 数据源健康记录
export interface DataSourceHealth {
  id: number
  source_id: number
  status: 'success' | 'failure'
  message: string
  response_time: number
  created_at: string
}

// 故障切换日志
export interface FailoverLog {
  id: number
  device_id: number
  category: string
  from_source_id: number
  to_source_id: number
  reason: string
  created_at: string
  from_source?: DataSource
  to_source?: DataSource
}

// 数据源API
export const dataSourceApi = {
  // 获取数据源列表
  list(params?: {
    page?: number
    page_size?: number
    device_id?: number
    category?: string
    status?: DataSourceStatus
  }) {
    return client.get<{ data: DataSource[]; total: number }>('/data-sources', { params })
  },

  // 获取数据源详情
  get(id: number) {
    return client.get<{ data: DataSource }>(`/data-sources/${id}`)
  },

  // 创建数据源
  create(data: CreateDataSourceRequest) {
    return client.post<{ data: DataSource }>('/data-sources', data)
  },

  // 更新数据源
  update(id: number, data: UpdateDataSourceRequest) {
    return client.put<{ data: DataSource }>(`/data-sources/${id}`, data)
  },

  // 删除数据源
  delete(id: number) {
    return client.delete(`/data-sources/${id}`)
  },

  // 激活数据源
  activate(id: number) {
    return client.post(`/data-sources/${id}/activate`)
  },

  // 停用数据源
  deactivate(id: number) {
    return client.post(`/data-sources/${id}/deactivate`)
  },

  // 重置数据源
  reset(id: number) {
    return client.post(`/data-sources/${id}/reset`)
  },

  // 获取数据源健康记录
  getHealth(id: number, limit?: number) {
    return client.get<{ data: DataSourceHealth[] }>(`/data-sources/${id}/health`, {
      params: { limit },
    })
  },

  // 获取故障切换日志
  getFailoverLogs(deviceId: number, limit?: number) {
    return client.get<{ data: FailoverLog[] }>(`/devices/${deviceId}/failover-logs`, {
      params: { limit },
    })
  },
}
