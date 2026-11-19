import client, { type ApiEnvelope } from './client'

/**
 * 固件身份与渠道（数据模型 §5.2 / 契约 §4.3）。
 *
 * 破坏性变更（M0，v1.5 方案）：`target_model` 与 `stable` 两列**已从库中删除**，
 * 前端不得再读写它们 —— 展示信息由 `model_key`（chip-flash，不含 p）+ `build_profile`
 * 承担；稳定性由 `channel` 表达。旧字段留在类型里会让「删列」在界面上表现为
 * 一排空型号，正是本节要消灭的形态（T0.6 / AC-17）。
 */
export interface Firmware {
  id: number
  version: string
  filename: string
  checksum: string
  size_bytes: number
  url: string
  changelog?: string
  min_from_version?: string
  created_at: string

  // ── 身份（来自镜像头 + sidecar 交叉校验，不接受人手填写）──────────────────
  /** `esp32c6` / `esp32s3` */
  chip?: string
  /** 8 / 16；0 = 未知（只有 legacy 行会如此） */
  flash_mb?: number
  /** `yes` / `no`；`''` 只出现在 identity_state='legacy' 的行 */
  psram?: string
  /** chip-flash，如 `esp32c6-n8`；**不含 p** */
  model_key?: string
  /** sidecar 的 profile，如 `s3p-n16`；仅展示与排障 */
  build_profile?: string
  elf_sha256?: string
  built_at_raw?: string
  idf_ver?: string

  // ── 生命周期与身份状态 ───────────────────────────────────────────────────
  channel?: FirmwareChannel | string
  /** 撤回时间；非空 = 已撤回（不可选、可恢复、可下载历史） */
  withdrawn_at?: string | null
  identity_state?: FirmwareIdentityState | string

  // ── T1b.1：**仅当列表带 node_id 过滤时**，后端逐行给出兼容判定 ─────────────
  /** false = 置灰（单一模式：命中即置灰） */
  compatible?: boolean
  /** 命中的闸门机器码（与 409 的 data.reasons 同源），用于展示原因 */
  reasons?: string[]
}

export type FirmwareChannel = 'stable' | 'beta' | 'internal'
export type FirmwareIdentityState = 'verified' | 'legacy' | 'duplicate'

/**
 * 列表查询参数（契约 §4.3）。
 *
 * `status` / `model` / `page` / `page_size` **已从契约删除**：后端不声明也不承诺语义。
 * 固件量级为几十条，端点不做分页，`data` 形状仍是 `Firmware[]`；
 * 页面的分页是**纯前端**的（避免一次删掉用户已有的翻页习惯）。
 */
export interface FirmwareListParams {
  node_id?: number | string
  chip?: string
  flash_mb?: number
  channel?: string
  q?: string
  include_withdrawn?: boolean
}

/**
 * 上传结果（契约补充：命中同 `elf_sha256` 的 legacy 行时**就地补齐**，不新增记录）。
 *
 * 后端此时返回 200 且 `data.completed_legacy = true`（而不是 409 FW_DUPLICATE）——
 * 语义是「这份固件以前就在库里，只缺 sidecar，这次补上了」。
 * 前端必须把这两件事说清楚：
 *   · 说成「上传成功」→ 运维会以为库里多了一条；
 *   · 说成「重复上传」→ 运维会以为这次没生效，反复重传。
 * 缺省（字段不存在 / false）时保持既有文案「上传成功」，向后兼容旧后端。
 */
export interface FirmwareUploadResult extends Firmware {
  completed_legacy?: boolean
}

/** 上传成功后的提示（level 决定 ElMessage 类型：补齐是 info，普通上传是 success）。 */
export interface FirmwareUploadOutcome {
  level: 'success' | 'info'
  message: string
}

/**
 * 上传结果的提示文案真源（单一出口，便于单测；组件只负责发消息）。
 *
 * 只有 **严格 true** 才走「补齐」分支：后端字段若为 missing / false / 字符串 "true"
 * 都按普通上传处理 —— 宁可少一次特殊提示，不可把普通上传误报成「补了身份」。
 */
export function firmwareUploadOutcome(
  result?: Pick<FirmwareUploadResult, 'completed_legacy'> | null,
): FirmwareUploadOutcome {
  if (result?.completed_legacy === true) {
    return { level: 'info', message: '已补齐该固件的身份信息（未新增记录）' }
  }
  return { level: 'success', message: '上传成功' }
}

/** PUT {id} 只允许这三项（version 由镜像决定、target_model/stable 列已删）。 */
export interface FirmwareUpdatePayload {
  changelog?: string
  channel?: string
  min_from_version?: string
}

/**
 * 上传表单字段名（multipart）。
 *
 * ⚠️ 与后端 handler 的契约点：bin 走 `file`（沿用既有实现），sidecar 走 `meta`。
 * 只在这里改一次即可全链路生效（FirmwareUploadDialog 也用它），
 * 避免「前端发 meta、后端读 sidecar」这种只有联调才会发现的错配。
 */
export const FIRMWARE_UPLOAD_BIN_FIELD = 'file'
export const FIRMWARE_UPLOAD_META_FIELD = 'meta'

/** 组装上传体（bin 必填 + sidecar 必填，§4.2「sidecar 必填」）。 */
export function buildFirmwareUploadFormData(bin: File, meta: File): FormData {
  const formData = new FormData()
  formData.append(FIRMWARE_UPLOAD_BIN_FIELD, bin)
  formData.append(FIRMWARE_UPLOAD_META_FIELD, meta)
  return formData
}

/* ────────────────────────────────────────────────────────────────────────────
 * 闸门原因码 → 用户可读文案（附录 A 的唯一前端映射）
 *
 * 与后端附录 A「错误码总表」逐条对应。文案原则：先说**事实**（哪里不匹配），
 * 再说**动作**（该怎么修）；硬件类一律不给「强制」假希望 —— 那类分支后端不接受 force，
 * 前端就不该出现强制入口（AC-8 UI 侧）。
 * ──────────────────────────────────────────────────────────────────────────── */
export const OTA_GATE_REASON_TEXT: Record<string, string> = {
  OTA_GATE_WITHDRAWN: '该固件已撤回，不能再用于升级',
  OTA_GATE_FIRMWARE_UNVERIFIED: '固件身份未验证（旧上传或重复构建），需重新上传带 .meta.json 的产物',
  OTA_GATE_HARDWARE_UNKNOWN: '节点硬件档案不完整（芯片/容量/PSRAM 有缺失），需先补录',
  OTA_GATE_MODEL_MISMATCH: '芯片型号与节点不一致',
  OTA_GATE_FLASH_MISMATCH: 'Flash 容量档位与节点不一致',
  OTA_GATE_PSRAM_REQUIRED: '该固件需要 PSRAM，节点没有 PSRAM',
  OTA_GATE_PSRAM_MISMATCH: '该固件不带 PSRAM，节点有 PSRAM',
  OTA_GATE_IMAGE_TOO_LARGE: '固件超出节点分区槽容量',
  OTA_GATE_NOT_AN_UPGRADE: '目标版本不高于节点当前版本',
  OTA_GATE_BELOW_MIN_VERSION: '节点当前版本低于该固件要求的最低版本',
}

/** 版本方向类原因（**唯二**允许 force 的分支，§6.3 第 9/10 条）。 */
export const OTA_FORCEABLE_REASONS = ['OTA_GATE_NOT_AN_UPGRADE', 'OTA_GATE_BELOW_MIN_VERSION'] as const

/** 机器码 → 文案；未知码原样返回（后端新增分支时至少不是空白）。 */
export function gateReasonText(reason: string): string {
  return OTA_GATE_REASON_TEXT[reason] || reason
}

/** 一组原因码 → 可读文案数组（去重、去空）。 */
export function describeGateReasons(reasons?: string[] | null): string[] {
  if (!Array.isArray(reasons)) return []
  const out: string[] = []
  for (const reason of reasons) {
    if (typeof reason !== 'string' || !reason) continue
    const text = gateReasonText(reason)
    if (!out.includes(text)) out.push(text)
  }
  return out
}

/**
 * 该固件能否被选用于 OTA（UI 侧的第一道过滤）。
 *
 * 判据与后端闸门第 1/2 条同源：撤回的不可选；身份未验证（legacy/duplicate）
 * 不进入可选集合（AC-17 明确要求「列表可见但不可选」）。`compatible === false`
 * 由服务端逐行给出（带 node_id 查询时），有则以它为准。
 */
export function isFirmwareSelectable(firmware: Firmware): boolean {
  if (firmware.withdrawn_at) return false
  const identity = firmware.identity_state
  if (identity && identity !== 'verified') return false
  if (firmware.compatible === false) return false
  return true
}

/** 身份状态 → 中文（'verified' 之外的两态都需要重传，必须让用户看见）。 */
export function firmwareIdentityText(state?: string): string {
  if (state === 'verified') return '已验证'
  if (state === 'legacy') return '未验证（旧数据）'
  if (state === 'duplicate') return '重复构建'
  return '未知'
}

/** 渠道 → 中文。 */
export function firmwareChannelText(channel?: string): string {
  if (channel === 'stable') return '稳定'
  if (channel === 'beta') return '灰度'
  if (channel === 'internal') return '内部'
  return channel || '未知'
}

/** 列表/详情展示用「档位」：优先 model_key，退回 build_profile，都无则空串。 */
export function firmwareProfileText(firmware: Firmware): string {
  const profile = firmware.build_profile || ''
  const modelKey = firmware.model_key || ''
  if (profile && modelKey && profile !== modelKey) return `${modelKey} / ${profile}`
  return modelKey || profile
}

/* ────────────────────────────────────────────────────────────────────────────
 * 上传 / 管理动作的错误文案（附录 A）
 *
 * 错误码 → 人话。**不再**只透出后端 message（多数是英文机器话术），
 * 而是给出「发生了什么 + 该怎么修」。未知码退回服务端 message。
 * ──────────────────────────────────────────────────────────────────────────── */
export const FIRMWARE_ERROR_TEXT: Record<string, string> = {
  FW_META_REQUIRED: '缺少 .meta.json 侧车文件：请把构建产出的同名 .meta.json 与 .bin 一起上传',
  FW_META_CONFLICT: '侧车与镜像不一致（chip / flash_mb / version / sha256 至少一项对不上）：请用构建产出的原始 .meta.json 重传',
  FW_NOT_IMAGE: '不是 ESP 固件镜像（magic 不是 0xE9）',
  FW_TRUNCATED: '固件文件被截断或过短，无法解析镜像头',
  FW_PROJECT_MISMATCH: '固件 project 不是 ehome_collector，传错项目的产物了',
  FW_CHIP_UNSUPPORTED: '镜像内的芯片型号不在支持列表（esp32c6 / esp32s3）',
  FW_FLASH_UNSUPPORTED: '镜像内的 Flash 档位不受支持（只支持 8MB / 16MB）',
  FW_TOO_LARGE: '固件超出该容量档位的分区槽上限',
  FW_DUPLICATE: '同一份构建已经上传过（elf_sha256 重复），无需重传',
  FIRMWARE_IN_USE: '该固件有在途 OTA 任务，暂时不能撤回或删除；等任务结束后再试',
}

/** 从任意错误里取机器码（ApiError.errorCode，其次 response.data.error_code）。 */
export function firmwareErrorCode(error: unknown): string {
  const candidate = error as { errorCode?: unknown; response?: { data?: { error_code?: unknown } } } | null
  const direct = candidate?.errorCode
  if (typeof direct === 'string' && direct) return direct
  const nested = candidate?.response?.data?.error_code
  return typeof nested === 'string' ? nested : ''
}

/** 上传失败的人话文案：已知码给动作建议，未知码退回服务端 message / 兜底。 */
export function uploadErrorMessage(error: unknown): string {
  const code = firmwareErrorCode(error)
  if (code && FIRMWARE_ERROR_TEXT[code]) return FIRMWARE_ERROR_TEXT[code]
  const message = (error as { message?: unknown } | null)?.message
  return typeof message === 'string' && message ? message : '上传失败'
}

/**
 * 撤回/删除保护（409 FIRMWARE_IN_USE）的人话文案。
 *
 * 设计要点（T1b.2 / DEF-10）：**不能**把 409 说成「操作失败」——
 * 用户需要知道「不是坏了，是有任务在跑，等它结束」。`el-button` 的禁用态
 * 做不到这一点（禁用不说话），所以这里选择「允许点击 + 明确解释」。
 */
export function firmwareActionErrorMessage(
  error: unknown,
  action: 'delete' | 'withdraw' | 'download' | 'copy-url'
): string {
  const code = firmwareErrorCode(error)
  if (code === 'FIRMWARE_IN_USE') {
    return action === 'delete'
      ? '删除被拒绝：该固件有在途 OTA 任务，等任务结束或取消后再删除'
      : '撤回被拒绝：该固件有在途 OTA 任务，等任务结束或取消后再撤回'
  }
  if (code && FIRMWARE_ERROR_TEXT[code]) return FIRMWARE_ERROR_TEXT[code]
  const message = (error as { message?: unknown } | null)?.message
  const fallbackByAction: Record<typeof action, string> = {
    delete: '删除失败',
    withdraw: '撤回失败',
    download: '下载地址获取失败',
    'copy-url': '下载链接获取失败',
  }
  const fallback = fallbackByAction[action]
  return typeof message === 'string' && message ? message : fallback
}

/** 下载票（30 分钟有效）；expires_at 为 RFC3339，供 UI 提示有效期。 */
export interface FirmwareDownloadTicket {
  url: string
  expires_at: string
}

export const firmwareApi = {
  async getList(params?: FirmwareListParams): Promise<{ total: number, list: Firmware[] }> {
    // 后端统一 envelope: {code, data: Firmware[], message}（**故意不分页**，见 Params 注释）
    const response = await client.get<unknown, ApiEnvelope<Firmware[]>>('/api/v1/firmwares', { params })
    const list = response.data ?? []
    return { total: list.length, list }
  },

  async upload(formData: FormData): Promise<FirmwareUploadResult> {
    // 后端统一 envelope: {code, data: Firmware & {completed_legacy?: boolean}, message}
    const response = await client.post<unknown, ApiEnvelope<FirmwareUploadResult>>(
      '/api/v1/firmwares/upload',
      formData
    )
    return response.data
  },

  async update(id: number, data: FirmwareUpdatePayload): Promise<void> {
    await client.put(`/api/v1/firmwares/${id}`, data)
  },

  /** 撤回（契约 §4.3）：有在途任务 → 409 FIRMWARE_IN_USE。 */
  async withdraw(id: number): Promise<void> {
    await client.post(`/api/v1/firmwares/${id}/withdraw`)
  },

  async delete(id: number): Promise<void> {
    await client.delete(`/api/v1/firmwares/${id}`)
  },

  /**
   * 取一次性下载票（修复「下载/复制链接 401」）。
   *
   * 公开下载路由只认 HMAC 票据（expires+signature），不接受 JWT——
   * 旧实现把长期 token 拼进 URL，服务端从未有这条分支，所以必然 401。
   * 这里改为在已鉴权接口里换票，再由浏览器打开票据 URL。
   */
  async createDownloadTicket(id: number): Promise<FirmwareDownloadTicket> {
    const response = await client.post<unknown, ApiEnvelope<FirmwareDownloadTicket>>(
      `/api/v1/firmwares/${id}/download-ticket`
    )
    return response.data
  }
}
