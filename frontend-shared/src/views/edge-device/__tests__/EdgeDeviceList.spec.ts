import { describe, expect, it, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import EdgeDeviceList from '@/views/edge-device/EdgeDeviceList.vue'
import source from '@/views/edge-device/EdgeDeviceList.vue?raw'
import type { EdgeDeviceListParams } from '@/api/edgeDevice'

// 显式标注 mock 签名: 无参 `vi.fn(() => ...)` 会把 `mock.calls` 推成 `[][]`
// (长度 0 的元组), 之后读 `calls[0][0]` 会报 TS2493。标注后 calls 是
// `[params?, force?][]`, 既能断言参数、也不需要任何 `as any`。
const { mockEdgeDeviceGetList, mockGetLogicalDeviceInfo, mockGetDriverCommands } = vi.hoisted(() => ({
  mockEdgeDeviceGetList: vi.fn<(params?: EdgeDeviceListParams) => Promise<{ items: unknown[]; total: number }>>(() => Promise.resolve({
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
  // EDGE-WIZ-004/005: 默认返回空指令集 (普通驱动); 具体测试按需覆盖为
  // jiabaida_bms 的 5 条 schedulable 指令。
  mockGetDriverCommands: vi.fn((..._args: any[]) => Promise.resolve([])),
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
    // 方案 v3.3 §1.3: 步骤 0 候选组件依赖; 测试默认空候选集
    getCandidates: vi.fn(() => Promise.resolve([])),
    // EDGE-WIZ-004/005: 创建向导逐指令轮询间隔依赖
    getDriverCommands: mockGetDriverCommands,
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

  // ── 负债 I-11: 真分页行为 (不是源码契约, 是真实请求参数) ──

  it('I-11: 挂载时带分页参数请求第 1 页', async () => {
    mountList()
    await flushPromises()
    expect(mockEdgeDeviceGetList).toHaveBeenCalledTimes(1)
    // page_size=24 是本页默认; page=1 是初始页。两者都必须真的发出去 ——
    // 改前虽也发, 但后端忽略, 这里钉死"前端侧参数正确"这一半。
    expect(mockEdgeDeviceGetList.mock.calls[0][0]).toMatchObject({ page: 1, page_size: 24 })
  })

  it('I-11: 翻页发 page=2 (服务端取数, 不是本地切片)', async () => {
    const wrapper = mountList()
    await flushPromises()
    const vm = wrapper.vm as any
    mockEdgeDeviceGetList.mockClear()

    vm.currentPage = 2
    await vm.fetchDevices()
    await flushPromises()

    expect(mockEdgeDeviceGetList).toHaveBeenCalledTimes(1)
    expect(mockEdgeDeviceGetList.mock.calls[0][0]).toMatchObject({ page: 2, page_size: 24 })
  })

  it('I-11: 渲染接口返回的当前页, 不再本地切片 (total 驱动分页器)', async () => {
    // 后端说 total=57 但本页只给 2 条 —— 若前端仍把全量数组本地切片,
    // 页面会渲染出远超 2 行的内容, 或分页器按错误的总数算页数。
    mockEdgeDeviceGetList.mockResolvedValueOnce({
      items: [
        { id: 1, name: 'Device A', status: 'active', device_type: 'temp_humidity', hardware_type: 'uart' },
        { id: 2, name: 'Device B', status: 'offline', device_type: 'wind_speed', hardware_type: 'i2c' },
      ],
      total: 57,
    })
    const wrapper = mountList()
    await flushPromises()
    const vm = wrapper.vm as any

    expect(vm.filteredDevices).toHaveLength(2)
    expect(vm.total).toBe(57)
  })

  it('I-11: 筛选变化重置 page=1 并重新请求 (§3.2.6 MUST)', async () => {
    const wrapper = mountList()
    await flushPromises()
    const vm = wrapper.vm as any

    // 先翻到第 3 页, 模拟"用户在第 3 页上改筛选"
    vm.currentPage = 3
    await vm.fetchDevices()
    await flushPromises()
    mockEdgeDeviceGetList.mockClear()

    // 改类型筛选 → 必须回到第 1 页 (第 3 页在新筛选下可能已越界)
    vm.typeFilter = 'jiabaida_bms'
    await flushPromises()

    expect(vm.currentPage).toBe(1)
    expect(mockEdgeDeviceGetList).toHaveBeenCalled()
    const params = mockEdgeDeviceGetList.mock.calls.at(-1)![0]
    expect(params).toMatchObject({ page: 1, page_size: 24, device_type: 'jiabaida_bms' })
  })

  it('I-11: hardware 筛选也重置页码 (改前它不在 watch 里, 页码不重置)', async () => {
    const wrapper = mountList()
    await flushPromises()
    const vm = wrapper.vm as any

    vm.currentPage = 3
    await vm.fetchDevices()
    await flushPromises()
    mockEdgeDeviceGetList.mockClear()

    vm.hardwareFilter = 'i2c'
    await flushPromises()

    expect(vm.currentPage).toBe(1)
    const params = mockEdgeDeviceGetList.mock.calls.at(-1)![0]
    // hardware_type 作为**服务端**筛选参数下发 (非本地 filter)。
    expect(params).toMatchObject({ page: 1, hardware_type: 'i2c' })
  })

  it('I-11: 搜索变化重置页码并作为服务端 search 参数下发', async () => {
    const wrapper = mountList()
    await flushPromises()
    const vm = wrapper.vm as any

    vm.currentPage = 2
    await vm.fetchDevices()
    await flushPromises()
    mockEdgeDeviceGetList.mockClear()

    vm.searchKeyword = 'bms'
    // 走 useDebouncedSearch 的 300ms 防抖; 直接同步其 debounced 值等价于防抖到期。
    await new Promise(resolve => setTimeout(resolve, 350))
    await flushPromises()

    expect(vm.currentPage).toBe(1)
    const params = mockEdgeDeviceGetList.mock.calls.at(-1)![0]
    expect(params).toMatchObject({ page: 1, search: 'bms' })
  })

  it('I-11: 清空筛选只触发一次取数 (watch 收口, 不重复请求)', async () => {
    const { useEdgeDeviceStore } = await import('@/stores/edgeDevice')
    const store = useEdgeDeviceStore()
    const fetchSpy = vi.spyOn(store, 'fetchList')

    const wrapper = mountList()
    await flushPromises()
    const vm = wrapper.vm as any

    vm.typeFilter = 'jiabaida_bms'
    vm.hardwareFilter = 'uart'
    vm.searchKeyword = 'bms'
    await new Promise(resolve => setTimeout(resolve, 350))
    await flushPromises()
    fetchSpy.mockClear()

    vm.clearFilters()
    await flushPromises()

    // 四个 ref 在同一 tick 内变更 → Vue 批处理成**一次** watch 回调 → 一次取数。
    // 断言的是 fetchList 的调用次数而不是 API 次数: 清空后回到
    // {page:1,page_size:24} 这个键, store 有新鲜缓存, 合法地不发网络请求 ——
    // 那样的断言会把"缓存命中"误判成"没触发取数"。
    expect(fetchSpy).toHaveBeenCalledTimes(1)
    // 清空后不得再携带任何筛选参数 —— 用 toEqual 钉死**恰好**只剩分页两项,
    // 而不是 toMatchObject (后者对多出来的字段视而不见, 会漏掉"清空没清干净")。
    expect(fetchSpy.mock.calls[0][0]).toEqual({ page: 1, page_size: 24 })
    fetchSpy.mockRestore()
  })

  it('keeps component-level list responses sequence guarded and rejects malformed list entries', () => {
    expect(source).toContain('sequence !== listRequestSequence')
    expect(source).toContain('devices.value = compactEdgeDeviceList(initialCache?.items)')
    // 为什么这里断言"某段代码**不存在**":
    // 本地 filter 链 (_searchFilteredItems / device_type / status / hardware_type
    // 逐个 filter) 已按 §3.3.5「服务端分页时不得把当前页本地筛选伪装成全局检索」
    // **下沉服务端并整体删除**。分页落地后, 本地再过滤一次只会把服务端已筛好的
    // 当前页二次筛一遍, 且口径会漂移 (服务端 hardware_type 大小写不敏感、本地是
    // 严格相等; 服务端 search 同时匹配中文标签之外的 name/type)。反向钉死是为了
    // **防止回退到本地过滤** —— 那是本负债的原始形态, 一旦复活, 用户又会看到
    // "搜索只在当前页生效"这种伪装成全局检索的行为。
    expect(source).not.toContain('_searchFilteredItems')
    expect(source).not.toContain("result = result.filter(d => d.device_type === typeFilter.value)")
    expect(source).not.toContain("result = result.filter(d => d.status === statusFilter.value)")
    expect(source).not.toContain("result = result.filter(d => d.hardware_type === hardwareFilter.value)")
    expect(source).toContain('const filteredDevices = computed(() => devices.value)')
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
    expect(source).toContain('confirmBatchDeleteBase(deleteData)')
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

  // P1-1: 编辑模式不走向导——隐藏步骤指示器与前三步,只改基本信息,且不传 type。
  // (步骤编号含 v3.3 §3.1 新增的步骤 0"历史数据继承": 0=继承 1=型号 2=通道 3=基本信息)
  it('P1-1: edit mode bypasses the wizard and never resubmits type', () => {
    expect(source).toContain('v-if="!editingDeviceId" :active="createStep"')
    expect(source).toContain('v-show="!editingDeviceId && createStep === 0"')
    expect(source).toContain('v-show="!editingDeviceId && createStep === 1"')
    expect(source).toContain('v-show="!editingDeviceId && createStep === 2"')
    expect(source).toContain('v-show="editingDeviceId || createStep === 3"')
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

  it('包裹移动端横滚容器并显示滑动提示 (源码契约)', () => {
    // 移动端宽表（规范 §4.3.4.1 MUST）：表格必须包在 .mobile-table-wrapper 内并提供横滑提示
    expect(source).toContain('<div class="mobile-table-wrapper">')
    expect(source).toContain('<div class="mobile-table-hint">')
    expect(/<div class="mobile-table-wrapper">[\s\S]*<el-table[\s\S]*<\/el-table>[\s\S]*<\/div>/.test(source)).toBe(true)
    // 容器闭合必须落在表格所在的 el-card 内（不能把卡片列表一起包进去）
    const wrapperClose = source.indexOf('</el-table>')
    const cardClose = source.indexOf('</el-card>', wrapperClose)
    // 用"设备卡片列表"分区注释作锚点：页首 loading 骨架屏也有一个 .device-grid
    const cardListStart = source.indexOf('<!-- 设备卡片列表 -->')
    expect(wrapperClose).toBeGreaterThan(-1)
    expect(cardClose).toBeGreaterThan(wrapperClose)
    expect(cardClose).toBeLessThan(cardListStart)
  })

  it('窄屏操作列收窄到 96px 并给行内图标按钮补触控热区 (源码契约)', () => {
    // 原 160px 固定操作列在 390px 视口占 41%：三个按钮都只有图标，96px 足够
    expect(source).toContain('<el-table-column label="操作" width="96" fixed="right">')
    expect(source).not.toContain('<el-table-column label="操作" width="160" fixed="right">')
    // 每个行内图标按钮都有可访问名称 + 热区类
    expect(source).toContain('class="touch-target" :aria-label="`查看 ${row.name}`"')
    expect(source).toContain('class="touch-target" :aria-label="`编辑 ${row.name}`"')
    expect(source).toContain('class="touch-target" :aria-label="`删除 ${row.name}`"')
  })

  it('删除弹窗不再使用 ElMessageBox (源码契约)', () => {
    expect(source).not.toContain('ElMessageBox.confirm')
    expect(source).toContain('DeviceDeleteDialog')
    expect(source).toContain('DeviceBatchDeleteDialog')
    expect(source).toContain('useDeviceDelete')
  })

  // ---- 数据生命周期 T5: 创建继承 步骤 0 (方案 v3.3 §3.1/§3.2) ----

  it('步骤0: 向导默认"作为新设备创建", 不渲染候选组件', async () => {
    const wrapper = mountList()
    await flushPromises()
    // 打开创建对话框
    const createBtn = wrapper.findAll('button').find(b => b.text().includes('创建边缘设备'))
    expect(createBtn).toBeTruthy()
    await createBtn!.trigger('click')
    await flushPromises()

    const dialog = wrapper.find('.el-dialog')
    expect(dialog.exists()).toBe(true)
    // 步骤指示器含新增的步骤 0 (ElSteps/ElStep 为通用 stub 不渲染 title prop,
    // 用源码契约断言步骤标题)
    expect(source).toContain('<el-step title="历史数据继承" />')
    expect(dialog.text()).toContain('此设备是否要继承历史数据')
    // 默认 radio = 作为新设备创建 (is-checked 在第一个 radio 上)
    const radios = dialog.findAll('.inherit-mode-radio input[type="radio"]')
    expect(radios.length).toBe(2)
    expect((radios[0].element as HTMLInputElement).checked).toBe(true)
    expect((radios[1].element as HTMLInputElement).checked).toBe(false)
    // 未选"继承" → 候选组件不渲染
    expect(dialog.find('.inherit-candidate-area').exists()).toBe(false)
  })

  it('步骤0: 选"继承历史数据"展开候选组件, 型号未选时显示引导', async () => {
    const wrapper = mountList()
    await flushPromises()
    const createBtn = wrapper.findAll('button').find(b => b.text().includes('创建边缘设备'))
    await createBtn!.trigger('click')
    await flushPromises()

    const dialog = wrapper.find('.el-dialog')
    const radios = dialog.findAll('.inherit-mode-radio input[type="radio"]')
    await radios[1].setValue(true)
    await flushPromises()

    expect(dialog.find('.inherit-candidate-area').exists()).toBe(true)
    // 步骤 0 时型号未选 (type=''), 候选组件显示"先选型号"引导而非空列表
    expect(dialog.find('[data-testid="candidate-awaiting-type"]').exists()).toBe(true)
  })

  // ---- EDGE-WIZ-003: 打开创建向导强制刷新通道 ----

  it('EDGE-WIZ-003: 打开创建向导强制刷新通道 (即使 store 已有旧数据)', () => {
    expect(source).toContain('channelStore.fetchChannels(undefined, true)')
    expect(source).not.toContain('channelStore.channels.length === 0 ? channelStore.fetchChannels')
  })

  it('EDGE-WIZ-003: 节点 change 事件也会刷新通道列表', () => {
    expect(source).toContain('@change="handleNodeChange"')
    expect(source).toContain('const handleNodeChange = () => {')
    expect(source).toContain('void channelStore.fetchChannels(undefined, true)')
  })

  it('EDGE-WIZ-003: 行为验证 — 通道 store 已有旧数据时打开向导仍强制刷新通道', async () => {
    const { useChannelStore } = await import('@/stores/channel')
    const channelStore = useChannelStore()
    // 模拟旧缓存: store 里已经有通道 (节点页此前加载过)
    channelStore.channels = [
      { id: 99, node_id: 'node-1', hardware_type: 'i2c' as const, hardware_id: 'I2C0', config: {} },
    ]
    const spy = vi.spyOn(channelStore, 'fetchChannels').mockResolvedValue(undefined as any)

    const wrapper = mountList()
    await flushPromises()
    const vm = wrapper.vm as any
    // 打开创建向导 (触发 watch(showCreateDialog) → loadCreateWizardData)
    vm.showCreateDialog = true
    await flushPromises()

    // 虽然 store 已有通道, 仍强制 fetchChannels(undefined, force=true)
    expect(spy).toHaveBeenCalledWith(undefined, true)
    expect(channelStore.channels.length).toBeGreaterThan(0)
    spy.mockRestore()
  })

  // ---- EDGE-WIZ-002: 通道卡片字体 ----

  it('EDGE-WIZ-002: 通道卡片不再使用原生 <code> 默认等宽字体', () => {
    // 卡片内容用 span.channel-name + 正文字体; 原生 <code> 展示通道名的写法已移除
    expect(source).toContain('<span class="channel-name"')
    expect(source).not.toContain('<code>{{ ch.name ||')
    expect(source).toContain('.channel-select-card .channel-name')
    expect(source).toContain('font-family: var(--el-font-family);')
  })

  it('EDGE-WIZ-002: 通道卡片仍保留硬件类型与硬件ID信息', () => {
    expect(source).toContain('class="channel-bus-id"')
    expect(source).toContain('getHardwareTagType(ch.hardware_type)')
  })

  // ---- EDGE-WIZ-004/005: 创建向导逐指令轮询间隔 ----

  const jiabaidaCommands = () => [
    { id: 'read_basic_info', name: '读取基本信息', type: 'read', cmd_byte: 0x03, write_data: '', read_length: 60, delay_ms: 100, interval_ms: 5000, schedulable: true, description: '总电压、电流、剩余容量' },
    { id: 'read_cell_voltage', name: '读取单体电压', type: 'read', cmd_byte: 0x04, write_data: '', read_length: 50, delay_ms: 100, interval_ms: 0, schedulable: true, description: '每串电芯电压' },
    { id: 'read_hardware_version', name: '读取硬件版本', type: 'read', cmd_byte: 0x05, write_data: '', read_length: 40, delay_ms: 100, interval_ms: 0, schedulable: true, description: '硬件版本字符串' },
    { id: 'read_comprehensive', name: '读取综合信息', type: 'read', cmd_byte: 0x0F, write_data: '', read_length: 100, delay_ms: 100, interval_ms: 0, schedulable: true, description: '0x03超集' },
    { id: 'read_protection_count', name: '读取保护历史次数', type: 'read', cmd_byte: 0xAA, write_data: '', read_length: 40, delay_ms: 100, interval_ms: 0, schedulable: true, description: '保护触发次数统计' },
  ]

  // 打开创建对话框并选中指定型号, 使 CreateWizardCommandIntervals 挂载
  const openWizardWithParser = async (wrapper: any, parser: any) => {
    const createBtn = wrapper.findAll('button').find((b: any) => b.text().includes('创建边缘设备'))
    await createBtn!.trigger('click')
    await flushPromises()
    wrapper.vm.selectedParser = parser
    await wrapper.vm.$nextTick()
    await flushPromises()
    return wrapper.vm
  }

  it('EDGE-WIZ-004/005: 选择 jiabaida_bms 后加载并渲染 5 条 schedulable 轮询指令', async () => {
    mockGetDriverCommands.mockImplementationOnce(() => Promise.resolve(jiabaidaCommands() as any))
    const wrapper = mountList()
    await flushPromises()
    const vm = await openWizardWithParser(wrapper, { id: 'jiabaida_bms', name: '嘉佰达 BMS', hardware_types: ['uart'] })

    expect(mockGetDriverCommands).toHaveBeenCalledWith('jiabaida_bms')
    // 子组件公开的 schedulable 指令列表
    const el = vm.commandIntervalsRef
    expect(el).toBeTruthy()
    const schedulable = el?.schedulableCommands || []
    expect(schedulable.map((c: any) => c.id)).toEqual([
      'read_basic_info', 'read_cell_voltage', 'read_hardware_version',
      'read_comprehensive', 'read_protection_count',
    ])
    expect(schedulable).toHaveLength(5)
  })

  it('EDGE-WIZ-004/005: 修改一条指令间隔、禁用另一条后 create 携带正确 command_intervals', async () => {
    mockGetDriverCommands.mockImplementationOnce(() => Promise.resolve(jiabaidaCommands() as any))
    const { edgeDeviceApi } = await import('@/api/edgeDevice')
    const wrapper = mountList()
    await flushPromises()
    const vm = await openWizardWithParser(wrapper, { id: 'jiabaida_bms', name: '嘉佰达 BMS', hardware_types: ['uart'] })

    vm.selectedChannel = { id: 5, hardware_id: 'UART0', config: { device_type: 'jiabaida_bms' } }
    vm.channelTab = 'existing'
    vm.deviceForm.name = 'BMS-2'
    vm.deviceForm.node_id = 1
    vm.deviceFormRef = { validate: () => Promise.resolve() }

    const el = vm.commandIntervalsRef
    expect(el).toBeTruthy()
    // 直接驱动子组件内部状态等价于用户编辑 input-number
    el.setInterval('read_basic_info', 10000)
    el.setInterval('read_protection_count', 0)  // 禁用
    await vm.handleCreate()
    await flushPromises()

    expect(edgeDeviceApi.create).toHaveBeenCalledTimes(1)
    const arg = (edgeDeviceApi.create as any).mock.calls[0][0]
    expect(arg.command_intervals).toEqual({
      read_basic_info: 10000,
      read_cell_voltage: 0,
      read_hardware_version: 0,
      read_comprehensive: 0,
      read_protection_count: 0,
    })
  })

  it('EDGE-WIZ-004/005: 有 schedulable 指令时隐藏全局“采集间隔”, 无则保留', () => {
    // 有 schedulable → 全局 input-number 不渲染, 显示“按下方轮询指令逐条设置”
    expect(source).toContain('v-if="!hasSchedulableCommands" label="采集间隔 (ms)"')
    expect(source).toContain('该设备型号按下方“轮询指令”逐条设置间隔（0 = 禁用）')
    // 确认卡片: 有快照显示“N 条已配置”, 否则显示全局间隔
    expect(source).toContain('轮询指令')
    expect(source).toContain('条已配置')
    expect(source).toContain('const hasSchedulableCommands = computed(() => {')
  })

  it('EDGE-WIZ-004/005: 切换设备型号不会携带旧型号的 command_intervals', async () => {
    mockGetDriverCommands.mockImplementationOnce(() => Promise.resolve(jiabaidaCommands() as any))
    mockGetDriverCommands.mockImplementationOnce(() => Promise.resolve([] as any))  // 新型号无 schedulable 指令
    const { edgeDeviceApi } = await import('@/api/edgeDevice')
    const wrapper = mountList()
    await flushPromises()
    const vm = await openWizardWithParser(wrapper, { id: 'jiabaida_bms', name: '嘉佰达 BMS', hardware_types: ['uart'] })

    const el = vm.commandIntervalsRef
    expect(el).toBeTruthy()
    el.setInterval('read_basic_info', 7000)

    // 切换到无 schedulable 指令的型号
    vm.selectParser({ id: 'sn3001_rain', name: 'SN-3001', hardware_types: ['uart'] })
    await wrapper.vm.$nextTick()
    await flushPromises()

    vm.selectedChannel = { id: 5, hardware_id: '0x01', config: { device_type: 'sn3001_rain' } }
    vm.channelTab = 'existing'
    vm.deviceForm.name = 'SN'
    vm.deviceForm.node_id = 1
    vm.deviceFormRef = { validate: () => Promise.resolve() }
    await vm.handleCreate()
    await flushPromises()

    expect(edgeDeviceApi.create).toHaveBeenCalledTimes(1)
    const arg = (edgeDeviceApi.create as any).mock.calls[0][0]
    expect(arg.command_intervals).toBeUndefined()
  })

  it('EDGE-WIZ-004/005: 驱动指令加载失败时拦截提交并提示, 不携带旧间隔', async () => {
    mockGetDriverCommands.mockRejectedValueOnce(new Error('driver down'))
    const { edgeDeviceApi } = await import('@/api/edgeDevice')
    const { ElMessage } = await import('element-plus')
    const wrapper = mountList()
    await flushPromises()
    const vm = await openWizardWithParser(wrapper, { id: 'jiabaida_bms', name: '嘉佰达 BMS', hardware_types: ['uart'] })

    vm.selectedChannel = { id: 5, hardware_id: 'UART0', config: { device_type: 'jiabaida_bms' } }
    vm.channelTab = 'existing'
    vm.deviceForm.name = 'BMS'
    vm.deviceForm.node_id = 1
    vm.deviceFormRef = { validate: () => Promise.resolve() }

    // 加载失败 → 子组件 loadFailed 置位, 父组件记录错误
    expect(vm.commandIntervalsError).toBeTruthy()
    expect((vm.commandIntervalsRef as any)?.loadFailed).toBe(true)
    expect(ElMessage.error).toHaveBeenCalled()

    await vm.handleCreate()
    await flushPromises()
    expect(edgeDeviceApi.create).not.toHaveBeenCalled()
  })

  it('步骤0: 默认与选"继承"均可进入下一步 (候选依赖型号, 提交时拦截)', () => {
    // 交互死锁防护: 候选列表依赖设备型号 (步骤 1), 若步骤 0 要求先选候选
    // 才能进步骤 1 会形成死锁; 故步骤 0 恒可过, 拦截逻辑在 handleCreate。
    expect(source).toContain('if (createStep.value === 0) return true')
    expect(source).toContain("inheritMode.value === 'inherit' && inheritLogicalDeviceId.value === null")
    expect(source).toContain('已选择"继承历史数据"但未选中候选逻辑设备')
  })

  it('创建: "作为新设备创建"不携带 logical_device_id', async () => {
    const { edgeDeviceApi } = await import('@/api/edgeDevice')
    const wrapper = mountList()
    await flushPromises()
    const vm = wrapper.vm as any

    // 构造创建上下文: 型号 + 已有通道 + 表单
    vm.selectedParser = { id: 'sn3001_rain', name: 'SN-3001', hardware_types: ['uart'] }
    vm.selectedChannel = { id: 5, hardware_id: '0x01', config: { device_type: 'sn3001_rain' } }
    vm.channelTab = 'existing'
    vm.deviceForm.name = '新雨量计'
    vm.deviceForm.node_id = 1
    vm.inheritMode = 'new'
    vm.inheritLogicalDeviceId = null
    vm.deviceFormRef = { validate: () => Promise.resolve() }
    await vm.handleCreate()
    await flushPromises()

    expect(edgeDeviceApi.create).toHaveBeenCalledTimes(1)
    const arg = (edgeDeviceApi.create as any).mock.calls[0][0]
    expect(arg).toEqual(expect.objectContaining({
      name: '新雨量计',
      node_id: '1',
      channel_id: 5,
      type: 'sn3001_rain',
    }))
    expect(arg.logical_device_id).toBeUndefined()
  })

  it('创建: 选"继承"且已选候选 → create 携带 logical_device_id', async () => {
    const { edgeDeviceApi } = await import('@/api/edgeDevice')
    const wrapper = mountList()
    await flushPromises()
    const vm = wrapper.vm as any

    vm.selectedParser = { id: 'sn3001_rain', name: 'SN-3001', hardware_types: ['uart'] }
    vm.selectedChannel = { id: 5, hardware_id: '0x01', config: { device_type: 'sn3001_rain' } }
    vm.channelTab = 'existing'
    vm.deviceForm.name = '雨量计-重建'
    vm.deviceForm.node_id = 1
    vm.inheritMode = 'inherit'
    vm.inheritLogicalDeviceId = 42
    vm.deviceFormRef = { validate: () => Promise.resolve() }
    await vm.handleCreate()
    await flushPromises()

    expect(edgeDeviceApi.create).toHaveBeenCalledWith(expect.objectContaining({
      name: '雨量计-重建',
      logical_device_id: 42,
    }))
  })

  it('创建: 选"继承"但未选候选 → 提交拦截, 不调 create', async () => {
    const { edgeDeviceApi } = await import('@/api/edgeDevice')
    const { ElMessage } = await import('element-plus')
    const wrapper = mountList()
    await flushPromises()
    const vm = wrapper.vm as any

    vm.selectedParser = { id: 'sn3001_rain', name: 'SN-3001', hardware_types: ['uart'] }
    vm.selectedChannel = { id: 5, hardware_id: '0x01', config: { device_type: 'sn3001_rain' } }
    vm.channelTab = 'existing'
    vm.deviceForm.name = '雨量计'
    vm.deviceForm.node_id = 1
    vm.inheritMode = 'inherit'
    vm.inheritLogicalDeviceId = null
    vm.deviceFormRef = { validate: () => Promise.resolve() }
    await vm.handleCreate()
    await flushPromises()

    expect(edgeDeviceApi.create).not.toHaveBeenCalled()
    expect(ElMessage.warning).toHaveBeenCalledWith(expect.stringContaining('未选中候选逻辑设备'))
  })

  it('创建对话框关闭/重置清空继承状态', async () => {
    const wrapper = mountList()
    await flushPromises()
    const vm = wrapper.vm as any

    vm.inheritMode = 'inherit'
    vm.inheritLogicalDeviceId = 7
    vm.resetCreateDialog()
    expect(vm.inheritMode).toBe('new')
    expect(vm.inheritLogicalDeviceId).toBeNull()
    expect(vm.createStep).toBe(0)
  })
})
