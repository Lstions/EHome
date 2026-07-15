import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import { useDeviceData } from '../useDeviceData'

const { mockFetchDetail, mockGetLatestData, mockSyncDevice, success, error } = vi.hoisted(() => ({
  mockFetchDetail: vi.fn(),
  mockGetLatestData: vi.fn(),
  mockSyncDevice: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
}))

vi.mock('@/stores/edgeDevice', () => ({
  useEdgeDeviceStore: () => ({ fetchDetail: mockFetchDetail }),
}))
vi.mock('@/api/edgeDevice', () => ({
  edgeDeviceApi: { getLatestData: mockGetLatestData },
}))
vi.mock('@/stores/websocket', () => ({
  useWebSocketStore: () => ({ connected: false, subscribe: vi.fn(() => vi.fn()) }),
}))
vi.mock('@/composables/useRealtimeData', () => ({
  useRealtimeData: () => ({
    dataItems: ref([]), latestData: ref(null), messageCount: ref(0), clear: vi.fn(),
  }),
}))
vi.mock('element-plus', () => ({ ElMessage: { success, error } }))
vi.mock('@/utils/logger', () => ({ logger: { error: vi.fn() } }))
vi.mock('@/api/homeassistant', () => ({ haApi: { syncDevice: mockSyncDevice } }))

describe('useDeviceData', () => {
  beforeEach(() => vi.clearAllMocks())

  it('does not let an older detail request overwrite the current device', async () => {
    let resolveOlder!: (value: any) => void
    let resolveNewer!: (value: any) => void
    mockFetchDetail
      .mockImplementationOnce(() => new Promise(resolve => { resolveOlder = resolve }))
      .mockImplementationOnce(() => new Promise(resolve => { resolveNewer = resolve }))
    const id = ref<number | null>(1)
    const state = useDeviceData(id)
    const older = state.fetchDeviceDetail()
    id.value = 2
    const newer = state.fetchDeviceDetail()
    resolveNewer({ id: 2, name: 'Newer' })
    await newer
    resolveOlder({ id: 1, name: 'Older' })
    await older
    expect(state.device.value?.id).toBe(2)
  })

  it('does not report refresh success when latest data fails', async () => {
    mockFetchDetail.mockResolvedValue({ id: 1, name: 'Device' })
    mockGetLatestData.mockRejectedValue(new Error('failed'))
    const state = useDeviceData(ref(1))
    await state.handleRefresh()
    expect(success).not.toHaveBeenCalledWith('数据已刷新')
    expect(error).toHaveBeenCalledWith('刷新失败')
  })

  it('does not let an old device HA sync own the new device result or loading', async () => {
    let resolve!: () => void
    mockSyncDevice.mockImplementationOnce(() => new Promise<void>(r => { resolve = r }))
    const id = ref<number | null>(1)
    const state = useDeviceData(id)
    const pending = state.handleSyncToHA()
    id.value = 2
    resolve()
    await pending
    expect(success).not.toHaveBeenCalledWith('设备已同步到HomeAssistant')
    expect(state.syncingHA.value).toBe(false)
  })
})