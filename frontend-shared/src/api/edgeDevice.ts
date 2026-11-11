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

/**
 * `GET /api/v1/devices/:id/history` 的单行（后端 models.UnifiedData）。
 *
 * ⚠ **一行 = 一个字段的一次采样**，不是"一次采集"。同一时刻解析出的 N 个物理量
 * 是 N 行，它们的 `timestamp` 完全相同（后端整帧共用一个 now）。需要"一次采集
 * 一行"的视图必须自己按 timestamp 聚合。
 */
export interface UnifiedDataRow {
  id: number
  device_id: number
  sensor_name: string
  value: number
  unit: string
  timestamp: string
  created_at: string
  /** 帧表解码路径会带（= device_id），unified_data 回落路径可能缺省 */
  edge_device_id?: number
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
  /**
   * 删除设备时勾选了「同时删除历史数据」的待清理标记（后端
   * edge_devices.data_purge_requested）。软删后由后台 purge 任务分批硬删数据、
   * 清标记并硬删设备行；在此之前设备处于「已删除 + 数据待清理」状态。
   * 逻辑设备移除（2026-10-23）后该标记从 logical_devices 迁到本表。
   */
  data_purge_requested?: boolean
  device_config?: { id?: number; protocol?: string; config?: Record<string, any> | string; operations?: Record<string, OperationDef> }
}

/**
 * GET /api/v1/edge-devices/deleted 的列表项（已软删、可恢复的设备）。
 *
 * 除常规身份字段外，额外带四个「值不值得恢复 / 还能不能恢复」的判据：
 *   · last_data_at  —— 最后一次收到数据的时刻（判断历史数据有多"新鲜"）
 *   · row_estimate  —— 该 device_id 名下数据行数估算（后端按 reltuples/有界 COUNT
 *                      估算，可能为 null —— 表示"估算不出来"，**不是 0 行**，
 *                      展示层必须区分二者）
 *   · data_purge_requested —— 删除时勾选了「同时删除历史数据」（恢复会撤回该请求）
 *   · purge_in_progress    —— 后台 DevicePurger 已认领、正在分批硬删数据
 *                             ⇒ 此时 restore 必然 409，前端必须提前禁用入口
 */
export interface DeletedEdgeDevice {
  id: number
  name: string
  /** 后端同时回 type / device_type（同值）；前端统一用 device_type */
  device_type: string
  hardware_id: string
  node_id: number | string
  channel_id: number
  deleted_at: string | null
  last_data_at: string | null
  row_estimate: number | null
  data_purge_requested?: boolean
  purge_in_progress?: boolean
  purge_started_at?: string | null
}

/**
 * GET /api/v1/edge-devices/deleted 的分页信封（2026-10-25 起后端分页）。
 * 旧后端返回裸数组时由封装层归一为「单页、total=当前条数」，不伪造更多页。
 */
export interface DeletedEdgeDevicePage {
  items: DeletedEdgeDevice[]
  total: number
  page: number
  page_size: number
}

/**
 * 批量操作的逐条结果（POST /edge-devices/batch-update 与
 * POST /edge-devices/deleted/batch-delete 共用形状）。
 *
 * 后端逐条独立事务：一条失败不影响其它条；前端必须把 failed>0 的明细展示给用户，
 * 不能只用一句"批量操作完成"掩盖部分失败。
 */
export interface BatchItemResult {
  id: number
  success: boolean
  /** 该条失败原因（来源组规则引用 / 设备不存在 / 未曾软删 等） */
  error?: string
  /** batch-update：该条写入后的 enabled */
  enabled?: boolean
  /** batch-update：该条写入后的 interval_ms */
  interval_ms?: number
  /** deleted/batch-delete：已提交后台清理队列（重复提交幂等也为 true） */
  queued?: boolean
}

export interface BatchUpdateResult {
  total: number
  succeeded: number
  failed: number
  results: BatchItemResult[]
}

export interface BatchDeleteHistoryResult {
  total: number
  succeeded: number
  failed: number
  results: BatchItemResult[]
}

/** POST /api/v1/edge-devices/:id/restore 的请求体（节点与通道必须成对且相容）。 */
export interface RestoreEdgeDeviceParams {
  node_id: string | number
  channel_id: number
  name?: string
  interval_ms?: number
}

/** data-summary 的来源状态：deleted=已软删 / live=存活在役 / missing=无设备行的孤儿历史。 */
export type DataSummarySourceState = 'deleted' | 'live' | 'missing'

/**
 * data-summary 的单个来源条目：该来源有多少行、覆盖什么时间段、处于什么状态。
 *
 * `rows` **可以为 null**：后端估算超时/失败时省略该字段。消费者必须先判空再
 * 格式化 —— 直接 `.toLocaleString()` 在非空分支之外会抛 TypeError。
 * `device_name`/`state` 是新契约字段：state=missing 表示"没有设备行，但可能有孤儿数据"。
 * `device_type`（2026-10-25 产品重做）：合并弹窗据此做「同型号」准入校验/展示。
 */
export interface EdgeDeviceDataSummaryItem {
  source_id: number
  device_name?: string
  device_type?: string
  state?: DataSummarySourceState
  rows: number | null
  first_time: string | null
  last_time: string | null
}

/**
 * GET /api/v1/edge-devices/:id/data-summary 响应（2026-10-25 新口径）。
 *
 * 目标与源**分两组返回**，不再混用：
 *   · target        —— 目标设备自身（删除确认弹窗用）
 *   · sources       —— 每个源设备一项（合并预览用）
 *   · target_rows   —— 目标自身合计（仅目标估算成功时出现）
 *   · source_rows   —— 源合计（**仅当全部源估算成功才出现**；缺失必须显示「—」，
 *                      不能兜底成 0 —— 报一个偏小的合计比不报更危险）
 * 两者绝不相加；「预计迁移行数」只能读 source_rows。
 */
export interface EdgeDeviceDataSummary {
  target: EdgeDeviceDataSummaryItem | null
  sources: EdgeDeviceDataSummaryItem[]
  target_rows?: number | null
  source_rows?: number | null
}

/**
 * merge-history 响应里的单源结果（2026-10-25 产品重做后）。
 *
 * 后端准入已收紧为「存在 + 已软删 + 与目标同型号」，因此正常响应里 state 只会是
 * deleted；unknown 仅用于响应形状异常/旧后端时的防御性回填（不谎报状态）。
 */
export type MergeHistorySourceState = 'deleted' | 'unknown'

export interface MergeHistorySourceResult {
  id: number
  state: MergeHistorySourceState
  /** 源设备型号（后端回传；理论恒等于目标型号，用于展示/自证） */
  device_type?: string
}

/** POST /api/v1/edge-devices/:id/merge-history 响应（幂等：重复调用 rows_migrated=0）。 */
export interface MergeHistoryResult {
  rows_migrated: number
  sources: MergeHistorySourceResult[]
  /** 非致命问题（如人工 data_sources 冲突保留、台账迁移失败、last_data_at 回写失败），可重试补齐 */
  warnings: string[]
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
  data_purge_requested?: boolean
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
  data_purge_requested: d.data_purge_requested ?? undefined,
  device_config: d.device_config
})

const normalizeList = (items: unknown[]): EdgeDevice[] =>
  items.filter(isRawEdgeDevice).map(normalize)

/**
 * 把任意来源的 id 收敛为「正整数数组」（去重、保序）。
 *
 * 为什么必需：`el-select allow-create` 手输的 id 是 **string**（Element Plus
 * 用输入框原文当 option value），直接作为 JSON 数组发给 Go 的 `[]uint` 会
 * 400（`cannot unmarshal string into ... type uint`）。这里在封装层单点收敛，
 * 任何调用方都不必再各自 map(Number)（也避免漏掉一处就复发）。
 */
function normalizeSourceIDs(sourceIds: readonly unknown[]): number[] {
  const out: number[] = []
  const seen = new Set<number>()
  for (const raw of sourceIds) {
    const n = Number(raw)
    if (!Number.isInteger(n) || n <= 0) continue
    if (seen.has(n)) continue
    seen.add(n)
    out.push(n)
  }
  return out
}

/** 批量操作响应的防御性归一：缺字段不伪装成成功；results 逐条收敛。 */
function normalizeBatchResult<T extends { total?: number; succeeded?: number; failed?: number; results?: unknown[] }>(
  data: T | undefined,
): { total: number; succeeded: number; failed: number; results: BatchItemResult[] } {
  const rawResults = Array.isArray(data?.results) ? data.results : []
  const results: BatchItemResult[] = []
  for (const raw of rawResults) {
    if (!isObjectRecord(raw)) continue
    const id = Number((raw as { id?: unknown }).id)
    if (!Number.isInteger(id) || id <= 0) continue
    const item: BatchItemResult = { id, success: (raw as { success?: unknown }).success === true }
    const errorRaw = (raw as { error?: unknown }).error
    if (typeof errorRaw === 'string' && errorRaw) item.error = errorRaw
    const enabledRaw = (raw as { enabled?: unknown }).enabled
    if (typeof enabledRaw === 'boolean') item.enabled = enabledRaw
    const intervalRaw = (raw as { interval_ms?: unknown }).interval_ms
    if (typeof intervalRaw === 'number') item.interval_ms = intervalRaw
    if ((raw as { queued?: unknown }).queued === true) item.queued = true
    results.push(item)
  }
  return {
    total: typeof data?.total === 'number' ? data.total : results.length,
    succeeded: typeof data?.succeeded === 'number' ? data.succeeded : results.filter(r => r.success).length,
    failed: typeof data?.failed === 'number' ? data.failed : results.filter(r => !r.success).length,
    results,
  }
}

/** data-summary 的单条目归一：非法/无 id 的条目返回 null（由调用方过滤）。 */
function normalizeSummaryItem(raw: unknown): EdgeDeviceDataSummaryItem | null {
  if (!isObjectRecord(raw)) return null
  const sourceID = Number((raw as { source_id?: unknown }).source_id)
  if (!Number.isInteger(sourceID) || sourceID <= 0) return null
  const rowsRaw = (raw as { rows?: unknown }).rows
  const firstRaw = (raw as { first_time?: unknown }).first_time
  const lastRaw = (raw as { last_time?: unknown }).last_time
  const nameRaw = (raw as { device_name?: unknown }).device_name
  const typeRaw = (raw as { device_type?: unknown }).device_type
  const stateRaw = (raw as { state?: unknown }).state
  const item: EdgeDeviceDataSummaryItem = {
    source_id: sourceID,
    rows: typeof rowsRaw === 'number' ? rowsRaw : null,
    first_time: typeof firstRaw === 'string' && firstRaw ? firstRaw : null,
    last_time: typeof lastRaw === 'string' && lastRaw ? lastRaw : null,
  }
  if (typeof nameRaw === 'string' && nameRaw) item.device_name = nameRaw
  if (typeof typeRaw === 'string' && typeRaw) item.device_type = typeRaw
  if (stateRaw === 'deleted' || stateRaw === 'live' || stateRaw === 'missing') item.state = stateRaw
  return item
}

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

  /**
   * POST /api/v1/edge-devices/batch-update — 在役设备批量管理（启用/停用/采集间隔）。
   *
   * body：`{ids, enabled?, interval_ms?}`（至少一个字段；ids 1..100）。
   * 后端逐条独立事务，返回每一条结果：来源组/规则引用的设备停用 → 该条失败并给原因，
   * 其余条照常成功（前端必须展示失败明细，不能只 toast "成功 N 台"）。
   */
  async batchUpdate(ids: number[], payload: { enabled?: boolean; interval_ms?: number }): Promise<BatchUpdateResult> {
    const body: { ids: number[]; enabled?: boolean; interval_ms?: number } = {
      ids: ids.map(Number).filter(n => Number.isInteger(n) && n > 0),
    }
    if (typeof payload.enabled === 'boolean') body.enabled = payload.enabled
    if (typeof payload.interval_ms === 'number') body.interval_ms = payload.interval_ms
    const response = await client.post<unknown, ApiEnvelope<BatchUpdateResult>>(
      '/api/v1/edge-devices/batch-update',
      body,
    )
    return normalizeBatchResult(response.data)
  },

  /**
   * POST /api/v1/edge-devices/deleted/batch-delete — 历史设备「彻底删除」。
   *
   * 语义：只接受已软删设备；逐条置 `data_purge_requested=true` 并唤醒 DevicePurger，
   * 由后台分批硬删记录+全部历史数据，**不可恢复**（与在役「删除=软删可恢复」严格区分）。
   * 重复提交（已请求清理）幂等成功；在役/不存在 id 该条失败。
   */
  async batchDeleteHistory(ids: number[]): Promise<BatchDeleteHistoryResult> {
    const body = { ids: ids.map(Number).filter(n => Number.isInteger(n) && n > 0) }
    const response = await client.post<unknown, ApiEnvelope<BatchDeleteHistoryResult>>(
      '/api/v1/edge-devices/deleted/batch-delete',
      body,
    )
    return normalizeBatchResult(response.data)
  },

  /**
   * GET /api/v1/edge-devices/deleted — 已软删（可恢复）的边缘设备列表（分页）。
   *
   * 「换节点/删除后重建」场景的第二条路：设备被软删后，它名下的
   * unified_data/device_data 行仍然挂在**同一个 device_id** 上；恢复这条
   * edge_devices 行（并改绑定）就让历史数据立刻连续可见，无需搬运任何行。
   *
   * 2026-10-25 后端改为分页：`data = {items,total,page,page_size}`。调用方必须消费
   * total（否则 >page_size 的已删设备会被静默隐藏 —— 恢复入口失去完整性）。
   * 旧后端返回裸数组时按「单页、total=当前条数」归一，不伪造更多页。
   *
   * `device_type`（2026-10-25 产品重做）：服务端精确型号过滤。合并历史弹窗只用
   * 「与目标同型号」的已删设备，必须传它而不是前端本地过滤（分页下本地过滤会漏数据）。
   *
   * `search`（2026-10-25 历史设备视图）：名称/型号模糊（大小写不敏感，服务端过滤）。
   */
  async getDeleted(params?: {
    page?: number
    page_size?: number
    device_type?: string
    search?: string
  }): Promise<DeletedEdgeDevicePage> {
    const response = await client.get<unknown, ApiEnvelope<{
      items?: DeletedEdgeDevice[]
      total?: number
      page?: number
      page_size?: number
    } | DeletedEdgeDevice[]>>(
      '/api/v1/edge-devices/deleted',
      { params },
    )
    const data = response?.data
    if (Array.isArray(data)) {
      return { items: data, total: data.length, page: 1, page_size: data.length }
    }
    const items = Array.isArray(data?.items) ? data.items : []
    return {
      items,
      total: typeof data?.total === 'number' ? data.total : items.length,
      page: typeof data?.page === 'number' ? data.page : (params?.page ?? 1),
      page_size: typeof data?.page_size === 'number' ? data.page_size : (params?.page_size ?? items.length),
    }
  },

  /**
   * POST /api/v1/edge-devices/:id/restore — 恢复已软删设备并重新绑定节点/通道。
   *
   * 成功后返回恢复后的完整 EdgeDevice（与 getDetail 同形状）。
   * 失败语义（2026-10-25 后端收紧）：
   *   · 404 设备不存在 / 节点不存在；
   *   · 400 通道不可服务（disabled / GPIO·PWM 外设 / hardware_type≠bus_type /
   *         设备配置与通道硬件类型不兼容 / 驱动声明总线不匹配）；
   *   · 409 设备未被删除、通道不属于节点、三元唯一性冲突（槽位被占）、
   *         purge 已认领（数据正在后台清理）。
   * 调用方按 feedback.handleError 展示服务端 message；前端应提前过滤通道。
   */
  async restore(id: number, body: RestoreEdgeDeviceParams): Promise<EdgeDevice> {
    const response = await client.post<unknown, ApiEnvelope<RawEdgeDevice>>(
      `/api/v1/edge-devices/${id}/restore`,
      body,
    )
    return normalize(response.data)
  },

  /**
   * GET /api/v1/edge-devices/:id/data-summary?source_ids=1,2 — 数据量/时间范围估算。
   *
   * 2026-10-25 新口径：`data = {target, sources, target_rows?, source_rows?}`。
   * 目标与源分两组；「预计迁移行数」只能读 source_rows，target_rows 只用于删除确认。
   * 兼容旧后端 `{items,total_rows}`：items[0] 视为目标自身、其余为源，此时源合计
   * 不可得（total_rows 含目标，不能拿来冒充 source_rows）⇒ source_rows 保持缺失。
   */
  async getDataSummary(id: number, sourceIds: number[]): Promise<EdgeDeviceDataSummary> {
    const valid = normalizeSourceIDs(sourceIds)
    const response = await client.get<unknown, ApiEnvelope<{
      target?: unknown
      sources?: unknown[]
      target_rows?: number | null
      source_rows?: number | null
      items?: unknown[]
      total_rows?: number | null
    }>>(
      `/api/v1/edge-devices/${id}/data-summary`,
      { params: { source_ids: valid.join(',') } },
    )
    const data = response?.data
    if (data && (data.target !== undefined || Array.isArray(data.sources))) {
      const target = normalizeSummaryItem(data.target)
      const sources = Array.isArray(data.sources)
        ? data.sources.map(normalizeSummaryItem).filter((it): it is EdgeDeviceDataSummaryItem => it !== null)
        : []
      return {
        target,
        sources,
        target_rows: typeof data.target_rows === 'number' ? data.target_rows : undefined,
        source_rows: typeof data.source_rows === 'number' ? data.source_rows : undefined,
      }
    }
    // 旧后端降级路径（仅兼容，新代码不再依赖）
    const legacy = Array.isArray(data?.items)
      ? data.items.map(normalizeSummaryItem).filter((it): it is EdgeDeviceDataSummaryItem => it !== null)
      : []
    const legacyTarget = legacy.length > 0 ? legacy[0] : null
    return {
      target: legacyTarget,
      sources: legacy.slice(1),
      target_rows: legacyTarget && typeof legacyTarget.rows === 'number' ? legacyTarget.rows : undefined,
      source_rows: undefined,
    }
  },

  /**
   * POST /api/v1/edge-devices/:id/merge-history — 把**同型号已删除**历史设备的数据并到目标设备。
   *
   * 语义（docs/分析/合并历史-产品设计重做-2026-10-25.md）：
   *   · 目标必须存活，且它自己的 type 是源设备准入基准；
   *   · 源必须「存在 + 已软删 + type 与目标完全相同 + 未被 purge 认领」；
   *   · 跨型号 400、在役源 409、无设备行 400、purge 认领中 409、源=目标 400；
   *   · 批量 UPDATE unified_data/device_data（分批 + 幂等）；台账失败 200 + warnings。
   *
   * 响应：`{rows_migrated, sources:[{id,state:'deleted',device_type}], warnings?}`。
   * 2026-10-25 产品重做已**删除** `allow_live_source` 与 `retired_sources`：在役设备
   * 根本不是合法源，不存在"确认后退役"的语义。
   *
   * 封装层强制 source_ids 收敛成正整数数组：`el-select allow-create` 产出的是
   * **字符串**，直接发出去会被 Go `[]uint` 绑定拒绝（400）。
   */
  async mergeHistory(id: number, sourceIds: number[]): Promise<MergeHistoryResult> {
    const resolved = normalizeSourceIDs(sourceIds)
    const response = await client.post<unknown, ApiEnvelope<{
      rows_migrated?: number
      sources?: unknown[]
      warnings?: unknown[]
    }>>(
      `/api/v1/edge-devices/${id}/merge-history`,
      { source_ids: resolved },
    )
    const data = response?.data
    const sources: MergeHistorySourceResult[] = []
    if (Array.isArray(data?.sources)) {
      for (const raw of data.sources) {
        if (typeof raw === 'number') {
          // 更早的响应形状（number[]）：保留但标注 unknown，不谎报状态
          if (Number.isInteger(raw) && raw > 0) sources.push({ id: raw, state: 'unknown' })
          continue
        }
        if (!isObjectRecord(raw)) continue
        const sid = Number((raw as { id?: unknown }).id)
        if (!Number.isInteger(sid) || sid <= 0) continue
        const rawState = (raw as { state?: unknown }).state
        const source: MergeHistorySourceResult = {
          id: sid,
          state: rawState === 'deleted' ? 'deleted' : 'unknown',
        }
        const typeRaw = (raw as { device_type?: unknown }).device_type
        if (typeof typeRaw === 'string' && typeRaw) source.device_type = typeRaw
        sources.push(source)
      }
    }
    if (sources.length === 0) {
      // 响应缺 sources（异常/旧后端）：如实回填请求 id + state unknown，不凭空声称成功
      for (const sid of resolved) sources.push({ id: sid, state: 'unknown' })
    }
    return {
      rows_migrated: typeof data?.rows_migrated === 'number' ? data.rows_migrated : 0,
      sources,
      warnings: Array.isArray(data?.warnings)
        ? data.warnings.filter((w): w is string => typeof w === 'string' && w.trim().length > 0)
        : [],
    }
  },

  /**
   * 该设备**每个传感器**各自的最新值（跨命令聚合）。
   *
   * 2026-11-11 原始数据退役后，这是详情页实时读数**唯一**的 REST 取数路径：
   * 原 `/latest-data`（device_data 最后一条原始记录）已随表一并删除。
   *
   * 它就是按 sensor_name 聚合后的全量快照 —— 不是"最后一条命令的产物"。
   * 历史缺陷（2026-10-18 实测）正是由后者引起的：ASCII 一问一答设备
   * （Techfine 逆变器）一个轮询周期发 9 条命令，每条各带 2~14 个物理量，
   * 取"最新那一条"只能看到最后执行的那条命令，实测 36 个字段里只显示 3 个；
   * 而其余字段在库里、界面永远看不到，表现为"页面大部分是空的"。
   */
  async getLatestValues(id: number): Promise<LatestSensorValue[]> {
    const response = await client.get<unknown, ApiEnvelope<LatestSensorValue[]>>(`/api/v1/edge-devices/${id}/latest-values`)
    const data = response?.data
    return Array.isArray(data) ? data : []
  },

  /**
   * 该设备的**原始历史采样**（一字段一行）。
   *
   * 2026-11-11 原始数据退役：原 `GET /edge-devices/:id/data`（device_data 分页）
   * 随表一并删除，历史明细改由本方法取数。响应是 `[]UnifiedData`：
   *
   *   {id, device_id, sensor_name, value, unit, timestamp, created_at, edge_device_id}
   *
   * ⚠ **一行一个字段，不是一行一次采集**。同一时刻解析出来的 N 个物理量是 N 行，
   * 且这 N 行的 timestamp **完全相等**（后端 SensorParserConsumer 用同一个
   * `now := time.Now()` 写整帧，见 consumers_heavy.go:412）。调用方若需要
   * 「一次采集一行」，必须自己按 timestamp 聚合 —— DataPanel 就是这么做的。
   *
   * 服务端**没有分页**：一次返回整个时间窗（`max_points` 只是降采样，且降采样
   * 发生在解码之后，不能省内存）。所以 `max_points` 要按「前端最多想要多少行」
   * 传，不要按页大小传 —— 传小了会把不同时刻的字段抽成残缺行。
   *
   * 内存护栏（后端 history_frame_budget.go）：帧数 × 字段数 > 300 万行直接 413。
   * 生产实测 29 字段设备 24h 约 246 万行放行，48h/168h 会 413 —— 这是**服务端
   * 拒绝**，不是前端能兜住的，调用方应把 413 转成「缩小时间范围」的提示。
   */
  async getDeviceHistory(id: number, params: {
    start_time: string
    end_time: string
    /** 可选：只看某个字段（不传 = 全部字段） */
    sensor?: string
    /** 可选：服务端降采样上限（0/不传 = 不降采样） */
    max_points?: number
  }): Promise<UnifiedDataRow[]> {
    const response = await client.get<unknown, ApiEnvelope<UnifiedDataRow[]>>(
      `/api/v1/devices/${id}/history`,
      { params },
    )
    const data = response?.data
    return Array.isArray(data) ? data : []
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
