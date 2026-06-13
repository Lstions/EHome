import client from './client'

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
  }
  latest_data: Array<{
    device_id: number
    device_name: string
    node_name: string
    data: Record<string, any>
    collected_at: string
    raw_data?: string
  }>
}

export const dataApi = {
  async getOverview(): Promise<Overview> {
    const response = await client.get<unknown, any>('/api/v1/overview')
    // Interceptor returns {code, data, message} → response.data = the overview
    return (response as any).data
  },

  async getNodeDevicesData(nodeId: number, params: {
    start_time: string
    end_time: string
  }): Promise<any> {
    const response = await client.get<unknown, any>(`/api/v1/nodes/${nodeId}/latest`, { params })
    return (response as any).data
  }
}
