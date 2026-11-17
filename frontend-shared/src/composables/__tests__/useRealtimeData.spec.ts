import { describe, it, expect, beforeEach, vi } from 'vitest'
import { defineComponent, ref } from 'vue'
import { mount, flushPromises } from '@vue/test-utils'
import { useRealtimeData } from '@/composables/useRealtimeData'

// Capture the WS handlers registered by the composable so the test can drive
// canonical data_update frames without a real socket.
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

vi.mock('@/stores/websocket', () => ({
  useWebSocketStore: () => ({ subscribe }),
}))

// useRealtimeData registers onMounted/onUnmounted, so it needs a component scope.
function mountHarness() {
  let state!: ReturnType<typeof useRealtimeData>
  const Harness = defineComponent({
    setup() {
      state = useRealtimeData({ deviceId: ref(42) })
      return () => null
    },
  })
  mount(Harness)
  return state
}

describe('useRealtimeData canonical WS field names', () => {
  beforeEach(() => {
    for (const key of Object.keys(handlers)) delete handlers[key]
    subscribe.mockClear()
  })

  it('accepts a data_update keyed by edge_device_id', async () => {
    const state = mountHarness()
    expect(typeof handlers['data_update']).toBe('function')

    handlers['data_update']({
      type: 'data_update',
      payload: {
        edge_device_id: 42,
        data: { temperature: 21 },
        collected_at: '2024-01-01T00:00:00Z',
      },
    })
    await flushPromises()

    expect(state.messageCount.value).toBe(1)
    expect(state.dataItems.value).toHaveLength(1)
    expect(state.dataItems.value[0].data).toEqual({ temperature: 21 })
  })

  it('does not match the removed legacy device_id alias', async () => {
    const state = mountHarness()
    handlers['data_update']({
      type: 'data_update',
      payload: { device_id: 42, data: { temperature: 21 } },
    })
    await flushPromises()

    expect(state.messageCount.value).toBe(0)
  })

  /**
   * 2026-11-17：fieldAt 必须是**按字段**的时刻，不能是"最新一帧"的时刻。
   * BMS 的 MOS 卡用 fieldAt.fet_status 判断"手里的 MOS 状态有多新"；
   * 若一帧不含 fet_status 的帧（0xAA 保护次数 / 0x04 单体电压）也算数，
   * 刚被写后回读确认的开关状态会被打回旧值 —— 正是真机报障的"跳一下"。
   */
  it('fieldAt 只记录该帧真正携带的字段', async () => {
    const state = mountHarness()

    handlers['data_update']({
      type: 'data_update',
      payload: { edge_device_id: 42, data: { fet_status: 2 }, collected_at: '2024-01-01T00:00:10Z' },
    })
    await flushPromises()
    expect(state.fieldAt.value.fet_status).toBe(Date.parse('2024-01-01T00:00:10Z'))

    // 更晚的一帧，但不含 fet_status：不得推进 fet_status 的时刻
    handlers['data_update']({
      type: 'data_update',
      payload: { edge_device_id: 42, data: { protection_status: 0 }, collected_at: '2024-01-01T00:00:20Z' },
    })
    await flushPromises()
    expect(state.fieldAt.value.protection_status).toBe(Date.parse('2024-01-01T00:00:20Z'))
    expect(state.fieldAt.value.fet_status).toBe(Date.parse('2024-01-01T00:00:10Z'))

    // 乱序到达的旧帧不得把时刻拉回去（单调递增）
    handlers['data_update']({
      type: 'data_update',
      payload: { edge_device_id: 42, data: { fet_status: 3 }, collected_at: '2024-01-01T00:00:05Z' },
    })
    await flushPromises()
    expect(state.fieldAt.value.fet_status).toBe(Date.parse('2024-01-01T00:00:10Z'))
  })
})
