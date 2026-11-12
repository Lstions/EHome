import { UNKNOWN } from '@/utils/format'

/**
 * NodeList 卡片/表格共用的「相对时间」文案（2026-11-12 随列表页拆分抽出）。
 *
 * 边界依据：同一个纯函数被两个子组件同时使用，放在任一子组件里都会让另一个反向依赖
 * 兄弟组件；因此抽成纯函数模块（同 dashboardFormat.ts / automationFormat.ts 的先例）。
 * 函数体逐字搬移，未改任何分档与文案。
 */
export const formatRelativeTime = (time: string) => {
  if (!time) return UNKNOWN
  const now = new Date()
  const date = new Date(time)
  const diff = now.getTime() - date.getTime()
  
  const minutes = Math.floor(diff / 60000)
  const hours = Math.floor(diff / 3600000)
  const days = Math.floor(diff / 86400000)
  
  if (minutes < 1) return '刚刚'
  if (minutes < 60) return `${minutes}分钟前`
  if (hours < 24) return `${hours}小时前`
  if (days < 7) return `${days}天前`
  return date.toLocaleDateString('zh-CN')
}
