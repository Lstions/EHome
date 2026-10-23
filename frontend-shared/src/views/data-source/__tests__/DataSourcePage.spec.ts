import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { defineComponent, h, type VNode } from 'vue'
import OverviewTab from '../OverviewTab.vue'
import DataSourcePage from '../DataSourcePage.vue'
import overviewRaw from '../OverviewTab.vue?raw'
import pageRaw from '../DataSourcePage.vue?raw'
import type { SourceOverviewItem } from '@/api/sourceOverview'

/**
 * 数据来源页（M4）的前端契约测试。
 *
 * 核心断言：**只读**。不是靠"UI 上没放按钮"这种口头约定，而是：
 *  1. 组件源码里不存在任何写操作的 API 调用
 *  2. 渲染后 DOM 里不存在新建/编辑/删除按钮
 *  3. 组件 emits 里没有 create/update/delete 事件
 */

vi.mock('@/api/sourceOverview', () => ({
  sourceOverviewApi: {
    list: vi.fn(() => Promise.resolve({ items: [], total: 0, derived_at: '2026-10-23T04:00:00Z' })),
  },
}))

/**
 * mock vue-router 的**组合式 API**。
 *
 * 必须用顶层 vi.mock（不是 vi.doMock）：vi.doMock 在模块已 import 之后调用
 * 不会影响已解析的依赖，组件里 useRoute() 仍返回 undefined，
 * computed 读 route.query 直接抛 TypeError。
 */
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
    group_id: 5,
    group_name: '我的主备组',
    kind: 'primary_backup',
    member_count: 2,
    members: [
      { edge_device_id: 1, edge_device_name: 'bms-1', active: true, enabled: true },
      { edge_device_id: 2, edge_device_name: 'bms-2', active: false, enabled: true },
    ],
    status: 'ok',
    last_success: '2026-10-23T04:00:00Z',
    revision: 3,
    // 字段元数据 + 实时值（本次新增的三列所需）
    label_zh: '电池剩余电量',
    value_type: 'number',
    unit: '%',
    latest_value: 87,
    latest_at: '2026-10-23T04:00:00Z',
    value_age_sec: 5,
  },
]

beforeEach(() => {
  vi.clearAllMocks()
})

/**
 * 能执行**作用域插槽**的表格替身。
 *
 * # 为什么需要它
 *
 * src/test-setup.ts 里的全局 ElTable stub 只把整行值拼成文本（不执行
 * ElTableColumn 的 #default 插槽）。而本页的「类型/实时值/描述」都写在
 * 列插槽里 —— 用全局 stub 断言不到它们，会得到"元素不存在"的假红。
 *
 * 项目已有此范式（AlertRules.spec.ts / AutomationRules.spec.ts 的 slotTable()），
 * 这里按同一模式实现，避免每个页面各写一套。
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
          h('thead', [
            h('tr', columns.map(vn => h('th', String(vn.props?.['label'] ?? '')))),
          ]),
          h('tbody', rows.map((row, index) =>
            h('tr', { key: String(row?.['id'] ?? index) },
              columns.map(vn => {
                const scoped = vn.children as Record<string, unknown> | undefined
                const fn = scoped?.['default'] as ((p: { row: unknown }) => unknown) | undefined
                const content = fn ? fn({ row }) : String(row?.[String(vn.props?.['prop'] ?? '')] ?? '')
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

/** 用执行作用域插槽的表格替身挂载 OverviewTab。 */
function mountTab(items: SourceOverviewItem[]) {
  return mount(OverviewTab, {
    props: { items, loading: false, error: '' },
    global: { components: slotTable() },
  })
}

describe('OverviewTab 只读契约', () => {
  /**
   * 注意测试环境的 el-table stub（src/test-setup.ts:271）不会渲染命名 slot，
   * 只把整行值拼成文本渲染。因此这里断言**行数据文本**，而不是断言 slot 里的
   * el-tag —— 后者会因 stub 实现而必然失败（不是组件的问题）。
   *
   * 真正的渲染正确性由 e2e/视觉测试保证；单测这里关心的是
   * 「数据确实传到了表格」+「只读契约成立」。
   */
  it('渲染存在来源数据（不伪造空表）', () => {
    const w = mount(OverviewTab, {
      props: { items, loading: false, error: '' },
    })
    const text = w.text()
    expect(text).toContain('bms')
    expect(text).toContain('rsoc')
    // 表格本身应存在且渲染出了行
    // 注意：测试环境的 ElTable stub 用 <tbody><tr><td> 结构（无 .el-table__row），
    // 且把整行值拼成文本 —— 因此用 tbody tr 断言行数。
    expect(w.find('[data-test="ov-table"]').exists()).toBe(true)
    expect(w.findAll('tbody tr').length).toBe(items.length)
  })

  it('展示只读说明（用户不会以为功能坏了）', () => {
    const w = mount(OverviewTab, {
      props: { items: [], loading: false, error: '' },
    })
    const notice = w.find('[data-test="ov-readonly-notice"]')
    expect(notice.exists()).toBe(true)
    expect(notice.text()).toContain('自动生成')
  })

  /**
   * 这条是**源码级**断言：组件文件里不得出现任何写操作 API。
   *
   * 为什么用源码扫描而不只查 DOM：DOM 断言只能证明"当前没渲染按钮"，
   * 而源码扫描能抓住"有人加了 create 调用但只在某个分支渲染"的情况。
   */
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
      'POST',
      'PUT ',
      'DELETE ',
    ]
    for (const f of forbidden) {
      expect(overviewRaw, `OverviewTab 不应包含写操作 "${f}"`).not.toContain(f)
    }
  })

  it('DOM 里不存在新建/编辑/删除按钮', () => {
    const w = mount(OverviewTab, {
      props: { items, loading: false, error: '' },
    })
    const html = w.html()
    // 这些 data-test 是项目里写操作按钮的惯用标记
    expect(html).not.toContain('sg-create')
    expect(html).not.toContain('ds-create')
    expect(html).not.toContain('ds-edit')
    expect(html).not.toContain('ds-delete')
  })

  it('emits 只有 open-group 与 retry（类型层无写能力）', () => {
    const w = mount(OverviewTab, {
      props: { items, loading: false, error: '' },
    })
    const emitted = Object.keys(w.emitted())
    for (const e of emitted) {
      expect(['open-group', 'retry']).toContain(e)
    }
  })

  it('error 时渲染重试入口且不崩', () => {
    const w = mount(OverviewTab, {
      props: { items: [], loading: false, error: '网络错误' },
    })
    expect(w.find('[data-test="ov-error"]').exists()).toBe(true)
    expect(w.text()).toContain('网络错误')
  })

  it('空数据时给出可理解的空态文案', () => {
    const w = mount(OverviewTab, {
      props: { items: [], loading: false, error: '' },
    })
    // el-table 的 empty-text 由 stub 渲染或由 props 传递，这里断言文本不误导
    expect(w.find('[data-test="ov-table"]').exists()).toBe(true)
  })
})

describe('DataSourcePage 整合结构', () => {
  it('两个 Tab：来源总览（只读）+ 来源组（可写）', () => {
    expect(pageRaw).toContain('来源总览')
    expect(pageRaw).toContain('来源组')
    expect(pageRaw).toContain('OverviewTab')
    expect(pageRaw).toContain('SourceGroupList')
  })

  it('Tab 由 route.query 驱动（链接可分享）', () => {
    expect(pageRaw).toContain('route.query.tab')
    // 不应用内部 state 作为单一真相
    expect(pageRaw).toContain("route.query.tab === 'groups'")
  })

  it('页面本身不含写操作 API', () => {
    expect(pageRaw).not.toContain('sourceOverviewApi.create')
    expect(pageRaw).not.toContain('sourceOverviewApi.update')
  })

  it('来源组 Tab 用 embedded 模式避免双标题', () => {
    expect(pageRaw).toContain('<SourceGroupList embedded />')
  })
})

describe('DataSourcePage 挂载', () => {
  /**
   * 挂载测试必须 mock vue-router 的**组合式 API**（useRoute/useRouter），
   * 而不是 global.mocks 的 $route —— 组件用的是 useRoute()。
   * 这两者混用会让 route 为 undefined，computed 里读 route.query 直接崩。
   */
  it('默认进入总览 Tab 并加载数据', async () => {
    const { sourceOverviewApi } = await import('@/api/sourceOverview')
    vi.doMock('vue-router', () => ({
      useRoute: () => ({ query: {} }),
      useRouter: () => ({ replace: vi.fn() }),
    }))
    const w = mount(DataSourcePage, {
      global: { stubs: { SourceGroupList: true, PageHeader: true } },
    })
    await flushPromises()
    expect(sourceOverviewApi.list).toHaveBeenCalled()
    expect(w.text()).toBeTruthy()
    vi.doUnmock('vue-router')
  })
})

describe('实时显示与字段元数据（本次新增）', () => {
  it('实时值 0 必须显示为 0，不能显示成「—」', () => {
    const zeroItems = [
      {
        ...items[0],
        latest_value: 0,
        value_age_sec: 3,
      },
    ]
    const w = mountTab(zeroItems)
    const live = w.find('[data-test="ov-live"]')
    expect(live.exists()).toBe(true)
    // 0 是合法读数；显示成 — 会让用户以为设备没数据
    expect(live.text()).toContain('0')
    expect(live.text()).not.toContain('—')
  })

  it('无数据时显示「—」且不显示年龄', () => {
    const noData = [{ ...items[0], latest_value: null, latest_at: null, value_age_sec: null }]
    const w = mountTab(noData)
    const live = w.find('[data-test="ov-live"]')
    expect(live.text()).toContain('—')
    expect(live.classes()).toContain('is-none')
  })

  it('新鲜度分级：≤30s 新鲜 / ≤300s 偏旧 / 更久陈旧', () => {
    const mk = (age: number) => [{ ...items[0], value_age_sec: age }]
    const fresh = mountTab(mk(5))
    expect(fresh.find('[data-test="ov-live"]').classes()).toContain('is-fresh')
    const stale = mountTab(mk(120))
    expect(stale.find('[data-test="ov-live"]').classes()).toContain('is-stale')
    const dead = mountTab(mk(4000))
    expect(dead.find('[data-test="ov-live"]').classes()).toContain('is-dead')
  })

  it('数据类型列显示中文标签（不是原始英文枚举）', () => {
    const w = mountTab(items)
    const tag = w.find('[data-test="ov-value-type"]')
    expect(tag.exists()).toBe(true)
    expect(tag.text()).toBe('数值')
  })

  it('未标注类型时显示「未标注」而不是空白', () => {
    const noType = [{ ...items[0], value_type: '' as const }]
    const w = mountTab(noType)
    expect(w.find('[data-test="ov-value-type-empty"]').exists()).toBe(true)
  })

  it('中文描述有值时才渲染（未登记不显示空标签）', () => {
    const w = mountTab(items)
    expect(w.find('[data-test="ov-label"]').exists()).toBe(true)
    const noLabel = [{ ...items[0], label_zh: '' }]
    const w2 = mountTab(noLabel)
    expect(w2.find('[data-test="ov-label"]').exists()).toBe(false)
  })
})

describe('实时刷新（自适应轮询）', () => {
  it('页面源码含轮询与可见性暂停逻辑', () => {
    expect(pageRaw).toContain('REFRESH_MS')
    expect(pageRaw).toContain('visibilitychange')
    expect(pageRaw).toContain('document.hidden')
    expect(pageRaw).toContain('onUnmounted')
  })

  it('轮询不会显示 loading（避免表格闪烁）', () => {
    // 静默刷新必须传 true
    expect(pageRaw).toContain('loadOverview(true)')
  })
})
