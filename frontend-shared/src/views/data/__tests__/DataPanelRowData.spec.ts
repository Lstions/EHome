import { describe, it, expect, vi, beforeEach } from 'vitest'
import { nextTick } from 'vue'
import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { defineComponent, h, type VNode } from 'vue'
import DataPanel, { parseRowData } from '@/views/data/DataPanel.vue'
import { aggregateHistoryRows } from '@/views/data/historyRows'
import { UNKNOWN } from '@/utils/format'
import { edgeDeviceApi } from '@/api/edgeDevice'
import type { UnifiedDataRow } from '@/api/edgeDevice'

/**
 * DataPanel「历史数据」表格的回归锁（**2026-11-11 原始数据退役后重写**）。
 *
 * ============ 历史（为什么曾经有这一整个文件）============
 *
 * 改造前表格数据源是 `GET /edge-devices/:id/data`（device_data 分页）。
 * 那个接口只返回 data_json，而表格读 row.parsed_data || row.data / row.raw_data
 * ⇒ 恒 undefined ⇒ 「数据」「原始数据」两列**恒显示 —**（实测 2026-09-17）。
 * 于是有了两层用例：① parseRowData 纯函数层；② 真实渲染层（必须执行
 * el-table-column 的 #default 插槽 —— 只断言函数等于不守渲染路径）。
 *
 * ============ 现在（device_data 整表删除）============
 *
 * 数据源换成 `GET /devices/:id/history`，响应是 **一字段一行**（[]UnifiedData）。
 * 表格要的是"一次采集一行"，所以中间多了一步 aggregateHistoryRows()。
 * 于是本文件的关注点变成：
 *
 *   ① **聚合才是新的最易错点**：把「同一 timestamp 的 N 行字段」合成一行。
 *      合错了表格会莫名其妙多行/少行，而且看不出错。
 *   ② 渲染层仍要守：单元格必须显示真实值，取不到才显示「—」。
 *   ③ 「原始数据」「状态」两列已删除 —— 用例要钉住它们**不会**被加回来
 *      （加回来就是指向不存在的数据源）。
 *
 * 为什么不用 src/test-setup.ts 的全局 ElTable stub：它把整行 Object.values
 * 拍平成一个 <td>，列插槽根本不执行 ⇒ "单元格恒显示 —" 这类缺陷在测试里
 * 永远看不见。下面的替身与 views/alert/__tests__/AlertRules.spec.ts 的
 * slotTable() 同一范式。
 */

// ─── /devices/:id/history 的真实行形状（一字段一行）────────────────────────
const TS = '2026-09-17T07:01:49.271798Z'

/** 一次采集：同一 timestamp 的三行字段（后端整帧共用一个 now） */
const ONE_COLLECTION: UnifiedDataRow[] = [
  { id: 11, device_id: 42, sensor_name: 'rainfall', value: 0.5, unit: 'mm', timestamp: TS, created_at: TS },
  { id: 12, device_id: 42, sensor_name: 'temperature', value: 21.5, unit: '°C', timestamp: TS, created_at: TS },
  { id: 13, device_id: 42, sensor_name: 'humidity', value: 60, unit: '%', timestamp: TS, created_at: TS },
]

const row = (sensorName: string, value: number, timestamp: string, id: number): UnifiedDataRow => ({
  id, device_id: 42, sensor_name: sensorName, value, unit: '', timestamp, created_at: timestamp,
})

// ── API / store mocks（与 DataPanel.spec.ts 同一套，缺一即页面挂载失败）──
vi.mock('@/api/edgeDevice', () => ({
  edgeDeviceApi: {
    getList: vi.fn(() => Promise.resolve({ total: 0, items: [] })),
    getDetail: vi.fn(() => Promise.resolve({})),
    create: vi.fn(() => Promise.resolve({ id: 1 })),
    update: vi.fn(() => Promise.resolve()),
    delete: vi.fn(() => Promise.resolve()),
    getDeviceHistory: vi.fn(() => Promise.resolve([])),
    getLatestValues: vi.fn(() => Promise.resolve([])),
  },
}))

vi.mock('@/stores/edgeDevice', () => ({
  useEdgeDeviceStore: () => ({
    list: [{ id: 42, name: '设备 42' }],
    listTotal: 1,
    listLoading: false,
    fetchList: vi.fn(() => Promise.resolve()),
    getCachedList: vi.fn(() => ({ items: [{ id: 42, name: '设备 42' }] })),
    deleteDevice: vi.fn(() => Promise.resolve()),
    clearCache: vi.fn(),
    isFresh: vi.fn(() => false),
  }),
}))

const { mockSubscribe } = vi.hoisted(() => ({
  // 显式标出订阅签名，测试里才能安全取出 handler 并模拟一次 WS 推送
  mockSubscribe: vi.fn((_type: string, _handler: (message: unknown) => void) => vi.fn()),
}))
vi.mock('@/stores/websocket', () => ({
  useWebSocketStore: () => ({ subscribe: mockSubscribe, isConnected: false, statusMessage: '' }),
}))

const { mockClientGet } = vi.hoisted(() => ({
  mockClientGet: vi.fn<(url: string, config?: unknown) => Promise<unknown>>(() => Promise.resolve([])),
}))
vi.mock('@/api/client', () => ({
  default: {
    get: mockClientGet,
    post: vi.fn(() => Promise.resolve({ data: {} })),
    put: vi.fn(() => Promise.resolve()),
    delete: vi.fn(() => Promise.resolve()),
  },
}))

vi.mock('@/utils/feedback', () => ({
  default: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
  feedback: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}))
vi.mock('@/utils/exportData', () => ({ exportCSV: vi.fn(), exportJSON: vi.fn() }))
// A5：DataPanel 在 onMounted 里消费 `?device=`/`?range=`，故必须有 vue-router 替身。
// 缺它时 useRoute() 返回 undefined → applyQueryPrefill 抛 "Cannot read properties of
// undefined (reading 'query')"，表现为**未处理拒绝**（测试仍绿、但 vitest 退出码 1）。
vi.mock('vue-router', () => ({
  useRoute: () => ({ query: {}, params: {}, name: 'DataPanel', path: '/data' }),
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
}))
vi.mock('@/utils/downsample', () => ({ downsampleData: vi.fn((data: unknown[]) => data) }))
vi.mock('@/utils/sensor', () => ({
  sensorNameMap: { rainfall: '雨量', temperature: '温度', humidity: '湿度' },
  sensorUnitMap: { rainfall: 'mm', temperature: '°C', humidity: '%' },
}))
vi.mock('@/utils/logger', () => ({
  logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() },
}))

// ── 执行列插槽的表格替身（仅本文件使用）────────────────────────────────────
function slotTable() {
  const defaultSlotOf = (vn: VNode) => {
    const children = vn.children
    if (children && typeof children === 'object' && !Array.isArray(children)) {
      const slot = (children as Record<string, unknown>)['default']
      if (typeof slot === 'function') return slot as (scope: { row: unknown }) => VNode
    }
    return null
  }
  const SlotColumn = defineComponent({
    name: 'ElTableColumn',
    props: { prop: String, label: String, width: [String, Number], align: String, type: String },
    setup() {
      return () => null
    },
  })
  const SlotTable = defineComponent({
    name: 'ElTable',
    props: { data: { type: Array, default: () => [] } },
    setup(props, { slots }) {
      return () => {
        // ⚠ 过滤掉注释/文本 VNode：模板里写在 <el-table> 内部的 HTML 注释会被
        // 当作子节点传进插槽（真实 el-table 忽略它们）。不过滤就会多出一个
        // 没有 label 的「幽灵列」，把"只剩两列"的断言搞成假红。
        const columns = ((slots.default?.() ?? []) as VNode[])
          .filter((vn) => vn && typeof vn === 'object' && vn.props && 'label' in (vn.props as object))
        const rows = props.data as Array<Record<string, unknown>>
        return h('table', { class: 'el-table' }, [
          h('tbody', rows.map((r, index) => h('tr', { key: String(r?.id ?? index) }, columns.map((vn) => {
            const scoped = defaultSlotOf(vn)
            const content = scoped
              ? scoped({ row: r })
              : String(r?.[String(vn.props?.['prop'] ?? '')] ?? '')
            return h('td', { class: 'el-table__cell', 'data-label': String(vn.props?.['label'] ?? '') }, content)
          })))),
        ])
      }
    },
  })
  return {
    ElTable: SlotTable,
    'el-table': SlotTable,
    ElTableColumn: SlotColumn,
    'el-table-column': SlotColumn,
    'el-card': { template: '<div class="el-card"><slot /><slot name="header" /></div>' },
    ElCard: { template: '<div class="el-card"><slot /><slot name="header" /></div>' },
    'el-form': { template: '<div class="el-form"><slot /></div>' },
    ElForm: { template: '<div class="el-form"><slot /></div>' },
    'el-form-item': { template: '<div class="el-form-item"><slot /></div>' },
    ElFormItem: { template: '<div class="el-form-item"><slot /></div>' },
    'el-button': {
      template: '<button class="el-button" :disabled="disabled" @click="$emit(\'click\')"><slot /><slot name="icon" /></button>',
      props: ['disabled', 'loading', 'type', 'size'],
      emits: ['click'],
    },
    ElButton: {
      template: '<button class="el-button" :disabled="disabled" @click="$emit(\'click\')"><slot /><slot name="icon" /></button>',
      props: ['disabled', 'loading', 'type', 'size'],
      emits: ['click'],
    },
    'el-icon': { template: '<i class="el-icon"><slot /></i>' },
    ElIcon: { template: '<i class="el-icon"><slot /></i>' },
    'el-skeleton': { template: '<div class="el-skeleton" />' },
    ElSkeleton: { template: '<div class="el-skeleton" />' },
    'el-tag': { template: '<span class="el-tag"><slot /></span>' },
    ElTag: { template: '<span class="el-tag"><slot /></span>' },
    'el-alert': { template: '<div class="el-alert" :data-testid="$attrs[\'data-testid\']">{{ title }}{{ description }}</div>', props: ['title', 'description', 'type', 'closable', 'showIcon'] },
    ElAlert: { template: '<div class="el-alert">{{ title }}{{ description }}</div>', props: ['title', 'description', 'type', 'closable', 'showIcon'] },
    'el-empty': { template: '<div class="el-empty"><slot /></div>' },
    ElEmpty: { template: '<div class="el-empty"><slot /></div>' },
    'el-tooltip': { template: '<span class="el-tooltip"><slot /></span>' },
    ElTooltip: { template: '<span class="el-tooltip"><slot /></span>' },
    'el-checkbox-group': { template: '<div class="el-checkbox-group"><slot /></div>' },
    ElCheckboxGroup: { template: '<div class="el-checkbox-group"><slot /></div>' },
    'el-checkbox': { template: '<label class="el-checkbox"><slot /></label>' },
    ElCheckbox: { template: '<label class="el-checkbox"><slot /></label>' },
  }
}

/**
 * 必须走 stubs 而不是 components 的几个：DataPanel 里的 PageHeader / LineChart /
 * 图标都是**本地注册**的（import 后直接用在模板里），本地注册优先于
 * global.components ⇒ 放 components 里不生效。真实 LineChart 在 happy-dom 里
 * 没有 canvas，mounted 会抛（zrender Layer.initContext），整个 mount 随之失败。
 */
const componentStubs = {
  PageHeader: { template: '<div class="page-header"><slot /><slot name="extra" /></div>' },
  LineChart: { template: '<div class="line-chart" />' },
  Download: { template: '<i />' },
  Connection: { template: '<i />' },
  DataAnalysis: { template: '<i />' },
  DocumentChecked: { template: '<i />' },
  Timer: { template: '<i />' },
  Cpu: { template: '<i />' },
  WarningFilled: { template: '<i />' },
}

/** 以给定历史响应（一字段一行）挂载并触发一次查询；返回挂载后的 wrapper。 */
async function mountWithHistory(rows: UnifiedDataRow[]) {
  vi.mocked(edgeDeviceApi.getDeviceHistory).mockResolvedValue(rows)
  mockClientGet.mockImplementation((url: string) => {
    if (url === '/api/v1/unified-data/categories') {
      return Promise.resolve([{ code: 'rainfall', unit: 'mm' }])
    }
    if (url === '/api/v1/unified-data/historical-batch') {
      return Promise.resolve([{ category: 'rainfall', data: [{ timestamp: TS, value: 0.5 }] }])
    }
    return Promise.resolve([])
  })

  const pinia = createPinia()
  setActivePinia(pinia)
  const wrapper = mount(DataPanel, {
    global: {
      plugins: [pinia],
      components: slotTable(),
      stubs: componentStubs,
    },
  })
  await flushPromises()

  const selects = wrapper.findAll('select.el-select')
  await selects[0].setValue('42')
  await wrapper.find('[data-test="query"]').trigger('click')
  await flushPromises()
  await flushPromises()
  return wrapper
}

const cellText = (wrapper: VueWrapper, label: string) =>
  wrapper.find(`td[data-label="${label}"]`).text()

// ═══════════════════════════════════════════════════════════════════════
// ① aggregateHistoryRows —— 一字段一行 → 一次采集一行
// ═══════════════════════════════════════════════════════════════════════
describe('aggregateHistoryRows —— 一字段一行聚合成一次采集一行', () => {
  it('同一 timestamp 的多行字段合成一行，指标在 data 里', () => {
    const rows = aggregateHistoryRows(ONE_COLLECTION)
    expect(rows).toHaveLength(1)
    expect(rows[0].data).toEqual({ rainfall: 0.5, temperature: 21.5, humidity: 60 })
    expect(rows[0].timestamp).toBe(TS)
  })

  it('行 id 取该采集里最小的后端 id（稳定可当 v-for key）', () => {
    expect(aggregateHistoryRows(ONE_COLLECTION)[0].id).toBe(11)
  })

  it('不同 timestamp 各成一行，按时间**倒序**（最新在前）', () => {
    const rows = aggregateHistoryRows([
      row('rainfall', 1, '2026-09-17T07:00:00Z', 1),
      row('rainfall', 2, '2026-09-17T07:02:00Z', 2),
      row('rainfall', 3, '2026-09-17T07:01:00Z', 3),
    ])
    expect(rows.map(r => r.data.rainfall)).toEqual([2, 3, 1])
  })

  it('⚠ 关键：1Hz 连续上报**不得被合并**（相邻采集只差 1 秒也各成一行）', () => {
    // 这是一条防"±2 秒模糊合并"的回归锁。BMS 以 1Hz 上报，模糊合并会把
    // 两次真实采集并成一行 —— 表格莫名少行，且丢数据完全不可见。
    const rows = aggregateHistoryRows([
      row('total_voltage', 52.4, '2026-09-17T07:00:00.000000001Z', 1),
      row('total_voltage', 52.5, '2026-09-17T07:00:01.000000001Z', 2),
      row('total_voltage', 52.6, '2026-09-17T07:00:02.000000001Z', 3),
    ])
    expect(rows).toHaveLength(3)
    expect(rows.map(r => r.data.total_voltage)).toEqual([52.6, 52.5, 52.4])
  })

  it('逆变器的 9 条命令各是一次解析 ⇒ 表格里就是 9 行（不合并）', () => {
    // 每条约间隔 250ms，都不是"同一次解析"（各自独立 now）。
    const rows = aggregateHistoryRows([
      row('grid_voltage', 220, '2026-09-17T07:00:00.000Z', 1),
      row('output_voltage', 219, '2026-09-17T07:00:00.250Z', 2),
      row('battery_voltage', 26, '2026-09-17T07:00:00.500Z', 3),
    ])
    expect(rows).toHaveLength(3)
  })

  it('值是 0 时保留 0（不是丢弃）——「真的是 0」必须可与「取不到」区分', () => {
    const rows = aggregateHistoryRows([row('rainfall', 0, TS, 1)])
    expect(rows).toHaveLength(1)
    expect(rows[0].data.rainfall).toBe(0)
  })

  it('缺 timestamp / 缺 sensor_name / value 非有限数值的行被丢弃（不伪造采集）', () => {
    const rows = aggregateHistoryRows([
      { ...row('rainfall', 1, TS, 1) },
      { ...row('', 1, TS, 2) },
      { ...row('rainfall', 1, '', 3) },
      { ...row('humidity', NaN, TS, 4) },
      { ...row('humidity', Infinity, TS, 5) },
    ] as UnifiedDataRow[])
    // 只剩第一条：同一 TS 下其余行都被丢弃
    expect(rows).toHaveLength(1)
    expect(rows[0].data).toEqual({ rainfall: 1 })
  })

  it('空输入 / 非数组：返回空数组，不抛异常', () => {
    expect(aggregateHistoryRows([])).toEqual([])
    expect(aggregateHistoryRows(undefined as unknown as UnifiedDataRow[])).toEqual([])
  })
})

// ═══════════════════════════════════════════════════════════════════════
// ② parseRowData —— 本页唯一的行取数入口（纯函数层）
// ═══════════════════════════════════════════════════════════════════════
describe('parseRowData —— 本页唯一的行取数入口（纯函数层）', () => {
  it('聚合后的历史行：从 data 取出全部指标', () => {
    const parsed = parseRowData(aggregateHistoryRows(ONE_COLLECTION)[0])
    expect(parsed.values).toEqual({ rainfall: 0.5, temperature: 21.5, humidity: 60 })
    expect(parsed.numbers).toEqual({ rainfall: 0.5, temperature: 21.5, humidity: 60 })
  })

  it('值是 0 时得到 0（不是 null）', () => {
    const parsed = parseRowData({ data: { rainfall: 0 } })
    expect(parsed.values).toEqual({ rainfall: 0 })
    expect(parsed.values).not.toBeNull()
  })

  it('行本身缺失/非对象：不抛异常，values 与 numbers 均为 null', () => {
    for (const r of [null, undefined, 'x', 42, true]) {
      expect(() => parseRowData(r)).not.toThrow()
      expect(parseRowData(r)).toEqual({ values: null, numbers: null })
    }
  })

  it('空对象 / 空 data：values 为 null（不是空对象）', () => {
    expect(parseRowData({}).values).toBeNull()
    expect(parseRowData({ data: {} }).values).toBeNull()
    expect(parseRowData({ data: [] }).values).toBeNull()
  })

  it('兼容扁平形状 { rainfall: 0.5 }（既有 fixture 走这里）', () => {
    expect(parseRowData({ rainfall: 0.5 }).values).toEqual({ rainfall: 0.5 })
  })

  it('元数据字段不被当成读数（id / device_id / timestamp ...）', () => {
    const parsed = parseRowData({
      id: 1, device_id: 42, node_id: 'X', channel_id: 2,
      timestamp: TS, created_at: TS,
      data: { rainfall: 0.5 },
    })
    expect(parsed.values).toEqual({ rainfall: 0.5 })
  })

  it('字符串数值（"0.5"）不被静默转成数字：原文保留，但绝不进 numbers', () => {
    // 判据是"不得做 Number(str)"，而不是"必须丢弃"：把后端给的文本原样展示
    // 是诚实的，静默转成 0.5 才是伪造。numbers 必须排除它 —— 否则 "V19"
    // 会被当 0 拉低均值、并在趋势图上画出掉到 0 的假线。
    const parsed = parseRowData({ data: { rainfall: '0.5' } })
    expect(parsed.values?.rainfall).toBe('0.5')
    expect(parsed.numbers).toBeNull()
  })

  it('字符串读数（真值本就是文本）保留为字符串，但不进 numbers', () => {
    // 实时 WS 推送可能带文本字段；只认数字会把真实读数丢掉显示成 —
    const parsed = parseRowData({ data: { hardware_version: 'V19', rainfall: 0.5 } })
    expect(parsed.values).toEqual({ hardware_version: 'V19', rainfall: 0.5 })
    expect(parsed.numbers).toEqual({ rainfall: 0.5 })
  })

  it('只有字符串读数时 numbers 为 null（趋势/统计不得画出掉到 0 的假线）', () => {
    const parsed = parseRowData({ data: { sn: 'SIM-BMS-0001' } })
    expect(parsed.values?.sn).toBe('SIM-BMS-0001')
    expect(parsed.numbers).toBeNull()
  })
})

// ═══════════════════════════════════════════════════════════════════════
// ③ 真实渲染层 —— 单元格必须显示真实值；删掉的列不得回来
// ═══════════════════════════════════════════════════════════════════════
describe('DataPanel 历史表渲染 —— 真实行不得再显示「—」', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    // 清掉 vi.clearAllMocks 后丢失的默认实现
    mockSubscribe.mockImplementation(() => vi.fn())
  })

  it('「数据」列显示该次采集的全部指标（rainfall: 0.50 等）', async () => {
    const wrapper = await mountWithHistory(ONE_COLLECTION)

    expect(cellText(wrapper, '数据')).not.toBe(UNKNOWN)
    expect(cellText(wrapper, '数据')).toContain('rainfall: 0.50')
    expect(cellText(wrapper, '数据')).toContain('temperature: 21.50')
    expect(wrapper.find('[data-testid="row-values"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="values-unknown"]').exists()).toBe(false)
  })

  it('一次采集在表格里只占**一行**（三行字段被聚合）', async () => {
    const wrapper = await mountWithHistory(ONE_COLLECTION)
    expect(wrapper.findAll('tbody tr')).toHaveLength(1)
  })

  it('⚠ 「原始数据」「状态/错误码」两列已删除（不得被加回来）', async () => {
    // 原始数据列的 raw_hex 随 device_data 退役已无数据源；
    // 状态列的 error_code 后端从未返回（既有 bug，一直恒显示「正常」）。
    const wrapper = await mountWithHistory(ONE_COLLECTION)
    // 只看历史表（弹层里的详情表也是 el-table，不排除会把它的列算进来）
    const labels = wrapper.find('table.el-table').findAll('td.el-table__cell')
      .map((c) => c.attributes('data-label'))
    expect(labels).not.toContain('原始数据')
    expect(labels).not.toContain('状态')
    // 只有「采集时间」「数据」两列
    expect([...new Set(labels)]).toEqual(['采集时间', '数据'])
  })

  it('真的是 0 的行显示 rainfall: 0.00 —— 与"取不到"的 — 可区分', async () => {
    const wrapper = await mountWithHistory([row('rainfall', 0, TS, 1)])

    expect(cellText(wrapper, '数据')).toBe('rainfall: 0.00')
    expect(cellText(wrapper, '数据')).not.toBe(UNKNOWN)
  })

  it('接口返回空 ⇒ 表格走空态（不是显示一行「—」）', async () => {
    const wrapper = await mountWithHistory([])
    expect(wrapper.find('tbody tr').exists()).toBe(false)
    expect(wrapper.find('.el-empty').exists()).toBe(true)
  })

  it('统计卡不再因读不到字段而整组消失：rainfall=0.5 时渲染出「最新雨量」卡', async () => {
    const wrapper = await mountWithHistory(ONE_COLLECTION)

    const cards = wrapper.findAll('.stat-card')
    expect(cards.length, '统计卡为 0 张：dynamicStats 又读不到行数据了').toBeGreaterThan(0)
    const rainfallCard = cards.find((c) => c.text().includes('雨量'))
    expect(rainfallCard, '没有渲染出 rainfall 统计卡').toBeDefined()
    const valueText = rainfallCard!.find('.stat-value').text()
    expect(valueText).not.toBe(UNKNOWN)
    expect(valueText).toContain('0.5')
    expect(valueText).toContain('mm')
  })

  it('实时推送的新条目与表格期望同形状：数值能渲染出来，且行上没有 raw_hex 字段', async () => {
    const wrapper = await mountWithHistory(ONE_COLLECTION)

    // 打开实时开关（test-setup 的 ElSwitch stub 真实 emit change）
    await wrapper.find('.el-switch').trigger('click')
    await flushPromises()

    const handler = mockSubscribe.mock.calls[0][1] as (message: unknown) => void
    expect(typeof handler, '未订阅到 DATA_UPDATE，用例前提不成立').toBe('function')
    handler({
      payload: {
        edge_device_id: 42,
        collected_at: '2026-09-17T07:02:00Z',
        data: { rainfall: 1.25 },
        raw_hex: 'aabb',
      },
    })
    await nextTick()

    const firstRow = wrapper.find('tbody tr')
    expect(firstRow.exists()).toBe(true)
    // 实时行的「数据」列必须解析出 payload.data，而不是恒 —
    expect(firstRow.text()).toContain('rainfall: 1.25')
    // 「原始数据」列已删除 ⇒ 帧里的 raw_hex 不得出现在表格里
    expect(firstRow.text()).not.toContain('2B hex')
  })

  it('响应被服务端降采样时给出提示（否则"只剩 1 个指标"会被误认为设备只上报一个字段）', async () => {
    // 2000 行 = max_points 上限 ⇒ 服务端抽过样
    const many: UnifiedDataRow[] = []
    for (let i = 0; i < 2000; i += 1) {
      const ts = new Date(Date.UTC(2026, 8, 17, 0, 0, 0) + i * 1000).toISOString()
      many.push(row('rainfall', i % 7, ts, i + 1))
    }
    const wrapper = await mountWithHistory(many)
    expect(wrapper.find('[data-testid="history-downsampled"]').exists()).toBe(true)
  })

  it('未降采样（行数远小于上限）时不显示降采样提示', async () => {
    const wrapper = await mountWithHistory(ONE_COLLECTION)
    expect(wrapper.find('[data-testid="history-downsampled"]').exists()).toBe(false)
  })
})
