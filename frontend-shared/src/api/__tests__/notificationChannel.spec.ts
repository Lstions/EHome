import { describe, it, expect, vi, beforeEach } from 'vitest'

/**
 * 外发通知通道 api 层的路径与请求体契约（切片二新增的 create / update / test）。
 *
 * 为什么还要这一层：页面级测试把 api 模块整体 mock 掉了（它断言的是"页面调了
 * notificationChannelApi.test(1)"），**真正的 HTTP 路径与请求体**只有在这里才被验证。
 * 没有本文件的话，"测试按钮调 POST /:id/test" 这条验收就只剩下一半证据。
 */

const mockClient = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  put: vi.fn(),
  delete: vi.fn(),
  defaults: { baseURL: '' },
}))

vi.mock('../client', () => ({ default: mockClient }))

import { notificationChannelApi } from '../notificationChannel'
import type { NotificationChannelUpdatePayload } from '../notificationChannel'

const VIEW = { id: 7, name: '运维群', type: 'wecom', has_secret: true, secret_hint: 'abcd' }

beforeEach(() => {
  vi.clearAllMocks()
  mockClient.post.mockResolvedValue({ data: VIEW })
  mockClient.put.mockResolvedValue({ data: VIEW })
  mockClient.get.mockResolvedValue({ data: { items: [], total: 0, page: 1, page_size: 20 } })
})

describe('notificationChannelApi 写路径', () => {
  it('create → POST /api/v1/notification-channels，请求体原样发出', async () => {
    await notificationChannelApi.create({ name: '运维群', type: 'wecom', target_url: 'https://x', secret: 'k' })
    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/notification-channels', {
      name: '运维群', type: 'wecom', target_url: 'https://x', secret: 'k',
    })
  })

  it('update → PUT /api/v1/notification-channels/:id，且**不改写**请求体里的 secret 键', async () => {
    // 不含 secret 键：这正是"留空 = 不修改"的形状，api 层必须原样透传（不得补键、不得删键）
    const withoutSecret: NotificationChannelUpdatePayload = { name: '改名', target_url: 'https://x' }
    await notificationChannelApi.update(7, withoutSecret)
    const [url, body] = mockClient.put.mock.calls[0]
    expect(url).toBe('/api/v1/notification-channels/7')
    expect(Object.keys(body as object)).not.toContain('secret')
    // 与传入对象**同一引用**：任何"顺手加工"都会在这里暴露
    expect(body).toBe(withoutSecret)
  })

  it('update 传空串 → 空串原样发出（后端语义：清空密钥）', async () => {
    await notificationChannelApi.update(7, { name: '改名', target_url: 'https://x', secret: '' })
    expect(mockClient.put.mock.calls[0][1]).toMatchObject({ secret: '' })
  })

  it('test → POST /api/v1/notification-channels/:id/test，并解包 envelope.data', async () => {
    mockClient.post.mockResolvedValue({
      data: { channel_id: 7, notification_id: 3, state: 'pending', deliveries_url: '/api/v1/notification-deliveries?channel_id=7' },
    })
    const res = await notificationChannelApi.test(7)
    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/notification-channels/7/test')
    expect(res.state).toBe('pending')
    expect(res.channel_id).toBe(7)
  })
})

describe('notificationChannelApi 投递审计查询', () => {
  const ROW = {
    id: 91, notification_id: 3, channel_id: 7, state: 'failed',
    attempt_no: 2, status_code: 500, error_message: '接收端返回 500', duration_ms: 1234,
    created_at: '2026-09-15T10:00:00Z',
  }

  it('listDeliveries → GET /api/v1/notification-deliveries，page/page_size 走 query 参数（真分页）', async () => {
    mockClient.get.mockResolvedValue({ data: { items: [ROW], total: 42, page: 3, page_size: 20 } })
    const res = await notificationChannelApi.listDeliveries({ page: 3, page_size: 20 })

    // 断言的是**路径 + 参数**：分页必须是"打给后端的 query"，本地切片不会有任何 HTTP 调用。
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/notification-deliveries', {
      params: { page: 3, page_size: 20 },
    })
    // total 是**全量总数**（后端 Count 后回传），不是当前页条数 —— 分页器靠它算页数。
    expect(res.total).toBe(42)
    expect(res.page).toBe(3)
    expect(res.page_size).toBe(20)
    expect(res.items).toHaveLength(1)
  })

  it('listDeliveries → channel_id / state 原样发出，且不做"空值补键"', async () => {
    await notificationChannelApi.listDeliveries({ channel_id: 7, state: 'failed', page: 1, page_size: 20 })
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/notification-deliveries', {
      params: { channel_id: 7, state: 'failed', page: 1, page_size: 20 },
    })

    mockClient.get.mockClear()
    // 不过滤：params 里不得凭空出现 channel_id: undefined / state: '' ——
    // 后者会被后端当成"state= 非法"直接 400（后端对非空 state 做白名单校验）。
    const params = { page: 1, page_size: 20 }
    await notificationChannelApi.listDeliveries(params)
    const sent = mockClient.get.mock.calls[0][1].params as Record<string, unknown>
    expect(Object.keys(sent)).toEqual(['page', 'page_size'])
  })

  it('listDeliveries → 解包 envelope.data（拿到的就是 {items,total,page,page_size}）', async () => {
    mockClient.get.mockResolvedValue({ data: { items: [ROW], total: 1, page: 1, page_size: 20 } })
    const res = await notificationChannelApi.listDeliveries()
    expect(res.items[0].error_message).toBe('接收端返回 500')
    expect(res.items[0].status_code).toBe(500)
  })
})
