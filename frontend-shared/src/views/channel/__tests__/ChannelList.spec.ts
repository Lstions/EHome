import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import ChannelList from '@/views/channel/ChannelList.vue'
import channelListSource from '@/views/channel/ChannelList.vue?raw'

vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn() }),
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

// 仅替换项目内展示组件；Element Plus 交互组件使用全局 test-setup stub。
const stubs = {
  PageHeader: { template: '<div data-testid="page-header"><slot /><slot name="extra" /></div>' },
  SkeletonCard: { template: '<div data-testid="skeleton-card" />' },
  EmptyState: { template: '<div data-testid="empty-state" />' },
  RouterLink: { template: '<a><slot /></a>' },
}

function mountList() {
  return mount(ChannelList, { global: { stubs } })
}

describe('ChannelList.vue', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    localStorage.clear()
    sessionStorage.clear()
  })

  it('reads node options from the requested parameter cache', () => {
    expect(channelListSource).toContain('nodeStore.getCachedList(nodeListParams)')
  })

  it('renders the channel page and loads channels on mount', async () => {
    const wrapper = mountList()
    await flushPromises()
    expect(wrapper.find('.channel-page').exists()).toBe(true)
    expect(mockGetList).toHaveBeenCalled()
  })

  it('renders all fetched channels in the table data text', async () => {
    const wrapper = mountList()
    await flushPromises()
    const text = wrapper.text()
    expect(text).toContain('I2C0_0x77')
    expect(text).toContain('UART0')
    expect(text).toContain('SPI0_CS0')
  })

  it('filters channels through the node selector', async () => {
    const wrapper = mountList()
    await flushPromises()
    const selects = wrapper.findAll('select.el-select')
    await selects[0].setValue('1')
    await flushPromises()

    const text = wrapper.text()
    expect(text).toContain('I2C0_0x77')
    expect(text).toContain('UART0')
    expect(text).not.toContain('SPI0_CS0')
  })

  it('declares node, hardware type, and keyword filtering behavior', () => {
    expect(channelListSource).toContain('ch.node_id === nodeFilter.value')
    expect(channelListSource).toContain('ch.hardware_type === hardwareTypeFilter.value')
    expect(channelListSource).toContain('name.includes(keyword) || hwId.includes(keyword) || nodeName.includes(keyword)')
    expect(channelListSource).toContain('currentPage.value = 1')
  })

  it('offers scan only for I2C and potentially RS485 UART channels', () => {
    expect(channelListSource).toContain("if (row.hardware_type === 'i2c') return true")
    expect(channelListSource).toContain("if (row.hardware_type === 'uart')")
    expect(channelListSource).toContain("return !!row.bus_config")
  })

  it('routes I2C scans through the channel API with i2c scan type', () => {
    expect(channelListSource).toContain("const scanType = row.hardware_type === 'i2c' ? 'i2c' : 'modbus'")
    expect(channelListSource).toContain('channelApi.scan(row.id, { scan_type: scanType })')
  })

  it('routes visible node-detail actions through the NodeDetail route', () => {
    expect(channelListSource).toContain("router.push({ name: 'NodeDetail', params: { id: row.node_id } })")
  })

  it('maps node names and hardware labels in the component contract', () => {
    expect(channelListSource).toContain('return node?.name || `节点 #${nodeId}`')
    expect(channelListSource).toContain("uart: '串行'")
    expect(channelListSource).toContain("i2c: 'I²C'")
    expect(channelListSource).toContain("spi: 'SPI'")
  })

  it('contains mobile table scroll affordance for the wide channel table', () => {
    expect(channelListSource).toContain('mobile-table-wrapper')
    expect(channelListSource).toContain('← 左右滑动查看完整表格 →')
  })
})
