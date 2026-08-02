import { describe, expect, it, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import EdgeDeviceList from '@/views/edge-device/EdgeDeviceList.vue'
import source from '@/views/edge-device/EdgeDeviceList.vue?raw'

const { mockEdgeDeviceGetList } = vi.hoisted(() => ({
  mockEdgeDeviceGetList: vi.fn(() => Promise.resolve({
    items: [
      { id: 1, name: 'Device A', status: 'active', device_type: 'temp_humidity', hardware_type: 'uart' },
      { id: 2, name: 'Device B', status: 'offline', device_type: 'wind_speed', hardware_type: 'i2c' },
    ],
    total: 2,
  })),
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn() }),
  useRoute: () => ({ query: {} }),
}))
vi.mock('@/stores/node', () => ({
  useNodeStore: () => ({
    fetchNodes: vi.fn(() => Promise.resolve()),
    getCachedList: vi.fn(() => ({ items: [{ id: 1, node_id: 'node-1', name: 'Collector-A', status: 'online' }], total: 1 })),
  }),
}))
vi.mock('@/stores/websocket', () => ({
  useWebSocketStore: () => ({ connected: false, connect: vi.fn(), subscribe: vi.fn(() => vi.fn()) }),
}))
vi.mock('@/api/edgeDevice', () => ({
  compactEdgeDeviceList: (items: unknown) => Array.isArray(items) ? items.filter(item => item && typeof item === 'object' && 'id' in item) : [],
  edgeDeviceApi: { getList: mockEdgeDeviceGetList, create: vi.fn(), update: vi.fn(), delete: vi.fn() },
}))
vi.mock('@/api/channel', () => ({
  compactChannelList: (items: unknown) => Array.isArray(items) ? items.filter(item => item && typeof item === 'object') : [],
  channelApi: { getList: vi.fn(() => Promise.resolve([])), update: vi.fn() },
}))
vi.mock('@/api/deviceConfig', () => ({ deviceConfigApi: { getList: vi.fn(() => Promise.resolve({ list: [] })) } }))
vi.mock('@/api/parser', () => ({ parserApi: { getList: vi.fn(() => Promise.resolve([])) } }))
vi.mock('@/api/client', () => ({ default: { get: vi.fn(() => Promise.resolve({ data_count_today: 0 })) } }))

const stubs = {
  SkeletonCard: { template: '<div data-testid="skeleton-card" />' },
  EmptyState: { template: '<div data-testid="empty-state" />' },
  CountUp: { template: '<span data-testid="count-up">{{ $attrs.value }}</span>' },
}

function mountList() {
  return mount(EdgeDeviceList, { global: { stubs } })
}

describe('EdgeDeviceList.vue', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    localStorage.clear()
    sessionStorage.clear()
  })

  it('renders the device page and requests the device list on mount', async () => {
    const wrapper = mountList()
    await flushPromises()
    expect(wrapper.find('.device-page').exists()).toBe(true)
    expect(mockEdgeDeviceGetList).toHaveBeenCalled()
  })

  it('renders four summary cards without an obsolete stat action control', async () => {
    const wrapper = mountList()
    await flushPromises()
    expect(wrapper.findAll('.stat-card')).toHaveLength(4)
    expect(wrapper.find('.stat-action').exists()).toBe(false)
  })

  it('renders fetched device records in the page', async () => {
    const wrapper = mountList()
    await flushPromises()
    expect(wrapper.text()).toContain('Device A')
    expect(wrapper.text()).toContain('Device B')
  })

  it('keeps component-level list responses sequence guarded and rejects malformed list entries', () => {
    expect(source).toContain('sequence !== listRequestSequence')
    expect(source).toContain('const devices = ref<EdgeDevice[]>(compactEdgeDeviceList(initialCache?.items))')
    expect(source).toContain('let result = compactEdgeDeviceList(devices.value)')
  })

  it('defers wizard dependencies until the create dialog is opened', () => {
    expect(source).toContain('wizardDataLoaded = false')
    expect(source).toContain('const loadCreateWizardData = async () =>')
    expect(source).toContain('void loadCreateWizardData()')
    expect(source).toContain('watch(showCreateDialog')
  })

  it('creates devices from frozen form, channel, and parser snapshots', () => {
    expect(source).toContain('const frozenDeviceForm = { ...deviceForm }')
    expect(source).toContain('const frozenNewChannel = { ...newChannel }')
    expect(source).toContain('const frozenParser = selectedParser.value')
    expect(source).toContain('const deviceConfigId = frozenParser?.device_config_id ?? matchingDeviceConfig?.id')
    expect(source).toContain('device_config_id: deviceConfigId')
  })

  it('uses route-query initialization, card view, filter reset, and detail navigation contracts', () => {
    expect(source).toContain("const viewMode = ref<'card' | 'table'>('card')")
    expect(source).toContain("router.push(`/edge-device/${id}`)")
    expect(source).toContain("const routeSearch = typeof route.query.search === 'string' ? route.query.search : ''")
    expect(source).toContain("const routeStatus = typeof route.query.status === 'string' ? route.query.status : ''")
    expect(source).toContain('if (routeSearch) searchKeyword.value = routeSearch')
    expect(source).toContain('statusFilter.value = routeStatus')
    expect(source).toContain("currentPage.value = 1")
  })

  it('uses compact facts and reading regions in card view', () => {
    expect(source).toContain('class="card-facts"')
    expect(source).toContain('class="card-reading"')
  })

  it('invalidates caches and forces refresh after writes and deletion batches', () => {
    expect(source.match(/await fetchDevices\(true\)/g)).toHaveLength(2)
    expect(source.match(/await fetchDevices\(true, true\)/g)).toHaveLength(2)
    expect(source.match(/edgeDeviceStore\.invalidateLists\(\)/g)).toHaveLength(2)
    expect(source).toContain('Promise.allSettled(ids.map')
    expect(source).toContain('assertSessionGeneration(sessionGeneration)')
  })

  // P0-1: 选解析器后同步新通道硬件类型到解析器第一个可用总线,
  // 避免默认 i2c 与 uart 解析器不匹配。
  it('P0-1: selectParser syncs newChannel.hardware_type to the parser bus', () => {
    expect(source).toContain('const selectParser = (parser: Parser) => {')
    expect(source).toContain('const buses = parser.hardware_types || []')
    expect(source).toContain('if (buses.length > 0 && !buses.includes(newChannel.hardware_type)) {')
    expect(source).toContain('newChannel.hardware_type = buses[0]')
  })

  // P0-2: 硬件总线/已有通道过滤对 node_id(string 序列号 vs number)和
  // hardware_type(后端大写 UART vs 表单小写 uart)做归一化宽松匹配。
  it('P0-2: bus and existing-channel lookups normalize node_id and hardware_type', () => {
    expect(source).toContain('const nodeIdStr = String(deviceForm.node_id)')
    expect(source).toContain("const hwTypeLower = hardwareType.toLowerCase()")
    expect(source).toContain("String(ch.node_id) === nodeIdStr && (ch.hardware_type || '').toLowerCase() === hwTypeLower")
    expect(source).toContain("const parserBuses = selectedParser.value!.hardware_types.map(b => b.toLowerCase())")
    expect(source).toContain("parserBuses.includes((ch.hardware_type || '').toLowerCase())")
  })

  // P0-3: 步骤指示器标题不折行。
  it('P0-3: wizard step titles do not wrap', () => {
    expect(source).toContain('width="760px"')
    expect(source).toContain('.create-device-dialog :deep(.el-step__title) {')
    expect(source).toContain('white-space: nowrap;')
  })

  // P1-1: 编辑模式不走向导——隐藏步骤指示器与前两步,只改基本信息,且不传 type。
  it('P1-1: edit mode bypasses the wizard and never resubmits type', () => {
    expect(source).toContain('v-if="!editingDeviceId" :active="createStep"')
    expect(source).toContain('v-show="!editingDeviceId && createStep === 0"')
    expect(source).toContain('v-show="!editingDeviceId && createStep === 1"')
    expect(source).toContain('v-show="editingDeviceId || createStep === 2"')
    expect(source).toContain('保存修改')
    // 编辑提交不得携带 type(后端 G1 在 device_config_id>0 时拒绝)
    const editBlock = source.slice(source.indexOf('if (frozenEditingDeviceId) {'))
    const updateCall = editBlock.slice(0, editBlock.indexOf('})'))
    expect(updateCall).not.toContain('type:')
  })

  // Step2 通道模板 hardware_type 兜底:防 hardware_type 缺失(undefined/null)时
  // .toUpperCase() 抛 TypeError。带可选链的 ch.hardware_type?.toUpperCase() 是合法的。
  it('Step2 template guards ch.hardware_type with a fallback before toUpperCase()', () => {
    expect(source).toContain("(ch.hardware_type || '').toUpperCase()")
    // 不允许裸的 ch.hardware_type.toUpperCase()(允许可选链 ?. 形式)
    const bareMatches = source.match(/ch\.hardware_type\.toUpperCase\(\)/g)
    expect(bareMatches).toBeNull()
  })
})
