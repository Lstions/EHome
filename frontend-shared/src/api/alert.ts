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
  async listEvents(params?: AlertEventListParams): Promise<AlertEvent[]> {
    return unwrap<AlertEvent[]>(client.get('/api/v1/alert-events', { params }))
  },
  async markEventsRead(ids: number[]): Promise<void> {
    await client.post('/api/v1/alert-events/read', { ids })
  },
  async markAllEventsRead(): Promise<void> {
    await client.post('/api/v1/alert-events/read', { all: true })
  },
}
