import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import { useDeviceData } from '../useDeviceData'

const { mockFetchDetail, mockGetLatestData, mockSyncDevice, success, error, message } = vi.hoisted(() => ({
  mockFetchDetail: vi.fn(),
  mockGetLatestData: vi.fn(),
  mockSyncDevice: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  // feedback.error() 走 ElMessage({...}) 函数式调用（真实 EP 的 ElMessage 可调用）
  message: vi.fn(),
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
vi.mock('element-plus', () => ({ ElMessage: Object.assign(message, { success, error }) }))
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
    // I-1: 刷新失败走统一出口（5 秒错误停留）
    expect(message).toHaveBeenCalledWith(expect.objectContaining({
      message: '刷新失败',
      type: 'error',
      duration: 5000,
    }))
  })

  it('I-1: 同步到 HA 失败时展示服务端 message 而非拼接文案', async () => {
    mockSyncDevice.mockRejectedValue(
      Object.assign(new Error('Request failed with status code 400'), {
        response: { data: { message: 'HA 未配置正确的访问令牌' } },
      }),
    )
    const state = useDeviceData(ref(1))

    await state.handleSyncToHA()

    // 关键判据：后端具体原因必须出现在用户看到的文案里（旧实现只显示"同步到HomeAssistant失败: ..."）
    expect(message).toHaveBeenCalledWith(expect.objectContaining({
      message: 'HA 未配置正确的访问令牌',
      type: 'error',
      duration: 5000,
    }))
  })

  it('I-1: 同步到 HA 失败且无服务端 message 时回落到前缀文案', async () => {
    mockSyncDevice.mockRejectedValue(new Error(''))
    const state = useDeviceData(ref(1))

    await state.handleSyncToHA()

    expect(message).toHaveBeenCalledWith(expect.objectContaining({
      message: '同步到HomeAssistant失败',
    }))
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