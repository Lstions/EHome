import { computed } from 'vue'
import { hexOf, num } from './bmsFormat'

/**
 * BMS 保护状态（12 位掩码）的判定口径。
 *
 * 为什么单独一个模块：保护位决定「保护状态卡」的 12 格明细，也决定 MOS 卡
 * 「可能被硬件保护关断」的提示（bit0 / bit9 / bit10）。拆分后这两处分别在
 * 子组件内部计算，但**口径只能有一份**，否则两处会各自漂移。
 *
 * ⚠ 安全语义（拆分前既有注释，逐字保留）：
 *   只有真的收到 protection_status 才谈「已触发」；未知时不得推断（未知 ≠ 全部正常）。
 */

export const PROTECTION_BITS: Array<{ bit: number; label: string }> = [
  { bit: 0, label: '单体过压' },
  { bit: 1, label: '单体欠压' },
  { bit: 2, label: '整组过压' },
  { bit: 3, label: '整组欠压' },
  { bit: 4, label: '充电过温' },
  { bit: 5, label: '充电低温' },
  { bit: 6, label: '放电过温' },
  { bit: 7, label: '放电低温' },
  { bit: 8, label: '充电过流' },
  { bit: 9, label: '放电过流' },
  { bit: 10, label: '短路保护' },
  { bit: 11, label: '前端 IC 错误' },
]

export interface ProtectionBit { bit: number; label: string; active: boolean }

/**
 * 由实时值字典（latestData）派生保护位。传入取值函数而不是裸值：
 * 实时字典会随 WS 推送整体替换，computed 必须跟着重算（调用方传 () => props.latestData）。
 */
export function useBmsProtection(latestData: () => Record<string, any> | null) {
  const hasProtectionData = computed(() => num(latestData()?.protection_status) !== null)
  const protectionStatus = computed(() => num(latestData()?.protection_status) ?? 0)
  const protHex = computed(() => hexOf(protectionStatus.value))
  // （2026-10-16 已删除 protectExpanded：用户要求保护明细默认展开、不留收起/展开交互）
  function protectionActive(bit: number): boolean {
    return (protectionStatus.value & (1 << bit)) !== 0
  }
  const protectionBits = computed<ProtectionBit[]>(() =>
    PROTECTION_BITS.map((p) => ({ ...p, active: protectionActive(p.bit) })),
  )
  /** 只有真的收到 protection_status 才谈「已触发」；未知时不得推断（未知 ≠ 全部正常） */
  const activeProtections = computed(() =>
    hasProtectionData.value ? protectionBits.value.filter((p) => p.active) : [],
  )
  return { hasProtectionData, protectionStatus, protHex, protectionActive, protectionBits, activeProtections }
}
