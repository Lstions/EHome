import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'
import { reactive } from 'vue'
import EdgeDeviceDetailRouter from '../EdgeDeviceDetailRouter.vue'

const { routeState, fetchDetail, messageError } = vi.hoisted(() => ({
  routeState: { params: { id: '1' } },
  fetchDetail: vi.fn(),
  messageError: vi.fn(),
}))
const route = reactive(routeState)

vi.mock('vue-router', () => ({ useRoute: () => route }))
vi.mock('@/stores/edgeDevice', () => ({
  useEdgeDeviceStore: () => ({ fetchDetail }),
}))
vi.mock('element-plus', () => ({ ElMessage: { error: messageError } }))

describe('EdgeDeviceDetailRouter', () => {
  beforeEach(() => {
    route.params.id = '1'
    vi.clearAllMocks()
  })

  it('keeps the newer route result when the older request finishes last', async () => {
    let resolveOlder!: (value: any) => void
    let resolveNewer!: (value: any) => void
    fetchDetail
      .mockImplementationOnce(() => new Promise(resolve => { resolveOlder = resolve }))
      .mockImplementationOnce(() => new Promise(resolve => { resolveNewer = resolve }))

    const wrapper = shallowMount(EdgeDeviceDetailRouter, {
      global: {
        stubs: {
          'el-card': true,
          'el-skeleton': true,
          'el-result': true,
          'el-button': true,
        },
      },
    })

    route.params.id = '2'
    await wrapper.vm.$nextTick()
    expect(fetchDetail).toHaveBeenNthCalledWith(1, 1, true)
    expect(fetchDetail).toHaveBeenNthCalledWith(2, 2, true)

    resolveNewer({ id: 2, device_type: 'inverter' })
    await flushPromises()
    const newerComponent = (wrapper.vm as any).targetComponent
    expect(newerComponent).toBeTruthy()
    expect((wrapper.vm as any).loading).toBe(false)

    resolveOlder({ id: 1, device_type: 'jiabaida_bms' })
    await flushPromises()
    expect((wrapper.vm as any).targetComponent).toBe(newerComponent)
    expect((wrapper.vm as any).deviceType).toBe('inverter')
    expect((wrapper.vm as any).loading).toBe(false)
    expect(messageError).not.toHaveBeenCalled()
  })

  it('keys the resolved detail page by route id', async () => {
    fetchDetail.mockResolvedValueOnce({ id: 1, device_type: 'inverter' })
    const wrapper = shallowMount(EdgeDeviceDetailRouter, { global: { stubs: { 'el-card': true, 'el-skeleton': true, 'el-result': true, 'el-button': true } } })
    await flushPromises()
    expect(wrapper.html()).toContain('data-detail-key="1"')
  })

  it('invalidates an in-flight request when route id becomes invalid', async () => {
    let resolve!: (value: any) => void
    fetchDetail.mockImplementationOnce(() => new Promise(r => { resolve = r }))
    const wrapper = shallowMount(EdgeDeviceDetailRouter, { global: { stubs: { 'el-card': true, 'el-skeleton': true, 'el-result': true, 'el-button': true } } })
    route.params.id = 'invalid'
    await wrapper.vm.$nextTick()
    expect((wrapper.vm as any).error).toBe(true)
    resolve({ id: 1, device_type: 'inverter' })
    await flushPromises()
    expect((wrapper.vm as any).targetComponent).toBeNull()
  })
})
