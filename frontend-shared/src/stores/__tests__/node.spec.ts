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

  it('fetchNodes uses cache when fresh', async () => {
    await store.fetchNodes() // first fetch
    const { nodeApi } = await import('@/api/node')
    const callCountBefore = vi.mocked(nodeApi.getList).mock.calls.length
    await store.fetchNodes() // should use cache, no API call
    expect(vi.mocked(nodeApi.getList).mock.calls.length).toBe(callCountBefore)
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
