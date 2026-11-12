/**
 * 边缘设备 API 的**响应归一化层**（自 api/edgeDevice.ts 原样搬出，逻辑一字未改）。
 *
 * 边界依据：所有「后端原始形状 → 前端 EdgeDevice」的收敛都集中在这里 —— 列表、详情、
 * 恢复三条读取路径共用同一个 normalize；若把它塞进任一域文件，其余域就得反向依赖它，
 * 拆分的边界会立刻漏掉。故单列一层，且**不**经 barrel 转出（它不是对外契约）。
 *
 * 语义红线（由 EdgeDeviceAddressSemanticsGate 源码门禁钉死）：
 *   · hardware_id = 设备从站地址，只取后端真实存储值，不做任何跨语义兜底；
 *   · 总线名只走 channel_hardware_id（展示层唯一入口）。
 */
import type { OperationDef } from '../deviceConfig'
import type { BatchItemResult, DeviceStatus, EdgeDevice, EdgeDeviceDataSummaryItem } from './types'

// ============================================================
// Normalize backend fields → frontend EdgeDevice interface
// ============================================================

// RawEdgeDevice represents the raw API response from the backend
// (M8 fix: replaces `any` with proper interface for type safety)
// 拆分后仅在本层（及其同级域文件）可见：barrel 不转出它，对外契约不变。
export interface RawEdgeDevice {
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

export function isObjectRecord(value: unknown): value is Record<string, unknown> {
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

export const normalize = (d: RawEdgeDevice): EdgeDevice => ({
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

export const normalizeList = (items: unknown[]): EdgeDevice[] =>
  items.filter(isRawEdgeDevice).map(normalize)

/**
 * 把任意来源的 id 收敛为「正整数数组」（去重、保序）。
 *
 * 为什么必需：`el-select allow-create` 手输的 id 是 **string**（Element Plus
 * 用输入框原文当 option value），直接作为 JSON 数组发给 Go 的 `[]uint` 会
 * 400（`cannot unmarshal string into ... type uint`）。这里在封装层单点收敛，
 * 任何调用方都不必再各自 map(Number)（也避免漏掉一处就复发）。
 */
export function normalizeSourceIDs(sourceIds: readonly unknown[]): number[] {
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
export function normalizeBatchResult<T extends { total?: number; succeeded?: number; failed?: number; results?: unknown[] }>(
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
export function normalizeSummaryItem(raw: unknown): EdgeDeviceDataSummaryItem | null {
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
