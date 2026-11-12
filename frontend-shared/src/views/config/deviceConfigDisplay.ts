/**
 * DeviceConfigList 的纯展示映射（拆分自页面原脚本，函数体与注释一字未改）。
 *
 * 为什么独立成模块：这些函数都是「入参 → 展示值 / 图标」的纯映射，
 * 被页面的筛选标签、卡片正文与预览对话框三处共用；放到 .ts 里既能被多个
 * 子组件复用，也避免把展示细节塞回页面或 composable（页面只留接线）。
 */
import { DataBoard, DataAnalysis, Files } from '@element-plus/icons-vue'
import { UNKNOWN } from '@/utils/format'
import { deviceTypeOptions } from '@/utils/deviceType'

// 总线图标
export const getBusIcon = (busType: string) => {
  const iconMap: Record<string, any> = {
    uart: DataBoard,
    i2c: DataAnalysis,
    spi: Files,
    adc: Files,
  }
  return iconMap[busType] || Files
}

// 设备类型标签
export const getDeviceTypeLabel = (type: string) => {
  return deviceTypeOptions.find(t => t.value === type)?.label || type
}

// 参数预览
export const getParamsPreview = (config: any) => {
  if (!config) return {}

  const preview: Record<string, any> = {}
  const keys = Object.keys(config).slice(0, 3)

  for (const key of keys) {
    let value = config[key]
    if (typeof value === 'number') {
      value = value.toString()
    }
    if (value && typeof value === 'string' && value.length > 10) {
      value = value.slice(0, 10) + '...'
    }
    preview[key] = value
  }

  return preview
}

// 格式化时间
export const formatTime = (time: string | null | undefined) => {
  if (!time || time === '0001-01-01T00:00:00Z' || time === '1970-01-01T00:00:00Z') return UNKNOWN
  const date = new Date(time)
  if (isNaN(date.getTime()) || date.getFullYear() <= 1970) return UNKNOWN
  return date.toLocaleString('zh-CN')
}
