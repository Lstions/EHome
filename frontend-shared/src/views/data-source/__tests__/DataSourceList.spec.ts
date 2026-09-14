import { describe, it, expect, vi, beforeEach } from 'vitest'
import { defineComponent, h, type VNodeChild } from 'vue'
import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { compileStyleAsync, parse } from '@vue/compiler-sfc'
import DataSourceList from '../DataSourceList.vue'
import source from '../DataSourceList.vue?raw'
import { dataSourceApi, type DataSource, type DataSourceListParams } from '@/api/dataSource'
import { logicalDeviceApi, type LogicalDeviceItem } from '@/api/logicalDevice'

// Element Plus 组件由 src/test-setup.ts 全局 stub；这里 mock 数据层（与 AlertRules.spec.ts 同法）。
vi.mock('@/api/dataSource', () => ({
  dataSourceApi: {
    list: vi.fn(),
    get: vi.fn(),
    create: vi.fn(),
    update: vi.fn(),
    remove: vi.fn(),
    activate: vi.fn(),
    deactivate: vi.fn(),
    reset: vi.fn(),
    getHealth: vi.fn(),
    getFailoverLogs: vi.fn(),
  },
}))
vi.mock('@/api/logicalDevice', () => ({
  logicalDeviceApi: {
    list: vi.fn(),
  },
}))
// useResponsive 的视口宽度是模块级共享 ref：直接驱动它模拟窄屏，无需真实 resize。
const { viewportWidth, isMobileRef } = vi.hoisted(() => {
  const { ref } = require('vue')
  return { viewportWidth: ref(1440), isMobileRef: ref(false) }
})
vi.mock('@/composables/useResponsive', () => ({
  useResponsive: () => ({
    width: viewportWidth,
    isMobile: isMobileRef,
    isTablet: { value: false },
    isDesktop: { value: true },
  }),
}))

vi.mock('element-plus', async importOriginal => {
  const actual = await importOriginal<typeof import('element-plus')>()
  return {
    ...actual,
    ElMessage: { success: vi.fn(), warning: vi.fn(), error: vi.fn(), info: vi.fn() },
    ElMessageBox: { confirm: vi.fn().mockResolvedValue(true) },
  }
})

const mockedApi = vi.mocked(dataSourceApi)
const mockedLogical = vi.mocked(logicalDeviceApi)

/**
 * 测试环境的全局 ElTable stub 不渲染 el-table-column 的 scoped slot，
 * 而本页依赖单元格内的状态标签与操作按钮，故提供一个最小替身：
 * 逐行调用每列的 default 槽并传入 { row }，使单元格内容真实渲染。
 */
type ColumnSlots = { default?: (scope: { row: unknown }) => VNodeChild }
const TableStub = defineComponent({
  name: 'ElTable',
  inheritAttrs: false,
  props: { data: { type: Array, default: () => [] } },
  setup(props, { slots, attrs }) {
    return () => {
      const rendered = slots.default?.() ?? []
      const columns = (Array.isArray(rendered) ? rendered : [rendered]) as unknown as Array<{ children?: unknown }>
      const cellSlots = columns.map(col => (col.children as ColumnSlots | null | undefined)?.default)
      const rows = props.data as unknown[]
      return h('table', { class: 'el-table', ...attrs }, [
        h('tbody', rows.map((row, i) =>
          h('tr', { key: (row as { id?: number })?.id ?? i }, cellSlots.map((slotFn, j) =>
            h('td', { class: 'el-table__cell', key: j }, slotFn ? [slotFn({ row })] : []),
          )),
        )),
      ])
    }
  },
})

/**
 * 抽屉替身：把 el-drawer 的 size 透传到 DOM（data-size）并显式声明 prop，
 * 使「组件实际传给 Element Plus 的宽度」可被真实断言。
 * 说明：happy-dom 不做布局，getBoundingClientRect() 恒为 0，
 * 因此像素级几何（left >= 0）由真实浏览器验证，见交付报告。
 */
const DrawerStub = defineComponent({
  name: 'ElDrawer',
  props: {
    modelValue: { type: Boolean, default: false },
    title: { type: String, default: '' },
    size: { type: [String, Number], default: '30%' },
  },
  setup(props, { slots }) {
    return () => h('div', { class: 'el-drawer', 'data-size': String(props.size) }, slots.default?.())
  },
})

function baseSource(overrides: Partial<DataSource> = {}): DataSource {
  return {
    id: 1,
    device_id: 10,
    category: 'temperature',
    edge_device_id: 100,
    source_type: 'edge_device',
    name: '温度主来源',
    description: '',
    priority: 10,
    is_primary: true,
    max_fail_count: 3,
    fail_count: 0,
    status: 'active',
    last_success: '2026-09-01T00:00:00Z',
    last_failure: null,
    config: '',
    created_at: '2026-09-01T00:00:00Z',
    updated_at: '2026-09-01T00:00:00Z',
    ...overrides,
  }
}

const logicalDeviceFixture = { id: 10, name: '客厅温控' } as unknown as LogicalDeviceItem


beforeEach(() => {
  setActivePinia(createPinia())
  vi.clearAllMocks()
  viewportWidth.value = 1440
  mockedApi.list.mockResolvedValue({ items: [baseSource()], total: 1 })
  mockedApi.activate.mockResolvedValue(baseSource({ status: 'active' }))
  mockedApi.deactivate.mockResolvedValue(baseSource({ status: 'disabled' }))
  mockedApi.reset.mockResolvedValue(baseSource({ status: 'standby' }))
  mockedApi.remove.mockResolvedValue(undefined)
  mockedApi.create.mockResolvedValue(baseSource({ id: 2 }))
  mockedApi.update.mockResolvedValue(baseSource())
  mockedApi.getHealth.mockResolvedValue([])
  mockedApi.getFailoverLogs.mockResolvedValue([])
  mockedLogical.list.mockResolvedValue({ items: [logicalDeviceFixture], total: 1 })
})

async function mountPage() {
  const wrapper = mount(DataSourceList, {
    global: {
      plugins: [createPinia()],
      stubs: { ElTable: TableStub, ElDrawer: DrawerStub },
    },
  })
  await flushPromises()
  return wrapper
}

/** 分页器控件（test-setup.ts 的 ElPagination stub 渲染为 <button class="el-pagination">，
 *  点击 = currentPage + 1 并 emit current-change）。 */
/**
 * 分页条元素。参数用 `VueWrapper`（而不是手写的窄类型）：
 * 手写类型只能表达调试时用到的那一两个成员，随后调用 `.exists()` 就会 typecheck 失败
 * —— 而 vitest 不做类型检查，于是"测试全绿"与"类型干净"会脱节（本仓已记录过此类盲区）。
 */
const pager = (wrapper: VueWrapper) => wrapper.find('[data-test="ds-pagination"]')

/** 第 n 次（从 0 起）list 调用的真实请求参数 —— 断言的是**发出去的请求**，不是源码字符串。 */
function listParamsAt(call: number): DataSourceListParams {
  const calls = mockedApi.list.mock.calls
  expect(calls.length, `list 只被调用了 ${calls.length} 次，取不到第 ${call} 次`).toBeGreaterThan(call)
  return calls[call][0] as DataSourceListParams
}
/** 最后一次 list 调用的请求参数。 */
function lastListParams(): DataSourceListParams {
  return listParamsAt(mockedApi.list.mock.calls.length - 1)
}

// ─── 分页样式契约辅助（与 LogicalDeviceList.spec.ts 同法） ───
// 用 @vue/compiler-sfc 真实编译 <style scoped>（复现构建期的 [data-v-*] 改写），
// 再注入 DOM 读 getComputedStyle —— 比"源码字符串包含"更接近浏览器实际应用的声明。
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
 * 注入编译产物 + 合成同结构 DOM（<div class="ds-pagination" data-v-testscope>
 * <div class="el-pagination">），返回内层元素的计算 flex-wrap。
 * 反证：把 parentClass 换成不匹配的值时返回空串 —— 说明真的在走选择器匹配。
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


// ─── F8 移动端宽表横滚合同（§4.3.2.2 MUST / §4.4.1 MUST） ───────────────────
// 断言**真实渲染出的 DOM 祖先链**，不是源码字符串包含：
// `expect(src).toContain('class="mobile-table-wrapper"')` 无法区分
// 「包住了这张表」还是「包住了另一张表 / 只写在注释里」。
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

describe('DataSourceList.vue', () => {
  it('F8：来源表渲染在 .mobile-table-wrapper 内并带横滑提示（§4.3.2.2 MUST）', async () => {
    const wrapper = await mountPage()
    expectEveryTableWrapped(wrapper)
    // 操作列 330px 在窄屏已取消 fixed（F26，见页面模板注释）：它不再粘在右缘，
    // 也就不再压住「名称」列。包裹仍必须保留（真浏览器实测见
    // .tmp-probe/f26-acceptance.mjs）；本断言与列宽/固定无关，继续守护横滚容器与提示。
    expect(wrapper.findAll('.mobile-table-wrapper')).toHaveLength(1)
  })

  it('挂载后加载列表，表格渲染 mock 数据行', async () => {
    const wrapper = await mountPage()
    expect(mockedApi.list).toHaveBeenCalledTimes(1)
    const table = wrapper.find('[data-test="ds-table"]')
    expect(table.exists()).toBe(true)
    expect(table.text()).toContain('temperature')
    expect(table.text()).toContain('温度主来源')
  })

  it('items 为空时显示空态与新建主操作', async () => {
    mockedApi.list.mockResolvedValue({ items: [], total: 0 })
    const wrapper = await mountPage()
    const empty = wrapper.find('[data-test="ds-empty"]')
    expect(empty.exists()).toBe(true)
    expect(empty.text()).toContain('暂无数据源')
    expect(empty.text()).toContain('新建数据源')
  })

  it('四种状态渲染中文标签与对应颜色类型', async () => {
    mockedApi.list.mockResolvedValue({
      items: [
        baseSource({ id: 1, status: 'active' }),
        baseSource({ id: 2, status: 'standby' }),
        baseSource({ id: 3, status: 'error' }),
        baseSource({ id: 4, status: 'disabled' }),
      ],
      total: 4,
    })
    const wrapper = await mountPage()
    const tags = wrapper.findAll('[data-test="ds-status"]')
    expect(tags).toHaveLength(4)
    expect(tags.map(t => t.text())).toEqual(['权威', '待命', '熔断', '已停用'])
    expect(tags.map(t => t.attributes('data-type'))).toEqual(['success', 'info', 'danger', 'info'])
  })

  it('点击"切换为权威"调用 activate 并传对 id；active 行按钮 disabled', async () => {
    mockedApi.list.mockResolvedValue({
      items: [
        baseSource({ id: 1, status: 'active' }),
        baseSource({ id: 2, status: 'standby' }),
      ],
      total: 2,
    })
    const wrapper = await mountPage()
    const buttons = wrapper.findAll('[data-test="ds-activate"]')
    expect(buttons).toHaveLength(2)
    expect(buttons[0].attributes('disabled')).toBeDefined()
    expect(buttons[1].attributes('disabled')).toBeUndefined()

    await buttons[1].trigger('click')
    await flushPromises()
    expect(mockedApi.activate).toHaveBeenCalledWith(2)
  })

  it('删除走确认框：取消不调用 remove，确认后调用 remove', async () => {
    const wrapper = await mountPage()
    const { ElMessageBox } = await import('element-plus')
    vi.mocked(ElMessageBox.confirm).mockRejectedValueOnce(new Error('cancel'))

    await wrapper.find('[data-test="ds-delete"]').trigger('click')
    await flushPromises()
    expect(mockedApi.remove).not.toHaveBeenCalled()

    await wrapper.find('[data-test="ds-delete"]').trigger('click')
    await flushPromises()
    expect(mockedApi.remove).toHaveBeenCalledWith(1)
  })

  /**
   * 危险确认契约（规范 §3.4.3 / §4.3.4）：
   * 确认键必须是 danger 语义类型 + danger class，且 autofocus:false（焦点不落在破坏性按钮上）。
   * 这里是“页面到 feedback.confirmDanger 的接线”守卫；
   * confirmDanger 自身的真实渲染行为由 utils/__tests__/feedbackConfirmDanger.spec.ts 覆盖。
   */
  const dangerContract = expect.objectContaining({
    confirmButtonType: 'danger',
    confirmButtonClass: 'el-button--danger',
    autofocus: false,
  })

  it('删除数据源走危险确认契约，且文案含对象身份', async () => {
    const { ElMessageBox } = await import('element-plus')
    const confirmSpy = vi.mocked(ElMessageBox.confirm)
    mockedApi.list.mockResolvedValue({
      items: [baseSource({ id: 1, name: '温度主来源' })],
      total: 1,
    })
    const wrapper = await mountPage()

    await wrapper.find('[data-test="ds-delete"]').trigger('click')
    await flushPromises()

    expect(confirmSpy).toHaveBeenCalledWith(
      expect.stringContaining('温度主来源'),
      expect.any(String),
      dangerContract,
    )
  })

  it('停用权威来源走危险确认契约（组内候选自动接替属破坏性影响）', async () => {
    const { ElMessageBox } = await import('element-plus')
    const confirmSpy = vi.mocked(ElMessageBox.confirm)
    mockedApi.list.mockResolvedValue({
      items: [baseSource({ id: 1, status: 'active', name: '温度主来源' })],
      total: 1,
    })
    mockedApi.deactivate.mockResolvedValue(baseSource({ status: 'disabled' }))
    const wrapper = await mountPage()

    await wrapper.find('[data-test="ds-deactivate"]').trigger('click')
    await flushPromises()

    expect(confirmSpy).toHaveBeenCalledWith(
      expect.stringContaining('温度主来源'),
      expect.any(String),
      dangerContract,
    )
  })

  it('新建对话框提交调用 create，payload 含必填三字段', async () => {
    const wrapper = await mountPage()
    await wrapper.find('[data-test="create-source"]').trigger('click')
    expect(wrapper.find('[data-test="ds-dialog"]').exists()).toBe(true)

    await wrapper.find('[data-test="field-device-id"]').setValue('11')
    await wrapper.find('[data-test="field-category"]').setValue('humidity')
    await wrapper.find('[data-test="field-edge-device-id"]').setValue('101')
    await wrapper.find('[data-test="field-name"]').setValue('湿度来源')
    await wrapper.find('[data-test="save-source"]').trigger('click')
    await flushPromises()

    expect(mockedApi.create).toHaveBeenCalledTimes(1)
    expect(mockedApi.create).toHaveBeenCalledWith(expect.objectContaining({
      device_id: 11,
      category: 'humidity',
      edge_device_id: 101,
    }))
  })

  it('编辑提交 payload 不含 device_id/category/edge_device_id', async () => {
    const wrapper = await mountPage()
    await wrapper.find('[data-test="ds-edit"]').trigger('click')
    await wrapper.find('[data-test="field-name"]').setValue('改名后的来源')
    await wrapper.find('[data-test="save-source"]').trigger('click')
    await flushPromises()

    expect(mockedApi.update).toHaveBeenCalledTimes(1)
    const [id, payload] = mockedApi.update.mock.calls[0]
    expect(id).toBe(1)
    expect(payload).toEqual(expect.objectContaining({ name: '改名后的来源' }))
    expect(payload).not.toHaveProperty('device_id')
    expect(payload).not.toHaveProperty('category')
    expect(payload).not.toHaveProperty('edge_device_id')
  })

  /**
   * 窄屏抽屉宽度（审计 D3 #10/#14）：原先 size 硬编码 560px，360px 视口下 drawer 左边缘为 -200，
   * 详情里的 label 全部落在可视区外。这里断言交给 Element Plus 的 size 值随视口收敛，
   * 真实浏览器下的几何验证见交付报告（drawer rect.left 由 -200 变为 0）。
   */
  it('详情抽屉宽度随视口收敛：桌面 560px，360px 视口下不溢出', async () => {
    const wrapper = await mountPage()
    await wrapper.find('[data-test="ds-detail"]').trigger('click')
    await flushPromises()

    const drawer = wrapper.find('[data-test="ds-drawer"]')
    expect(drawer.exists()).toBe(true)
    // 桌面视口下宽度仍是原来的 560px（不影响桌面布局）
    expect(drawer.attributes('data-size')).toBe('560px')

    viewportWidth.value = 360
    await flushPromises()
    const narrowSize = drawer.attributes('data-size') ?? ''
    expect(narrowSize).toBe('331px')
    const px = Number(narrowSize.replace('px', ''))
    expect(px).toBeLessThanOrEqual(360)
    // 右对齐抽屉：左边缘 = 视口宽 - 抽屉宽，必须 >= 0
    expect(360 - px).toBeGreaterThanOrEqual(0)
  })

  it('详情抽屉宽度跟随视口连续变化，不在断点处突变', async () => {
    const wrapper = await mountPage()
    await wrapper.find('[data-test="ds-detail"]').trigger('click')
    await flushPromises()
    const sizeAt = async (w: number) => {
      viewportWidth.value = w
      await flushPromises()
      return Number(String(wrapper.find('[data-test="ds-drawer"]').attributes('data-size')).replace('px', ''))
    }
    // 560 / 0.92 ≈ 608.7：视口宽于该值时封顶 560px（桌面布局不变）
    expect(await sizeAt(1440)).toBe(560)
    expect(await sizeAt(1024)).toBe(560)
    expect(await sizeAt(609)).toBe(560)
    // 窄于该值后随视口连续收敛（round(视口 * 0.92)），而不是只在断点跳变
    expect(await sizeAt(600)).toBe(552)
    expect(await sizeAt(500)).toBe(460)
    expect(await sizeAt(390)).toBe(359)
    expect(await sizeAt(360)).toBe(331)
  })

  it('打开详情抽屉调用 fetchHealth 与 fetchFailoverLogs', async () => {
    const wrapper = await mountPage()
    await wrapper.find('[data-test="ds-detail"]').trigger('click')
    await flushPromises()
    expect(mockedApi.getHealth).toHaveBeenCalledWith(1, 50)
    expect(mockedApi.getFailoverLogs).toHaveBeenCalledWith(10, { limit: 20 })
  })

  it('store.error 非空时错误提示条可见并可重试', async () => {
    mockedApi.list.mockRejectedValue(new Error('网络异常'))
    const wrapper = await mountPage()
    const alert = wrapper.find('[data-test="ds-error"]')
    expect(alert.exists()).toBe(true)
    expect(alert.text()).toContain('网络异常')
  })
})

// ─── 数据源列表静默截断修复：真分页 (§3.2.6 MUST) ───────────────────────────
//
// 缺陷：本页原先从不发送 page/page_size，后端恒按 Page=1,PageSize=20 返回
// （backend/internal/datasource/service.go:339-371），统计卡「总来源」显示 total=25，
// 表格却永远只有 20 行，另外 5 条**无入口可达**且界面无任何截断迹象。
// 隔离实例实测：不带 page → items_len=20, total=25；page=2 → items_len=5。
//
// 以下断言的是 **list 实际收到的请求参数**（而不是"渲染了分页控件"）：
// 控件存在只说明有入口，page=2 真的发出去才说明数据可达。
// 反例守卫（禁止本地切片）见本节「翻页会增加 API 调用次数」与「每页行数恒等于后端返回行数」。
describe('DataSourceList.vue — 分页（真分页，非本地切片）', () => {
  beforeEach(() => {
    // 25 条总数 / 每页 20：第 1 页 20 行、第 2 页 5 行 —— 与隔离实例实测一致。
    mockedApi.list.mockImplementation(async (params?: DataSourceListParams) => {
      const page = params?.page ?? 1
      const size = params?.page_size ?? 20
      const from = (page - 1) * size
      const items = Array.from({ length: Math.max(0, Math.min(size, 25 - from)) }, (_, i) =>
        baseSource({ id: from + i + 1, name: '来源 ' + (from + i + 1) }),
      )
      return { items, total: 25 }
    })
  })

  it('挂载时按第 1 页显式请求（page=1, page_size=20），不再依赖后端默认值', async () => {
    await mountPage()
    expect(mockedApi.list).toHaveBeenCalledTimes(1)
    expect(listParamsAt(0).page).toBe(1)
    expect(listParamsAt(0).page_size).toBe(20)
  })

  it('翻页控件渲染 total（来自接口的真总数，不是当前页行数）', async () => {
    const wrapper = await mountPage()
    const el = pager(wrapper)
    expect(el.exists(), '分页器不存在 —— 用户没有翻页入口').toBe(true)
    // stub 渲染 "共 N 条"；N 必须等于接口 total=25 而非当前页 20 行。
    expect(el.text()).toContain('共 25 条')
  })

  it('翻到第 2 页时确实以 page=2 调用后端，且表格换成第 2 页数据', async () => {
    const wrapper = await mountPage()
    expect(wrapper.find('[data-test="ds-table"]').text()).toContain('来源 1')

    await pager(wrapper).trigger('click')
    await flushPromises()

    expect(lastListParams().page).toBe(2)
    expect(lastListParams().page_size).toBe(20)
    // 后端第 2 页返回 21..25：行内容真的换了（不是把第 1 页再渲染一遍）。
    const table = wrapper.find('[data-test="ds-table"]')
    expect(table.text()).toContain('来源 25')
    expect(table.text()).not.toContain('来源 1')
  })

  it('反例守卫：翻页会增加 API 调用次数（本地切片不会发请求）', async () => {
    // 60 条总数构造 3 个合法页：两次翻页都落在有数据的页码上，
    // 调用次数才严格等于「1 次挂载 + 每翻一页 1 次」（total=25 时第 3 页越界，
    // 会额外触发空页回退，反而测不出"翻页 = 多一次请求"）。
    mockedApi.list.mockImplementation(async (params?: DataSourceListParams) => {
      const page = params?.page ?? 1
      const size = params?.page_size ?? 20
      const from = (page - 1) * size
      return {
        items: Array.from({ length: Math.max(0, Math.min(size, 60 - from)) }, (_, i) =>
          baseSource({ id: from + i + 1, name: '来源 ' + (from + i + 1) }),
        ),
        total: 60,
      }
    })
    const wrapper = await mountPage()
    expect(mockedApi.list).toHaveBeenCalledTimes(1)

    await pager(wrapper).trigger('click')
    await flushPromises()
    expect(mockedApi.list).toHaveBeenCalledTimes(2)
    expect(lastListParams().page).toBe(2)

    await pager(wrapper).trigger('click')
    await flushPromises()
    expect(mockedApi.list).toHaveBeenCalledTimes(3)
    expect(lastListParams().page).toBe(3)
  })

  it('反例守卫：每页行数恒等于后端返回行数（本地全量切片会出现 25 行）', async () => {
    const wrapper = await mountPage()
    const rowCount = () => wrapper.findAll('[data-test="ds-table"] tbody tr').length
    expect(rowCount()).toBe(20)
    await pager(wrapper).trigger('click')
    await flushPromises()
    expect(rowCount()).toBe(5)
  })

  it('筛选变化（状态下拉）把 page 重置为 1，避免筛选后停在越界空页', async () => {
    const wrapper = await mountPage()
    await pager(wrapper).trigger('click')
    await flushPromises()
    expect(lastListParams().page).toBe(2)

    await wrapper.find('[data-test="filter-status"]').setValue('standby')
    await flushPromises()

    expect(lastListParams().page).toBe(1)
    expect(lastListParams().status).toBe('standby')
  })

  it('筛选变化（查询按钮 / 重置筛选）同样把 page 重置为 1', async () => {
    const wrapper = await mountPage()
    await pager(wrapper).trigger('click')
    await flushPromises()
    expect(lastListParams().page).toBe(2)

    await wrapper.find('[data-test="search-btn"]').trigger('click')
    await flushPromises()
    expect(lastListParams().page).toBe(1)

    await pager(wrapper).trigger('click')
    await flushPromises()
    expect(lastListParams().page).toBe(2)
    await wrapper.find('[data-test="reset-filters"]').trigger('click')
    await flushPromises()
    expect(lastListParams().page).toBe(1)
  })

  it('每页条数切换回到第 1 页并按新 page_size 请求', async () => {
    const wrapper = await mountPage()
    await pager(wrapper).trigger('click')
    await flushPromises()
    expect(lastListParams().page).toBe(2)

    // stub 的 ElPagination 无 sizes 控件，故按其对外契约直接 emit size-change
    // （真实控件切换每页条数时发的就是这两个事件）。
    const pagination = wrapper.findComponent({ name: 'ElPagination' })
    pagination.vm.$emit('update:pageSize', 50)
    pagination.vm.$emit('size-change', 50)
    await flushPromises()

    expect(lastListParams().page).toBe(1)
    expect(lastListParams().page_size).toBe(50)
  })

  it('删除导致当前页被抽空时回退一页（不留空列表假象）', async () => {
    const wrapper = await mountPage()
    // 第 2 页只有 1 行（总数 21），删掉它后第 2 页变空。
    let total = 21
    mockedApi.list.mockImplementation(async (params?: DataSourceListParams) => {
      const page = params?.page ?? 1
      const size = params?.page_size ?? 20
      const from = (page - 1) * size
      const count = Math.max(0, Math.min(size, total - from))
      return {
        items: Array.from({ length: count }, (_, i) => baseSource({ id: from + i + 1 })),
        total,
      }
    })
    mockedApi.remove.mockImplementation(async () => { total = 20 })

    await pager(wrapper).trigger('click')
    await flushPromises()
    expect(lastListParams().page).toBe(2)
    expect(wrapper.findAll('[data-test="ds-table"] tbody tr')).toHaveLength(1)

    await wrapper.find('[data-test="ds-delete"]').trigger('click')
    await flushPromises()

    // 第 2 页已空 → 自动回退到第 1 页重取，而不是停在第 2 页显示「暂无数源」。
    expect(lastListParams().page).toBe(1)
    expect(wrapper.findAll('[data-test="ds-table"] tbody tr')).toHaveLength(20)
  })

  it('第 1 页为空时不回退（不产生负页码 / 无限回退）', async () => {
    mockedApi.list.mockResolvedValue({ items: [], total: 0 })
    const wrapper = await mountPage()
    expect(mockedApi.list).toHaveBeenCalledTimes(1)
    expect(listParamsAt(0).page).toBe(1)
    // total=0 → 分页条隐藏，且没有第二次请求。
    expect(pager(wrapper).exists()).toBe(false)
  })

  it('无 status 筛选时状态操作不回取列表，有 status 筛选时回取（被改写行可能不再属于结果集）', async () => {
    // 「切换为权威」按钮只对非 active 行可点：这里固定返回 standby 行。
    mockedApi.list.mockImplementation(async () => ({
      items: [baseSource({ id: 1, status: 'standby' })],
      total: 1,
    }))
    mockedApi.activate.mockResolvedValue(baseSource({ id: 1, status: 'active' }))

    const wrapper = await mountPage()
    await wrapper.find('[data-test="ds-activate"]').trigger('click')
    await flushPromises()
    // 行只被本地替换：请求次数仍是挂载时那一次。
    expect(mockedApi.list).toHaveBeenCalledTimes(1)

    await wrapper.find('[data-test="filter-status"]').setValue('standby')
    await flushPromises()
    expect(mockedApi.list).toHaveBeenCalledTimes(2)

    await wrapper.find('[data-test="ds-activate"]').trigger('click')
    await flushPromises()
    expect(mockedApi.list).toHaveBeenCalledTimes(3)
  })

  it('窄屏分页控件自身声明 flex-wrap（避免上一页按钮被推出可视区）', async () => {
    const css = await compiledScopedCss(source, 'DataSourceList.vue')
    const rule = css.match(/\.ds-pagination\[data-v-[a-z0-9]+\]\s+\.el-pagination\s*\{[^}]*\}/)
    expect(rule, 'DataSourceList.vue 缺少 .ds-pagination :deep(.el-pagination) 编译产物').not.toBeNull()
    expect(measureFlexWrap(css, 'ds-pagination')).toBe('wrap')
    // 反证：换一个不匹配的父类名必须测不到 wrap（说明上面不是恒真）。
    expect(measureFlexWrap(css, 'not-ds-pagination')).toBe('')
  })
})
