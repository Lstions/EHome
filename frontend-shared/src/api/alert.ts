import client from './client'

/** 告警规则目标类型 */
export type AlertTargetType = 'edge_device' | 'logical_device'

/** 告警比较符 */
export type AlertComparator = 'gt' | 'gte' | 'lt' | 'lte' | 'eq' | 'neq'

/** 告警级别 (后端映射 Notification.Type: critical→error, warning→warning, info→info) */
export type AlertLevel = 'info' | 'warning' | 'critical'

/** 阈值告警规则 (方案 v0.4 §5.1.1) */
export interface AlertRule {
  id: number
  target_type: AlertTargetType
  target_id: number
  sensor_name: string
  comparator: AlertComparator
  threshold: number
  duration_sec: number
  silence_sec: number
  level: AlertLevel
  enabled: boolean
  name: string
  created_at: string
  updated_at: string
}

export interface CreateAlertRuleRequest {
  target_type: AlertTargetType
  target_id: number
  sensor_name: string
  comparator: AlertComparator
  threshold: number
  duration_sec?: number
  silence_sec?: number
  level: AlertLevel
  enabled?: boolean
  name: string
}

export interface UpdateAlertRuleRequest {
  target_type?: AlertTargetType
  target_id?: number
  sensor_name?: string
  comparator?: AlertComparator
  threshold?: number
  duration_sec?: number
  silence_sec?: number
  level?: AlertLevel
  enabled?: boolean
  name?: string
}

/** 告警事件 (含恢复; firing→resolved 同一行 episode) */
export interface AlertEvent {
  id: number
  rule_id: number
  state: 'firing' | 'resolved'
  value: number
  fired_at: string | null
  resolved_at: string | null
  notified_at: string | null
  created_at: string
}

export interface AlertEventListParams {
  rule_id?: number
  state?: 'firing' | 'resolved'
  start_time?: string
  end_time?: string
  /** 页码 (从 1 开始, 后端 <1 归 1) */
  page?: number
  /** 每页条数 (后端默认 20, 超出 [1,200] 归 20) */
  page_size?: number
}

/**
 * 告警事件分页形状 (架构与接口评估 P1.2 裁决: items + total)。
 * 与 `AutomationEventPage` 同范式; 调用方拿到的永远是分页形状,
 * 因此前端不再有「全量 or 分页」两种可能, 也就不会重演
 * 「内联分页器却本地全量渲染」的假分页。
 */
export interface AlertEventPage {
  items: AlertEvent[]
  total: number
  page: number
  page_size: number
}

/** 拦截器返回 response.data (envelope {code,data,message}), 用 any 双跳转取 data 字段 */
type Envelope<T> = { code: number; data: T; message: string }

async function unwrap<T>(p: Promise<unknown>): Promise<T> {
  const res = (await p) as unknown as Envelope<T> | T
  if (res && typeof res === 'object' && 'data' in (res as Envelope<T>)) {
    return (res as Envelope<T>).data
  }
  return res as T
}

export const alertApi = {
  async listRules(params?: { target_type?: AlertTargetType; target_id?: number; level?: AlertLevel }): Promise<AlertRule[]> {
    return unwrap<AlertRule[]>(client.get('/api/v1/alert-rules', { params }))
  },
  async createRule(data: CreateAlertRuleRequest): Promise<AlertRule> {
    return unwrap<AlertRule>(client.post('/api/v1/alert-rules', data))
  },
  async updateRule(id: number, data: UpdateAlertRuleRequest): Promise<AlertRule> {
    return unwrap<AlertRule>(client.put(`/api/v1/alert-rules/${id}`, data))
  },
  async deleteRule(id: number): Promise<void> {
    await client.delete(`/api/v1/alert-rules/${id}`)
  },
  async setRuleEnabled(id: number, enabled: boolean): Promise<AlertRule> {
    return unwrap<AlertRule>(client.patch(`/api/v1/alert-rules/${id}/enabled`, { enabled }))
  },
  /**
   * 告警事件列表 (服务端分页)。
   *
   * 后端自本任务起返回 `{items,total,page,page_size}`; 此处归一化为
   * `AlertEventPage`, 与 `automationApi.listEvents` 完全同构 ——
   * 这样视图层不可能再误把「当前页」当成「全量」。
   *
   * 与 automation 不同, 本端点**不需要**裸数组兼容分支: 旧形态是后端
   * `Limit(500)` + 裸数组, 而本次是同一任务内前后端一起改, 没有第三方
   * (仿真套件等) 消费方 (已 grep 确认 alertApi.listEvents 仅 store 一处调用)。
   */
  async listEvents(params?: AlertEventListParams): Promise<AlertEventPage> {
    const data = await unwrap<AlertEventPage>(client.get('/api/v1/alert-events', { params }))
    return {
      items: Array.isArray(data?.items) ? data.items : [],
      total: typeof data?.total === 'number' ? data.total : 0,
      page: typeof data?.page === 'number' ? data.page : (params?.page ?? 1),
      page_size: typeof data?.page_size === 'number' ? data.page_size : (params?.page_size ?? 20),
    }
  },
  async markEventsRead(ids: number[]): Promise<void> {
    await client.post('/api/v1/alert-events/read', { ids })
  },
  async markAllEventsRead(): Promise<void> {
    await client.post('/api/v1/alert-events/read', { all: true })
  },
}
