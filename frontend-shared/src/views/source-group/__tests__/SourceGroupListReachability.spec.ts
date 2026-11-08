import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { defineComponent, h, type VNode } from 'vue'
import SourceGroupList from '../SourceGroupList.vue'
import { useSourceGroupStore } from '@/stores/sourceGroup'

/**
 * 来源组列表的**可达性**测试。
 *
 * # 为什么单独一个文件、且聚焦"可达性"
 *
 * 2026-10-23 的真实缺陷：SourceGroupList 被嵌入「数据源管理」页时
 * `embedded=true` ⇒ PageHeader 整块不渲染 ⇒ **唯一**的"新建来源组"按钮
 * 跟着消失，空态里也没有第二个入口 ⇒ 用户完全无法创建来源组。
 *
 * 这个缺陷的恶劣之处在于：**功能本身完全正常**（对话框、表单、后端接口都对），
 * 只是没有按钮能触发它。所以"功能测试"永远抓不到它 ——
 * 必须断言"入口在渲染结果里存在"。
 */

// ⚠ mock 的 list 必须能返回有数据的列表：组件 onMounted 会调 loadList()，
// 若这里恒返回空数组，测试里手工设置的 store.items 会被覆盖 ⇒ 行内按钮
// 断言假红（我第一次就踩了这个坑）。用 rowsForMock 控制返回值。
const mockRows: { value: unknown[] } = { value: [] }

vi.mock('@/api/sourceGroup', () => ({
  sourceGroupApi: {
    list: vi.fn(() => Promise.resolve({ items: mockRows.value, total: mockRows.value.length })),
    get: vi.fn(),
    create: vi.fn(),
    update: vi.fn(),
    remove: vi.fn(),
    activate: vi.fn(),
    deactivate: vi.fn(),
    failoverLogs: vi.fn(() => Promise.resolve({ items: [], total: 0 })),
    health: vi.fn(),
  },
}))

/**
 * 能执行**作用域插槽**的表格替身。
 *
 * 全局 ElTable stub 不执行 ElTableColumn 的 #default 插槽，因此断言不到
 * 行内按钮（编辑/删除/激活）。项目已有此范式（AlertRules.spec.ts）。
 */
function slotTable() {
  const SlotColumn = defineComponent({
    props: { label: String, prop: String, width: [String, Number], minWidth: [String, Number] },
    setup(_, { slots }) {
      return () => slots.default?.()
    },
  })
  const SlotTable = defineComponent({
    props: { data: { type: Array, default: () => [] } },
    setup(props, { slots }) {
      return () => {
        const columns = (slots.default?.() ?? []) as VNode[]
        const rows = props.data as Array<Record<string, unknown>>
        return h('table', { class: 'el-table' }, [
          h('tbody', rows.map((row, index) =>
            h('tr', { key: String(row?.id ?? index) },
              columns.map(vn => {
                const scoped = vn.children as Record<string, unknown> | undefined
                const fn = scoped?.['default'] as ((p: { row: unknown }) => unknown) | undefined
                const content = fn
                  ? fn({ row })
                  : String(row?.[String(vn.props?.['prop'] ?? '')] ?? '')
                return h('td', { class: 'el-table__cell' }, content as never)
              }),
            ),
          )),
        ])
      }
    },
  })
  return { ElTable: SlotTable, 'el-table': SlotTable, ElTableColumn: SlotColumn, 'el-table-column': SlotColumn }
}

beforeEach(() => {
  setActivePinia(createPinia())
})

function mountList(embedded: boolean) {
  const store = useSourceGroupStore()
  store.items = []
  store.loading = false
  store.error = ''
  return mount(SourceGroupList, {
    props: { embedded },
    global: {
      plugins: [createPinia()],
      stubs: { PageHeader: { template: '<div class="page-header-stub" />' } },
    },
  })
}

describe('创建入口可达性', () => {
  it('embedded 模式下工具栏仍有「新建来源组」按钮', async () => {
    const w = mountList(true)
    await flushPromises()
    expect(
      w.find('[data-test="sg-create"]').exists(),
      'embedded 模式下缺少新建按钮 —— 用户将无法创建来源组',
    ).toBe(true)
  })

  it('非 embedded（独立路由）模式下也有新建按钮', async () => {
    const w = mountList(false)
    await flushPromises()
    expect(w.find('[data-test="sg-create"]').exists()).toBe(true)
  })

  it('空态里也提供创建入口（空表时用户注意力在居中盒子里）', async () => {
    const w = mountList(true)
    await flushPromises()
    expect(
      w.find('[data-test="sg-create-empty"]').exists(),
      '空态缺少创建入口 —— 新用户看到"还没有来源组"却无处可点',
    ).toBe(true)
  })

  it('点击新建按钮真的打开创建对话框（入口连上了动作）', async () => {
    const w = mountList(true)
    await flushPromises()
    await w.find('[data-test="sg-create"]').trigger('click')
    await flushPromises()
    expect(w.find('[data-test="sg-dialog"]').exists()).toBe(true)
  })

  it('空态的创建按钮与工具栏按钮是同一动作', async () => {
    const w = mountList(true)
    await flushPromises()
    await w.find('[data-test="sg-create-empty"]').trigger('click')
    await flushPromises()
    expect(w.find('[data-test="sg-dialog"]').exists()).toBe(true)
  })
})

describe('embedded 模式的页面级约束', () => {
  it('embedded=true 时不渲染 PageHeader（避免双标题）', async () => {
    const w = mountList(true)
    await flushPromises()
    expect(w.find('.page-header-stub').exists()).toBe(false)
  })

  it('embedded=false 时渲染 PageHeader', async () => {
    const w = mountList(false)
    await flushPromises()
    expect(w.find('.page-header-stub').exists()).toBe(true)
  })
})

describe('CRUD 操作入口（增删改查齐全）', () => {
  it('表格行内提供编辑与删除按钮', async () => {
    const store = useSourceGroupStore()
    store.items = [
      {
        id: 1,
        name: '主备组A',
        kind: 'primary_backup',
        category: 'rsoc',
        enabled: true,
        members: [],
      } as never,
    ]
    mockRows.value = store.items
    const w = mount(SourceGroupList, {
      props: { embedded: true },
      global: { plugins: [createPinia()], components: slotTable() },
    })
    await flushPromises()
    expect(w.find('[data-test="sg-edit"]').exists()).toBe(true)
    expect(w.find('[data-test="sg-delete"]').exists()).toBe(true)
  })

  it('主备组行内提供激活按钮；复合组不提供（无权威语义）', async () => {
    const store = useSourceGroupStore()
    store.items = [
      { id: 1, name: 'P', kind: 'primary_backup', category: 'a', enabled: true, members: [] } as never,
      { id: 2, name: 'C', kind: 'composite', category: 'b', enabled: true, members: [] } as never,
    ]
    mockRows.value = store.items
    const w = mount(SourceGroupList, {
      props: { embedded: true },
      global: { plugins: [createPinia()], components: slotTable() },
    })
    await flushPromises()
    // 只应有一个激活按钮（复合组那行不该有）
    expect(w.findAll('[data-test="sg-activate"]').length).toBe(1)
  })
})
