import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import NodeList from '@/views/node/NodeList.vue'
import nodeListSource from '@/views/node/NodeList.vue?raw'

// Mock vue-router
const { mockPush, mockRoute } = vi.hoisted(() => ({
  mockPush: vi.fn(),
  mockRoute: { query: {} as Record<string, string> },
}))
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: mockPush }),
  useRoute: () => mockRoute,
}))

// Mock node store
const mockFetchNodes = vi.fn(() => Promise.resolve())
vi.mock('@/stores/node', () => ({
  useNodeStore: () => ({
    fetchNodes: mockFetchNodes,
    hasCachedList: vi.fn(() => true),
    hasFreshList: vi.fn(() => true),
    getCachedList: vi.fn(() => ({
      items: [
        { id: 1, node_id: 'node-1', name: 'Collector-A', model: 'ESP32', status: 'online', connection_quality: 95, firmware_version: '1.2.0', last_online_time: new Date().toISOString(), latency_ms: 20 },
        { id: 2, node_id: 'node-2', name: 'Collector-B', model: 'RPi4', status: 'offline', connection_quality: 0, firmware_version: '1.1.0', last_online_time: '2025-01-01T00:00:00Z', latency_ms: 0 },
        { id: 3, node_id: 'node-3', name: 'Collector-C', model: 'ESP32', status: 'online', connection_quality: 70, firmware_version: '1.2.0', last_online_time: new Date().toISOString(), latency_ms: 50 },
      ],
      total: 3,
    })),
    deleteNode: vi.fn(() => Promise.resolve()),
    nodes: [
      { id: 1, node_id: 'node-1', name: 'Collector-A', model: 'ESP32', status: 'online', connection_quality: 95, firmware_version: '1.2.0', last_online_time: new Date().toISOString(), latency_ms: 20 },
      { id: 2, node_id: 'node-2', name: 'Collector-B', model: 'RPi4', status: 'offline', connection_quality: 0, firmware_version: '1.1.0', last_online_time: '2025-01-01T00:00:00Z', latency_ms: 0 },
      { id: 3, node_id: 'node-3', name: 'Collector-C', model: 'ESP32', status: 'online', connection_quality: 70, firmware_version: '1.2.0', last_online_time: new Date().toISOString(), latency_ms: 50 },
    ],
    total: 3,
    loading: false,
  }),
}))

// Mock websocket store
vi.mock('@/stores/websocket', () => ({
  useWebSocketStore: () => ({
    subscribe: vi.fn(() => vi.fn()),
    connected: false,
  }),
}))

// Mock WS_EVENT
vi.mock('@/events/events', () => ({
  WS_EVENT: { NODE_STATUS: 'node_status' },
}))

// Stub child components
const stubs = {
  SkeletonCard: { template: '<div data-testid="skeleton-card" />' },
  EmptyState: { template: '<div data-testid="empty-state" />' },
  CountUp: { template: '<span data-testid="count-up">{{ $attrs.value }}</span>' },
  'el-input': { template: '<input class="el-input" />' },
  'el-select': { template: '<div class="el-select"><slot /></div>' },
  'el-option': { template: '<div />' },
  'el-button': { template: '<button class="el-button" @click="$emit(\'click\')"><slot /></button>' },
  'el-button-group': { template: '<div class="el-button-group"><slot /></div>' },
  'el-card': { template: '<div class="el-card"><slot /></div>' },
  'el-icon': { template: '<i class="el-icon"><slot /></i>' },
  'el-tag': { template: '<span class="el-tag"><slot /></span>' },
  'el-progress': { template: '<div class="el-progress" />' },
  'el-table': { template: '<div class="el-table"><slot /></div>' },
  'el-table-column': { template: '<div />' },
  'el-pagination': { template: '<div class="el-pagination" />' },
}

describe('NodeList.vue', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    localStorage.clear()
    sessionStorage.clear()
  })

  it('ignores stale component-level list completions', () => {
    expect(nodeListSource).toContain('sequence !== listRequestSequence')
  })

  it('propagates explicit refresh errors and immediately removes deleted nodes', () => {
    expect(nodeListSource).toContain('fetchNodes(false, true, true)')
    expect(nodeListSource).toContain('catch {')
    expect(nodeListSource).toContain('nodes.value = nodes.value.filter')
    expect(nodeListSource).toContain('节点已删除，但列表刷新失败')
  })

  it('renders the collector page container', async () => {
    const wrapper = mount(NodeList, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('.collector-page').exists()).toBe(true)
  })

  it('calls fetchNodes on mount', async () => {
    mount(NodeList, { global: { stubs } })
    await flushPromises()
    expect(mockFetchNodes).toHaveBeenCalled()
  })

  it('keeps cached node content visible while validating the list cache', () => {
    const wrapper = mount(NodeList, { global: { stubs } })
    expect(wrapper.find('[data-testid="skeleton-card"]').exists()).toBe(false)
  })

  it('forces the store request for an explicit user refresh', async () => {
    const wrapper = mount(NodeList, { global: { stubs } })
    await flushPromises()
    mockFetchNodes.mockClear()

    ;(wrapper.vm as any).refreshData()
    await flushPromises()

    expect(mockFetchNodes).toHaveBeenCalledWith(
      { page: 1, page_size: 20 },
      true,
    )
  })

  it('renders stat cards after loading', async () => {
    const wrapper = mount(NodeList, { global: { stubs } })
    await flushPromises()
    const statCards = wrapper.findAll('.stat-card')
    expect(statCards.length).toBe(4) // total, online, offline, warning
  })

  it('computes stats from nodes correctly', async () => {
    const wrapper = mount(NodeList, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.stats.total).toBe(3)
    expect(vm.stats.online).toBe(2)
    expect(vm.stats.offline).toBe(1)
  })

  it('filters nodes by search keyword', async () => {
    const wrapper = mount(NodeList, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    vm.searchKeyword = 'ESP'
    await wrapper.vm.$nextTick()
    expect(vm.filteredNodes.length).toBe(2) // Two ESP32 nodes
  })

  it('filters nodes by status filter', async () => {
    const wrapper = mount(NodeList, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    vm.statusFilter = 'online'
    await wrapper.vm.$nextTick()
    expect(vm.filteredNodes.length).toBe(2)
  })

  it('initializes search and status filters from route query', async () => {
    mockRoute.query = { search: 'Collector-A', status: 'online' }
    const wrapper = mount(NodeList, { global: { stubs } })
    await flushPromises()

    const vm = wrapper.vm as any
    expect(vm.searchKeyword).toBe('Collector-A')
    expect(vm.statusFilter).toBe('online')
    expect(vm.filteredNodes).toHaveLength(1)
  })

  it('clears all filters and restores the first page', async () => {
    const wrapper = mount(NodeList, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    vm.searchKeyword = 'ESP'
    vm.statusFilter = 'online'
    vm.modelFilter = 'ESP32'
    vm.currentPage = 3

    vm.clearFilters()

    expect(vm.searchKeyword).toBe('')
    expect(vm.statusFilter).toBe('')
    expect(vm.modelFilter).toBe('')
    expect(vm.currentPage).toBe(1)
  })

  it('navigates to node detail on goToDetail', async () => {
    const wrapper = mount(NodeList, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    vm.goToDetail('node-1')
    expect(mockPush).toHaveBeenCalledWith('/node/node-1')
  })

  it('computes model options from nodes', async () => {
    const wrapper = mount(NodeList, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.modelOptions).toContain('ESP32')
    expect(vm.modelOptions).toContain('RPi4')
  })

  it('computes online rate correctly', async () => {
    const wrapper = mount(NodeList, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    // 2 online / 3 total = 66%
    expect(vm.stats.onlineRate).toBe(67)
  })

  it('toggles view mode between grid and list', async () => {
    const wrapper = mount(NodeList, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.viewMode).toBe('grid')
    vm.viewMode = 'list'
    await wrapper.vm.$nextTick()
    expect(vm.viewMode).toBe('list')
  })
})
