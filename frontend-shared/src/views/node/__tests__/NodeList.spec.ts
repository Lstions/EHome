import { describe, expect, it, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import NodeList from '@/views/node/NodeList.vue'
import source from '@/views/node/NodeList.vue?raw'
import OTAForm from '@/components/forms/OTAForm.vue'
import type { NodeListParams } from '@/api/node'

// 显式标注 mock 签名: 无参 `vi.fn(() => ...)` 会把 `mock.calls` 推成 `[][]`
// (长度 0 的元组), 之后读 `calls[0][0]` 会报 TS2493。标注后 calls 是
// `[params?, force?][]`, 既能断言参数、也不需要任何 `as any`。
const { mockFetchNodes, mockGetCachedList, mockPush, mockRouteQuery, mockCreate, mockInfo, mockWarning, mockSuccess } = vi.hoisted(() => ({
  mockFetchNodes: vi.fn<(params?: NodeListParams, force?: boolean) => Promise<void>>(() => Promise.resolve()),
  mockGetCachedList: vi.fn(() => ({
    items: [
      { id: 1, node_id: 'node-1', name: 'Collector-A', model: 'ESP32', status: 'online' },
      { id: 2, node_id: 'node-2', name: 'Collector-B', model: 'RPi4', status: 'offline' },
      { id: 3, node_id: 'node-3', name: 'Collector-C', model: 'ESP32', status: 'online' },
    ],
    total: 3,
  })),
  mockPush: vi.fn(),
  // `?action=add` 深链用例要在 mount 前改这个对象, 因此必须是可变的共享引用。
  mockRouteQuery: {} as Record<string, string>,
  mockCreate: vi.fn<(data: { node_id: string; name?: string; config?: string }) => Promise<unknown>>(),
  mockInfo: vi.fn(),
  mockWarning: vi.fn(),
  mockSuccess: vi.fn(),
}))
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: mockPush }),
  useRoute: () => ({ query: mockRouteQuery }),
}))
// 本用例组只关心"有没有点着入口": 创建走 mock。OTAForm(被测生产组件) 内部
// import { nodeApi } 并调用 startOTA —— 真实 nodeApi 会发 HTTP, 必须一并 mock。
// importOriginal 保留其余 api/* 导出 (client / 类型), 不能整模块替换。
vi.mock('@/api/node', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/node')>()
  return { ...actual, nodeApi: { ...actual.nodeApi, create: mockCreate } }
})
// 成功/失败文案要能被断言, 但不要把整个 element-plus 换掉 (ElDialog 等 stub 依赖它)。
// ElMessage 本体仍是**可调用函数**(feedback.error → ElMessage({...})), 只能挂钩子、
// 不能整体替换成对象 —— 否则 feedback 会抛 "ElMessage is not a function"。
vi.mock('element-plus', async (importOriginal) => {
  const actual = await importOriginal<typeof import('element-plus')>()
  return {
    ...actual,
    ElMessage: Object.assign((opts: unknown) => (actual.ElMessage as any)(opts), {
      success: mockSuccess,
      warning: mockWarning,
      info: mockInfo,
      error: vi.fn(),
    }),
  }
})
vi.mock('@/stores/node', () => ({
  useNodeStore: () => ({
    fetchNodes: mockFetchNodes,
    hasCachedList: vi.fn(() => true),
    hasFreshList: vi.fn(() => true),
    getCachedList: mockGetCachedList,
  }),
}))
vi.mock('@/stores/websocket', () => ({ useWebSocketStore: () => ({ connected: false, subscribe: vi.fn(() => vi.fn()) }) }))
vi.mock('@/events/events', () => ({ WS_EVENT: { NODE_STATUS: 'node_status' } }))
// OTAForm(生产组件) 挂载即拉固件列表: 不 mock 会真的发 HTTP, 用例不再自洽。
vi.mock('@/stores/firmware', () => ({ useFirmwareStore: () => ({ list: [], fetchList: vi.fn(() => Promise.resolve()) }) }))

const stubs = {
  SkeletonCard: { template: '<div data-testid="skeleton-card" />' },
  EmptyState: { template: '<div data-testid="empty-state" />' },
  CountUp: { template: '<span data-testid="count-up">{{ $attrs.value }}</span>' },
}

function mountList() {
  return mount(NodeList, { global: { stubs } })
}

describe('NodeList.vue', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    for (const key of Object.keys(mockRouteQuery)) delete mockRouteQuery[key]
  })

  it('renders the collector page, reads cache, and validates the list on mount', async () => {
    const wrapper = mountList()
    await flushPromises()
    expect(wrapper.find('.collector-page').exists()).toBe(true)
    expect(mockFetchNodes).toHaveBeenCalled()
    expect(wrapper.find('[data-testid="skeleton-card"]').exists()).toBe(false)
  })

  it('renders four summary cards without obsolete action or trend controls', async () => {
    const wrapper = mountList()
    await flushPromises()
    expect(wrapper.findAll('.stat-card')).toHaveLength(4)
    expect(wrapper.find('.stat-action').exists()).toBe(false)
    expect(wrapper.find('.stat-trend').exists()).toBe(false)
  })

  it('guards list requests and keeps explicit refresh errors observable', () => {
    expect(source).toContain('sequence !== listRequestSequence')
    expect(source).toContain('fetchNodes(false, true, true)')
    expect(source).toContain('节点已删除，但列表刷新失败')
    expect(source).toContain('nodes.value = nodes.value.filter')
  })

  it('uses search, status, model filters and restores page one when filters reset', () => {
    expect(source).toContain('searchKeyword')
    expect(source).toContain('statusFilter')
    expect(source).toContain('modelFilter')
    expect(source).toContain('const filteredNodes = computed')
    expect(source).toContain('currentPage.value = 1')
  })

  it('initializes query filters, offers grid/list views, and routes detail navigation', () => {
    expect(source).toContain("const viewMode = ref<'grid' | 'list'>('grid')")
    expect(source).toContain("router.push(`/node/${nodeId}`)")
    expect(source).toContain('route.query.search')
    expect(source).toContain('route.query.status')
  })

  it('wraps the wide table view with the mobile scroll container and hint', () => {
    // 移动端宽表（规范 §4.3.4.1 MUST）：表格必须包在 .mobile-table-wrapper 内并提供横滑提示
    expect(source).toContain('<div class="mobile-table-wrapper">')
    expect(source).toContain('<div class="mobile-table-hint">')
    // 容器必须真的包住 el-table，而不是只出现在注释里
    expect(/<div class="mobile-table-wrapper">[\s\S]*<el-table[\s\S]*<\/el-table>[\s\S]*<\/div>/.test(source)).toBe(true)
    // 操作列宽度收窄到能容纳"详情/删除"两个 2 字按钮（原 240px 在 390px 视口占 62%）
    expect(source).toContain('<el-table-column label="操作" width="120" fixed="right">')
    expect(source).not.toContain('<el-table-column label="操作" width="240" fixed="right">')
  })

  it('deletes through the danger confirm contract instead of a bare ElMessageBox', () => {
    // 危险确认（规范 §3.4.3 / §4.3.4 MUST）：删除节点必须带对象身份 + 不可逆影响，
    // 并走 feedback.confirmDanger（danger 确认键 + 焦点不落在破坏性按钮上）。
    expect(source).toContain('feedback.confirmDanger(')
    expect(source).toContain('此操作不可恢复')
    expect(source).toContain("confirmText: '删除'")
    expect(source).not.toContain('ElMessageBox')
    // 取消必须直接 return，不能继续执行删除
    expect(source).toContain('if (!confirmed) return')
  })

  it('derives summary stats and model options from cached nodes', () => {
    expect(source).toContain('const stats = reactive')
    expect(source).toContain('const modelOptions = computed')
    expect(source).toContain("stats.online = list.filter(c => c.status === 'online').length")
  })

  // ── 负债 I-11: 真分页行为 (真实请求参数, 不是源码契约) ──

  it('I-11: 挂载时带分页参数请求第 1 页', async () => {
    mountList()
    await flushPromises()
    expect(mockFetchNodes).toHaveBeenCalled()
    expect(mockFetchNodes.mock.calls[0][0]).toMatchObject({ page: 1, page_size: 20 })
  })

  it('I-11: 翻页发 page=2 并重新取数 (服务端分页, 不是本地切片)', async () => {
    const wrapper = mountList()
    await flushPromises()
    const vm = wrapper.vm as any
    mockFetchNodes.mockClear()

    vm.currentPage = 2
    await vm.fetchNodes()
    await flushPromises()

    expect(mockFetchNodes).toHaveBeenCalledTimes(1)
    expect(mockFetchNodes.mock.calls[0][0]).toMatchObject({ page: 2, page_size: 20 })
  })

  it('I-11: 渲染接口返回的当前页, 不再本地切片 (total 驱动分页器)', async () => {
    // 后端说 total=57 但本页只给 3 条 —— 本地切片会渲染出错误行数或错误页数。
    mockGetCachedList.mockReturnValue({
      items: [
        { id: 1, node_id: 'n1', name: 'Collector-A', model: 'ESP32', status: 'online' },
        { id: 2, node_id: 'n2', name: 'Collector-B', model: 'RPi4', status: 'offline' },
        { id: 3, node_id: 'n3', name: 'Collector-C', model: 'ESP32', status: 'online' },
      ],
      total: 57,
    })
    const wrapper = mountList()
    await flushPromises()
    const vm = wrapper.vm as any

    expect(vm.filteredNodes).toHaveLength(3)
    expect(vm.total).toBe(57)
  })

  it('I-11: 状态筛选重置 page=1 并作为服务端参数下发 (§3.2.6 MUST)', async () => {
    const wrapper = mountList()
    await flushPromises()
    const vm = wrapper.vm as any

    vm.currentPage = 3
    await vm.fetchNodes()
    await flushPromises()
    mockFetchNodes.mockClear()

    vm.statusFilter = 'online'
    await flushPromises()

    expect(vm.currentPage).toBe(1)
    // 改前 handleFilter 只重置 currentPage、**从不重新请求** —— 页码归 1 但数据是旧的。
    expect(mockFetchNodes.mock.calls.at(-1)![0]).toMatchObject({ page: 1, page_size: 20, status: 'online' })
  })

  it('I-11: 型号筛选重置页码并作为服务端 model 参数下发', async () => {
    const wrapper = mountList()
    await flushPromises()
    const vm = wrapper.vm as any

    vm.currentPage = 3
    await vm.fetchNodes()
    await flushPromises()
    mockFetchNodes.mockClear()

    vm.modelFilter = 'ESP32'
    await flushPromises()

    expect(vm.currentPage).toBe(1)
    expect(mockFetchNodes.mock.calls.at(-1)![0]).toMatchObject({ page: 1, model: 'ESP32' })
  })

  it('I-11: 搜索变化重置页码并作为服务端 search 参数下发', async () => {
    const wrapper = mountList()
    await flushPromises()
    const vm = wrapper.vm as any

    vm.currentPage = 2
    await vm.fetchNodes()
    await flushPromises()
    mockFetchNodes.mockClear()

    vm.searchKeyword = 'collector-a'
    await new Promise(resolve => setTimeout(resolve, 350))
    await flushPromises()

    expect(vm.currentPage).toBe(1)
    expect(mockFetchNodes.mock.calls.at(-1)![0]).toMatchObject({ page: 1, search: 'collector-a' })
  })

  it('I-11: 清空筛选只触发一次取数且不残留筛选参数 (watch 收口)', async () => {
    const wrapper = mountList()
    await flushPromises()
    const vm = wrapper.vm as any

    vm.statusFilter = 'online'
    vm.modelFilter = 'ESP32'
    vm.searchKeyword = 'collector'
    await new Promise(resolve => setTimeout(resolve, 350))
    await flushPromises()
    mockFetchNodes.mockClear()

    vm.clearFilters()
    await flushPromises()

    // 三个 ref 在同一 tick 内变更 → Vue 批处理成一次 watch 回调 → 一次取数。
    expect(mockFetchNodes).toHaveBeenCalledTimes(1)
    // 用 toEqual 钉死**恰好**只剩分页两项: toMatchObject 对多余字段视而不见,
    // 会漏掉"清空没清干净"。
    expect(mockFetchNodes.mock.calls[0][0]).toEqual({ page: 1, page_size: 20 })
  })

  it('I-11: 每页条数变化回到第 1 页 (原页在新页长下可能越界)', async () => {
    const wrapper = mountList()
    await flushPromises()
    const vm = wrapper.vm as any

    vm.currentPage = 5
    await vm.fetchNodes()
    await flushPromises()
    mockFetchNodes.mockClear()

    vm.pageSize = 50
    vm.handlePageSizeChange()
    await flushPromises()

    expect(vm.currentPage).toBe(1)
    expect(mockFetchNodes.mock.calls.at(-1)![0]).toMatchObject({ page: 1, page_size: 50 })
  })

  // ── A1: 「添加节点」死入口 (改前 push('/node?action=add'), 全仓无消费者) ──

  it('A1: 「添加节点」按钮打开本页对话框, 且不再 push ?action=add', async () => {
    const wrapper = mountList()
    await flushPromises()
    expect(wrapper.find('.el-dialog').exists()).toBe(false)

    const addButton = wrapper.findAll('button').find(b => b.text().includes('添加节点'))!
    expect(addButton, '工具栏必须有「添加节点」按钮').toBeTruthy()
    await addButton.trigger('click')

    const dialog = wrapper.find('.el-dialog')
    expect(dialog.exists()).toBe(true)
    expect(dialog.text()).toContain('添加节点')
    // 死入口的判据是"按钮点了什么都不发生": 既不能 push 无消费者的 query,
    // 也不能只弹一句 ElMessage.info 假跳转。
    expect(mockPush).not.toHaveBeenCalled()
    expect(mockInfo).not.toHaveBeenCalled()
  })

  it('A1: 带 ?action=add 挂载即打开对话框 (深链 query 现在真被消费)', async () => {
    mockRouteQuery.action = 'add'
    const wrapper = mountList()
    await flushPromises()
    expect(wrapper.find('.el-dialog').exists()).toBe(true)
    expect(wrapper.find('.el-dialog').text()).toContain('添加节点')
    expect(source).toContain("route.query.action === 'add'")
  })

  it('A1: node_id 非法格式时报错且 nodeApi.create 未被调用', async () => {
    const wrapper = mountList()
    await flushPromises()
    const vm = wrapper.vm as any

    vm.openAddDialog()
    await flushPromises()
    vm.addForm.node_id = 'zzz'
    await vm.submitAddNode()
    await flushPromises()

    expect(mockCreate).not.toHaveBeenCalled()
    const warned = mockWarning.mock.calls.map(c => String(c[0])).join('|')
    expect(warned).toContain('12 位十六进制')
    // 失败必须留在对话框里让用户改, 而不是像成功那样静默关闭。
    expect(wrapper.find('.el-dialog').exists()).toBe(true)
  })

  it('A1: 合法 node_id (去空格转大写) 才发请求, 成功后提示并关窗刷新', async () => {
    mockCreate.mockResolvedValueOnce({ id: 9, node_id: 'F0F5BD02F35C' })
    const wrapper = mountList()
    await flushPromises()
    const vm = wrapper.vm as any
    mockFetchNodes.mockClear()

    vm.openAddDialog()
    await flushPromises()
    vm.addForm.node_id = ' f0f5bd02f35c '
    vm.addForm.name = '新节点'
    await vm.submitAddNode()
    await flushPromises()

    expect(mockCreate).toHaveBeenCalledTimes(1)
    expect(mockCreate.mock.calls[0][0]).toEqual({ node_id: 'F0F5BD02F35C', name: '新节点' })
    expect(mockSuccess.mock.calls.map(c => String(c[0])).join('|')).toContain('节点已添加')
    expect(wrapper.find('.el-dialog').exists()).toBe(false)
    expect(mockFetchNodes).toHaveBeenCalled()
  })

  it('A1: 409 冲突翻成可读中文 (后端 message 是英文 node_id already exists)', async () => {
    mockCreate.mockRejectedValueOnce(Object.assign(new Error('node_id already exists'), { status: 409 }))
    const wrapper = mountList()
    await flushPromises()
    const vm = wrapper.vm as any

    vm.openAddDialog()
    await flushPromises()
    vm.addForm.node_id = 'F0F5BD02F35C'
    await vm.submitAddNode()
    await flushPromises()

    const shown = document.body.textContent || ''
    expect(shown).toContain('该 node_id 已存在')
    expect(shown).not.toContain('node_id already exists')
  })

  // ── A4: 「升级」死入口 (改前 push('/firmware?node=X'), FirmwareManage 不消费) ──

  it('A4: 「升级」按钮打开本页 OTA 对话框, 且不再 push /firmware', async () => {
    const wrapper = mountList()
    await flushPromises()

    const otaButton = wrapper.findAll('button').find(b => b.text().includes('升级'))!
    expect(otaButton, '卡片操作区必须有「升级」按钮').toBeTruthy()
    await otaButton.trigger('click')
    await flushPromises()

    const dialog = wrapper.find('.el-dialog')
    expect(dialog.exists()).toBe(true)
    expect(dialog.text()).toContain('OTA 固件升级')
    expect(mockPush).not.toHaveBeenCalled()
  })

  it('A4: OTA 对话框按 OTAForm 契约拿到该节点的 node_id / model / 当前版本', async () => {
    const wrapper = mountList()
    await flushPromises()

    const node = (wrapper.vm as any).filteredNodes[1]
    ;(wrapper.vm as any).handleQuickAction('ota', node)
    await flushPromises()

    const otaForm = wrapper.findComponent(OTAForm)
    expect(otaForm.exists()).toBe(true)
    expect(otaForm.props('visible')).toBe(true)
    expect(otaForm.props('collectorId')).toBe(node.node_id)
    expect(otaForm.props('collectorModel')).toBe(node.model)
    // config/tab 分支保持原样: 那条 query 由 NodeOverview 消费, 不是本轮死入口。
    expect(source).toContain("router.push(`/node/${node.node_id}?tab=config`)")
  })
})
