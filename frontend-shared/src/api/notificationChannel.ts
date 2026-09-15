import client, { type ApiEnvelope } from './client'

/**
 * 外发通知通道 API（后端 handler_notification_channel.go 的冻结契约：list/remove/create/update/test）。
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

/**
 * 创建请求体（= 后端 createNotificationChannelRequest 的逐字段投影）。
 *
 * timeout_sec / max_retries 是 `number | null`：后端 DTO 是 *int，null 与 0 **语义不同**
 * （设计 §7.6）—— null = 未配置（应用层默认值生效），0 = 用户显式零值
 * （max_retries=0 就是"不重试"）。表单里"留空"必须发 null，绝不能发 0。
 *
 * secret 可省略：不传 = 该通道暂不配置密钥（例如仅用 URL 认证的企业微信）。
 */
export interface NotificationChannelCreatePayload {
  name: string
  type: NotificationChannelType
  target_url: string
  secret?: string
  template?: string
  min_level?: NotificationMinLevel
  enabled?: boolean
  timeout_sec?: number | null
  max_retries?: number | null
  allow_private?: boolean
}

/**
 * 更新请求体（= 后端 updateNotificationChannelRequest 的逐字段投影）。
 *
 * ★ secret 的三态是本文件最关键的契约（设计 §4 冻结 + §7.6 零值陷阱）：
 *   - **键不出现**（或值为 undefined）→ 后端 *string == nil → **不改密钥**，连 secret_hint 都不动；
 *   - `secret: ''`                        → 后端拿到非 nil 的空串 → **清空密钥**；
 *   - `secret: 'xxx'`                     → 覆盖为新密钥。
 *
 * 因此本类型里 secret 是 `?: string` 而**不是** `string`：类型层就要求调用方显式表达
 * "这次到底动不动密钥"。历史上把 secret_hint 当 secret 回传、或留空时回传空串，
 * 都会静默清掉用户已配置的密钥 —— 这正是本类型的注释要挡住的两类实现。
 *
 * 其余字段全部可选：后端 updateNotificationChannelRequest 是**全字段指针**（部分更新），
 * 未传 = 不动该列。这里若照抄创建体的必填字段，会凭空禁止合法的部分更新
 * （例如"只改个名字"），把后端能力误封在类型层。
 */
export type NotificationChannelUpdatePayload = Partial<Omit<NotificationChannelCreatePayload, 'secret'>> & {
  secret?: string
}

/** 测试投递响应：后端立即返回 pending，投递结果异步落到 notification_deliveries。 */
export interface NotificationChannelTestResult {
  channel_id: number
  notification_id: number
  /** 后端固定回 pending（models.DeliveryStatePending）——"已发出"不等于"已送达"。 */
  state: string
  /** 投递审计入口（本轮不实现审计页，先按后端契约把字段透出）。 */
  deliveries_url: string
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
  async create(payload: NotificationChannelCreatePayload): Promise<NotificationChannel> {
    return unwrap<NotificationChannel>(
      client.post<unknown, ApiEnvelope<NotificationChannel>>('/api/v1/notification-channels', payload),
    )
  },
  /**
   * 更新通道（部分更新：只有出现在 payload 里的键才会被后端写库）。
   *
   * 调用方必须自己保证"留空的密钥"= **键不出现**：这里不做任何 JSON.stringify 层面的
   * 兜底（例如删掉 undefined 键）—— 那是把契约藏在看不见的地方；改为让类型
   * （NotificationChannelUpdatePayload.secret?: string）与调用点显式表达。
   */
  async update(id: number, payload: NotificationChannelUpdatePayload): Promise<NotificationChannel> {
    return unwrap<NotificationChannel>(
      client.put<unknown, ApiEnvelope<NotificationChannel>>(`/api/v1/notification-channels/${id}`, payload),
    )
  },
  /**
   * 发送测试消息（POST /:id/test）。
   *
   * 后端是**异步投递**：HTTP 200 只代表"测试消息已发出"，出站结果稍后落到投递审计
   * （响应 state 恒为 pending）。因此 UI 文案必须是"已发出"而不是"投递成功" ——
   * 这是本仓"假绿"纪律的直接应用：不能把"请求成功"说成"送达成功"。
   */
  async test(id: number): Promise<NotificationChannelTestResult> {
    return unwrap<NotificationChannelTestResult>(
      client.post<unknown, ApiEnvelope<NotificationChannelTestResult>>(`/api/v1/notification-channels/${id}/test`),
    )
  },
}

export default notificationChannelApi
