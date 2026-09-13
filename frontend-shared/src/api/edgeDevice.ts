import client, { type ApiEnvelope } from './client'
import type { OperationDef } from './deviceConfig'

// M10 fix: Export DeviceStatus type for use across components
// Extended to support health status: active, warning, error, disabled
export type DeviceStatus = 'active' | 'online' | 'offline' | 'warning' | 'error' | 'disabled' | 'pending' | 'initializing' | 'unknown'

export interface EdgeDevice {
  id: number
  node_id: number | string
  channel_id: number
  node?: { id: number | string; name: string; firmware_version?: string }
  name: string
  device_type: string
  protocol: string
  hardware_type: string
  hardware_id: string
  config: Record<string, any>
  status: DeviceStatus
  last_data: Record<string, number> | null
  last_data_time: string | null
  last_error_code?: number
  created_at: string
  // 配置版本 (后端 EdgeDevice.ConfigVersion，配置清单版本标识)
  config_version?: string
  // 数据生命周期 P0: 逻辑身份锚点 (后端 omitempty — 未建立时字段缺省为 undefined)
  logical_device_id?: number
  device_config?: { id?: number; protocol?: string; config?: Record<string, any> | string; operations?: Record<string, OperationDef> }
}

// GET /edge-devices/:id/logical-device-info 响应 (方案 v3.3 §2.1)
// row_estimate 在估算超时时由后端省略 → 前端按可选处理。
export interface LogicalDeviceInfo {
  edge_device_id: number
  name: string | null
  logical_device_id: number | null
  retention_days: number | null
  instance_count: number
  row_estimate?: number
}

// GET /edge-devices/candidates 响应项 (方案 v3.3 §1.3)。
// row_estimate 在估算超时时由后端省略 → 前端按可选处理。
export interface LogicalDeviceCandidate {
  id: number
  name: string
  device_type: string
  retention_days: number
  instance_count: number
  last_data_at: string | null
  match_weight: number
  row_estimate?: number
}

export interface CandidateQueryParams {
  type: string
  node_id?: string
  hardware_id?: string
  channel_id?: number
}

export interface EdgeDeviceListParams {
  node_id?: number | string
  device_type?: string
  status?: string
  /** 总线类型 (uart/i2c/spi/...), 服务端按 channels.hardware_type 大小写不敏感匹配 */
  hardware_type?: string
  /** 服务端全库检索 (name/type 模糊匹配, 大小写不敏感) */
  search?: string
  page?: number
  page_size?: number
}

/** GET /edge-devices 的分页信封 (架构评估 P1.2: items + total, 非 list)。 */
export interface EdgeDeviceListResponse {
  items: EdgeDevice[]
  /** 过滤后的**全量**条数 (不是当前页条数); 分页器用它算总页数 */
  total: number
  /** 后端自负债 I-11 起回显; 旧后端缺省时由 getList() 以请求值兜底 */
  page?: number
  page_size?: number
}

// 创建参数 — 精确对齐后端 CreateDTO，不继承 Partial<EdgeDevice>
// 后端 DTO: name(*string), type(*string), node_id(*string), channel_id(*uint), hardware_id(*string)
export interface CreateEdgeDeviceParams {
  name?: string
  type?: string
  node_id?: string
  channel_id?: number
  hardware_id?: string
  enabled?: boolean
  interval_ms?: number
  device_config_id?: number
  // 方案 v3.3 §3.3/§九: 继承目标逻辑设备 (可选)。指定时后端校验
  // 目标存在/type 匹配/merged_into NULL/purge_requested FALSE +
  // 存活实例唯一性; 未指定则后端新建逻辑身份。
  logical_device_id?: number
  // F: inline channel creation — when channel_id is 0/absent and channel is
  // provided, the backend creates the channel inside the same transaction.
  channel?: {
    hardware_type: string
    hardware_id?: string
    address?: string
    config?: Record<string, unknown>
    // hex-encoded pin-route payload; the wizard's inline path omits this (no
    // route to validate), a caller that supplies one gets the backend's
    // peripheral pin-conflict gate.
    bus_config?: string
  }
  // EDGE-WIZ-004: 逐指令轮询间隔 (command_id → interval_ms, 0 = 禁用)。
  // 创建接口按最终设备类型校验 schedulable command id, 未知/非轮询 id 会被拒绝;
  // 仅当驱动声明了 schedulable 轮询指令时才应携带。
  command_intervals?: Record<string, number>
}

// ============================================================
// Normalize backend fields → frontend EdgeDevice interface
// ============================================================

// RawEdgeDevice represents the raw API response from the backend
// (M8 fix: replaces `any` with proper interface for type safety)
interface RawEdgeDevice {
  id: number
  node_id?: number | string
  channel_id?: number
  node?: { id?: number | string; name?: string; firmware_version?: string }
  name?: string
  type?: string
  device_type?: string
  protocol?: string
  device_config?: { id?: number; protocol?: string; config?: Record<string, any> | string; operations?: Record<string, OperationDef> }
  hardware_type?: string
  channel?: { hardware_type?: string; hardware_id?: string }
  hardware_id?: string
  config?: Record<string, any>
  status?: string
  last_data?: Record<string, number> | null
  last_data_at?: string | null
  last_data_time?: string | null
  error_code?: number
  last_error_code?: number
  created_at?: string
  config_version?: string
  logical_device_id?: number
}

function isObjectRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function isRawEdgeDevice(value: unknown): value is RawEdgeDevice {
  return isObjectRecord(value) && value.id !== undefined && value.id !== null
}

/**
 * Keeps list consumers from receiving null, undefined, sparse, or otherwise
 * unusable entries from an API/cache boundary.
 */
export function compactEdgeDeviceList(items: unknown): EdgeDevice[] {
  if (!Array.isArray(items)) return []
  return items.filter(isRawEdgeDevice) as EdgeDevice[]
}

// M9 fix: Explicit status mapping from backend values to frontend display values
// Preserves health status values (active, warning, error, disabled) instead of collapsing to online/offline
const STATUS_MAP: Record<string, DeviceStatus> = {
  active: 'active',
  online: 'online',
  offline: 'offline',
  warning: 'warning',
  error: 'error',
  disabled: 'disabled',
  pending: 'pending',
  initializing: 'initializing',
  unknown: 'unknown',
}

function mapStatus(rawStatus?: string): DeviceStatus {
  if (!rawStatus) return 'offline'
  return STATUS_MAP[rawStatus] ?? 'unknown' // unmapped statuses become 'unknown' instead of unsafe cast
}

const normalize = (d: RawEdgeDevice): EdgeDevice => ({
  id: d.id,
  node_id: d.node_id ?? 0,
  channel_id: d.channel_id ?? 0,
  // node 名为空（节点未命名）时仍保留 id/firmware_version，展示层回退为 node_id 序列号
  node: d.node && (d.node.name || d.node.id !== undefined || d.node.firmware_version)
    ? { id: (d.node.id ?? d.node_id ?? 0) as string | number, name: d.node.name || '', firmware_version: d.node.firmware_version || undefined }
    : undefined,
  name: d.name || '',
  device_type: d.type || d.device_type || '',
  protocol: d.protocol || d.device_config?.protocol || '',
  hardware_type: d.hardware_type || d.channel?.hardware_type || '',
  hardware_id: d.hardware_id || d.channel?.hardware_id || '',
  config: d.config || {},
  status: mapStatus(d.status),
  last_data: d.last_data || null,
  last_data_time: d.last_data_at || d.last_data_time || null,
  last_error_code: d.error_code ?? d.last_error_code ?? undefined,
  created_at: d.created_at || '',
  config_version: d.config_version || undefined,
  logical_device_id: d.logical_device_id ?? undefined,
  device_config: d.device_config
})

const normalizeList = (items: unknown[]): EdgeDevice[] =>
  items.filter(isRawEdgeDevice).map(normalize)

// ============================================================
// API (edgeDeviceApi)
// ============================================================

export const edgeDeviceApi = {
  /**
   * GET /api/v1/edge-devices — 真分页列表 (负债 I-11)。
   *
   * 契约 (架构评估 P1.2 裁决): `data = { items, total, page, page_size }`,
   * total 是**过滤后全量**条数。latest-data 富化由后端只对**当前页**执行。
   *
   * 历史 (为什么必须改): 本方法原先同时兼容 `data` 是裸数组和是 {items,total}
   * 两种形状 —— 对裸数组直接 `total = data.length`, 于是后端忽略 page 参数时
   * 前端仍能拿到一个"自洽"的信封, "假分页"因此在两端都不报错。后端补分页后只认
   * 新契约, 形状不符即返回空页 (调用方展示空态, 而不是假装成功)。
   */
  async getList(params?: EdgeDeviceListParams): Promise<EdgeDeviceListResponse> {
    // 拦截器返回统一 envelope; data 为 {items,total,page,page_size}。
    const response = await client.get<unknown, ApiEnvelope<{ items: unknown[]; total?: number; page?: number; page_size?: number }>>('/api/v1/edge-devices', { params })
    const data = response?.data
    if (data && Array.isArray(data.items)) {
      return {
        total: data.total ?? data.items.length,
        page: data.page ?? params?.page ?? 1,
        page_size: data.page_size ?? params?.page_size ?? 20,
        items: normalizeList(data.items),
      }
    }
    return { total: 0, page: params?.page ?? 1, page_size: params?.page_size ?? 20, items: [] }
  },

  async getDetail(id: number): Promise<EdgeDevice> {
    // GET /edge-devices/:id 的 data 为设备对象。
    const response = await client.get<unknown, ApiEnvelope<RawEdgeDevice>>(`/api/v1/edge-devices/${id}`)
    return normalize(response.data)
  },

  async create(data: CreateEdgeDeviceParams): Promise<{id: number}> {
    // POST 的 data 为新建设备对象。
    const response = await client.post<unknown, ApiEnvelope<{ id: number }>>('/api/v1/edge-devices', data)
    return { id: response.data.id }
  },

  // 更新参数 — 对齐后端 UpdateDTO
  async update(id: number, data: CreateEdgeDeviceParams): Promise<void> {
    await client.put(`/api/v1/edge-devices/${id}`, data)
  },

  // 方案 v3.3 §2.1/§九: delete_data 可选查询参数 (默认 false = 保留历史数据)。
  // 现有调用方 delete(id) 不带第二参数 → 请求与旧版完全一致 (不附加 config)。
  async delete(id: number, options?: { delete_data?: boolean }): Promise<void> {
    if (options?.delete_data === true) {
      await client.delete(`/api/v1/edge-devices/${id}`, { params: { delete_data: 'true' } })
      return
    }
    await client.delete(`/api/v1/edge-devices/${id}`)
  },

  // 方案 v3.3 §2.2: 批量删除边缘设备。复用单删逻辑，返回每条结果汇总。
  async batchDelete(ids: number[], options?: { delete_data?: boolean }): Promise<{
    total: number
    succeeded: number
    failed: number
    results: Array<{ id: number; success: boolean; error?: string }>
  }> {
    const response = await client.post<unknown, ApiEnvelope<{
      total: number
      succeeded: number
      failed: number
      results: Array<{ id: number; success: boolean; error?: string }>
    }>>('/api/v1/edge-devices/batch-delete', {
      ids,
      delete_data: options?.delete_data === true,
    })
    return response.data
  },

  // 方案 v3.3 §2.1: 删除弹窗信息区 — 逻辑设备信息 (实例数/数据量估算/保留天数)。
  // 失败由调用方降级处理 (不显示信息区, 不阻塞删除)。
  async getLogicalDeviceInfo(id: number): Promise<LogicalDeviceInfo> {
    const response = await client.get<unknown, ApiEnvelope<LogicalDeviceInfo>>(`/api/v1/edge-devices/${id}/logical-device-info`)
    return response.data
  },

  // 方案 v3.3 §1.3/§九: 创建继承候选逻辑设备列表 (Unscoped 聚合,
  // 权重排序, 数据量估算 + 3s 超时降级)。失败由调用方降级处理。
  async getCandidates(params: CandidateQueryParams): Promise<LogicalDeviceCandidate[]> {
    const response = await client.get<unknown, ApiEnvelope<LogicalDeviceCandidate[]>>('/api/v1/edge-devices/candidates', { params })
    const data = response?.data
    return Array.isArray(data) ? data : []
  },

  async getLatestData(id: number): Promise<any> {
    const response = await client.get<unknown, ApiEnvelope<any>>(`/api/v1/edge-devices/${id}/latest-data`)
    return response.data
  },

  async getHistoryData(id: number, params: {
    start_time: string
    end_time: string
    page?: number
    page_size?: number
  }): Promise<any> {
    const response = await client.get<unknown, ApiEnvelope<any>>(`/api/v1/edge-devices/${id}/data`, { params })
    return response.data
  },

  async getOperationHistory(id: number, limit: number = 50): Promise<any[]> {
    const response = await client.get<unknown, ApiEnvelope<unknown[]>>(
      `/api/v1/edge-devices/${id}/operations/history`,
      { params: { limit } }
    )
    const data = response?.data
    return Array.isArray(data) ? data : []
  },

  // Driver command templates
  async getDriverCommands(deviceType: string): Promise<CommandTemplate[]> {
    const response = await client.get<unknown, ApiEnvelope<CommandTemplate[]>>(`/api/v1/drivers/${deviceType}/commands`)
    const data = response?.data
    return Array.isArray(data) ? data : []
  },

  // Edge device command intervals
  async getCommandIntervals(edgeDeviceId: number): Promise<CommandTemplateWithInterval[]> {
    const response = await client.get<unknown, ApiEnvelope<CommandTemplateWithInterval[]>>(`/api/v1/edge-devices/${edgeDeviceId}/commands`)
    const data = response?.data
    return Array.isArray(data) ? data : []
  },

  async updateCommandIntervals(edgeDeviceId: number, intervals: Record<string, number>): Promise<void> {
    await client.put(`/api/v1/edge-devices/${edgeDeviceId}/commands`, { intervals })
  },
}

export interface CommandTemplate {
  id: string
  name: string
  type: string
  cmd_byte: number
  write_data: string
  read_length: number
  delay_ms: number
  interval_ms: number
  schedulable: boolean
  description: string
}

export interface CommandTemplateWithInterval extends CommandTemplate {
  current_interval_ms: number
}
