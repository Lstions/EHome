import client, { type ApiEnvelope } from './client'

/** 数据源状态 */
export type DataSourceStatus = 'active' | 'standby' | 'error' | 'disabled'

/** 数据源来源类型 (v1.0 仅 edge_device, 其余枚举预留) */
export type DataSourceSourceType = 'edge_device'

/** 故障切换原因 */
export type FailoverReason = 'auto' | 'manual' | 'manual_deactivate'

/** 故障切换触发信号 (auto 时非空) */
export type FailoverTrigger = 'device_offline' | 'stale_data' | ''

/**
 * 数据源主备记录 (设计 §5/§7)。
 *
 * 2026-10-23（逻辑设备移除）：组键从 (device_id, category) 收敛为
 * (edge_device_id, category) —— 原 device_id 是**逻辑设备** id，随逻辑身份层
 * 一起从表里删除（契约 §6）。所以本类型不再有 device_id 字段。
 */
export interface DataSource {
  id: number
  /** 数据类别（与 edge_device_id 一起构成组键） */
  category: string
  /** 边缘设备 ID：既是组键的一半，也是提供数据的设备本身 */
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
  /** 边缘设备 ID（原 device_id = 逻辑设备 id，随逻辑层删除后改名） */
  edge_device_id: number
  category: string
  status: 'failure' | 'transition'
  message: string
  response_time: number
  created_at: string
}

/** 故障切换日志 */
export interface FailoverLog {
  id: number
  /** 边缘设备 ID（原 device_id = 逻辑设备 id，随逻辑层删除后改名） */
  edge_device_id: number
  category: string
  from_source_id: number
  to_source_id: number
  reason: FailoverReason
  trigger: FailoverTrigger
  created_at: string
}

export interface DataSourceListParams {
  edge_device_id?: number
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

/** 拦截器返回后端统一 envelope；只从 envelope.data 取值。 */
async function unwrap<T>(p: Promise<ApiEnvelope<T>>): Promise<T> {
  return (await p).data
}

export const dataSourceApi = {
  async list(params?: DataSourceListParams): Promise<DataSourceListResult> {
    return unwrap<DataSourceListResult>(client.get<unknown, ApiEnvelope<DataSourceListResult>>('/api/v1/data-sources', { params }))
  },
  async get(id: number): Promise<DataSource> {
    return unwrap<DataSource>(client.get<unknown, ApiEnvelope<DataSource>>(`/api/v1/data-sources/${id}`))
  },
  async create(data: CreateDataSourceRequest): Promise<DataSource> {
    return unwrap<DataSource>(client.post<unknown, ApiEnvelope<DataSource>>('/api/v1/data-sources', data))
  },
  async update(id: number, data: UpdateDataSourceRequest): Promise<DataSource> {
    return unwrap<DataSource>(client.put<unknown, ApiEnvelope<DataSource>>(`/api/v1/data-sources/${id}`, data))
  },
  // 2026-10-24（遗留处置方案 A）：remove / activate / deactivate / reset 四个方法
  // **已删除** —— 后端对应端点一并删了（组键二元化后它们恒 409/恒 400，且删掉的行
  // 会被投影器以新 id 重建）。手工干预「谁在供数」请用来源组：
  //   sourceGroup 域的 activateGroup / deactivateGroup（见 stores/sourceGroup.ts）
  // create/update 保留：它们创建的是 managed_by=manual 的行，投影器永不覆盖。
  async getHealth(id: number, limit?: number): Promise<DataSourceHealth[]> {
    return unwrap<DataSourceHealth[]>(client.get<unknown, ApiEnvelope<DataSourceHealth[]>>(`/api/v1/data-sources/${id}/health`, { params: { limit } }))
  },
  /**
   * GET /api/v1/edge-devices/:id/failover-logs?category=&limit=
   *
   * 2026-10-23（逻辑设备移除）：原路径是 /api/v1/devices/:id/failover-logs，
   * 其中 :id 是**逻辑设备 id**。逻辑身份层删除后该维度不存在，路由改挂到
   * 边缘设备上（契约 §6：组键 = (edge_device_id, category)）。
   */
  async getFailoverLogs(edgeDeviceId: number, params?: { limit?: number; category?: string }): Promise<FailoverLog[]> {
    return unwrap<FailoverLog[]>(client.get<unknown, ApiEnvelope<FailoverLog[]>>(`/api/v1/edge-devices/${edgeDeviceId}/failover-logs`, { params }))
  },
}
