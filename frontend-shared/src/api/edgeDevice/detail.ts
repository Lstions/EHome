/**
 * 边缘设备**详情域**（自 api/edgeDevice.ts 按域搬出，方法体一字未改）。
 *
 * 边界依据：本域是「打开某台设备」所需的取数 —— 设备自身字段（getDetail）与
 * 跨命令聚合后的每传感器最新值（getLatestValues，详情页实时读数的唯一 REST 路径）。
 * 集合视图在 list.ts；历史采样在 history.ts。
 */
import client, { type ApiEnvelope } from '../client'
import { normalize, type RawEdgeDevice } from './normalize'
import type { EdgeDevice, LatestSensorValue } from './types'

export const detailApi = {
  async getDetail(id: number): Promise<EdgeDevice> {
    // GET /edge-devices/:id 的 data 为设备对象。
    const response = await client.get<unknown, ApiEnvelope<RawEdgeDevice>>(`/api/v1/edge-devices/${id}`)
    return normalize(response.data)
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
}
