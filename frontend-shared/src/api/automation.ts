import client from './client'

/**
 * 自动化策略引擎 (设计/自动化策略引擎方案.md v0.1)。
 * 后端: backend/internal/api/handler_automation.go (路由 /api/v1/automation-rules|automation-events)。
 * 求值: backend/internal/automation/evaluator.go (armed→triggered→cooldown 三态)。
 */

/** 触发器类型 */
export type AutomationTriggerType =
  | 'sensor_threshold' // 传感器阈值 (主触发)
  | 'time_window'      // 时间窗口 (TriggerWindowStart/End/Edge)
  | 'device_state'     // 设备状态变化 (event 占位, P0 仅挂载)
  | 'manual'           // 手动触发

/** 比较符 (复用 alert 阈值原语) */
export type AutomationComparator = 'gt' | 'gte' | 'lt' | 'lte' | 'eq' | 'neq'

/** 时间窗口触发沿 */
export type AutomationWindowEdge = 'enter' | 'exit'

/** 动作类型 */
export type AutomationActionType =
  | 'device_command' // 设备控制命令 (经 commandexec, 9 项 availability gate)
  | 'notification'   // 纯通知 (ActionLevel 必填)

/** 通知级别 (后端映射 Notification.Type) */
export type AutomationActionLevel = 'info' | 'warning' | 'critical'

/** 自动化策略规则 */
export interface AutomationRule {
  id: number
  name: string
  /** GORM 无 default tag (default:true 会把 false 序列化为 SQLite true, 2026-08-23 实锤);
   *  默认启用由后端 Create 显式赋值 */
  enabled: boolean

  // ── Trigger ──
  trigger_type: AutomationTriggerType
  trigger_sensor_name?: string
  trigger_comparator?: AutomationComparator
  trigger_threshold?: number
  trigger_duration_sec?: number
  trigger_window_start?: string
  trigger_window_end?: string
  trigger_window_edge?: AutomationWindowEdge
  trigger_edge_device_id?: number
  /** 附加条件 AND 列表 JSON (同一解析后字段批内复核) */
  conditions_json?: string

  // ── Action ──
  action_type: AutomationActionType
  action_device_id?: number
  action_id?: string
  action_params_json?: string
  action_level?: AutomationActionLevel

  // ── 执行约束 ──
  /** 冷却期 (默认 300s), 触发后抑制 */
  cooldown_sec: number
  /** 每日最大执行次数, 0=不限 */
  max_daily_exec: number
  /** 高风险动作需人工确认 (BMS MOS 等) */
  require_confirmed: boolean

  created_at: string
  updated_at: string
}

export interface CreateAutomationRuleRequest {
  name: string
  enabled?: boolean
  trigger_type: AutomationTriggerType
  trigger_sensor_name?: string
  trigger_comparator?: AutomationComparator
  trigger_threshold?: number
  trigger_duration_sec?: number
  trigger_window_start?: string
  trigger_window_end?: string
  trigger_window_edge?: AutomationWindowEdge
  trigger_edge_device_id?: number
  conditions_json?: string
  action_type: AutomationActionType
  action_device_id?: number
  action_id?: string
  action_params_json?: string
  action_level?: AutomationActionLevel
  cooldown_sec?: number
  max_daily_exec?: number
  require_confirmed?: boolean
}

export interface UpdateAutomationRuleRequest {
  name?: string
  enabled?: boolean
  trigger_type?: AutomationTriggerType
  trigger_sensor_name?: string
  trigger_comparator?: AutomationComparator
  trigger_threshold?: number
  trigger_duration_sec?: number
  trigger_window_start?: string
  trigger_window_end?: string
  trigger_window_edge?: AutomationWindowEdge
  trigger_edge_device_id?: number
  conditions_json?: string
  action_type?: AutomationActionType
  action_device_id?: number
  action_id?: string
  action_params_json?: string
  action_level?: AutomationActionLevel
  cooldown_sec?: number
  max_daily_exec?: number
  require_confirmed?: boolean
}

/** 策略触发事件 (armed→triggered/cooldown 记录) */
export interface AutomationEvent {
  id: number
  rule_id: number
  /** armed | triggered | cooldown | executed | confirmed_pending | failed */
  state: string
  /** 触发时实际采样值 */
  value?: number
  /** 关联 commandexec 执行 ID (device_command 时) */
  execution_id?: string
  message?: string
  fired_at?: string | null
  created_at: string
}

export interface AutomationEventListParams {
  rule_id?: number
  state?: string
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

export const automationApi = {
  async listRules(params?: { enabled?: boolean; trigger_type?: AutomationTriggerType }): Promise<AutomationRule[]> {
    return unwrap<AutomationRule[]>(client.get('/api/v1/automation-rules', { params }))
  },
  async createRule(data: CreateAutomationRuleRequest): Promise<AutomationRule> {
    return unwrap<AutomationRule>(client.post('/api/v1/automation-rules', data))
  },
  async getRule(id: number): Promise<AutomationRule> {
    return unwrap<AutomationRule>(client.get(`/api/v1/automation-rules/${id}`))
  },
  async updateRule(id: number, data: UpdateAutomationRuleRequest): Promise<AutomationRule> {
    return unwrap<AutomationRule>(client.put(`/api/v1/automation-rules/${id}`, data))
  },
  async deleteRule(id: number): Promise<void> {
    await client.delete(`/api/v1/automation-rules/${id}`)
  },
  async setRuleEnabled(id: number, enabled: boolean): Promise<AutomationRule> {
    return unwrap<AutomationRule>(client.patch(`/api/v1/automation-rules/${id}/enabled`, { enabled }))
  },
  async listEvents(params?: AutomationEventListParams): Promise<AutomationEvent[]> {
    return unwrap<AutomationEvent[]>(client.get('/api/v1/automation-events', { params }))
  },
}
