import client from './client'
import type { RiskLevel } from './deviceOperation'

/**
 * 自动化策略引擎 (设计/自动化策略引擎方案.md v0.1 + 设计/自动化确认制闭环实现方案.md)。
 * 后端: backend/internal/api/handler_automation.go (路由 /api/v1/automation-rules|automation-events)。
 * 求值: backend/internal/automation/evaluator.go (armed→triggered→cooldown 三态)。
 * 确认制闭环 (裁决 4): require_confirmed=true 触发落 pending_confirm 事件 + 纯通知,
 *   人工经 confirmEvent 确认后才真正下发 commandexec。
 */

/** 触发器类型 (与后端 models/automation.go:18-23 对齐) */
export type AutomationTriggerType =
  | 'sensor_threshold' // 数据驱动: 滑动窗口连续满足
  | 'time_window'      // 时钟驱动: 每日窗口 enter/exit/inside
  | 'sun_event'        // 时钟驱动: 每日日出/日落瞬时点事件 (用户裁决 O-4)
  | 'event'            // 事件驱动 (本期占位; 创建时后端禁配, 更新兼容存量)

/**
 * 日出/日落事件种类 (后端 models/automation.go:31-34 的 AutomationSunKind*)。
 *
 * 为什么是独立字段而不是把 "sunrise-30" 写进 time_window 的 HH:MM 字段:
 * 那个列是 size:5、"时刻"语义, 承载方言会破坏列宽与解析器 (O-4 裁决)。
 */
export type SunEventKind = 'sunrise' | 'sunset'

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
  | 'confirmation_required'   // 自动路径被确认门拦住 (规则未授权风险动作); 与 failed_dispatch 分开, 是"需人工确认"不是"故障"
  | 'partial_failure'         // 多动作执行到第 k 个失败、剩余已中止 (危险状态: 设备已被改了一半, 需人工介入)
  | 'suppressed_no_solar_event' // 极昼/极夜: 本日无日出/日落事件, 规则本日不触发 (显式审计, 不静默)

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
  /** sun_event: 日出/日落二选一 (后端 fail-closed 必填, 方案 V2) */
  trigger_sun_kind?: SunEventKind
  /** sun_event: 相对日出/日落时刻的偏移分钟, **可负** (负=提前), 范围 [-720, 720] */
  trigger_sun_offset_min?: number
  /** 规则级坐标覆盖; 缺省 (undefined) = 继承节点值 (用户裁决 O-3: 节点级为权威) */
  latitude?: number
  longitude?: number
  /** 规则级时区覆盖 (IANA 名); 缺省 = 继承节点值 */
  timezone?: string
  /** 附加条件 AND 列表 JSON (同一解析后字段批内复核) */
  conditions_json?: string

  // ── Action (兼容期镜像列 = seq 最小的动作; 多动作用下面的 actions 数组) ──
  action_type: AutomationActionType
  action_device_id?: number
  action_id?: string
  action_params_json?: string
  action_level?: AutomationActionLevel

  /**
   * 动作集合 (后端 automation_rule_actions 子表, 方案 §3.2.3 M1)。
   *
   * 为什么与单动作镜像列并存: 兼容期后端双写, 老客户端读镜像列、新客户端读本数组。
   * 本数组按 seq 升序, 是**完整的动作集合**; 前端一律以它为准。
   */
  actions?: AutomationAction[]
  /**
   * 风险动作的规则级授权时刻 (用户裁决 O-2)。只读 —— 由后端在保存时写入,
   * 客户端不得直接赋值 (写入途径是请求体的 risk_acknowledged)。
   * null/undefined = 未授权: 该规则的风险动作在自动路径上仍会被确认门拦住。
   */
  risk_acknowledged_at?: string | null

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

/**
 * 单个动作 (后端 automation_rule_actions 子表, 方案 §4.1.1)。
 *
 * risk_level / requires_confirmation 是**预检端点回填的派生值** (后端据目标设备的
 * Action Catalog 归一化后下发), 规则列表/详情的 actions 里不含这两个字段。
 * 前端只做样式映射, 不得自行判断风险等级 (防漂移门禁 F-7)。
 */
export interface AutomationAction {
  /** 1 起, 同规则内唯一, 升序串行执行 (后端唯一索引 (rule_id, seq) 兜底) */
  seq: number
  action_type: AutomationActionType
  /** device_action 必填 */
  action_device_id?: number
  /** device_action 必填 */
  action_id?: string
  /** 参数 JSON 串 (后端逐动作过 CanonicalizeParams) */
  action_params_json?: string
  /** notification 必填 */
  action_level?: AutomationActionLevel
  /** 单个动作开关 (后端缺省 true) */
  enabled?: boolean
  /** 后端下发的 2 级风险契约 (仅预检响应回填) */
  risk_level?: RiskLevel
  /** 后端下发的确认要求 (仅预检响应回填) —— 前端一律读它, 不自己算风险 */
  requires_confirmation?: boolean
}

/** 提交时的动作输入 (字段与 AutomationAction 相同, 服务端字段为只读不下发) */
export type AutomationActionInput = AutomationAction

/** 预检响应里的单个风险动作 (方案 §4.1.2)。 */
export interface PrecheckRiskAction {
  seq: number
  action_id: string
  action_device_id: number
  /** 后端回填的目标设备名 (前端不必自己查表, 避免与后端不一致) */
  action_device_name?: string
  risk_level?: RiskLevel
  /** 后端给的展示标签 (如"风险"); 缺失时前端才回落到 risk_level 映射 */
  risk_label?: string
  /**
   * **风险原因**: 后端可选的面向用户的"为什么危险"说明。
   *
   * 用户裁决 O-2 = 后端 risk_reason 字段为单一事实源; 该字段当前后端未实现
   * (设计稿 §9 O-2 记录在前端硬编码会漂移, 故不落地前端映射表)。字段缺失时
   * 前端**如实说明原因文案尚未提供**, 不得编造危险说明 (安全文案不得编造)。
   */
  risk_reason?: string
  /** 后端给的参数摘要 (缺失时前端用 action_params_json 自行格式化) */
  params_summary?: string
  /** 后端可能回带参数 JSON 原文 (若后端只给摘要则此处为空) */
  action_params_json?: string
}

/** POST /api/v1/automation-rules/precheck 响应 data (方案 §4.1.2, 只读无副作用) */
export interface PrecheckResponse {
  /** **全部**风险动作 (含 seq/设备/参数), 前端据此渲染保存前风险清单 */
  risk_actions?: PrecheckRiskAction[]
  /** 与 V1-V7 相同的校验失败 (前端可在弹窗前拦截并逐条展示) */
  blocking_errors?: string[]
  warnings?: string[]
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
  /** sun_event 必填 (后端 V2) */
  trigger_sun_kind?: SunEventKind
  /** 偏移分钟, 可负; 缺省 0 (恰好在事件时刻) */
  trigger_sun_offset_min?: number
  /** 规则级坐标/时区覆盖; 省略 = 继承节点 (null 与省略语义不同, 见后端 V2/V3) */
  latitude?: number
  longitude?: number
  timezone?: string
  conditions_json?: string
  action_type?: AutomationActionType
  action_device_id?: number
  action_id?: string
  action_params_json?: string
  action_level?: AutomationActionLevel
  /** 多动作新形态: 与旧单动作字段二选一, 同时给且不一致时后端 400 */
  actions?: AutomationActionInput[]
  cooldown_sec?: number
  max_daily_exec?: number
  require_confirmed?: boolean
  /**
   * O-2 规则级授权: 保存含风险动作的规则时, 用户已在风险清单弹窗逐项确认。
   * 注意这与 require_confirmed=false 是**两个**条件, 后端 V7 要求二者齐备才放行。
   */
  risk_acknowledged?: boolean
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
  trigger_sun_kind?: SunEventKind
  trigger_sun_offset_min?: number
  latitude?: number
  longitude?: number
  timezone?: string
  conditions_json?: string
  action_type?: AutomationActionType
  action_device_id?: number
  action_id?: string
  action_params_json?: string
  action_level?: AutomationActionLevel
  /** 非空数组时**整体替换**动作集合 (后端语义) */
  actions?: AutomationActionInput[]
  cooldown_sec?: number
  max_daily_exec?: number
  require_confirmed?: boolean
  /** 同创建: 显式确认/撤回对风险动作的规则级授权 */
  risk_acknowledged?: boolean
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
  /**
   * 多动作执行中"失败/部分失败发生在第几个动作" (1 起, 与 automation_rule_actions.seq 同域)。
   * 规则级事件 (冷却抑制/日熔断/待确认) 无动作维度, 后端给 null ⇒ 此处 undefined。
   */
  action_seq?: number | null
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
 * 用 `items` 而非 `list` —— `/device-configs` 的 `{list,...}` 旧方言已于 2026-09-15 收敛
 * （全仓 12 个分页端点现已全部使用 `items`）。
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
  /**
   * 保存前风险预检 (**只读, 无副作用** —— 方案 §8 A-12 要求该端点不得落库)。
   *
   * 为什么需要它: 风险清单弹窗必须列出"**全部**风险动作"及其目标设备/参数/原因,
   * 而风险等级的唯一事实源在后端 (deviceaction.RiskLevel)。前端自己按 action_id
   * 猜风险等级就会出现第二份判定 —— 后端改映射时前端静默漂移。
   *
   * 返回 null 表示预检端点不可用 (后端未实现 / 网络失败): 调用方**必须 fail-closed**
   * 处理 —— 风险动作清单拿不到时不得放行保存, 否则风险清单弹窗会被静默跳过。
   */
  async precheckRule(data: CreateAutomationRuleRequest): Promise<PrecheckResponse | null> {
    try {
      return await unwrap<PrecheckResponse>(client.post('/api/v1/automation-rules/precheck', data))
    } catch {
      return null
    }
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
