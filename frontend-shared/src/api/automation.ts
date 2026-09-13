import client from './client'

/**
 * 自动化策略引擎 (设计/自动化策略引擎方案.md v0.1 + 设计/自动化确认制闭环实现方案.md)。
 * 后端: backend/internal/api/handler_automation.go (路由 /api/v1/automation-rules|automation-events)。
 * 求值: backend/internal/automation/evaluator.go (armed→triggered→cooldown 三态)。
 * 确认制闭环 (裁决 4): require_confirmed=true 触发落 pending_confirm 事件 + 纯通知,
 *   人工经 confirmEvent 确认后才真正下发 commandexec。
 */

/** 触发器类型 (与后端 models/automation.go:18-22 对齐) */
export type AutomationTriggerType =
  | 'sensor_threshold' // 数据驱动: 滑动窗口连续满足
  | 'time_window'      // 时钟驱动: 每日窗口 enter/exit/inside
  | 'event'            // 事件驱动 (本期占位; 创建时后端禁配, 更新兼容存量)

/** 比较符 (复用 alert 阈值原语) */
export type AutomationComparator = 'gt' | 'gte' | 'lt' | 'lte' | 'eq' | 'neq'

/** 时间窗口触发沿 (后端 models/automation.go:31-35, inside=窗口内每 tick 求值) */
export type AutomationWindowEdge = 'enter' | 'exit' | 'inside'

/** 动作类型 (与后端 models/automation.go:25-28 对齐) */
export type AutomationActionType =
  | 'device_action' // 走 commandexec 受控操作链路 (9 项 availability gate + 幂等 + 审计)
  | 'notification'  // 纯通知 (ActionLevel 必填; 无需 require_confirmed)

/** 通知级别 (后端映射 Notification.Type) */
export type AutomationActionLevel = 'info' | 'warning' | 'critical'

/**
 * 触发/执行结果 (AutomationEvent.result 取值, 与后端 models/automation.go:38-48 对齐)。
 * 注意: 是 result 不是 state —— 前端旧版误用 state/armed/confirmed_pending/failed 等字段名。
 */
export type AutomationEventResult =
  | 'executed'                // 已提交 commandexec 执行
  | 'pending_confirm'         // 高风险动作, 等待人工确认 (confirmEvent 闭环)
  | 'suppressed_cooldown'     // 冷却期内抑制
  | 'suppressed_daily_limit'  // 达到每日熔断上限
  | 'condition_changed'       // 触发到执行间条件失效
  | 'failed_gate'             // availability gate fail-closed
  | 'failed_dispatch'         // commandexec.Create 调用失败
  | 'notification'            // 纯通知动作已发出
  | 'expired'                 // pending_confirm 超时未确认 (24h 清扫置位)

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
  /** 高风险动作需人工确认 (BMS MOS 等); 仅 device_action 有意义 (后端 fail-closed 校验) */
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

/** 策略触发/执行审计 (后端 AutomationEvent, 一行 = 一次触发决策) */
export interface AutomationEvent {
  id: number
  rule_id: number
  /** 触发时刻 (索引) */
  triggered_at: string
  /** sensor_threshold 触发时实际采样值 */
  trigger_value?: number
  /** 触发来源: auto=求值器/ticker 自动触发, manual=手动触发 */
  trigger_source?: 'auto' | 'manual'
  /** 触发/执行结果, 取值见 AutomationEventResult */
  result: AutomationEventResult
  /** 关联 commandexecutions (device_action 执行时回填) */
  command_id?: string
  /** 失败/抑制原因 */
  detail?: string
  created_at: string
}

export interface AutomationEventListParams {
  rule_id?: number
  /** 按 result 过滤 (非旧版 state) */
  result?: AutomationEventResult
  /** 页码, 从 1 起; 后端 <1 归 1 */
  page?: number
  /** 每页条数, 后端默认 20, 取值 [1,200] 外归 20 */
  page_size?: number
}

/**
 * 分页响应 (后端 `GET /automation-events` 的 `data`)。
 * 契约依据: 架构与接口评估及优化方案 P1.2「裁决 items + total」。
 * 用 `items` 而非 `list` —— `/device-configs` 的 `{list,...}` 是待收敛的旧方言。
 */
export interface AutomationEventPage {
  items: AutomationEvent[]
  /** 过滤后的**全量**条数 (不是当前页条数); 分页器用它算总页数 */
  total: number
  page: number
  page_size: number
}

/** 裁决 4 确认制闭环: 后端 planner 即铸即销 confirmation token, 不跨请求存储。
 *  confirm 端点无 body —— 操作者身份来自 JWT (subject_id),
 *  前置仅须先调 POST /auth/manual-confirmation (核密码刷 LastLoginAt, 10min 窗)。 */

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
  /**
   * 触发历史 (服务端分页)。
   *
   * 后端自本任务起返回 `{items,total,page,page_size}`; 此处**同时兼容裸数组**,
   * 原因是 `backend/simulation/catalog/auto.go:134-144` (autoListEvents) 仍按裸数组
   * `json.Unmarshal(r.Data, &rows)` 解包, 而该文件带 `//go:build simulation` 标签、
   * 不在主门禁内, 本任务无权改动它。
   * **何时可删这段兼容**: 仿真套件同步改为读 `.items` 后, 即可把返回值收窄为
   * `AutomationEventPage` 并删除 Array.isArray 分支。
   *
   * 归一化后调用方拿到的永远是分页形状, 因此前端不再有「全量 or 分页」两种可能,
   * 也就不会重演内联分页器却本地全量渲染的假分页。
   */
  async listEvents(params?: AutomationEventListParams): Promise<AutomationEventPage> {
    const data = await unwrap<AutomationEvent[] | Partial<AutomationEventPage>>(
      client.get('/api/v1/automation-events', { params }),
    )
    if (Array.isArray(data)) {
      // 旧后端 (或仿真夹具) 裸数组: total 取数组长度, page/page_size 回显请求值。
      return {
        items: data,
        total: data.length,
        page: params?.page ?? 1,
        page_size: params?.page_size ?? data.length,
      }
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
   * 裁决 4 确认制闭环: 人工确认 pending_confirm 事件, 触发真实下发。
   * 前置: 前端须先调 POST /auth/manual-confirmation (核密码刷 LastLoginAt, 10min 窗),
   *       再调本端点 (后端即铸即销 token, 近认证门不豁免)。本端点无 body。
   * 幂等: 同一事件重复 confirm 命中同 (scope,key,hash) → 唯一索引 replay 只读不写;
   *       且 planner 条件 UPDATE (result='pending_confirm') 闸门防双发。
   */
  async confirmEvent(id: number): Promise<AutomationEvent> {
    return unwrap<AutomationEvent>(client.post(`/api/v1/automation-events/${id}/confirm`))
  },
  /**
   * 手动触发端点: 跳过条件评估与确认制 (用户点击即确认),
   * 但保留 cooldown / max_daily_exec / 日熔断安全门禁。
   * 返回落库的 AutomationEvent (含 result), 前端据此展示执行状态。
   */
  async triggerRule(id: number): Promise<AutomationEvent> {
    return unwrap<AutomationEvent>(client.post(`/api/v1/automation-rules/${id}/trigger`))
  },
}
