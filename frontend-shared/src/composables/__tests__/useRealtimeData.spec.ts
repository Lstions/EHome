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
})
