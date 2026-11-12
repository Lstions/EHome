import type {
  AutomationAction,
  AutomationComparator,
  AutomationEventResult,
  AutomationRule,
  AutomationTriggerType,
} from '@/api/automation'
import { UNKNOWN } from '@/utils/format'
import { sunOffsetText } from './sunEvent'
import { riskBadge } from './actionList'

/**
 * 自动化策略页的**纯展示函数**（列表侧拆分第一轮，2026-11-12）。
 *
 * 职责：把「后端字段 → 用户可读文案」的全部映射集中到一处 —— 触发器类型/摘要、
 * 动作摘要、事件结果文案与配色、触发值/时间格式化，以及规则动作数组的兼容期归一化
 * （ruleActions / ruleRiskCount）。这里只做纯函数，不持有任何响应式状态、不发请求。
 *
 * 为什么抽出来（而不是留在页面里）：
 *   · 拆分后有三处消费方 —— AutomationRules.vue（onTrigger 的结果提示、编辑回填、
 *     动作目录）、RuleListSection.vue（规则表格与窄屏卡片）、EventHistorySection.vue
 *     （事件表格与窄屏卡片）。留在页面里就只能靠 props 往下传函数，或者让子组件
 *     反向 import 页面 —— 前者把展示细节漏进对外契约，后者产生环形依赖；
 *   · 这些映射有「三处必须同源」的硬约束（见下方 comparators / resultOptions 的注释）：
 *     列表摘要、筛选下拉、标签配色必须用同一份措辞，分文件维护必然漂移；
 *   · 纯函数可以直接被单测覆盖（本仓 actionList.spec / sunEvent.spec 同一思路），
 *     不必再用「源码字符串断言」间接保护。
 *
 * ⚠ 搬家纪律：本文件是从 AutomationRules.vue **原样搬移**的，函数体、文案、枚举
 * 分支顺序一字未改（第一轮纯搬移）。任何行为改动都属于下一轮。
 */

/**
 * 比较符：**中文语义 + 符号**并列，符号保留供工程用户快速识别。
 *
 * 为什么不再只显示符号：本页的主要用户是现场配置规则的人，不是写 SQL 的人；
 * 只给 `>`/`≥` 时要在脑内完成"符号 → 中文语义"的翻译，而 `>` 与 `≥` 的差别
 * 恰好是"触发早一点还是晚一点"，翻错就是规则行为错一个月。
 *
 * ⚠️ 这个数组被**三处**复用：① 表单里的比较符下拉 ② 列表/摘要的 triggerSummary()
 * ③ 移动端规则卡片。改 label 会同时改变列表摘要的文案（例：`temperature 高于 > 30`），
 * 这是**期望行为** —— 列表与表单必须用同一套措辞，否则用户会在两处读到两种说法。
 */
export const comparators: Array<{ value: AutomationComparator; label: string }> = [
  { value: 'gt', label: '高于 >' },
  { value: 'gte', label: '不低于 ≥' },
  { value: 'lt', label: '低于 <' },
  { value: 'lte', label: '不高于 ≤' },
  { value: 'eq', label: '等于 =' },
  { value: 'neq', label: '不等于 ≠' },
]

// F-8: 新结果枚举必须在**三处**映射齐全（resultOptions / resultText / resultTagType），
// 否则新结果码会以英文原串直接显示给用户（"字面量泄漏"）。三处同源维护：
//   ① 本数组加一项中文标签 —— 否则筛选下拉里选不到该类结果（用户无法按它过滤）；
//   ② resultText 的 map 加同名映射 —— 否则结果列直接渲染英文原码（兜底 return map[r] ?? r）；
//   ③ resultTagType 给出标签色 —— 否则会落到 info，被当作"正常/已抑制"。
// 最近一次增量是数据字段解析层的 unresolved_selector / field_resolver_disabled
// （设计 §10 要求前端枚举同步）：两者都是**规则不工作**（配置错 / 运维开关），
// 不是执行故障 ⇒ resultTagType 一律 warning（理由见该函数内注释）。
// 新增枚举时若只改一处，tests 里的 F-8 门禁会红；照上面 ①②③ 三处补齐并补用例即可。
export const resultOptions: Array<{ value: AutomationEventResult; label: string }> = [
  { value: 'executed', label: '已执行' },
  { value: 'pending_confirm', label: '待确认' },
  { value: 'suppressed_cooldown', label: '冷却抑制' },
  { value: 'suppressed_daily_limit', label: '达每日上限' },
  { value: 'condition_changed', label: '条件已变' },
  { value: 'failed_gate', label: '门禁失败' },
  { value: 'failed_dispatch', label: '下发失败' },
  { value: 'notification', label: '已通知' },
  { value: 'expired', label: '已过期' },
  { value: 'confirmation_required', label: '需确认（未授权）' },
  { value: 'partial_failure', label: '部分失败' },
  { value: 'suppressed_no_solar_event', label: '无日出日落事件' },
  { value: 'unresolved_selector', label: '选择器解析不到' },
  { value: 'field_resolver_disabled', label: '字段解析未启用' },
]

/** 规则动作的显示辅助（列表页与编辑回填共用）。 */
export function ruleActions(rule: AutomationRule): AutomationAction[] {
  if (rule.actions && rule.actions.length > 0) return rule.actions
  // 兼容期回落：后端尚未回填 actions 子表时用单动作镜像列合成 seq=1。
  if (rule.action_type === 'device_action' && rule.action_id) {
    return [{ seq: 1, action_type: rule.action_type, action_id: rule.action_id, action_device_id: rule.action_device_id }]
  }
  if (rule.action_type === 'notification') {
    return [{ seq: 1, action_type: rule.action_type, action_level: rule.action_level }]
  }
  return []
}

/** 列表页「含 N 个风险动作」计数：读后端下发的 requires_confirmation。 */
export function ruleRiskCount(rule: AutomationRule): number {
  return ruleActions(rule).filter(a => riskBadge(a)).length
}

export function triggerTypeText(t: AutomationTriggerType): string {
  if (t === 'sensor_threshold') return '传感器阈值'
  if (t === 'time_window') return '时间窗口'
  if (t === 'sun_event') return '日出/日落'
  return '事件驱动'
}

/** 触发器摘要：日出/日落与表单里的实时文案**同源**（sunOffsetText），避免两套措辞。 */
export function triggerSummary(rule: AutomationRule): string {
  if (rule.trigger_type === 'sensor_threshold') {
    const cmp = comparators.find(c => c.value === rule.trigger_comparator)?.label ?? rule.trigger_comparator
    const base = `${rule.trigger_sensor_name} ${cmp} ${rule.trigger_threshold}`
    // 来源组规则：在触发摘要里显式标注来源组，避免用户把它误读成"某台设备"。
    return rule.source_kind === 'source_group' ? `来源组 #${rule.source_group_id} · ${base}` : base
  }
  if (rule.trigger_type === 'time_window') {
    const edge = rule.trigger_window_edge === 'enter' ? '进入' : rule.trigger_window_edge === 'exit' ? '离开' : '持续'
    return `${rule.trigger_window_start}-${rule.trigger_window_end} ${edge}`
  }
  if (rule.trigger_type === 'sun_event') {
    return sunOffsetText(rule.trigger_sun_kind, rule.trigger_sun_offset_min)
  }
  return UNKNOWN
}

/** 动作列摘要：多动作下把所有动作 ID 串起来（单值摘要已无法表达）。 */
export function actionSummary(rule: AutomationRule): string {
  const actions = ruleActions(rule)
  if (actions.length === 0) return UNKNOWN
  return actions
    .map(a => a.action_type === 'notification'
      ? `通知(${a.action_level === 'critical' ? '严重' : a.action_level === 'warning' ? '警告' : '信息'})`
      : (a.action_id ?? UNKNOWN))
    .join(', ')
}

export function resultText(r: AutomationEventResult): string {
  const map: Record<AutomationEventResult, string> = {
    executed: '已执行',
    pending_confirm: '待确认',
    suppressed_cooldown: '冷却抑制',
    suppressed_daily_limit: '达每日上限',
    condition_changed: '条件已变',
    failed_gate: '门禁失败',
    failed_dispatch: '下发失败',
    notification: '已通知',
    expired: '已过期',
    confirmation_required: '需确认（未授权）',
    partial_failure: '部分失败',
    suppressed_no_solar_event: '无日出日落事件',
    // 字段解析层：配置错（解析不到）与运维开关（解析未启用）分列，处置方式不同
    unresolved_selector: '选择器解析不到',
    field_resolver_disabled: '字段解析未启用',
    source_member_changed: '来源成员已变',
    source_stale: '来源数据过期',
    source_degraded: '来源降级',
  }
  return map[r] ?? r
}

export function resultTagType(r: AutomationEventResult): 'success' | 'warning' | 'danger' | 'info' {
  if (r === 'executed' || r === 'notification') return 'success'
  if (r === 'pending_confirm' || r === 'confirmation_required') return 'warning'
  // 字段解析层两码: unresolved_selector（选择器语法合法但解析不到 ⇒ 规则永不触发，配置错）
  // 与 field_resolver_disabled（解析层开关关闭 ⇒ 整条规则 fail-closed，运维开关）。
  // 判断依据: **不工作但不是执行故障** ⇒ warning。
  //   · 不能归 info: info 在本函数里是兜底档（suppressed_*/condition_changed 这类
  //     "规则正常工作时的取舍"），归 info 会被用户当成正常，而这两条意味着规则压根没在求值；
  //   · 也不必用 danger: danger 留给 failed_*/partial_failure/expired 这类**需要立即介入**
  //     的状态（设备已被改动、下发失败、待确认已超时）；这两条没有任何动作下发、无设备副作用，
  //     处置是改配置或开开关，属"需要处理但不紧急"。
  if (r === 'unresolved_selector' || r === 'field_resolver_disabled') return 'warning'
  // partial_failure 是**危险状态**（设备已被改了一半，需人工介入），必须比 failed_* 更显眼，
  // 故与 expired/failed_* 同为 danger。
  if (r.startsWith('failed_') || r === 'expired' || r === 'partial_failure') return 'danger'
  return 'info'
}

export function formatValue(v: number): string {
  return Number.isFinite(v) ? String(Math.round(v * 1000) / 1000) : UNKNOWN
}

export function formatTime(t?: string | null): string {
  if (!t) return UNKNOWN
  return new Date(t).toLocaleString()
}
