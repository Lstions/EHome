import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import Monitor from '../Monitor.vue'

// Mock API
vi.mock('@/api/monitor', () => ({
  getMetricsSummary: vi.fn(() =>
    Promise.resolve({
      code: 200,
      data: {
        http: { requests_total: 100, requests_in_flight: 2 },
        mqtt: { messages_received: 50, messages_sent: 30, connection_errors: 0 },
        device: { online: 3, offline: 1 },
        collector: { online: 2, offline: 0 },
        data: { points_collected: 5000, points_stored: 4990 },
        websocket: { connections_active: 4, messages_total: 200 },
        control: {
          operations_total: 20, active: 2, queued: 1, succeeded: 15, failed: 2,
          unknown: 1, unresolved_unknown: 1, cancelled: 0, outbox_pending: 1,
          outbox_leased: 0, capability_stale_nodes: 1, audit_write_failures: 0,
        },
      },
    })
  ),
}))

// Mock router
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn() }),
}))

// Stub Element Plus components
const stubs = {
  PageHeader: { template: '<div class="page-header"><slot /></div>' },
  'el-card': { template: '<div class="el-card"><slot /><slot name="header" /></div>' },
  'el-row': { template: '<div class="el-row"><slot /></div>' },
  'el-col': { template: '<div class="el-col"><slot /></div>' },
  'el-button': { template: '<button class="el-button" @click="$emit(\'click\')"><slot /></button>' },
  'el-select': { template: '<select class="el-select" @change="$emit(\'change\')"><slot /></select>' },
  'el-option': { template: '<option />' },
  'el-icon': { template: '<i class="el-icon"><slot /></i>' },
  'el-descriptions': { template: '<div class="el-descriptions"><slot /></div>' },
  'el-descriptions-item': { template: '<div class="el-desc-item"><slot /></div>' },
  'el-progress': { template: '<div class="el-progress" />' },
  'el-tag': { template: '<span class="el-tag"><slot /></span>' },
  'el-alert': { props: ['title'], template: '<div class="el-alert">{{ title }}</div>' },
}

describe('Monitor.vue', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('renders monitor container', () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    expect(wrapper.find('.monitor-container').exists()).toBe(true)
  })

  it('renders toolbar with title', () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    expect(wrapper.find('.toolbar').exists()).toBe(true)
    expect(wrapper.text()).toContain('系统监控')
  })

  it('uses text and an icon rather than an emoji-only heading', () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    expect(wrapper.find('.toolbar h2').text()).toBe('系统监控')
  })

  it('renders refresh button', () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    const btn = wrapper.find('button')
    expect(btn.exists()).toBe(true)
    expect(btn.text()).toContain('刷新')
  })

  it('renders stat cards section', () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    expect(wrapper.find('.stat-cards').exists()).toBe(true)
  })

  it('loads metrics on mount', async () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    await flushPromises()
    // After API resolves, stat values should be visible
    expect(wrapper.text()).toContain('100') // http.requests_total
    expect(wrapper.text()).toContain('3')   // device.online
  })

  it('displays HTTP requests total', async () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    await flushPromises()
    expect(wrapper.text()).toContain('HTTP')
    expect(wrapper.text()).toContain('请求总数')
  })

  it('displays device online/offline status', async () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    await flushPromises()
    expect(wrapper.text()).toContain('设备在线状态')
  })

  it('displays collector online/offline status', async () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    await flushPromises()
    expect(wrapper.text()).toContain('采集器在线状态')
  })

  it('renders detail panels section', () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    expect(wrapper.find('.detail-panels').exists()).toBe(true)
  })

  it('shows durable control health and attention counts', async () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    await flushPromises()
    expect(wrapper.text()).toContain('控制面健康')
    expect(wrapper.text()).toContain('未处置 UNKNOWN')
    expect(wrapper.text()).toContain('能力快照过期')
    expect(wrapper.find('.control-alert').text()).toContain('2 项需要关注')
  })

  it('renders HTTP monitoring panel', async () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    await flushPromises()
    expect(wrapper.text()).toContain('HTTP 监控')
  })

  it('renders MQTT monitoring panel', async () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    await flushPromises()
    expect(wrapper.text()).toContain('MQTT 监控')
  })

  it('renders WebSocket monitoring panel', async () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    await flushPromises()
    expect(wrapper.text()).toContain('WebSocket')
  })

  it('renders footer with last update time', async () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('.footer-info').exists()).toBe(true)
  })

  it('formats large numbers with K/M suffix', async () => {
    const wrapper = mount(Monitor, { global: { stubs } })
    await flushPromises()
    // 5000 points → "5.00K"
    expect(wrapper.text()).toContain('K')
  })

  it('restarts polling when the refresh interval changes', async () => {
    const setIntervalSpy = vi.spyOn(globalThis, 'setInterval')
    const wrapper = mount(Monitor, { global: { stubs } })
    const callsAfterMount = setIntervalSpy.mock.calls.length

    await wrapper.find('.el-select').trigger('change')

    expect(setIntervalSpy.mock.calls.length).toBeGreaterThan(callsAfterMount)
    setIntervalSpy.mockRestore()
  })
})
