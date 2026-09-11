export type TagType = 'success' | 'primary' | 'info' | 'warning' | 'danger'

const VALID_TAG_TYPES: readonly TagType[] = ['success', 'primary', 'info', 'warning', 'danger']

/** 归一化任意值到合法的 el-tag type；非法/空值回退 'primary'（Element Plus 默认外观）。 */
export function asTagType(value: unknown): TagType {
  return typeof value === 'string' && (VALID_TAG_TYPES as readonly string[]).includes(value)
    ? (value as TagType)
    : 'primary'
}
