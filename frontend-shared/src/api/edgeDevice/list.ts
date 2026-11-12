/**
 * 边缘设备**在役设备集合域**（自 api/edgeDevice.ts 按域搬出，方法体一字未改）。
 *
 * 边界依据：本域只负责「集合视图 + 单条增删改」，全部落在 /api/v1/edge-devices 及
 * 其 batch-* 批量端点上；单设备详情在 detail.ts，已软删设备在 deleted.ts。
 */
import client, { type ApiEnvelope } from '../client'
import { normalizeBatchResult, normalizeList } from './normalize'
import type {
  BatchUpdateResult,
  CreateEdgeDeviceParams,
  EdgeDeviceListParams,
  EdgeDeviceListResponse,
} from './types'

export const listApi = {
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
}
