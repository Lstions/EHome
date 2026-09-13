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
    ElMessageBox: { confirm: vi.fn().mockResolvedValue(true) },
  }
})

const mockedAlertApi = vi.mocked(alertApi)
const mockedEdgeApi = vi.mocked(edgeDeviceApi)

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
  mockedAlertApi.listEvents.mockResolvedValue([])
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

describe('AlertRules.vue', () => {
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
    mockedAlertApi.listEvents.mockResolvedValue([eventFixture])
    const wrapper = await mountWithSlotTable()

    const row = wrapper.find('[data-test="events-table"] tbody tr')
    expect(row.exists()).toBe(true)
    const cells = row.findAll('td').map(td => td.text())
    expect(cells).toContain('电池过压')
    // 回归护栏：修复前列内容恰为 String(rule_id) === '1'
    expect(cells).not.toContain('1')
  })

  it('告警事件「规则」列在规则已被删除时回退为 #id，不留空白', async () => {
    mockedAlertApi.listEvents.mockResolvedValue([{ ...eventFixture, id: 51, rule_id: 999 }])
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
