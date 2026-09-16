import { describe, it, expect, beforeEach, vi } from 'vitest'
import { defineComponent, h } from 'vue'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import ChannelList from '@/views/channel/ChannelList.vue'
import channelListSource from '@/views/channel/ChannelList.vue?raw'
// EmptyState 使用真实实现：它的 props 契约已支持 kind="error"，
// 失败态用例必须验证真实契约被接线，而不是被 stub 吞掉。
import RealEmptyState from '@/components/common/EmptyState.vue'
// 页长上界常量由 vi.mock('@/api/channel') 的工厂一并导出（值同真实实现）。
import { CHANNEL_LIST_MAX_PAGE_SIZE, NODE_FILTER_MAX_PAGE_SIZE } from '@/api/channel'

vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn() }),
}))

// 只给"真实 getPage 包装器"用例用：组件本身走的是被 mock 的 channelApi，
// 但新加的服务端分页参数契约必须在**真实包装器**上验一遍（否则它一行测试都没有）。
const { mockClientGet } = vi.hoisted(() => ({ mockClientGet: vi.fn() }))
vi.mock('@/api/client', () => ({
  default: { get: mockClientGet, post: vi.fn(), put: vi.fn(), delete: vi.fn() },
}))

// ============================================================================
// 真实形态 fixture（缺陷①②③的行为测试基础）
//
//   · node_id 是**物理序列号字符串**（后端 json tag 是 string，生产库形如 'F0F5BDFFFE02'），
//     不是 nodes 表的自增主键。旧断言用 node_id: 1 这种数字 fixture，恰好绕开了
//     "string === number 恒 false" 这个真实缺陷。
//   · hardware_type 是**大写枚举**（生产库实测只有 UART/I2C/SPI…），旧 fixture 用小写，
//     恰好绕开了"大小写敏感比较恒 false"。
//
// mockGetPage 实现了**服务端语义**：按 node_id / hardware_type 过滤 + 分页 + 回 total，
// 所以"选中筛选后请求参数错 / 行数错"都会被行为断言抓住，而不是只检查源码里有没有某行字。
// ============================================================================
const { mockGetPage, serverListImpl, mockScan, mockGetCachedList, NODES } = vi.hoisted(() => {
  const FIXTURE = [
    { id: 1, node_id: 'F0F5BDFFFE02', name: 'I2C0_0x77', hardware_type: 'I2C', hardware_id: 'I2C0', address: '0x77', enabled: true, config: '{}' },
    { id: 2, node_id: 'F0F5BDFFFE02', name: 'UART0', hardware_type: 'UART', hardware_id: 'UART0', enabled: true, bus_config: '1415000012C0', config: '{}' },
    { id: 3, node_id: 'A1B2C3D4E5F6', name: 'SPI0_CS0', hardware_type: 'SPI', hardware_id: 'SPI0', enabled: false, config: '{}' },
    { id: 4, node_id: 'A1B2C3D4E5F6', name: 'UART1', hardware_type: 'UART', hardware_id: 'UART1', enabled: true, bus_config: '1415000012C0', config: '{}' },
  ]
  const nodes = [
    { id: 1, node_id: 'F0F5BDFFFE02', name: 'Collector-A', status: 'online' },
    { id: 2, node_id: 'A1B2C3D4E5F6', name: 'Collector-B', status: 'offline' },
  ]
  /** 服务端语义的列表实现：node_id 精确匹配、hardware_type 大小写不敏感、真分页 + total。 */
  const serverListImpl = (query?: any): Promise<{ items: any[]; total: number; page: number; page_size: number }> => {
    const q = query && typeof query === 'object' ? query : {}
    let items = FIXTURE.filter((ch) => {
      if (q.node_id !== undefined && q.node_id !== '' && String(ch.node_id) !== String(q.node_id)) return false
      if (q.hardware_type !== undefined && q.hardware_type !== '' &&
        String(ch.hardware_type).toLowerCase() !== String(q.hardware_type).toLowerCase()) return false
      return true
    })
    const total = items.length
    const page = q.page ?? 1
    const pageSize = q.page_size ?? 20
    items = items.slice((page - 1) * pageSize, page * pageSize)
    return Promise.resolve({ items, total, page, page_size: pageSize })
  }
  const mockGetPage = vi.fn(serverListImpl)
  return {
    mockGetPage,
    serverListImpl,
    mockScan: vi.fn(() => Promise.resolve({ channel_id: 1, devices: ['0x48', '0x49'] })),
    mockGetCachedList: vi.fn(() => ({ items: nodes, total: nodes.length })),
    NODES: nodes,
  }
})

vi.mock('@/api/channel', () => ({
  // 这两个常量是页面传给 getList 的页长上界（服务端分页后必须显式下发，
  // 否则 >20 条会被后端默认页长静默截断）。mock 必须提供同名导出，否则
  // 组件导入即抛 "No ... export is defined on the mock"。
  CHANNEL_LIST_MAX_PAGE_SIZE: 200,
  NODE_FILTER_MAX_PAGE_SIZE: 200,
  channelApi: {
    // 页面走服务端分页入口 getPage；getList 一并给出以保持 mock 与真实 channelApi 形状一致
    // （getList = "某节点的全部通道"，见 api/channel.ts 的注释，本页已不再使用）。
    getList: mockGetPage,
    getPage: mockGetPage,
    scan: mockScan,
  },
}))

vi.mock('@/stores/node', () => ({
  useNodeStore: () => ({
    fetchNodes: vi.fn(() => Promise.resolve()),
    getCachedList: mockGetCachedList,
    nodes: NODES,
    total: NODES.length,
    loading: false,
  }),
}))

// ============================================================================
// 行渲染 stub：默认的 el-table stub 不展开列的**作用域插槽**（只把 row 的字段拼成
// 一行文本），因此"操作列有没有地址扫描按钮""节点名称列显示谁"这类真实行为无法断言。
// 这里用最小实现展开 { row } 作用域插槽 —— 断的是渲染结果，不是源码字符串。
// ============================================================================
const TableStub = defineComponent({
  props: { data: { type: Array, default: () => [] } },
  setup(props, { slots }) {
    return () => {
      const raw = slots.default?.() ?? []
      const cols = (Array.isArray(raw) ? raw : [raw]) as any[]
      return h('table', { class: 'el-table' }, [
        h('tbody', (props.data as any[]).map((row: any, i: number) =>
          h('tr', { key: row?.id ?? i }, cols.map((col: any, ci: number) => {
            const cell = col?.children?.default
            // 与真实 el-table 同语义：有作用域插槽走插槽，否则渲染 row[prop]
            // （ChannelList 的 ID / 硬件ID 列就是 prop 列；不渲染它们就没法按硬件ID 定位行）
            if (typeof cell === 'function') return h('td', { class: 'el-table__cell', key: ci }, cell({ row }))
            const prop = col?.props?.prop
            return h('td', { class: 'el-table__cell', key: ci }, prop ? String(row?.[prop] ?? '') : '')
          })),
        )),
      ])
    }
  },
})

// 仅替换项目内展示组件；其余 Element Plus 交互组件使用全局 test-setup stub。
const stubs = {
  PageHeader: { template: '<div data-testid="page-header"><slot /><slot name="extra" /></div>' },
  SkeletonCard: { template: '<div data-testid="skeleton-card" />' },
  EmptyState: { template: '<div data-testid="empty-state" />' },
  RouterLink: { template: '<a><slot /></a>' },
  ElTable: TableStub,
}

function mountList() {
  return mount(ChannelList, { global: { stubs } })
}

function rowsOf(wrapper: any) {
  return wrapper.findAll('tbody tr')
}

function rowTexts(wrapper: any): string[] {
  return rowsOf(wrapper).map((r: any) => r.text())
}

function rowByText(wrapper: any, text: string) {
  const row = rowsOf(wrapper).find((r: any) => r.text().includes(text))
  if (!row) throw new Error('未找到包含 "' + text + '" 的数据行；实际行=' + JSON.stringify(rowTexts(wrapper)))
  return row
}

/** 最近一次 /channels 请求的第一个实参（服务端分页后必须是 {page,page_size,...} 查询对象）。 */
function lastListParams(): any {
  const calls = mockGetPage.mock.calls
  return calls.length > 0 ? (calls[calls.length - 1] as any[])[0] : undefined
}

function selectAt(wrapper: any, index: number) {
  return wrapper.findAll('select.el-select')[index]
}

describe('ChannelList.vue', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    // clearAllMocks 只清调用记录、不清实现；上一条用例若改过 getCachedList 的返回值，
    // 会串到下一条（截断提示用例就是这样互相污染的）。这里显式把实现复位。
    vi.clearAllMocks()
    // clearAllMocks 只清调用记录、不清实现：任一用例改过 mockImplementation 都会串到下一条。
    mockGetPage.mockImplementation(serverListImpl)
    mockGetCachedList.mockReturnValue({ items: NODES, total: NODES.length })
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
    expect(mockGetPage).toHaveBeenCalled()
  })

  // ── 服务端分页契约（替代"本地切片 + 全量下发"的旧契约）──────────────────────
  it('首屏按服务端分页请求第 1 页（page/page_size 必须显式下发，否则退回默认页长）', async () => {
    mountList()
    await flushPromises()
    const params = lastListParams()
    expect(params, '首屏请求必须带 {page,page_size} 查询对象，实际=' + JSON.stringify(params)).toBeTruthy()
    expect(params.page).toBe(1)
    expect(params.page_size).toBe(20)
    // 不得再出现"要全量 200 条再本地切片"的旧契约
    expect(params.page_size).not.toBe(CHANNEL_LIST_MAX_PAGE_SIZE)
  })

  it('分页器绑定服务端 total，且当前页数据不做本地切片', async () => {
    mockGetPage.mockResolvedValueOnce({ items: [
      { id: 1, node_id: 'F0F5BDFFFE02', name: 'I2C0_0x77', hardware_type: 'I2C', hardware_id: 'I2C0', config: '{}' },
      { id: 2, node_id: 'F0F5BDFFFE02', name: 'UART0', hardware_type: 'UART', hardware_id: 'UART0', config: '{}' },
      { id: 3, node_id: 'A1B2C3D4E5F6', name: 'SPI0_CS0', hardware_type: 'SPI', hardware_id: 'SPI0', config: '{}' },
    ], total: 57, page: 1, page_size: 20 })

    const wrapper = mountList()
    await flushPromises()

    // 3 行必须全部渲染（本地切片会按 pageSize 再切一次，行数会与后端页长脱节）
    expect(rowsOf(wrapper)).toHaveLength(3)
    // 分页器显示的"共 N 条"必须是服务端 total(57)，不是本页行数(3)
    expect(wrapper.find('.el-pagination').text()).toContain('57')
    expect(wrapper.find('.el-pagination').text()).not.toContain('共 3 条')
  })

  it('翻页重新请求第 2 页（不是本地切片）', async () => {
    const wrapper = mountList()
    await flushPromises()
    expect(rowsOf(wrapper)).toHaveLength(4)

    await wrapper.find('.el-pagination').trigger('click')
    await flushPromises()

    expect(lastListParams()).toMatchObject({ page: 2, page_size: 20 })
  })

  // ── 缺陷①：节点筛选（string 序列号 vs number 主键）────────────────────────
  it('缺陷① 节点下拉的 value 是物理序列号，不是自增主键', async () => {
    const wrapper = mountList()
    await flushPromises()

    const values = selectAt(wrapper, 0).findAll('option').map((o: any) => o.attributes('value'))
    expect(values, '节点下拉的可选值=' + JSON.stringify(values)).toContain('F0F5BDFFFE02')
    expect(values).toContain('A1B2C3D4E5F6')
    // 主键 number 作为 value ⇒ 与 ch.node_id(string) 恒不相等，缺陷①
    expect(values).not.toContain('1')
    expect(values).not.toContain('2')
  })

  it('缺陷① 选中节点后按序列号下发 node_id，且只渲染该节点的通道', async () => {
    const wrapper = mountList()
    await flushPromises()
    expect(rowsOf(wrapper)).toHaveLength(4)

    await selectAt(wrapper, 0).setValue('F0F5BDFFFE02')
    await flushPromises()

    const params = lastListParams()
    expect(params, '筛选后必须重新请求服务端，实际 params=' + JSON.stringify(params)).toBeTruthy()
    expect(params.node_id).toBe('F0F5BDFFFE02')
    expect(params.page).toBe(1)
    // 服务端只回了该节点的 2 条
    expect(rowsOf(wrapper)).toHaveLength(2)
    expect(rowTexts(wrapper).join('|')).toContain('I2C0')
    expect(rowTexts(wrapper).join('|')).toContain('UART0')
    expect(rowTexts(wrapper).join('|')).not.toContain('SPI0')
  })

  it('节点名映射按 node_id(序列号) 索引，而不是 nodes 表主键', async () => {
    const wrapper = mountList()
    await flushPromises()
    // 通道行里的节点名应显示真实名称（按主键索引时这里会退化成 "节点 #F0F5BDFFFE02"）
    expect(rowTexts(wrapper).join('|')).toContain('Collector-A')
    expect(wrapper.text()).not.toContain('节点 #F0F5BDFFFE02')
  })

  // ── 缺陷②：硬件类型筛选（小写下拉值 vs 大写存储）──────────────────────────
  it('缺陷② 选中 uart 后按服务端 hardware_type 过滤，只渲染大写存储的 UART 通道', async () => {
    const wrapper = mountList()
    await flushPromises()
    expect(rowsOf(wrapper)).toHaveLength(4)

    await selectAt(wrapper, 1).setValue('uart')
    await flushPromises()

    const params = lastListParams()
    expect(params, '筛选后必须重新请求服务端，实际 params=' + JSON.stringify(params)).toBeTruthy()
    expect(params.hardware_type).toBe('uart')
    expect(params.page).toBe(1)
    expect(rowsOf(wrapper)).toHaveLength(2)
    expect(rowTexts(wrapper).join('|')).toContain('UART0')
    expect(rowTexts(wrapper).join('|')).toContain('UART1')
    expect(rowTexts(wrapper).join('|')).not.toContain('I2C0')
  })

  it('筛选变化必须把页码重置回第 1 页', async () => {
    const wrapper = mountList()
    await flushPromises()
    // 翻到第 2 页（先由服务端分页产生 page=2 的请求）
    await wrapper.find('.el-pagination').trigger('click')
    await flushPromises()
    // 本地分页实现下不会重新请求 ⇒ calls 仍为 1 ⇒ 该断言即为主控探针的"筛选恒返回 0 行"根因
    expect(mockGetPage.mock.calls.length, '翻页未重新请求服务端（本地切片）').toBe(2)
    expect(lastListParams()).toMatchObject({ page: 2 })

    await selectAt(wrapper, 1).setValue('uart')
    await flushPromises()

    expect(lastListParams()).toMatchObject({ page: 1, hardware_type: 'uart' })
  })

  // ── 缺陷③：扫描入口的大小写敏感判断（真实大写数据下按钮永不出现）──────────
  it('缺陷③ 大写 I2C/UART 行仍渲染"地址扫描"入口，SPI 行不渲染', async () => {
    const wrapper = mountList()
    await flushPromises()

    const i2cRow = rowByText(wrapper, 'I2C0')
    expect(i2cRow.text(), 'I2C 行应有地址扫描按钮').toContain('地址扫描')

    const uartRow = rowByText(wrapper, 'UART0')
    expect(uartRow.text(), 'UART(with bus_config) 行应有地址扫描按钮').toContain('地址扫描')

    const spiRow = rowByText(wrapper, 'SPI0')
    expect(spiRow.text(), 'SPI 行不该有地址扫描按钮').not.toContain('地址扫描')

    // 缺陷③ 效果层口径（主控探针）：大写 UART 数据下可扫描行数必须是 3（2×UART + 1×I2C），
    // 大小写敏感判断时会退化成 0 —— 按钮永不出现。
    const scannableRows = rowsOf(wrapper).filter((r: any) => r.text().includes('地址扫描'))
    expect(scannableRows, '可扫描行数（期望 3，大小写敏感时为 0）').toHaveLength(3)
  })

  it('缺陷③ I2C 行点击扫描下发 scan_type=i2c（大写数据下不得退化成 modbus）', async () => {
    const wrapper = mountList()
    await flushPromises()

    const scanButton = rowByText(wrapper, 'I2C0').findAll('button').find((b: any) => b.text().includes('地址扫描'))
    expect(scanButton, '未找到 I2C 行的地址扫描按钮').toBeTruthy()
    await scanButton!.trigger('click')
    await flushPromises()

    expect(mockScan).toHaveBeenCalledWith(1, { scan_type: 'i2c' })
  })

  it('缺陷③ UART 行点击扫描下发 scan_type=modbus', async () => {
    const wrapper = mountList()
    await flushPromises()

    const scanButton = rowByText(wrapper, 'UART0').findAll('button').find((b: any) => b.text().includes('地址扫描'))
    expect(scanButton, '未找到 UART 行的地址扫描按钮').toBeTruthy()
    await scanButton!.trigger('click')
    await flushPromises()

    expect(mockScan).toHaveBeenCalledWith(2, { scan_type: 'modbus' })
  })

  // ── 关键词：客户端 + 只作用当前页，必须有「本页」范围词（规范 §4.3 MUST）──
  // ==========================================================================
  // 关键词与分页的一致性（主控探针抓到的真缺陷）
  //
  // 缺陷形态：第 2 页输入关键词后，handleSearch 只做 currentPage=1（程序化改绑定值
  // **不会**触发 el-pagination 的 current-change，EP 只在分页器内部 setter 里 emit），
  // 于是 channels 仍是第 2 页的响应，而分页器与范围提示已显示"第 1 页" ——
  // 范围提示说得越确定，这个不一致越有害（伪造的领域事实）。
  //
  // 断言必须拿**实际渲染的行**对账，不能只断言 currentPage === 1：
  // 后者在缺陷下也成立（这正是主控探针抓到、而旧用例漏掉的原因）。
  // ==========================================================================
  it('在第 2 页输入关键词后，表格实际渲染的必须是第 1 页数据（页码↔数据↔范围提示三者一致）', async () => {
    mockGetPage.mockImplementation((query?: any) => {
      const page = query?.page ?? 1
      // 每页数据可区分：PAGE1-ROW / PAGE2-ROW（主控探针同款 fixture）
      const items = page === 2
        ? [{ id: 21, node_id: 'F0F5BDFFFE02', name: 'PAGE2-ROW', hardware_type: 'UART', hardware_id: 'UART-2', config: '{}' }]
        : [{ id: 1, node_id: 'F0F5BDFFFE02', name: 'PAGE1-ROW', hardware_type: 'UART', hardware_id: 'UART-1', config: '{}' }]
      return Promise.resolve({ items, total: 40, page, page_size: 20 })
    })

    const wrapper = mountList()
    await flushPromises()
    expect(rowTexts(wrapper).join('|')).toContain('UART-1')

    // 翻到第 2 页（真请求 page=2）
    await wrapper.find('.el-pagination').trigger('click')
    await flushPromises()
    expect(lastListParams()).toMatchObject({ page: 2 })
    expect(rowTexts(wrapper).join('|'), '第 2 页应渲染 PAGE2 的数据').toContain('UART-2')

    // 输入关键词
    await wrapper.find('input[placeholder="搜索通道名称、硬件ID..."]').setValue('UART')
    await flushPromises()

    // ① 必须重新请求第 1 页（不是"改了页码却不取数"）
    expect(lastListParams(), '输入关键词后必须回到第 1 页并**重新取数**').toMatchObject({ page: 1 })
    // ② 表格实际渲染的必须是第 1 页的行（PAGE1-ROW 在这一页的有界 key 是 UART-1）
    expect(
      rowTexts(wrapper).join('|'),
      '输入关键词后表格仍是第 2 页数据 —— 页码已显示第 1 页，这是伪造的领域事实',
    ).toContain('UART-1')
    expect(rowTexts(wrapper).join('|')).not.toContain('UART-2')
    // ③ 范围提示里的页码必须与实际内容一致
    expect(wrapper.find('[data-test="channel-search-scope"]').text()).toContain('第 1 页')
  })

  it('乱序响应不得覆盖新页数据（翻页途中改筛选的同类风险）', async () => {
    // 先让首屏正常返回（分页器 v-if="total > 0"，没有 total 就点不到分页器），
    // 之后把「下一次 page=1 的请求」挂起 ⇒ 它会晚于 page=2 的响应到达。
    let holdNextPage1 = false
    // 用对象属性持有 release：闭包里赋值 + 外部调用，用裸 let 会被 TS 的控制流分析判成 never
    const held: { release?: () => void } = {}
    mockGetPage.mockImplementation((query?: any) => {
      const page = query?.page ?? 1
      const payload = {
        items: [{ id: page * 10, node_id: 'F0F5BDFFFE02', name: 'P' + page, hardware_type: 'UART', hardware_id: 'HW-' + page, config: '{}' }],
        total: 40, page, page_size: 20,
      }
      if (page === 1 && holdNextPage1) {
        holdNextPage1 = false
        return new Promise(resolve => { held.release = () => resolve(payload) })
      }
      return Promise.resolve(payload)
    })

    const wrapper = mountList()
    await flushPromises()
    expect(rowTexts(wrapper).join('|')).toContain('HW-1')

    await wrapper.find('.el-pagination').trigger('click')
    await flushPromises()
    expect(rowTexts(wrapper).join('|')).toContain('HW-2')

    // 从第 2 页输入关键词 → 触发第 1 页请求（挂起，成为"先发后到"的过期响应）
    holdNextPage1 = true
    // 关键词用 'HW'（对两页的 hardware_id 都命中），否则客户端过滤会把行全滤掉、
    // 断言就分不清"数据没跟上"与"被关键词滤掉"了
    await wrapper.find('input[placeholder="搜索通道名称、硬件ID..."]').setValue('HW')
    await flushPromises()
    // 关键词请求还在飞 → 再翻一页，触发更新的请求
    await wrapper.find('.el-pagination').trigger('click')
    await flushPromises()
    expect(rowTexts(wrapper).join('|')).toContain('HW-2')

    // 放行**过期**的第 1 页响应：它必须被丢弃，不得把表格覆盖回旧数据
    held.release?.()
    await flushPromises()

    expect(lastListParams()).toMatchObject({ page: 2 })
    expect(
      rowTexts(wrapper).join('|'),
      '过期响应覆盖了新页数据 —— 页码显示第 2 页、内容却是第 1 页（伪造的领域事实）',
    ).toContain('HW-2')
    expect(rowTexts(wrapper).join('|')).not.toContain('HW-1')
  })

  it('关键词只过滤当前页，不发请求，且界面标注范围为「本页」', async () => {
    // 用持久实现（不是 Once）：输入关键词会再请求一次第 1 页，Once 会让第二次落到默认 fixture 上
    mockGetPage.mockResolvedValue({ items: [
      { id: 1, node_id: 'F0F5BDFFFE02', name: 'I2C0_0x77', hardware_type: 'I2C', hardware_id: 'I2C0', config: '{}' },
      { id: 2, node_id: 'F0F5BDFFFE02', name: 'UART0', hardware_type: 'UART', hardware_id: 'UART0', config: '{}' },
      { id: 3, node_id: 'A1B2C3D4E5F6', name: 'SPI0_CS0', hardware_type: 'SPI', hardware_id: 'SPI0', config: '{}' },
    ], total: 57, page: 1, page_size: 20 })

    const wrapper = mountList()
    await flushPromises()
    expect(mockGetPage).toHaveBeenCalledTimes(1)

    // 范围词必须常驻可见（不是只在空态里出现）
    const scopeHint = wrapper.find('[data-test="channel-search-scope"]')
    expect(scopeHint.exists(), '缺少关键词范围提示元素 data-test="channel-search-scope"').toBe(true)
    expect(scopeHint.text()).toContain('本页')

    await wrapper.find('input[placeholder="搜索通道名称、硬件ID..."]').setValue('UART')
    await flushPromises()

    // 关键词**不下沉服务端**（请求参数里不得出现 search/关键词），但会回到第 1 页重新取数
    const params = lastListParams()
    expect(params).toMatchObject({ page: 1 })
    // 关键词**不下沉服务端**：请求里不得出现任何关键词字段（分页/筛选字段另算）
    expect(Object.keys(params)).not.toContain('search')
    expect(params.keyword).toBeUndefined()
    expect(params.hardware_type).toBeUndefined()
    expect(params.node_id).toBeUndefined()
    // 过滤仍然发生在客户端：本页 3 条里只有 UART0 命中
    expect(rowsOf(wrapper)).toHaveLength(1)
    expect(rowTexts(wrapper).join('|')).toContain('UART0')
  })

  it('关键词在本页无匹配但全库有数据时，空态必须说清是「本页」无匹配', async () => {
    mockGetPage.mockResolvedValue({ items: [
      { id: 3, node_id: 'A1B2C3D4E5F6', name: 'SPI0_CS0', hardware_type: 'SPI', hardware_id: 'SPI0', config: '{}' },
    ], total: 57, page: 2, page_size: 20 })

    const wrapper = mount(ChannelList, { global: { stubs: { ...stubs, EmptyState: RealEmptyState } } })
    await flushPromises()

    await wrapper.find('input[placeholder="搜索通道名称、硬件ID..."]').setValue('I2C0_0x77')
    await flushPromises()

    expect(rowsOf(wrapper)).toHaveLength(0)
    const empties = wrapper.findAllComponents(RealEmptyState)
    expect(empties).toHaveLength(1)
    expect(empties[0].props('kind')).toBe('filtered')
    expect(String(empties[0].props('title'))).toContain('本页')
    expect(String(empties[0].props('description'))).toContain('本页')
    // 不得把「本页没搜到」说成「全库没有」这种伪造的领域事实
    expect(String(empties[0].props('description'))).not.toContain('没有任何通道')
  })

  // ── 节点下拉静默截断：必须显式提示，不得只列前 N 个 ──────────────────────
  it('节点总数超过下拉页长上界时显式提示已截断（不得静默）', async () => {
    const many = Array.from({ length: NODE_FILTER_MAX_PAGE_SIZE }, (_, i) => ({ id: i + 1, node_id: 'SN-' + (i + 1), name: '节点' + (i + 1), status: 'online' }))
    // 页面每次 refreshData 都会重新取一次缓存（筛选变化会再次 refreshData），
    // 所以用 mockReturnValue 而不是 Once，避免断言被"取了几次"这种实现细节绊倒。
    mockGetCachedList.mockReturnValue({ items: many, total: 418 })

    const wrapper = mountList()
    await flushPromises()

    const hint = wrapper.find('[data-test="channel-node-truncated"]')
    expect(hint.exists(), '缺少节点下拉截断提示元素 data-test="channel-node-truncated"').toBe(true)
    expect(hint.text()).toContain('200')
    expect(hint.text()).toContain('418')
    // 下拉本身仍必须请求页长上界（否则连前 N 个都拿不满）
    expect(mockGetCachedList).toHaveBeenCalled()
  })

  it('节点数未截断时不显示截断提示（提示不得常驻）', async () => {
    const wrapper = mountList()
    await flushPromises()
    expect(wrapper.find('[data-test="channel-node-truncated"]').exists()).toBe(false)
  })

  it('routes visible node-detail actions through the NodeDetail route', () => {
    expect(channelListSource).toContain("router.push({ name: 'NodeDetail', params: { id: row.node_id } })")
  })

  it('maps node names and hardware labels in the component contract', () => {
    expect(channelListSource).toContain('return node?.name || \`节点 #\${nodeId}\`')
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
// 真实 channelApi.getPage 包装器（服务端分页 + 服务端筛选的参数契约）
//
// 上面的组件用例把 @/api/channel 整个 mock 掉了，所以**真实包装器**必须单独验：
// 否则"页面传了 params 但包装器没下发"这类断链会完全无人看守。
// ============================================================================
describe('channelApi.getPage — 服务端分页/筛选参数契约（真实包装器）', () => {
  beforeEach(() => vi.clearAllMocks())

  it('page/page_size/node_id/hardware_type 全部原样下发，并回传 total 与后端回显页码', async () => {
    const { channelApi: realApi } = await vi.importActual<typeof import('@/api/channel')>('@/api/channel')
    mockClientGet.mockResolvedValueOnce({ code: 200, data: { items: [{ id: 1 }], total: 57, page: 2, page_size: 20 } })

    const res = await realApi.getPage({ page: 2, page_size: 20, node_id: 'F0F5BDFFFE02', hardware_type: 'uart' })

    expect(mockClientGet).toHaveBeenCalledWith('/api/v1/channels', {
      params: { page: 2, page_size: 20, node_id: 'F0F5BDFFFE02', hardware_type: 'uart' },
    })
    expect(res).toEqual({ items: [{ id: 1 }], total: 57, page: 2, page_size: 20 })
  })

  it('未筛选时不下发 node_id/hardware_type（空串不得变成 "筛了空值"）', async () => {
    const { channelApi: realApi } = await vi.importActual<typeof import('@/api/channel')>('@/api/channel')
    mockClientGet.mockResolvedValueOnce({ code: 200, data: { items: [], total: 0, page: 1, page_size: 20 } })

    await realApi.getPage({ page: 1, page_size: 20, node_id: '', hardware_type: '' })

    const callArgs = mockClientGet.mock.calls[0][1] as { params: Record<string, unknown> }
    expect(Object.keys(callArgs.params).sort()).toEqual(['page', 'page_size'])
  })

  it('业务错误码必须抛错，不得把"接口失败"解成空列表（否则页面会用空态冒充"确实没有"）', async () => {
    const { channelApi: realApi } = await vi.importActual<typeof import('@/api/channel')>('@/api/channel')
    mockClientGet.mockResolvedValueOnce({ code: 500, data: null })
    await expect(realApi.getPage({ page: 1, page_size: 20 })).rejects.toThrow('获取通道列表失败')
  })

  it('旧后端裸数组：如实按"只有一页"处理，不伪造 total', async () => {
    const { channelApi: realApi } = await vi.importActual<typeof import('@/api/channel')>('@/api/channel')
    mockClientGet.mockResolvedValueOnce({ code: 200, data: [{ id: 1, node_id: 'F0F5BDFFFE02', hardware_type: 'UART', hardware_id: 'UART0', config: '{}' }] })
    const res = await realApi.getPage({ page: 1, page_size: 20 })
    expect(res.items).toHaveLength(1)
    expect(res.total).toBe(1)
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

describe('ChannelList.vue — 接口失败态与空态语义', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    localStorage.clear()
    sessionStorage.clear()
  })

  it('接口 500 时渲染常驻错误态 + 重试入口，且不出现「暂无通道」式空态结论', async () => {
    mockGetPage.mockRejectedValueOnce(new Error('Request failed with status code 500'))

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
    mockGetPage.mockRejectedValueOnce(new Error('boom'))

    const wrapper = mountWithRealEmptyState()
    await flushPromises()
    expect(wrapper.find('[data-test="channel-error"]').exists()).toBe(true)

    mockGetPage.mockResolvedValueOnce({
      items: [{ id: 9, node_id: 'F0F5BDFFFE02', name: 'RETRY-OK', hardware_type: 'I2C', hardware_id: 'I2C9', address: '0x09', enabled: true, config: '{}' }],
      total: 1, page: 1, page_size: 20,
    })
    await wrapper.find('[data-test="channel-retry"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-test="channel-error"]').exists()).toBe(false)
    expect(wrapper.findAllComponents(RealEmptyState).map(e => e.props('kind'))).not.toContain('error')
    expect(wrapper.text()).toContain('RETRY-OK')
    expect(mockGetPage).toHaveBeenCalledTimes(2)
  })

  it('加载成功但全库确实为空时是空态（initial），不是错误态；分页器不渲染', async () => {
    mockGetPage.mockResolvedValueOnce({ items: [], total: 0, page: 1, page_size: 20 })

    const wrapper = mountWithRealEmptyState()
    await flushPromises()

    expect(wrapper.find('[data-test="channel-error"]').exists()).toBe(false)
    const empties = wrapper.findAllComponents(RealEmptyState)
    expect(empties).toHaveLength(1)
    expect(empties[0].props('kind')).toBe('initial')
    expect(empties[0].props('title')).toBe('暂无通道')
    expect(wrapper.find('.el-pagination').exists()).toBe(false)
  })

  it('服务端筛选后全库无匹配：kind=filtered，且结论范围是整库筛选（不是「本页」）', async () => {
    const wrapper = mountWithRealEmptyState()
    await flushPromises()
    expect(wrapper.text()).toContain('I2C0_0x77')

    // 硬件类型筛选（第 2 个 select）切到 adc —— fixture 里没有 adc 通道，
    // mock 按服务端语义返回 {items: [], total: 0}
    await selectAt(wrapper, 1).setValue('adc')
    await flushPromises()

    const empties = wrapper.findAllComponents(RealEmptyState)
    expect(empties).toHaveLength(1)
    expect(empties[0].props('kind')).toBe('filtered')
    expect(empties[0].props('title')).toBe('没有匹配的通道')
    expect(wrapper.find('[data-test="channel-error"]').exists()).toBe(false)
  })
})
