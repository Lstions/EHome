import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import DataPanel from '@/views/data/DataPanel.vue'
import dataPanelSource from '@/views/data/DataPanel.vue?raw'
import { edgeDeviceApi } from '@/api/edgeDevice'

// ── Mock API module (correct path: @/api/edgeDevice, NOT @/api/modules/data) ──
vi.mock('@/api/edgeDevice', () => ({
  edgeDeviceApi: {
    getList: vi.fn(() =>
      Promise.resolve({
        total: 0,
        items: [],
      })
    ),
    getDetail: vi.fn(() => Promise.resolve({})),
    create: vi.fn(() => Promise.resolve({ id: 1 })),
    update: vi.fn(() => Promise.resolve()),
    delete: vi.fn(() => Promise.resolve()),
    getHistoryData: vi.fn(() => Promise.resolve({ items: [], total: 0 })),
    getLatestData: vi.fn(() => Promise.resolve(null)),
  },
  // type EdgeDevice is erased at runtime; no mock needed
}))

// ── Mock edge device store ──
vi.mock('@/stores/edgeDevice', () => ({
  useEdgeDeviceStore: () => ({
    list: [{ id: 1, name: '设备 1' }, { id: 42, name: '设备 42' }],
    listTotal: 0,
    listLoading: false,
    fetchList: vi.fn(() => Promise.resolve()),
    getCachedList: vi.fn(() => ({
      items: [{ id: 1, name: '设备 1' }, { id: 42, name: '设备 42' }],
    })),
    deleteDevice: vi.fn(() => Promise.resolve()),
    clearCache: vi.fn(),
    isFresh: vi.fn(() => false),
  }),
}))

// ── Mock websocket store ──
vi.mock('@/stores/websocket', () => ({
  useWebSocketStore: () => ({
    subscribe: vi.fn(() => vi.fn()),
    isConnected: false,
    statusMessage: '',
  }),
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

// ── Mock utility modules ──
vi.mock('@/utils/exportData', () => ({
  exportCSV: vi.fn(),
  exportJSON: vi.fn(),
}))

vi.mock('@/utils/feedback', () => ({
  default: {
    success: vi.fn(),
    error: vi.fn(),
    warning: vi.fn(),
    info: vi.fn(),
  },
}))

vi.mock('@/utils/downsample', () => ({
  downsampleData: vi.fn((data: any[]) => data),
}))

vi.mock('@/utils/sensor', () => ({
  sensorNameMap: {},
  sensorUnitMap: {},
}))

vi.mock('@/utils/errorCode', () => ({
  getErrorInfo: vi.fn(() => ({ type: 'info', label: '正常' })),
}))

vi.mock('@/utils/logger', () => ({
  logger: {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  },
}))

// ── Stub Element Plus components and local components ──
/**
 * 取 getHistoryData 最后一次调用的实参。
 * 用索引而非 Array.prototype.at —— tsconfig 的 lib 未含 ES2022。
 */
function lastHistoryCall() {
  const calls = vi.mocked(edgeDeviceApi.getHistoryData).mock.calls
  return calls[calls.length - 1]
}

const stubs = {
  'el-card': { template: '<div class="el-card"><slot /><slot name="header" /></div>' },
  'el-form': { template: '<div class="el-form"><slot /></div>' },
  'el-form-item': { template: '<div class="el-form-item"><slot /></div>' },
  // el-select / el-option 使用 test-setup.ts 全局 stub（真实 <select>/<option>），
  // 本地 stub 会覆盖全局注册并导致 select.el-select 断言失败。
  'el-button': {
    template: '<button class="el-button" :disabled="disabled" @click="$emit(\'click\')"><slot /><slot name="icon" /></button>',
    props: ['disabled', 'loading', 'type', 'size'],
  },
  'el-icon': { template: '<i class="el-icon"><slot /></i>' },
  'el-row': { template: '<div class="el-row"><slot /></div>' },
  'el-col': { template: '<div class="el-col"><slot /></div>' },
  'el-skeleton': { template: '<div class="el-skeleton" />' },
  'el-switch': { template: '<div class="el-switch" />' },
  'el-tag': { template: '<span class="el-tag"><slot /></span>' },
  'el-empty': { template: '<div class="el-empty"><slot /></div>' },
  'el-table': { template: '<table class="el-table"><slot /></table>' },
  'el-table-column': { template: '<col />' },
  // 不再本地 stub 'el-pagination': 原先的 { template: '<div class="el-pagination" />' }
  // 是**惰性**替身 (不渲染 total、不 emit current-change), 使「翻页」在测试里根本无法发生,
  // 也就无法断言"筛选变化重置页码"。改用 src/test-setup.ts 的全局 ElPagination stub ——
  // 它点击时 emit update:currentPage + current-change, 与真实组件契约一致。
  'el-checkbox-group': { template: '<div class="el-checkbox-group"><slot /></div>' },
  'el-checkbox': { template: '<label class="el-checkbox"><slot /></label>' },
  PageHeader: { template: '<div class="page-header"><slot /><slot name="extra" /></div>' },
  LineChart: { template: '<div class="line-chart" />' },
  // Icon components from @element-plus/icons-vue
  Download: { template: '<i />' },
  Connection: { template: '<i />' },
}

describe('DataPanel', () => {
  it('reads devices from the requested parameter cache', () => {
    expect(dataPanelSource).toContain('edgeDeviceStore.getCachedList(params)')
  })

  const getMounted = () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    return mount(DataPanel, {
      global: { plugins: [pinia], stubs },
    })
  }

  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('renders the .data-panel root element', () => {
    const wrapper = getMounted()
    expect(wrapper.find('.data-panel').exists()).toBe(true)
  })

  it('renders the page header', () => {
    const wrapper = getMounted()
    expect(wrapper.find('.page-header').exists()).toBe(true)
  })

  it('renders the query form (el-form)', () => {
    const wrapper = getMounted()
    expect(wrapper.find('.el-form').exists()).toBe(true)
  })

  it('renders the el-card container', () => {
    const wrapper = getMounted()
    expect(wrapper.find('.el-card').exists()).toBe(true)
  })

  it('does not show stat-card when no device is selected and no data', async () => {
    const wrapper = getMounted()
    await flushPromises()
    // stat-card is v-if="queryForm.deviceId && historyData.length > 0"
    // With no device selected and empty data, it should NOT render
    expect(wrapper.find('.stat-card').exists()).toBe(false)
  })

  it('renders export CSV button', async () => {
    const wrapper = getMounted()
    await flushPromises()
    const buttons = wrapper.findAll('button')
    // The export button contains text "导出CSV"
    const exportBtn = buttons.find(b => b.text().includes('导出CSV'))
    expect(exportBtn).toBeDefined()
    expect(exportBtn!.exists()).toBe(true)
  })

  it('export button is disabled when no device selected', async () => {
    const wrapper = getMounted()
    await flushPromises()
    const buttons = wrapper.findAll('button')
    const exportBtn = buttons.find(b => b.text().includes('导出CSV'))
    expect(exportBtn).toBeDefined()
    // disabled because !queryForm.deviceId || !historyData || historyData.length === 0
    expect(exportBtn!.attributes('disabled')).toBeDefined()
  })

  it('handles empty device list gracefully', async () => {
    const wrapper = getMounted()
    await flushPromises()
    // Component should still render without errors
    expect(wrapper.find('.data-panel').exists()).toBe(true)
    // No stat cards when empty
    expect(wrapper.find('.stat-card').exists()).toBe(false)
  })

  it('filters invalid category API rows before rendering selected-device metrics', async () => {
    mockClientGet.mockResolvedValueOnce([
      { code: 'temperature', unit: '°C' },
      { code: '', unit: 'ignored' },
    ])

    const wrapper = getMounted()
    await flushPromises()
    // 通过设备 select 的 v-model 用户交互选中设备，触发 loadDeviceCategories。
    const selects = wrapper.findAll('select.el-select')
    await selects[0].setValue('1')
    await wrapper.findAll('button').find(button => button.text().includes('查询'))!.trigger('click')
    await flushPromises()

    expect(mockClientGet).toHaveBeenCalledWith('/api/v1/unified-data/categories', {
      params: { device_pk: 1 },
    })
  })

  it('uses the selected device categories instead of a global hardcoded list', async () => {
    ;(mockClientGet as any).mockImplementation(((url: string) => {
      if (url === '/api/v1/unified-data/categories') {
        return Promise.resolve([{ code: 'battery_voltage', unit: 'V' }])
      }
      return Promise.resolve([])
    }) as any)

    const wrapper = getMounted()
    await flushPromises()
    const selects = wrapper.findAll('select.el-select')
    await selects[0].setValue('42')
    await wrapper.findAll('button').find(button => button.text().includes('查询'))!.trigger('click')
    await flushPromises()

    expect(mockClientGet).toHaveBeenCalledWith('/api/v1/unified-data/categories', {
      params: { device_pk: 42 },
    })
    // 真实渲染中只出现 API 返回的类别，不出现全局默认类别名称。
    expect(dataPanelSource).not.toContain("['temperature', 'humidity']")
  })

  // 后端契约变更回归：/unified-data/{categories,historical-batch,historical}
  // 现返回 {code,data,message} envelope；若仍按裸数组解包会静默走空数组路径。
  const triggerQuery = async (wrapper: ReturnType<typeof getMounted>, deviceId: string) => {
    const selects = wrapper.findAll('select.el-select')
    await selects[0].setValue(deviceId)
    await wrapper.findAll('button').find(button => button.text().includes('查询'))!.trigger('click')
    await flushPromises()
    await flushPromises()
  }

  const mockHistoryWithOneRow = () => {
    vi.mocked(edgeDeviceApi.getHistoryData).mockResolvedValue({
      items: [{ id: 1, collected_at: '2024-01-01T00:00:00Z', data: { temperature: 21 } }],
      total: 1,
    })
  }

  it('unwraps envelope categories + historical-batch to render chart series', async () => {
    mockHistoryWithOneRow()
    mockClientGet.mockImplementation((url: string) => {
      if (url === '/api/v1/unified-data/categories') {
        return Promise.resolve({ code: 200, data: [{ code: 'temperature', unit: '°C' }], message: 'ok' })
      }
      if (url === '/api/v1/unified-data/historical-batch') {
        return Promise.resolve({
          code: 200,
          data: [{ category: 'temperature', data: [{ timestamp: '2024-01-01T00:00:00Z', value: 21 }] }],
          message: 'ok',
        })
      }
      return Promise.resolve({ code: 200, data: [], message: 'ok' })
    })

    const wrapper = getMounted()
    await flushPromises()
    await triggerQuery(wrapper, '1')

    // categories envelope 被解包 → 批量请求带上真实 category（而非空类别短路）
    expect(mockClientGet).toHaveBeenCalledWith('/api/v1/unified-data/historical-batch', {
      params: expect.objectContaining({ categories: 'temperature' }),
    })
    // historical-batch envelope 被解包 → 图表真实渲染
    expect(wrapper.find('.line-chart').exists()).toBe(true)
  })

  it('still builds chart series when APIs return bare arrays (backward compatible)', async () => {
    mockHistoryWithOneRow()
    mockClientGet.mockImplementation((url: string) => {
      if (url === '/api/v1/unified-data/categories') {
        return Promise.resolve([{ code: 'temperature', unit: '°C' }])
      }
      if (url === '/api/v1/unified-data/historical-batch') {
        return Promise.resolve([{ category: 'temperature', data: [{ timestamp: '2024-01-01T00:00:00Z', value: 21 }] }])
      }
      return Promise.resolve([])
    })

    const wrapper = getMounted()
    await flushPromises()
    await triggerQuery(wrapper, '1')

    expect(mockClientGet).toHaveBeenCalledWith('/api/v1/unified-data/historical-batch', {
      params: expect.objectContaining({ categories: 'temperature' }),
    })
    expect(wrapper.find('.line-chart').exists()).toBe(true)
  })

  it('falls back to empty trend state when categories envelope data is empty', async () => {
    mockHistoryWithOneRow()
    mockClientGet.mockImplementation(() => Promise.resolve({ code: 200, data: [], message: 'ok' }))

    const wrapper = getMounted()
    await flushPromises()
    await triggerQuery(wrapper, '1')

    expect(mockClientGet).toHaveBeenCalledWith('/api/v1/unified-data/categories', {
      params: { device_pk: 1 },
    })
    // 空类别 → buildChartSeries 提前返回，不再请求批量历史，图表走空态
    expect(mockClientGet).not.toHaveBeenCalledWith('/api/v1/unified-data/historical-batch', expect.anything())
    expect(wrapper.find('.line-chart').exists()).toBe(false)
  })

  // ─── 查询范围变化重置页码 (U-4 / §3.2.6 MUST) ───
  // 断言真实请求参数: 审计实测「翻到第 3 页后换设备/时间范围, 新查询仍发 page=3」。

  it('查询按钮重置页码: 翻页后换设备再查询, 请求的 page 参数为 1', async () => {
    mockHistoryWithOneRow()
    mockClientGet.mockImplementation(() => Promise.resolve({ code: 200, data: [], message: 'ok' }))

    const wrapper = getMounted()
    await flushPromises()

    // 第 1 次查询 (设备 1)
    await triggerQuery(wrapper, '1')
    const first = lastHistoryCall()
    expect((first[1] as Record<string, unknown>).page).toBe(1)

    // 翻到第 3 页: 直接点分页器两次 (真实分页组件的 current-change)
    const pagination = wrapper.find('.el-pagination')
    expect(pagination.exists()).toBe(true)
    await pagination.trigger('click')
    await flushPromises() // 两次点击之间必须 flush: 否则第二次仍读到旧 props.currentPage
    await wrapper.find('.el-pagination').trigger('click')
    await flushPromises()
    const paged = lastHistoryCall()
    expect((paged[1] as Record<string, unknown>).page).toBe(3)

    // 换设备后点「查询」→ 必须以 page=1 发出 (改前会继续发 page=3)
    await triggerQuery(wrapper, '42')
    const afterDevice = lastHistoryCall()
    expect(afterDevice[0]).toBe(42)
    expect((afterDevice[1] as Record<string, unknown>).page).toBe(1)
  })

  it('查询按钮重置页码: 换时间范围后请求的 page 参数为 1', async () => {
    mockHistoryWithOneRow()
    mockClientGet.mockImplementation(() => Promise.resolve({ code: 200, data: [], message: 'ok' }))

    const wrapper = getMounted()
    await flushPromises()
    await triggerQuery(wrapper, '1')

    const pagination = wrapper.find('.el-pagination')
    await pagination.trigger('click')
    await flushPromises()
    expect((lastHistoryCall()[1] as Record<string, unknown>).page).toBe(2)

    // 换时间范围 (第二个 select) 后再查询
    const selects = wrapper.findAll('select.el-select')
    await selects[1].setValue('7d')
    await wrapper.find('[data-test="query"]').trigger('click')
    await flushPromises()

    const params = lastHistoryCall()[1] as Record<string, unknown>
    expect(params.page).toBe(1)
  })

  it('翻页本身不重置页码 (分页控件入口不得被打回第 1 页)', async () => {
    mockHistoryWithOneRow()
    mockClientGet.mockImplementation(() => Promise.resolve({ code: 200, data: [], message: 'ok' }))

    const wrapper = getMounted()
    await flushPromises()
    await triggerQuery(wrapper, '1')

    const pagination = wrapper.find('.el-pagination')
    await pagination.trigger('click')
    await flushPromises()
    expect((lastHistoryCall()[1] as Record<string, unknown>).page).toBe(2)

    await wrapper.find('.el-pagination').trigger('click')
    await flushPromises()
    expect((lastHistoryCall()[1] as Record<string, unknown>).page).toBe(3)
  })
})

