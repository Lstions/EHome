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
  /** 投递审计入口（投递审计页 NotificationDeliveries.vue 的查询入口）。 */
  deliveries_url: string
}

/**
 * 投递状态：与 models.DeliveryState* 一一对应（设计 §5 状态机 pending → delivered | failed）。
 *
 * pending 是**中间态**（"尝试已开始、结论未落库"）：后端先写一行 pending 再去出站，
 * 拿到结论后改写为 delivered/failed。因此页面上看到 pending 只表示"还在途中或进程
 * 在写入结论前退出了"，**不能**读成"已送达"，也不能读成"失败"。
 */
export type NotificationDeliveryState = 'pending' | 'delivered' | 'failed'

/**
 * 投递审计行（= 后端 models.NotificationDelivery 的逐字段投影，设计 §3）。
 *
 * 为什么类型要逐字段手写而不是从后端生成：后端出站形状就是这张**审计表本身**
 * （handler_notification_channel.go 的 listDeliveries 直接 Success(items)），
 * 表里根本没有任何密钥载体 —— 通道密钥在 notification_channels.secret 与 target_url
 * 的查询串里，审计行只有 error_message（落库前已过 RedactText）与 status_code。
 * 手写这九个字段，等于把"投递审计页拿不到密钥"这件事钉在类型层：页面想拼密钥也无处可取。
 */
export interface NotificationDelivery {
  id: number
  /** 被投递的通知 id（notifications 表） */
  notification_id: number
  /** 目标通道 id；通道被删除后审计行仍在（DELETE 不级联），故这里可能指向已不存在的通道 */
  channel_id: number
  state: NotificationDeliveryState
  /** 第几次尝试，从 1 开始；重试**新开一行**、attempt_no 递增（不是原地改） */
  attempt_no: number
  /** 出站 HTTP 状态码；0 = 没走到拿到响应那一步（连不上/DNS/SSRF 拒绝/超时） */
  status_code: number
  /** 失败原因（后端已脱敏并截断）；成功时为空串 */
  error_message: string
  /** 本次尝试耗时（毫秒） */
  duration_ms: number
  /** 审计行创建时刻（= 该次尝试开始时刻） */
  created_at: string
}

/** 投递审计查询参数（后端支持 channel_id / state 过滤 + page / page_size 真分页）。 */
export interface NotificationDeliveryListParams {
  /** 只看某条通道的投递；不传 = 全部通道 */
  channel_id?: number
  /** 只看某种状态；不传 = 全部状态 */
  state?: NotificationDeliveryState
  page?: number
  page_size?: number
}

/** 投递审计解包结果：后端 data 为 { items, total, page, page_size }（与通道列表同形）。 */
export interface NotificationDeliveryListResult {
  items: NotificationDelivery[]
  total: number
  page: number
  page_size: number
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
  /**
   * 投递审计列表（GET /notification-deliveries）。
   *
   * 服务端**真分页**：page / page_size 原样发给后端，返回的 items 就是**当前页切片**，
   * total 是全量总数（后端在 Offset/Limit 之前先 Count）。调用方不得对 items 再切片 ——
   * 那样页码与数据会双双脱节（分页器说第 2 页、内容却是第 1 页的前 N 条）。
   *
   * 参数不传即"不过滤"：channel_id / state 都省略时返回全部通道的全部审计行。
   * state 只能是 pending|delivered|failed —— 后端对非法值直接 400，前端类型与
   * 下拉项都不产生别的取值（不在这里做静默兜底，否则非法值会变成"看起来过滤了"）。
   */
  async listDeliveries(params?: NotificationDeliveryListParams): Promise<NotificationDeliveryListResult> {
    return unwrap<NotificationDeliveryListResult>(
      client.get<unknown, ApiEnvelope<NotificationDeliveryListResult>>('/api/v1/notification-deliveries', { params }),
    )
  },
}

export default notificationChannelApi
