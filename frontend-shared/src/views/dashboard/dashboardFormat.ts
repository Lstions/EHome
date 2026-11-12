import { UNKNOWN } from '@/utils/format'

/**
 * 仪表盘「展示口径」纯函数（原 Dashboard.vue `displayNodeName` / `formatData` /
 * `formatRawData` / `formatTime` 整段搬出，函数体一字未改，只加了 export）。
 *
 * 边界依据（为什么是这四个）：
 *   · 它们只做「原始值 → 展示字符串」的转换：入参是行数据 / 时间串，出参是字符串，
 *     没有组件状态、没有请求、没有副作用，是页面脚本里唯一可以零风险机械搬走的一类逻辑；
 *   · 同一份口径被两处渲染共用 —— 桌面「最新数据」宽表与窄屏卡片（后者还要把
 *     device/node 名格式化进 44px 触控标题），留在页面会让两处各长一版；
 *   · 搬走的是**纯函数**，不涉及生命周期与请求时序，因此不需要改动任何调用点的行为。
 *
 * 刻意留在页面的（源码文本门禁钉住，不能搬）：
 *   · `SUMMARY_PRIORITY` + `formatDataSummary` ——
 *     src/styles/__tests__/UiAuditSystemicFixes.spec.ts 断言 Dashboard.vue 源码里
 *     必须出现 `SUMMARY_PRIORITY`（「窄屏摘要不截断配置值」这条审计结论的锚点）。
 *     把它藏进本文件会让该门禁失去对象，故摘要口径整体留在页面。
 */

/**
 * 所属节点展示名：接口的 node_name 实测为空串（3 台设备全空），直接渲染会让整列空白。
 * 回退到「节点 <物理 ID>」，节点名缺失时也始终有可识别信息。
 */
export const displayNodeName = (row: { node_name?: string; node_id?: string }) => {
  const name = (row.node_name || '').trim()
  if (name) return name
  const id = (row.node_id || '').toString().trim()
  return id ? `节点 ${id}` : '—'
}

export const formatData = (data: Record<string, any>) => {
  if (!data) return UNKNOWN

  return Object.entries(data)
    .map(([key, value]) => {
      if (typeof value === 'object' && value !== null && 'value' in value) {
        const val = value.value
        const unit = value.unit || ''
        if (typeof val === 'number') {
          return `${key}: ${val.toFixed(2)} ${unit}`.trim()
        }
        return `${key}: ${val} ${unit}`.trim()
      }
      if (typeof value === 'number') {
        return `${key}: ${value.toFixed(2)}`
      }
      return `${key}: ${value}`
    })
    .join(', ')
}

export const formatRawData = (rawData: any) => {
  if (!rawData) return UNKNOWN
  if (typeof rawData === 'string') {
    // hex-encoded raw bytes from backend
    if (/^[0-9a-f]+$/i.test(rawData)) {
      const truncated = rawData.length > 64 ? rawData.slice(0, 64) + '…' : rawData
      return truncated
    }
    try {
      const parsed = JSON.parse(rawData)
      return JSON.stringify(parsed).slice(0, 80)
    } catch {
      return rawData.slice(0, 80)
    }
  }
  return JSON.stringify(rawData).slice(0, 80)
}

export const formatTime = (time: string | null | undefined) => {
  if (!time || time === '0001-01-01T00:00:00Z' || time === '1970-01-01T00:00:00Z') return UNKNOWN
  const date = new Date(time)
  if (isNaN(date.getTime()) || date.getFullYear() <= 1970) return UNKNOWN
  return date.toLocaleString('zh-CN')
}
