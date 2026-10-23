import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { defineComponent, h, type VNode } from 'vue'
import OverviewTab from '../OverviewTab.vue'
import DataSourcePage from '../DataSourcePage.vue'
import overviewRaw from '../OverviewTab.vue?raw'
import pageRaw from '../DataSourcePage.vue?raw'
import type { SourceOverviewItem } from '@/api/sourceOverview'

/**
 * 数据来源页的契约测试。
 *
 * 两条主线：
 *  1. **只读**：组件源码无写 API、DOM 无写按钮、后端写方法 405。
 *  2. **列语义正确**：只展示字段事实，不展示主备配置概念。
 *
 * 第 2 条来自一次真实的设计错误纠正（2026-10-23）：早期版本把「来源类型/
 * 成员/当前来源/来源组」四列渲染到只读字段目录里 —— 那些属于主备配置，
 * 且「当前来源」与第一列「逻辑设备」完全重复。本文件用**反向断言**锁住。
 */

vi.mock('@/api/sourceOverview', () => ({
  sourceOverviewApi: {
    list: vi.fn(() =>
      Promise.resolve({ items: [], total: 0, generated_at: '2026-10-23T04:00:00Z' }),
    ),
  },
}))

const { mockReplace } = vi.hoisted(() => ({ mockReplace: vi.fn() }))
vi.mock('vue-router', () => ({
  useRoute: () => ({ query: {} }),
  useRouter: () => ({ replace: mockReplace }),
}))

const items: SourceOverviewItem[] = [
  {
    logical_device_id: 14,
    logical_device_name: 'bms',
    category: 'rsoc',
    label_zh: '剩余电量百分比',
    value_type: 'number',
    unit: '%',
    latest_value: 87,
    updated_at: '2026-10-23T04:00:00Z',
    age_sec: 5,
    status: 'ok',
  },
]

/**
 * 能执行**作用域插槽**的表格替身。
 *
 * 全局 ElTable stub 只把整行值拼成文本、不执行 ElTableColumn 的 #default 插槽，
 * 因此断言不到列插槽内容。项目已有此范式（AlertRules.spec.ts 的 slotTable()）。
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
          h('thead', [h('tr', columns.map(vn => h('th', String(vn.props?.['label'] ?? ''))))]),
          h(
            'tbody',
            rows.map((row, index) =>
              h(
                'tr',
                { key: String(row?.['id'] ?? index) },
                columns.map(vn => {
                  const scoped = vn.children as Record<string, unknown> | undefined
                  const fn = scoped?.['default'] as ((p: { row: unknown }) => unknown) | undefined
                  const content = fn
                    ? fn({ row })
                    : String(row?.[String(vn.props?.['prop'] ?? '')] ?? '')
                  return h('td', { class: 'el-table__cell' }, content as never)
                }),
              ),
            ),
          ),
        ])
      }
    },
  })
  return { ElTable: SlotTable, 'el-table': SlotTable, ElTableColumn: SlotColumn, 'el-table-column': SlotColumn }
}

function mountTab(list: SourceOverviewItem[]) {
  return mount(OverviewTab, {
    props: { items: list, loading: false, error: '' },
    global: { components: slotTable() },
  })
}

beforeEach(() => {
  vi.clearAllMocks()
})

describe('OverviewTab 只读契约', () => {
  it('渲染字段数据', () => {
    const w = mountTab(items)
    const text = w.text()
    expect(text).toContain('bms')
    expect(text).toContain('rsoc')
    expect(text).toContain('剩余电量百分比')
    expect(w.find('[data-test="ov-table"]').exists()).toBe(true)
  })

  it('展示只读说明（用户不会以为功能坏了）', () => {
    const w = mountTab([])
    const notice = w.find('[data-test="ov-readonly-notice"]')
    expect(notice.exists()).toBe(true)
    expect(notice.text()).toContain('只读')
  })

  it('源码里不存在任何写操作 API 调用', () => {
    const forbidden = [
      'sourceOverviewApi.create',
      'sourceOverviewApi.update',
      'sourceOverviewApi.delete',
      'dataSourceApi.create',
      'dataSourceApi.update',
      'dataSourceApi.delete',
      'activate',
      'deactivate',
    ]
    for (const f of forbidden) {
      expect(overviewRaw, 'OverviewTab 不应包含写操作 ' + f).not.toContain(f)
    }
  })

  it('DOM 里不存在新建/编辑/删除按钮', () => {
    const html = mountTab(items).html()
    expect(html).not.toContain('sg-create')
    expect(html).not.toContain('ds-create')
    expect(html).not.toContain('ds-edit')
    expect(html).not.toContain('ds-delete')
  })

  it('emits 只有 open-group 与 retry', () => {
    const w = mountTab(items)
    for (const e of Object.keys(w.emitted())) {
      expect(['open-group', 'retry']).toContain(e)
    }
  })
})

describe('列语义：只展示字段事实，不展示主备配置概念', () => {
  it('不得出现「来源类型 / 成员 / 当前来源 / 来源组」列', () => {
    const cols = ['来源类型', '成员', '当前来源', '来源组']
    for (const c of cols) {
      expect(overviewRaw, '不应有列 ' + c).not.toContain('label="' + c + '"')
    }
  })

  it('不得出现「直连 / 单设备直连 / 权威」这些主备术语', () => {
    for (const term of ['单设备直连', '权威']) {
      expect(overviewRaw, '不应出现主备术语 ' + term).not.toContain(term)
    }
  })

  it('列集合就是字段事实那 7 列', () => {
    for (const c of ['逻辑设备', '数据类别', '数据类型', '单位', '实时值', '更新时间', '状态']) {
      expect(overviewRaw, '缺少列 ' + c).toContain('label="' + c + '"')
    }
  })
})

describe('实时值显示', () => {
  it('值、单位、年龄不粘连（单位独立成列）', () => {
    const w = mountTab(items)
    const val = w.find('[data-test="ov-value"]')
    expect(val.exists()).toBe(true)
    expect(val.text()).toBe('87')
    expect(val.text()).not.toContain('%')
    expect(val.text()).not.toContain('s')
    expect(w.html()).toContain('%')
  })

  it('实时值 0 必须显示为 0，不能显示成「—」', () => {
    const w = mountTab([{ ...items[0], latest_value: 0, age_sec: 3 }])
    expect(w.find('[data-test="ov-value"]').text()).toBe('0')
  })

  it('无数据时显示「—」', () => {
    const w = mountTab([
      { ...items[0], latest_value: null, updated_at: null, age_sec: null, status: 'nodata' },
    ])
    expect(w.find('[data-test="ov-value"]').text()).toBe('—')
  })

  it('小数保留至多 3 位（去浮点噪声）', () => {
    const w = mountTab([{ ...items[0], latest_value: 3.3530000000001 }])
    expect(w.find('[data-test="ov-value"]').text()).toBe('3.353')
  })
})

describe('更新时间与状态', () => {
  it('更新时间同时给出相对年龄（可感知新鲜度）', () => {
    const w = mountTab(items)
    expect(w.find('[data-test="ov-updated"]').text()).toContain('前')
  })

  it('状态按新鲜度分级：正常 / 延迟 / 陈旧 / 无数据', () => {
    const cases: Array<[string, string]> = [
      ['ok', '正常'],
      ['delay', '延迟'],
      ['stale', '陈旧'],
      ['nodata', '无数据'],
    ]
    for (const pair of cases) {
      const status = pair[0] as SourceOverviewItem['status']
      const label = pair[1]
      const w = mountTab([{ ...items[0], status }])
      expect(w.find('[data-test="ov-status"]').text()).toBe(label)
    }
  })
})

describe('DataSourcePage 整合结构', () => {
  it('两个 Tab：字段目录（只读）+ 来源组（可写）', () => {
    expect(pageRaw).toContain('OverviewTab')
    expect(pageRaw).toContain('SourceGroupList')
  })

  it('Tab 由 route.query 驱动（链接可分享）', () => {
    expect(pageRaw).toContain('route.query.tab')
    expect(pageRaw).toContain("route.query.tab === 'groups'")
  })

  it('来源组 Tab 用 embedded 模式避免双标题', () => {
    expect(pageRaw).toContain('<SourceGroupList embedded />')
  })
})

describe('实时刷新（自适应轮询）', () => {
  it('含轮询与可见性暂停逻辑', () => {
    expect(pageRaw).toContain('REFRESH_MS')
    expect(pageRaw).toContain('visibilitychange')
    expect(pageRaw).toContain('document.hidden')
    expect(pageRaw).toContain('onUnmounted')
  })

  it('轮询静默刷新（不闪 loading）', () => {
    expect(pageRaw).toContain('loadOverview(true)')
  })
})

describe('DataSourcePage 挂载', () => {
  it('默认进入字段目录并加载数据', async () => {
    const { sourceOverviewApi } = await import('@/api/sourceOverview')
    const w = mount(DataSourcePage, {
      global: { stubs: { SourceGroupList: true, PageHeader: true } },
    })
    await flushPromises()
    expect(sourceOverviewApi.list).toHaveBeenCalled()
    expect(w.text()).toBeTruthy()
  })
})
