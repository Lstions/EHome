import type { Firmware, FirmwareListParams } from '@/api/firmware'

/**
 * 固件页的筛选条件与分页口径（纯函数，便于单测；组件只做绑定）。
 *
 * 筛选字段与后端 §4.3 契约一一对应：`node_id` / `chip` / `flash_mb` / `channel` /
 * `q` / `include_withdrawn`。旧的 `status` / `model` 已从契约删除，这里**不再出现**。
 */
export interface FirmwareFilters {
  /** 按节点视角看兼容性（后端据此逐行给 compatible / reasons） */
  node_id: string
  chip: string
  flash_mb: string
  channel: string
  q: string
  include_withdrawn: boolean
}

export function createFirmwareFilters(): FirmwareFilters {
  return {
    node_id: '',
    chip: '',
    flash_mb: '',
    channel: '',
    q: '',
    include_withdrawn: false,
  }
}

/** 是否有任何「收窄」条件（决定空态文案：没有固件 vs 没有匹配）。 */
export function hasActiveFirmwareFilters(filters: FirmwareFilters): boolean {
  return Boolean(
    filters.node_id.trim() ||
    filters.chip ||
    filters.flash_mb ||
    filters.channel ||
    filters.q.trim()
  )
}

/**
 * 组装列表请求参数：空串一律**不发**（后端把空串当精确匹配会得到 0 行）。
 * `include_withdrawn` 恒定发送（false 也是明确语义：只要在架固件）。
 */
export function buildFirmwareListParams(filters: FirmwareFilters): FirmwareListParams {
  const params: FirmwareListParams = { include_withdrawn: filters.include_withdrawn }
  const nodeId = filters.node_id.trim()
  if (nodeId) params.node_id = nodeId
  if (filters.chip) params.chip = filters.chip
  if (filters.flash_mb) params.flash_mb = Number(filters.flash_mb)
  const q = filters.q.trim()
  if (q) params.q = q
  if (filters.channel) params.channel = filters.channel
  return params
}

/**
 * 前端分页（端点**故意不分页**，返回全量几十条）。
 *
 * 保留前端分页是刻意的：一次性删掉翻页会让既有习惯与用例（FirmwareManage.spec
 * 的「total > 0 时必须渲染分页」）双双回归；而把整表一次铺满反而更难扫读。
 */
export function paginateFirmwares(list: Firmware[], page: number, pageSize: number): Firmware[] {
  if (!Array.isArray(list) || list.length === 0) return []
  const size = Math.max(1, Math.floor(pageSize))
  const totalPages = Math.max(1, Math.ceil(list.length / size))
  const current = Math.min(Math.max(1, Math.floor(page)), totalPages)
  const start = (current - 1) * size
  return list.slice(start, start + size)
}

/**
 * 上传前置校验：**一个**固件包（`.ehfw`，或同格式别名 `.zip`，§4.1）。
 *
 * 判据只在扩展名一层：真正确认「是不是包」靠服务端按内容嗅探首 4 字节
 * `PK\x03\x04`（§4.1「识别按内容，不看扩展名」），前端不复制那套规则。
 */
export function isFirmwarePackageFile(file: File | null | undefined): boolean {
  return Boolean(file && /\.(ehfw|zip)$/i.test(file.name))
}

/**
 * 裸镜像（`.bin`）：改造前是「两个必传文件之一」，现在是**最常见的一种拿错**。
 * 只为给出针对性文案（「需要打包成 .ehfw」）而保留，不参与上传判定。
 */
export function isFirmwareBinFile(file: File | null | undefined): boolean {
  return Boolean(file && /\.bin$/i.test(file.name))
}
