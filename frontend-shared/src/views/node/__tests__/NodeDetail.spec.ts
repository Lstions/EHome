import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import NodeDetail from '../NodeDetail.vue'
import source from '../NodeDetail.vue?raw'

const { mockGetDetail, mockGetOTAHistory } = vi.hoisted(() => ({
  mockGetDetail: vi.fn(() => Promise.resolve({
    id: 1, node_id: 'node-1', name: 'Collector-A', model: 'ESP32', status: 'online',
    firmware_version: '1.2.0', connection_quality: 95, ping_latency_ms: 20,
    last_online_time: new Date().toISOString(), online_duration: 86400,
    config_sync_state: 'in_sync', protocol_version: '2.0',
  })),
  mockGetOTAHistory: vi.fn(() => Promise.resolve([])),
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({ back: vi.fn(), push: vi.fn() }),
  useRoute: () => ({ params: { id: 'node-1' } }),
}))
vi.mock('@/api/node', () => ({
  nodeApi: {
    getDetail: mockGetDetail,
    getOTAHistory: mockGetOTAHistory,
    syncConfig: vi.fn(), ping: vi.fn(), cancelOTA: vi.fn(),
  },
}))
vi.mock('@/api/edgeDevice', () => ({ edgeDeviceApi: { getList: vi.fn(() => Promise.resolve({ items: [] })) } }))
vi.mock('@/api/channel', () => ({ channelApi: { getList: vi.fn(() => Promise.resolve([])) } }))
vi.mock('@/stores/websocket', () => ({
  useWebSocketStore: () => ({ connected: false, connect: vi.fn(), disconnect: vi.fn(), subscribe: vi.fn(() => vi.fn()) }),
}))
vi.mock('@/stores/dma', () => ({ useDmaStore: () => ({ mergedChannels: [], loading: false, fetch: vi.fn() }) }))
vi.mock('@/utils/logger', () => ({ logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() } }))
vi.mock('@/composables/useResponsive', () => ({
  useResponsive: () => ({ width: { value: 1440 }, isMobile: { value: false }, isTablet: { value: false }, isDesktop: { value: true } }),
}))

const stubs = {
  PageHeader: { template: '<div data-testid="page-header"><slot /><slot name="extra" /></div>' },
  StatusBadge: true,
  OTAForm: true,
  ChannelPanel: true,
  LogPanel: true,
}

describe('NodeDetail', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('renders the collector detail shell and loads core detail plus OTA history', async () => {
    const wrapper = mount(NodeDetail, { global: { stubs } })
    await flushPromises()
    expect(wrapper.find('.collector-detail').exists()).toBe(true)
    expect(mockGetDetail).toHaveBeenCalledWith('node-1')
    expect(mockGetOTAHistory).toHaveBeenCalledWith('node-1')
  })

  it('loads route-scoped data again when route id changes and cancels stale completions', () => {
    expect(source).toContain('watch(() => route.params.id')
    expect(source).toContain('sequence === collectorDetailSequence')
    expect(source).toContain('sequence !== devicesRequestSequence')
    expect(source).toContain('sequence !== otaRequestSequence')
    expect(source).toContain('editingName.value = false')
    expect(source).toContain('showOTADialog.value = false')
  })

  it('updates names through the store and keeps save completion bound to the originating route', () => {
    expect(source).toContain('nodeStore.updateNode')
    expect(source).toContain('const sequence = ++nameSaveSequence')
    expect(source).toContain('sequence !== nameSaveSequence')
    expect(source).toContain('nameSaveSequence++')
  })

  it('reads related devices from the requested cache and exposes mobile-safe tables', () => {
    expect(source).toContain('edgeDeviceStore.getCachedList(params)')
    expect(source).toContain('useResponsive')
    expect(source).toContain('descColumns = computed(() => (isMobile.value ? 1 : 2))')
    expect(source).toContain(':column="descColumns"')
    expect(source.match(/mobile-table-wrapper/g)?.length).toBeGreaterThanOrEqual(2)
    expect(source.match(/mobile-table-hint/g)?.length).toBeGreaterThanOrEqual(2)
  })

  it('defines fail-visible sync status, OTA, duration, and capability presentation mappings', () => {
    expect(source).toContain('syncStateLabel')
    expect(source).toContain('syncStateTagType')
    expect(source).toContain("return '离线'")
    expect(source).toContain('getOTAStatusType')
    expect(source).toContain('getOTAStatusText')
    expect(source).toContain('formatOnlineDuration')
    expect(source).toContain('capText')
    expect(source).toContain('busText')
  })
})
