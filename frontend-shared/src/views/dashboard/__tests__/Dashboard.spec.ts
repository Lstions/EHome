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

  it('uses the responsive KPI grid instead of fixed 24-column spans', async () => {
    const wrapper = mount(Dashboard, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('.dashboard-stats').exists()).toBe(true)
  })

  it('makes dashboard KPI navigation cards keyboard-operable', async () => {
    const wrapper = mount(Dashboard, { global: { stubs } })
    await flushPromises()
    const firstCard = wrapper.findAll('.stat-card')[0]

    expect(firstCard.attributes('role')).toBe('link')
    expect(firstCard.attributes('tabindex')).toBe('0')
    expect(firstCard.attributes('aria-label')).toBe('查看采集器总数')

    await firstCard.trigger('keydown.enter')
    expect(mockPush).toHaveBeenCalledWith('/node')

    await firstCard.trigger('keydown.space')
    expect(mockPush).toHaveBeenLastCalledWith('/node')
  })

  it('does not render a simulated status timeline as operational data', async () => {
    const wrapper = mount(Dashboard, { global: { stubs } })
    await flushPromises()
    expect(wrapper.text()).not.toContain('模拟数据')
  })

  it('loads real status history from the API', async () => {
    const client = (await import('@/api/client')).default
    mount(Dashboard, { global: { stubs } })
    await flushPromises()
    expect(client.get).toHaveBeenCalledWith('/api/v1/nodes/status-history', { params: { limit: 20 } })
  })

  it('computes offline collectors correctly', async () => {
    const wrapper = mount(Dashboard, { global: { stubs } })
    await flushPromises()
    // overview: total=5, online=3 → offline=2；通过告警卡的渲染值验证
    expect(wrapper.find('.alert-item .alert-value').text()).toBe('2')
  })

  it('computes offline devices correctly', async () => {
    const wrapper = mount(Dashboard, { global: { stubs } })
    await flushPromises()
    // 第二个告警值：total=10, online=7 → offline=3
    const values = wrapper.findAll('.alert-item .alert-value')
    expect(values[1].text()).toBe('3')
  })

  it('navigates to node list on stat card click', async () => {
    const wrapper = mount(Dashboard, { global: { stubs } })
    await flushPromises()
    await wrapper.findAll('.stat-card')[0].trigger('click')
    expect(mockPush).toHaveBeenCalledWith('/node')
  })

  it('handles overview fetch error gracefully', async () => {
    const { dataApi } = await import('@/api/data')
    vi.mocked(dataApi.getOverview).mockRejectedValueOnce(new Error('Network error'))

    const wrapper = mount(Dashboard, { global: { stubs } })
    await flushPromises()
    // 出错后不显示 loading skeleton，页面仍保留 dashboard 容器
    expect(wrapper.find('.dashboard').exists()).toBe(true)
    expect(wrapper.find('.el-skeleton').exists()).toBe(false)
  })

  it('shows the default trend range label', async () => {
    const wrapper = mount(Dashboard, { global: { stubs } })
    await flushPromises()
    expect(wrapper.text()).toContain('最近 24 小时趋势')
  })
})
