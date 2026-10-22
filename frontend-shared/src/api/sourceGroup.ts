import client, { type ApiEnvelope } from './client'

/**
 * 规则数据来源 —— 来源组（Source Group）API。
 *
 * 后端：backend/internal/api/handler_source_group.go（契约 .contract/M0-契约.md）
 * 模型：backend/internal/models/source_group.go（json tag 与本文件逐字段对齐）
 * 设计：docs/设计/规则数据来源与复合来源-方案-2026-10-22.md §4
 *
 * 一个来源组 = 一个「数据来源」产品对象：
 *   - kind=primary_backup：组内同一时刻至多一个 active 成员（R1–R11 状态机维护）；
 *   - kind=composite：多成员值按 combine 合成，**没有 active 概念**（禁激活）。
 * 组的成员**跨逻辑设备**，与 data_sources（某逻辑设备某类别的候选成员台账）是两套对象。
 */

/** 组类型（models.SourceGroupKind*） */
export type SourceGroupKind = 'primary_backup' | 'composite'

/** 复合合并算子（仅 composite；primary_backup 必须为空串） */
export type SourceGroupCombine = 'first_available' | 'avg' | 'max' | 'min' | 'sum'

/** 缺员处置（仅 composite；空串 = fail_closed） */
export type SourceGroupOnDegrade = 'fail_closed' | 'use_available' | ''

/** 组内成员运行态（与 data_sources.status 同词表、不同语义域） */
export type SourceGroupMemberStatus = 'active' | 'standby' | 'error' | 'disabled' | ''

/** 组内成员（含组内运行态，只读） */
export interface SourceGroupMember {
  id: number
  group_id: number
  /** EdgeDevice.ID（= unified_data.device_id；禁 edge_device_id，见 DS-14） */
  edge_device_id: number
  /** 后端 JOIN 边缘设备名（可选；缺省时前端用已加载设备目录回退） */
  edge_device_name?: string
  priority: number
  required: boolean
  status: SourceGroupMemberStatus
  fail_count: number
  max_fail_count: number
  last_success: string | null
  last_failure: string | null
  created_at?: string
  updated_at?: string
}

/** 来源组 */
export interface SourceGroup {
  id: number
  name: string
  kind: SourceGroupKind
  /** 组读取的物理名（= unified_data.sensor_name） */
  category: string
  combine?: SourceGroupCombine | ''
  /** composite 最少可用成员数；0 由后端归一（first_available 强制 1，P-2） */
  min_members: number
  /** 成员值年龄上限（秒）；0 = 由采集间隔推导 */
  staleness_sec: number
  on_degrade?: SourceGroupOnDegrade
  enabled: boolean
  /** 配置/成员/active 变更单调 +1；切换清窗与缓存失效依赖它 */
  revision: number
  description?: string
  members?: SourceGroupMember[]
  /** 当前 active 成员（仅 primary_backup；composite 恒 undefined/0） */
  active_member_id?: number
  created_at?: string
  updated_at?: string
}

/** 成员输入（创建/更新时提交） */
export interface SourceGroupMemberInput {
  edge_device_id: number
  priority?: number
  required?: boolean
}

export interface CreateSourceGroupRequest {
  name: string
  kind: SourceGroupKind
  category: string
  combine?: SourceGroupCombine | ''
  min_members?: number
  staleness_sec?: number
  on_degrade?: SourceGroupOnDegrade
  enabled?: boolean
  description?: string
  members: SourceGroupMemberInput[]
}

export interface UpdateSourceGroupRequest {
  name?: string
  kind?: SourceGroupKind
  category?: string
  combine?: SourceGroupCombine | ''
  min_members?: number
  staleness_sec?: number
  on_degrade?: SourceGroupOnDegrade
  enabled?: boolean
  description?: string
  members?: SourceGroupMemberInput[]
  /** 乐观锁：编辑期间若后端 revision 已变，返回 409（后端可暂忽略） */
  revision: number
}

export interface SourceGroupListParams {
  page?: number
  page_size?: number
  kind?: SourceGroupKind
  enabled?: boolean
}

export interface SourceGroupListResult {
  items: SourceGroup[]
  total: number
  page: number
  page_size: number
}

/** 组健康事件（source_group_health） */
export interface SourceGroupHealth {
  id: number
  group_id: number
  member_id: number
  event: string
  message: string
  created_at: string
}

/** 组切换日志（source_group_failover_logs） */
export interface SourceGroupFailoverLog {
  id: number
  group_id: number
  from_member_id: number
  to_member_id: number
  reason: string
  trigger: string
  created_at: string
}

async function unwrap<T>(p: Promise<ApiEnvelope<T>>): Promise<T> {
  return (await p).data
}

export const sourceGroupApi = {
  async list(params?: SourceGroupListParams): Promise<SourceGroupListResult> {
    return unwrap<SourceGroupListResult>(
      client.get<unknown, ApiEnvelope<SourceGroupListResult>>('/api/v1/source-groups', { params }),
    )
  },
  async get(id: number): Promise<SourceGroup> {
    return unwrap<SourceGroup>(client.get<unknown, ApiEnvelope<SourceGroup>>(`/api/v1/source-groups/${id}`))
  },
  async create(data: CreateSourceGroupRequest): Promise<SourceGroup> {
    return unwrap<SourceGroup>(client.post<unknown, ApiEnvelope<SourceGroup>>('/api/v1/source-groups', data))
  },
  async update(id: number, data: UpdateSourceGroupRequest): Promise<SourceGroup> {
    return unwrap<SourceGroup>(client.put<unknown, ApiEnvelope<SourceGroup>>(`/api/v1/source-groups/${id}`, data))
  },
  async remove(id: number): Promise<void> {
    await client.delete(`/api/v1/source-groups/${id}`)
  },
  /** 仅 primary_backup；composite 组后端 400（契约裁决 #6） */
  async activate(id: number): Promise<SourceGroup> {
    return unwrap<SourceGroup>(client.post<unknown, ApiEnvelope<SourceGroup>>(`/api/v1/source-groups/${id}/activate`))
  },
  async deactivate(id: number): Promise<SourceGroup> {
    return unwrap<SourceGroup>(client.post<unknown, ApiEnvelope<SourceGroup>>(`/api/v1/source-groups/${id}/deactivate`))
  },
  async health(id: number, limit = 50): Promise<SourceGroupHealth[]> {
    return unwrap<SourceGroupHealth[]>(
      client.get<unknown, ApiEnvelope<SourceGroupHealth[]>>(`/api/v1/source-groups/${id}/health`, { params: { limit } }),
    )
  },
  async failoverLogs(id: number, limit = 50): Promise<SourceGroupFailoverLog[]> {
    return unwrap<SourceGroupFailoverLog[]>(
      client.get<unknown, ApiEnvelope<SourceGroupFailoverLog[]>>(`/api/v1/source-groups/${id}/failover-logs`, {
        params: { limit },
      }),
    )
  },
}
