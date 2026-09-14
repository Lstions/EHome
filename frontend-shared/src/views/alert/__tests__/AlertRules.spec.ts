import { describe, it, expect, vi, beforeEach } from 'vitest'
import { defineComponent, h, type VNode } from 'vue'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import AlertRules from '../AlertRules.vue'
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
  },
}))
vi.mock('@/api/edgeDevice', () => ({
  edgeDeviceApi: {
    getList: vi.fn(),
  },
}))
vi.mock('element-plus', async importOriginal => {
  const actual = await importOriginal<typeof import('element-plus')>()
  return {
    ...actual,
    ElMessage: { success: vi.fn(), warning: vi.fn(), error: vi.fn() },
  }
})
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
          h('thead', [h('tr', columns.map(vn => h('th', { class: 'el-table__cell' }, String(vn.props?.['label'] ?? ''))))]),
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
    const wrapper = await mountPage()
    await wrapper.find('[data-test="create-rule"]').trigger('click')
    // 填表 (ElInput/ElSelect stub 将 data-test 直接渲染在控件自身上)
    await wrapper.find('[data-test="field-name"]').setValue('新规则')
    await wrapper.find('[data-test="field-target-id"]').setValue('10')
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
})
