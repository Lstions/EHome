import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import ChannelList from '@/views/channel/ChannelList.vue'
import channelListSource from '@/views/channel/ChannelList.vue?raw'
// EmptyState 使用真实实现：它的 props 契约已支持 kind="error"，
// 失败态用例必须验证真实契约被接线，而不是被 stub 吞掉。
import RealEmptyState from '@/components/common/EmptyState.vue'

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

// ============================================================================
// 失败态范式（U-1 阻断项）：GET /api/v1/channels 失败时，页面必须呈现
// 「常驻错误态 + 重试入口」，不得把接口失败伪装成「暂无通道 / 0 条」。
//
// 这些用例是真实 mount + DOM 断言（不是源码字符串断言）：
//   - EmptyState 用真实实现（它的 kind="error" 契约此前未被本页接线）
//   - El* 沿用 test-setup.ts 的全局 stub（ElAlert 渲染 title slot，
//     ElButton 真实 emit click，el-table 渲染 <tr>）
// 因此断言针对的是页面真实结构，重试路径也是真实点击。
// ============================================================================

const failureStubs = {
  PageHeader: { template: '<div data-testid="page-header"><slot /><slot name="extra" /></div>' },
  SkeletonCard: { template: '<div data-testid="skeleton-card" />' },
  RouterLink: { template: '<a><slot /></a>' },
}

function mountWithRealEmptyState() {
  return mount(ChannelList, { global: { stubs: { ...failureStubs, EmptyState: RealEmptyState } } })
}

describe('ChannelList.vue — 接口失败态', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    localStorage.clear()
    sessionStorage.clear()
  })

  it('接口 500 时渲染常驻错误态 + 重试入口，且不出现「暂无通道」式空态结论', async () => {
    mockGetList.mockRejectedValueOnce(new Error('Request failed with status code 500'))

    const wrapper = mountWithRealEmptyState()
    await flushPromises()

    // 1) 常驻错误提示条 + 可点击的重试入口
    const alert = wrapper.find('[data-test="channel-error"]')
    expect(alert.exists()).toBe(true)
    expect(alert.text()).toContain('加载通道列表失败')
    expect(alert.text()).toContain('500')
    const retry = wrapper.find('[data-test="channel-retry"]')
    expect(retry.exists()).toBe(true)
    expect(retry.text()).toBe('重试')

    // 2) 错误空态走真实 kind="error" 契约，且优先级高于「暂无通道」
    const empties = wrapper.findAllComponents(RealEmptyState)
    expect(empties.map(e => e.props('kind'))).toContain('error')
    expect(empties.map(e => e.props('title'))).toContain('通道列表加载失败')

    // 3) 失败不得被伪装成「确实没有通道」
    expect(wrapper.text()).not.toContain('暂无通道')
    expect(wrapper.text()).not.toContain('还没有配置任何通道')
    // 失败时不得渲染出任何数据行（空表格 = 伪造的「0 条」领域事实）
    expect(wrapper.findAll('tbody tr')).toHaveLength(0)
    expect(wrapper.find('.channel-table').exists()).toBe(false)
  })

  it('重试入口真实可用：第二次成功后错误态消失并渲染接口返回的真实数据', async () => {
    mockGetList.mockRejectedValueOnce(new Error('boom'))

    const wrapper = mountWithRealEmptyState()
    await flushPromises()
    expect(wrapper.find('[data-test="channel-error"]').exists()).toBe(true)

    mockGetList.mockResolvedValueOnce([
      { id: 9, node_id: 1, name: 'RETRY-OK', hardware_type: 'i2c', hardware_id: 'I2C9', address: '0x09', enabled: true },
    ])
    await wrapper.find('[data-test="channel-retry"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-test="channel-error"]').exists()).toBe(false)
    expect(wrapper.findAllComponents(RealEmptyState).map(e => e.props('kind'))).not.toContain('error')
    expect(wrapper.text()).toContain('RETRY-OK')
    expect(mockGetList).toHaveBeenCalledTimes(2)
  })

  it('加载成功但确实为空时是空态（initial），不是错误态', async () => {
    mockGetList.mockResolvedValueOnce([])

    const wrapper = mountWithRealEmptyState()
    await flushPromises()

    expect(wrapper.find('[data-test="channel-error"]').exists()).toBe(false)
    const empties = wrapper.findAllComponents(RealEmptyState)
    expect(empties).toHaveLength(1)
    expect(empties[0].props('kind')).toBe('initial')
    expect(empties[0].props('title')).toBe('暂无通道')
  })

  it('筛选后无匹配结果与「确实为空」区分：kind=filtered', async () => {
    const wrapper = mountWithRealEmptyState()
    await flushPromises()
    expect(wrapper.text()).toContain('I2C0_0x77')

    // 硬件类型筛选（第 2 个 select）切到 adc —— 默认 3 条数据里没有 adc 通道
    const selects = wrapper.findAll('select.el-select')
    await selects[1].setValue('adc')
    await flushPromises()

    const empties = wrapper.findAllComponents(RealEmptyState)
    expect(empties).toHaveLength(1)
    expect(empties[0].props('kind')).toBe('filtered')
    expect(empties[0].props('title')).toBe('没有匹配的通道')
    expect(wrapper.find('[data-test="channel-error"]').exists()).toBe(false)
  })
})
