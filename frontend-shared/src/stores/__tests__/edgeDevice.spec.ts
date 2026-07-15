import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useEdgeDeviceStore } from '../edgeDevice'

const { mockGetList, mockGetDetail } = vi.hoisted(() => ({
  mockGetList: vi.fn<(...args: any[]) => Promise<any>>(() => Promise.resolve({ items: [], total: 0 })),
  mockGetDetail: vi.fn<(...args: any[]) => Promise<any>>(),
}))

vi.mock('@/api/edgeDevice', () => ({
  edgeDeviceApi: {
    getList: mockGetList,
    getDetail: mockGetDetail,
    delete: vi.fn(),
  },
}))

describe('useEdgeDeviceStore list cache', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('reuses a fresh list for the same params', async () => {
    const store = useEdgeDeviceStore()
    const params = { page: 1, page_size: 24 }

    await store.fetchList(params)
    await store.fetchList(params)

    expect(mockGetList).toHaveBeenCalledTimes(1)
    expect(store.hasFreshList(params)).toBe(true)
    expect(store.hasCachedList(params)).toBe(true)
    expect(store.hasCachedList({ page: 2, page_size: 24 })).toBe(false)
  })

  it('does not reuse a list for different params', async () => {
    const store = useEdgeDeviceStore()

    await store.fetchList({ page: 1, page_size: 24 })
    await store.fetchList({ page: 2, page_size: 24 })

    expect(mockGetList).toHaveBeenCalledTimes(2)
  })

  it('does not let an old-session completion close a new-session loading state', async () => {
    let resolveOld!: (value: any) => void
    let resolveNew!: (value: any) => void
    mockGetList
      .mockImplementationOnce(() => new Promise(r => { resolveOld = r }))
      .mockImplementationOnce(() => new Promise(r => { resolveNew = r }))
    const store = useEdgeDeviceStore()
    const oldRequest = store.fetchList({ page: 1 }, true)
    store.clearCache()
    const newRequest = store.fetchList({ page: 2 }, true)
    resolveOld({ items: [], total: 0 })
    await oldRequest
    expect(store.listLoading).toBe(true)
    resolveNew({ items: [], total: 0 })
    await newRequest
    expect(store.listLoading).toBe(false)
  })

  it('normalizes semantically equivalent list params to the same cache key', async () => {
    const store = useEdgeDeviceStore()
    await store.fetchList({ page_size: 20, page: 1 })
    await store.fetchList({ page: 1, page_size: 20, status: undefined })
    expect(mockGetList).toHaveBeenCalledTimes(1)
  })

  it('keeps parameter-specific cached lists when another page becomes active', async () => {
    mockGetList
      .mockResolvedValueOnce({ items: [{ id: 1, name: 'Page-1' } as any], total: 1 })
      .mockResolvedValueOnce({ items: [{ id: 2, name: 'Page-2' } as any], total: 1 })
    const store = useEdgeDeviceStore()

    await store.fetchList({ page: 1, page_size: 24 })
    await store.fetchList({ page: 2, page_size: 24 })

    expect(store.list[0].name).toBe('Page-2')
    expect(store.getCachedList({ page: 1, page_size: 24 })?.items[0].name).toBe('Page-1')
  })

  it('does not let an older response overwrite the latest requested list', async () => {
    let resolveFirst!: (value: any) => void
    let resolveSecond!: (value: any) => void
    mockGetList
      .mockImplementationOnce(() => new Promise(resolve => { resolveFirst = resolve }))
      .mockImplementationOnce(() => new Promise(resolve => { resolveSecond = resolve }))
    const store = useEdgeDeviceStore()

    const first = store.fetchList({ page: 1, page_size: 24 })
    const second = store.fetchList({ page: 2, page_size: 24 })
    resolveSecond({ items: [{ id: 2, name: 'Latest' }], total: 1 })
    await second
    resolveFirst({ items: [{ id: 1, name: 'Older' }], total: 1 })
    await first

    expect(store.list[0].name).toBe('Latest')
  })

  it('deduplicates concurrent non-forced requests for the same params', async () => {
    let resolveRequest!: (value: any) => void
    mockGetList.mockImplementationOnce(() => new Promise(resolve => { resolveRequest = resolve }))
    const store = useEdgeDeviceStore()
    const params = { page: 1, page_size: 24 }

    const first = store.fetchList(params)
    const second = store.fetchList(params)
    expect(mockGetList).toHaveBeenCalledTimes(1)
    resolveRequest({ items: [], total: 0 })
    await Promise.all([first, second])
  })

  it('joins the latest forced refresh instead of an older superseded request', async () => {
    let resolveOlder!: (value: any) => void
    let resolveRefresh!: (value: any) => void
    mockGetList
      .mockImplementationOnce(() => new Promise(resolve => { resolveOlder = resolve }))
      .mockImplementationOnce(() => new Promise(resolve => { resolveRefresh = resolve }))
    const store = useEdgeDeviceStore()
    const params = { page: 1, page_size: 24 }

    const older = store.fetchList(params)
    const refresh = store.fetchList(params, true)
    const mount = store.fetchList(params)
    expect(mockGetList).toHaveBeenCalledTimes(2)

    resolveOlder({ items: [{ id: 1, name: 'Older' }], total: 1 })
    await older
    expect(store.getCachedList(params)).toBeUndefined()
    resolveRefresh({ items: [{ id: 2, name: 'Fresh' }], total: 1 })
    await Promise.all([refresh, mount])
    expect(store.list[0].name).toBe('Fresh')
  })

  it('invalidates totals and freshness for every cached page after deletion', async () => {
    mockGetList
      .mockResolvedValueOnce({ items: [{ id: 1, name: 'Delete-me', status: 'active' }], total: 3 })
      .mockResolvedValueOnce({ items: [{ id: 2, name: 'Other-page' }], total: 3 })
      .mockResolvedValueOnce({ items: [{ id: 3, name: 'Offline' }], total: 1 })
    const store = useEdgeDeviceStore()
    const pageOne = { page: 1, page_size: 1 }
    const pageTwo = { page: 2, page_size: 1 }
    const offlinePage = { page: 1, page_size: 1, status: 'offline' }
    await store.fetchList(pageOne)
    await store.fetchList(pageTwo)
    await store.fetchList(offlinePage)

    await store.deleteDevice(1)

    expect(store.getCachedList(pageOne)?.items).toEqual([])
    expect(store.getCachedList(pageOne)?.total).toBe(3)
    expect(store.getCachedList(pageTwo)?.total).toBe(3)
    expect(store.getCachedList(offlinePage)?.total).toBe(1)
    expect(store.hasFreshList(pageOne)).toBe(false)
    expect(store.hasFreshList(pageTwo)).toBe(false)
    expect(store.hasFreshList(offlinePage)).toBe(false)
  })

  it('invalidates every parameterized list after create or edit writes', async () => {
    const store = useEdgeDeviceStore()
    const first = { page: 1, page_size: 24 }
    const filtered = { page: 1, page_size: 24, status: 'offline' }
    await store.fetchList(first)
    await store.fetchList(filtered)
    store.invalidateLists()
    expect(store.hasFreshList(first)).toBe(false)
    expect(store.hasFreshList(filtered)).toBe(false)
  })

  it('does not join a pre-delete in-flight request after deletion', async () => {
    let resolveOlder!: (value: any) => void
    mockGetList
      .mockImplementationOnce(() => new Promise(resolve => { resolveOlder = resolve }))
      .mockResolvedValueOnce({ items: [{ id: 2, name: 'After-delete' }], total: 1 })
    const store = useEdgeDeviceStore()
    const params = { page: 1, page_size: 24 }
    const older = store.fetchList(params)
    await store.deleteDevice(1)
    const afterDelete = store.fetchList(params)
    expect(mockGetList).toHaveBeenCalledTimes(2)
    resolveOlder({ items: [{ id: 1, name: 'Deleted' }], total: 2 })
    await Promise.all([older, afterDelete])
    expect(store.getCachedList(params)?.items[0].name).toBe('After-delete')
  })

  it('does not restore detail cache when a pre-logout request completes late', async () => {
    let resolveDetail!: (value: any) => void
    mockGetDetail.mockImplementationOnce(() => new Promise(resolve => { resolveDetail = resolve }))
    const store = useEdgeDeviceStore()

    const pending = store.fetchDetail(1, true)
    store.clearCache()
    resolveDetail({ id: 1, name: 'Old user device' })
    await pending

    expect(store.getCachedDetail(1)).toBeUndefined()
  })

  it('does not let an older detail response replace a newer one', async () => {
    let resolveOlder!: (value: any) => void
    let resolveNewer!: (value: any) => void
    mockGetDetail
      .mockImplementationOnce(() => new Promise(resolve => { resolveOlder = resolve }))
      .mockImplementationOnce(() => new Promise(resolve => { resolveNewer = resolve }))
    const store = useEdgeDeviceStore()

    const older = store.fetchDetail(1, true)
    const newer = store.fetchDetail(1, true)
    resolveNewer({ id: 1, name: 'Newer' })
    await newer
    resolveOlder({ id: 1, name: 'Older' })
    await older

    expect(store.getCachedDetail(1)?.name).toBe('Newer')
  })

  it('does not let a pre-write detail request overwrite updateLocal', async () => {
    let resolveOlder!: (value: any) => void
    mockGetDetail.mockImplementationOnce(() => new Promise(resolve => { resolveOlder = resolve }))
    const store = useEdgeDeviceStore()
    const older = store.fetchDetail(1, true)

    store.updateLocal({ id: 1, name: 'Written' } as any)
    resolveOlder({ id: 1, name: 'Older' })
    await older

    expect(store.getCachedDetail(1)?.name).toBe('Written')
  })

  it('invalidates detail cache and blocks a pre-write response', async () => {
    let resolveOlder!: (value: any) => void
    mockGetDetail.mockImplementationOnce(() => new Promise(resolve => { resolveOlder = resolve }))
    const store = useEdgeDeviceStore()
    const older = store.fetchDetail(1, true)

    store.invalidateDetail(1)
    resolveOlder({ id: 1, name: 'Older' })
    await older

    expect(store.getCachedDetail(1)).toBeUndefined()
  })

  it('does not apply a previous-session delete completion or retain loading', async () => {
    const { edgeDeviceApi } = await import('@/api/edgeDevice')
    let resolveDelete!: () => void
    vi.mocked(edgeDeviceApi.delete).mockImplementationOnce(() => new Promise<void>(resolve => { resolveDelete = resolve }))
    const store = useEdgeDeviceStore()
    const deletion = store.deleteDevice(1)
    store.clearCache()
    resolveDelete()
    await expect(deletion).rejects.toThrow('会话已变更')
    expect(store.list).toEqual([])
    expect(store.listLoading).toBe(false)
  })
})