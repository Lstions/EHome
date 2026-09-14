import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { compileStyleAsync, parse } from '@vue/compiler-sfc'
import { readFileSync } from 'node:fs'
import { Window } from 'happy-dom'
import DeviceConfigList from '@/views/config/DeviceConfigList.vue'
import source from '@/views/config/DeviceConfigList.vue?raw'

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
  for (const file of ['src/views/config/DeviceConfigList.vue', 'src/components/common/PageHeader.vue']) {
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

const { mockGetList } = vi.hoisted(() => ({
  mockGetList: vi.fn(() => Promise.resolve({
    list: [
      {
        id: 1,
        name: 'UART 温湿度模板',
        description: '',
        device_type: 'temp_humidity',
        hardware_type: 'uart',
        config: {},
        is_default: false,
        status: 'active',
        created_at: '',
        updated_at: '',
      },
    ],
    total: 1,
    page: 1,
    page_size: 12,
  })),
}))

vi.mock('@/api/deviceConfig', () => ({
  deviceConfigApi: {
    getList: mockGetList,
    create: vi.fn(),
    update: vi.fn(),
    delete: vi.fn(),
    setDefault: vi.fn(),
  },
}))

vi.mock('element-plus', () => ({
  ElMessage: Object.assign(vi.fn(), { error: vi.fn(), success: vi.fn(), warning: vi.fn() }),
  ElMessageBox: { confirm: vi.fn() },
}))

const stubs = {
  DeviceConfigForm: true,
  EmptyState: true,
  'el-card': { template: '<section class="el-card"><slot /><slot name="header" /></section>' },
  'el-icon': { template: '<i><slot /></i>' },
  'el-input': { template: '<input />' },
  'el-select': { template: '<select><slot /></select>' },
  'el-option': true,
  'el-button': { template: '<button @click="$emit(\'click\')"><slot /></button>' },
  'el-tag': { template: '<span><slot /></span>' },
  'el-dropdown': { template: '<div><slot /><slot name="dropdown" /></div>' },
  'el-dropdown-menu': true,
  'el-dropdown-item': true,
  'el-pagination': true,
  'el-empty': true,
  'el-dialog': { template: '<div><slot /><slot name="footer" /></div>' },
}

describe('DeviceConfigList.vue', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('renders four statistic cards after loading template statistics', async () => {
    const wrapper = mount(DeviceConfigList, { global: { stubs } })
    await flushPromises()

    expect(mockGetList).toHaveBeenCalledWith({
      device_type: undefined,
      hardware_type: undefined,
      page: 1,
      page_size: 12,
    })
    expect(wrapper.findAll('.stats-row .stat-card')).toHaveLength(4)
    expect(wrapper.text()).toContain('模板总数')
    expect(wrapper.text()).toContain('本页启用')
  })

  // ─── I-6 页头统一 ─────────────────────────────────────────────────────────
  // PageHeader 由**真实组件**渲染（本地 stubs 不含 PageHeader，全局 test-setup 也未注册它），
  // 断言打在渲染结果 .page-header h2 的文本上，而非源码字符串。
  // 变异自证：摘掉 DeviceConfigList.vue 里的 <PageHeader> 后本用例必须变红。
  it('renders PageHeader as the page title', async () => {
    const wrapper = mount(DeviceConfigList, { global: { stubs } })
    await flushPromises()

    expect(wrapper.find('.page-header').exists()).toBe(true)
    expect(wrapper.find('.page-header h2').text()).toBe('配置模板')
    expect(wrapper.find('.page-header .page-header-subtitle').text())
      .toBe('为边缘设备复用连接、解析与初始化配置')

    // 页头必须在最前：标题不属于任何数据卡片，避免"标题长在统计卡/筛选卡里"
    const page = wrapper.find('.config-page')
    expect(page.element.firstElementChild?.classList.contains('page-header')).toBe(true)

    // 去重守卫：页内只有这一个页面级标题
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
    const root = mk('div', 'config-page', w.document.body)
    const header = mk('div', 'page-header', root)
    const left = mk('div', 'page-header-left', header)
    const box = mk('div', '', left)
    const h2 = mk('h2', '', box); h2.textContent = '配置模板'
    const sub = mk('p', 'page-header-subtitle', box); sub.textContent = '为边缘设备复用连接、解析与初始化配置'

    // 前置条件：390px 真的命中 <=768px 媒体查询，否则下面全是空断言
    expect(w.matchMedia('(max-width: 768px)').matches).toBe(true)

    const cs = (e: Element) => w.getComputedStyle(e)
    // PageHeader 自身窄屏规则生效（padding 从 16px 24px 收敛到 14px 16px，h2 24→18px）
    expect(cs(header).padding).toBe('14px 16px')
    expect(cs(h2).fontSize).toBe('18px')
    // 会撑破横排的三处 nowrap 都不存在（标题/副标题/左侧组均是默认 normal + 可收缩）
    expect(cs(h2).whiteSpace).not.toBe('nowrap')
    expect(cs(sub).whiteSpace).not.toBe('nowrap')
    expect(cs(h2).textOverflow).not.toBe('ellipsis')
    expect(cs(left).minWidth).not.toBe('max-content')
    // 副标题不得被压成不换行单行（否则 28 个汉字会直接顶破 390px 内容区）
    expect(cs(sub).whiteSpace).toBe('')
  })

  it('declares a four-column compact grid for mobile statistics', () => {
    expect(source).toContain('@media (max-width: 768px)')
    expect(source).toContain('.stats-row { grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 8px; }')
  })

  // 360px 实测: card__body 内容宽 310px，.filter-bar 可用 270px；三个动作按钮
  // 合计 261px + 2×8px gap = 277px > 270px。.filter-bar / .filter-left 均有 flex-wrap，
  // 缺了 .filter-right 时「导入」被推到内容区左界之外（x=9.95 < 21）且祖先链
  // scrollWidth === clientWidth（真实裁切，非可滚动溢出）。
  it('toolbar action group allows wrapping so no action is clipped on narrow viewports', () => {
    const start = source.indexOf('.filter-right {')
    expect(start).toBeGreaterThan(-1)
    const block = source.slice(start, source.indexOf('}', start) + 1)
    expect(block).toContain('flex-wrap: wrap;')
  })

  it('wrapping the toolbar actions does not drop any of the three actions', async () => {
    const wrapper = mount(DeviceConfigList, { global: { stubs } })
    await flushPromises()

    const actions = wrapper.find('.filter-right').findAll('button')
    expect(actions.map(button => button.text().replace(/\s+/g, ''))).toEqual(['导入', '导出', '新建模板'])
  })

  // ── 窄屏下卡片操作区换行（F9 同类遗漏的回归钉子） ─────────────────────────
  //
  // 根因：.card-footer 是 display:flex + justify-content:flex-end + gap:8px 且**无 flex-wrap**。
  // 360px 视口实测：.el-card__body 内容宽 270px，四个操作（预览 69.02 + 克隆 69.02 +
  // 编辑 69.02 + 下拉 39，含 item 间距共 294.05px）放不下，右对齐把「预览」推出左界：
  // x=16.95 < body.x=21（裁 4.05px）；而 body/card/grid/el-main 的
  // scrollWidth === clientWidth（不可回滚）⇒ **真实裁切**，不是可滚动溢出。
  // F9 只补了 .filter-right，这是同一根因漏掉的另一个元素。
  //
  // 为什么是样式契约而不是真实布局断言（规范允许的例外，须标注理由）：
  // happy-dom **无布局引擎** —— getBoundingClientRect() 恒为 0、scrollWidth/clientWidth
  // 恒为 0，写不出"按钮是否落在容器内"的行为断言。故此处退化为**样式契约**，
  // 但用 @vue/compiler-sfc 真实编译 scoped CSS 并注入 DOM 读 computed style
  // （而非字符串包含），真浏览器几何与可达性由 e2e 门禁覆盖。
  describe('窄屏下卡片操作区可换行（样式契约，happy-dom 无布局引擎的例外）', () => {
    it('.card-footer 自身声明 flex-wrap，按钮不再被推出卡片内容区', async () => {
      const css = await compiledScopedCss(source, 'DeviceConfigList.vue')
      const rule = css.match(/\.card-footer\[data-v-[a-z0-9]+\]\s*\{[^}]*\}/)
      expect(rule, 'DeviceConfigList.vue 缺少 .card-footer 的 scoped 规则').not.toBeNull()
      expect(rule![0]).toContain('flex-wrap: wrap')
      // DOM 级复核：注入编译产物后，同选择器命中的元素计算值必须真的是 wrap。
      // .card-footer 是**自身**选择器（不是后代），故 childClass 传 null 直接测元素自身。
      expect(measureFlexWrap(css, 'card-footer', null)).toBe('wrap')
      // 反证：class 不匹配时必须取不到该声明（证明测的是选择器而非恒真）
      expect(measureFlexWrap(css, 'not-the-container')).toBe('')
    })

    it('F9 的 .filter-right 契约不回退', async () => {
      const css = await compiledScopedCss(source, 'DeviceConfigList.vue')
      const rule = css.match(/\.filter-right\[data-v-[a-z0-9]+\]\s*\{[^}]*\}/)
      expect(rule, 'DeviceConfigList.vue 缺少 .filter-right 的 scoped 规则').not.toBeNull()
      expect(rule![0]).toContain('flex-wrap: wrap')
    })
  })

  /**
   * 危险确认契约（规范 §3.4.3 / §4.3.4）：删除配置模板不可恢复，
   * 确认键必须是 danger 语义类型 + danger class，且 autofocus:false；
   * 这里是「页面 → feedback.confirmDanger」的接线守卫。
   * confirmDanger 自身的真实渲染/焦点行为由 utils/__tests__/feedbackConfirmDanger.spec.ts 覆盖。
   */
  const dangerContract = expect.objectContaining({
    confirmButtonType: 'danger',
    confirmButtonClass: 'el-button--danger',
    autofocus: false,
  })

  it('删除配置走危险确认契约，且文案含对象身份', async () => {
    const { ElMessageBox } = await import('element-plus')
    const confirmSpy = vi.mocked(ElMessageBox.confirm)
    const wrapper = mount(DeviceConfigList, { global: { stubs } })
    await flushPromises()

    // 「删除」是 el-dropdown 的 command 项；stub 不触发 command 事件，
    // 故直接调用组件暴露的 handleMoreAction（与下拉触发同一入口）。
    const vm = wrapper.vm as unknown as { handleMoreAction: (c: string, cfg: unknown) => Promise<void> }
    await vm.handleMoreAction('delete', { id: 7, name: '温度模板' })
    await flushPromises()

    expect(confirmSpy).toHaveBeenCalledWith(
      expect.stringContaining('温度模板'),
      expect.any(String),
      dangerContract,
    )
  })

  it('删除配置取消时不调用 delete 接口', async () => {
    const { ElMessageBox } = await import('element-plus')
    const { deviceConfigApi } = await import('@/api/deviceConfig')
    vi.mocked(ElMessageBox.confirm).mockRejectedValueOnce(new Error('cancel'))
    const wrapper = mount(DeviceConfigList, { global: { stubs } })
    await flushPromises()

    const vm = wrapper.vm as unknown as { handleMoreAction: (c: string, cfg: unknown) => Promise<void> }
    await vm.handleMoreAction('delete', { id: 7, name: '温度模板' })
    await flushPromises()

    expect(vi.mocked(deviceConfigApi.delete)).not.toHaveBeenCalled()
  })

  it('删除配置确认后调用 delete 接口，失败时报「删除失败」', async () => {
    const { ElMessageBox } = await import('element-plus')
    const { deviceConfigApi } = await import('@/api/deviceConfig')
    const { ElMessage } = await import('element-plus')
    vi.mocked(ElMessageBox.confirm).mockResolvedValueOnce('confirm' as never)
    const wrapper = mount(DeviceConfigList, { global: { stubs } })
    await flushPromises()

    const vm = wrapper.vm as unknown as { handleMoreAction: (c: string, cfg: unknown) => Promise<void> }
    await vm.handleMoreAction('delete', { id: 7, name: '温度模板' })
    await flushPromises()
    expect(vi.mocked(deviceConfigApi.delete)).toHaveBeenCalledWith(7)

    // 接口失败分支：迁移后不再靠 error !== 'cancel' 判断，仍须提示失败
    vi.mocked(deviceConfigApi.delete).mockRejectedValueOnce(new Error('boom'))
    await vm.handleMoreAction('delete', { id: 8, name: '湿度模板' })
    await flushPromises()
    // I-1: 删除失败走 feedback.error → ElMessage({message, type:'error', duration:5000})
    expect(ElMessage).toHaveBeenCalledWith(expect.objectContaining({
      message: '删除失败',
      type: 'error',
      duration: 5000,
    }))
  })
})
