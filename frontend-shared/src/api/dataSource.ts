import client from './client'

/** 数据源状态 */
export type DataSourceStatus = 'active' | 'standby' | 'error' | 'disabled'

/** 数据源 */
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
  config?: Record<string, unknown>
  created_at: string
  updated_at: string
  device?: { id: number; name: string }
}

export interface CreateDataSourceRequest {
  device_id: number
  category: string
  name: string
  description?: string
  priority?: number
  is_primary?: boolean
  max_fail_count?: number
  config?: Record<string, unknown>
}

export interface UpdateDataSourceRequest {
  name?: string
  description?: string
  priority?: number
  is_primary?: boolean
  max_fail_count?: number
  status?: DataSourceStatus
  config?: Record<string, unknown>
}

/** 数据源健康记录 */
export interface DataSourceHealth {
  id: number
  source_id: number
  status: 'success' | 'failure'
  message: string
  response_time: number
  created_at: string
}

/** 故障切换日志 */
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

interface ListParams {
  page?: number
  page_size?: number
  device_id?: number
  category?: string
  status?: DataSourceStatus
}

export const dataSourceApi = {
  list(params?: ListParams) {
    return client.get<{ data: DataSource[]; total: number }>('/api/v1/data-sources', { params })
  },
  get(id: number) {
    return client.get<{ data: DataSource }>(`/api/v1/data-sources/${id}`)
  },
  create(data: CreateDataSourceRequest) {
    return client.post<{ data: DataSource }>('/api/v1/data-sources', data)
  },
  update(id: number, data: UpdateDataSourceRequest) {
    return client.put<{ data: DataSource }>(`/api/v1/data-sources/${id}`, data)
  },
  delete(id: number) {
    return client.delete(`/api/v1/data-sources/${id}`)
  },
  activate(id: number) {
    return client.post(`/api/v1/data-sources/${id}/activate`)
  },
  deactivate(id: number) {
    return client.post(`/api/v1/data-sources/${id}/deactivate`)
  },
  reset(id: number) {
    return client.post(`/api/v1/data-sources/${id}/reset`)
  },
  getHealth(id: number, limit?: number) {
    return client.get<{ data: DataSourceHealth[] }>(`/api/v1/data-sources/${id}/health`, {
      params: { limit },
    })
  },
  getFailoverLogs(deviceId: number, limit?: number) {
    return client.get<{ data: FailoverLog[] }>(`/api/v1/devices/${deviceId}/failover-logs`, {
      params: { limit },
    })
  },
}
