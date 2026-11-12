/**
 * 列表页「展示映射」单一出口：状态 → 中文/标签色、型号 → 图标/色调、时间与原始数据 → 可读文本。
 *
 * 为什么从 EdgeDeviceList.vue 搬出来：这些函数不碰任何页面状态，只依赖 @/utils/* 的纯映射，
 * 却被表格列、设备卡片、历史面板、两处 CSV 导出共用。留在 3000+ 行的页面 SFC 里，
 * 历史面板（子组件）就无法复用 —— 只能复制一份，而复制出来的第二份必然与原版漂移。
 *
 * 刻意**不**搬 getDeviceBusLabel / channelCardLabel：
 *   · getDeviceBusLabel 的取值口径与 3 个调用点被 EdgeDeviceListBusLabel.spec.ts 按**页面源文件**
 *     逐字钉死（含 `device.channel_hardware_id || device.channel?.hardware_id || UNKNOWN`）；
 *   · channelCardLabel 的实现体（`getHardwareDisplay(ch.hardware_type, ch.hardware_id)`）被
 *     EdgeDeviceList.spec.ts 按页面源文件钉死。搬走等于把门禁指向空壳，所以留在页面。
 */
import { ElMessage } from 'element-plus'
import { feedback } from '@/utils/feedback'
import { UNKNOWN } from '@/utils/format'
import type { TagType } from '@/utils/tagType'
import {
  getDeviceTypeIcon,
  getDeviceTypeLabel as getGlobalDeviceTypeLabel,
  getDeviceTypeTone,
} from '@/utils/deviceType'

// 设备类型相关
export const getDeviceIcon = (type: string) => {
  return getDeviceTypeIcon(type)
}

// 语义色调走全局单源（src/utils/deviceType.ts），本页不再维护第二份 classMap。
export const getDeviceTone = (type: string) => 'tone-' + getDeviceTypeTone(type)

export const getDeviceTypeLabel = (type: string) => {
  return getGlobalDeviceTypeLabel(type)
}

// Health status tag type mapping (Element Plus tag types)
//
// 'pending' 必须与 online 区分（2026-10-03 缺陷 5）：设备刚创建、还没采到
// 任何数据时后端落 pending。此前模型默认 active、前端把 active 渲染成
// 「在线」，于是新建的空设备看起来已经正常工作了。等待数据是 warning
// 而非 success —— 它不是成功，也不是故障。
export function statusTagType(status: string): TagType {
  switch (status) {
    case 'active': return 'success'
    case 'online': return 'success'
    case 'pending':
    case 'initializing': return 'warning'
    case 'warning': return 'warning'
    case 'error': return 'danger'
    case 'disabled': return 'info'
    case 'offline': return 'info'
    default: return 'info'
  }
}

// Health status label mapping
//
// 'pending' 显示为「等待数据」而不是「在线」或「离线」：它表达的是
// 「已创建但尚未收到任何数据」这一事实，既不能宣称设备正常，
// 也不能在设备还在等第一帧时就说它故障。
export function statusLabel(status: string): string {
  switch (status) {
    case 'active': return '在线'
    case 'online': return '在线'
    case 'pending': return '等待数据'
    case 'initializing': return '初始化中'
    case 'warning': return '警告'
    case 'error': return '故障'
    case 'disabled': return '已禁用'
    case 'offline': return '离线'
    default: return status || '未知'
  }
}

// 复制到剪贴板（跟随 FirmwareManage 的复制交互模式）
export const copyText = async (text: string) => {
  try {
    await navigator.clipboard.writeText(text)
    ElMessage.success('已复制: ' + text)
  } catch {
    feedback.error('复制失败，请手动复制: ' + text)
  }
}

export const formatRelativeTime = (time: string | null | undefined) => {
  if (!time) return UNKNOWN
  const now = new Date()
  const date = new Date(time)
  const diff = now.getTime() - date.getTime()
  
  const minutes = Math.floor(diff / 60000)
  if (minutes < 1) return '刚刚'
  if (minutes < 60) return `${minutes}分钟前`
  if (minutes < 1440) return `${Math.floor(minutes / 60)}小时前`
  return `${Math.floor(minutes / 1440)}天前`
}

// 格式化通用数据为可读字符串
export const formatDeviceData = (data: any): string => {
  const entries = Object.entries(data).filter(([k]) =>
    !['timestamp', 'device_id', 'device_type', 'channel_id'].includes(k))
  if (entries.length === 0) return ''
  return entries.map(([k, v]) => {
    const num = Number(v)
    const val = Number.isFinite(num) ? num.toFixed(2) : v
    return `${k}: ${val}`
  }).join(' | ')
}

/**
 * composable 入口：页面 script setup 顶层解构后，模板里的 statusLabel(...) / formatRelativeTime(...)
 * 调用形态与搬移前完全一致（不改成 utils.xxx 的第二套写法）。
 */
export function useDeviceListPresentation() {
  return {
    getDeviceIcon,
    getDeviceTone,
    getDeviceTypeLabel,
    statusTagType,
    statusLabel,
    copyText,
    formatRelativeTime,
    formatDeviceData,
  }
}
