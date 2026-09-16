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

/**
 * /nodes 服务端分页的页长上界（后端 clamp 语义同 /channels：page_size > 200 → 回默认 20）。
 *
 * 用途：ChannelList 的**节点筛选下拉**是一次性把节点灌进 <el-option> 的选择器，
 * 它没有任何"翻页"入口。若不显式下发上界，服务端只给 20 个（审计库实测 418 个节点），
 * 其余节点用户根本选不到 —— 这正是本仓禁止的"静默截断"。
 * 这里下发上界把可选项拉满，并在 total 超过上界时由页面**显式**提示已截断（不得静默）。
 * 与 CHANNEL_LIST_MAX_PAGE_SIZE 同一范式：值 200 是后端承认的闭区间上界。
 */
export const NODE_FILTER_MAX_PAGE_SIZE = 200

/** GET /channels 的查询参数（服务端分页 + 服务端筛选）。 */
export interface ChannelListQuery {
  /** 服务端页码（从 1 开始；后端 clamp：<1 → 1） */
  page?: number
  /** 页长（后端 clamp：<1 或 >200 → 20） */
  page_size?: number
  /**
   * 节点过滤。后端 handler_device.go 先试 ParseUint（兼容自增主键），
   * 失败则按 nodes.node_id 查 —— 因此**物理序列号字符串**（'F0F5BDFFFE02'）是首选形态。
   */
  node_id?: number | string
  /**
   * 硬件类型过滤（'uart'/'i2c'…）。后端用 UPPER(hardware_type) = UPPER(?) 比较，
   * **大小写不敏感**，故小写下拉值可直接下发。
   */
  hardware_type?: string
}

function isChannelRecord(value: unknown): value is Channel {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

/** Remove nullish/sparse entries before channel consumers read channel fields. */
export function compactChannelList(items: unknown): Channel[] {
  if (!Array.isArray(items)) return []
  return items.filter(isChannelRecord)
}

/**
 * 解开 /channels 的统一 envelope：{code,data:{items,total,page,page_size}} → {items,total,…}。
 * 4xx/5xx 业务码必须抛错（与 client.ts 拦截器逻辑一致）—— 否则"接口失败"会被
 * 解成"空列表"，页面就会用空态冒充"确实没有通道"（本仓禁止的静默失败）。
 */
function unwrapChannelList(response: unknown): Channel[] | { items: Channel[]; total?: number; page?: number; page_size?: number } {
  const body = response as { code?: number; data?: Channel[] | { items?: Channel[]; total?: number; page?: number; page_size?: number } }
  if (body.code && body.code >= 400) {
    throw new Error('获取通道列表失败')
  }
  const inner = body.data
  if (Array.isArray(inner)) return compactChannelList(inner)
  if (inner && typeof inner === 'object' && Array.isArray(inner.items)) {
    return { ...inner, items: compactChannelList(inner.items) }
  }
  return { items: [] }
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
    return unwrapChannelList(response)
  },

  /**
   * 服务端分页 + 服务端筛选的列表入口（ChannelList.vue 走这条）。
   *
   * 与上面的 getList 的区别：getList 的语义是「给我某节点的全部通道」（缺省下发上界），
   * 本函数是**真分页**：page/page_size/node_id/hardware_type 全部原样下发，并回传服务端 total。
   * 二者并存是因为既有调用方（ChannelPanel/ChannelTerminal/stores/channel…）要的是"全部"。
   */
  async getPage(query: ChannelListQuery = {}): Promise<{ items: Channel[]; total: number; page: number; page_size: number }> {
    const params: Record<string, unknown> = {
      page: query.page ?? 1,
      page_size: query.page_size ?? CHANNEL_LIST_MAX_PAGE_SIZE,
    }
    // 空串/undefined 一律不下发：后端对空串不过滤，但显式省略能让"没筛选"与"筛了空值"不可混淆。
    if (query.node_id !== undefined && query.node_id !== '') params.node_id = query.node_id
    if (query.hardware_type) params.hardware_type = query.hardware_type
    const response = await client.get('/api/v1/channels', { params })
    const inner = unwrapChannelList(response)
    if (Array.isArray(inner)) {
      // 旧后端（裸数组）不认得 page/page_size：如实按"只有一页"处理，不伪造 total。
      return { items: inner, total: inner.length, page: 1, page_size: inner.length }
    }
    const items = inner.items
    const total = typeof (inner as { total?: unknown }).total === 'number'
      ? (inner as { total: number }).total
      : items.length
    const echoed = inner as { page?: number; page_size?: number }
    return {
      items,
      total,
      // 回显后端 clamp 后的值；旧后端缺省时以请求值兜底（范式同 api/node.ts:275-280）
      page: typeof echoed.page === 'number' ? echoed.page : (query.page ?? 1),
      page_size: typeof echoed.page_size === 'number' ? echoed.page_size : (query.page_size ?? CHANNEL_LIST_MAX_PAGE_SIZE),
    }
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
