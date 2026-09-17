import type { TagType } from './tagType'

/**
 * Node ConfigSyncState 的展示口径 —— 单一权威。
 *
 * 后端可能写入的取值（backend/internal/nodemgr）：
 *   in_sync  ConfigResult 成功 / StatusReport 自愈
 *   syncing  已持久化 "syncing" 后发布 ConfigManifest
 *   failed   ConfigManifest 被拒或 ConfigResult 失败
 * 另有历史/兜底值 lag / error / unknown。
 *
 * 为什么必须集中：NodeOverview 与 NodeDetail 曾各自维护一份映射表，两份都漏了
 * "failed" —— 后端明明写的是 failed，UI 却落到兜底分支显示「未知」，而
 * 「未知」在视觉上与被修掉前完全一样地"不像出错"。同一个枚举在两处漂移，
 * 就是缺陷本身。
 */
export type ConfigSyncState = 'in_sync' | 'syncing' | 'failed' | 'lag' | 'error' | 'unknown'

const LABELS: Record<string, string> = {
  in_sync: '已同步',
  syncing: '同步中',
  failed: '同步失败',
  lag: '落后',
  error: '错误',
  unknown: '未知',
}

const TAGS: Record<string, TagType> = {
  in_sync: 'success',
  syncing: 'warning',
  failed: 'danger',
  lag: 'danger',
  error: 'danger',
  unknown: 'info',
}

/** 同步状态的中文标签；未知取值显式落到「未知」而不是静默显示正常。 */
export function configSyncStateLabel(state: unknown): string {
  return (typeof state === 'string' && LABELS[state]) || LABELS.unknown
}

/** 同步状态对应的 el-tag 类型。 */
export function configSyncStateTagType(state: unknown): TagType {
  return (typeof state === 'string' && TAGS[state]) || TAGS.unknown
}

/** 是否为"未达成同步"的失败态（供需要区分失败与未知的调用方使用）。 */
export function isConfigSyncFailed(state: unknown): boolean {
  return state === 'failed' || state === 'error'
}
