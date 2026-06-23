/**
 * DMA 通道状态枚举与辅助函数 (v2.5)
 *
 * 统一管理 DMA 状态的魔数值，避免在 NodeDetail / ChannelPanel / dmaStore 中散布硬编码。
 * 状态值与后端 proto 定义对齐：0=free, 1=allocated, 2=disabled
 */

/** DMA 通道状态枚举 */
export enum DmaState {
  /** 空闲 — 未被任何硬件占用 */
  FREE = 0,
  /** 已分配 — 绑定到某硬件（或孤儿：allocated 但 bound_to 为空） */
  ALLOCATED = 1,
  /** 已禁用 — 被用户显式关闭 */
  DISABLED = 2,
}

/** DMA 状态 → 中文文本 */
const DMA_STATE_TEXT: Record<DmaState, string> = {
  [DmaState.FREE]: '空闲',
  [DmaState.ALLOCATED]: '已分配',
  [DmaState.DISABLED]: '已禁用',
}

/** DMA 状态 → CSS class 后缀 */
const DMA_STATE_CLASS: Record<DmaState, string> = {
  [DmaState.FREE]: 'dma-state-free',
  [DmaState.ALLOCATED]: 'dma-state-allocated',
  [DmaState.DISABLED]: 'dma-state-disabled',
}

/** DMA 状态 → Element Plus tag type */
const DMA_STATE_TAG_TYPE: Record<DmaState, string> = {
  [DmaState.FREE]: 'info',
  [DmaState.ALLOCATED]: 'success',
  [DmaState.DISABLED]: 'danger',
}

export function dmaStateText(state: number): string {
  return DMA_STATE_TEXT[state as DmaState] ?? '未知'
}

export function dmaStateClass(state: number): string {
  return DMA_STATE_CLASS[state as DmaState] ?? ''
}

export function dmaStateTagType(state: number): string {
  return DMA_STATE_TAG_TYPE[state as DmaState] ?? 'info'
}

/** DMA 是否处于"可重新绑定"状态（空闲/禁用/孤儿） */
export function isDmaRebindable(state: number, boundTo: string | undefined | null): boolean {
  return state === DmaState.FREE
    || state === DmaState.DISABLED
    || (state === DmaState.ALLOCATED && !boundTo)  // 孤儿状态：allocated 但 bound_to 为空
}
