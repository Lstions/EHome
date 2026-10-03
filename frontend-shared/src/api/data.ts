import client, { type ApiEnvelope } from './client'

export interface Overview {
  nodes: {
    total: number
    online: number
    offline: number
  }
  edge_devices: {
    total: number
    online: number
    offline: number
    /**
     * 已创建但尚未收到任何数据（后端 EdgeDeviceStatusPending）。
     * 2026-10-03：后端新增该字段后，Dashboard 原先用 `total - online` 推 offline，
     * 会把新建设备在 ~60s 的 pending 窗口内算成「离线设备」。
     * 旧后端不返回时为 undefined，按 0 处理。
     */
    pending?: number
  }
  latest_data: Array<{
    device_id: number
    device_name: string
    node_name: string
    data: Record<string, any>
    collected_at: string
    raw_data?: string
    error_code?: number
  }>
}

export const dataApi = {
  async getOverview(): Promise<Overview> {
    const response = await client.get<unknown, ApiEnvelope<Overview>>('/api/v1/overview')
    // Interceptor returns {code, data, message} → response.data = the overview
    return response.data
  },

  async getNodeDevicesData(nodeId: number, params: {
    start_time: string
    end_time: string
  }): Promise<any> {
    const response = await client.get<unknown, ApiEnvelope<any>>(`/api/v1/nodes/${nodeId}/latest`, { params })
    return response.data
  }
}
