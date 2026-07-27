import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import ChannelList from '@/views/channel/ChannelList.vue'
import channelListSource from '@/views/channel/ChannelList.vue?raw'

const mockPush = vi.fn()
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: mockPush }),
}))

const { mockGetList, mockScan } = vi.hoisted(() => ({
  mockGetList: vi.fn(() =>
    Promise.resolve([
      { id: 1, node_id: 1, name: 'I2C0_0x77', hardware_type: 'i2c', hardware_id: 'I2C0', address: '0x77', enabled: true },
      { id: 2, node_id: 1, name: 'UART0', hardware_type: 'uart', hardware_id: 'UART0', enabled: true, bus_config: '1415000012C0' },
      { id: 3, node_id: 2, name: 'SPI0_CS0', hardware_type: 'spi', hardware_id: 'SPI0', enabled: false },
    ])
  ),
  mockScan: vi.fn(() => Promise.resolve({ channel_id: 1, devices: ['0x48', '0x49'] })),
}))

vi.mock('@/api/channel', () => ({
  channelApi: {
    getList: mockGetList,
    scan: mockScan,
  },
}))

vi.mock('@/stores/node', () => ({
  useNodeStore: () => ({
    fetchNodes: vi.fn(() => Promise.resolve()),
    getCachedList: vi.fn(() => ({ items: [
      { id: 1, name: 'Collector-A', status: 'online' },
      { id: 2, name: 'Collector-B', status: 'offline' },
    ], total: 2 })),
    nodes: [
      { id: 1, name: 'Collector-A', status: 'online' },
      { id: 2, name: 'Collector-B', status: 'offline' },
    ],
    total: 2,
    loading: false,
  }),
}))

const stubs = {
  PageHeader: { template: '<div data-testid="page-header"><slot /></div>' },
  SkeletonCard: { template: '<div data-testid="skeleton-card" />' },
  EmptyState: { template: '<div data-testid="empty-state" />' },
  'el-input': { template: '<input class="el-input" />' },
  'el-select': { template: '<div class="el-select"><slot /></div>' },
  'el-option': { template: '<div />' },
  'el-button': { template: '<button class="el-button" @click="$emit(\'click\')"><slot /></button>' },
  'el-card': { template: '<div class="el-card"><slot /></div>' },
  'el-icon': { template: '<i class="el-icon"><slot /></i>' },
  'el-tag': { template: '<span class="el-tag"><slot /></span>' },
  'el-table': { template: '<div class="el-table"><slot /></div>' },
  'el-table-column': { template: '<div />' },
  'el-pagination': { template: '<div class="el-pagination" />' },
  'el-switch': { template: '<div data-testid="el-switch" />' },
}

describe('ChannelList.vue', () => {
  it('reads node options from the requested parameter cache', () => {
    expect(channelListSource).toContain('nodeStore.getCachedList(nodeListParams)')
  })

  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    localStorage.clear()
    sessionStorage.clear()
  })

  it('renders the channel page container', async () => {
    const wrapper = mount(ChannelList, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('.channel-page').exists()).toBe(true)
  })

  it('loads channels on mount', async () => {
    mount(ChannelList, { global: { stubs } })
    await flushPromises()
    expect(mockGetList).toHaveBeenCalled()
  })

  it('stores fetched channels', async () => {
    const wrapper = mount(ChannelList, { global: { stubs } })
    await flushPromises()
    expect(wrapper.vm.channels.length).toBe(3)
  })

  it('filters channels by node filter', async () => {
    const wrapper = mount(ChannelList, { global: { stubs } })
    await flushPromises()
    wrapper.vm.nodeFilter = 1
    await wrapper.vm.$nextTick()
    expect(wrapper.vm.filteredChannels.length).toBe(2)
  })

  it('filters channels by hardware type', async () => {
    const wrapper = mount(ChannelList, { global: { stubs } })
    await flushPromises()
    wrapper.vm.hardwareTypeFilter = 'i2c'
    await wrapper.vm.$nextTick()
    expect(wrapper.vm.filteredChannels.length).toBe(1)
    expect(wrapper.vm.filteredChannels[0].hardware_type).toBe('i2c')
  })

  it('filters channels by search keyword', async () => {
    const wrapper = mount(ChannelList, { global: { stubs } })
    await flushPromises()
    wrapper.vm.searchKeyword = '0x77'
    await wrapper.vm.$nextTick()
    expect(wrapper.vm.filteredChannels.length).toBe(1)
  })

  it('resets page on filter/search changes', async () => {
    const wrapper = mount(ChannelList, { global: { stubs } })
    await flushPromises()
    wrapper.vm.currentPage = 3
    wrapper.vm.handleFilter()
    expect(wrapper.vm.currentPage).toBe(1)
    wrapper.vm.currentPage = 2
    wrapper.vm.handleSearch()
    expect(wrapper.vm.currentPage).toBe(1)
  })

  it('identifies scannable channels correctly', async () => {
    const wrapper = mount(ChannelList, { global: { stubs } })
    await flushPromises()
    expect(wrapper.vm.isScannable({ hardware_type: 'i2c' })).toBe(true)
    expect(wrapper.vm.isScannable({ hardware_type: 'uart', bus_config: '1415000012C0' })).toBe(true)
    expect(wrapper.vm.isScannable({ hardware_type: 'gpio' })).toBe(false)
  })

  it('navigates to node detail on goToNodeDetail', async () => {
    const wrapper = mount(ChannelList, { global: { stubs } })
    await flushPromises()
    wrapper.vm.goToNodeDetail({ node_id: 1 })
    expect(mockPush).toHaveBeenCalledWith({ name: 'NodeDetail', params: { id: 1 } })
  })

  it('paginates channels correctly', async () => {
    const wrapper = mount(ChannelList, { global: { stubs } })
    await flushPromises()
    wrapper.vm.currentPage = 1
    expect(wrapper.vm.paginatedChannels.length).toBe(3)
  })

  it('computes hardware tag type correctly', async () => {
    const wrapper = mount(ChannelList, { global: { stubs } })
    await flushPromises()
    expect(wrapper.vm.getHardwareTagType('uart')).toBe('')
    expect(wrapper.vm.getHardwareTagType('i2c')).toBe('success')
    expect(wrapper.vm.getHardwareTagType('spi')).toBe('warning')
    expect(wrapper.vm.getHardwareTagType('gpio')).toBe('info')
    expect(wrapper.vm.getHardwareTagType('unknown')).toBe('info')
  })

  it('computes bus type label correctly', async () => {
    const wrapper = mount(ChannelList, { global: { stubs } })
    await flushPromises()
    expect(wrapper.vm.getBusTypeLabel('uart')).toBe('串行')
    expect(wrapper.vm.getBusTypeLabel('i2c')).toBe('I²C')
    expect(wrapper.vm.getBusTypeLabel('spi')).toBe('SPI')
  })

  it('handles channel scan', async () => {
    const wrapper = mount(ChannelList, { global: { stubs } })
    await flushPromises()
    await wrapper.vm.handleScan({ id: 1, hardware_type: 'i2c' })
    expect(mockScan).toHaveBeenCalledWith(1, { scan_type: 'i2c' })
  })

  it('resolves nodeMap names', async () => {
    const wrapper = mount(ChannelList, { global: { stubs } })
    await flushPromises()
    expect(wrapper.vm.getNodeName(1)).toBe('Collector-A')
    expect(wrapper.vm.getNodeName(999)).toBe('节点 #999')
  })
})
