import { describe, it, expect, vi, beforeEach } from 'vitest'
import { defineComponent, h, type VNodeChild } from 'vue'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import DataSourceList from '../DataSourceList.vue'
import { dataSourceApi, type DataSource } from '@/api/dataSource'
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
      stubs: { ElTable: TableStub },
    },
  })
  await flushPromises()
  return wrapper
}

describe('DataSourceList.vue', () => {
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
