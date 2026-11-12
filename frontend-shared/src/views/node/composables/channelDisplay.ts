import type { Channel } from '@/api/channel'
import { getHardwareDisplay, getHardwareLabel } from '@/utils/hardwareTag'

/**
 * 通道级展示与只读解析。
 *
 * 为什么从页面里抽出来：这些函数只依赖 Channel 本身（名称/状态/硬件标识/波特率字节），
 * 却被页面三处不同的区块用到 —— 通道健康卡、总线配置的「已创建通道」卡、以及
 * 波特率批量配置的范围计算。留在页面里会让「同一通道在两处显示成不同名字」再次分叉；
 * 抽成无状态函数后，各区块只是调用同一套口径，不复制判断。
 */

export function channelStatusText(ch: Channel): string {
  const s = (ch.status || '').toLowerCase()
  if (s === 'ok' || s === 'normal' || s === 'online' || s === 'running') return '正常'
  if (s === 'error' || s === 'failed' || s === 'warn') return '异常'
  return s ? s : '正常'
}

export function channelBadgeClass(ch: Channel): string {
  const t = channelStatusText(ch)
  if (t === '正常') return 'cb-ok'
  if (t === '异常') return 'cb-warn'
  return 'cb-off'
}

/**
 * 通道的硬件标识展示（name 为空时的回退）。
 * 判据（自带总线名则不重复前缀，如 'UART0' 不再拼成 "UART UART0"）集中在共享
 * hardwareIdIncludesBusName/getHardwareDisplay，避免各视图再维护一份总线名清单。
 */
export function channelHardwareDisplay(ch: Channel): string {
  return getHardwareDisplay(ch.hardware_type, String(ch.hardware_id || ''))
}

export function channelName(ch: Channel): string {
  return ch.name || channelHardwareDisplay(ch) || `通道 #${ch.id}`
}

/** 通道类型标签：后端 hardware_type 实测为大写（'UART'），统一归一后显示。 */
export function channelTypeLabel(ch: Channel): string {
  return getHardwareLabel(ch.hardware_type) || '未知总线'
}

/** 通道自身的硬件标识（如 UART1）+ 通道号，用于与「资源」区分开。 */
export function channelHardwareKey(ch: Channel): string {
  const hw = String(ch.hardware_id || '').trim() || '—'
  return `${hw} #${ch.id}`
}

/** 使能态：后端 enabled 为布尔；缺失时按未启用呈现（不假装已启用）。 */
export function channelEnabled(ch: Channel): boolean {
  return (ch as any).enabled === true
}

/** 该通道是否是 UART。后端 hardware_type 实测为大写，故必须归一后比较。 */
export function isUARTChannel(ch: Channel): boolean {
  return String(ch.hardware_type || '').toUpperCase() === 'UART'
}

/**
 * 该通道 bus_config 里当前生效的波特率（字节 2..5，big-endian）；解不出返回 null。
 * 布局与 withUARTBaudrate（backend/internal/api/channel_reconfigure.go）逐字节一致；
 * 本函数只**读**它，写入永远走服务端，避免前端自造一份 hex 布局形成第二真相。
 */
export function baudCurrentRate(ch: Channel): number | null {
  const raw = String((ch as any).bus_config || '').trim().replace(/^\\x/i, '')
  if (!/^[0-9a-f]+$/i.test(raw) || raw.length < 12) return null
  const value = Number.parseInt(raw.slice(4, 12), 16)
  return Number.isFinite(value) && value > 0 ? value : null
}
