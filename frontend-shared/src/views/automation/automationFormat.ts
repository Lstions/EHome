import type {
  AutomationAction,
  AutomationComparator,
  AutomationEventResult,
  AutomationRule,
  AutomationTriggerType,
  SunTimesPreview,
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
  // 2026-11-18: 该结果码在保存入口 V7 校验存在的前提下**不应出现**（能保存成功 = 已完成
  // 规则级风险授权）。它只可能来自 V7 之前的存量数据/风险等级被升级/绕过 API 写库,
  // 且**不能**靠"去事件表确认"修复（确认入口只对 pending_confirm 开放）—— 唯一处置是
  // 编辑规则重新勾选风险确认。故标签按真实语义命名, 不再写"需确认（未授权）"。
  { value: 'confirmation_required', label: '规则未授权' },
  { value: 'partial_failure', label: '部分失败' },
  // 2026-11-20：命令已下发但设备没执行成功（后端终态回填的结果码）。
  // 不标"下发失败"—— failed_dispatch 才是"没发出去"；这条是"发出去了、设备没照做"，
  // 两者处置方向不同，标签必须能让用户一眼分开。
  { value: 'failed_execution', label: '执行失败' },
  { value: 'suppressed_no_solar_event', label: '无日出日落事件' },
  { value: 'missed_solar_event', label: '错过日出日落' },
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
    // 见 resultOptions 同名项注释: 这是"数据异常"而非"等待用户确认"。
    confirmation_required: '规则未授权',
    partial_failure: '部分失败',
    // 命令已下发但设备未执行成功（终态回填）。与 partial_failure 的区别是维度：
    // 那条是"多动作第 k 个失败"，这条是"单条命令最终没成功"。
    failed_execution: '执行失败',
    suppressed_no_solar_event: '无日出日落事件',
    missed_solar_event: '错过日出日落',
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
  // missed_solar_event: 事件存在但没执行（停机/重启跨过且超补发窗口）。没有任何动作
  // 下发、无设备副作用，但用户必须知道"这次设备没动过" ⇒ warning，与 suppressed_* 区分。
  if (r === 'missed_solar_event') return 'warning'
  // partial_failure 是**危险状态**（设备已被改了一半，需人工介入），必须比 failed_* 更显眼，
  // 故与 expired/failed_* 同为 danger。
  // failed_execution（2026-11-20 终态回填）由下面的 failed_ 前缀分支覆盖：命令已经
  // 下发到设备，失败可能有真实副作用（UNKNOWN 时设备甚至可能已动作），必须 danger。
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

// ─────────────────────────────────────────────────────────────────────────
// 「下次执行时间」(2026-11-17)
// ─────────────────────────────────────────────────────────────────────────

/**
 * 列表/卡片里「下次执行时间」的展示结果。
 *
 * 为什么返回三件套而不是一个字符串: 调用方要决定**是否加"下次"前缀**
 * （"下次 明天 06:42" 成立, "下次 等待数据满足条件" 不成立）, 以及配色。
 */
export interface NextRunView {
  /** 直接展示的文本 (不含"下次"前缀) */
  text: string
  /** ok=有确定时刻; muted=无需行动; warn=需要用户注意 (即将触发/配置异常) */
  tone: 'ok' | 'muted' | 'warn'
  /** true 表示 text 是一个具体时刻, 调用方可以加"下次"前缀 */
  hasTime: boolean
}

const NEXT_RUN_NONE: NextRunView = { text: UNKNOWN, tone: 'muted', hasTime: false }

// Intl.DateTimeFormat 的构造成本远高于 formatToParts, 而列表每次渲染每行都要用。
const zonedFormatters = new Map<string, Intl.DateTimeFormat>()

function zonedFormatter(timeZone?: string): Intl.DateTimeFormat {
  const key = timeZone || ''
  const cached = zonedFormatters.get(key)
  if (cached) return cached
  const fmt = new Intl.DateTimeFormat('en-CA', {
    // 空串传进 Intl 会抛 RangeError（后端在拿不到真实 IANA 名时会下发 ""），
    // 必须归一成 undefined = 用浏览器本地时区。
    timeZone: timeZone || undefined,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    // h23 而不是 hour12:false —— 后者在部分 ICU 版本里把午夜格式化成 "24",
    // 会让"今天 24:00"这种不存在的钟面值出现在界面上。
    hourCycle: 'h23',
  })
  zonedFormatters.set(key, fmt)
  return fmt
}

/** 把毫秒时刻拆成**指定时区**下的年月日时分。 */
function zonedParts(ms: number, timeZone?: string): { y: number; m: number; d: number; H: string; M: string } {
  let parts: Intl.DateTimeFormatPart[]
  try {
    parts = zonedFormatter(timeZone).formatToParts(new Date(ms))
  } catch {
    // 非法 IANA 名 (后端脏数据): 退回浏览器本地时区, 而不是抛错炸掉整张表。
    parts = zonedFormatter(undefined).formatToParts(new Date(ms))
  }
  const pick = (type: string) => parts.find(p => p.type === type)?.value ?? ''
  return {
    y: Number(pick('year')),
    m: Number(pick('month')),
    d: Number(pick('day')),
    H: pick('hour'),
    M: pick('minute'),
  }
}

function pad2(n: number): string {
  return String(n).padStart(2, '0')
}

/**
 * 把绝对时刻按**规则所在时区**渲染成「今天 / 明天 HH:mm」。
 *
 * 为什么要按时区渲染而不是直接 toLocaleString: sun_event 的时刻是**规则时区**的
 * 钟面值 (设备在美国、规则配 America/New_York 时, 后端返回的就是那边的日落时刻)。
 * 按浏览器时区格式化会显示成"明天 05:41"这类与该规则语义无关的数字,
 * 用户会以为规则配错了。
 */
export function nextRunAtText(at: string, timeZone: string | undefined, nowMs: number): string {
  const atMs = Date.parse(at)
  if (!Number.isFinite(atMs)) return UNKNOWN
  const a = zonedParts(atMs, timeZone)
  const n = zonedParts(nowMs, timeZone)
  const dayDiff = Math.round((Date.UTC(a.y, a.m - 1, a.d) - Date.UTC(n.y, n.m - 1, n.d)) / 86_400_000)
  const hm = `${a.H}:${a.M}`
  if (dayDiff === 0) return `今天 ${hm}`
  if (dayDiff === 1) return `明天 ${hm}`
  return `${pad2(a.m)}-${pad2(a.d)} ${hm}`
}

/**
 * 规则列表的「下次执行时间」文案。
 *
 * nowMs 必须由调用方传入**会随时间推进**的值 (页面 30s ticker): 如果这里直接调
 * Date.now(), 模板只在其它响应式依赖变化时才重算, 页面停留跨过触发点后文案会
 * 永远停在旧值上。
 */
export function nextRunText(rule: AutomationRule, nowMs: number): NextRunView {
  const nr = rule.next_run
  // undefined = 后端未接线该能力 (老后端/单测替身), 与"规则没有下次执行"不是一回事。
  if (!nr) return NEXT_RUN_NONE

  switch (nr.status) {
    case 'disabled':
      return { text: '已停用', tone: 'muted', hasTime: false }
    case 'unsupported':
      return { text: '触发器暂未实现', tone: 'muted', hasTime: false }
    case 'unavailable':
      // 坐标/时区/窗口格式非法: 这条规则**不会**按预期工作, 必须显眼而不是静默。
      return { text: nr.detail ? `配置异常：${nr.detail}` : '配置异常', tone: 'warn', hasTime: false }
    case 'no_event':
      return { text: '今日无日出/日落', tone: 'muted', hasTime: false }
    case 'waiting_data':
      // 数据驱动: 给不出"下次什么时候", 只能说明在等什么。
      // 冷却期是唯一例外 —— 那时能诚实地说"最早何时可再次触发"。
      if (!nr.at) return { text: '等待数据满足条件', tone: 'muted', hasTime: false }
      return { text: `可再触发 ${nextRunAtText(nr.at, nr.timezone, nowMs)}`, tone: 'muted', hasTime: false }
    default:
      break
  }

  if (!nr.at) return NEXT_RUN_NONE
  const atMs = Date.parse(nr.at)
  if (!Number.isFinite(atMs)) return NEXT_RUN_NONE

  const remaining = atMs - nowMs
  // 确认制规则到点只是落 pending_confirm, 必须把这层说清楚, 否则用户以为会自动下发。
  const confirm = rule.require_confirmed ? '（需确认）' : ''
  if (remaining <= 0) {
    // 计划时刻已过: 一般是页面停留跨过了触发点, 或求值器刚处理完还没被重新拉取。
    return { text: `即将触发${confirm}`, tone: 'warn', hasTime: false }
  }
  if (remaining <= 60_000) {
    return { text: `1 分钟内${confirm}`, tone: 'warn', hasTime: true }
  }
  return { text: `${nextRunAtText(nr.at, nr.timezone, nowMs)}${confirm}`, tone: 'ok', hasTime: true }
}

// ─────────────────────────────────────────────────────────────────────────
// 日出/日落预览（P3，2026-11-18；后端 models/sun_times.go）
// ─────────────────────────────────────────────────────────────────────────

/** 把 ISO 时刻按**规则时区**渲染成 HH:mm（与 next_run 同一套 zonedParts）。 */
export function zonedHM(iso: string, timeZone?: string): string {
  const ms = Date.parse(iso)
  if (!Number.isFinite(ms)) return UNKNOWN
  const p = zonedParts(ms, timeZone)
  return `${p.H}:${p.M}`
}

type SunTimePointStatus = SunTimesPreview['sunrise']['status']

function sunStatusText(status: SunTimePointStatus): string {
  switch (status) {
    case 'always_up':
      return '极昼'
    case 'always_down':
      return '极夜'
    case 'error':
      return '计算失败'
    default:
      return ''
  }
}

/**
 * 表单里「今天（MM-DD）：日出 HH:mm ｜ 日落 HH:mm」一行。
 *
 * 时区必须用**规则时区**渲染：设备在纽约、规则配 America/New_York 时，
 * 后端返回的就是那边的钟面值；按浏览器时区格式化会显示成无关数字。
 */
export function sunTimesText(p: SunTimesPreview): string {
  const date = p.date.length >= 10 ? p.date.slice(5) : p.date
  const sr = p.sunrise.status === 'ok' && p.sunrise.at
    ? zonedHM(p.sunrise.at, p.timezone)
    : sunStatusText(p.sunrise.status)
  const ss = p.sunset.status === 'ok' && p.sunset.at
    ? zonedHM(p.sunset.at, p.timezone)
    : sunStatusText(p.sunset.status)
  return `今天（${date}）：日出 ${sr} ｜ 日落 ${ss}`
}

/**
 * 表单里「本规则会怎么触发」一行。
 *
 * 跨午夜偏移必须显式提示（旧实现这类规则一次都不触发，修复后由候选集处理）：
 * 只显示事件时刻会让用户以为规则配错了，所以把真实 fireAt 与「次日/前一日」写出来。
 */
export function sunSelectionView(p: SunTimesPreview): { text: string; tone: 'info' | 'warning' } | null {
  const sel = p.selected
  if (!sel) return null
  const kind = sel.kind === 'sunrise' ? '日出' : '日落'
  if (sel.status !== 'ok' || !sel.event_at || !sel.fire_at) {
    const why = sunStatusText(sel.status)
    return { text: `本规则：今天没有${kind}事件${why ? `（${why}）` : ''}`, tone: 'warning' }
  }
  const evMs = Date.parse(sel.event_at)
  const fireMs = Date.parse(sel.fire_at)
  if (!Number.isFinite(evMs) || !Number.isFinite(fireMs)) return null
  const ev = zonedParts(evMs, p.timezone)
  const fire = zonedParts(fireMs, p.timezone)
  const dayDiff = Math.round(
    (Date.UTC(fire.y, fire.m - 1, fire.d) - Date.UTC(ev.y, ev.m - 1, ev.d)) / 86_400_000,
  )
  const dayPrefix = dayDiff === 1 ? '次日 ' : dayDiff === -1 ? '前一日 ' : ''
  const offset = sel.offset_min
  const offsetText = offset === 0 ? '正点' : `${offset > 0 ? '后' : '前'} ${Math.abs(offset)} 分钟`
  if (dayDiff !== 0 && dayPrefix === '') {
    // ±720 分钟最多跨一日；真出现跨多日就按绝对日期写，不猜「次日」。
    return {
      text: `本规则：${kind} ${ev.H}:${ev.M}，${offsetText} → ${fire.m}-${fire.d} ${fire.H}:${fire.M} 触发`,
      tone: 'warning',
    }
  }
  return {
    text: `本规则：${kind} ${ev.H}:${ev.M}，${offsetText} → ${dayPrefix}${fire.H}:${fire.M} 触发`,
    tone: dayDiff === 0 ? 'info' : 'warning',
  }
}
