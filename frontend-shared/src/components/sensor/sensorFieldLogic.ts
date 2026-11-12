import type { SensorField, SensorFieldErrorCode } from '@/api/sensorField'

/**
 * SensorFieldPicker 的纯逻辑层（可导出、无副作用、无 Vue 依赖）。
 *
 * 为什么单独抽出来：这些判断是**规则会不会永久沉默**的判据（类型门禁、族分词、错误码翻译），
 * 放在组件里就只能靠挂载 SFC 才能测；抽成纯函数后可以逐条钉死，也便于别的入口（告警规则页）
 * 复用同一份判据 —— 文档 §4.6 要求自动化与告警两个创建入口共用同一套选择器语义。
 */

// ────────────────────────────── 类型门禁 ──────────────────────────────

/**
 * 类型门禁：只有 `number` 与「未知」（空串）放行。
 *
 * 依据（文档 §3.2 + 后端实测链）：
 *   sensor_field_meta.value_type = boolean|enum|bitmask|string|raw
 *     → resolve.go:55-60 fieldNumeric() = false
 *     → eval.go:297-305 判 numeric=false ⇒ Verdict{OK:false, Reason:"non_numeric"}
 *     → 规则**永不触发**，每次求值落一条 unresolved_selector 审计（5 分钟节流）。
 * 生产上非数值字段占 25.4%，而现状下拉里 `protection_status` 与 `temperature_1` 长得一模一样。
 *
 * 为什么「未知」放行而不是过滤：空串只表示"声明与元数据都没给出类型"，不等于非数值；
 * 真伪由后端 resolve 兜底（unbound 会显式报出来），不能因为前端缺元数据就把真实数值字段藏掉。
 * 为什么 boolean/bitmask 也不放行：它们需要新的比较谓词（"为真"/"第 N 位为 1"）才能求值，
 * 用 `= 1` 假装支持只会得到一个同样永不触发的规则（fieldNumeric 直接判 unbound）。
 */
export function isNumericGate(valueType: string | null | undefined): boolean {
  const t = (valueType ?? '').trim()
  return t === 'number' || t === ''
}

// ────────────────────────────── 字段族 ──────────────────────────────

/**
 * 族成员物理名：`<基名>_<数字后缀>`，基名首字符必须是 a-z（与后端 family 语法同级）。
 * 只认小写是刻意的：后端选择器语法区分大小写，`Cell_1` 既不是合法族成员也不是合法字面量，
 * 前端把它聚成族只会让用户选中一条运行期解析不到的选择器。
 */
const FAMILY_MEMBER_PATTERN = /^([a-z][a-z0-9_]*)_(\d+)$/

export interface SensorFieldFamily {
  /** 基名（不含 `_数字`），如 `cell_voltage` */
  prefix: string
  /** 成员，按数字后缀升序（_1、_2、…、_10 而不是字典序 _1、_10、_2） */
  members: SensorField[]
}

/**
 * 把字段目录按物理名聚成字段族，**成员数 ≥2 才成族**（文档 §4.1）。
 *
 * 单成员不成族：`temperature_1` 单独出现时它就是字面量，额外造一条"族(1)"只会让用户
 * 多一次选择、并诱导他配 `temperature_*:max`（当前只有一个探头，聚合没有任何意义）。
 */
export function groupFieldFamilies(fields: readonly SensorField[]): SensorFieldFamily[] {
  const buckets = new Map<string, { index: number; field: SensorField }[]>()
  for (const field of fields) {
    const name = (field?.name ?? '').trim()
    const matched = FAMILY_MEMBER_PATTERN.exec(name)
    if (!matched) continue
    const prefix = matched[1]
    const index = Number(matched[2])
    const bucket = buckets.get(prefix)
    if (bucket) bucket.push({ index, field })
    else buckets.set(prefix, [{ index, field }])
  }
  const families: SensorFieldFamily[] = []
  for (const [prefix, bucket] of buckets) {
    if (bucket.length < 2) continue
    bucket.sort((a, b) => (a.index - b.index) || a.field.name.localeCompare(b.field.name))
    families.push({ prefix, members: bucket.map(item => item.field) })
  }
  families.sort((a, b) => a.prefix.localeCompare(b.prefix))
  return families
}

/**
 * 族条目文案：「电芯电压族(16)」。
 *
 * 基名取自首成员 label_zh 去掉尾部序号（"电芯电压 1" → "电芯电压"）；label_zh 全缺时
 * 退回物理名基名（`cell_voltage 族(16)`）—— 宁可露物理名，也不要编一个中文名。
 */
export function buildFamilyOptionTitle(family: SensorFieldFamily): string {
  const first = family.members[0]
  const raw = (first?.label_zh ?? '').trim()
  const stripped = raw ? (raw.replace(/[\s_\-]*\d+$/, '').trim() || raw) : family.prefix
  const asciiTail = /[a-z0-9_]$/i.test(stripped)
  return stripped + (asciiTail ? ' ' : '') + '族(' + family.members.length + ')'
}

/** 下拉里族条目的值前缀（不是选择器语法，纯粹为了和字面量选项区分开）。 */
export const FAMILY_SELECT_PREFIX = 'family:'

/**
 * 生成字段族选择器：`cell_voltage_*:max`。
 *
 * 注意分隔符是 **`_*:`**（下划线星号 + 冒号），不是 `_*.`：`cell_voltage_*.max`
 * 会被后端判语法错误（selector.go）。这个字符串直接落库成为规则条件，写错就是静默死规则。
 */
export function buildFamilySelector(prefix: string, aggregate: string): string {
  return prefix + '_*:' + aggregate
}

export interface ParsedFamilySelector {
  prefix: string
  aggregate: string
}

/**
 * 反向解析族选择器（编辑存量规则时用）：`cell_voltage_*:max` → {prefix,aggregate}。
 * 非族形态（字面量 / `@soc` / `virtual:xxx` / 缺聚合）一律返回 null，交给字面量分支处理。
 */
export function parseFamilySelector(selector: string | null | undefined): ParsedFamilySelector | null {
  const text = (selector ?? '').trim()
  const matched = /^([a-z][a-z0-9_]*)_\*:([a-z][a-z0-9_]*)$/.exec(text)
  if (!matched) return null
  return { prefix: matched[1], aggregate: matched[2] }
}

// ────────────────────────────── 选项文案 ──────────────────────────────

/**
 * 相对时间文案（"12s 前" / "3h 前" / "从未上报"）。
 *
 * 用 `last_seen` 而不是 `first_seen`：用户要回答的是"设备现在还发这个字段吗"。
 * 未来时间（设备与服务端时钟偏差）夹到 0，避免出现"-5s 前"这种荒谬文案。
 */
export function formatRelativeTime(lastSeen: string | null | undefined, nowMs: number): string {
  if (!lastSeen) return '从未上报'
  const ts = Date.parse(lastSeen)
  if (!Number.isFinite(ts)) return '从未上报'
  const seconds = Math.max(0, Math.floor((nowMs - ts) / 1000))
  if (seconds < 60) return seconds + 's 前'
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return minutes + 'm 前'
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return hours + 'h 前'
  return Math.floor(hours / 24) + 'd 前'
}

/**
 * 字段选项文案：`label_zh（缺失回落 name）· 物理名 · 单位 · 相对 last_seen`。
 *
 * 为什么物理名必须出现：`label_zh` 会随产品文案改，落库的是物理名；用户必须能对照
 * 自己配进规则的那一串字符。为什么中文名和物理名必须在**同一个 label** 里：
 * el-select 的默认过滤只匹配选项 label 文本，两者同串才能"中文搜温度命中 temperature_1"，
 * 又"英文搜 temperature 命中"（本仓没有自定义 filter-method 的先例，不另造一套过滤）。
 *
 * 后缀两个标记都是**文案即判据**：
 *   「非数值，阈值比较不生效」= 该字段永远不可选（见 isNumericGate）；
 *   「已弃用」= 后端标记废弃，灰显但仍可选（存量规则可能正在引用它）。
 */
export function buildFieldOptionLabel(field: SensorField, nowMs: number): string {
  const name = (field.name ?? '').trim()
  const labelZh = (field.label_zh ?? '').trim()
  const parts: string[] = [labelZh || name]
  if (labelZh && name && labelZh !== name) parts.push(name)
  if (field.unit) parts.push(field.unit)
  parts.push(formatRelativeTime(field.last_seen, nowMs))
  let text = parts.join(' · ')
  if (field.deprecated) text += '（已弃用）'
  if (!isNumericGate(field.value_type)) text += '（非数值，阈值比较不生效）'
  return text
}

// ────────────────────────────── 聚合人话 ──────────────────────────────

/**
 * 聚合的「人话」与「缺员语义」文案。
 *
 * ⚠ 这张表**只提供展示文案，绝不提供候选**：聚合候选与默认值必须来自 /schema
 * （设计文档 §8.4「禁止在代码里另存一份」）。表外的聚合（后端将来新增）返回空串，
 * 由调用方原样显示聚合名 —— 宁可显示 `median`，也不要用近似语义误导用户。
 *
 * 缺员语义是这里最容易配错的地方（本帧少一个探头时）：
 *   all/avg/sum 缺员即不触发（后端 unbound）；min/max 按现有成员求值并标 degraded。
 */
export const AGGREGATE_BRIEF: Readonly<Record<string, string>> = {
  any: '任意一个',
  all: '全部',
  max: '最高',
  min: '最低',
  avg: '平均',
  sum: '合计',
}

export const AGGREGATE_GAP_NOTE: Readonly<Record<string, string>> = {
  any: '缺员不影响',
  all: '缺员即不触发',
  max: '缺员按现有成员求值并标降级',
  min: '缺员按现有成员求值并标降级',
  avg: '缺员即不触发',
  sum: '缺员即不触发',
}

/** 聚合的一句人话；未知聚合返回空串（不编造语义）。 */
export function aggregateBrief(aggregate: string): string {
  return AGGREGATE_BRIEF[aggregate] ?? ''
}

/** 聚合的缺员语义；未知聚合返回 '—'。 */
export function aggregateGapNote(aggregate: string): string {
  return AGGREGATE_GAP_NOTE[aggregate] ?? '—'
}

// ────────────────────────────── 错误码人话 ──────────────────────────────

/**
 * 选择器错误码 → 人话。
 *
 * 键名与后端 `handler_sensor_fields.go:612-624` 的 §8.6 契约表逐项一致；
 * 具体**翻译哪些码**由 /schema.error_codes 决定（后端新增码时前端不硬猜）。
 */
export const SELECTOR_ERROR_TEXT: Readonly<Record<string, string>> = {
  invalid_selector_syntax: '选择器语法不合法（示例：temperature_1 / cell_voltage_*:max / @soc）',
  empty_selector: '选择器不能为空',
  selector_too_long: '选择器过长，请缩短字段名',
  unknown_semantic: '未知语义别名（当前设备的别名表里没有这一项）',
  unknown_virtual: '未知派生量（设备未声明该派生字段）',
  unsupported_aggregate: '不支持的聚合方式（聚合词表见下方说明）',
  field_resolver_disabled: '字段解析器已停用（后端开关关闭），该选择器无法求值',
  invalid_json: '请求格式错误',
  resolution_unbound: '当前解析不到，保存后可能永不触发',
}

/**
 * 把 400 响应里的 `error_code` 翻成人话。
 *
 * 只翻译 schema.error_codes 契约表里确实列出的码：
 *   · 表外的码说明后端扩展了前端不认识的语义，硬翻译会给出错误的处置暗示；
 *   · 契约表拿不到（/schema 请求失败或未返回 error_codes）时同样回落后端 message，
 *     不凭本地副本猜契约 —— 显示英文原文虽难读，但不会误导用户去改一个没问题的字段。
 * 未知码（表内但本文件没有文案，或表外）一律回落 message，绝不把 `error_code` 裸串怼给用户。
 */
export function translateSelectorError(
  errorCode: string | null | undefined,
  fallbackMessage: string,
  contract: readonly SensorFieldErrorCode[] | null | undefined,
): string {
  const code = (errorCode ?? '').trim()
  if (!code) return fallbackMessage
  const declared = Array.isArray(contract) && contract.some(item => item?.code === code)
  if (!declared) return fallbackMessage
  return SELECTOR_ERROR_TEXT[code] ?? fallbackMessage
}

export interface SelectorErrorInfo {
  /** HTTP 状态；非 400 时调用方按「校验不可用」处理，而不是字段名写错 */
  status?: number
  errorCode?: string
  message: string
}

/**
 * 把抛出物归一成 `{status,errorCode,message}`（结构化，不依赖 instanceof ApiError）。
 *
 * 为什么不用 instanceof：组件测试用 `vi.mock` 替换 api 层时抛的是普通对象/Error，
 * 依赖具体类会让"400 可翻译"这条判据在测试里失真；结构判定在生产与测试里同一口径。
 */
export function toSelectorErrorInfo(error: unknown): SelectorErrorInfo {
  if (error instanceof Error) {
    const withMeta = error as Error & { status?: unknown; errorCode?: unknown }
    return {
      status: typeof withMeta.status === 'number' ? withMeta.status : undefined,
      errorCode: typeof withMeta.errorCode === 'string' ? withMeta.errorCode : undefined,
      message: withMeta.message || '选择器校验失败',
    }
  }
  if (error && typeof error === 'object') {
    const record = error as { status?: unknown; errorCode?: unknown; message?: unknown }
    return {
      status: typeof record.status === 'number' ? record.status : undefined,
      errorCode: typeof record.errorCode === 'string' ? record.errorCode : undefined,
      message: typeof record.message === 'string' && record.message ? record.message : '选择器校验失败',
    }
  }
  return { message: String(error ?? '') || '选择器校验失败' }
}

// ────────────────────────────── 降级 ──────────────────────────────

/**
 * 降级链第二级：把 `/unified-data/categories` 的 `{code,unit}` 补成字段目录同形状。
 *
 * `value_type` 置空串（未知）⇒ isNumericGate 放行，用户仍能看到并选中这些字段；
 * 这是**有意的**：该接口不提供类型，若在这里当作非数值过滤，会把整个降级路径变成空列表。
 * 真正的类型判定仍由后端 resolve 兜底，UI 用「降级」提示说明信息缺失。
 */
export function fallbackFieldFromCategory(code: string, unit: string): SensorField {
  return {
    name: (code ?? '').trim(),
    unit: (unit ?? '').trim(),
    label_zh: '',
    value_type: '',
    precision: 0,
    sources: ['obs'],
    semantics: [],
    first_seen: null,
    last_seen: null,
    samples: 0,
    deprecated: false,
  }
}
