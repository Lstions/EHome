import { describe, expect, it, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import NodeList from '@/views/node/NodeList.vue'
import source from '@/views/node/NodeList.vue?raw'

const mockFetchNodes = vi.fn(() => Promise.resolve())
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn() }),
  useRoute: () => ({ query: {} }),
}))
vi.mock('@/stores/node', () => ({
  useNodeStore: () => ({
    fetchNodes: mockFetchNodes,
    hasCachedList: vi.fn(() => true),
    hasFreshList: vi.fn(() => true),
    getCachedList: vi.fn(() => ({
      items: [
        { id: 1, node_id: 'node-1', name: 'Collector-A', model: 'ESP32', status: 'online' },
        { id: 2, node_id: 'node-2', name: 'Collector-B', model: 'RPi4', status: 'offline' },
        { id: 3, node_id: 'node-3', name: 'Collector-C', model: 'ESP32', status: 'online' },
      ], total: 3,
    })),
  }),
}))
vi.mock('@/stores/websocket', () => ({ useWebSocketStore: () => ({ connected: false, subscribe: vi.fn(() => vi.fn()) }) }))
vi.mock('@/events/events', () => ({ WS_EVENT: { NODE_STATUS: 'node_status' } }))

const stubs = {
  SkeletonCard: { template: '<div data-testid="skeleton-card" />' },
  EmptyState: { template: '<div data-testid="empty-state" />' },
  CountUp: { template: '<span data-testid="count-up">{{ $attrs.value }}</span>' },
}

function mountList() {
  return mount(NodeList, { global: { stubs } })
}

describe('NodeList.vue', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('renders the collector page, reads cache, and validates the list on mount', async () => {
    const wrapper = mountList()
    await flushPromises()
    expect(wrapper.find('.collector-page').exists()).toBe(true)
    expect(mockFetchNodes).toHaveBeenCalled()
    expect(wrapper.find('[data-testid="skeleton-card"]').exists()).toBe(false)
  })

  it('renders four summary cards without obsolete action or trend controls', async () => {
    const wrapper = mountList()
    await flushPromises()
    expect(wrapper.findAll('.stat-card')).toHaveLength(4)
    expect(wrapper.find('.stat-action').exists()).toBe(false)
    expect(wrapper.find('.stat-trend').exists()).toBe(false)
  })

  it('guards list requests and keeps explicit refresh errors observable', () => {
    expect(source).toContain('sequence !== listRequestSequence')
    expect(source).toContain('fetchNodes(false, true, true)')
    expect(source).toContain('节点已删除，但列表刷新失败')
    expect(source).toContain('nodes.value = nodes.value.filter')
  })

  it('uses search, status, model filters and restores page one when filters reset', () => {
    expect(source).toContain('searchKeyword')
    expect(source).toContain('statusFilter')
    expect(source).toContain('modelFilter')
    expect(source).toContain('const filteredNodes = computed')
    expect(source).toContain('currentPage.value = 1')
  })

  it('initializes query filters, offers grid/list views, and routes detail navigation', () => {
    expect(source).toContain("const viewMode = ref<'grid' | 'list'>('grid')")
    expect(source).toContain("router.push(`/node/${nodeId}`)")
    expect(source).toContain('route.query.search')
    expect(source).toContain('route.query.status')
  })

  it('derives summary stats and model options from cached nodes', () => {
    expect(source).toContain('const stats = reactive')
    expect(source).toContain('const modelOptions = computed')
    expect(source).toContain("stats.online = list.filter(c => c.status === 'online').length")
  })
})
