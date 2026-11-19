/**
 * 边缘设备状态词汇表 —— 「后端状态字符串 → 前端 DeviceStatus」的**唯一**映射点。
 *
 * 为什么单列一个模块（原先藏在 normalize.ts 内部）：
 *   状态不只从 HTTP 响应来。实时层（stores/edgeDeviceLive.ts）从 WebSocket 的
 *   edge_device_status 事件里收到的是同一套裸字符串，它必须映射成**同一个**
 *   DeviceStatus —— 否则同一个设备在列表页（实时补丁）与详情页（HTTP 归一化）
 *   会呈现两种状态类型，而这种「双映射」正是长期维护里最容易漂移的地方。
 *
 * 为什么经 barrel（api/edgeDevice.ts）转出：
 *   normalize.ts 刻意不经 barrel 转出（它不是对外契约，见该文件头注释），
 *   所以共用映射不能留在那里；本模块是纯函数 + 常量，无请求逻辑，可以安全对外。
 */
import type { DeviceStatus } from './types'

/**
 * 后端状态 → 前端展示状态。
 *
 * 保留健康状态细分（active/warning/error/disabled）而不是收敛成 online/offline：
 * 设备「故障」与「离线」对用户的行动含义完全不同（前者要查数据源/协议，后者查供电）。
 */
export const STATUS_MAP: Record<string, DeviceStatus> = {
  active: 'active',
  online: 'online',
  offline: 'offline',
  warning: 'warning',
  error: 'error',
  disabled: 'disabled',
  pending: 'pending',
  initializing: 'initializing',
  unknown: 'unknown',
}

/**
 * 把后端状态字符串收敛为 DeviceStatus。
 *
 * 缺省（空串 / undefined）→ 'offline'：保持与旧 normalize() 完全一致的历史行为。
 * 未登记的新值 → 'unknown'：不静默当成离线，否则后端新增状态时界面会谎报故障。
 */
export function mapStatus(rawStatus?: string | null): DeviceStatus {
  if (!rawStatus) return 'offline'
  return STATUS_MAP[rawStatus] ?? 'unknown'
}
