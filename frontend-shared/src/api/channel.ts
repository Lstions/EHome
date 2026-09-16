// src/api/channel.ts
import client from './client'

export interface Channel {
  id?: number
  // 后端 node_id 是物理序列号(string,如 'F0F5BDFFFE02');历史声明为 number 是类型债。
  node_id: number | string
  name?: string              // 通道名称（后端自动生成，如 "I2C0_0x77"）
  // 后端 hardware_type 回大写（UART/I2C/…），历史小写声明是类型债，两侧都保留。
  hardware_type: 'uart' | 'i2c' | 'spi' | 'adc' | 'UART' | 'I2C' | 'SPI' | 'ADC'
  hardware_id: string            // "I2C0"
  interval_ms?: number      // 后端 Channel.IntervalMs（json: interval_ms）
  bus_type?: string         // 后端 Channel.BusType（json: bus_type），如 "UART"
  bus_config?: string       // 后端 Channel.BusConfig（引脚/速率等 hex 串）
  enabled?: boolean         // 后端 Channel.Enabled
  address?: string          // "0x77" 或 "10"
  // 后端 Config 是 text 列（JSON 字符串）；读侧需归一化（见 ChannelPanel typeof 判断）。
  config: string | {
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

/**
 * /channels 服务端分页的页长上界（与后端 pageSize > 200 → clamp 回 20 的约定一致）。
 * "要全量"的调用方必须显式传它 —— 不传时服务端只给默认的 20 条。
 */
export const CHANNEL_LIST_MAX_PAGE_SIZE = 200

function isChannelRecord(value: unknown): value is Channel {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

/** Remove nullish/sparse entries before channel consumers read channel fields. */
export function compactChannelList(items: unknown): Channel[] {
  if (!Array.isArray(items)) return []
  return items.filter(isChannelRecord)
}

export const channelApi = {
  // 获取通道列表
  //
  // 服务端分页（2026-09-16）：GET /channels 返回 {items,total,page,page_size}，
  // 默认 page_size=20、上界 200（见 backend/internal/api/handler_device.go）。
  //
  // ⚠️ 为什么缺省 pageSize 要下发 CHANNEL_LIST_MAX_PAGE_SIZE（而不是不下发）：
  // 改造前本接口是全量返回；改造后服务端默认 page_size=20。本函数绝大多数调用方
  // （NodeOverview / NodeDetail / ChannelPanel / PeripheralControl / ChannelTerminal /
  // stores/channel）只传 nodeId，语义是「要该节点的全部通道」。
  // 若缺省就不下发，它们会从「全量」静默退化为「只取前 20 条」——少显示且无报错，
  // 正是本仓禁止的静默失败。200 是后端承认的上界，显式下发它即恢复改造前行为
  // （只有 >200 条才可能截断，而 200 远超现实通道数）。
  // 「不下发 = 默认 20」是一种行为变更，不该由「调用方没传参数」隐式触发。
  // 因此：不要把它「优化」回 if (pageSize !== undefined) 再下发。
  async getList(nodeId?: number | string, pageSize?: number): Promise<Channel[] | { items: Channel[]; total?: number }> {
    const params: Record<string, unknown> = nodeId ? { node_id: nodeId } : {}
    params.page_size = pageSize !== undefined ? pageSize : CHANNEL_LIST_MAX_PAGE_SIZE
    const response = await client.get('/api/v1/channels', { params })
    // response is the full body: { code: 200, data: { items, total, ... } }
    const body = response as { code?: number; data?: Channel[] | { items?: Channel[]; total?: number } }
    // 2xx 为成功，4xx/5xx 为业务错误（与 client.ts 拦截器逻辑一致）
    if (body.code && body.code >= 400) {
      throw new Error('获取通道列表失败')
    }
    // Unwrap: { data: { items: [...] } } -> { items: [...] }
    const inner = body.data
    if (Array.isArray(inner)) return compactChannelList(inner)
    if (inner && typeof inner === 'object' && Array.isArray(inner.items)) {
      return { ...inner, items: compactChannelList(inner.items) }
    }
    return { items: [] }
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
      hex_mode: true,  // 告知后端 data 是 hex 编码
    })
    return response.data
  },

  // 终端写入（需要 device_id，SPI/I2C 可传 read_size 指定预期读取字节数）
  async terminalWrite(id: number, deviceId: string, dataHex: string, readSize?: number): Promise<ChannelWriteResponse> {
    const body: Record<string, unknown> = {
      device_id: deviceId,
      data_hex: dataHex,
    }
    if (readSize !== undefined && readSize > 0) {
      body.read_size = readSize
    }
    const response = await client.post<unknown, { data: ChannelWriteResponse }>(`/api/v1/channels/${id}/terminal/write`, body)
    return response.data
  },

  // 扫描通道所在总线
  async scan(id: number, options?: { scan_type?: string; start_addr?: number; end_addr?: number; timeout_ms?: number }): Promise<{ channel_id: number; devices: string[] }> {
    const response = await client.post<unknown, { data: { channel_id: number; devices: string[] } }>(`/api/v1/channels/${id}/scan`, {
      scan_type: options?.scan_type ?? 'i2c',
      start_addr: options?.start_addr ?? 1,
      end_addr: options?.end_addr ?? 247,
      timeout_ms: options?.timeout_ms ?? 200,
    })
    return response.data
  },

  // 重配置通道（改波特率）
  //
  // 返回 status 语义（2026-09-16 后端修复后）：
  //   · 'reconfigured' —— bus_config 已改并触发下发；
  //   · 'unchanged'    —— 目标波特率与现值相同，未改动。
  // 修复前后端固定返回 'reconfigured' 且带 request_id（其实是**谎报成功**：
  // 既不解析 baudrate 也不下发）。故类型随之去掉 request_id，并保留 status 供调用方如实提示。
  async reconfigure(id: number, baudrate: number, clockHz: number = 0): Promise<{ status: string; baudrate?: number; bus_config?: string; node_id?: string }> {
    const response = await client.post<unknown, { data: { status: string; baudrate?: number; bus_config?: string; node_id?: string } }>(`/api/v1/channels/${id}/reconfigure`, {
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
