import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { compileStyleAsync, parse } from '@vue/compiler-sfc'
import { readFileSync } from 'node:fs'
import { Window } from 'happy-dom'
import LogicalDeviceList from '@/views/logical-device/LogicalDeviceList.vue'
import source from '@/views/logical-device/LogicalDeviceList.vue?raw'

/**
 * 样式契约辅助：用 @vue/compiler-sfc 真实编译 <style scoped>（复现构建期的
 * [data-v-*] 选择器改写），再注入 DOM 读 getComputedStyle —— 比字符串包含更接近
 * 浏览器实际会应用的声明。真实几何/可达性由 e2e 门禁在真浏览器里验收。
 */
async function compiledScopedCss(raw: string, filename: string): Promise<string> {
  const { descriptor } = parse(raw, { filename })
  const chunks: string[] = []
  for (const block of descriptor.styles) {
    const res = await compileStyleAsync({
      source: block.content,
      filename,
      id: 'data-v-testscope',
      scoped: Boolean(block.scoped),
    })
    expect(res.errors, JSON.stringify(res.errors)).toEqual([])
    chunks.push(res.code)
  }
  return chunks.join('\n')
}

/**
 * 注入编译产物 + 合成同结构 DOM（<div class="parentClass" data-v-testscope><div class="el-pagination">），
 * 返回内层元素的计算 flex-wrap。
 *
 * happy-dom 实测：只有在元素**插入文档后**读 getComputedStyle 才会做选择器匹配；
 * 若用"回调构造 DOM"或"插入前先读一次"，返回的是空串（不会命中已注入的样式表）。
 * 因此这里固定 DOM 形状、固定读取顺序。反证：把 parentClass 换成不匹配的值时
 * 返回值必须是空串 —— 说明该函数真的在走选择器匹配而非恒返回 wrap。
 */
function measureFlexWrap(css: string, parentClass: string, childClass: string | null = 'el-pagination'): string {
  const style = document.createElement('style')
  style.textContent = css
  document.head.appendChild(style)
  const wrap = document.createElement('div')
  wrap.className = parentClass
  wrap.setAttribute('data-v-testscope', '')
  const target = childClass === null ? wrap : document.createElement('div')
  if (childClass !== null) {
    target.className = childClass
    wrap.appendChild(target)
  }
  document.body.appendChild(wrap)
  const value = getComputedStyle(target).flexWrap
  style.remove()
  wrap.remove()
  return value
}

/**
 * 390px 窄屏页头契约（**真实级联**，非源码字符串包含）。
 *
 * happy-dom 有 CSS 级联，但 @media 只在**新建** Window 里按 innerWidth 求值
 * （当前测试窗口改 innerWidth 不会重算，实测 1024→390 后 matchMedia 仍为 false）。
 * 故这里开一个 390px Window，注入 @vue/compiler-sfc 真实编译后的 scoped CSS。
 *
 * 关键：本页与 PageHeader 是**两个** SFC，各自 scope id 不同。测试里用同一个合成 id
 * 编译两边（'data-v-narrow'），并把该 id 挂到所有合成节点上 —— 这样 PageHeader 自身的
 * 窄屏规则与页面侧的 :deep() 覆盖**都**会命中，等价于运行时 PageHeader 根节点继承
 * 父组件 scope id 的真实行为（Vue 3 scoped 规则）。
 *
 * 为什么不断言"像素没溢出"：happy-dom **无布局引擎**，getBoundingClientRect/scrollWidth
 * 恒为 0（仓内既有约定，见本文件上方 measureFlexWrap 的说明）。所以这里断言的是
 * "390px 下页头不会横向撑破"的**级联事实**；真像素几何由 e2e 门禁覆盖。
 */
async function narrowPageHeaderCss(): Promise<string> {
  const id = 'data-v-narrow'
  const chunks: string[] = []
  for (const file of ['src/views/logical-device/LogicalDeviceList.vue', 'src/components/common/PageHeader.vue']) {
    const { descriptor } = parse(readFileSync(file, 'utf8'), { filename: file })
    for (const block of descriptor.styles) {
      const res = await compileStyleAsync({
        source: block.content,
        filename: file,
        id,
        scoped: Boolean(block.scoped),
      })
      expect(res.errors, JSON.stringify(res.errors)).toEqual([])
      chunks.push(res.code)
    }
  }
  return chunks.join('\n')
}


/**
 * happy-dom 的 Window 自带**另一套** DOM 类型（happy-dom/lib/nodes/...），与 tsconfig 的
 * lib.dom 不是同一套：直接把它的节点传给 getComputedStyle/appendChild 会触发 TS2345
 * （两边各有私有成员，互不兼容）。这里做**一次**收窄到标准 DOM 类型的外观，下游全部用
 * 标准类型 —— 不需要 any，也不必在每个调用点撒 as。
 */
interface NarrowWindow {
  innerWidth: number
  document: Document
  getComputedStyle: (el: Element) => CSSStyleDeclaration
  matchMedia: (query: string) => { matches: boolean }
}

function newNarrowWindow(width: number): NarrowWindow {
  const w = new Window({ width, height: 844 }) as unknown as NarrowWindow
  w.innerWidth = width
  return w
}

// ── Mocks ──────────────────────────────────────────────

const { mockPush, mockRoute } = vi.hoisted(() => ({
  mockPush: vi.fn(),
  mockRoute: { path: '/logical-device', query: {} as Record<string, string> },
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({ push: mockPush }),
  useRoute: () => mockRoute,
}))

vi.mock('element-plus', () => ({
  ElMessage: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}))

// 保留真实 extractMergeConflicts (结构化 409 解析), 仅 mock API 方法。
vi.mock('@/api/logicalDevice', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/logicalDevice')>()
  return {
    ...actual,
    logicalDeviceApi: {
      list: vi.fn(),
      mergePreview: vi.fn(),
      merge: vi.fn(),
      mergeJob: vi.fn(),
      update: vi.fn(),
    },
  }
})

import { logicalDeviceApi } from '@/api/logicalDevice'
import { ElMessage } from 'element-plus'

const mockList = vi.mocked(logicalDeviceApi.list)
const mockPreview = vi.mocked(logicalDeviceApi.mergePreview)
const mockMerge = vi.mocked(logicalDeviceApi.merge)
const mockMergeJob = vi.mocked(logicalDeviceApi.mergeJob)
const mockUpdate = vi.mocked(logicalDeviceApi.update)

// ── Fixtures ───────────────────────────────────────────

const makeItem = (over: Partial<any> = {}) => ({
  id: 3,
  identity_key: 'bms:0x76',
  name: '客厅BMS',
  device_type: 'bms',
  retention_days: 365,
  merged_into: null,
  merge_status: null,
  purge_requested: false,
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-01T00:00:00Z',
  instance_count: 1,
  row_estimate: 12847,
  last_data_at: '2026-08-04T08:00:00Z',
  ...over,
})

/**
 * 取 list() 最后一次调用的实参。
 * 用索引而非 Array.prototype.at —— tsconfig 的 lib 未含 ES2022。
 */
function lastListParams(): Record<string, unknown> {
  const calls = mockList.mock.calls
  return calls[calls.length - 1][0] as Record<string, unknown>
}

const mountPage = () =>
  mount(LogicalDeviceList, {
    global: {
      stubs: {
        // 图标无交互语义, stub 掉避免解析告警
        Connection: true,
        Refresh: true,
        InfoFilled: true,
        WarningFilled: true,
      },
    },
  })


// ─── F8 移动端宽表横滚合同（§4.3.2.2 MUST / §4.4.1 MUST） ───────────────────
// 断言**真实渲染出的 DOM 祖先链**，不是源码字符串包含。
// happy-dom 无布局引擎（getBoundingClientRect 恒 0），像素级可达性由真浏览器
// 探针验收：frontend-shared/.tmp-probe/f8-f10-probe.mjs
function expectEveryTableWrapped(wrapper: { findAll: (s: string) => Array<{ element: Element }> }) {
  const tables = wrapper.findAll('.el-table')
  expect(tables.length, '渲染出的 el-table 数量为 0，断言会假绿').toBeGreaterThan(0)
  for (const t of tables) {
    const el = t.element as HTMLElement
    const box = el.closest('.mobile-table-wrapper')
    expect(box, 'el-table 不在 .mobile-table-wrapper 祖先链上').not.toBeNull()
    const hint = (box as HTMLElement).querySelector(':scope > .mobile-table-hint')
    expect(hint, '.mobile-table-wrapper 缺少直接子节点 .mobile-table-hint').not.toBeNull()
    expect(hint!.textContent).toContain('左右滑动')
  }
}

// ─── I-6 页头统一 ─────────────────────────────────────────────────────────
// PageHeader 由**真实组件**渲染（mountPage 的 stubs 只 stub 了图标），断言打在渲染结果
// .page-header h2 的文本上，而非源码字符串。
// 变异自证：摘掉 LogicalDeviceList.vue 里的 <PageHeader> 后本用例必须变红。
describe('LogicalDeviceList.vue 页头（I-6）', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockRoute.query = {}
    mockList.mockResolvedValue({ items: [], total: 0 })
  })

  it('renders PageHeader as the page title', async () => {
    const wrapper = mountPage()
    await flushPromises()

    expect(wrapper.find('.page-header').exists()).toBe(true)
    expect(wrapper.find('.page-header h2').text()).toBe('逻辑设备')
    expect(wrapper.find('.page-header .page-header-subtitle').text())
      .toBe('聚合边缘设备数据，实现统一视图和数据保留策略管理')

    // 页头必须在最前，且页内只有这一个页面级标题（不叠在工具栏卡片里）
    const page = wrapper.find('.logical-device-page')
    expect(page.element.firstElementChild?.classList.contains('page-header')).toBe(true)
    expect(wrapper.findAll('.page-header')).toHaveLength(1)
  })

  it('PageHeader 在 390px 下不撑破横向：标题换行、副标题折行、无 nowrap', async () => {
    const css = await narrowPageHeaderCss()
    const w = newNarrowWindow(390)
    const style = w.document.createElement('style')
    style.textContent = css
    w.document.head.appendChild(style)

    const id = 'data-v-narrow'
    const mk = (tag: string, cls: string, parent: Element) => {
      const el = w.document.createElement(tag)
      if (cls) el.className = cls
      el.setAttribute(id, '')
      parent.appendChild(el)
      return el
    }
    const root = mk('div', 'logical-device-page', w.document.body)
    const header = mk('div', 'page-header', root)
    const left = mk('div', 'page-header-left', header)
    const box = mk('div', '', left)
    const h2 = mk('h2', '', box); h2.textContent = '逻辑设备'
    const sub = mk('p', 'page-header-subtitle', box); sub.textContent = '聚合边缘设备数据，实现统一视图和数据保留策略管理'

    // 前置条件：390px 真的命中 <=768px 媒体查询，否则下面全是空断言
    expect(w.matchMedia('(max-width: 768px)').matches).toBe(true)

    const cs = (e: Element) => w.getComputedStyle(e)
    expect(cs(header).padding).toBe('14px 16px')
    expect(cs(h2).fontSize).toBe('18px')
    // 副标题 22 个汉字：必须能折行，且不得用省略号截断（页面级标题不应靠 ... 藏信息）
    expect(cs(sub).whiteSpace).toBe('')
    expect(cs(sub).textOverflow).not.toBe('ellipsis')
    expect(cs(h2).whiteSpace).not.toBe('nowrap')
    // 标题区可收缩，不被右侧内容挤出（本页无 #extra，但契约一并钉住）
    expect(cs(left).minWidth).not.toBe('max-content')
  })

  it('合并/刷新仍留在工具栏卡片内，未被搬进页头（列表操作 ≠ 页级操作）', async () => {
    const wrapper = mountPage()
    await flushPromises()

    const toolbar = wrapper.find('.toolbar-card')
    expect(toolbar.find('.filter-right button').exists()).toBe(true)
    expect(wrapper.find('.page-header .filter-right').exists()).toBe(false)
  })
})

describe('LogicalDeviceList.vue', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockRoute.query = {}

    mockList.mockResolvedValue({ items: [], total: 0 })
    mockPreview.mockResolvedValue({ sources: [], target_retention_days: 365 })
    mockMerge.mockResolvedValue({ target_id: 9, job_ids: [11] })
    mockMergeJob.mockResolvedValue({
      id: 11,
      source_logical_id: 3,
      target_logical_id: 9,
      status: 'done',
      migrated_rows: 100,
      total_estimate: 100,
      watermark_id: 0,
      watermark_phase: 'unified_data',
      retry_count: 0,
      created_at: '',
      updated_at: '',
      finished_at: null,
    })
    mockUpdate.mockImplementation(async (_id, updates) => makeItem(updates))
  })

  // ─── F8 移动端宽表横滚合同 ───

  it('F8：列表宽表渲染在 .mobile-table-wrapper 内并带横滑提示（§4.3.2.2 MUST）', async () => {
    mockList.mockResolvedValue({ items: [makeItem()], total: 1 })
    const wrapper = mountPage()
    await flushPromises()
    expectEveryTableWrapped(wrapper)
  })

  it('F8：合并预览弹窗内的每源明细表也被包裹（弹窗 720px > 390px 视口）', async () => {
    mockList.mockResolvedValue({
      items: [makeItem({ id: 3, name: 'A', device_type: 'bms' }), makeItem({ id: 5, name: 'B', device_type: 'bms' })],
      total: 2,
    })
    mockPreview.mockResolvedValue({
      sources: [{ id: 3, name: 'A', device_type: 'bms', first_data_at: null, last_data_at: null, row_estimate: 1000, overlap_with_others: false }],
      target_retention_days: 365,
    })

    const wrapper = mountPage()
    await flushPromises()
    // 选中两行 -> 点「合并所选」打开预览弹窗（与既有用例同一条路径）
    const checkboxes = wrapper.findAll('input.el-table__row-checkbox')
    await checkboxes[0].setValue(true)
    await checkboxes[1].setValue(true)
    await wrapper.findAll('button').find(b => b.text().includes('合并所选'))!.trigger('click')
    await flushPromises()

    // 前提校验：预览弹窗内的明细表必须真的渲染出来了，否则本用例会假绿
    expect(wrapper.find('.preview-table').exists(), '预览弹窗未渲染 .preview-table，用例前提不成立').toBe(true)
    expectEveryTableWrapped(wrapper)
    // 本页此时应有 2 个 wrapper：列表宽表 + 弹窗内每源明细表
    expect(wrapper.findAll('.mobile-table-wrapper')).toHaveLength(2)
  })

  // ─── 列表渲染 ───

  it('renders list items with name/type/instance count/retention', async () => {
    mockList.mockResolvedValue({
      items: [makeItem(), makeItem({ id: 5, name: '卧室BMS', instance_count: 2 })],
      total: 2,
    })
    const wrapper = mountPage()
    await flushPromises()

    expect(mockList).toHaveBeenCalledTimes(1)
    const text = wrapper.text()
    expect(text).toContain('客厅BMS')
    expect(text).toContain('卧室BMS')
    // 表格行内合并值含实例数/保留天数/数据量
    expect(text).toContain('365')
  })

  it('marks merged/pending/purge devices with status tags (源码约定)', async () => {
    // el-table stub 不渲染 scoped slot, 状态标签模板用源码断言锁定。
    const src = (await import('@/views/logical-device/LogicalDeviceList.vue?raw')).default
    expect(src).toContain("row.merge_status === 'pending'")
    expect(src).toContain("row.merge_status === 'done'")
    expect(src).toContain('row.purge_requested')
    expect(src).toContain('已合并 →')
  })

  it('filters by search keyword', async () => {
    // useDebouncedSearch 有 300ms 防抖。**本用例已随分页改造更新**: 搜索不再是
    // 当前页本地过滤 (§3.3.5 禁止把本地筛选伪装成全局检索), 而是防抖后带
    // search 参数重新请求服务端, 并把页码重置为 1。
    vi.useFakeTimers()
    try {
      mockList.mockResolvedValue({
        items: [makeItem({ id: 3, name: '客厅BMS' }), makeItem({ id: 5, name: '卧室BMS' })],
        total: 2,
      })
      const wrapper = mountPage()
      await flushPromises()
      const callsBefore = mockList.mock.calls.length

      const search = wrapper.find('input[placeholder="搜索逻辑设备名称..."]')
      expect(search.exists()).toBe(true)
      await search.setValue('卧室')
      await flushPromises()

      // 防抖未到: 尚未发起新的服务端查询
      expect(mockList.mock.calls.length).toBe(callsBefore)

      await vi.advanceTimersByTimeAsync(300)
      await flushPromises()

      // 防抖到点: 以 search 参数重新请求, 且页码重置为 1
      expect(mockList.mock.calls.length).toBe(callsBefore + 1)
      const params = lastListParams()
      expect(params.search).toBe('卧室')
      expect(params.page).toBe(1)
    } finally {
      vi.useRealTimers()
    }
  })

  // ─── 分页与「筛选变化重置页码」 (§3.2.6 MUST) ───
  // 断言真实请求参数, 不是源码字符串。

  it('首次加载按默认页码请求 (page=1, page_size=20)', async () => {
    mountPage()
    await flushPromises()
    const params = mockList.mock.calls[0][0] as Record<string, unknown>
    expect(params.page).toBe(1)
    expect(params.page_size).toBe(20)
  })

  it('翻到第 2 页后请求带 page=2', async () => {
    mockList.mockResolvedValue({ items: [makeItem()], total: 50 })
    const wrapper = mountPage()
    await flushPromises()

    await wrapper.find('[data-test="ld-pagination"]').trigger('click')
    await flushPromises()

    const params = lastListParams()
    expect(params.page).toBe(2)
  })

  it('设备类型筛选变化后请求的 page 参数被重置为 1', async () => {
    mockList.mockResolvedValue({ items: [makeItem()], total: 50 })
    const wrapper = mountPage()
    await flushPromises()

    // 先翻到第 3 页, 制造「页码非 1」的前置状态
    await wrapper.find('[data-test="ld-pagination"]').trigger('click')
    await flushPromises()
    await wrapper.find('[data-test="ld-pagination"]').trigger('click')
    await flushPromises()
    expect((lastListParams()).page).toBe(3)

    // 换设备类型 → 必须以 page=1 重新查询。
    // 取值必须来自 deviceTypeOptions (utils/deviceType.ts) 的真实项, 否则 el-select
    // 的 stub 会因 value 不匹配而回落到空值, 断言就失去意义。
    await wrapper.find('select.filter-select').setValue('jiabaida_bms')
    await flushPromises()

    const params = lastListParams()
    expect(params.page).toBe(1)
    expect(params.device_type).toBe('jiabaida_bms')
  })

  it('total 来自接口响应而非当前页长度 (分页器算总页数用)', async () => {
    mockList.mockResolvedValue({ items: [makeItem()], total: 1003 })
    const wrapper = mountPage()
    await flushPromises()
    expect(wrapper.find('[data-test="ld-pagination"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="ld-pagination"]').text()).toContain('共 1003 条')
    // 只渲染当前页 1 行, 不是 1003 行
    expect(wrapper.findAll('tbody tr')).toHaveLength(1)
  })

  // ── 分页控件窄屏可换行（§4.3.2 MUST：分页换行是独立验收项） ────────────────
  //
  // 根因（不是"外层缺 flex-wrap"）：.ld-pagination 早已有 flex-wrap，但 flex 换行只发生在
  // 多个 item **之间**，管不到**单个过宽 item 的内部**：el-pagination 自带
  // white-space:nowrap + display:flex，实测宽 676.94px（layout="total, sizes, prev,
  // pager, next, jumper"）。
  //   360px：容器 .ld-pagination 宽 312px（x=20），el-pagination x=-344.94、
  //          btn-prev x=-118、el-pagination__total x=-344.94；
  //   390px：el-pagination x=-314.94、btn-prev x=-88；
  //   768px：el-pagination x=63.06（容器 x=220，裁 157px）。
  // 且 .ld-pagination / .mobile-table-wrapper / .el-main 的 scrollWidth === clientWidth
  // （横向位移预算 0）⇒ **真实裁切**；elementFromPoint 在 btn-prev 中心返回 null，
  // **上一页按钮永久不可达**（不是"看起来挤"）。
  // 修法照同仓范式 firmware/FirmwareManage.vue 的 .firmware-manage :deep(.el-pagination)。
  //
  // 为什么是样式契约而非真实布局断言（规范允许的例外，须标注理由）：
  // happy-dom **无布局引擎**，getBoundingClientRect() 恒 0、scrollWidth/clientWidth 恒 0，
  // "控件是否溢出容器"不可在单测里量化。故此处用 @vue/compiler-sfc **真实编译**
  // scoped 样式并读 computed style（断言浏览器实际会应用的声明），真浏览器几何与
  // 可达性由 e2e 门禁（frontend-shared/e2e）验收。
  describe('分页控件窄屏换行（样式契约，happy-dom 无布局引擎的例外）', () => {
    it('内层 .el-pagination 自身声明 flex-wrap，过宽时才会拆行', async () => {
      const css = await compiledScopedCss(source, 'LogicalDeviceList.vue')
      const rule = css.match(/\.ld-pagination\[data-v-[a-z0-9]+\]\s+\.el-pagination\s*\{[^}]*\}/)
      expect(rule, 'LogicalDeviceList.vue 缺少 .ld-pagination :deep(.el-pagination) 编译产物').not.toBeNull()
      expect(rule![0]).toContain('flex-wrap: wrap')
      // 外层必须同时保留 wrap：两者防的是不同层级（item 之间 / item 内部）
      const outer = css.match(/\.ld-pagination\[data-v-[a-z0-9]+\]\s*\{[^}]*\}/)
      expect(outer, 'LogicalDeviceList.vue 缺少 .ld-pagination 自身的 scoped 规则').not.toBeNull()
      expect(outer![0]).toContain('flex-wrap: wrap')
      // DOM 级复核：把编译产物注入后，内层选择器命中的元素计算值必须真的是 wrap
      expect(measureFlexWrap(css, 'ld-pagination')).toBe('wrap')
      // 反证：容器 class 不匹配时必须取不到该声明（证明测的是选择器而非恒真）
      expect(measureFlexWrap(css, 'not-the-container')).toBe('')
    })

    it('分页控件仍在页面上渲染（换行不得把分页器整个藏掉）', async () => {
      mockList.mockResolvedValue({ items: [makeItem()], total: 1003 })
      const wrapper = mountPage()
      await flushPromises()
      expect(wrapper.find('[data-test="ld-pagination"]').exists()).toBe(true)
    })
  })

  // ─── 合并门控 ───

  it('disables merge button until 2+ same-type sources selected', async () => {
    mockList.mockResolvedValue({
      items: [
        makeItem({ id: 3, name: 'A', device_type: 'bms' }),
        makeItem({ id: 5, name: 'B', device_type: 'bms' }),
        makeItem({ id: 7, name: 'C', device_type: 'sn3001' }),
      ],
      total: 3,
    })
    const wrapper = mountPage()
    await flushPromises()

    const mergeButton = () =>
      wrapper.findAll('button').find(b => b.text().includes('合并所选'))!
    const checkboxes = wrapper.findAll('input.el-table__row-checkbox')
    expect(checkboxes).toHaveLength(3)

    // 0 选: 禁用
    expect(mergeButton().attributes('disabled')).toBeDefined()

    // 1 选: 仍禁用
    await checkboxes[0].setValue(true)
    expect(mergeButton().attributes('disabled')).toBeDefined()

    // 2 个同 type: 可用
    await checkboxes[1].setValue(true)
    expect(mergeButton().attributes('disabled')).toBeUndefined()

    // 换成跨 type 组合 (A + C): 再次禁用
    await checkboxes[1].setValue(false)
    await checkboxes[2].setValue(true)
    expect(mergeButton().attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain('所选逻辑设备必须属于同一设备类型')
  })

  // ─── 预览 + 确认合并 ───

  it('opens preview with time ranges/overlap/target retention, then confirms merge', async () => {
    mockList.mockResolvedValue({
      items: [
        makeItem({ id: 3, name: 'A', device_type: 'bms' }),
        makeItem({ id: 5, name: 'B', device_type: 'bms' }),
      ],
      total: 2,
    })
    mockPreview.mockResolvedValue({
      sources: [
        {
          id: 3, name: 'A', device_type: 'bms',
          first_data_at: '2026-07-01T00:00:00Z', last_data_at: '2026-07-20T00:00:00Z',
          row_estimate: 1000, overlap_with_others: true,
        },
        {
          id: 5, name: 'B', device_type: 'bms',
          first_data_at: '2026-07-15T00:00:00Z', last_data_at: '2026-08-01T00:00:00Z',
          row_estimate: 2000, overlap_with_others: true,
        },
      ],
      target_retention_days: 365,
    })

    const wrapper = mountPage()
    await flushPromises()

    const checkboxes = wrapper.findAll('input.el-table__row-checkbox')
    await checkboxes[0].setValue(true)
    await checkboxes[1].setValue(true)

    const mergeButton = wrapper.findAll('button').find(b => b.text().includes('合并所选'))!
    await mergeButton.trigger('click')
    await flushPromises()

    expect(mockPreview).toHaveBeenCalledWith([3, 5])
    const dialogText = wrapper.find('.el-dialog').text()
    expect(dialogText).toContain('A')
    expect(dialogText).toContain('B')
    expect(dialogText).toContain('重叠')
    expect(dialogText).toContain('365')
    // 数据量合计 = 1000 + 2000
    expect(dialogText).toContain('3.0k')

    // 目标名称默认拼接源名, 可编辑
    const targetInput = wrapper.find('input[data-testid="merge-target-name"]')
    expect((targetInput.element as HTMLInputElement).value).toBe('A + B')
    await targetInput.setValue('合并后的BMS')

    const confirm = wrapper.find('button[data-testid="merge-confirm"]')
    await confirm.trigger('click')
    await flushPromises()

    expect(mockMerge).toHaveBeenCalledWith('合并后的BMS', [3, 5])
    expect(ElMessage.success).toHaveBeenCalledWith('合并已发起，后台正在搬迁数据')
    // 进度弹窗打开; mergeJob mock 返回 done → 首轮轮询即收敛并刷新列表
    await flushPromises()
    expect(mockMergeJob).toHaveBeenCalled()
  })

  // ─── 409 conflicts ───

  it('renders 409 conflicts item-by-item with instance jump', async () => {
    mockList.mockResolvedValue({
      items: [
        makeItem({ id: 3, name: 'A', device_type: 'bms' }),
        makeItem({ id: 5, name: 'B', device_type: 'bms' }),
      ],
      total: 2,
    })
    mockMerge.mockRejectedValue({
      status: 409,
      message: '合并校验未通过',
      response: {
        data: {
          code: 409,
          message: '合并校验未通过',
          conflicts: [
            {
              logical_device_id: 3, logical_name: 'A', reason: 'alive_instance',
              instance_id: 17, instance_name: 'BMS-1', node_name: '客厅采集器',
            },
            { logical_device_id: 5, logical_name: 'B', reason: 'purge_requested' },
          ],
        },
      },
    })

    const wrapper = mountPage()
    await flushPromises()

    const checkboxes = wrapper.findAll('input.el-table__row-checkbox')
    await checkboxes[0].setValue(true)
    await checkboxes[1].setValue(true)
    await wrapper.findAll('button').find(b => b.text().includes('合并所选'))!.trigger('click')
    await flushPromises()
    // 预览弹窗 → 确认
    await wrapper.find('button[data-testid="merge-confirm"]').trigger('click')
    await flushPromises()

    const conflictDialog = wrapper.findAll('.el-dialog').find(d => d.text().includes('合并被拒绝'))!
    expect(conflictDialog.text()).toContain('仍有存活实例')
    expect(conflictDialog.text()).toContain('BMS-1')
    expect(conflictDialog.text()).toContain('客厅采集器')
    expect(conflictDialog.text()).toContain('已标记删除数据')

    // 实例跳转
    const jump = conflictDialog.find('button[data-testid="conflict-jump"]')
    expect(jump.exists()).toBe(true)
    await jump.trigger('click')
    expect(mockPush).toHaveBeenCalledWith('/edge-device/17')
  })

  // ─── 编辑 (retention 深链打开) ───

  it('opens edit dialog via ?retention=<id> deep link and saves name/retention', async () => {
    mockList.mockResolvedValue({
      items: [makeItem({ id: 7, name: '待延期设备', retention_days: 30 })],
      total: 1,
    })
    mockRoute.query = { retention: '7' }

    const wrapper = mountPage()
    await flushPromises()

    // 深链自动打开编辑弹窗; 名称回显在 input value 上 (不在 textContent 中)
    const dialog = wrapper.findAll('.el-dialog').find(d => d.text().includes('编辑逻辑设备'))!
    expect(dialog.exists()).toBe(true)

    const nameInput = wrapper.find('input[data-testid="edit-name"]')
    expect((nameInput.element as HTMLInputElement).value).toBe('待延期设备')
    await nameInput.setValue('延期后的设备')
    const retentionInput = wrapper.find('input[data-testid="edit-retention"]')
    await retentionInput.setValue('730')

    await wrapper.find('button[data-testid="edit-save"]').trigger('click')
    await flushPromises()

    expect(mockUpdate).toHaveBeenCalledWith(7, { name: '延期后的设备', retention_days: 730 })
    expect(ElMessage.success).toHaveBeenCalledWith('已保存')
  })

  it('rejects empty name on save', async () => {
    mockList.mockResolvedValue({ items: [makeItem({ id: 7 })], total: 1 })
    mockRoute.query = { retention: '7' }

    const wrapper = mountPage()
    await flushPromises()

    await wrapper.find('input[data-testid="edit-name"]').setValue('   ')
    await wrapper.find('button[data-testid="edit-save"]').trigger('click')
    await flushPromises()

    expect(mockUpdate).not.toHaveBeenCalled()
    expect(ElMessage.warning).toHaveBeenCalledWith('名称不能为空')
  })

  // ─── 进度轮询 ───

  it('shows failed job status and warning when migration fails', async () => {
    mockList.mockResolvedValue({
      items: [
        makeItem({ id: 3, name: 'A', device_type: 'bms' }),
        makeItem({ id: 5, name: 'B', device_type: 'bms' }),
      ],
      total: 2,
    })
    mockMerge.mockResolvedValue({ target_id: 9, job_ids: [11, 12] })
    mockMergeJob.mockImplementation(async (id) => ({
      id,
      source_logical_id: id === 11 ? 3 : 5,
      target_logical_id: 9,
      status: id === 11 ? 'done' : 'failed',
      migrated_rows: 100,
      total_estimate: 100,
      watermark_id: 0,
      watermark_phase: 'unified_data',
      retry_count: id === 12 ? 3 : 0,
      created_at: '',
      updated_at: '',
      finished_at: null,
    }) as any)

    const wrapper = mountPage()
    await flushPromises()

    const checkboxes = wrapper.findAll('input.el-table__row-checkbox')
    await checkboxes[0].setValue(true)
    await checkboxes[1].setValue(true)
    await wrapper.findAll('button').find(b => b.text().includes('合并所选'))!.trigger('click')
    await flushPromises()
    await wrapper.find('button[data-testid="merge-confirm"]').trigger('click')
    await flushPromises()

    expect(ElMessage.warning).toHaveBeenCalledWith(expect.stringContaining('1 个任务失败'))
    const progressDialog = wrapper.findAll('.el-dialog').find(d => d.text().includes('数据搬迁进度'))!
    expect(progressDialog.text()).toContain('完成')
    expect(progressDialog.text()).toContain('失败')
    expect(progressDialog.text()).toContain('重试 3 次')
  })
})
