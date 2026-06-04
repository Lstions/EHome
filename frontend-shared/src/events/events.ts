/**
 * WebSocket 事件名常量 — 单一来源 (Single Source of Truth)
 *
 * 必须与后端 backend/internal/events/events.go 一一对应
 * CI: scripts/check_ws_event_names.sh 验证 union match
 *
 * 命名规范: <noun>_<verb> (全小写, 下划线分隔)
 */

export const WS_EVENT = {
  // 采集器
  COLLECTOR_STATUS: 'collector_status',
  COLLECTOR_CONFIG_SYNCED: 'collector_config_synced',
  COLLECTOR_CONFIG_CHANGED: 'collector_config_changed',

  // 设备
  DEVICE_STATUS: 'device_status',

  // 数据
  DATA_UPDATE: 'data_update',

  // OTA
  OTA_PROGRESS: 'ota_progress',
  OTA_COMPLETED: 'ota_completed',

  // 通知
  NOTIFICATION: 'notification',

  // Ping
  PING_RESULT: 'ping_result',

  // 通道终端
  CHANNEL_DATA: 'channel_data',
  TERMINAL_ACK: 'terminal_ack',

  // 扫描
  SCAN_RESULT: 'scan_result',
} as const

/** 所有合法 WS 事件名的联合类型 */
export type WsEventName = typeof WS_EVENT[keyof typeof WS_EVENT]

/**
 * 校验事件名是否符合规范格式
 * - 全小写字母
 * - 下划线分隔 (推荐 <noun>_<verb>)
 * - 允许单字名 (如 'notification')，但推荐使用 <noun>_<verb>
 * - 不允许连续下划线或首尾下划线
 */
export function isValidEventName(name: string): boolean {
  return /^[a-z]+(_[a-z]+)*$/.test(name)
}

/**
 * 严格校验: 事件名必须符合 <noun>_<verb> 格式 (至少一个下划线)
 * 用于新事件的 lint 检查
 */
export function isStrictEventName(name: string): boolean {
  return /^[a-z]+(_[a-z]+)+$/.test(name)
}
