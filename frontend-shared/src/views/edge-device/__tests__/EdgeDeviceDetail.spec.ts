import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import GenericDeviceDetail from '../GenericDeviceDetail.vue'
import source from '../GenericDeviceDetail.vue?raw'

const { mockFetchDeviceDetail, mockFetchLatestData, mockWsSubscribe } = vi.hoisted(() => ({
  mockFetchDeviceDetail: vi.fn(),
  mockFetchLatestData: vi.fn(),
  mockWsSubscribe: vi.fn(() => vi.fn()),
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({ back: vi.fn() }),
  useRoute: () => ({ params: { id: '42' } }),
}))
vi.mock('@/composables/useDeviceData', () => ({
  useDeviceData: () => ({
    device: { value: null },
    loading: { value: false },
    refreshing: { value: false },
    syncingHA: { value: false },
    wsConnected: { value: false },
    realtimeDataItems: { value: [] },
    clearRealtimeData: vi.fn(),
    fetchDeviceDetail: mockFetchDeviceDetail,
    fetchLatestData: mockFetchLatestData,
    handleRefresh: vi.fn(),
    handleSyncToHA: vi.fn(),
  }),
}))
vi.mock('@/stores/websocket', () => ({ useWebSocketStore: () => ({ subscribe: mockWsSubscribe }) }))

const stubs = {
  DeviceHeader: { template: '<div data-testid="device-header" />' },
  DeviceInfoCard: { template: '<div data-testid="device-info-card" />' },
  HistoryChartSection: { template: '<div data-testid="history-chart-section" />' },
  CommandFrequencySection: true,
  DeviceControlPanel: true,
  RealtimeDataList: true,
}

describe('GenericDeviceDetail.vue', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('renders its detail container', async () => {
    const wrapper = mount(GenericDeviceDetail, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('.device-detail').exists()).toBe(true)
  })

  it('starts detail and latest-data loading on mount', async () => {
    mount(GenericDeviceDetail, { global: { stubs } })
    await flushPromises()
    expect(mockFetchDeviceDetail).toHaveBeenCalledOnce()
    expect(mockFetchLatestData).toHaveBeenCalledOnce()
  })

  it('uses route id, composable state, back navigation, and device type label contracts', () => {
    expect(source).toContain('const deviceId = ref(Number(route.params.id) || null)')
    expect(source).toContain('const goBack = () => router.back()')
    expect(source).toContain('const deviceTypeText = computed(() => device.value ? getDeviceTypeLabel(device.value.device_type) : \'\')')
    expect(source).toContain('fetchDeviceDetail()')
    expect(source).toContain('fetchLatestData()')
  })

  it('renders loaded device sections only when a device is available', () => {
    expect(source).toContain('<template v-else-if="device">')
    expect(source).toContain('<DeviceInfoCard :device="device" />')
    expect(source).toContain('<HistoryChartSection')
    expect(source).toContain('<RealtimeDataList')
  })

  it('filters realtime entries lacking usable payload before passing them to the list', () => {
    expect(source).toContain('const displayRealtimeItems = computed(() =>')
    expect(source).toContain("Object.values(data).some((v) => v !== null && v !== undefined && v !== '')")
    expect(source).toContain('Array.isArray(item.rawData) && item.rawData.length > 0')
  })
})
