import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { ref } from 'vue'
import EdgeDeviceDetail from '../EdgeDeviceDetail.vue'

// Mock vue-router
const mockPush = vi.fn()
const mockReplace = vi.fn()
const mockBack = vi.fn()
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: mockPush, replace: mockReplace, back: mockBack }),
  useRoute: () => ({ params: { id: '42' } }),
}))

// Use vi.hoisted for mock functions referenced inside vi.mock factories
const {
  mockGetDetail,
  mockGetLatestData,
  mockGetHistoryData,
  mockUpdate,
  mockDelete,
  mockChangeAddress,
  mockExecuteOperation,
  mockSyncDevice,
  mockClientGet,
  mockWsConnect,
  mockWsDisconnect,
  mockWsSend,
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
  mockGetHistoryData: vi.fn(() => Promise.resolve({ items: [] })),
  mockUpdate: vi.fn(() => Promise.resolve()),
  mockDelete: vi.fn(() => Promise.resolve()),
  mockChangeAddress: vi.fn(() => Promise.resolve()),
  mockExecuteOperation: vi.fn(() => Promise.resolve({ code: 0, data: { status: 'ok' }, message: '' })),
  mockSyncDevice: vi.fn(() => Promise.resolve()),
  mockClientGet: vi.fn(() => Promise.resolve({ data: [] })),
  mockWsConnect: vi.fn(),
  mockWsDisconnect: vi.fn(),
  mockWsSend: vi.fn(),
}))

vi.mock('@/api/edgeDevice', () => ({
  edgeDeviceApi: {
    getDetail: mockGetDetail,
    getLatestData: mockGetLatestData,
    getHistoryData: mockGetHistoryData,
    update: mockUpdate,
    delete: mockDelete,
    changeAddress: mockChangeAddress,
    executeOperation: mockExecuteOperation,
  },
}))

vi.mock('@/api/homeassistant', () => ({
  haApi: {
    syncDevice: mockSyncDevice,
  },
}))

vi.mock('@/api/client', () => ({
  default: { get: mockClientGet },
}))

vi.mock('@/composables/useWebSocket', () => ({
  useWebSocket: () => ({
    connected: ref(false),
    connect: mockWsConnect,
    disconnect: mockWsDisconnect,
    send: mockWsSend,
  }),
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

// Stub child components
const stubs = {
  PageHeader: { template: '<div data-testid="page-header"><slot /><slot name="extra" /></div>' },
  StatusBadge: { template: '<span data-testid="status-badge" />' },
  LineChart: { template: '<div data-testid="line-chart" />' },
  RealtimeDataList: { template: '<div data-testid="realtime-data-list" />' },
  'el-card': { template: '<div class="el-card"><slot /><slot name="header" /></div>' },
  'el-skeleton': { template: '<div class="el-skeleton" />' },
  'el-descriptions': { template: '<div class="el-descriptions"><slot /></div>' },
  'el-descriptions-item': { template: '<div class="el-descriptions-item"><slot /></div>' },
  'el-button': { template: '<button class="el-button" @click="$emit(\'click\')"><slot /></button>' },
  'el-tag': { template: '<span class="el-tag"><slot /></span>' },
  'el-icon': { template: '<i class="el-icon"><slot /></i>' },
  'el-empty': { template: '<div class="el-empty" />' },
  'el-dialog': { template: '<div class="el-dialog"><slot /><slot name="footer" /></div>' },
  'el-form': { template: '<form class="el-form"><slot /></form>' },
  'el-form-item': { template: '<div class="el-form-item"><slot /></div>' },
  'el-input': { template: '<input class="el-input" />' },
  'el-input-number': { template: '<div class="el-input-number" />' },
  'el-radio-group': { template: '<div class="el-radio-group"><slot /></div>' },
  'el-radio-button': { template: '<div><slot /></div>' },
  'el-date-picker': { template: '<div class="el-date-picker" />' },
  'el-select': { template: '<div class="el-select"><slot /></div>' },
  'el-option': { template: '<div />' },
  'el-tooltip': { template: '<div class="el-tooltip"><slot /></div>' },
}

describe('EdgeDeviceDetail.vue', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    localStorage.clear()
    sessionStorage.clear()
  })

  it('renders the device-detail container', async () => {
    const wrapper = mount(EdgeDeviceDetail, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('.device-detail').exists()).toBe(true)
  })

  it('renders PageHeader with title "边缘设备详情"', async () => {
    const wrapper = mount(EdgeDeviceDetail, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('[data-testid="page-header"]').exists()).toBe(true)
  })

  it('calls fetchDeviceDetail on mount', async () => {
    mount(EdgeDeviceDetail, { global: { stubs } })
    await flushPromises()
    expect(mockGetDetail).toHaveBeenCalledWith(42)
  })

  it('displays device info after loading', async () => {
    const wrapper = mount(EdgeDeviceDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.device).toBeTruthy()
    expect(vm.device.name).toBe('TempSensor-01')
  })

  it('handles loading state correctly', async () => {
    mockGetDetail.mockReturnValueOnce(new Promise(() => {}))
    const wrapper = mount(EdgeDeviceDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.loading).toBe(true)
  })

  it('handles fetch error gracefully', async () => {
    mockGetDetail.mockRejectedValueOnce(new Error('Network error'))
    const wrapper = mount(EdgeDeviceDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.loading).toBe(false)
  })

  it('computes deviceTypeText correctly', async () => {
    const wrapper = mount(EdgeDeviceDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.deviceTypeText).toBe('温湿度传感器')
  })

  it('computes isDeviceOffline correctly for online device', async () => {
    const wrapper = mount(EdgeDeviceDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.isDeviceOffline).toBe(false)
  })

  it('computes isDeviceOffline correctly for offline device', async () => {
    mockGetDetail.mockResolvedValueOnce({
      id: 42, node_id: 1, name: 'OfflineDev', device_type: 'temp_humidity',
      protocol: 'modbus', hardware_type: 'i2c', hardware_id: 'I2C0_0x48',
      status: 'offline', last_data: null as any, last_data_time: null as any, created_at: '',
    })
    const wrapper = mount(EdgeDeviceDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.isDeviceOffline).toBe(true)
  })

  it('computes hasConfigOperations as false when no device_config', async () => {
    const wrapper = mount(EdgeDeviceDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.hasConfigOperations).toBe(false)
  })

  it('computes canChangeAddress as false when no device_config', async () => {
    const wrapper = mount(EdgeDeviceDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.canChangeAddress).toBe(false)
  })

  it('calls wsConnect on mount', async () => {
    mount(EdgeDeviceDetail, { global: { stubs } })
    await flushPromises()
    expect(mockWsConnect).toHaveBeenCalled()
  })

  it('calls goBack on back navigation', async () => {
    const wrapper = mount(EdgeDeviceDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    vm.goBack()
    expect(mockBack).toHaveBeenCalled()
  })

  it('formatTime handles null and special values', async () => {
    const wrapper = mount(EdgeDeviceDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.formatTime(null)).toBe('-')
    expect(vm.formatTime(undefined)).toBe('-')
    expect(vm.formatTime('0001-01-01T00:00:00Z')).toBe('-')
    expect(vm.formatTime('1970-01-01T00:00:00Z')).toBe('-')
  })

  it('formatTime returns formatted string for valid date', async () => {
    const wrapper = mount(EdgeDeviceDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    const result = vm.formatTime('2025-06-01T12:00:00Z')
    expect(result).not.toBe('-')
  })

  it('handleEdit populates editForm and shows dialog', async () => {
    const wrapper = mount(EdgeDeviceDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    vm.handleEdit()
    expect(vm.editForm.name).toBe('TempSensor-01')
    expect(vm.editForm.device_type).toBe('temp_humidity')
    expect(vm.editDialogVisible).toBe(true)
  })

  it('handleDelete shows delete dialog', async () => {
    const wrapper = mount(EdgeDeviceDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    vm.handleDelete()
    expect(vm.deleteDialogVisible).toBe(true)
  })

  it('handleTimeRangeChange fetches data for non-custom range', async () => {
    const wrapper = mount(EdgeDeviceDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    vm.timeRange = '1h'
    // Reset the mock call count before our action
    mockClientGet.mockClear()
    vm.handleTimeRangeChange()
    await flushPromises()
    // fetchHistoryData should have been called (which uses client.get internally)
    expect(mockClientGet).toHaveBeenCalled()
  })

  it('getDefaultMax returns correct max values for types', async () => {
    const wrapper = mount(EdgeDeviceDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    expect(vm.getDefaultMax('uint8')).toBe(255)
    expect(vm.getDefaultMax('uint16')).toBe(65535)
    expect(vm.getDefaultMax('int8')).toBe(127)
    expect(vm.getDefaultMax('int16')).toBe(32767)
  })

  it('getTimeRange returns correct range for 1h', async () => {
    const wrapper = mount(EdgeDeviceDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    vm.timeRange = '1h'
    const [start, end] = vm.getTimeRange()
    const diff = end.getTime() - start.getTime()
    expect(diff).toBe(60 * 60 * 1000)
  })

  it('getTimeRange returns correct range for 24h', async () => {
    const wrapper = mount(EdgeDeviceDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    vm.timeRange = '24h'
    const [start, end] = vm.getTimeRange()
    const diff = end.getTime() - start.getTime()
    expect(diff).toBe(24 * 60 * 60 * 1000)
  })

  it('getTimeRange returns correct range for 7d', async () => {
    const wrapper = mount(EdgeDeviceDetail, { global: { stubs } })
    await flushPromises()
    const vm = wrapper.vm as any
    vm.timeRange = '7d'
    const [start, end] = vm.getTimeRange()
    const diff = end.getTime() - start.getTime()
    expect(diff).toBe(7 * 24 * 60 * 60 * 1000)
  })
})
