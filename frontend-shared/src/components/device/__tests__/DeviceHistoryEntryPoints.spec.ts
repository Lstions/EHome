import { describe, expect, it, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { defineComponent, h, type VNode } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import DeviceRestoreDialog from '@/components/device/DeviceRestoreDialog.vue'
import DeviceMergeHistoryDialog from '@/components/device/DeviceMergeHistoryDialog.vue'
import mergeSourceRaw from '@/components/device/DeviceMergeHistoryDialog.vue?raw'
import type { EdgeDevice } from '@/api/edgeDevice'

/**
 * 契约 §4.2 / §4.3 两个新入口的行为锁（逻辑设备移除后承接历史数据的两条路径）。
 *
 * 2026-10-25 与后端新契约对齐（用户反馈「恢复/合并功能逻辑还有大问题」）后重写：
 *   · data-summary 拆 target/sources，「预计迁移」只读 source_rows（不含目标自身）；
 *   · 合并源候选 = 已删除设备（getDeleted 分页）+ 当前页存活设备（高风险分组）；
 *   · el-select allow-create 手输 id 是 string ⇒ 提交前必须收敛为正整数；
 *   · 2026-10-25 产品重做：源只能是「同型号 + 已删除」的历史设备，跨型号/在役/孤儿一律阻断；
 *   · /deleted 分页 + purge_in_progress / data_purge_requested 状态；
 *   · 恢复通道必须提前过滤不可服务通道（disabled / 外设 / hardware_type≠bus_type）。
 */

const mockGetDeleted = vi.fn()
const mockRestore = vi.fn()
const mockGetDataSummary = vi.fn()
const mockMergeHistory = vi.fn()

vi.mock('@/api/edgeDevice', () => ({
  edgeDeviceApi: {
    getDeleted: (...a: unknown[]) => mockGetDeleted(...a),
    restore: (...a: unknown[]) => mockRestore(...a),
    getDataSummary: (...a: unknown[]) => mockGetDataSummary(...a),
    mergeHistory: (...a: unknown[]) => mockMergeHistory(...a),
  },
}))

const mockChannelGetList = vi.fn()
const mockNodeFetch = vi.fn(() => Promise.resolve())
vi.mock('@/api/channel', () => ({
  channelApi: { getList: (...a: unknown[]) => mockChannelGetList(...a) },
  compactChannelList: (items: unknown) =>
    Array.isArray(items) ? items.filter(i => i && typeof i === 'object') : [],
}))
vi.mock('@/stores/node', () => ({
  useNodeStore: () => ({
    fetchNodes: (...a: unknown[]) => mockNodeFetch(...a as []),
    getCachedList: vi.fn(() => ({
      items: [{ id: 1, node_id: 'node-1', name: '节点A', status: 'online', model: 'ESP32' }],
      total: 1,
    })),
    loading: false,
  }),
}))
vi.mock('element-plus', async (importOriginal) => {
  const actual = await importOriginal<typeof import('element-plus')>()
  return {
    ...actual,
    ElMessage: Object.assign(vi.fn(), { success: vi.fn(), warning: vi.fn(), error: vi.fn(), info: vi.fn() }),
  }
})

/**
 * 能执行**作用域插槽**的表格替身（与 data-source/__tests__/DataSourcePage.spec.ts 同一范式）。
 * 全局 ElTable stub 只拼原始值、不执行列插槽 ⇒ 状态 tag/格式化输出/展开行按钮全都测不到。
 */
function slotTable() {
  const SlotColumn = defineComponent({
    props: { label: String, prop: String, width: [String, Number], minWidth: [String, Number], type: String },
    setup(_, { slots }) {
      return () => slots.default?.() ?? null
    },
  })
  const SlotTable = defineComponent({
    props: { data: { type: Array, default: () => [] } },
    setup(props, { slots }) {
      return () => {
        const columns = ((slots.default?.() ?? []) as VNode[]).flat().filter(Boolean) as VNode[]
        const rows = props.data as Array<Record<string, unknown>>
        return h('table', { class: 'el-table' }, [
          h('tbody', rows.map((row, index) => h('tr', { key: String(row?.['id'] ?? row?.['source_id'] ?? index) },
            columns.map((vn) => {
              const scoped = vn.children as Record<string, unknown> | undefined
              const fn = scoped?.['default'] as ((p: { row: unknown }) => unknown) | undefined
              const content = fn ? fn({ row }) : String(row?.[String(vn.props?.['prop'] ?? '')] ?? '')
              return h('td', { class: 'el-table__cell' }, content as never)
            }),
          ))),
        ])
      }
    },
  })
  return { ElTable: SlotTable, 'el-table': SlotTable, ElTableColumn: SlotColumn, 'el-table-column': SlotColumn }
}

const mountWithSlots = (component: unknown, props: Record<string, unknown>) =>
  mount(component as never, {
    props: props as never,
    global: { components: slotTable() },
  } as never)

const deletedRow = (overrides: Record<string, unknown> = {}) => ({
  id: 3,
  name: '旧BMS',
  device_type: 'jiabaida_bms',
  hardware_id: '1',
  node_id: 'node-1',
  channel_id: 5,
  deleted_at: '2026-10-01T00:00:00Z',
  last_data_at: '2026-09-30T00:00:00Z',
  row_estimate: 1234,
  data_purge_requested: false,
  purge_in_progress: false,
  ...overrides,
})

const channelFixture = (overrides: Record<string, unknown> = {}) => ({
  id: 5,
  node_id: 'node-1',
  name: 'UART0',
  hardware_type: 'UART',
  bus_type: 'UART',
  hardware_id: 'UART0',
  enabled: true,
  config: {},
  ...overrides,
})

// 2026-10-25 产品重做：目标型号是源设备准入基准，fixture 必须带 device_type
const target = { id: 1, name: '目标设备', device_type: 'jiabaida_bms' } as EdgeDevice
const noTypeTarget = { id: 1, name: '目标设备' } as EdgeDevice

const summaryFixture = (overrides: Record<string, unknown> = {}) => ({
  target: {
    source_id: 1, device_name: '目标设备', device_type: 'jiabaida_bms',
    state: 'live', rows: 200, first_time: null, last_time: null,
  },
  sources: [
    {
      source_id: 3, device_name: '旧BMS', device_type: 'jiabaida_bms',
      state: 'deleted', rows: 500, first_time: null, last_time: null,
    },
  ],
  target_rows: 200,
  source_rows: 500,
  ...overrides,
})

describe('DeviceRestoreDialog.vue（契约 §4.2 恢复已删除设备）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    mockNodeFetch.mockResolvedValue(undefined)
    mockGetDeleted.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20 })
    mockChannelGetList.mockResolvedValue({ items: [channelFixture()], total: 1 })
  })

  it('打开即拉取已删除设备（分页参数），空集显示空态文案', async () => {
    const wrapper = mountWithSlots(DeviceRestoreDialog, { visible: true })
    await flushPromises()
    expect(mockGetDeleted).toHaveBeenCalledWith({ page: 1, page_size: 20 })
    expect(wrapper.text()).not.toContain('旧BMS')
  })

  it('列表显示数据量估算；row_estimate 为 null 显示「—」而不是「约 0 条」', async () => {
    mockGetDeleted.mockResolvedValue({
      items: [deletedRow(), deletedRow({ id: 4, name: '无估算', row_estimate: null })],
      total: 2, page: 1, page_size: 20,
    })
    const wrapper = mountWithSlots(DeviceRestoreDialog, { visible: true })
    await flushPromises()
    const text = wrapper.text()
    expect(text).toContain('旧BMS')
    expect(text).toContain('约 1,234 条')
    expect(text).not.toContain('约 0 条')
    expect(text).toContain('可恢复')
    // 展开行给出「原位置」+ 合并历史出路（G-4：同址被占时用户需要知道还能合并）
    expect(text).toContain('原位置')
    expect(text).toContain('合并历史数据')
  })

  it('分页：消费 total 并支持翻页', async () => {
    mockGetDeleted.mockResolvedValue({ items: [deletedRow()], total: 61, page: 1, page_size: 20 })
    const wrapper = mountWithSlots(DeviceRestoreDialog, { visible: true })
    await flushPromises()
    const pager = wrapper.find('[data-test="restore-pagination"]')
    expect(pager.exists()).toBe(true)
    expect(pager.text()).toContain('61')
    await pager.trigger('click')
    await flushPromises()
    expect(mockGetDeleted).toHaveBeenLastCalledWith({ page: 2, page_size: 20 })
  })

  it('purge_in_progress：状态标签出现、恢复按钮禁用、点击不发请求', async () => {
    mockGetDeleted.mockResolvedValue({
      items: [deletedRow({ purge_in_progress: true, data_purge_requested: true })],
      total: 1, page: 1, page_size: 20,
    })
    const wrapper = mountWithSlots(DeviceRestoreDialog, { visible: true })
    await flushPromises()
    expect(wrapper.find('[data-test="restore-purge-in-progress"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="restore-purge-in-progress"]').text()).toContain('数据清理中')
    const confirmBtn = wrapper.find('[data-test="restore-confirm"]')
    expect(confirmBtn.exists()).toBe(true)
    expect(confirmBtn.attributes('disabled')).toBeDefined()
    await confirmBtn.trigger('click')
    await flushPromises()
    expect(mockRestore).not.toHaveBeenCalled()
  })

  it('data_purge_requested：提示「已请求删数据，恢复将撤回」', async () => {
    mockGetDeleted.mockResolvedValue({
      items: [deletedRow({ data_purge_requested: true })],
      total: 1, page: 1, page_size: 20,
    })
    const wrapper = mountWithSlots(DeviceRestoreDialog, { visible: true })
    await flushPromises()
    expect(wrapper.find('[data-test="restore-purge-requested"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('撤回')
  })

  it('通道过滤：只列 enabled 且非外设且 hardware_type==bus_type 的通道', async () => {
    mockChannelGetList.mockResolvedValue({
      items: [
        channelFixture(),
        channelFixture({ id: 6, name: 'UART1', hardware_id: 'UART1', enabled: false }),
        channelFixture({ id: 7, name: 'I2C0', hardware_id: 'I2C0', hardware_type: 'I2C', bus_type: 'UART' }),
        channelFixture({ id: 8, name: 'GPIO0', hardware_id: 'GPIO0', hardware_type: 'GPIO', bus_type: 'GPIO' }),
      ],
      total: 4,
    })
    mockGetDeleted.mockResolvedValue({ items: [deletedRow()], total: 1, page: 1, page_size: 20 })
    const wrapper = mountWithSlots(DeviceRestoreDialog, { visible: true })
    await flushPromises()
    const vm = wrapper.vm as any
    vm.bind[3].node_id = 'node-1'
    vm.onNodeChange(3)
    await flushPromises()
    expect(vm.channelsFor(3).map((c: any) => c.id)).toEqual([5])
  })

  it('恢复提交体带 node_id + channel_id，成功后 emit restored 并重载列表', async () => {
    mockGetDeleted.mockResolvedValue({ items: [deletedRow()], total: 1, page: 1, page_size: 20 })
    mockRestore.mockResolvedValue({ id: 3 })
    const wrapper = mountWithSlots(DeviceRestoreDialog, { visible: true })
    await flushPromises()
    const vm = wrapper.vm as any
    const before = mockGetDeleted.mock.calls.length
    vm.bind[3].node_id = 'node-1'
    vm.bind[3].channel_id = 5
    await flushPromises()
    await wrapper.find('[data-test="restore-confirm"]').trigger('click')
    await flushPromises()

    expect(mockRestore).toHaveBeenCalledWith(3, { node_id: 'node-1', channel_id: 5 })
    expect(wrapper.emitted('restored')).toBeTruthy()
    expect(mockGetDeleted.mock.calls.length).toBeGreaterThan(before)
  })

  it('单飞提交：请求在途时重复点击不会发出第二次', async () => {
    mockGetDeleted.mockResolvedValue({ items: [deletedRow()], total: 1, page: 1, page_size: 20 })
    let resolveRestore!: (v: unknown) => void
    mockRestore.mockImplementation(() => new Promise((resolve) => { resolveRestore = resolve }))
    const wrapper = mountWithSlots(DeviceRestoreDialog, { visible: true })
    await flushPromises()
    const vm = wrapper.vm as any
    vm.bind[3].node_id = 'node-1'
    vm.bind[3].channel_id = 5
    await flushPromises()
    const btn = wrapper.find('[data-test="restore-confirm"]')
    await btn.trigger('click')
    await btn.trigger('click')
    await flushPromises()
    expect(mockRestore).toHaveBeenCalledTimes(1)
    resolveRestore({ id: 3 })
    await flushPromises()
  })

  it('节点列表加载失败不拖垮已删设备列表（allSettled 解耦）', async () => {
    mockNodeFetch.mockRejectedValueOnce(new Error('nodes down'))
    mockGetDeleted.mockResolvedValue({ items: [deletedRow()], total: 1, page: 1, page_size: 20 })
    const wrapper = mountWithSlots(DeviceRestoreDialog, { visible: true })
    await flushPromises()
    expect(wrapper.text()).toContain('旧BMS')
    expect(wrapper.find('[data-test="restore-node-error"]').exists()).toBe(true)
  })

  it('initialDeviceId：load 后自动展开该设备行（历史设备列表行操作「恢复」用）', async () => {
    mockGetDeleted.mockResolvedValue({
      items: [deletedRow({ id: 3 }), deletedRow({ id: 4, name: '另一台' })],
      total: 2, page: 1, page_size: 20,
    })
    const wrapper = mountWithSlots(DeviceRestoreDialog, { visible: true, initialDeviceId: 4 })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.expandRowKeys).toEqual(['4'])
  })

  it('initialDeviceId 找不到时静默（不展开任何行，也不影响其它功能）', async () => {
    mockGetDeleted.mockResolvedValue({ items: [deletedRow()], total: 1, page: 1, page_size: 20 })
    const wrapper = mountWithSlots(DeviceRestoreDialog, { visible: true, initialDeviceId: 999 })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.expandRowKeys).toEqual([])
  })
})
describe('DeviceMergeHistoryDialog.vue（2026-10-25 产品重做：同型号已删除历史设备）', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockGetDeleted.mockResolvedValue({
      items: [
        // 用不带时区后缀的本地时间，避免断言随 CI 时区漂移（formatShortTime 按本地时区展示）
        { ...deletedRow(), id: 3, name: '旧BMS', row_estimate: 1234, last_data_at: '2026-10-02T08:00:00' },
        { ...deletedRow(), id: 9, name: '更旧的BMS', row_estimate: null, last_data_at: null },
        // 行数估算 0（已合并过/无数据）：不下拉展示，避免用户白点
        { ...deletedRow(), id: 11, name: '空壳BMS', row_estimate: 0 },
      ],
      total: 61, page: 1, page_size: 200,
    })
    mockGetDataSummary.mockResolvedValue(summaryFixture())
    mockMergeHistory.mockResolvedValue({
      rows_migrated: 500,
      sources: [{ id: 3, state: 'deleted', device_type: 'jiabaida_bms' }],
      warnings: [],
    })
  })

  it('打开只请求「同型号 + 已删除」：getDeleted 带 device_type，且不展示现役设备', async () => {
    const wrapper = mountWithSlots(DeviceMergeHistoryDialog, { visible: true, target })
    await flushPromises()
    const vm = wrapper.vm as any
    // 服务端精确型号过滤（不是前端本地过滤：分页下本地过滤会漏数据）
    expect(mockGetDeleted).toHaveBeenCalledWith({ device_type: 'jiabaida_bms', page: 1, page_size: 200 })
    // 候选只含已删除同型号设备；row_estimate===0 被过滤（null/undefined 保留）
    expect(vm.candidates.map((d: any) => d.id)).toEqual([3, 9])
    // 组件不再接收/展示当前页存活设备
    expect(mergeSourceRaw).not.toContain('props.devices')
    expect(wrapper.find('[data-test="merge-sources"]').text()).not.toContain('现役')
    // total > 展示条数 ⇒ 明确提示（不静默截断）
    expect(wrapper.text()).toContain('同型号已删除设备共 61 台')
  })

  it('候选 label 含名称/行数估算/最后数据；行数未知不伪造 0', async () => {
    const wrapper = mountWithSlots(DeviceMergeHistoryDialog, { visible: true, target })
    await flushPromises()
    const vm = wrapper.vm as any
    const labels = vm.candidates.map((d: any) => vm.candidateLabel(d))
    expect(labels[0]).toContain('#3 旧BMS')
    expect(labels[0]).toContain('约 1,234 行')
    expect(labels[0]).toContain('最后数据 10-02 08:00')
    expect(labels[1]).toContain('行数未知')
  })

  it('顶部与源列表上方有「同型号≠同一台设备」的前身确认提示（不做自动身份推断）', async () => {
    const wrapper = mountWithSlots(DeviceMergeHistoryDialog, { visible: true, target })
    await flushPromises()
    // 顶部说明
    expect(wrapper.text()).toContain('同型号只是必要条件')
    expect(wrapper.text()).toContain('前身')
    // 源列表上方独立提示（data-test 固定）
    const hint = wrapper.find('[data-test="merge-identity-hint"]')
    expect(hint.exists()).toBe(true)
    expect(hint.text()).toContain('当前设备的前身')
    expect(hint.text()).toContain('合并后源记录下的历史将归当前设备')
  })

  it('目标设备只读回显：名称 + 型号中文名/type', async () => {
    const wrapper = mountWithSlots(DeviceMergeHistoryDialog, { visible: true, target })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.targetLabel).toContain('#1 目标设备')
    expect(vm.targetLabel).toContain('jiabaida_bms')
  })

  it('预计迁移只读 source_rows（不含目标自身），明细含型号列且目标不混入 sources', async () => {
    const wrapper = mountWithSlots(DeviceMergeHistoryDialog, { visible: true, target })
    await flushPromises()
    const vm = wrapper.vm as any
    vm.sourceIds = [3]
    await flushPromises()

    const title = wrapper.find('.merge-summary-title').text()
    expect(title).toContain('500')
    expect(title).not.toContain('700')
    expect(title).toContain('200') // 目标现有行数单独展示
    const table = wrapper.find('[data-test="merge-summary-table"]').text()
    expect(table).toContain('#3')
    expect(table).toContain('旧BMS')
    expect(table).toContain('jiabaida_bms') // 型号列
    // 目标不能被当"源设备"列出来（旧缺陷）
    expect(table).not.toContain('#1')
  })

  it('source_rows 缺失时显示「—」而不是 0；rows=null 也显示「—」', async () => {
    mockGetDataSummary.mockResolvedValue(summaryFixture({
      sources: [{
        source_id: 3, device_name: '旧BMS', device_type: 'jiabaida_bms',
        state: 'deleted', rows: null, first_time: null, last_time: null,
      }],
      source_rows: undefined,
      target_rows: undefined,
    }))
    const wrapper = mountWithSlots(DeviceMergeHistoryDialog, { visible: true, target })
    await flushPromises()
    const vm = wrapper.vm as any
    vm.sourceIds = [3]
    await flushPromises()
    const title = wrapper.find('.merge-summary-title').text()
    expect(title).toContain('—')
    expect(title).not.toContain('预计迁移 0')
    expect(wrapper.find('[data-test="merge-summary-table"]').text()).not.toMatch(/\b0\b/)
  })

  it('手输 string 源 id 收敛为正整数，提交体不带 allow_live_source', async () => {
    const wrapper = mountWithSlots(DeviceMergeHistoryDialog, { visible: true, target })
    await flushPromises()
    const vm = wrapper.vm as any
    vm.sourceIds = ['3']
    await flushPromises()
    expect(vm.resolvedSourceIds).toEqual([3])
    await wrapper.find('[data-test="merge-confirm"]').trigger('click')
    await flushPromises()
    // 只传 (id, sourceIds)：产品重做已删除 allow_live_source 选项
    expect(mockMergeHistory).toHaveBeenCalledWith(1, [3])
    expect(mergeSourceRaw).not.toContain('allow_live_source')
    expect(mergeSourceRaw).not.toContain('retired')
  })

  it('手输跨型号 ID → 红字阻断并说明双方型号，不发提交请求', async () => {
    mockGetDataSummary.mockResolvedValue(summaryFixture({
      sources: [{
        source_id: 15, device_name: '逆变器旧机', device_type: 'inverter_techfine',
        state: 'deleted', rows: 300, first_time: null, last_time: null,
      }],
      source_rows: 300,
    }))
    const wrapper = mountWithSlots(DeviceMergeHistoryDialog, { visible: true, target })
    await flushPromises()
    const vm = wrapper.vm as any
    vm.sourceIds = [15]
    await flushPromises()

    const blocked = wrapper.find('[data-test="merge-blocked"]')
    expect(blocked.exists()).toBe(true)
    expect(blocked.text()).toContain('#15')
    expect(blocked.text()).toContain('inverter_techfine')
    expect(blocked.text()).toContain('jiabaida_bms')
    expect(blocked.text()).toContain('只能合并同型号设备')
    expect(vm.confirmDisabled).toBe(true)
    vm.onConfirm()
    await flushPromises()
    expect(mockMergeHistory).not.toHaveBeenCalled()
  })

  it('手输「无设备行」ID（state=missing）→ 阻断：无法验证型号', async () => {
    mockGetDataSummary.mockResolvedValue(summaryFixture({
      sources: [{ source_id: 99, state: 'missing', rows: 18, first_time: null, last_time: null }],
      source_rows: 18,
    }))
    const wrapper = mountWithSlots(DeviceMergeHistoryDialog, { visible: true, target })
    await flushPromises()
    const vm = wrapper.vm as any
    vm.sourceIds = [99]
    await flushPromises()

    expect(wrapper.find('[data-test="merge-blocked"]').text()).toContain('没有设备记录')
    expect(vm.confirmDisabled).toBe(true)
    vm.onConfirm()
    await flushPromises()
    expect(mockMergeHistory).not.toHaveBeenCalled()
  })

  it('手输「使用中」ID（state=live）→ 阻断：在役设备不能当源', async () => {
    mockGetDataSummary.mockResolvedValue(summaryFixture({
      sources: [{
        source_id: 2, device_name: '现役BMS', device_type: 'jiabaida_bms',
        state: 'live', rows: 400, first_time: null, last_time: null,
      }],
      source_rows: 400,
    }))
    const wrapper = mountWithSlots(DeviceMergeHistoryDialog, { visible: true, target })
    await flushPromises()
    const vm = wrapper.vm as any
    vm.sourceIds = [2]
    await flushPromises()

    expect(wrapper.find('[data-test="merge-blocked"]').text()).toContain('仍在使用中')
    expect(vm.confirmDisabled).toBe(true)
  })

  it('非法输入 / 目标自身：拒绝提交且明确提示', async () => {
    const wrapper = mountWithSlots(DeviceMergeHistoryDialog, { visible: true, target })
    await flushPromises()
    const vm = wrapper.vm as any

    vm.sourceIds = ['abc']
    await flushPromises()
    expect(vm.invalidSourceTokens).toEqual(['abc'])
    expect(vm.confirmDisabled).toBe(true)
    vm.onConfirm()
    await flushPromises()
    expect(mockMergeHistory).not.toHaveBeenCalled()

    vm.sourceIds = [1]
    await flushPromises()
    expect(vm.targetSelfTokens).toEqual(['1'])
    expect(vm.confirmDisabled).toBe(true)
  })

  it('目标设备缺型号：显式报错并禁止合并（无法校验同型号）', async () => {
    const wrapper = mountWithSlots(DeviceMergeHistoryDialog, { visible: true, target: noTypeTarget })
    await flushPromises()
    expect(wrapper.find('[data-test="merge-no-type"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="merge-confirm"]').attributes('disabled')).toBeDefined()
    // 型号为空时不发「按型号过滤」的候选请求
    expect(mockGetDeleted).not.toHaveBeenCalled()
  })

  it('空态：没有同型号历史设备时给出三条路径引导，不显示任何现役设备', async () => {
    mockGetDeleted.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 200 })
    const wrapper = mountWithSlots(DeviceMergeHistoryDialog, { visible: true, target })
    await flushPromises()
    const empty = wrapper.find('[data-test="merge-empty"]')
    expect(empty.exists()).toBe(true)
    expect(empty.text()).toContain('没有可合并的同型号历史设备')
    expect(empty.text()).toContain('编辑')
    expect(empty.text()).toContain('恢复已删除设备')
    expect(empty.text()).toContain('合并历史')
    expect(wrapper.text()).not.toContain('现役')
  })

  it('结果含 warnings 时留在弹窗内逐条展示（不只 toast），emit 携带 warnings', async () => {
    mockMergeHistory.mockResolvedValue({
      rows_migrated: 0,
      sources: [{ id: 3, state: 'deleted', device_type: 'jiabaida_bms' }],
      warnings: ['人工 data_sources 与目标冲突，已保留在源设备上'],
    })
    const wrapper = mountWithSlots(DeviceMergeHistoryDialog, { visible: true, target })
    await flushPromises()
    const vm = wrapper.vm as any
    vm.sourceIds = [3]
    await flushPromises()
    await wrapper.find('[data-test="merge-confirm"]').trigger('click')
    await flushPromises()

    const warnings = wrapper.find('[data-test="merge-warnings"]')
    expect(warnings.exists()).toBe(true)
    expect(warnings.text()).toContain('人工 data_sources 与目标冲突')
    // 有 warnings 时不自动关闭，给出「完成」
    expect(wrapper.find('[data-test="merge-done"]').exists()).toBe(true)
    const merged = wrapper.emitted('merged')![0][0] as any
    expect(merged.rowsMigrated).toBe(0)
    expect(merged.warnings).toEqual(['人工 data_sources 与目标冲突，已保留在源设备上'])
    expect(merged).not.toHaveProperty('retiredSources')
  })

  it('无 warnings 时成功后关闭弹窗；emit 只有 targetId/rowsMigrated/warnings', async () => {
    const wrapper = mountWithSlots(DeviceMergeHistoryDialog, { visible: true, target })
    await flushPromises()
    const vm = wrapper.vm as any
    vm.sourceIds = [3]
    await flushPromises()
    await wrapper.find('[data-test="merge-confirm"]').trigger('click')
    await flushPromises()

    const merged = wrapper.emitted('merged')![0][0] as any
    expect(merged.targetId).toBe(1)
    expect(merged.rowsMigrated).toBe(500)
    expect(merged.warnings).toEqual([])
    expect(wrapper.emitted('update:visible')![0]).toEqual([false])
  })

  it('未选源设备时确认按钮禁用', async () => {
    const wrapper = mountWithSlots(DeviceMergeHistoryDialog, { visible: true, target })
    await flushPromises()
    expect(wrapper.find('[data-test="merge-confirm"]').attributes('disabled')).toBeDefined()
  })

  it('候选列表加载失败时降级为可手输（不阻塞弹窗）', async () => {
    mockGetDeleted.mockRejectedValue(new Error('deleted down'))
    const wrapper = mountWithSlots(DeviceMergeHistoryDialog, { visible: true, target })
    await flushPromises()
    expect(wrapper.find('[data-test="merge-candidates-error"]').exists()).toBe(true)
    const vm = wrapper.vm as any
    vm.sourceIds = [42]
    await flushPromises()
    expect(vm.resolvedSourceIds).toEqual([42])
  })
})
