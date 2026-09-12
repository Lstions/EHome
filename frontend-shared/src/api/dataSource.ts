import client from './client'

/** 数据源状态 */
export type DataSourceStatus = 'active' | 'standby' | 'error' | 'disabled'

/** 数据源来源类型 (v1.0 仅 edge_device, 其余枚举预留) */
export type DataSourceSourceType = 'edge_device'

/** 故障切换原因 */
export type FailoverReason = 'auto' | 'manual' | 'manual_deactivate'

/** 故障切换触发信号 (auto 时非空) */
export type FailoverTrigger = 'device_offline' | 'stale_data' | ''

/** 数据源主备记录 (设计 §5/§7) */
export interface DataSource {
  id: number
  /** 逻辑设备 ID (来源组键) */
  device_id: number
  /** 数据类别 (来源组键) */
  category: string
  /** 提供数据的边缘设备 (血缘来源) */
  edge_device_id: number
  source_type: DataSourceSourceType
  name: string
  description: string
  priority: number
  is_primary: boolean
  max_fail_count: number
  fail_count: number
  status: DataSourceStatus
  last_success: string | null
  last_failure: string | null
  config: string
  created_at: string
  updated_at: string
}

export interface CreateDataSourceRequest {
  device_id: number
  category: string
  edge_device_id: number
  source_type?: DataSourceSourceType
  name?: string
  description?: string
  priority?: number
  is_primary?: boolean
  max_fail_count?: number
  config?: string
}

export interface UpdateDataSourceRequest {
  name?: string
  description?: string
  priority?: number
  is_primary?: boolean
  max_fail_count?: number
  config?: string
}

/** 数据源健康事件 (failure | transition) */
export interface DataSourceHealth {
  id: number
  source_id: number
  device_id: number
  category: string
  status: 'failure' | 'transition'
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
  reason: FailoverReason
  trigger: FailoverTrigger
  created_at: string
}

export interface DataSourceListParams {
  device_id?: number
  category?: string
  status?: DataSourceStatus
  page?: number
  page_size?: number
}

/** 列表解包结果 (后端 data 为 { items, total }) */
export interface DataSourceListResult {
  items: DataSource[]
  total: number
}

/** 拦截器返回 response.data (envelope {code,data,message}), 用 any 双跳转取 data 字段 */
type Envelope<T> = { code: number; data: T; message: string }

async function unwrap<T>(p: Promise<unknown>): Promise<T> {
  const res = (await p) as unknown as Envelope<T> | T
  if (res && typeof res === 'object' && 'data' in (res as Envelope<T>)) {
    return (res as Envelope<T>).data
  }
  return res as T
}

export const dataSourceApi = {
  async list(params?: DataSourceListParams): Promise<DataSourceListResult> {
    return unwrap<DataSourceListResult>(client.get('/api/v1/data-sources', { params }))
  },
  async get(id: number): Promise<DataSource> {
    return unwrap<DataSource>(client.get(`/api/v1/data-sources/${id}`))
  },
  async create(data: CreateDataSourceRequest): Promise<DataSource> {
    return unwrap<DataSource>(client.post('/api/v1/data-sources', data))
  },
  async update(id: number, data: UpdateDataSourceRequest): Promise<DataSource> {
    return unwrap<DataSource>(client.put(`/api/v1/data-sources/${id}`, data))
  },
  async remove(id: number): Promise<void> {
    await client.delete(`/api/v1/data-sources/${id}`)
  },
  async activate(id: number): Promise<DataSource> {
    return unwrap<DataSource>(client.post(`/api/v1/data-sources/${id}/activate`))
  },
  async deactivate(id: number): Promise<DataSource> {
    return unwrap<DataSource>(client.post(`/api/v1/data-sources/${id}/deactivate`))
  },
  async reset(id: number): Promise<DataSource> {
    return unwrap<DataSource>(client.post(`/api/v1/data-sources/${id}/reset`))
  },
  async getHealth(id: number, limit?: number): Promise<DataSourceHealth[]> {
    return unwrap<DataSourceHealth[]>(client.get(`/api/v1/data-sources/${id}/health`, { params: { limit } }))
  },
  async getFailoverLogs(deviceId: number, params?: { limit?: number; category?: string }): Promise<FailoverLog[]> {
    return unwrap<FailoverLog[]>(client.get(`/api/v1/devices/${deviceId}/failover-logs`, { params }))
  },
}
