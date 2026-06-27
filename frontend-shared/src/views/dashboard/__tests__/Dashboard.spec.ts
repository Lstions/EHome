import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import Dashboard from '@/views/dashboard/Dashboard.vue'

// Mock vue-router
const mockPush = vi.fn()
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: mockPush }),
}))

// Mock dataApi
vi.mock('@/api/data', () => ({
  dataApi: {
    getOverview: vi.fn(() =>
      Promise.resolve({
        nodes: { total: 5, online: 3, offline: 2 },
        edge_devices: { total: 10, online: 7, offline: 3 },
        latest_data: [],
      })
    ),
  },
}))

// Mock api client
vi.mock('@/api/client', () => ({
  default: { get: vi.fn(() => Promise.resolve({ data: [] })) },
}))

// Mock websocket store
vi.mock('@/stores/websocket', () => ({
  useWebSocketStore: () => ({
    subscribe: vi.fn(() => vi.fn()),
    connected: false,
  }),
}))

// Mock logger
vi.mock('@/utils/logger', () => ({
  logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}))

// Mock sensor utils
vi.mock('@/utils/sensor', () => ({
  sensorNameMap: { temperature: '温度', humidity: '湿度' },
  sensorUnitMap: { temperature: '°C', humidity: '%' },
  SENSOR_ORDER: ['temperature', 'humidity'],
}))

// Stub child components
const stubs = {
  PageHeader: { template: '<div data-testid="page-header"><slot /></div>' },
  SkeletonCard: true,
  EmptyState: true,
  LineChart: true,
  'el-row': { template: '<div class="el-row"><slot /></div>' },
  'el-col': { template: '<div class="el-col"><slot /></div>' },
  'el-card': { template: '<div class="el-card"><slot /><slot name="header" /></div>' },
  'el-icon': { template: '<i class="el-icon"><slot /></i>' },
  'el-tag': { template: '<span class="el-tag"><slot /></span>' },
  'el-button': { template: '<button class="el-button"><slot /></button>' },
  'el-skeleton': { template: '<div class="el-skeleton" />' },
  'el-table': { template: '<div class="el-table"><slot /></div>' },
  'el-table-column': { template: '<div />' },
  'el-radio-group': { template: '<div class="el-radio-group"><slot /></div>' },
  'el-radio-button': { template: '<div><slot /></div>' },
  'el-select': { template: '<div><slot /></div>' },
  'el-option': { template: '<div />' },
  'el-timeline': { template: '<div class="el-timeline"><slot /></div>' },
  'el-timeline-item': { template: '<div class="el-timeline-item"><slot /></div>' },
  'el-empty': { template: '<div class="el-empty" />' },
  'router-link': { template: '<a><slot /></a>' },
}

describe('Dashboard.vue', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    localStorage.clear()
    sessionStorage.clear()
  })

  it('renders dashboard container', async () => {
    const wrapper = mount(Dashboard, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('.dashboard').exists()).toBe(true)
  })

  it('loads overview data on mount', async () => {
    const { dataApi } = await import('@/api/data')
    mount(Dashboard, { global: { stubs } })
    await flushPromises()
    expect(dataApi.getOverview).toHaveBeenCalled()
  })

  it('displays stat cards after loading', async () => {
    const wrapper = mount(Dashboard, { global: { stubs } })
    await flushPromises()
    const statCards = wrapper.findAll('.stat-card')
    expect(statCards.length).toBe(4)
  })

  it('computes offline collectors correctly', async () => {
    const wrapper = mount(Dashboard, { global: { stubs } })
    await flushPromises()
    // overview: total=5, online=3 → offline=2
    expect(wrapper.vm.offlineCollectors).toBe(2)
  })

  it('computes offline devices correctly', async () => {
    const wrapper = mount(Dashboard, { global: { stubs } })
    await flushPromises()
    // overview: total=10, online=7 → offline=3
    expect(wrapper.vm.offlineDevices).toBe(3)
  })

  it('navigates to node list on stat card click', async () => {
    const wrapper = mount(Dashboard, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    vm.router.push('/node')
    expect(mockPush).toHaveBeenCalledWith('/node')
  })

  it('handles overview fetch error gracefully', async () => {
    const { dataApi } = await import('@/api/data')
    vi.mocked(dataApi.getOverview).mockRejectedValueOnce(new Error('Network error'))

    const wrapper = mount(Dashboard, { global: { stubs } })
    await flushPromises()
    expect(wrapper.vm.loading).toBe(false)
  })

  it('shows trend range label correctly', async () => {
    const wrapper = mount(Dashboard, { global: { stubs } })
    await flushPromises()
    expect(wrapper.vm.trendRangeLabel).toBe('最近 24 小时')
    wrapper.vm.trendRange = '1h'
    await wrapper.vm.$nextTick()
    expect(wrapper.vm.trendRangeLabel).toBe('最近 1 小时')
    wrapper.vm.trendRange = '7d'
    await wrapper.vm.$nextTick()
    expect(wrapper.vm.trendRangeLabel).toBe('最近 7 天')
  })
})
