import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { defineComponent } from 'vue'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { deviceOperationApi, type DeviceOperation } from '@/api/deviceOperation'
import { useDeviceOperationStore } from '@/stores/deviceOperation'
import {
  OPERATION_FOLLOW_INTERVAL_MS,
  OPERATION_FOLLOW_MAX_ATTEMPTS,
  toDeviceOperation,
  useDeviceOperationFeed,
} from '@/composables/useDeviceOperationFeed'

/**
 * 2026-11-17 真机缺陷回归：受控操作回执必须能自己到达页面。
 *
 * 缺陷现象（用户报障）：BMS 页把 MOS 开关打开/关闭后，卡片上一直显示「已发送…」，
 * 开关也跟着抖；**必须手点页头「刷新数据」**才恢复。
 * 根因：本页只有 POST 返回值（QUEUED），既没订阅 WS device_operation_update，
 * 也没有任何轮询 ⇒ 终端状态与 verified_result（写后回读）永远到不了前端。
 *
 * 这里钉住三件事：
 *   ① WS 回执写入 store（并且容忍外壳/字符串 id 两种线上形态）；
 *   ② 兜底轮询只在非终态期间进行，到终态即停、到上限即止（不留长期定时器）；
 *   ③ 组件卸载后既不订阅也不轮询。
 */

const { handlers, subscribe } = vi.hoisted(() => {
  const handlers: Record<string, (message: any) => void> = {}
  const subscribe = vi.fn((type: string, handler: (message: any) => void) => {
    handlers[type] = handler
    return () => {
      delete handlers[type]
    }
  })
  return { handlers, subscribe }
})

vi.mock('@/stores/websocket', () => ({ useWebSocketStore: () => ({ subscribe }) }))

function makeOp(overrides: Partial<DeviceOperation> = {}): DeviceOperation {
  return {
    command_id: 'cmd-1',
    edge_device_id: 7,
    node_id: 'F0F5BD02DD84',
    action_id: 'set_mos_policy',
    action_version: 1,
    status: 'QUEUED',
    created_at: '2026-11-17T10:00:00Z',
    updated_at: '2026-11-17T10:00:00Z',
    ...overrides,
  } as DeviceOperation
}

function mountFeed() {
  let feed!: ReturnType<typeof useDeviceOperationFeed>
  const Harness = defineComponent({
    setup() {
      feed = useDeviceOperationFeed()
      return () => null
    },
  })
  const wrapper = mount(Harness)
  return { feed, wrapper }
}

describe('toDeviceOperation 规整（不信任 WS payload）', () => {
  it('接受 {execution} 外壳，并把字符串 edge_device_id 归一成 number', () => {
    const op = toDeviceOperation({ execution: { command_id: 'c', edge_device_id: '7', status: 'QUEUED' } })
    expect(op?.edge_device_id).toBe(7)
    expect(op?.command_id).toBe('c')
  })

  it('缺 command_id / edge_device_id / status 的一律丢弃', () => {
    expect(toDeviceOperation(null)).toBeNull()
    expect(toDeviceOperation({})).toBeNull()
    expect(toDeviceOperation({ edge_device_id: 7, status: 'QUEUED' })).toBeNull()
    expect(toDeviceOperation({ command_id: 'c', status: 'QUEUED' })).toBeNull()
    expect(toDeviceOperation({ command_id: 'c', edge_device_id: 0, status: 'QUEUED' })).toBeNull()
    expect(toDeviceOperation({ command_id: 'c', edge_device_id: 7 })).toBeNull()
  })
})

describe('useDeviceOperationFeed', () => {
  let store: ReturnType<typeof useDeviceOperationStore>

  beforeEach(() => {
    setActivePinia(createPinia())
    store = useDeviceOperationStore()
    for (const key of Object.keys(handlers)) delete handlers[key]
    subscribe.mockClear()
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('订阅 device_operation_update 并把回执写入 store', () => {
    mountFeed()
    expect(subscribe).toHaveBeenCalledTimes(1)
    expect(typeof handlers['device_operation_update']).toBe('function')

    handlers['device_operation_update']({ type: 'device_operation_update', payload: makeOp({ status: 'SUCCEEDED' }) })

    const list = store.histories.get(7) ?? []
    expect(list).toHaveLength(1)
    expect(list[0].status).toBe('SUCCEEDED')
  })

  it('丢弃残缺 payload（不把半条记录写进 store）', () => {
    mountFeed()
    handlers['device_operation_update']({ type: 'device_operation_update', payload: { edge_device_id: 7 } })
    expect(store.histories.get(7)).toBeUndefined()
  })

  it('兜底轮询：非终态继续，终态即停（不再发请求）', async () => {
    vi.useFakeTimers()
    const get = vi.spyOn(deviceOperationApi, 'get')
      .mockResolvedValueOnce(makeOp({ status: 'DISPATCHED' }))
      .mockResolvedValueOnce(makeOp({ status: 'SUCCEEDED' }))
    const { feed } = mountFeed()

    feed.startFollow('cmd-1')
    await vi.advanceTimersByTimeAsync(OPERATION_FOLLOW_INTERVAL_MS)
    expect(get).toHaveBeenCalledTimes(1)
    expect(store.histories.get(7)?.[0].status).toBe('DISPATCHED')

    await vi.advanceTimersByTimeAsync(OPERATION_FOLLOW_INTERVAL_MS)
    expect(get).toHaveBeenCalledTimes(2)
    expect(store.histories.get(7)?.[0].status).toBe('SUCCEEDED')

    await vi.advanceTimersByTimeAsync(OPERATION_FOLLOW_INTERVAL_MS * 5)
    expect(get).toHaveBeenCalledTimes(2)
  })

  it('兜底轮询有界：最多 OPERATION_FOLLOW_MAX_ATTEMPTS 次，到点即止', async () => {
    vi.useFakeTimers()
    const get = vi.spyOn(deviceOperationApi, 'get').mockResolvedValue(makeOp({ status: 'QUEUED' }))
    const { feed } = mountFeed()

    feed.startFollow('cmd-1')
    await vi.advanceTimersByTimeAsync(OPERATION_FOLLOW_INTERVAL_MS * (OPERATION_FOLLOW_MAX_ATTEMPTS + 5))
    expect(get).toHaveBeenCalledTimes(OPERATION_FOLLOW_MAX_ATTEMPTS)
  })

  it('WS 已推到终态后不再发兜底请求', async () => {
    vi.useFakeTimers()
    const get = vi.spyOn(deviceOperationApi, 'get')
    const { feed } = mountFeed()

    feed.startFollow('cmd-1')
    handlers['device_operation_update']({ type: 'device_operation_update', payload: makeOp({ status: 'FAILED' }) })
    await vi.advanceTimersByTimeAsync(OPERATION_FOLLOW_INTERVAL_MS * 3)
    expect(get).not.toHaveBeenCalled()
  })

  it('卸载后取消订阅并停掉兜底轮询', async () => {
    vi.useFakeTimers()
    const get = vi.spyOn(deviceOperationApi, 'get').mockResolvedValue(makeOp({ status: 'QUEUED' }))
    const { feed, wrapper } = mountFeed()
    feed.startFollow('cmd-1')
    wrapper.unmount()
    await vi.advanceTimersByTimeAsync(OPERATION_FOLLOW_INTERVAL_MS * 3)
    expect(get).not.toHaveBeenCalled()
    expect(handlers['device_operation_update']).toBeUndefined()
  })
})
