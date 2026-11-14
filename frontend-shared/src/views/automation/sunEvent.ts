import type { SunEventKind } from '@/api/automation'

/**
 * 日出/日落触发器 (用户裁决 O-4: 新增 sun_event) 的表单辅助。
 *
 * 为什么单独成模块而不是内联在 AutomationRules.vue: 偏移分钟的**符号文案**是一条约会
 * 漂移的规则（"日落前 30 分钟" vs 数字 -30），要能被单测直接钉死 (F-1)；内联在
 * 900 行的 .vue 里只能靠源码字符串断言。
 *
 * 2026-11-13 用户裁决「不需要节点位置」：坐标/时区改为规则必填，原来的
 * "继承/部分/覆盖"三态与相关文案（sunLocationState / sunLocationText）已随
 * 节点位置一起删除 —— 不再有"留空 = 继承节点"这种状态可展示。
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
 * 坐标/时区是否三项齐全（2026-11-13 起为**规则必填项**）。
 *
 * 为什么数字不能只判真值: 纬度 0 是**合法坐标**（几内亚湾），用 0 表达"未配置"
 * 会让"赤道上的规则"与"没填坐标的规则"不可区分。后端 sun_location.go 为此用
 * 指针区分 nil / 0，前端这里用 Number.isFinite 区分"填了 0"与"没填"。
 *
 * 历史：2026-11-13 之前这里还有 inherit / partial / override 三态（节点级权威 +
 * 规则级覆盖）。用户裁决「不需要节点位置」后，继承路径与三态一并删除。
 */
export function sunLocationComplete(draft: {
  latitude?: number | null
  longitude?: number | null
  timezone?: string | null
}): boolean {
  const lat = typeof draft.latitude === 'number' && Number.isFinite(draft.latitude)
  const lon = typeof draft.longitude === 'number' && Number.isFinite(draft.longitude)
  const tz = typeof draft.timezone === 'string' && draft.timezone.trim() !== ''
  return lat && lon && tz
}
