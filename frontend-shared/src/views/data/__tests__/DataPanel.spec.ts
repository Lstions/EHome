import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import DataPanel from '@/views/data/DataPanel.vue'

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
    list: [],
    listTotal: 0,
    listLoading: false,
    fetchList: vi.fn(() => Promise.resolve()),
    fetchDetail: vi.fn(() => Promise.resolve({})),
    deleteDevice: vi.fn(() => Promise.resolve()),
    updateLocal: vi.fn(),
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
  mockClientGet: vi.fn(() => Promise.resolve({ data: [] })),
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
const stubs = {
  'el-card': { template: '<div class="el-card"><slot /><slot name="header" /></div>' },
  'el-form': { template: '<div class="el-form"><slot /></div>' },
  'el-form-item': { template: '<div class="el-form-item"><slot /></div>' },
  'el-select': { template: '<div class="el-select"><slot /></div>' },
  'el-option': { template: '<div />' },
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
  'el-pagination': { template: '<div class="el-pagination" />' },
  'el-checkbox-group': { template: '<div class="el-checkbox-group"><slot /></div>' },
  'el-checkbox': { template: '<label class="el-checkbox"><slot /></label>' },
  PageHeader: { template: '<div class="page-header"><slot /><slot name="extra" /></div>' },
  LineChart: { template: '<div class="line-chart" />' },
  // Icon components from @element-plus/icons-vue
  Download: { template: '<i />' },
  Connection: { template: '<i />' },
}

describe('DataPanel', () => {
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

  it('uses the selected device categories instead of a global hardcoded list', async () => {
    const wrapper = getMounted()
    const vm = wrapper.vm as any
    vm.queryForm.deviceId = 42
    ;(mockClientGet as any).mockImplementation(((url: string) => {
      if (url === '/api/v1/unified-data/categories') {
        return Promise.resolve([{ code: 'battery_voltage', unit: 'V' }])
      }
      return Promise.resolve([])
    }) as any)

    await vm.loadDeviceCategories()

    expect(vm.availableCategories).toEqual([{ code: 'battery_voltage', unit: 'V' }])
  })
})
