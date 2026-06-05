// src/api/channel.ts
import client from './client'

export interface Channel {
  id?: number
  node_id: number
  name?: string              // 通道名称（后端自动生成，如 "I2C0_0x77"）
  hardware_type: 'uart' | 'i2c' | 'spi' | 'gpio' | 'adc' | 'pwm'
  hardware_id: string            // "I2C0"
  address?: string          // "0x77" 或 "10"
  config: {
    commands?: Array<{
      write?: string        // hex 字符串 "F4"
      delay_ms?: number
      key?: string
    }>
    interval_ms?: number
    device_type?: string    // 关联的设备类型，用于模板构建
  }
  status?: string
  created_at?: string
}

export const channelApi = {
  // 获取通道列表
  // Server returns: { code: 200, data: { items: Channel[], total, page, page_size } }
  // or with collector_id filter: { items: Channel[] }
  async getList(nodeId?: number): Promise<Channel[] | { items: Channel[]; total?: number }> {
    const params = nodeId ? { node_id: nodeId } : {}
    const response = await client.get('/api/v1/channels', { params })
    // response is the full body: { code: 200, data: { items, total, ... } }
    const body = response as { code?: number; data?: unknown }
    if (body.code && body.code !== 200) {
      throw new Error('获取通道列表失败')
    }
    // Unwrap: { data: { items: [...] } } -> { items: [...] }
    const inner = (body as any).data
    if (Array.isArray(inner)) return inner
    return inner || { items: [] }
  },

  // 获取单个通道
  async getById(id: number): Promise<Channel> {
    const response = await client.get<unknown, { data: Channel }>(`/api/v1/channels/${id}`)
    return response.data
  },

  // 创建通道
  async create(data: Partial<Channel>): Promise<Channel> {
    const response = await client.post<unknown, { data: Channel }>('/api/v1/channels', data)
    return response.data
  },

  // 更新通道
  async update(id: number, data: Partial<Channel>): Promise<void> {
    await client.put(`/api/v1/channels/${id}`, data)
  },

  // 删除通道
  async delete(id: number): Promise<void> {
    await client.delete(`/api/v1/channels/${id}`)
  },

  // 向通道写入数据（终端交互，只写不等，响应通过 DataReport 异步到达）
  async write(id: number, data: string): Promise<ChannelWriteResponse> {
    const response = await client.post<unknown, { data: ChannelWriteResponse }>(`/api/v1/channels/${id}/write`, {
      data,        // hex 字符串，如 "F4"
    })
    return response.data
  },

  // 扫描通道所在总线
  async scan(id: number): Promise<{ channel_id: number; devices: string[] }> {
    const response = await client.post<unknown, { data: { channel_id: number; devices: string[] } }>(`/api/v1/channels/${id}/scan`)
    return response.data
  },

  // 重配置通道（改波特率等）
  async reconfigure(id: number, baudrate: number, clockHz: number = 0): Promise<{ status: string; request_id: string }> {
    const response = await client.post<unknown, { data: { status: string; request_id: string } }>(`/api/v1/channels/${id}/reconfigure`, {
      baudrate,
      clock_hz: clockHz
    })
    return response.data
  }
}

export interface ChannelWriteResponse {
  channel_id: number
  request_id: number
  success: boolean
}
