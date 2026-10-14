import type { RiskLevel } from '@/api/deviceOperation'

/**
 * 多动作列表的纯函数工具 (方案 §3.2.3 M1 / designs/automation-sunrise-mos/README.md §5 R7-R8)。
 *
 * 为什么抽出来: "序号重排"与"风险徽标"是两条必须被单测直接钉死的规则 —— 前者决定
 * 落库的 seq 是否满足后端 V1 (1..N 连续无重复)，后者决定安全信息是否**不只靠颜色**
 * (色觉障碍用户看不到红色徽标)。内联在 .vue 里只能靠源码字符串断言。
 *
 * ⚠️ 本模块**不得**出现 4 级 risk 字面量: 风险等级的唯一事实源是后端下发的
 * risk_level (normal|elevated)，防漂移门禁见 __tests__/riskLiteralGate.spec.ts。
 */

/** 一条动作草稿 (前端形态: 参数以 JSON 串承载，与既有单动作字段同形态)。 */
export interface ActionDraft {
  /** 前端稳定标识 (仅用于 v-for key 与勾选状态, 不落库) */
  uid: number
  action_type: 'device_action' | 'notification'
  action_device_id: number
  action_id: string
  action_params_json: string
  action_level: 'info' | 'warning' | 'critical'
}

/** 可提交的动作 (seq 由数组下标派生)。 */
export interface SequencedAction {
  seq: number
  action_type: 'device_action' | 'notification'
  action_device_id?: number
  action_id?: string
  action_params_json?: string
  action_level?: 'info' | 'warning' | 'critical'
}

export function emptyAction(uid: number): ActionDraft {
  return { uid, action_type: 'device_action', action_device_id: 0, action_id: '', action_params_json: '', action_level: 'warning' }
}

/**
 * 上移/下移 (delta = -1 / +1)。
 *
 * 返回**新数组**而不是原地交换: 组件的响应式列表按引用比较，原地改会让
 * v-for 拿到同一引用、重排不触发重渲染 (设计稿 S8 的"上移生效"就会假绿)。
 * 越界 (首行上移/末行下移) 直接返回副本，不抛错 —— 界面上那两个按钮本就是 disabled。
 */
export function moveAction(actions: ActionDraft[], uid: number, delta: number): ActionDraft[] {
  const next = actions.slice()
  const index = next.findIndex(a => a.uid === uid)
  if (index < 0) return next
  const target = index + delta
  if (target < 0 || target >= next.length) return next
  const moved = next[index]
  next[index] = next[target]
  next[target] = moved
  return next
}

/**
 * 动作列表 → 请求体动作数组: **seq 按数组下标重排** (1 起, 连续无重复)。
 *
 * 为什么必须在这里重排而不是让用户填: 后端校验 V1 要求 seq 为 1..N 连续无重复，
 * 唯一索引 (rule_id, seq) 是"顺序"语义的物理保证。把序号交给界面序号派生，
 * 就不会出现"上移后 seq 还是旧的"这种落库即 400 的组合。
 */
export function toSequencedActions(actions: ActionDraft[]): SequencedAction[] {
  return actions.map((a, index) => {
    const base: SequencedAction = { seq: index + 1, action_type: a.action_type }
    if (a.action_type === 'device_action') {
      base.action_device_id = a.action_device_id
      base.action_id = a.action_id
      if (a.action_params_json) base.action_params_json = a.action_params_json
    } else {
      base.action_level = a.action_level
    }
    return base
  })
}

/** 规则响应里的动作 (含后端回填的 2 级风险字段)。 */
export interface ActionLike {
  seq?: number
  action_type?: string
  action_device_id?: number
  action_id?: string
  action_params_json?: string
  action_level?: string
  risk_level?: RiskLevel
  requires_confirmation?: boolean
}

/**
 * 风险徽标的**唯一**映射表。
 *
 * 判定源是后端下发的 risk_level (2 级契约)；旧的 risk 4 级字段是驱动出厂方言，
 * 前端一律不读 (R2 防漂移门禁)。**必须同时给出图标与文字** —— 只给颜色时
 * 红绿色盲用户读不到"风险"这一信息 (设计 D-6 四重编码: 图标 + 文字 + 徽标色 + 卡片边)。
 *
 * 未识别/缺失的等级**不渲染徽标** (fail-closed: 不猜风险等级)，由
 * requiresConfirmation() 单独兜住"是否要确认"。
 */
export function riskBadge(a: ActionLike | null | undefined): { type: 'danger'; label: string; icon: string } | null {
  if (a?.risk_level === 'elevated') return { type: 'danger', label: '风险', icon: '⚠' }
  return null
}

/** 普通动作徽标 (对照用: 无风险色，仍带文字与图标 —— 信息不靠颜色区分)。 */
export const NORMAL_BADGE = { type: 'info' as const, label: '普通', icon: '●' }

/**
 * 是否需要人工确认 —— **只读后端下发的 requires_confirmation**。
 *
 * 为什么不能自己算: 后端 deviceaction.RiskLevel.RequiresConfirmation() 是唯一判定点，
 * 前端再写一份比较就会出现第二份判定，后端改映射时静默漂移。
 * 后端未下发该字段时 fail-closed 判为"需确认"(宁可多一次确认，不可漏一次危险动作)。
 */
export function requiresConfirmation(a: ActionLike | null | undefined): boolean {
  return a?.requires_confirmation !== false
}

/** 动作 ID → 人类可读摘要 (参数 JSON → k=v 串联, 供卡片与风险清单展示)。 */
export function paramsSummary(actionParamsJson?: string | null): string {
  if (!actionParamsJson || !actionParamsJson.trim()) return '无参数'
  try {
    const parsed = JSON.parse(actionParamsJson) as unknown
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return actionParamsJson
    const entries = Object.entries(parsed as Record<string, unknown>)
    if (entries.length === 0) return '无参数'
    return entries.map(([key, value]) => `${key}=${JSON.stringify(value)}`).join(', ')
  } catch {
    // 参数非法 JSON 不在这里报错 (保存前的 validate 负责)，这里只如实回显原文。
    return actionParamsJson
  }
}
