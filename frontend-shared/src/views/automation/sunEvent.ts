import type { SunEventKind } from '@/api/automation'

/**
 * 日出/日落触发器 (用户裁决 O-4: 新增 sun_event) 的表单辅助。
 *
 * 为什么单独成模块而不是内联在 AutomationRules.vue: 偏移分钟的**符号文案**与
 * **坐标继承状态**是两条会漂移的规则 —— 一个说"日落前 30 分钟"，一个说"留空=继承
 * 节点"。二者都要能被单测直接钉死 (F-1)，内联在 900 行的 .vue 里只能靠源码字符串断言。
 *
 * 设计依据: designs/automation-sunrise-mos/README.md §5 R6 / §6 D-2、demo A2/A5。
 */

/** 偏移分钟取值域 (方案 §4.1.1: [-720, 720], 可负)。 */
export const SUN_OFFSET_MIN = -720
export const SUN_OFFSET_MAX = 720

export interface SunKindOption {
  value: SunEventKind
  label: string
}

/** 日出/日落二选一 (与后端 models.AutomationSunKind* 同域)。 */
export const SUN_KIND_OPTIONS: SunKindOption[] = [
  { value: 'sunrise', label: '日出' },
  { value: 'sunset', label: '日落' },
]

export function sunKindLabel(kind?: SunEventKind | null): string {
  if (kind === 'sunrise') return '日出'
  if (kind === 'sunset') return '日落'
  return '日出/日落'
}

/**
 * 偏移分钟的**实时自然语言回显** (设计 D-2: 界面直接说出用户脑子里的那句话)。
 *
 * 为什么必须有这句回显: 用户原始需求的原话就是"日落前 30 分钟"。只给一个数字框
 * (负号 = 之前) 会让方向被误读 —— 填了 -30 却以为在日落之后。
 *
 * 非法/缺省一律按 0 处理 (即"正点")，与后端缺省值一致。
 */
export function sunOffsetText(kind: SunEventKind | undefined, offsetMin: number | undefined | null): string {
  const base = sunKindLabel(kind)
  const offset = typeof offsetMin === 'number' && Number.isFinite(offsetMin) ? Math.trunc(offsetMin) : 0
  if (offset === 0) return `${base}正点`
  return `${base}${offset < 0 ? '前' : '后'} ${Math.abs(offset)} 分钟`
}

/**
 * 坐标/时区的"已填"判据。
 *
 * 为什么数字不能只判真值: 纬度 0 是**合法坐标** (几内亚湾)，用 0 表达"未配置"会让
 * "赤道上的节点"与"没配坐标的节点"不可区分 —— 后端 sun_location.go 正是为此用指针
 * (注释原文: 前者应正常求值, 后者必须 fail-closed 拒绝保存)。前端同理。
 */
function filled(value: number | string | null | undefined): boolean {
  if (typeof value === 'number') return Number.isFinite(value)
  if (typeof value === 'string') return value.trim() !== ''
  return false
}

export type SunLocationState = 'inherit' | 'override' | 'partial'

export interface SunLocationDraft {
  latitude?: number | null
  longitude?: number | null
  timezone?: string | null
}

/**
 * 规则级坐标覆盖的三种状态。
 *
 * 语义 (用户裁决 O-3): 节点级为权威 + 规则级可选覆盖 (NULL = 继承节点)。
 * 三项必须**同时**填写或**全部**留空 —— 部分填写是无意义的组合 (后端 V2 会
 * fail-closed 拒绝保存)，前端必须提前拦住并说清原因。
 */
export function sunLocationState(draft: SunLocationDraft): SunLocationState {
  const count = [draft.latitude, draft.longitude, draft.timezone].filter(v => filled(v)).length
  if (count === 0) return 'inherit'
  if (count === 3) return 'override'
  return 'partial'
}

/**
 * 坐标/时区继承状态的一句话说明 (界面上直接显示)。
 *
 * 为什么要把"节点没配会被后端拒绝保存"写进界面: O-7 裁决是 fail-closed，用户在
 * 节点页配坐标之前保存日出规则会拿到 400。把这层因果提前说出来，用户才不会
 * 以为页面坏了。
 */
export function sunLocationText(draft: SunLocationDraft): string {
  switch (sunLocationState(draft)) {
    case 'override':
      return '本规则使用规则级坐标与时区，不继承节点'
    case 'partial':
      return '纬度/经度/时区必须同时填写，或全部留空以继承节点'
    default:
      return '留空 = 继承节点坐标/时区；节点未配置时后端会拒绝保存（fail-closed）'
  }
}
