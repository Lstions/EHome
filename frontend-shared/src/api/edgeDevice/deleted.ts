/**
 * 边缘设备**已软删域**（自 api/edgeDevice.ts 按域搬出，方法体一字未改）。
 *
 * 边界依据：后端把「软删可恢复」与「在役」分成两套端点 —— 已删列表 /deleted、
 * 恢复 /:id/restore、彻底删除 /deleted/batch-delete；三者的语义只对这一层成立
 * （彻底删除 = 请求后台 purge，不可恢复），因此在役设备的增删改**不**放在这里。
 */
import client, { type ApiEnvelope } from '../client'
import { normalize, normalizeBatchResult, type RawEdgeDevice } from './normalize'
import type {
  BatchDeleteHistoryResult,
  DeletedEdgeDevice,
  DeletedEdgeDevicePage,
  EdgeDevice,
  RestoreEdgeDeviceParams,
} from './types'

export const deletedApi = {
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
}
