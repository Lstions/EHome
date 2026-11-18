/**
 * time_window 规则的时区选择（2026-11-18）。
 *
 * 为什么必须由用户显式选时区：后端 time_window 求值用的是**进程时区**，而生产容器是
 * UTC —— 东八区用户配「08:00–18:00」实际在 16:00–02:00 生效，界面上却看不出任何异常。
 * 后端本次改为「按规则自带时区解释」，所以表单必须让用户选并把 timezone 提交上去。
 * `sun_event` 也在用同一个 timezone 字段，但它的值由定位结果回填、不允许手选；
 * 两者语义口径一致（IANA 名 + 按规则时区解释），输入方式不同 —— 本模块只服务前者。
 *
 * 为什么词表要能力检测：Intl.supportedValuesOf 是较新的 API，旧内核 / 被裁剪的
 * WebView 里可能不存在。直接调用会在渲染表单时抛 TypeError，把整个规则对话框
 * 打成白屏 —— 而时区只是表单里的一个字段，降级到常见时区列表仍能完成配置。
 */

export interface TimeZoneOption {
  /** 提交给后端的 IANA 名（原样，不改写） */
  value: string
  /** 下拉展示文案：IANA 名 + 当前 UTC 偏移（帮助不熟悉 IANA 命名的用户定位自己的时区） */
  label: string
}

/**
 * 兜底词表：没有 Intl.supportedValuesOf 时使用。
 *
 * 覆盖 UTC / 中国 / 亚太 / 欧洲 / 美洲主要时区。用户所在时区不在这里时仍可通过
 * 浏览器默认值进入表单（并原样提交）；这只是缺 API 环境的降级，
 * 不是「我们只支持这些时区」的产品决定。
 */
const FALLBACK_TIME_ZONES = [
  'UTC',
  'Asia/Shanghai', 'Asia/Hong_Kong', 'Asia/Taipei', 'Asia/Tokyo', 'Asia/Seoul',
  'Asia/Singapore', 'Asia/Bangkok', 'Asia/Kolkata', 'Asia/Dubai', 'Asia/Jerusalem',
  'Europe/London', 'Europe/Paris', 'Europe/Berlin', 'Europe/Moscow',
  'America/New_York', 'America/Chicago', 'America/Denver', 'America/Los_Angeles',
  'America/Sao_Paulo', 'Australia/Sydney', 'Australia/Perth', 'Pacific/Auckland',
] as const

/** 读 Intl 支持的时区表；环境不支持（或返回空表）时返回 null，由调用方走兜底。 */
function readSupportedTimeZones(): string[] | null {
  if (typeof Intl.supportedValuesOf !== 'function') return null
  try {
    const values = Intl.supportedValuesOf('timeZone')
    if (!Array.isArray(values)) return null
    const cleaned = values.filter(value => typeof value === 'string' && value.trim() !== '')
    return cleaned.length > 0 ? cleaned : null
  } catch {
    // Safari 早期实现会抛 RangeError / 被 polyfill 成 throw：都按「不支持」处理。
    return null
  }
}

/**
 * 可选的 IANA 时区列表。
 *
 * 为什么手动补 UTC：Node 24 的 Intl.supportedValuesOf('timeZone') 实测 418 项里
 * **不含 'UTC'**，而 UTC 恰好是「服务器 / 容器时区」这个最需要被显式选中的值。
 */
export function listTimeZones(): string[] {
  const base = readSupportedTimeZones() ?? [...FALLBACK_TIME_ZONES]
  return base.includes('UTC') ? base : ['UTC', ...base]
}

/** 把 Intl 的偏移文本（GMT+08:00 / GMT）归一成 UTC 口径，避免界面同时出现两种写法。 */
function normalizeOffsetText(name: string): string {
  const text = name.trim()
  if (!text) return ''
  const unified = text.replace(/^GMT/, 'UTC')
  return unified === 'UTC' ? 'UTC+00:00' : unified
}

/**
 * 时区当前 UTC 偏移（如 UTC+08:00）；算不出来返回空串（只是少一段文案，不影响选择）。
 *
 * 用 timeZoneName:'longOffset' 而不是自己算 getTimezoneOffset：后者要先做一次
 * 「本地时间 → UTC」的反解，夏令时切换日容易差一小时；longOffset 由 ICU 直接给出。
 * 老内核不认这个取值会抛 RangeError —— catch 掉，标签退化成纯 IANA 名。
 */
function timeZoneOffsetText(timeZone: string): string {
  try {
    const parts = new Intl.DateTimeFormat('en-US', {
      timeZone,
      timeZoneName: 'longOffset',
      hour: 'numeric',
    }).formatToParts(new Date())
    return normalizeOffsetText(parts.find(part => part.type === 'timeZoneName')?.value ?? '')
  } catch {
    return ''
  }
}

/** 下拉文案缓存：418 个时区 × 每次表单重算都新建 DateTimeFormat 会让打开对话框明显卡顿。 */
const labelCache = new Map<string, string>()

/** 时区展示文案：`Asia/Shanghai（UTC+08:00）`。 */
export function timeZoneLabel(timeZone: string): string {
  const cached = labelCache.get(timeZone)
  if (cached !== undefined) return cached
  // UTC 自身不写「（UTC+00:00）」：同义反复，用户只会觉得啰嗦。
  const offset = timeZone === 'UTC' ? '' : timeZoneOffsetText(timeZone)
  const label = offset ? `${timeZone}（${offset}）` : timeZone
  labelCache.set(timeZone, label)
  return label
}

/**
 * 构造下拉选项。
 *
 * extraValues 是「当前选中值 / 浏览器时区」等必须出现在列表里的值：存量规则可能带
 * 别名或后端新增的时区名，若不在选项里，el-select 会显示一个下拉中找不到的选中项
 *（用户无法确认它是什么、也无法重新选回它）。这类值只追加、不改写、不在此处校验 ——
 * 合法性在提交前由 isValidTimeZone 判定。
 */
export function buildTimeZoneOptions(...extraValues: (string | undefined)[]): TimeZoneOption[] {
  const values = listTimeZones()
  const seen = new Set(values)
  for (const raw of extraValues) {
    const value = (raw ?? '').trim()
    if (!value || seen.has(value)) continue
    seen.add(value)
    values.push(value)
  }
  return values.map(value => ({ value, label: timeZoneLabel(value) }))
}

/** 浏览器时区（IANA）；拿不到或非法时退回 UTC。 */
export function browserTimeZone(): string {
  try {
    const zone = Intl.DateTimeFormat().resolvedOptions().timeZone
    if (typeof zone === 'string' && zone.trim() !== '' && isValidTimeZone(zone)) return zone.trim()
  } catch {
    // 被裁剪的 Intl / 隐私模式：不因为「读不到浏览器时区」就阻止用户配置规则。
  }
  return 'UTC'
}

/**
 * 是否是运行期能接受的 IANA 时区名。
 *
 * 为什么不用 supportedValuesOf 判定：它是**枚举表**，跨版本有增删（IANA 新增名、
 * Node 列表不含 UTC）；而 `new Intl.DateTimeFormat(..., {timeZone})` 是真正的解析，
 * 与后端 time.LoadLocation 的接受面更接近。后端仍有最终校验，这里只拦明显被
 * 手改坏 / 别的工具写坏的存量值，避免它静默保存后规则永不按预期生效。
 */
export function isValidTimeZone(value: string): boolean {
  const zone = (value ?? '').trim()
  if (!zone) return false
  // ES2024 起 Intl 接受固定偏移标识符（"+08:00" / "-05:00"），但后端用的是
  // Go time.LoadLocation —— 它只认 IANA 位置名与 zoneinfo 里的别名，不认偏移写法。
  // 不显式排除的话，这类值会"前端校验通过、保存时后端 400"，正是要避免的错位。
  if (zone.startsWith('+') || zone.startsWith('-')) return false
  try {
    new Intl.DateTimeFormat('en-US', { timeZone: zone })
    return true
  } catch {
    return false
  }
}
