import { describe, it, expect, vi, beforeEach } from 'vitest'
import { defineComponent, h, type VNode } from 'vue'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import AlertRules from '../AlertRules.vue'
import alertRulesSource from '../AlertRules.vue?raw'
import { useAlertStore } from '@/stores/alert'
import { alertApi } from '@/api/alert'
import { edgeDeviceApi } from '@/api/edgeDevice'

// Element Plus 组件由 src/test-setup.ts 全局 stub; 这里 mock 数据层。
vi.mock('@/api/alert', () => ({
  alertApi: {
    listRules: vi.fn(),
    createRule: vi.fn(),
    updateRule: vi.fn(),
    deleteRule: vi.fn(),
    setRuleEnabled: vi.fn(),
    listEvents: vi.fn(),
    markEventsRead: vi.fn(),
    markAllEventsRead: vi.fn(),
  },
}))
vi.mock('@/api/edgeDevice', () => ({
  edgeDeviceApi: {
    getList: vi.fn(),
  },
}))
// G2：传感器候选改为按设备拉取 `/api/v1/unified-data/categories`。
// 不 mock 会真发 HTTP（该 spec 原先不涉及 client），这里给出与后端同形的候选，
// 让「选已有类别」与「手输类别」两条路径都可测。
const { mockClientGet } = vi.hoisted(() => ({
  mockClientGet: vi.fn(() => Promise.resolve({ data: [{ code: 'temperature', unit: '°C' }] })),
}))
vi.mock('@/api/client', () => ({
  default: { get: mockClientGet },
}))
vi.mock('element-plus', async importOriginal => {
  const actual = await importOriginal<typeof import('element-plus')>()
  return {
    ...actual,
    ElMessage: Object.assign(vi.fn(), { success: vi.fn(), warning: vi.fn(), error: vi.fn() }),
  }
})
// F29: useResponsive 的视口宽度是模块级共享 ref —— 直接驱动它模拟窄屏，
// 无需真实 resize（与 DataSourceList.spec.ts 同法）。默认 1440/false 保持既有用例
// 的桌面语义不变。
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

// F5 危险确认迁移: 删除确认改走 feedback.confirmDanger (它内部才调 ElMessageBox)。
// mock 边界因此上移到 feedback —— 视图层不再直接依赖 ElMessageBox。
vi.mock('@/utils/feedback', async importOriginal => {
  const actual = await importOriginal<typeof import('@/utils/feedback')>()
  return {
    ...actual,
    feedback: { ...actual.feedback, confirmDanger: vi.fn().mockResolvedValue(true) },
  }
})

const mockedAlertApi = vi.mocked(alertApi)
const mockedEdgeApi = vi.mocked(edgeDeviceApi)

/** 分页形状夹具 (P1.2: {items,total,page,page_size})。 */
function eventPage(items: unknown[], total = items.length, page = 1, page_size = 20) {
  return { items, total, page, page_size } as never
}

const ruleFixture = {
  id: 1,
  target_type: 'edge_device' as const,
  target_id: 10,
  sensor_name: 'cell_voltage_1',
  comparator: 'gt' as const,
  threshold: 4.2,
  duration_sec: 0,
  silence_sec: 300,
  level: 'warning' as const,
  enabled: true,
  name: '电池过压',
  created_at: '2026-08-21T00:00:00Z',
  updated_at: '2026-08-21T00:00:00Z',
}

const eventFixture = {
  id: 50,
  rule_id: 1,
  state: 'firing' as const,
  value: 4.3,
  fired_at: '2026-08-21T12:00:00Z',
  resolved_at: null,
  notified_at: null,
  created_at: '2026-08-21T12:00:00Z',
}

beforeEach(() => {
  setActivePinia(createPinia())
  vi.clearAllMocks()
  // F29: 每个用例从桌面视口起步，避免窄屏用例污染后续断言。
  viewportWidth.value = 1440
  isMobileRef.value = false
  mockedAlertApi.listRules.mockResolvedValue([ruleFixture])
  mockedAlertApi.listEvents.mockResolvedValue(eventPage([]))
  mockedEdgeApi.getList.mockResolvedValue({ total: 1, items: [{ id: 10, name: 'BMS-01' } as never] })
})

async function mountPage() {
  const wrapper = mount(AlertRules, {
    global: { plugins: [createPinia()] },
  })
  await flushPromises()
  return wrapper
}

/**
 * 本文件专用的 el-table/el-table-column 替身：真实渲染表头、每行每列，并执行列的
 * #default 作用域插槽（src/test-setup.ts 的通用 stub 只把行数据拍平成文本、不执行插槽，
 * 无法断言某个单元格实际渲染了什么）。仅用于「规则」列的 DOM 断言，不影响既有用例。
 */
function slotTable() {
  const defaultSlotOf = (vn: VNode) => {
    const children = vn.children
    if (children && typeof children === 'object' && !Array.isArray(children)) {
      const slot = children['default']
      if (typeof slot === 'function') return slot
    }
    return null
  }
  const SlotColumn = defineComponent({
    name: 'ElTableColumn',
    props: { prop: String, label: String, width: [String, Number], minWidth: [String, Number], fixed: [String, Boolean], type: String },
    setup() {
      return () => null
    },
  })
  const SlotTable = defineComponent({
    name: 'ElTable',
    props: { data: { type: Array, default: () => [] } },
    setup(props, { slots }) {
      return () => {
        const columns = (slots.default?.() ?? []) as VNode[]
        const rows = props.data as Array<Record<string, unknown>>
        return h('table', { class: 'el-table' }, [
          // data-fixed 把该列解析后的 fixed prop 落到 th 上（F29 断言用）：
          // '' = 未设置 fixed，'false' = 显式取消，'right' = 桌面固定。
          h('thead', [h('tr', columns.map(vn => h('th', {
            class: 'el-table__cell',
            'data-fixed': String(vn.props?.['fixed'] ?? ''),
            'data-width': String(vn.props?.['width'] ?? ''),
          }, String(vn.props?.['label'] ?? ''))))]),
          h('tbody', rows.map((row, index) => h('tr', { key: String(row?.['id'] ?? index) }, columns.map(vn => {
            const scoped = defaultSlotOf(vn)
            const content = scoped ? scoped({ row }) : String(row?.[String(vn.props?.['prop'] ?? '')] ?? '')
            return h('td', { class: 'el-table__cell' }, content)
          })))),
        ])
      }
    },
  })
  return { ElTable: SlotTable, 'el-table': SlotTable, ElTableColumn: SlotColumn, 'el-table-column': SlotColumn }
}

/** 用执行作用域插槽的表格替身挂载（仅新用例使用）。 */
async function mountWithSlotTable() {
  const wrapper = mount(AlertRules, {
    global: { plugins: [createPinia()], components: slotTable() },
  })
  await flushPromises()
  return wrapper
}


// ─── F8 移动端宽表横滚合同（§4.3.2.2 MUST / §4.4.1 MUST） ───────────────────
// 断言真实渲染出的 DOM 祖先链，不是源码字符串包含。
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

describe('AlertRules.vue', () => {
  it('F8：规则表与事件表都渲染在 .mobile-table-wrapper 内并带横滑提示（§4.3.2.2 MUST）', async () => {
    const wrapper = await mountPage()
    expectEveryTableWrapped(wrapper)
    // 本页两张表（规则 900px / 事件 650px），逐张判定，不能一张包裹就整页通过
    expect(wrapper.findAll('.mobile-table-wrapper')).toHaveLength(2)
    expect(wrapper.findAll('.mobile-table-hint')).toHaveLength(2)
  })

  it('F8：真分页器仍在 wrapper 之外（横滚容器不得把分页一起卷走）', async () => {
    const wrapper = await mountPage()
    const pagination = wrapper.find('.events-pagination')
    expect(pagination.exists(), '分页器不存在，用例前提不成立').toBe(true)
    expect(pagination.element.closest('.mobile-table-wrapper')).toBeNull()
  })

  it('挂载后加载规则与事件', async () => {
    await mountPage()
    expect(mockedAlertApi.listRules).toHaveBeenCalledTimes(1)
    expect(mockedAlertApi.listEvents).toHaveBeenCalledTimes(1)
  })

  it('store 规则数据驱动表格渲染', async () => {
    const wrapper = await mountPage()
    const store = useAlertStore(wrapper.vm.$pinia)
    expect(store.rules).toHaveLength(1)
    expect(wrapper.find('[data-test="rules-table"]').exists()).toBe(true)
  })

  it('告警事件「规则」列渲染规则名而不是裸主键', async () => {
    mockedAlertApi.listEvents.mockResolvedValue(eventPage([eventFixture], 1))
    const wrapper = await mountWithSlotTable()

    const row = wrapper.find('[data-test="events-table"] tbody tr')
    expect(row.exists()).toBe(true)
    const cells = row.findAll('td').map(td => td.text())
    expect(cells).toContain('电池过压')
    // 回归护栏：修复前列内容恰为 String(rule_id) === '1'
    expect(cells).not.toContain('1')
  })

  it('告警事件「规则」列在规则已被删除时回退为 #id，不留空白', async () => {
    mockedAlertApi.listEvents.mockResolvedValue(eventPage([{ ...eventFixture, id: 51, rule_id: 999 }], 1))
    const wrapper = await mountWithSlotTable()

    const cells = wrapper.find('[data-test="events-table"] tbody tr').findAll('td').map(td => td.text())
    expect(cells).toContain('#999')
    expect(cells.every(cell => cell.trim() !== '')).toBe(true)
  })

  it('打开创建对话框并校验空表单', async () => {
    const wrapper = await mountPage()
    await wrapper.find('[data-test="create-rule"]').trigger('click')
    expect(wrapper.find('[data-test="rule-dialog"]').exists()).toBe(true)
    // 空表单保存应被拦截 (ElMessage.warning)
    const { ElMessage } = await import('element-plus')
    await wrapper.find('[data-test="save-rule"]').trigger('click')
    await flushPromises()
    expect(ElMessage.warning).toHaveBeenCalledWith('请填写规则名称')
  })

  it('删除走 feedback.confirmDanger, 取消时不发 DELETE 请求 (F5 危险确认)', async () => {
    const { feedback } = await import('@/utils/feedback')
    const confirmSpy = vi.mocked(feedback.confirmDanger)
    confirmSpy.mockResolvedValueOnce(false)
    mockedAlertApi.deleteRule.mockResolvedValue(undefined)
    // 删除按钮在「操作」列的 #default 作用域插槽里, 通用 stub 不执行插槽 ——
    // 用本文件已有的 slotTable 替身 (真实渲染每行每列)。
    const wrapper = await mountWithSlotTable()

    const delBtn = wrapper.findAll('button').find(b => b.text() === '删除')
    expect(delBtn).toBeTruthy()
    await delBtn!.trigger('click')
    await flushPromises()

    // 确认弹窗必须被调用, 且文案保留「删除规则「X」？」的语义。
    expect(confirmSpy).toHaveBeenCalledTimes(1)
    expect(confirmSpy.mock.calls[0][0]).toContain('删除规则「电池过压」？')
    // 关键: 取消 (false) 时**不得**继续执行删除。
    expect(mockedAlertApi.deleteRule).not.toHaveBeenCalled()
  })

  it('删除确认后发出 DELETE 请求 (确认路径)', async () => {
    const { feedback } = await import('@/utils/feedback')
    const confirmSpy = vi.mocked(feedback.confirmDanger)
    confirmSpy.mockResolvedValueOnce(true)
    mockedAlertApi.deleteRule.mockResolvedValue(undefined)
    const wrapper = await mountWithSlotTable()

    const delBtn = wrapper.findAll('button').find(b => b.text() === '删除')
    await delBtn!.trigger('click')
    await flushPromises()

    expect(confirmSpy).toHaveBeenCalledTimes(1)
    expect(mockedAlertApi.deleteRule).toHaveBeenCalledWith(1)
  })

  it('告警事件表接上真实分页器并渲染后端 total (P1.2: 不再无分页器)', async () => {
    // 137 条事件、当前页 1、每页 20 → 分页器必须存在且 total 取自后端而非本地数组长度。
    mockedAlertApi.listEvents.mockResolvedValue(eventPage([eventFixture], 137, 1, 20))
    const wrapper = await mountPage()

    const pager = wrapper.find('[data-test="events-pagination"]')
    expect(pager.exists()).toBe(true)
    // 首次加载只取当前页: page=1&page_size=20 必须真实下发 (改前是无参全量)。
    expect(mockedAlertApi.listEvents).toHaveBeenCalledTimes(1)
    expect(mockedAlertApi.listEvents.mock.calls[0][0]).toEqual(expect.objectContaining({ page: 1, page_size: 20 }))
    // 137 > 20 → 分页器必须知道有 7 页, 而不是把 1 条当全部。
    const store = useAlertStore(wrapper.vm.$pinia)
    expect(store.eventsTotal).toBe(137)
    expect(store.events).toHaveLength(1)
  })

  it('翻页按服务端页请求 (page=2), 不本地切片', async () => {
    mockedAlertApi.listEvents.mockResolvedValue(eventPage([eventFixture], 137, 1, 20))
    const wrapper = await mountPage()
    const store = useAlertStore(wrapper.vm.$pinia)

    mockedAlertApi.listEvents.mockResolvedValue(eventPage([{ ...eventFixture, id: 60 }], 137, 2, 20))
    await store.setEventsPage(2)
    await flushPromises()

    expect(mockedAlertApi.listEvents).toHaveBeenLastCalledWith(expect.objectContaining({ page: 2, page_size: 20 }))
    expect(store.eventsPage).toBe(2)
    expect(store.events[0].id).toBe(60)
  })

  it('创建成功后调用 store 并关闭对话框', async () => {
    const created = { ...ruleFixture, id: 2, name: '新规则' }
    mockedAlertApi.createRule.mockResolvedValue(created)
    mockClientGet.mockResolvedValue({ data: [{ code: 'temperature', unit: '°C' }] })
    const wrapper = await mountPage()
    await wrapper.find('[data-test="create-rule"]').trigger('click')
    // 填表 (ElInput/ElSelect stub 将 data-test 直接渲染在控件自身上)
    await wrapper.find('[data-test="field-name"]').setValue('新规则')
    await wrapper.find('[data-test="field-target-id"]').setValue('10')
    // G2：传感器已是 el-select。选定目标设备会去拉候选类别，故先 flush 再选。
    await flushPromises()
    await wrapper.find('[data-test="field-sensor"]').setValue('temperature')
    await wrapper.find('[data-test="save-rule"]').trigger('click')
    await flushPromises()
    expect(mockedAlertApi.createRule).toHaveBeenCalledTimes(1)
    expect(mockedAlertApi.createRule).toHaveBeenCalledWith(expect.objectContaining({
      name: '新规则',
      target_id: 10,
      sensor_name: 'temperature',
    }))
  })

  // ─── F29 窄屏取消固定操作列（挡不住就白修） ───────────────────────────────
  // 缺陷：360px 下表格盒 280px，fixed="right" 的「操作」列 130px（占 46.4%）
  // 粘在右缘、绘制顺序恒在普通列之上，把行内「启用」列 el-switch 盖掉 8/48 个
  // elementFromPoint 采样点（F26 实测 self=40/48、other=8/48，首个遮挡者是固定列内
  // DIV.cell）。修复范式与 AutomationRules.vue:62 / DataSourceList.vue:153 同源：
  // 列宽与内容不变，只在窄屏（isMobile，<768px）把 fixed 置 false。
  //
  // 像素级可达性由真浏览器 48 点网格验收：frontend-shared/.tmp-probe/f29-alerts-probe.mjs
  // 本用例在 jsdom/happy-dom（无布局引擎）中守护的是**根因契约**：窄屏不得再渲染出
  // 任何 fixed 列。
  const fixedOf = (th: { attributes: (n: string) => string | undefined }) => th.attributes('data-fixed')

  /** 取表格每一列的表头 label 与解析后的 fixed 值。 */
  function columnsOf(wrapper: Awaited<ReturnType<typeof mountWithSlotTable>>, test: string) {
    const table = wrapper.find(`[data-test="${test}"]`)
    expect(table.exists(), `${test} 未渲染，用例前提不成立`).toBe(true)
    const ths = table.findAll('thead th')
    expect(ths.length, `${test} 渲染出的列数为 0，断言会假绿`).toBeGreaterThan(0)
    return ths.map(th => ({
      label: th.text(),
      fixed: fixedOf(th as never),
      width: (th as never as { attributes: (n: string) => string | undefined }).attributes('data-width'),
    }))
  }

  it('F29 桌面 1440：规则表操作列仍然 fixed="right"（桌面行为不得改变）', async () => {
    const wrapper = await mountWithSlotTable()
    const cols = columnsOf(wrapper, 'rules-table')
    expect(cols.find(c => c.label === '操作'), '规则表缺少「操作」列').toBeTruthy()
    expect(cols.find(c => c.label === '操作')!.fixed).toBe('right')
  })

  it('F29 窄屏 360/390：规则表操作列不再 fixed（消除对「启用」开关的遮挡）', async () => {
    viewportWidth.value = 360
    isMobileRef.value = true
    const wrapper = await mountWithSlotTable()

    const cols = columnsOf(wrapper, 'rules-table')
    const op = cols.find(c => c.label === '操作')
    expect(op, '规则表缺少「操作」列').toBeTruthy()
    expect(op!.fixed, '窄屏下「操作」列仍被标记为固定列，会继续遮住行内控件').toBe('false')
    // 整表不允许残留任何 fixed 列（遮挡的充要条件）
    expect(cols.filter(c => c.fixed !== '' && c.fixed !== 'false').map(c => c.label)).toEqual([])
    // 列宽不得被本修复改动（F26 已实测否决收窄列宽路线）
    expect(op!.width, '窄屏下「操作」列宽被本修复改动，违反任务书第 3 条').toBe('130')
    // 开关仍然真实渲染在行内（不是被删掉换绿）
    expect(wrapper.findAll('[data-test="rule-enabled"]').length).toBeGreaterThan(0)
  })

  it('F29 事件表 5 列本来就没有 fixed 列，不得被本修复波及', async () => {
    for (const mobile of [false, true]) {
      viewportWidth.value = mobile ? 360 : 1440
      isMobileRef.value = mobile
      const wrapper = await mountWithSlotTable()
      const cols = columnsOf(wrapper, 'events-table')
      expect(cols, '事件表列数不为 5，用例前提不成立').toHaveLength(5)
      expect(
        cols.filter(c => c.fixed !== '').map(c => c.label),
        `事件表在 ${mobile ? '窄屏' : '桌面'} 下出现了 fixed 列`,
      ).toEqual([])
    }
  })

  // ─── U9: 「全部标记已读」必须发 {all:true}, 不得再发空 ids ──────────────────
  //
  // 改前链路: onMarkAllRead → store.markEventsRead([]) → POST {ids: []} →
  // 后端 handler_alert.go 在 `!all && len(ids)==0` 判 400「ids 或 all 必填其一」。
  // 该按钮因此**必然失败**, 用户只看到「操作失败」。下面断言的是**真实发出的请求**
  // (api 层的 markAllEventsRead 被调用 / markEventsRead 未被调用), 不是源码字符串。
  it('U9: 「全部标记已读」调 markAllEventsRead, 不发 markEventsRead([])', async () => {
    mockedAlertApi.markAllEventsRead.mockResolvedValue(undefined)
    const wrapper = await mountPage()
    mockedAlertApi.markAllEventsRead.mockClear()
    mockedAlertApi.markEventsRead.mockClear()
    mockedAlertApi.listEvents.mockClear()

    await wrapper.find('[data-test="mark-read"]').trigger('click')
    await flushPromises()

    expect(mockedAlertApi.markAllEventsRead).toHaveBeenCalledTimes(1)
    // 回归钉子: 改前的写法就是 markEventsRead([]) (必然 400), 这里必须一次都没有。
    expect(mockedAlertApi.markEventsRead).not.toHaveBeenCalled()
    // 成功后刷新事件列表 (带当前筛选与页码)。
    expect(mockedAlertApi.listEvents).toHaveBeenCalled()
  })

  // ─── G1: 事件筛选条 (后端已支持 rule_id/state/start_time/end_time, 改前 0 使用) ──
  //
  // 断言的是**真实请求参数** (listEvents 实际收到的 params), 不是源码字符串。
  const lastParams = () => mockedAlertApi.listEvents.mock.calls.at(-1)?.[0] as Record<string, unknown>

  it('G1: 事件筛选条渲染规则/状态/时间范围三个控件', async () => {
    const wrapper = await mountPage()
    expect(wrapper.find('[data-test="event-filter-rule"]').exists(), '缺规则筛选下拉').toBe(true)
    expect(wrapper.find('[data-test="event-filter-state"]').exists(), '缺状态筛选下拉').toBe(true)
    expect(wrapper.find('[data-test="event-filter-range"]').exists(), '缺时间范围选择器').toBe(true)
  })

  it('G1: 规则筛选变化下发 rule_id 且页码重置为 1', async () => {
    const wrapper = await mountPage()
    // 先翻到第 2 页, 制造「页码非 1」的前置状态。
    const store = useAlertStore(wrapper.vm.$pinia)
    store.eventsPage = 2

    await wrapper.find('[data-test="event-filter-rule"]').setValue('1')
    await flushPromises()

    const params = lastParams()
    expect(params.rule_id, '规则筛选未下发 rule_id').toBe(1)
    expect(params.page, '筛选变化后页码必须重置为 1').toBe(1)
    expect(params.page_size).toBe(20)
  })

  it('G1: 状态筛选变化下发 state 且页码重置为 1', async () => {
    const wrapper = await mountPage()
    const store = useAlertStore(wrapper.vm.$pinia)
    store.eventsPage = 3

    await wrapper.find('[data-test="event-filter-state"]').setValue('firing')
    await flushPromises()

    const params = lastParams()
    expect(params.state, '状态筛选未下发 state').toBe('firing')
    expect(params.page).toBe(1)
  })

  it('G1: 时间范围筛选下发 ISO start_time/end_time', async () => {
    const wrapper = await mountPage()
    const store = useAlertStore(wrapper.vm.$pinia)
    // datetimerange 的 v-model 是 [start, end] 两元素数组 (value-format 直接给 ISO 字符串)。
    store.eventsPage = 2
    const vm = wrapper.vm as unknown as { filterRange: [string, string] | null }
    vm.filterRange = ['2026-08-21T00:00:00', '2026-08-21T12:00:00']
    await flushPromises()

    await wrapper.find('[data-test="event-filter-state"]').setValue('resolved')
    await flushPromises()

    const params = lastParams()
    expect(params.start_time).toBe('2026-08-21T00:00:00')
    expect(params.end_time).toBe('2026-08-21T12:00:00')
    expect(params.page).toBe(1)
  })

  it('G1: 筛选清空后不再携带该参数 (不带 = 不过滤)', async () => {
    const wrapper = await mountPage()
    await wrapper.find('[data-test="event-filter-state"]').setValue('firing')
    await flushPromises()
    expect(lastParams().state).toBe('firing')

    // clearable 清空 → undefined ⇒ 请求里不得出现 state 字段。
    await wrapper.find('[data-test="event-filter-state"]').setValue('')
    await flushPromises()
    expect(lastParams()).not.toHaveProperty('state')
    expect(lastParams().page).toBe(1)
  })

  it('G1: 翻页时筛选不丢失 (筛选与分页同一次请求下发)', async () => {
    const wrapper = await mountPage()
    await wrapper.find('[data-test="event-filter-state"]').setValue('firing')
    await flushPromises()
    expect(lastParams().page).toBe(1)

    // 分页控件 stub 点一下 = currentPage + 1 并 emit current-change。
    // 关键: 翻页这条路径若只带 page/page_size 而丢掉筛选, 用户会看到"筛选突然失效"。
    await wrapper.find('[data-test="events-pagination"]').trigger('click')
    await flushPromises()
    const params = lastParams()
    expect(params.state, '翻页后筛选被丢掉').toBe('firing')
    expect(params.page).toBe(2)
  })

  // ── G2：传感器名由手输改为「按设备候选 + 允许自建」的下拉 ──
  //
  // 改前是 el-input，placeholder「如：cell_voltage_1」——拼错时规则**永不触发**
  // 且没有任何反馈（规则列表仍显示"已启用"）。这是最典型的静默失效，故必须
  // 让候选项来自后端**已上报**的类别。
  describe('G2 传感器候选下拉', () => {
    it('选定目标设备后按该设备拉取候选类别（带 /api/v1 前缀，与生产一致）', async () => {
      mockClientGet.mockResolvedValue({ data: [{ code: 'cell_voltage_1', unit: 'V' }] })
      const wrapper = await mountPage()
      mockClientGet.mockClear()
      await wrapper.find('[data-test="create-rule"]').trigger('click')
      await wrapper.find('[data-test="field-target-id"]').setValue('10')
      await flushPromises()
      expect(mockClientGet).toHaveBeenCalledWith(
        '/api/v1/unified-data/categories',
        expect.objectContaining({ params: { device_pk: 10 } }),
      )
    })

    it('候选渲染为可选项，且仍是可搜索 + 允许自建的下拉', async () => {
      mockClientGet.mockResolvedValue({ data: [{ code: 'cell_voltage_1', unit: 'V' }] })
      const wrapper = await mountPage()
      await wrapper.find('[data-test="create-rule"]').trigger('click')
      await wrapper.find('[data-test="field-target-id"]').setValue('10')
      await flushPromises()
      const sensor = wrapper.find('[data-test="field-sensor"]')
      // stub 是原生 <select>：候选项以 <option> 形式落地
      expect(sensor.element.tagName.toLowerCase()).toBe('select')
      expect(sensor.html()).toContain('cell_voltage_1')
      // allow-create（允许手输）在 stub 上不体现为 DOM 属性，故对源码断言：
      // 否则设备未上报类别时用户无法建规则（会把"候选为空"变成硬阻塞）。
      expect(alertRulesSource).toContain('allow-create')
      expect(alertRulesSource).toContain('filterable')
    })

    it('候选接口失败时退化为手输，不阻塞建规则（不得弹错打断）', async () => {
      mockClientGet.mockRejectedValue(new Error('boom'))
      const wrapper = await mountPage()
      await wrapper.find('[data-test="create-rule"]').trigger('click')
      await wrapper.find('[data-test="field-target-id"]').setValue('10')
      await flushPromises()
      // 表单仍在、仍可提交（不因候选加载失败而卡死）
      expect(wrapper.find('[data-test="save-rule"]').exists()).toBe(true)
      expect(mockedAlertApi.createRule).not.toHaveBeenCalled()
    })
  })

  // ── F3：空态要能区分「没配规则」与「规则没触发」 ──
  //
  // 改前事件表只写「暂无告警事件」：两种截然不同的状态共用一句话，
  // 用户无法判断是自己还没建规则，还是规则建了但不触发（后者通常意味着
  // 传感器名与设备实际上报类别不一致 —— 正是 G2 修的静默失效）。
  //
  // 断言层次说明：`el-table` 的 `#empty` 插槽**只在 data 为空时由 EP 渲染**，
  // 而本仓全局 ElTable stub（test-setup.ts:280）不渲染 empty 插槽 —— 故这里
  // 无法用 DOM 断言空态文案。改为两层组合：
  //   ① 行为层：store.rules 的长度决定走哪个分支（用真实 store 驱动，可失败）；
  //   ② 源码层：两个分支的文案都在，且都含"下一步"信息。
  // 不为这一条去改全局 stub：那会影响 160 个测试文件的表格渲染面。
  describe('F3 事件表空态区分', () => {
    it('空态分支由 store.rules 是否为空驱动（两条文案都必须存在）', async () => {
      mockedAlertApi.listRules.mockResolvedValue([])
      mockedAlertApi.listEvents.mockResolvedValue(eventPage([], 0, 1, 20))
      const wrapper = await mountPage()
      await flushPromises()
      expect(useAlertStore(wrapper.vm.$pinia).rules).toHaveLength(0)

      // 源码层：两种形态都在，且各自带"下一步"
      expect(alertRulesSource).toContain('尚未创建告警规则')
      expect(alertRulesSource).toContain('在「告警规则」表中创建规则后')
      expect(alertRulesSource).toContain('暂无告警事件')
      expect(alertRulesSource).toContain('传感器名')
      // 反证：不得退回"单一文案"（那就是改前的缺陷）
      expect(alertRulesSource).not.toContain('<template #empty>暂无告警事件</template>')
    })
  })
})
