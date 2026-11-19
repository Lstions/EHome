import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
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
vi.mock('element-plus', () => ({ ElMessage: Object.assign(vi.fn(), { error: vi.fn() }) }))

/*
 * 三个详情页必须 stub 掉（不是「懒得测」，而是本 spec 的边界）。
 *
 * 本文件断言的是**路由壳**的解析逻辑：route.id → fetchDetail → device_type → 选哪个页面
 * + 竞态序号 + keyed 渲染。页面本体各有独立 spec（InverterDetailPage.spec.ts、
 * EdgeDeviceDetail.spec.ts 等），这里加载真实页面既测不到页面行为，又会引入一条
 * 谁都不 await 的 import 链：
 *
 *   EdgeDeviceDetailRouter.vue --defineAsyncComponent--> InverterDetailPage.vue
 *     -> DeviceHeader.vue -> api/edgeDevice.ts -> api/edgeDevice/list.ts
 *     -> api/client.ts -> stores/appUpdate.ts
 *
 * Vite 的模块图是**逐跳异步 transform**（每跳都要读/编译一个模块）。2026-11-19 本地实测：
 * 旧 spec 的 3 条用例在文件开始后 **21ms** 就全部跑完，而这条 import 链冷启动要 **1835ms**
 * 才 settle ⇒ 文件结束时链路必然还在飞。worker 随后 teardown，链路下一跳再向模块 runner
 * 要文件就抛 EnvironmentTeardownError（CI 日志原文：`Cannot load '/src/stores/appUpdate.ts'
 * imported from .../api/client.ts after the environment was torn down`）；Vue 在
 * AsyncComponentWrapper 的 onError 里报 “[Vue warn]: Unhandled error during execution of
 * async component loader”，vitest 记为 Unhandled Rejection ⇒ 2754 条用例全绿但退出码非 0。
 * 这正是 CI run 44467598362 挂掉的形态。
 *
 * stub 之后异步组件在下一轮 microtask 内 resolve；再配合下面每条用例显式 await 到
 * 「页面真的渲染出来」+ afterEach 卸载并 flush，文件结束时不再有在途 import。
 */
vi.mock('../bms/BmsDetailPage.vue', () => ({
  // __esModule 必须显式给出：Vue 的 async loader 靠它把 import() 的命名空间解包成 .default。
  __esModule: true,
  default: { name: 'BmsDetailPageStub', template: '<div class="page-bms" />' },
}))
vi.mock('../inverter/InverterDetailPage.vue', () => ({
  __esModule: true,
  default: { name: 'InverterDetailPageStub', template: '<div class="page-inverter" />' },
}))
vi.mock('../GenericDeviceDetail.vue', () => ({
  __esModule: true,
  default: { name: 'GenericDeviceDetailStub', template: '<div class="page-generic" />' },
}))

describe('EdgeDeviceDetailRouter', () => {
  let wrapper: VueWrapper | undefined

  beforeEach(() => {
    route.params.id = '1'
    vi.clearAllMocks()
  })

  afterEach(async () => {
    // 卸载会清掉 defineAsyncComponent 的 200ms delay timer 与在途渲染；
    // 再 flush 一次，确保没有悬挂任务漏到下一个用例或文件级 teardown。
    wrapper?.unmount()
    wrapper = undefined
    await flushPromises()
  })

  it('loads the current route id and reloads when the route changes', async () => {
    fetchDetail.mockResolvedValue({ id: 1, device_type: 'inverter' })
    wrapper = mount(EdgeDeviceDetailRouter)
    await flushPromises()
    expect(fetchDetail).toHaveBeenCalledWith(1, true)
    // 等异步页面真的加载出来（而不是只等 fetchDetail）：这就是「import 链已 settle」的断言。
    await vi.waitFor(() => expect(wrapper!.find('.page-inverter').exists()).toBe(true))

    route.params.id = '2'
    await flushPromises()
    expect(fetchDetail).toHaveBeenLastCalledWith(2, true)
    // key 跟随路由 id：旧页面被新的 keyed 页面替换。
    expect(wrapper.find('[data-detail-key="2"]').exists()).toBe(true)
    expect(wrapper.find('.page-inverter').exists()).toBe(true)
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
