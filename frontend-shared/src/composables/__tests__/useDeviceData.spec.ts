import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import { useDeviceData } from '../useDeviceData'

const { mockFetchDetail, mockGetLatestValues, mockSyncDevice, success, error, message } = vi.hoisted(() => ({
  mockFetchDetail: vi.fn(),
  mockGetLatestValues: vi.fn(),
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
  // 2026-11-11 原始数据退役：getLatestData 已删，composable 只调 /latest-values
  edgeDeviceApi: { getLatestValues: mockGetLatestValues },
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
    mockGetLatestValues.mockRejectedValue(new Error('failed'))
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

  // ── 2026-11-11 原始数据退役：快照来源只剩 /latest-values ──
  describe('fetchLatestData（/latest-values 单一路径）', () => {
    it('把「每传感器一行」的快照铺成 latestData 的字段字典', async () => {
      mockFetchDetail.mockResolvedValue({ id: 1, name: 'Device' })
      mockGetLatestValues.mockResolvedValue([
        { sensor_name: 'total_voltage', value: 52.4, unit: 'V', timestamp: '2026-11-11T06:00:00Z' },
        { sensor_name: 'rsoc', value: 87, unit: '%', timestamp: '2026-11-11T06:00:00Z' },
      ])
      const state = useDeviceData(ref(1))
      await state.fetchLatestData()
      expect(mockGetLatestValues).toHaveBeenCalledWith(1)
      expect(state.latestData.value).toEqual({ total_voltage: 52.4, rsoc: 87 })
    })

    it('快照是**合并**不是覆盖：WS 已推来的、快照里没有的字段必须保留', async () => {
      // 快照是几秒前的点，WS 才是最新。直接赋值会把 WS 的新值抹掉（回退成旧值）。
      mockFetchDetail.mockResolvedValue({ id: 1, name: 'Device' })
      mockGetLatestValues.mockResolvedValue([
        { sensor_name: 'total_voltage', value: 52.4, unit: 'V', timestamp: '2026-11-11T06:00:00Z' },
      ])
      const state = useDeviceData(ref(1))
      await state.fetchLatestData()
      state.latestData.value = { ...(state.latestData.value ?? {}), current: 3.1 }
      await state.fetchLatestData()
      expect(state.latestData.value).toEqual({ total_voltage: 52.4, current: 3.1 })
    })

    it('实时数据流**只由 WebSocket 填充**：快照不再往 dataItems 里塞历史项', async () => {
      // 改前会把快照包装成一条 isRealtime:false 的 item 塞进流里 —— 那是
      // "现在每个字段是多少"被伪装成"刚收到了一帧"，用户会看到一条从未发生的记录。
      // 端点删除后这个语义混装一并结束，本用例是防它被加回来的回归锁。
      mockFetchDetail.mockResolvedValue({ id: 1, name: 'Device' })
      mockGetLatestValues.mockResolvedValue([
        { sensor_name: 'total_voltage', value: 52.4, unit: 'V', timestamp: '2026-11-11T06:00:00Z' },
      ])
      const state = useDeviceData(ref(1))
      await state.fetchLatestData()
      expect(state.realtimeDataItems.value).toEqual([])
    })

    it('快照为空数组时不改 latestData（空=还没预热，不是"清零"）', async () => {
      mockFetchDetail.mockResolvedValue({ id: 1, name: 'Device' })
      mockGetLatestValues.mockResolvedValue([])
      const state = useDeviceData(ref(1))
      state.latestData.value = { total_voltage: 52.4 }
      await state.fetchLatestData()
      expect(state.latestData.value).toEqual({ total_voltage: 52.4 })
    })
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