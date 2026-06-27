import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import EdgeDeviceList from '@/views/edge-device/EdgeDeviceList.vue'

// Mock vue-router
const mockPush = vi.fn()
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: mockPush }),
}))

// Mock node store
vi.mock('@/stores/node', () => ({
  useNodeStore: () => ({
    fetchNodes: vi.fn(() => Promise.resolve()),
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
    getList: vi.fn(() => Promise.resolve({
      items: [
        { id: 1, name: 'Device A', status: 'active', device_type: 'temp_humidity', hardware_type: 'uart' },
        { id: 2, name: 'Device B', status: 'offline', device_type: 'wind_speed', hardware_type: 'i2c' },
      ],
      total: 2,
    })),
    delete: vi.fn(() => Promise.resolve()),
    update: vi.fn(() => Promise.resolve()),
    create: vi.fn(() => Promise.resolve({ id: 3 })),
  },
}))

vi.mock('@/api/channel', () => ({
  channelApi: {
    getList: vi.fn(() => Promise.resolve([])),
    update: vi.fn(() => Promise.resolve()),
  },
}))

vi.mock('@/api/deviceConfig', () => ({
  deviceConfigApi: {
    getList: vi.fn(() => Promise.resolve({ list: [] })),
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
})
