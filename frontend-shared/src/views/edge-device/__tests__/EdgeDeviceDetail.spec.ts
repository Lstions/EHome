import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import GenericDeviceDetail from '../GenericDeviceDetail.vue'

// Mock vue-router
const mockPush = vi.fn()
const mockReplace = vi.fn()
const mockBack = vi.fn()
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: mockPush, replace: mockReplace, back: mockBack }),
  useRoute: () => ({ params: { id: '42' } }),
}))

const {
  mockGetDetail,
  mockGetLatestData,
  mockSyncDevice,
  mockWsSubscribe,
} = vi.hoisted(() => ({
  mockGetDetail: vi.fn(() =>
    Promise.resolve({
      id: 42,
      node_id: 1,
      name: 'TempSensor-01',
      device_type: 'temp_humidity',
      protocol: 'modbus',
      hardware_type: 'i2c',
      hardware_id: 'I2C0_0x48',
      status: 'online',
      last_data: { temperature: 25.3, humidity: 60 },
      last_data_time: new Date().toISOString(),
      last_error_code: 0,
      created_at: '2025-01-01T00:00:00Z',
      device_config: null,
    })
  ),
  mockGetLatestData: vi.fn(() => Promise.resolve(null)),
  mockSyncDevice: vi.fn(() => Promise.resolve()),
  mockWsSubscribe: vi.fn(() => vi.fn()),
}))

vi.mock('@/api/edgeDevice', () => ({
  edgeDeviceApi: {
    getDetail: mockGetDetail,
    getLatestData: mockGetLatestData,
    getHistoryData: vi.fn(() => Promise.resolve({ items: [] })),
    update: vi.fn(() => Promise.resolve()),
    delete: vi.fn(() => Promise.resolve()),
    changeAddress: vi.fn(() => Promise.resolve()),
    executeOperation: vi.fn(() => Promise.resolve({ code: 0, data: { status: 'ok' }, message: '' })),
    getCommandIntervals: vi.fn(() => Promise.resolve([])),
  },
}))

vi.mock('@/api/homeassistant', () => ({
  haApi: { syncDevice: mockSyncDevice },
}))

vi.mock('@/api/client', () => ({
  default: { get: vi.fn(() => Promise.resolve({ data: [] })) },
}))

vi.mock('@/stores/websocket', () => ({
  useWebSocketStore: () => ({
    connected: false,
    subscribe: mockWsSubscribe,
  }),
}))

vi.mock('@/events/events', () => ({
  WS_EVENT: { DATA_UPDATE: 'data_update', NODE_STATUS: 'node_status' },
}))

vi.mock('@/utils/logger', () => ({
  logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}))

vi.mock('@/utils/sensor', () => ({
  sensorNameMap: { temperature: '温度', humidity: '湿度' },
  sensorUnitMap: { temperature: '°C', humidity: '%' },
  SENSOR_ORDER: ['temperature', 'humidity'],
}))

vi.mock('@/utils/errorCode', () => ({
  getErrorInfo: (code: number) => {
    if (!code || code === 0) return { label: '成功', type: 'success' }
    return { label: `错误${code}`, type: 'danger' }
  },
}))

vi.mock('@/api/deviceConfig', () => ({}))

// Stub child components — shared components have their own internals
const stubs = {
  PageHeader: { template: '<div data-testid="page-header"><slot /><slot name="extra" /></div>' },
  StatusBadge: { template: '<span data-testid="status-badge" />' },
  LineChart: { template: '<div data-testid="line-chart" />' },
  RealtimeDataList: { template: '<div data-testid="realtime-data-list" />' },
  CommandList: { template: '<div data-testid="command-list" />' },
  DeviceHeader: { template: '<div data-testid="device-header" />' },
  DeviceInfoCard: { template: '<div data-testid="device-info-card" />' },
  HistoryChartSection: { template: '<div data-testid="history-chart-section" />' },
  CommandFrequencySection: { template: '<div data-testid="command-frequency-section" />' },
  OperationButtons: { template: '<div data-testid="operation-buttons" />' },
  'el-card': { template: '<div class="el-card"><slot /><slot name="header" /></div>' },
  'el-skeleton': { template: '<div class="el-skeleton" />' },
  'el-empty': { template: '<div class="el-empty" />' },
  'el-tag': { template: '<span class="el-tag"><slot /></span>' },
  'el-icon': { template: '<i class="el-icon"><slot /></i>' },
}

describe('GenericDeviceDetail.vue', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    localStorage.clear()
    sessionStorage.clear()
  })

  it('renders the device-detail container', async () => {
    const wrapper = mount(GenericDeviceDetail, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('.device-detail').exists()).toBe(true)
  })

  it('renders PageHeader via DeviceHeader', async () => {
    const wrapper = mount(GenericDeviceDetail, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('[data-testid="device-header"]').exists()).toBe(true)
  })

  it('calls fetchDeviceDetail on mount', async () => {
    mount(GenericDeviceDetail, { global: { stubs } })
    await flushPromises()
    expect(mockGetDetail).toHaveBeenCalledWith(42)
  })

  it('loads device data after mount', async () => {
    const wrapper = mount(GenericDeviceDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.device).toBeTruthy()
    expect(vm.device.name).toBe('TempSensor-01')
  })

  it('handles loading state correctly', async () => {
    mockGetDetail.mockReturnValueOnce(new Promise(() => {}))
    const wrapper = mount(GenericDeviceDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.loading).toBe(true)
  })

  it('handles fetch error gracefully', async () => {
    mockGetDetail.mockRejectedValueOnce(new Error('Network error'))
    const wrapper = mount(GenericDeviceDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.loading).toBe(false)
  })

  it('computes deviceTypeText correctly', async () => {
    const wrapper = mount(GenericDeviceDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.deviceTypeText).toBe('温湿度传感器')
  })

  it('subscribes to WS data_update on mount', async () => {
    mount(GenericDeviceDetail, { global: { stubs } })
    await flushPromises()
    expect(mockWsSubscribe).toHaveBeenCalledWith('data_update', expect.any(Function))
  })

  it('calls goBack on back navigation', async () => {
    const wrapper = mount(GenericDeviceDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    vm.goBack()
    expect(mockBack).toHaveBeenCalled()
  })

  it('fetches latest data on mount', async () => {
    mount(GenericDeviceDetail, { global: { stubs } })
    await flushPromises()
    expect(mockGetLatestData).toHaveBeenCalledWith(42)
  })

  it('renders DeviceInfoCard after device loads', async () => {
    const wrapper = mount(GenericDeviceDetail, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('[data-testid="device-info-card"]').exists()).toBe(true)
  })

  it('renders HistoryChartSection after device loads', async () => {
    const wrapper = mount(GenericDeviceDetail, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('[data-testid="history-chart-section"]').exists()).toBe(true)
  })

  it('renders OperationButtons after device loads', async () => {
    const wrapper = mount(GenericDeviceDetail, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('[data-testid="operation-buttons"]').exists()).toBe(true)
  })
})
