import { describe, expect, it, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import EdgeDeviceList from '@/views/edge-device/EdgeDeviceList.vue'
import source from '@/views/edge-device/EdgeDeviceList.vue?raw'

const { mockEdgeDeviceGetList, mockGetLogicalDeviceInfo } = vi.hoisted(() => ({
  mockEdgeDeviceGetList: vi.fn(() => Promise.resolve({
    items: [
      { id: 1, name: 'Device A', status: 'active', device_type: 'temp_humidity', hardware_type: 'uart', logical_device_id: 11 },
      { id: 2, name: 'Device B', status: 'offline', device_type: 'wind_speed', hardware_type: 'i2c' },
    ],
    total: 2,
  })),
  mockGetLogicalDeviceInfo: vi.fn(() => Promise.resolve({
    edge_device_id: 1,
    name: 'Logic-A',
    logical_device_id: 11,
    retention_days: 30,
    instance_count: 2,
    row_estimate: 500,
  })),
}))

vi.mock('element-plus', () => ({
  ElMessage: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
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
  edgeDeviceApi: {
    getList: mockEdgeDeviceGetList,
    create: vi.fn(),
    update: vi.fn(),
    delete: vi.fn(),
    getLogicalDeviceInfo: mockGetLogicalDeviceInfo,
  },
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

  // ---- 数据生命周期 T2: 删除确认弹窗改造 (方案 v3.3 §2.1/§2.2) ----

  it('单删: 卡片视图点击删除打开 DeviceDeleteDialog 并异步加载逻辑设备信息', async () => {
    const wrapper = mountList()
    await flushPromises()

    // 卡片视图 (默认) 中的删除按钮
    const deleteButtons = wrapper.findAll('.device-grid .el-button--danger')
    expect(deleteButtons.length).toBeGreaterThan(0)
    await deleteButtons[0].trigger('click')
    await flushPromises()

    // 弹窗渲染: 基本信息即时可见
    const dialog = wrapper.find('.el-dialog')
    expect(dialog.exists()).toBe(true)
    expect(dialog.text()).toContain('Device A')
    expect(dialog.text()).toContain('删除边缘设备')
    // 信息区异步加载完成 (mock resolve)
    expect(mockGetLogicalDeviceInfo).toHaveBeenCalledWith(1)
    expect(dialog.text()).toContain('Logic-A')
    expect(dialog.text()).toContain('约 500 条')
    expect(dialog.text()).toContain('保留 30 天')
  })

  it('单删: 默认保留历史数据 → delete_data=false 提交', async () => {
    const { edgeDeviceApi } = await import('@/api/edgeDevice')
    const wrapper = mountList()
    await flushPromises()

    await wrapper.findAll('.device-grid .el-button--danger')[0].trigger('click')
    await flushPromises()

    // 默认 radio = 保留
    const radios = wrapper.findAll('.data-action-group input[type="radio"]')
    expect((radios[0].element as HTMLInputElement).checked).toBe(true)

    await wrapper.find('.el-dialog__footer .el-button--danger').trigger('click')
    await flushPromises()

    expect(edgeDeviceApi.delete).toHaveBeenCalledWith(1, { delete_data: false })
  })

  it('单删: 选择同时删除历史数据 → delete_data=true 提交', async () => {
    const { edgeDeviceApi } = await import('@/api/edgeDevice')
    const wrapper = mountList()
    await flushPromises()

    await wrapper.findAll('.device-grid .el-button--danger')[0].trigger('click')
    await flushPromises()

    const radios = wrapper.findAll('.data-action-group input[type="radio"]')
    await radios[1].setValue(true)
    await wrapper.find('.el-dialog__footer .el-button--danger').trigger('click')
    await flushPromises()

    expect(edgeDeviceApi.delete).toHaveBeenCalledWith(1, { delete_data: true })
  })

  it('单删: 信息请求失败降级 — 信息区不显示但删除仍可提交', async () => {
    mockGetLogicalDeviceInfo.mockImplementationOnce(() => Promise.reject(new Error('network down')))
    const { edgeDeviceApi } = await import('@/api/edgeDevice')
    const wrapper = mountList()
    await flushPromises()

    await wrapper.findAll('.device-grid .el-button--danger')[0].trigger('click')
    await flushPromises()

    const dialog = wrapper.find('.el-dialog')
    expect(dialog.exists()).toBe(true)
    expect(dialog.text()).toContain('Device A')
    expect(dialog.find('.logical-info').exists()).toBe(false)

    await wrapper.find('.el-dialog__footer .el-button--danger').trigger('click')
    await flushPromises()
    expect(edgeDeviceApi.delete).toHaveBeenCalledWith(1, { delete_data: false })
  })

  it('批删: 表格多选打开汇总弹窗, 展示 N 台/M 台逻辑设备, 统一 radio 默认保留', async () => {
    const wrapper = mountList()
    await flushPromises()

    // 切换到表格视图
    const tableModeButton = wrapper.findAll('button').find((b: any) => b.attributes('aria-label') === '表格视图')
    expect(tableModeButton).toBeTruthy()
    await tableModeButton!.trigger('click')
    await flushPromises()

    // 勾选两行 (Device A 有 logical_device_id, Device B 无)
    const checkboxes = wrapper.findAll('.el-table__row-checkbox')
    expect(checkboxes).toHaveLength(2)
    await checkboxes[0].setValue(true)
    await checkboxes[1].setValue(true)
    await flushPromises()

    // 批量删除按钮出现并点击
    const batchDeleteButton = wrapper.findAll('button').find((b: any) => b.text().includes('批量删除'))
    expect(batchDeleteButton).toBeTruthy()
    await batchDeleteButton!.trigger('click')
    await flushPromises()

    const dialog = wrapper.find('.el-dialog')
    expect(dialog.exists()).toBe(true)
    expect(dialog.text()).toContain('批量删除确认')
    expect(dialog.text()).toContain('将删除')
    expect(dialog.text()).toContain('2')
    expect(dialog.text()).toContain('1')
    expect(dialog.text()).toContain('逻辑设备')

    // 统一 radio 默认全部保留
    const radios = dialog.findAll('.data-action-group input[type="radio"]')
    expect(radios).toHaveLength(2)
    expect((radios[0].element as HTMLInputElement).checked).toBe(true)
  })

  it('批删: 确认逐条调用 delete(id, {delete_data}) 并传入 radio 选择', async () => {
    const { edgeDeviceApi } = await import('@/api/edgeDevice')
    const wrapper = mountList()
    await flushPromises()

    const tableModeButton = wrapper.findAll('button').find((b: any) => b.attributes('aria-label') === '表格视图')
    await tableModeButton!.trigger('click')
    await flushPromises()

    const checkboxes = wrapper.findAll('.el-table__row-checkbox')
    await checkboxes[0].setValue(true)
    await checkboxes[1].setValue(true)
    await flushPromises()

    const batchDeleteButton = wrapper.findAll('button').find((b: any) => b.text().includes('批量删除'))
    await batchDeleteButton!.trigger('click')
    await flushPromises()

    // 选择全部删除
    const dialog = wrapper.find('.el-dialog')
    const radios = dialog.findAll('.data-action-group input[type="radio"]')
    await radios[1].setValue(true)
    await dialog.find('.el-dialog__footer .el-button--danger').trigger('click')
    await flushPromises()

    expect(edgeDeviceApi.delete).toHaveBeenCalledTimes(2)
    expect(edgeDeviceApi.delete).toHaveBeenCalledWith(1, { delete_data: true })
    expect(edgeDeviceApi.delete).toHaveBeenCalledWith(2, { delete_data: true })
  })

  it('删除弹窗不再使用 ElMessageBox (源码契约)', () => {
    expect(source).not.toContain('ElMessageBox.confirm')
    expect(source).toContain('DeviceDeleteDialog')
    expect(source).toContain('DeviceBatchDeleteDialog')
    expect(source).toContain('delete_data: deleteData')
  })
})
