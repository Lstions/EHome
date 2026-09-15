import client, { type ApiEnvelope } from './client'

/**
 * 外发通知通道 API（后端 handler_notification_channel.go 的冻结契约，本轮只用 list/remove）。
 *
 * 为什么类型要逐字段手写而不是从后端生成：后端出站形状是 notificationChannelView
 * 这个**白名单**（handler_notification_channel.go:160），里头根本没有 secret 字段 ——
 * 前端类型里也不该有，这样"把明文密钥渲染出来"在类型层就不可能发生。
 */

/** 通道类型：与 models.ChannelType* 的三个预设标识一一对应。 */
export type NotificationChannelType = 'webhook' | 'wecom' | 'onebot'

/** 最低告警级别：后端 notify.MinLevelOf 会把未知值归一化成 info。 */
export type NotificationMinLevel = 'info' | 'warning' | 'error' | 'critical'

/**
 * 通道视图（= 后端 notificationChannelView 的逐字段投影）。
 *
 * 凭据只以 secret_hint（末 4 位）+ has_secret（布尔）出现：
 * 后者让前端能区分"没密钥"与"密钥太短导致 hint 为空"，不必去猜。
 */
export interface NotificationChannel {
  id: number
  name: string
  type: NotificationChannelType
  /** 已脱敏的目标地址（企业微信 key= 这类查询串凭据由后端 RedactTargetURL 处理） */
  target_url: string
  /** 密钥末 4 位；未设置时为空串 */
  secret_hint: string
  has_secret: boolean
  template: string
  min_level: NotificationMinLevel
  enabled: boolean
  /** null = 未配置（应用层默认）；0 = 用户显式零值（设计 §7.6，二者语义不同，不可折叠） */
  timeout_sec: number | null
  max_retries: number | null
  allow_private: boolean
  created_at: string
  updated_at: string
}

/** 列表查询参数（后端支持 type / enabled 过滤 + page / page_size 分页）。 */
export interface NotificationChannelListParams {
  type?: NotificationChannelType
  enabled?: boolean
  page?: number
  page_size?: number
}

/** 列表解包结果：后端 data 为 { items, total, page, page_size }。 */
export interface NotificationChannelListResult {
  items: NotificationChannel[]
  total: number
  page: number
  page_size: number
}

/** 删除响应：后端明确回 deliveries_retained=true（审计行**不**连带删除）。 */
export interface NotificationChannelRemoveResult {
  id: number
  deleted: boolean
  deliveries_retained: boolean
}

/** 拦截器返回后端统一 envelope；只从 envelope.data 取值（同 api/dataSource.ts 范式）。 */
async function unwrap<T>(p: Promise<ApiEnvelope<T>>): Promise<T> {
  return (await p).data
}

export const notificationChannelApi = {
  async list(params?: NotificationChannelListParams): Promise<NotificationChannelListResult> {
    return unwrap<NotificationChannelListResult>(
      client.get<unknown, ApiEnvelope<NotificationChannelListResult>>('/api/v1/notification-channels', { params }),
    )
  },
  async remove(id: number): Promise<NotificationChannelRemoveResult> {
    return unwrap<NotificationChannelRemoveResult>(
      client.delete<unknown, ApiEnvelope<NotificationChannelRemoveResult>>(`/api/v1/notification-channels/${id}`),
    )
  },
}

export default notificationChannelApi
