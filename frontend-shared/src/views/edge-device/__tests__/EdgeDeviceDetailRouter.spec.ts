import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { reactive } from 'vue'
import EdgeDeviceDetailRouter from '../EdgeDeviceDetailRouter.vue'
import routerSource from '../EdgeDeviceDetailRouter.vue?raw'

const { routeState, fetchDetail } = vi.hoisted(() => ({
  routeState: { params: { id: '1' } },
  fetchDetail: vi.fn(),
}))
const route = reactive(routeState)

vi.mock('vue-router', () => ({ useRoute: () => route }))
vi.mock('@/stores/edgeDevice', () => ({
  useEdgeDeviceStore: () => ({ fetchDetail }),
}))
vi.mock('element-plus', () => ({ ElMessage: { error: vi.fn() } }))

describe('EdgeDeviceDetailRouter', () => {
  beforeEach(() => {
    route.params.id = '1'
    vi.clearAllMocks()
  })

  it('loads the current route id and reloads when the route changes', async () => {
    fetchDetail.mockResolvedValue({ id: 1, device_type: 'inverter' })
    const wrapper = mount(EdgeDeviceDetailRouter)
    await flushPromises()
    expect(fetchDetail).toHaveBeenCalledWith(1, true)

    route.params.id = '2'
    await wrapper.vm.$nextTick()
    expect(fetchDetail).toHaveBeenLastCalledWith(2, true)
  })

  it('guards resolution with a monotonically increasing sequence so stale responses cannot take over', () => {
    expect(routerSource).toContain('const sequence = ++resolveSequence')
    expect(routerSource).toContain('if (sequence !== resolveSequence || Number(route.params.id) !== id) return')
    expect(routerSource).toContain('if (sequence === resolveSequence) loading.value = false')
  })

  it('keys the resolved page by route id and rejects invalid ids before the request', () => {
    expect(routerSource).toContain(':key="route.params.id"')
    expect(routerSource).toContain(':data-detail-key="String(route.params.id)"')
    expect(routerSource).toContain('if (!id)')
    expect(routerSource).toContain('error.value = true')
  })
})
