import client, { type ApiEnvelope } from './client'

// 方案 v3.3 §九 — 逻辑设备管理端点 (GET /logical-devices, merge, preview,
// merge-jobs/:id, PUT /logical-devices/:id)。

export interface LogicalDeviceItem {
  id: number
  identity_key: string
  name: string
  device_type: string
  retention_days: number
  merged_into: number | null
  merge_status: string | null
  purge_requested: boolean
  created_at: string
  updated_at: string
  // 管理列表聚合字段 (handler_logical_device.go GET "")
  instance_count: number
  row_estimate?: number
  last_data_at?: string
}

export interface LogicalDeviceListResponse {
  items: LogicalDeviceItem[]
  /** 过滤后的**全量**逻辑设备条数 (不是当前页条数); 分页器用它算总页数 */
  total: number
  /** 后端自本任务起回显; 旧后端缺省时由 list() 以请求值兜底 */
  page?: number
  page_size?: number
}

export interface LogicalDeviceListParams {
  /** 页码, 从 1 起; 后端 <1 归 1 */
  page?: number
  /** 每页条数, 后端默认 20, 取值 [1,200] 外归 20 */
  page_size?: number
}

export interface MergePreviewSource {
  id: number
  name: string
  device_type: string
  first_data_at: string | null
  last_data_at: string | null
  row_estimate?: number
  overlap_with_others: boolean
}

export interface MergePreviewResponse {
  sources: MergePreviewSource[]
  // v3.3-N2: 新建目标将采用的 retention_days (系统级快照)
  target_retention_days: number
}

export interface MergeJob {
  id: number
  source_logical_id: number
  target_logical_id: number
  status: 'pending' | 'running' | 'done' | 'failed'
  migrated_rows: number
  total_estimate: number
  watermark_id: number
  watermark_phase: string
  retry_count: number
  created_at: string
  updated_at: string
  finished_at: string | null
}

export interface MergeResult {
  target_id: number
  job_ids: number[]
}

// 409 冲突项 (§3.4 D-1 结构) — 逐源校验失败 (存活实例/purge 已标记/已被占位)
export interface MergeConflict {
  logical_device_id: number
  logical_name: string
  reason: 'alive_instance' | 'purge_requested' | 'already_merging'
  instance_id?: number
  instance_name?: string
  node_name?: string
}

// 从 409 错误对象中提取结构化 conflicts (axios 拒绝 → ApiError.response)。
export function extractMergeConflicts(error: unknown): MergeConflict[] {
  const resp = (error as { response?: { data?: unknown } })?.response
  const data = resp?.data as { conflicts?: MergeConflict[] } | undefined
  return Array.isArray(data?.conflicts) ? data!.conflicts! : []
}

export const logicalDeviceApi = {
  // GET /api/v1/logical-devices — 管理列表 (实例数含已删, 数据量估算降级)。
  //
  // 服务端分页: page/page_size 与全站一致 (默认 1/20)。改前本方法不传任何分页参数,
  // 后端全量返回 1003 条, 页面一次性渲染 1003 行 / 28541 个 DOM 元素且无分页控件 (D2-02)。
  // 返回结构保持既有的 {items,total}, 仅纯增量读取 page/page_size 回显。
  async list(params?: LogicalDeviceListParams): Promise<LogicalDeviceListResponse> {
    const response = await client.get<unknown, ApiEnvelope<{
      items?: LogicalDeviceItem[]
      total?: number
      page?: number
      page_size?: number
    }>>('/api/v1/logical-devices', { params })
    const data = response.data
    const items: LogicalDeviceItem[] = Array.isArray(data?.items) ? data.items : []
    return {
      items,
      total: typeof data?.total === 'number' ? data.total : items.length,
      // 旧后端不回显 page/page_size 时以请求值兜底, 避免分页器算错当前页。
      page: typeof data?.page === 'number' ? data.page : (params?.page ?? 1),
      page_size: typeof data?.page_size === 'number' ? data.page_size : (params?.page_size ?? items.length),
    }
  },

  // POST /api/v1/logical-devices/merge/preview — 合并预览 (§3.4)
  async mergePreview(sourceIds: number[]): Promise<MergePreviewResponse> {
    const response = await client.post<unknown, ApiEnvelope<MergePreviewResponse>>('/api/v1/logical-devices/merge/preview', {
      source_ids: sourceIds,
    })
    return response.data
  },

  // POST /api/v1/logical-devices/merge — 发起合并 (§3.4 乐观占位, 201)。
  // 409 时 axios 抛错, 用 extractMergeConflicts(error) 取 conflicts。
  async merge(targetName: string, sourceIds: number[]): Promise<MergeResult> {
    const response = await client.post<unknown, ApiEnvelope<MergeResult>>('/api/v1/logical-devices/merge', {
      target_name: targetName,
      source_ids: sourceIds,
    })
    return response.data
  },

  // GET /api/v1/logical-devices/merge-jobs/:id — 搬迁进度轮询
  async mergeJob(jobId: number): Promise<MergeJob> {
    const response = await client.get<unknown, ApiEnvelope<MergeJob>>(`/api/v1/logical-devices/merge-jobs/${jobId}`)
    return response.data
  },

  // PUT /api/v1/logical-devices/:id — 改 name / retention_days
  async update(id: number, updates: { name?: string; retention_days?: number }): Promise<LogicalDeviceItem> {
    const response = await client.put<unknown, ApiEnvelope<LogicalDeviceItem>>(`/api/v1/logical-devices/${id}`, updates)
    return response.data
  },
}
