import { describe, it, expect, vi, beforeEach } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useNotificationDeliveryStore } from '../notificationDelivery'
import { notificationChannelApi } from '@/api/notificationChannel'
import { makeDelivery } from '@/views/notification/__tests__/deliveryFixtures'

/**
 * 投递审计 store：只读列表 + 真分页参数透传 + 失败写 error 并 rethrow。
 *
 * 与页面级 spec 的分工：页面 spec 断言"页面把参数拼对了"，
 * 这里断言 store **不再加工**这些参数（原样透传给 api），两层都要有 ——
 * 只测一层的话，"store 悄悄补了 page_size=20 覆盖调用方"这类改动不会被发现。
 */

vi.mock('@/api/notificationChannel', () => ({
  notificationChannelApi: { listDeliveries: vi.fn(), list: vi.fn() },
}))

const mockedApi = vi.mocked(notificationChannelApi)

beforeEach(() => {
  setActivePinia(createPinia())
  vi.clearAllMocks()
  mockedApi.listDeliveries.mockResolvedValue({ items: [makeDelivery()], total: 1, page: 1, page_size: 20 })
})

describe('useNotificationDeliveryStore', () => {
  it('fetchList 把分页/过滤参数**原样**透传给 api（store 不加工、不补默认值）', async () => {
    const store = useNotificationDeliveryStore()
    await store.fetchList({ channel_id: 7, state: 'failed', page: 2, page_size: 50 })
    expect(mockedApi.listDeliveries).toHaveBeenCalledWith({ channel_id: 7, state: 'failed', page: 2, page_size: 50 })
  })

  it('items 用响应**整体替换**（翻页后不得残留上一页的行）', async () => {
    const store = useNotificationDeliveryStore()
    mockedApi.listDeliveries.mockResolvedValueOnce({
      items: [makeDelivery({ id: 91 }), makeDelivery({ id: 92 })], total: 42, page: 1, page_size: 20,
    })
    await store.fetchList({ page: 1, page_size: 20 })
    expect(store.items.map((d) => d.id)).toEqual([91, 92])
    expect(store.total).toBe(42)

    mockedApi.listDeliveries.mockResolvedValueOnce({
      items: [makeDelivery({ id: 71 })], total: 42, page: 2, page_size: 20,
    })
    await store.fetchList({ page: 2, page_size: 20 })
    // 追加式实现会得到 [91, 92, 71] —— 那正是"分页器说第 2 页、表格却有 3 行"的假象来源
    expect(store.items.map((d) => d.id)).toEqual([71])
  })

  it('total 是全量总数（后端 Count 结果），不是当前页条数', async () => {
    const store = useNotificationDeliveryStore()
    mockedApi.listDeliveries.mockResolvedValueOnce({
      items: [makeDelivery({ id: 71 })], total: 42, page: 3, page_size: 20,
    })
    await store.fetchList({ page: 3, page_size: 20 })
    expect(store.items).toHaveLength(1)
    expect(store.total).toBe(42)
  })

  it('失败：写 error 并 rethrow（页面据此展示重试入口，store 自己不弹窗）', async () => {
    const store = useNotificationDeliveryStore()
    mockedApi.listDeliveries.mockRejectedValueOnce(new Error('查询投递记录失败'))
    await expect(store.fetchList({ page: 1, page_size: 20 })).rejects.toThrow('查询投递记录失败')
    expect(store.error).toBe('查询投递记录失败')
    expect(store.loading).toBe(false)
  })

  it('成功：清掉上一次的 error（否则重试成功后错误条会一直挂着）', async () => {
    const store = useNotificationDeliveryStore()
    mockedApi.listDeliveries.mockRejectedValueOnce(new Error('boom'))
    await expect(store.fetchList({ page: 1 })).rejects.toThrow()
    expect(store.error).toBe('boom')
    await store.fetchList({ page: 1 })
    expect(store.error).toBeNull()
  })

  it('响应缺 items/total 时不抛异常（回落成空列表 + 0，而不是把 undefined 灌进表格）', async () => {
    const store = useNotificationDeliveryStore()
    mockedApi.listDeliveries.mockResolvedValueOnce({} as never)
    await store.fetchList({ page: 1 })
    expect(store.items).toEqual([])
    expect(store.total).toBe(0)
  })
})
