/**
 * BMS 详情页「实时数据」首屏四态判定（2026-10-21 修复「加载中伪装成空数据」）。
 *
 * 缺陷（生产实测）：进入 /edge-device/14 后 0.5~6s 内，指标卡显示「—」、
 * 保护状态卡显示「暂无数据」+ 12 个「正常」徽标、MOS 卡显示「尚无 fet_status 数据」，
 * 而真实状态是**还在加载**（快照请求 in-flight 或设备尚未首帧上报）。
 * 把「未知」渲染成「空数据 / 正常 / 关断」是**信息错误**：保护位尤其危险，
 * 「未知」被当成「正常」会让用户错过真实故障。
 *
 * 四态（与逻辑设备页同款，互斥）：
 *   loading —— 首屏请求在途，或设备尚未首帧上报（宽限期内）；
 *   error   —— 请求失败且仍无任何数据（页面给独立错误文案 + 重试）；
 *   data    —— 已有任意一帧实时数据；
 *   empty   —— 宽限期已过、请求成功但设备确实没有上报任何数据（明确的空态，不是加载中）。
 *
 * 纯函数便于单测锁定两条硬契约：
 *   ① 加载中（loading）不得被判定成 empty / error；
 *   ② 一旦有数据（data）优先展示数据，即使之前失败过（WS 后到也要能自愈）。
 */

export type RealtimeViewState = 'loading' | 'error' | 'empty' | 'data'

export interface RealtimeViewSignals {
  /** 是否已收到任何一帧实时数据（WS 推送或最新快照） */
  hasData: boolean
  /** 最新快照请求是否在途（首屏/重试） */
  snapshotLoading: boolean
  /** 最新快照请求失败原因（空串 = 无错误） */
  snapshotError: string
  /** 首屏等待宽限期是否已过（设备按轮询周期上报，首帧可能晚于请求返回） */
  graceOver: boolean
}

/** 首屏等待宽限期（ms）：快照已返回但设备尚未推送时，先按「加载中」表达，避免把等待说成「暂无数据」。 */
export const REALTIME_FIRST_DATA_GRACE_MS = 12000

export function resolveRealtimeViewState(s: RealtimeViewSignals): RealtimeViewState {
  // 有数据优先：即使之前快照失败，WS 后到的数据也要能自愈成数据态
  if (s.hasData) return 'data'
  // 请求失败且无数据 ⇒ 错误态（独立文案 + 重试），绝不与「暂无数据」共用
  if (s.snapshotError) return 'error'
  // 请求在途，或首帧宽限期未过 ⇒ 加载中
  if (s.snapshotLoading || !s.graceOver) return 'loading'
  return 'empty'
}
