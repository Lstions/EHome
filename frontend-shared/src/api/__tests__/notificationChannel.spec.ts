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
