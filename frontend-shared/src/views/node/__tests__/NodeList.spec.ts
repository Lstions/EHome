import { describe, expect, it, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import NodeList from '@/views/node/NodeList.vue'
import source from '@/views/node/NodeList.vue?raw'
import type { NodeListParams } from '@/api/node'

// 显式标注 mock 签名: 无参 `vi.fn(() => ...)` 会把 `mock.calls` 推成 `[][]`
// (长度 0 的元组), 之后读 `calls[0][0]` 会报 TS2493。标注后 calls 是
// `[params?, force?][]`, 既能断言参数、也不需要任何 `as any`。
const { mockFetchNodes, mockGetCachedList } = vi.hoisted(() => ({
  mockFetchNodes: vi.fn<(params?: NodeListParams, force?: boolean) => Promise<void>>(() => Promise.resolve()),
  mockGetCachedList: vi.fn(() => ({
    items: [
      { id: 1, node_id: 'node-1', name: 'Collector-A', model: 'ESP32', status: 'online' },
      { id: 2, node_id: 'node-2', name: 'Collector-B', model: 'RPi4', status: 'offline' },
      { id: 3, node_id: 'node-3', name: 'Collector-C', model: 'ESP32', status: 'online' },
    ],
    total: 3,
  })),
}))
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn() }),
  useRoute: () => ({ query: {} }),
}))
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
})
