import client from './client'

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
  total: number
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
  // GET /api/v1/logical-devices — 管理列表 (实例数含已删, 数据量估算降级)
  async list(): Promise<LogicalDeviceListResponse> {
    const response = await client.get<unknown, any>('/api/v1/logical-devices')
    const data = response?.data ?? response
    const items: LogicalDeviceItem[] = Array.isArray(data?.items) ? data.items : []
    return { items, total: typeof data?.total === 'number' ? data.total : items.length }
  },

  // POST /api/v1/logical-devices/merge/preview — 合并预览 (§3.4)
  async mergePreview(sourceIds: number[]): Promise<MergePreviewResponse> {
    const response = await client.post<unknown, any>('/api/v1/logical-devices/merge/preview', {
      source_ids: sourceIds,
    })
    return (response?.data ?? response) as MergePreviewResponse
  },

  // POST /api/v1/logical-devices/merge — 发起合并 (§3.4 乐观占位, 201)。
  // 409 时 axios 抛错, 用 extractMergeConflicts(error) 取 conflicts。
  async merge(targetName: string, sourceIds: number[]): Promise<MergeResult> {
    const response = await client.post<unknown, any>('/api/v1/logical-devices/merge', {
      target_name: targetName,
      source_ids: sourceIds,
    })
    return (response?.data ?? response) as MergeResult
  },

  // GET /api/v1/logical-devices/merge-jobs/:id — 搬迁进度轮询
  async mergeJob(jobId: number): Promise<MergeJob> {
    const response = await client.get<unknown, any>(`/api/v1/logical-devices/merge-jobs/${jobId}`)
    return (response?.data ?? response) as MergeJob
  },

  // PUT /api/v1/logical-devices/:id — 改 name / retention_days
  async update(id: number, updates: { name?: string; retention_days?: number }): Promise<LogicalDeviceItem> {
    const response = await client.put<unknown, any>(`/api/v1/logical-devices/${id}`, updates)
    return (response?.data ?? response) as LogicalDeviceItem
  },
}
