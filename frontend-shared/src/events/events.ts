/**
 * WebSocket 事件名常量 — 单一来源 (Single Source of Truth)
 *
 * 必须与后端 backend/internal/events/events.go 一一对应
 * CI: scripts/check_ws_event_names.sh 验证 union match
 *
 * 命名规范: <noun>_<verb> (全小写, 下划线分隔)
 */

export const WS_EVENT = {
  // 节点 (v2.2)
  NODE_STATUS: 'node_status',
  NODE_CONFIG_SYNCED: 'node_config_synced',
  NODE_CONFIG_CHANGED: 'node_config_changed',

  // 边缘设备 (v2.2)
  EDGE_DEVICE_STATUS: 'edge_device_status',

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

  // 通道写入 (v2.2)
  CHANNEL_WRITE: 'channel_write',
  CHANNEL_WRITE_ERROR: 'channel_write_error',

  // ─── v2.1 兼容 (6个月后删除, remove after 2026-12) ───
  COLLECTOR_STATUS: 'collector_status',
  COLLECTOR_CONFIG_SYNCED: 'collector_config_synced',
  DEVICE_STATUS: 'device_status',
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

/**
 * v2.1 → v2.2 事件名映射 (兼容层)
 * 用于双订阅，确保前端同时处理新旧事件名
 * TODO: 2026-12 后删除
 */
export const WS_EVENT_COMPAT: Record<string, WsEventName> = {
  // v2.1 → v2.2
  [WS_EVENT.COLLECTOR_STATUS]: WS_EVENT.NODE_STATUS,
  [WS_EVENT.COLLECTOR_CONFIG_SYNCED]: WS_EVENT.NODE_CONFIG_SYNCED,
  [WS_EVENT.DEVICE_STATUS]: WS_EVENT.EDGE_DEVICE_STATUS,
}

/**
 * 创建兼容订阅函数
 * 同时订阅新事件名和旧事件名，返回统一的取消订阅函数
 *
 * @example
 * // v2.1 compat: 同时订阅 collector_status 和 node_status
 * const unsubscribe = subscribeCompat(wsStore, WS_EVENT.NODE_STATUS, handler)
 */
export function createCompatSubscribe(
  subscribe: (type: string, handler: (msg: any) => void) => () => void
) {
  return (eventName: WsEventName, handler: (msg: any) => void): (() => void) => {
    const unsubscribers: Array<() => void> = []

    // 订阅新事件名
    unsubscribers.push(subscribe(eventName, handler))

    // 检查是否有对应的旧事件名需要兼容订阅
    const oldEventName = Object.entries(WS_EVENT_COMPAT).find(([, newName]) => newName === eventName)?.[0]
    if (oldEventName) {
      // v2.1 compat: 同时订阅旧事件名
      unsubscribers.push(subscribe(oldEventName, handler))
    }

    // 返回统一的取消订阅函数
    return () => {
      unsubscribers.forEach(unsub => unsub())
    }
  }
}
