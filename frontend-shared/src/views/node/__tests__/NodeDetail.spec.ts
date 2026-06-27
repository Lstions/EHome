import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import NodeDetail from '../NodeDetail.vue'

// Mock vue-router
const mockPush = vi.fn()
const mockBack = vi.fn()
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: mockPush, back: mockBack }),
  useRoute: () => ({ params: { id: 'node-1' } }),
}))

// Use vi.hoisted for mock functions referenced inside vi.mock factories
const {
  mockGetDetail,
  mockSyncConfig,
  mockPing,
  mockGetOTAHistory,
  mockCancelOTA,
  mockGetList,
  mockGetChannelList,
} = vi.hoisted(() => ({
  mockGetDetail: vi.fn(() =>
    Promise.resolve({
      id: 1,
      node_id: 'node-1',
      name: 'Collector-A',
      model: 'ESP32',
      status: 'online',
      firmware_version: '1.2.0',
      connection_quality: 95,
      ping_latency_ms: 20,
      last_online_time: new Date().toISOString(),
      online_duration: 86400,
      config_sync_state: 'in_sync',
      config_epoch: 1,
      protocol_version: '2.0',
    })
  ),
  mockSyncConfig: vi.fn(() => Promise.resolve()),
  mockPing: vi.fn(() => Promise.resolve({ timestamp_us: '12345' })),
  mockGetOTAHistory: vi.fn(() => Promise.resolve([])),
  mockCancelOTA: vi.fn(() => Promise.resolve()),
  mockGetList: vi.fn(() => Promise.resolve({ total: 0, items: [] })),
  mockGetChannelList: vi.fn(() => Promise.resolve([])),
}))

vi.mock('@/api/node', () => ({
  nodeApi: {
    getDetail: mockGetDetail,
    syncConfig: mockSyncConfig,
    ping: mockPing,
    getOTAHistory: mockGetOTAHistory,
    cancelOTA: mockCancelOTA,
  },
}))

vi.mock('@/api/edgeDevice', () => ({
  edgeDeviceApi: {
    getList: mockGetList,
  },
}))

vi.mock('@/api/channel', () => ({
  channelApi: {
    getList: mockGetChannelList,
  },
}))

vi.mock('@/stores/websocket', () => ({
  useWebSocketStore: () => ({
    subscribe: vi.fn(() => vi.fn()),
    connected: false,
  }),
}))

vi.mock('@/stores/dma', () => ({
  useDmaStore: () => ({
    mergedChannels: [],
    loading: false,
    fetch: vi.fn(() => Promise.resolve()),
  }),
}))

vi.mock('@/utils/logger', () => ({
  logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}))

vi.mock('@/utils/dmaState', () => ({
  DmaState: { FREE: 0, ALLOCATED: 1, DISABLED: 2 },
  dmaStateText: (s: number) => ['空闲', '已分配', '已禁用'][s] || '未知',
  dmaStateClass: (s: number) => ['dma-state-free', 'dma-state-allocated', 'dma-state-disabled'][s] || '',
  dmaStateTagType: (s: number) => ['info', 'success', 'danger'][s] || 'info',
}))

vi.mock('@/events/events', () => ({
  WS_EVENT: { NODE_STATUS: 'node_status' },
}))

// Stub child components
const stubs = {
  PageHeader: { template: '<div data-testid="page-header"><slot /><slot name="extra" /></div>' },
  StatusBadge: { template: '<span data-testid="status-badge" />' },
  OTAForm: { template: '<div data-testid="ota-form" />' },
  ChannelPanel: { template: '<div data-testid="channel-panel" />' },
  'el-card': { template: '<div class="el-card"><slot /><slot name="header" /></div>' },
  'el-skeleton': { template: '<div class="el-skeleton" />' },
  'el-descriptions': { template: '<div class="el-descriptions"><slot /></div>' },
  'el-descriptions-item': { template: '<div class="el-descriptions-item"><slot /></div>' },
  'el-button': { template: '<button class="el-button" @click="$emit(\'click\')"><slot /></button>' },
  'el-tag': { template: '<span class="el-tag"><slot /></span>' },
  'el-icon': { template: '<i class="el-icon"><slot /></i>' },
  'el-progress': { template: '<div class="el-progress" />' },
  'el-empty': { template: '<div class="el-empty" />' },
  'el-table': { template: '<div class="el-table"><slot /></div>' },
  'el-table-column': { template: '<div />' },
}

describe('NodeDetail.vue', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    localStorage.clear()
    sessionStorage.clear()
  })

  it('renders the collector-detail container', async () => {
    const wrapper = mount(NodeDetail, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('.collector-detail').exists()).toBe(true)
  })

  it('renders PageHeader with title "节点详情"', async () => {
    const wrapper = mount(NodeDetail, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('[data-testid="page-header"]').exists()).toBe(true)
  })

  it('calls fetchCollectorDetail on mount', async () => {
    mount(NodeDetail, { global: { stubs } })
    await flushPromises()
    expect(mockGetDetail).toHaveBeenCalledWith('node-1')
  })

  it('displays collector info after loading', async () => {
    const wrapper = mount(NodeDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.collector).toBeTruthy()
    expect(vm.collector.name).toBe('Collector-A')
  })

  it('handles loading state correctly', async () => {
    mockGetDetail.mockReturnValueOnce(new Promise(() => {}))
    const wrapper = mount(NodeDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.loading).toBe(true)
  })

  it('handles fetch error gracefully', async () => {
    mockGetDetail.mockRejectedValueOnce(new Error('Network error'))
    const wrapper = mount(NodeDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.loading).toBe(false)
    expect(vm.collector).toBeNull()
  })

  it('computes syncStateLabel correctly', async () => {
    const wrapper = mount(NodeDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.syncStateLabel).toBe('已同步')
  })

  it('computes syncStateTagType correctly', async () => {
    const wrapper = mount(NodeDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.syncStateTagType).toBe('success')
  })

  it('computes collectorId from route params', async () => {
    const wrapper = mount(NodeDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.collectorId).toBe('node-1')
  })

  it('calls goBack on back navigation', async () => {
    const wrapper = mount(NodeDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    vm.goBack()
    expect(mockBack).toHaveBeenCalled()
  })

  it('formats time correctly for null values', async () => {
    const wrapper = mount(NodeDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.formatTime(null)).toBe('-')
    expect(vm.formatTime(undefined)).toBe('-')
  })

  it('formats online duration correctly', async () => {
    const wrapper = mount(NodeDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.formatOnlineDuration(86400)).toBe('1天')
    expect(vm.formatOnlineDuration(3600)).toBe('1小时')
    expect(vm.formatOnlineDuration(0)).toBe('-')
  })

  it('computes DMA channels from store', async () => {
    const wrapper = mount(NodeDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.dmaChannels).toEqual([])
    expect(vm.dmaLoading).toBe(false)
  })

  it('calls fetchOTAHistory on mount', async () => {
    mount(NodeDetail, { global: { stubs } })
    await flushPromises()
    expect(mockGetOTAHistory).toHaveBeenCalledWith('node-1')
  })

  it('getOTAStatusType returns correct tag types', async () => {
    const wrapper = mount(NodeDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.getOTAStatusType('success')).toBe('success')
    expect(vm.getOTAStatusType('failed')).toBe('danger')
    expect(vm.getOTAStatusType('pending')).toBe('info')
    expect(vm.getOTAStatusType('downloading')).toBe('warning')
  })

  it('getOTAStatusText returns correct Chinese text', async () => {
    const wrapper = mount(NodeDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.getOTAStatusText('success')).toBe('成功')
    expect(vm.getOTAStatusText('failed')).toBe('失败')
    expect(vm.getOTAStatusText('pending')).toBe('等待中')
  })

  it('getDeviceTypeText maps device types correctly', async () => {
    const wrapper = mount(NodeDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.getDeviceTypeText('wind_speed')).toBe('风速传感器')
    expect(vm.getDeviceTypeText('temp_humidity')).toBe('温湿度传感器')
    expect(vm.getDeviceTypeText('unknown_type')).toBe('unknown_type')
  })

  it('getQualityColor returns correct colors', async () => {
    const wrapper = mount(NodeDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.getQualityColor(95)).toBe('#67c23a')
    expect(vm.getQualityColor(70)).toBe('#e6a23c')
    expect(vm.getQualityColor(30)).toBe('#f56c6c')
  })

  it('getLatencyColor returns correct colors', async () => {
    const wrapper = mount(NodeDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.getLatencyColor(10)).toBe('#67c23a')
    expect(vm.getLatencyColor(100)).toBe('#e6a23c')
    expect(vm.getLatencyColor(300)).toBe('#f56c6c')
  })

  it('dmaTypeText returns correct type names', async () => {
    const wrapper = mount(NodeDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.dmaTypeText(0)).toBe('GDMA')
    expect(vm.dmaTypeText(1)).toBe('类型1')
  })

  it('capText decodes capability bits', async () => {
    const wrapper = mount(NodeDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.capText(1)).toBe('TX')
    expect(vm.capText(3)).toBe('TX, RX')
    expect(vm.capText(7)).toBe('TX, RX, Burst')
    expect(vm.capText(0)).toBe('无')
  })

  it('busText decodes bus compatibility bits', async () => {
    const wrapper = mount(NodeDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.busText(1)).toBe('UART')
    expect(vm.busText(2)).toBe('I2C')
    expect(vm.busText(7)).toBe('UART, I2C, SPI')
    expect(vm.busText(0)).toBe('无')
  })
})
