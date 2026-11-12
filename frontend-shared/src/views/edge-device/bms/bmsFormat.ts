/**
 * BMS 详情页的通用小工具（纯函数，无状态）。
 *
 * 为什么单独一个文件：页面、状态管理 composable、子组件都要用同一套口径 ——
 * 占位符（'—'）必须只有一处定义，否则「未知」的写法会漂移；
 * 时间戳格式化同时被页级时间窗回执与操作历史使用。
 * 这里**只搬移**，函数实现与拆分前逐字一致。
 */

/** 未知值占位符（全角破折号，占位符门禁 placeholderDashGate 要求） */
export const DASH = '—'

/** Element Plus 标签类型（状态色）。页头标签与操作历史结果标签共用同一组取值。 */
export type TagType = 'primary' | 'success' | 'info' | 'warning' | 'danger'

export function num(v: unknown): number | null {
  return typeof v === 'number' && Number.isFinite(v) ? v : null
}

export function fmt(v: number | null | undefined, digits: number): string {
  return v === null || v === undefined || !Number.isFinite(v) ? DASH : v.toFixed(digits)
}

export const pad2 = (n: number) => String(n).padStart(2, '0')

export function formatStamp(iso?: string | null): string {
  if (!iso) return DASH
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return DASH
  return d.getFullYear() + '-' + pad2(d.getMonth() + 1) + '-' + pad2(d.getDate()) + ' '
    + pad2(d.getHours()) + ':' + pad2(d.getMinutes()) + ':' + pad2(d.getSeconds())
}

export function hexOf(n: number): string {
  return Math.round(n).toString(16).toUpperCase().padStart(2, '0')
}
