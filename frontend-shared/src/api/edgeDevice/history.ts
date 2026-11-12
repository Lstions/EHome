/**
 * 边缘设备**历史数据域**（自 api/edgeDevice.ts 按域搬出，方法体一字未改）。
 *
 * 边界依据：本域覆盖「历史数据」的三种操作 —— 原始采样读取（/devices/:id/history）、
 * 迁移前的数据量与时间范围估算（data-summary）、跨设备历史迁移（merge-history）。
 * 三者共享同一套 source_ids 收敛与条目归一，且都服务于删除/合并弹窗，缺一不可。
 * 实时值不在这里（属 detail.ts），列表/恢复也不在这里（list.ts / deleted.ts）。
 */
import client, { type ApiEnvelope } from '../client'
import { isObjectRecord, normalizeSourceIDs, normalizeSummaryItem } from './normalize'
import type {
  EdgeDeviceDataSummary,
  EdgeDeviceDataSummaryItem,
  MergeHistoryResult,
  MergeHistorySourceResult,
  UnifiedDataRow,
} from './types'

export const historyApi = {
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
}
