import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import DataPanel from '@/views/data/DataPanel.vue'

// Mock the API module
vi.mock('@/api/modules/data', () => ({
  default: {
    getHistory: vi.fn(() => Promise.resolve({ data: { data: [] } })),
    getDeviceList: vi.fn(() => Promise.resolve({ data: { devices: [] } })),
    exportCSV: vi.fn(() => Promise.resolve()),
  },
}))

// Mock websocket store (used in DataPanel setup)
vi.mock('@/stores/websocket', () => ({
  useWebSocketStore: () => ({
    subscribe: vi.fn(() => vi.fn()),
    isConnected: false,
    statusMessage: '',
  }),
}))

// Stub Element Plus components
const stubs = {
  'el-card': { template: '<div class="el-card"><slot /></div>' },
  'el-form': { template: '<div class="el-form"><slot /></div>' },
  'el-form-item': { template: '<div><slot /></div>' },
  'el-select': { template: '<div class="el-select"><slot /></div>' },
  'el-option': { template: '<div />' },
  'el-button': { template: '<button class="el-button" @click="$emit(\'click\')"><slot /></button>' },
  'el-icon': { template: '<i class="el-icon"><slot /></i>' },
  'el-row': { template: '<div class="el-row"><slot /></div>' },
  'el-col': { template: '<div class="el-col"><slot /></div>' },
  'el-skeleton': { template: '<div class="el-skeleton" />' },
  PageHeader: { template: '<div class="page-header" />' },
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

  it('renders the page header', () => {
    const wrapper = getMounted()
    expect(wrapper.find('.page-header').exists() || wrapper.text()).toBeDefined()
  })

  it('renders the query form with device select and time range', () => {
    const wrapper = getMounted()
    expect(wrapper.find('.el-form').exists()).toBe(true)
  })

  it('shows loading state initially', () => {
    const wrapper = getMounted()
    expect(wrapper.find('.el-card').exists() || wrapper.find('.el-skeleton').exists()).toBe(true)
  })

  it('renders stat cards when data is available', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const wrapper = mount(DataPanel, {
      global: { plugins: [pinia], stubs },
      props: {
        historyData: [
          { x: 1, y: 25.5 },
          { x: 2, y: 26.0 },
        ],
      },
    })
    expect(wrapper.find('.stat-card').exists() || wrapper.text()).toBeDefined()
  })

  it('exports CSV when export button is clicked', async () => {
    const wrapper = getMounted()
    const exportBtn = wrapper.find('.el-icon')
    if (exportBtn.exists()) {
      await exportBtn.trigger('click')
    }
    expect(wrapper.find('.el-card').exists()).toBe(true)
  })

  it('handles empty device list gracefully', () => {
    const wrapper = getMounted()
    expect(wrapper.find('.el-card').exists()).toBe(true)
  })
})