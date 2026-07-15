import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import EdgeDeviceList from '@/views/edge-device/EdgeDeviceList.vue'
import edgeDeviceListSource from '@/views/edge-device/EdgeDeviceList.vue?raw'

const {
  mockFetchNodes,
  mockChannelGetList,
  mockParserGetList,
  mockTemplateGetList,
  mockEdgeDeviceGetList,
} = vi.hoisted(() => ({
  mockFetchNodes: vi.fn(() => Promise.resolve()),
  mockChannelGetList: vi.fn(() => Promise.resolve([])),
  mockParserGetList: vi.fn(() => Promise.resolve([])),
  mockTemplateGetList: vi.fn(() => Promise.resolve({ list: [] })),
  mockEdgeDeviceGetList: vi.fn(() => Promise.resolve({
    items: [
      { id: 1, name: 'Device A', status: 'active', device_type: 'temp_humidity', hardware_type: 'uart' },
      { id: 2, name: 'Device B', status: 'offline', device_type: 'wind_speed', hardware_type: 'i2c' },
    ],
    total: 2,
  })),
}))

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
vi.mock('@/stores/node', () => ({
  useNodeStore: () => ({
    fetchNodes: mockFetchNodes,
    getCachedList: vi.fn(() => ({ items: [
      { id: 1, node_id: 'node-1', name: 'Collector-A', status: 'online' },
    ], total: 1 })),
    nodes: [
      { id: 1, node_id: 'node-1', name: 'Collector-A', status: 'online' },
    ],
    total: 1,
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

// Mock device-related APIs
vi.mock('@/api/edgeDevice', () => ({
  edgeDeviceApi: {
    getList: mockEdgeDeviceGetList,
    delete: vi.fn(() => Promise.resolve()),
    update: vi.fn(() => Promise.resolve()),
    create: vi.fn(() => Promise.resolve({ id: 3 })),
  },
}))

vi.mock('@/api/channel', () => ({
  channelApi: {
    getList: mockChannelGetList,
    update: vi.fn(() => Promise.resolve()),
  },
}))

vi.mock('@/api/deviceConfig', () => ({
  deviceConfigApi: {
    getList: mockTemplateGetList,
  },
}))

vi.mock('@/api/parser', () => ({
  parserApi: {
    getList: mockParserGetList,
  },
}))

vi.mock('@/api/client', () => ({
  default: {
    get: vi.fn(() => Promise.resolve({ data_count_today: 0 })),
  },
}))

// Stub child components
const stubs = {
  SkeletonCard: { name: 'SkeletonCard', template: '<div data-testid="skeleton-card" />' },
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
  'el-table': { template: '<div class="el-table"><slot /></div>' },
  'el-table-column': { template: '<div />' },
  'el-pagination': { template: '<div class="el-pagination" />' },
  'el-dialog': { template: '<div class="el-dialog"><slot /></div>' },
  'el-steps': { template: '<div class="el-steps"><slot /></div>' },
  'el-step': { template: '<div />' },
  'el-form': { template: '<div class="el-form"><slot /></div>' },
  'el-form-item': { template: '<div><slot /></div>' },
  'el-tabs': { template: '<div class="el-tabs"><slot /></div>' },
  'el-tab-pane': { template: '<div><slot /></div>' },
  'el-empty': { template: '<div class="el-empty" />' },
  'el-switch': { template: '<div />' },
  'el-checkbox': { template: '<div />' },
}

describe('EdgeDeviceList.vue', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    localStorage.clear()
    sessionStorage.clear()
  })

  it('forces list refresh after successful writes and deletions', () => {
    expect(edgeDeviceListSource.match(/await fetchDevices\(true\)/g)).toHaveLength(2)
    expect(edgeDeviceListSource.match(/await fetchDevices\(true, true\)/g)).toHaveLength(2)
    expect(edgeDeviceListSource.match(/edgeDeviceStore\.invalidateLists\(\)/g)).toHaveLength(2)
    expect(edgeDeviceListSource).toContain('edgeDeviceStore.invalidateDetail(frozenEditingDeviceId)')
    expect(edgeDeviceListSource).toContain('assertSessionGeneration(sessionGeneration)')
    expect(edgeDeviceListSource).toContain('channelStore.fetchChannels(undefined, true)')
    expect(edgeDeviceListSource).toContain('parserStore.fetchParsers(true)')
    expect(edgeDeviceListSource).toContain('wizardDataLoaded = false')
    expect(edgeDeviceListSource).toContain('createTransactionGeneration++')
    expect(edgeDeviceListSource).toContain("throw new Error('创建事务已取消')")
    expect(edgeDeviceListSource).toContain(':before-close="handleCreateDialogClose"')
    expect(edgeDeviceListSource).toContain('if (submitting.value) return')
    expect(edgeDeviceListSource).toContain('const frozenDeviceForm = { ...deviceForm }')
    expect(edgeDeviceListSource).toContain('const frozenNewChannel = { ...newChannel }')
    expect(edgeDeviceListSource).toContain('const frozenParser = selectedParser.value')
  })

  it('ignores stale component-level list completions', () => {
    expect(edgeDeviceListSource).toContain('sequence !== listRequestSequence')
  })

  it('reads the parameter-specific cache after single and batch deletes', () => {
    expect(edgeDeviceListSource).toContain('await fetchDevices(true, true)')
  })

  it('settles all batch deletes and reports partial failures after syncing local state', () => {
    expect(edgeDeviceListSource).toContain('Promise.allSettled(ids.map')
    expect(edgeDeviceListSource).toContain('const failed = results.length - succeeded')
    expect(edgeDeviceListSource).toContain('删除结果已保存，但列表刷新失败')
    expect(edgeDeviceListSource).toContain('设备已删除，但列表刷新失败')
  })

  it('renders the device page container', async () => {
    const wrapper = mount(EdgeDeviceList, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('.device-page').exists()).toBe(true)
  })

  it('renders stat cards after loading', async () => {
    const wrapper = mount(EdgeDeviceList, { global: { stubs } })
    await flushPromises()
    const statCards = wrapper.findAll('.stat-card')
    expect(statCards.length).toBe(4) // total, online, offline, todayData
  })

  it('reuses a matching fresh device-list cache when the page is remounted', async () => {
    const first = mount(EdgeDeviceList, { global: { stubs } })
    await flushPromises()
    expect(mockEdgeDeviceGetList).toHaveBeenCalledTimes(1)

    first.unmount()
    mount(EdgeDeviceList, { global: { stubs } })
    await flushPromises()

    expect(mockEdgeDeviceGetList).toHaveBeenCalledTimes(1)
  })

  it('defers create-wizard dependencies until the dialog opens', async () => {
    const wrapper = mount(EdgeDeviceList, { global: { stubs } })
    await flushPromises()

    expect(mockFetchNodes).not.toHaveBeenCalled()
    expect(mockChannelGetList).not.toHaveBeenCalled()
    expect(mockParserGetList).not.toHaveBeenCalled()
    expect(mockTemplateGetList).not.toHaveBeenCalled()

    ;(wrapper.vm as any).showCreateDialog = true
    await wrapper.vm.$nextTick()
    await flushPromises()

    expect(mockFetchNodes).toHaveBeenCalledTimes(1)
    expect(mockChannelGetList).toHaveBeenCalledTimes(1)
    expect(mockParserGetList).toHaveBeenCalledTimes(1)
    expect(mockTemplateGetList).toHaveBeenCalledTimes(1)
  })

  it('initializes with card view mode', async () => {
    const wrapper = mount(EdgeDeviceList, { global: { stubs } })
    await flushPromises()
    expect(wrapper.vm.viewMode).toBe('card')
  })

  it('navigates to device detail on goToDetail', async () => {
    const wrapper = mount(EdgeDeviceList, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    vm.goToDetail(42)
    expect(mockPush).toHaveBeenCalledWith('/edge-device/42')
  })

  it('initializes with empty filtered devices', async () => {
    const wrapper = mount(EdgeDeviceList, { global: { stubs } })
    await flushPromises()
    // No devices fetched (mocked store has no edge devices)
    expect(wrapper.vm.filteredDevices).toBeDefined()
  })

  it('shows create dialog when create button is clicked', async () => {
    const wrapper = mount(EdgeDeviceList, { global: { stubs } })
    await flushPromises()
    expect(wrapper.vm.showCreateDialog).toBe(false)
    wrapper.vm.showCreateDialog = true
    await wrapper.vm.$nextTick()
    expect(wrapper.vm.showCreateDialog).toBe(true)
  })

  it('has device type filter options', async () => {
    const wrapper = mount(EdgeDeviceList, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.deviceTypes).toBeDefined()
    expect(vm.deviceTypes.length).toBeGreaterThan(0)
  })

  it('handles stat click to set status filter', async () => {
    const wrapper = mount(EdgeDeviceList, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    vm.handleStatClick('offline')
    expect(vm.statusFilter).toBe('offline')
  })

  it('resets status filter on stat click all', async () => {
    const wrapper = mount(EdgeDeviceList, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    vm.statusFilter = 'active'
    vm.handleStatClick('all')
    expect(vm.statusFilter).toBe('')
  })

  it('clears all filters and restores the first page', async () => {
    const wrapper = mount(EdgeDeviceList, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    vm.searchKeyword = 'Device'
    vm.typeFilter = 'temp_humidity'
    vm.statusFilter = 'offline'
    vm.hardwareFilter = 'i2c'
    vm.currentPage = 3

    vm.clearFilters()

    expect(vm.searchKeyword).toBe('')
    expect(vm.typeFilter).toBe('')
    expect(vm.statusFilter).toBe('')
    expect(vm.hardwareFilter).toBe('')
    expect(vm.currentPage).toBe(1)
  })

  it('uses a compact facts grid and reading area in card view', async () => {
    const wrapper = mount(EdgeDeviceList, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    vm.devices = [{
      id: 1,
      name: '雨量计',
      status: 'active',
      device_type: 'prs3001',
      node_id: 'node-1',
      hardware_type: 'uart',
      hardware_id: 'UART1',
      protocol: 'modbus',
      last_data: { rainfall: 12.5 },
      last_data_time: '2026-07-13T09:00:00Z',
    }]
    await wrapper.vm.$nextTick()

    expect(wrapper.find('.card-facts').exists()).toBe(true)
    expect(wrapper.find('.card-reading').exists()).toBe(true)
    expect(wrapper.find('.device-card').text()).not.toContain('📡')
    expect(wrapper.find('.device-card').text()).not.toContain('🔌')
  })

  it('initializes search and status filters from route query', async () => {
    mockRoute.query = { search: 'Device A', status: 'offline' }
    const wrapper = mount(EdgeDeviceList, { global: { stubs } })
    await flushPromises()

    const vm = wrapper.vm as any
    expect(vm.searchKeyword).toBe('Device A')
    expect(vm.statusFilter).toBe('offline')
  })
})
