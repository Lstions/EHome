import { describe, it, expect, vi, beforeEach } from 'vitest'
import { nextTick } from 'vue'
import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { defineComponent, h, type VNode } from 'vue'
import DataPanel, { parseRowData } from '@/views/data/DataPanel.vue'
import { UNKNOWN } from '@/utils/format'
import { edgeDeviceApi } from '@/api/edgeDevice'

/**
 * DataPanel「数据 / 原始数据」两列恒显示 — 的回归锁（P2，实测 2026-09-17）。
 *
 * 根因：GET /api/v1/edge-devices/:id/data 只返回 data_json
 * （实测键：created_at, data_json, device_id, id, logical_device_id, node_id, timestamp），
 * 而表格读 row.parsed_data || row.data / row.raw_data ⇒ 恒 undefined ⇒ 恒「—」。
 *
 * 本文件刻意分两层：
 *   ① 纯函数层（parseRowData）—— 「坏 JSON 不崩、不把取不到落成 0」；
 *   ② **真实渲染层**（执行 el-table-column 的 #default 作用域插槽）——
 *      缺陷的现象是"单元格里恒显示 —"，只断言函数等于不守渲染路径。
 *
 * 为什么不用 src/test-setup.ts 的全局 ElTable stub：它把整行 Object.values 拍平成一个
 * <td>，列插槽根本不执行 ⇒ 本缺陷在测试里永远看不见（这正是改前 22 条既有用例全绿却漏掉它的原因）。
 * 下面的替身与 views/alert/__tests__/AlertRules.spec.ts 的 slotTable() 同一范式。
 */

// ── 实测（生产环境真实雨量计）接口返回行 ──────────────────────────────────
const REAL_DATA_JSON = '{"channel_id":1,"raw_hex":"01030200057847","sensors":[{"Name":"rainfall","Value":0.5,"Unit":"mm","StringValue":""}],"timestamp":1789628509271}'
const REAL_ROW = {
  id: 3080755,
  device_id: 2,
  node_id: 'F0F5BDFFFE02',
  data_json: REAL_DATA_JSON,
  timestamp: '2026-09-17T07:01:49.271798Z',
  created_at: '2026-09-17T07:01:49.271798Z',
  logical_device_id: 2,
}

// ── API / store mocks（与 DataPanel.spec.ts 同一套，缺一即页面挂载失败）──
vi.mock('@/api/edgeDevice', () => ({
  edgeDeviceApi: {
    getList: vi.fn(() => Promise.resolve({ total: 0, items: [] })),
    getDetail: vi.fn(() => Promise.resolve({})),
    create: vi.fn(() => Promise.resolve({ id: 1 })),
    update: vi.fn(() => Promise.resolve()),
    delete: vi.fn(() => Promise.resolve()),
    getHistoryData: vi.fn(() => Promise.resolve({ items: [], total: 0 })),
    getLatestData: vi.fn(() => Promise.resolve(null)),
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
vi.mock('@/utils/downsample', () => ({ downsampleData: vi.fn((data: unknown[]) => data) }))
vi.mock('@/utils/sensor', () => ({
  sensorNameMap: { rainfall: '雨量' },
  sensorUnitMap: { rainfall: 'mm' },
}))
vi.mock('@/utils/errorCode', () => ({ getErrorInfo: vi.fn(() => ({ type: 'info', label: '正常' })) }))
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
        const columns = (slots.default?.() ?? []) as VNode[]
        const rows = props.data as Array<Record<string, unknown>>
        return h('table', { class: 'el-table' }, [
          h('tbody', rows.map((row, index) => h('tr', { key: String(row?.id ?? index) }, columns.map((vn) => {
            const scoped = defaultSlotOf(vn)
            const content = scoped
              ? scoped({ row })
              : String(row?.[String(vn.props?.['prop'] ?? '')] ?? '')
            return h('td', { class: 'el-table__cell', 'data-label': String(vn.props?.['label'] ?? '') }, content)
          })))),
        ])
      }
    },
  })
  return {
    // Element Plus 组件在本文件里沿用 src/test-setup.ts 的全局 stub
    // （PascalCase 与 kebab-case 各注册一次；test-utils 的 stubs 只按 PascalCase 匹配，
    //  所以必须像 ElTable 这样两种写法都给）。
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
 * 必须走 stubs 而不是 components 的几个：DataPanel 里的 PageHeader / LineChart / 图标都是
 * **本地注册**的（import 后直接用在模板里），本地注册优先于 global.components ⇒ 放 components 里
 * 不生效。真实 LineChart 在 happy-dom 里没有 canvas，mounted 会抛（zrender Layer.initContext），
 * 整个 mount 随之失败、wrapper.vm 为 null —— 这正是本文件第一版 8 条用例报
 * "Cannot read properties of null (reading '$')" 的原因。
 */
const componentStubs = {
  PageHeader: { template: '<div class="page-header"><slot /><slot name="extra" /></div>' },
  LineChart: { template: '<div class="line-chart" />' },
  Download: { template: '<i />' },
  Connection: { template: '<i />' },
}

/** 以「真实雨量计」的一条历史行挂载并触发查询；返回挂载后的 wrapper。 */
async function mountWithHistory(items: unknown[], total = items.length) {
  vi.mocked(edgeDeviceApi.getHistoryData).mockResolvedValue({ items, total } as never)
  mockClientGet.mockImplementation((url: string) => {
    if (url === '/api/v1/unified-data/categories') {
      return Promise.resolve([{ code: 'rainfall', unit: 'mm' }])
    }
    if (url === '/api/v1/unified-data/historical-batch') {
      return Promise.resolve([{ category: 'rainfall', data: [{ timestamp: '2026-09-17T07:01:49Z', value: 0.5 }] }])
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

describe('parseRowData —— 本页唯一的行取数入口（纯函数层）', () => {
  it('真实形状的行（只有 data_json）解析出 sensors 的 Name→Value', () => {
    expect(parseRowData(REAL_ROW).values).toEqual({ rainfall: 0.5 })
  })

  it('同一行解析出 data_json 内的 raw_hex（不带 0x 前缀）', () => {
    expect(parseRowData(REAL_ROW).rawHex).toBe('01030200057847')
  })

  it('值是 0 时得到 0（不是 null）——「真的是 0」必须可与「取不到」区分', () => {
    const zeroRow = {
      ...REAL_ROW,
      data_json: JSON.stringify({ sensors: [{ Name: 'rainfall', Value: 0 }] }),
    }
    expect(parseRowData(zeroRow).values).toEqual({ rainfall: 0 })
    expect(parseRowData(zeroRow).values).not.toBeNull()
  })

  it('data_json 是坏 JSON：不抛异常，values 为 null（绝不落成 0）', () => {
    expect(() => parseRowData({ ...REAL_ROW, data_json: '{not json' })).not.toThrow()
    const { values } = parseRowData({ ...REAL_ROW, data_json: '{not json' })
    expect(values).toBeNull()
    expect(values).not.toEqual({ rainfall: 0 })
  })

  it('data_json 为空/空串/空结构：不抛异常且 values 为 null', () => {
    for (const data_json of ['', '   ', '{}', 'null', '{"sensors":[]}', '{"sensors":"nope"}', '[]']) {
      const result = parseRowData({ ...REAL_ROW, data_json })
      expect(result.values, 'data_json=' + JSON.stringify(data_json)).toBeNull()
    }
  })

  it('行本身缺失/非对象：不抛异常，values 与 rawHex 均为 null', () => {
    for (const row of [null, undefined, 'x', 42, true]) {
      expect(() => parseRowData(row)).not.toThrow()
      expect(parseRowData(row)).toEqual({ values: null, numbers: null, rawHex: null })
    }
  })

  it('字符串数值（"0.5"）不被静默转成数字：宁可为 null 也不伪造事实', () => {
    const row = { data_json: JSON.stringify({ sensors: [{ Name: 'rainfall', Value: '0.5' }] }) }
    expect(parseRowData(row).values).toBeNull()
  })

  it('坏 JSON 时行级 raw_hex 仍可用于「原始数据」列', () => {
    const row = { ...REAL_ROW, data_json: '{broken', raw_hex: '01030200057847' }
    expect(parseRowData(row).values).toBeNull()
    expect(parseRowData(row).rawHex).toBe('01030200057847')
  })

  it('兼容既有形状：{ data: {...} } 与扁平行（既有用例的 fixture），并排除 raw_data 键', () => {
    expect(parseRowData({ id: 1, data: { temperature: 21 }, raw_data: 'aabb' }))
      .toEqual({ values: { temperature: 21 }, numbers: { temperature: 21 }, rawHex: 'aabb' })
    expect(parseRowData({ temperature: 21, humidity: 40 }))
      .toEqual({ values: { temperature: 21, humidity: 40 }, numbers: { temperature: 21, humidity: 40 }, rawHex: null })
    // raw_data / raw_hex 不是"指标"，不得混进数值列
    expect(parseRowData({ data: { temperature: 21 }, raw_hex: 'aabb' }).values).toEqual({ temperature: 21 })
  })

  it('hex 规整：0x 前缀与空白被剥掉（formatRawData 期望不带前缀的 hex）', () => {
    expect(parseRowData({ raw_hex: '0x01030200057847' }).rawHex).toBe('01030200057847')
    expect(parseRowData({ raw_hex: '  01030200057847  ' }).rawHex).toBe('01030200057847')
    expect(parseRowData({ raw_hex: '' }).rawHex).toBeNull()
  })

  // ── P7：字符串型读数（StringValue）────────────────────────────────────────
  // 后端 SensorData 有 StringValue 字段（drivers/registry.go:13），承载字符串真值：
  // jiabaida_parse.go 的 hardware_version / serial_number 的 **Value 恒为 0**，
  // 真值只在 StringValue。只取 Value ⇒ 显示成 0（比「—」更糟：0 像真实读数）。
  describe('P7 字符串型读数（StringValue）', () => {
    // 实测形状（驱动单测 jiabaida_test.go:677 断言 StringValue === "V19"）
    const stringSensorRow = {
      data_json: JSON.stringify({
        channel_id: 1,
        raw_hex: 'dd050003563139ff3d77',
        sensors: [
          { Name: 'hardware_version', Value: 0, Unit: '', StringValue: 'V19' },
          { Name: 'rainfall', Value: 0.5, Unit: 'mm', StringValue: '' },
        ],
      }),
    }

    it('StringValue 非空时取它，而不是把它显示成 0', () => {
      const { values } = parseRowData(stringSensorRow)
      expect(values?.hardware_version).toBe('V19')
      expect(values?.hardware_version).not.toBe(0)
      expect(values?.rainfall).toBe(0.5)
    })

    it('StringValue 为空串时回退到 Value（不得把空串当读数）', () => {
      const row = { data_json: JSON.stringify({ sensors: [{ Name: 'r', Value: 7, StringValue: '' }] }) }
      expect(parseRowData(row).values?.r).toBe(7)
    })

    it('numbers 只含数值型：字符串读数不得进入数值统计（否则被当 0 拉低均值）', () => {
      const { numbers } = parseRowData(stringSensorRow)
      expect(numbers).toEqual({ rainfall: 0.5 })
      expect(numbers && 'hardware_version' in numbers).toBe(false)
    })

    it('只有字符串读数时 numbers 为 null（趋势/统计不得画出掉到 0 的假线）', () => {
      const row = { data_json: JSON.stringify({ sensors: [{ Name: 'sn', Value: 0, StringValue: 'SIM-BMS-0001' }] }) }
      expect(parseRowData(row).values?.sn).toBe('SIM-BMS-0001')
      expect(parseRowData(row).numbers).toBeNull()
    })

    it('纯数值行：values 与 numbers 一致（既有行为不回归）', () => {
      const { values, numbers } = parseRowData(REAL_ROW)
      expect(values).toEqual({ rainfall: 0.5 })
      expect(numbers).toEqual({ rainfall: 0.5 })
    })
  })
})

describe('DataPanel 历史表渲染 —— 真实行不得再显示「—」', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    // 清掉 vi.clearAllMocks 后丢失的默认实现
    mockSubscribe.mockImplementation(() => vi.fn())
  })

  it('「数据」列显示 rainfall: 0.50，「原始数据」列显示 hex 字节数（7 字节 → 7B hex）', async () => {
    const wrapper = await mountWithHistory([REAL_ROW])

    // 缺陷现象是"两列恒显示 —"，故先断言**不是**占位符，再断言真实值
    expect(cellText(wrapper, '数据')).not.toBe(UNKNOWN)
    expect(cellText(wrapper, '数据')).toContain('rainfall: 0.50')
    expect(wrapper.find('[data-testid="row-values"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="values-unknown"]').exists()).toBe(false)

    // formatRawData 对 14 个 hex 字符 => 7 字节 => "7B hex"
    expect(cellText(wrapper, '原始数据')).toBe('7B hex')
    expect(wrapper.find('[data-testid="row-raw-hex"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="raw-unknown"]').exists()).toBe(false)
  })

  it('回归锁：data_json 有数据时，两列都不得是统一未知占位符「—」', async () => {
    const wrapper = await mountWithHistory([REAL_ROW])
    const cells = wrapper.findAll('td.el-table__cell').map((c) => c.text())

    expect(cells, '分母为 0：表格没有渲染出任何单元格，断言会假绿').not.toHaveLength(0)
    expect(cellText(wrapper, '数据'), '「数据」列退回恒 —（本次缺陷的原始形态）').not.toBe(UNKNOWN)
    expect(cellText(wrapper, '原始数据'), '「原始数据」列退回恒 —').not.toBe(UNKNOWN)
  })

  it('坏 JSON 的行显示「—」而不是 0，且不抛异常', async () => {
    const brokenRow = { ...REAL_ROW, id: 2, data_json: '{not json' }
    const wrapper = await mountWithHistory([brokenRow])

    // 页面还在（没被坏 JSON 打崩）
    expect(wrapper.find('.data-panel').exists()).toBe(true)
    expect(cellText(wrapper, '数据')).toBe(UNKNOWN)
    expect(wrapper.find('[data-testid="values-unknown"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="row-values"]').exists()).toBe(false)
    // 关键：不得把"取不到"渲染成 0
    expect(cellText(wrapper, '数据')).not.toContain('0')
    expect(cellText(wrapper, '数据')).not.toContain('rainfall')
    // 这一行的 raw_hex 只存在于**解析不出来的** data_json 里 ⇒ 原始数据列同样无从得知，
    // 显示「—」才是诚实的（不得凭正则去猜一段 hex）
    expect(cellText(wrapper, '原始数据')).toBe(UNKNOWN)
  })

  it('行级 raw_hex（实时推送形态）在坏 JSON 下仍能显示', async () => {
    // 实时流把 raw_hex 放在行上（useDeviceData / useRealtimeData 同此形状），
    // 此时即便 data_json 坏掉，「原始数据」列也不该跟着变成 —
    const row = { ...REAL_ROW, id: 4, data_json: '{not json', raw_hex: '01030200057847' }
    const wrapper = await mountWithHistory([row])

    expect(cellText(wrapper, '数据')).toBe(UNKNOWN)
    expect(cellText(wrapper, '原始数据')).toBe('7B hex')
  })

  it('真的是 0 的行显示 rainfall: 0.00 —— 与"取不到"的 — 可区分', async () => {
    const zeroRow = {
      ...REAL_ROW,
      id: 3,
      data_json: JSON.stringify({
        channel_id: 1,
        raw_hex: '01030200007845',
        sensors: [{ Name: 'rainfall', Value: 0, Unit: 'mm', StringValue: '' }],
      }),
    }
    const wrapper = await mountWithHistory([zeroRow])

    expect(cellText(wrapper, '数据')).toBe('rainfall: 0.00')
    expect(cellText(wrapper, '数据')).not.toBe(UNKNOWN)
    expect(cellText(wrapper, '原始数据')).toBe('7B hex')
  })

  it('统计卡不再因读不到字段而整组消失：rainfall=0.5 时渲染出「最新雨量」卡', async () => {
    const wrapper = await mountWithHistory([REAL_ROW])

    const cards = wrapper.findAll('.stat-card')
    expect(cards.length, '统计卡为 0 张：dynamicStats 又读不到 data_json 了').toBeGreaterThan(0)
    const rainfallCard = cards.find((c) => c.text().includes('雨量'))
    expect(rainfallCard, '没有渲染出 rainfall 统计卡').toBeDefined()
    const valueText = rainfallCard!.find('.stat-value').text()
    expect(valueText).not.toBe(UNKNOWN)
    expect(valueText).toContain('0.5')
    expect(valueText).toContain('mm')
  })

  it('实时推送的新条目与表格期望同形状：数值与 raw_hex 都能在表格里渲染出来', async () => {
    const wrapper = await mountWithHistory([REAL_ROW])

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
    // 实时行的「原始数据」列必须解析出 payload.raw_hex（2 字节 → 2B hex）
    expect(firstRow.text()).toContain('2B hex')
  })
})
