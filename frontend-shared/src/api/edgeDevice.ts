import client, { type ApiEnvelope } from './client'
import type { OperationDef } from './deviceConfig'

// M10 fix: Export DeviceStatus type for use across components
// Extended to support health status: active, warning, error, disabled
export type DeviceStatus = 'active' | 'online' | 'offline' | 'warning' | 'error' | 'disabled' | 'pending' | 'initializing' | 'unknown'

/**
 * 边缘设备**关联通道**（后端 GET /edge-devices 每个 item 携带的 channel 对象）。
 * 只声明展示所需的字段；其余字段（bus_config/address/...）按需追加。
 */
/**
 * 单个传感器的「最新值」。
 *
 * 与 EdgeDevice 的 latest_data 不同：后者是**一条**原始记录里的若干物理量
 * （对多命令设备只覆盖最后一条命令），本类型是**跨命令**聚合后的全集。
 */
export interface LatestSensorValue {
  sensor_name: string
  value: number
  unit: string
  /** 该传感器最近一次采样的时刻（各传感器独立，可能相差数秒） */
  timestamp: string
}

export interface EdgeDeviceChannel {
  id?: number
  /**
   * 通道所属节点的序列号（后端 channel.node_id，如 'F0F5BDFFFE02'）。
   * 与 EdgeDevice.node_id 同源，仅作展示兜底时的关联依据，不参与判定。
   */
  node_id?: number | string
  /** 总线名字，形如 "UART0" / "I2C1" / "SPI0" —— 才是「设备挂在哪个通道」的权威标识 */
  hardware_id?: string
  /** 总线类型枚举，后端存大写（"UART"/"I2C"/…），历史数据存在小写 */
  hardware_type?: string
  /** 后端 Channel.BusType（json: bus_type），如 "UART" */
  bus_type?: string
}

export interface EdgeDevice {
  id: number
  node_id: number | string
  channel_id: number
  node?: { id: number | string; name: string; firmware_version?: string }
  name: string
  device_type: string
  protocol: string
  hardware_type: string
  /**
   * **设备从站地址**（Modbus 地址 / I2C 地址），如 "1"、"0x76"、"7"。
   *
   * ⚠️ 它**不是**总线名，**不要**用它做「通道」展示 —— 后端 models.EdgeDevice.HardwareID
   * 的语义就是通信地址（空值按遗留默认地址 1 处理，见 backend/internal/deviceaction）。
   * 总线名在关联通道上：见 channel → EdgeDeviceChannel.hardware_id（"UART0"）。
   *
   * 历史坑：normalize() 曾把 d.hardware_id || d.channel?.hardware_id 合并进本字段，
   * 于是设备地址（真值 "1"）**赢过**总线名（"UART1"），让「通道」栏拼出误导性的 "UART 1"。
   * 本字段语义保持不变（其它调用方依赖它是地址）；通道显示一律走 channel_hardware_id。
   */
  hardware_id: string
  /**
   * 关联通道的总线名，直接取自后端 channel.hardware_id（"UART0"）。
   * 取不到时为 undefined（channel 缺失 / 该字段为空）—— 展示层必须回退为
   * UNKNOWN（'—'），不得拿 hardware_id（设备地址）冒充。
   */
  channel_hardware_id?: string
  /**
   * 归一化后的关联通道信息；后端 channel 缺失（老数据 / 紧凑列表）时为 undefined。
   * 需要总线类型（UART/I2C）等更多通道字段时用它，而不是 channel_hardware_id。
   */
  channel?: EdgeDeviceChannel
  config: Record<string, any>
  status: DeviceStatus
  last_data: Record<string, number> | null
  last_data_time: string | null
  last_error_code?: number
  created_at: string
  // 配置版本 (后端 EdgeDevice.ConfigVersion，配置清单版本标识)
  config_version?: string
  /**
   * 后端 MaxCommandsPerEdgeDevice：本设备**同时可轮询**的指令条数上限（2026-10-15）。
   * 来自 GET /edge-devices/:id 的附加字段；PUT .../commands 超过该值会整单 400 拒绝，
   * 前端据此在保存前给出可行动约束（"最多 3 条，当前已启用 5 条"）。
   * 后端未返回时为 undefined → 不展示约束，行为与改动前一致。
   */
  max_polling_commands?: number
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
  channel?: {
    id?: number
    node_id?: number | string
    hardware_type?: string
    hardware_id?: string
    bus_type?: string
  }
  hardware_id?: string
  config?: Record<string, any>
  status?: string
  last_data?: Record<string, number> | null
  last_data_at?: string | null
  max_polling_commands?: number
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
  // 注意: hardware_id = 设备从站地址（"1"/"0x76"），**不是**总线名；语义不得更改。
  // G4：改前这里是 `d.hardware_id || d.channel?.hardware_id || ''` —— 那个"兼容兜底"
  //    正好违反它头顶的注释：设备地址为空（后端默认地址 1 的合法形态）时，它把**通道
  //    总线名**填进本字段，编辑框于是以 "UART1" 打开，用户点保存就把总线名写回后端
  //    （与创建路径同一根因的回流路径）。
  //    现在：本字段**只**反映后端真实存储的设备地址，不做任何跨语义兜底；
  //    总线名一律走下方 channel_hardware_id（展示层唯一入口）。
  hardware_id: d.hardware_id || '',
  // 追加式保留关联通道（旧实现整体丢弃 d.channel，导致展示层拿不到真正的总线名）。
  channel_hardware_id: d.channel?.hardware_id || undefined,
  channel: d.channel ? { ...d.channel } : undefined,
  config: d.config || {},
  status: mapStatus(d.status),
  last_data: d.last_data || null,
  last_data_time: d.last_data_at || d.last_data_time || null,
  last_error_code: d.error_code ?? d.last_error_code ?? undefined,
  created_at: d.created_at || '',
  config_version: d.config_version || undefined,
  max_polling_commands: d.max_polling_commands ?? undefined,
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

  /**
   * 该设备**每个传感器**各自的最新值（跨命令聚合）。
   *
   * 与 getLatestData 的区别（2026-10-18 实测缺陷）：
   *   getLatestData 返回 device_data 的**一条**原始记录 —— 对 Modbus 设备够用
   *   （一条命令读出全部物理量），但对 ASCII 一问一答设备（Techfine 逆变器）
   *   只能拿到**最后执行的那条命令**的产物（实测 3/36 个字段），其余字段
   *   在库里但界面永远看不到，表现为"页面大部分是空的"。
   *
   * 本方法返回全部传感器的最新值，是详情页展示实时读数应使用的接口。
   */
  async getLatestValues(id: number): Promise<LatestSensorValue[]> {
    const response = await client.get<unknown, ApiEnvelope<LatestSensorValue[]>>(`/api/v1/edge-devices/${id}/latest-values`)
    const data = response?.data
    return Array.isArray(data) ? data : []
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

  // ⚠ 2026-10-11：此处原有 getOperationHistory()，指向 `GET /edge-devices/:id/operations/history`。
  //   该路径**后端从未实现**（实测 404），且**全仓无任何调用方**（只有它自己的单测）——
  //   属死代码。受控操作历史**已由 `deviceOperationApi.list()` 提供**（走
  //   `GET /edge-devices/:id/operations`，`DeviceControlPanel` 的执行时间线正在用它）。
  //   故删除，而不是为死代码补一个重复端点。

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
