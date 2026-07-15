import { describe, it, expect, beforeEach, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useNodeStore } from '../node'
import type { Node } from '@/api/node'

// Mock nodeApi
const mockNodes: Node[] = [
  { id: 1, node_id: 'AABBCC', name: 'Node-1', model: 'ESP32S3', firmware_version: '1.0.0', status: 'online', connection_type: 'wifi', connection_quality: 90, latency_ms: 50, ping_latency_ms: 45, last_online_time: '2024-06-01T10:00:00Z', online_duration: 3600, capabilities: {}, config: {}, created_at: '2024-01-01T00:00:00Z' },
  { id: 2, node_id: 'DDEEFF', name: 'Node-2', model: 'ESP32C6', firmware_version: '2.0.0', status: 'offline', connection_type: 'wifi', connection_quality: 0, latency_ms: 0, ping_latency_ms: 0, last_online_time: '2024-06-01T08:00:00Z', online_duration: 0, capabilities: {}, config: {}, created_at: '2024-01-02T00:00:00Z' },
]

vi.mock('@/api/node', () => ({
  nodeApi: {
    getList: vi.fn(() => Promise.resolve({ items: mockNodes, total: mockNodes.length })),
    getDetail: vi.fn((id: number | string) => Promise.resolve(mockNodes.find(n => n.id === id || n.node_id === String(id)) || mockNodes[0])),
    update: vi.fn(() => Promise.resolve()),
    delete: vi.fn(() => Promise.resolve()),
  },
}))

describe('useNodeStore', () => {
  let store: ReturnType<typeof useNodeStore>

  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    store = useNodeStore()
  })

  // ── Initial state ─────────────────────────────────

  it('starts with empty nodes array', () => {
    expect(store.nodes).toEqual([])
    expect(store.total).toBe(0)
    expect(store.loading).toBe(false)
  })

  // ── fetchNodes ────────────────────────────────────

  it('fetchNodes populates nodes and total', async () => {
    await store.fetchNodes()
    expect(store.nodes.length).toBe(2)
    expect(store.total).toBe(2)
    expect(store.nodes[0].name).toBe('Node-1')
  })

  it('fetchNodes sets loading during fetch', async () => {
    const promise = store.fetchNodes()
    expect(store.loading).toBe(true)
    await promise
    expect(store.loading).toBe(false)
  })

  it('does not let an old-session completion close a new-session loading state', async () => {
    const { nodeApi } = await import('@/api/node')
    let resolveOld!: (value: any) => void
    let resolveNew!: (value: any) => void
    vi.mocked(nodeApi.getList)
      .mockImplementationOnce(() => new Promise(r => { resolveOld = r }))
      .mockImplementationOnce(() => new Promise(r => { resolveNew = r }))
    const oldRequest = store.fetchNodes({ page: 1 }, true)
    store.clearCache()
    const newRequest = store.fetchNodes({ page: 2 }, true)
    resolveOld({ items: mockNodes, total: 2 })
    await oldRequest
    expect(store.loading).toBe(true)
    resolveNew({ items: mockNodes, total: 2 })
    await newRequest
    expect(store.loading).toBe(false)
  })

  it('fetchNodes uses cache when fresh', async () => {
    await store.fetchNodes() // first fetch
    const { nodeApi } = await import('@/api/node')
    const callCountBefore = vi.mocked(nodeApi.getList).mock.calls.length
    await store.fetchNodes() // should use cache, no API call
    expect(vi.mocked(nodeApi.getList).mock.calls.length).toBe(callCountBefore)
  })

  it('fetchNodes does not reuse a fresh cache entry for different list params', async () => {
    const { nodeApi } = await import('@/api/node')
    await store.fetchNodes({ page: 1, page_size: 100 })
    const callCountBefore = vi.mocked(nodeApi.getList).mock.calls.length

    await store.fetchNodes({ page: 1, page_size: 20 })

    expect(vi.mocked(nodeApi.getList).mock.calls.length).toBe(callCountBefore + 1)
    expect(nodeApi.getList).toHaveBeenLastCalledWith({ page: 1, page_size: 20 })
  })

  it('hasFreshList only reports a cache hit for matching params', async () => {
    const params = { page: 1, page_size: 20 }
    expect(store.hasFreshList(params)).toBe(false)

    await store.fetchNodes(params)

    expect(store.hasFreshList(params)).toBe(true)
    expect(store.hasFreshList({ page: 2, page_size: 20 })).toBe(false)
  })

  it('hasCachedList reports matching cached data independently of freshness checks', async () => {
    const params = { page: 1, page_size: 20 }
    expect(store.hasCachedList(params)).toBe(false)
    await store.fetchNodes(params)
    expect(store.hasCachedList(params)).toBe(true)
    expect(store.hasCachedList({ page: 2, page_size: 20 })).toBe(false)
  })

  it('returns cached data for the requested params instead of the last global list', async () => {
    const { nodeApi } = await import('@/api/node')
    const pageOne = [{ ...mockNodes[0], id: 101, name: 'Page-1' }]
    const pageTwo = [{ ...mockNodes[1], id: 202, name: 'Page-2' }]
    vi.mocked(nodeApi.getList)
      .mockResolvedValueOnce({ items: pageOne, total: 1 } as any)
      .mockResolvedValueOnce({ items: pageTwo, total: 1 } as any)

    await store.fetchNodes({ page: 1, page_size: 20 })
    await store.fetchNodes({ page: 2, page_size: 20 })

    expect(store.nodes[0].name).toBe('Page-2')
    expect(store.getCachedList({ page: 1, page_size: 20 })?.items[0].name).toBe('Page-1')
  })

  it('does not let an older request overwrite the latest requested list', async () => {
    const { nodeApi } = await import('@/api/node')
    let resolveFirst!: (value: any) => void
    let resolveSecond!: (value: any) => void
    vi.mocked(nodeApi.getList)
      .mockImplementationOnce(() => new Promise(resolve => { resolveFirst = resolve }))
      .mockImplementationOnce(() => new Promise(resolve => { resolveSecond = resolve }))

    const first = store.fetchNodes({ page: 1, page_size: 20 })
    const second = store.fetchNodes({ page: 2, page_size: 20 })
    resolveSecond({ items: [{ ...mockNodes[1], name: 'Latest' }], total: 1 })
    await second
    resolveFirst({ items: [{ ...mockNodes[0], name: 'Older' }], total: 1 })
    await first

    expect(store.nodes[0].name).toBe('Latest')
    expect(store.getCachedList({ page: 1, page_size: 20 })?.items[0].name).toBe('Older')
  })

  it('joins the latest forced refresh instead of an older superseded request', async () => {
    const { nodeApi } = await import('@/api/node')
    let resolveOlder!: (value: any) => void
    let resolveRefresh!: (value: any) => void
    vi.mocked(nodeApi.getList)
      .mockImplementationOnce(() => new Promise(resolve => { resolveOlder = resolve }))
      .mockImplementationOnce(() => new Promise(resolve => { resolveRefresh = resolve }))
    const params = { page: 1, page_size: 20 }

    const older = store.fetchNodes(params)
    const refresh = store.fetchNodes(params, true)
    const mount = store.fetchNodes(params)
    expect(nodeApi.getList).toHaveBeenCalledTimes(2)

    resolveOlder({ items: [{ ...mockNodes[0], name: 'Older' }], total: 1 })
    await older
    expect(store.getCachedList(params)).toBeUndefined()
    resolveRefresh({ items: [{ ...mockNodes[1], name: 'Fresh' }], total: 1 })
    await Promise.all([refresh, mount])
    expect(store.nodes[0].name).toBe('Fresh')
  })

  it('fetchNodes force=true bypasses cache', async () => {
    await store.fetchNodes() // first fetch
    const { nodeApi } = await import('@/api/node')
    const callCountBefore = vi.mocked(nodeApi.getList).mock.calls.length
    await store.fetchNodes(undefined, true) // force
    expect(vi.mocked(nodeApi.getList).mock.calls.length).toBeGreaterThan(callCountBefore)
  })

  // ── fetchDetail ───────────────────────────────────

  it('fetchDetail fetches and caches node detail', async () => {
    const node = await store.fetchDetail(1)
    expect(node.id).toBe(1)
    // Second call should use cache
    const { nodeApi } = await import('@/api/node')
    const callsBefore = vi.mocked(nodeApi.getDetail).mock.calls.length
    await store.fetchDetail(1)
    expect(vi.mocked(nodeApi.getDetail).mock.calls.length).toBe(callsBefore)
  })

  it('fetchDetail force=true bypasses cache', async () => {
    await store.fetchDetail(1)
    const { nodeApi } = await import('@/api/node')
    const callsBefore = vi.mocked(nodeApi.getDetail).mock.calls.length
    await store.fetchDetail(1, true)
    expect(vi.mocked(nodeApi.getDetail).mock.calls.length).toBeGreaterThan(callsBefore)
  })

  it('does not restore detail cache when a pre-logout request completes late', async () => {
    const { nodeApi } = await import('@/api/node')
    let resolveDetail!: (value: Node) => void
    vi.mocked(nodeApi.getDetail).mockImplementationOnce(() => new Promise(resolve => { resolveDetail = resolve }))

    const pending = store.fetchDetail(1, true)
    store.clearCache()
    resolveDetail(mockNodes[0])
    await pending

    expect(store.getCachedDetail(1)).toBeUndefined()
  })

  it('does not let an older detail response replace a newer one', async () => {
    const { nodeApi } = await import('@/api/node')
    let resolveOlder!: (value: Node) => void
    let resolveNewer!: (value: Node) => void
    vi.mocked(nodeApi.getDetail)
      .mockImplementationOnce(() => new Promise(resolve => { resolveOlder = resolve }))
      .mockImplementationOnce(() => new Promise(resolve => { resolveNewer = resolve }))

    const older = store.fetchDetail(1, true)
    const newer = store.fetchDetail(1, true)
    resolveNewer({ ...mockNodes[0], name: 'Newer' })
    await newer
    resolveOlder({ ...mockNodes[0], name: 'Older' })
    await older

    expect(store.getCachedDetail(1)?.name).toBe('Newer')
  })

  // ── getCachedDetail ───────────────────────────────

  it('getCachedDetail returns undefined when not cached', () => {
    expect(store.getCachedDetail(1)).toBeUndefined()
  })

  it('getCachedDetail returns node after fetchDetail', async () => {
    await store.fetchDetail(1)
    const cached = store.getCachedDetail(1)
    expect(cached).toBeDefined()
    expect(cached?.id).toBe(1)
  })

  // ── updateNode ────────────────────────────────────

  it('updateNode updates local cache and list', async () => {
    await store.fetchNodes()
    await store.fetchDetail(1)
    await store.updateNode(1, { name: 'Updated-Name' })
    // Detail cache should reflect update
    expect(store.getCachedDetail(1)?.name).toBe('Updated-Name')
    // List should reflect update
    expect(store.nodes.find(n => n.id === 1)?.name).toBe('Updated-Name')
  })

  it('does not let a pre-update detail request overwrite the updated detail', async () => {
    const { nodeApi } = await import('@/api/node')
    await store.fetchDetail(1)
    let resolveOlder!: (value: Node) => void
    vi.mocked(nodeApi.getDetail).mockImplementationOnce(() => new Promise(resolve => { resolveOlder = resolve }))
    const older = store.fetchDetail(1, true)

    await store.updateNode(1, { name: 'Written' })
    resolveOlder({ ...mockNodes[0], name: 'Older' })
    await older

    expect(store.getCachedDetail(1)?.name).toBe('Written')
  })

  it('keeps numeric id and node_id detail aliases consistent across update and delete', async () => {
    await store.fetchDetail('AABBCC')
    expect(store.getCachedDetail(1)?.node_id).toBe('AABBCC')

    await store.updateNode(1, { name: 'Alias-updated' })
    expect(store.getCachedDetail('AABBCC')?.name).toBe('Alias-updated')

    await store.deleteNode(1)
    expect(store.getCachedDetail('AABBCC')).toBeUndefined()
    expect(store.getCachedDetail(1)).toBeUndefined()
  })

  it('does not let an older numeric-id response overwrite a newer node_id response', async () => {
    const { nodeApi } = await import('@/api/node')
    let resolveOlder!: (value: Node) => void
    let resolveNewer!: (value: Node) => void
    vi.mocked(nodeApi.getDetail)
      .mockImplementationOnce(() => new Promise(resolve => { resolveOlder = resolve }))
      .mockImplementationOnce(() => new Promise(resolve => { resolveNewer = resolve }))

    const older = store.fetchDetail(1, true)
    const newer = store.fetchDetail('AABBCC', true)
    resolveNewer({ ...mockNodes[0], name: 'New alias response' })
    await newer
    resolveOlder({ ...mockNodes[0], name: 'Old alias response' })
    await older

    expect(store.getCachedDetail(1)?.name).toBe('New alias response')
    expect(store.getCachedDetail('AABBCC')?.name).toBe('New alias response')
  })

  it('does not apply previous-session update or delete completions', async () => {
    const { nodeApi } = await import('@/api/node')
    await store.fetchNodes()
    let resolveUpdate!: () => void
    vi.mocked(nodeApi.update).mockImplementationOnce(() => new Promise<void>(resolve => { resolveUpdate = resolve }))
    const update = store.updateNode(1, { name: 'Old session' })
    store.clearCache()
    resolveUpdate()
    await expect(update).rejects.toThrow('会话已变更')
    expect(store.nodes).toEqual([])

    let resolveDelete!: () => void
    vi.mocked(nodeApi.delete).mockImplementationOnce(() => new Promise<void>(resolve => { resolveDelete = resolve }))
    const deletion = store.deleteNode(1)
    store.clearCache()
    resolveDelete()
    await expect(deletion).rejects.toThrow('会话已变更')
    expect(store.nodes).toEqual([])
    expect(store.loading).toBe(false)
  })

  // ── deleteNode ────────────────────────────────────

  it('deleteNode removes from list and cache', async () => {
    await store.fetchNodes()
    expect(store.nodes.length).toBe(2)
    await store.deleteNode(1)
    expect(store.nodes.length).toBe(1)
    expect(store.nodes.find(n => n.id === 1)).toBeUndefined()
    expect(store.total).toBe(1)
  })

  // ── clearCache ────────────────────────────────────

  it('clearCache resets all state', async () => {
    await store.fetchNodes()
    await store.fetchDetail(1)
    expect(store.nodes.length).toBe(2)
    store.clearCache()
    expect(store.nodes).toEqual([])
    expect(store.total).toBe(0)
    expect(store.getCachedDetail(1)).toBeUndefined()
  })
})
