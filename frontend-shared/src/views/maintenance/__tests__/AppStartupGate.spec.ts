import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import type { MaintenanceSnapshot } from '@/types/system'
import { PROBE_GRACE_MS } from '../progress'

/**
 * App.vue 顶层启动门禁（契约 §4 / task C）。
 *
 * 这是本次改动**唯一可能把用户永久锁死**的地方，所以三条分支必须逐条钉住：
 *
 *   ① ready=true     → 渲染正常应用（router-view 存在），**且不得 reload**
 *                       （首次就 ready 也 reload 会造成每次打开都整页重载的自我循环）；
 *   ② ready=false    → 渲染维护页（替换 router-view），并按 2s 轮询；
 *                       后续 ready=true → 调 location.reload() 回到正常入口；
 *   ③ 请求失败        → **也必须渲染维护页的「连接中」态**并继续轮询，
 *                       绝不能直接展示正常页面（否则用户在维护期会撞满 503）。
 *
 * 另外两条兼容性断言与「三条分支」同等重要：
 *   ④ 后端不认识该接口（旧版 / 纯静态托管 / vite preview）→ fail-open 进正常页，
 *      否则既有 e2e 渲染契约用例会被永久锁在维护页；
 *   ⑤ ready 之后渲染的子树与改造前逐字节等价（ErrorBoundary > NetworkBanner >
 *      RouteProgressBar > router-view 顺序不变）。
 */

// ── mock：维护态接口 ──────────────────────────────────────────────────
const { getMaintenanceStatus, MaintenanceUnsupportedError } = vi.hoisted(() => {
  class MockUnsupported extends Error {
    constructor(message: string) {
      super(message)
      this.name = 'MaintenanceUnsupportedError'
    }
  }
  return { getMaintenanceStatus: vi.fn(), MaintenanceUnsupportedError: MockUnsupported }
})

vi.mock('@/api/system', () => ({
  getMaintenanceStatus,
  MaintenanceUnsupportedError,
  MAINTENANCE_TIMEOUT_MS: 5000,
}))

// ── mock：路由与外壳组件（本 spec 只关心门禁分支，不渲染真实外壳）──────────
vi.mock('vue-router', () => ({
  useRoute: () => ({ path: '/dashboard' }),
  RouterView: { name: 'RouterView', template: '<div class="router-view-stub" />' },
  RouterLink: { name: 'RouterLink', template: '<a><slot /></a>' },
}))

vi.mock('@/components/common/NetworkBanner.vue', () => ({
  default: { name: 'NetworkBanner', template: '<div class="network-banner-stub" />' },
}))
vi.mock('@/components/common/RouteProgressBar.vue', () => ({
  default: { name: 'RouteProgressBar', template: '<div class="route-progress-stub" />' },
}))
vi.mock('@/components/common/ErrorBoundary.vue', () => ({
  default: {
    name: 'ErrorBoundary',
    template: '<div class="error-boundary-stub"><slot /></div>',
  },
}))

import App from '@/App.vue'

/** 已知形状的快照工厂。 */
function makeSnapshot(patch: Partial<MaintenanceSnapshot> = {}): MaintenanceSnapshot {
  return {
    ready: false,
    failed: false,
    percent: 42,
    current_step: 'parent_indexes',
    current_label: '补齐父表索引',
    started_at: '2026-11-06T08:00:00Z',
    elapsed_ms: 3000,
    steps: [
      { id: 'db_connect', label: '连接数据库', weight: 3, state: 'done', progress: 100 },
      { id: 'parent_indexes', label: '补齐父表索引', weight: 25, state: 'running', progress: 20 },
    ],
    version: '2.0.0',
    build_commit: 'a0a983bf',
    ...patch,
  }
}

function mountApp() {
  setActivePinia(createPinia())
  return mount(App, {
    global: {
      stubs: {
        ElConfigProvider: { template: '<div class="el-config-provider"><slot /></div>', props: ['locale'] },
        // router-view 是 vue-router 的全局组件；本 spec 不装真实 router（只验证门禁分支），
        // 故显式 stub 成可断言的占位节点。
        RouterView: { name: 'RouterView', template: '<div class="router-view-stub" />' },
      },
    },
  })
}

describe('App 启动门禁：ready=true（正常路径不得回归）', () => {
  it('首次探测就 ready → 渲染正常应用，且**不**触发 reload', async () => {
    const reload = vi.fn()
    const originalLocation = window.location
    delete (window as unknown as { location?: Location }).location
    ;(window as unknown as Record<string, unknown>).location = { ...originalLocation, reload }

    getMaintenanceStatus.mockResolvedValue(makeSnapshot({ ready: true }))
    const wrapper = mountApp()
    await flushPromises()

    expect(wrapper.find('.router-view-stub').exists()).toBe(true)
    expect(wrapper.find('[data-testid="maintenance-page"]').exists()).toBe(false)
    expect(reload, '首次探测就 ready 时不得 reload（否则每次打开都整页重载）').not.toHaveBeenCalled()

    ;(window as unknown as Record<string, unknown>).location = originalLocation
    wrapper.unmount()
  })

  it('ready 后渲染结构保持改造前的顺序：ErrorBoundary > NetworkBanner > RouteProgressBar > router-view', async () => {
    getMaintenanceStatus.mockResolvedValue(makeSnapshot({ ready: true }))
    const wrapper = mountApp()
    await flushPromises()

    const boundary = wrapper.find('.error-boundary-stub')
    expect(boundary.exists()).toBe(true)
    const order = Array.from(boundary.element.children).map((el) => el.className)
    expect(order.slice(0, 3)).toEqual([
      'network-banner-stub',
      'route-progress-stub',
      'router-view-stub',
    ])
    wrapper.unmount()
  })

  it('首次探测期间（宽限期内）既不渲染维护页也不渲染业务路由（不闪「系统升级中」）', async () => {
    // 永不 resolve：模拟探测还在飞
    getMaintenanceStatus.mockReturnValue(new Promise(() => {}))
    const wrapper = mountApp()
    await flushPromises()

    expect(wrapper.find('[data-testid="maintenance-page"]').exists()).toBe(false)
    expect(wrapper.find('.router-view-stub').exists()).toBe(false)
    expect(wrapper.find('.startup-gate-pending').exists()).toBe(true)
    wrapper.unmount()
  })

  it('宽限期内必须给可见骨架（"空白 800ms"与闪一帧同样是体验缺陷）', async () => {
    getMaintenanceStatus.mockReturnValue(new Promise(() => {}))
    const wrapper = mountApp()
    await flushPromises()

    const skeleton = wrapper.find('.startup-gate-pending')
    expect(skeleton.exists()).toBe(true)
    // 骨架要有真实可见内容与无障碍语义，不能是个空 div
    expect(skeleton.text()).toContain('正在检查系统状态')
    expect(skeleton.attributes('role')).toBe('status')
    expect(skeleton.attributes('aria-live')).toBe('polite')
    wrapper.unmount()
  })

  it('宽限期到点仍未返回 → 切到维护页"连接中"态（不无限停留在骨架）', async () => {
    vi.useFakeTimers()
    getMaintenanceStatus.mockReturnValue(new Promise(() => {}))
    const wrapper = mountApp()
    await flushPromises()
    expect(wrapper.find('.startup-gate-pending').exists()).toBe(true)

    await vi.advanceTimersByTimeAsync(PROBE_GRACE_MS)
    await flushPromises()
    expect(wrapper.find('[data-testid="maintenance-page"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('正在连接服务器')

    vi.useRealTimers()
    wrapper.unmount()
  })
})

describe('App 启动门禁：ready=false（维护期）', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('渲染维护页并**替换** router-view（业务路由完全不参与）', async () => {
    getMaintenanceStatus.mockResolvedValue(makeSnapshot())
    const wrapper = mountApp()
    await flushPromises()

    expect(wrapper.find('[data-testid="maintenance-page"]').exists()).toBe(true)
    expect(wrapper.find('.router-view-stub').exists()).toBe(false)
    expect(wrapper.text()).toContain('系统升级中')
    wrapper.unmount()
  })

  it('维护页拿到快照：进度与当前阶段可见', async () => {
    getMaintenanceStatus.mockResolvedValue(makeSnapshot())
    const wrapper = mountApp()
    await flushPromises()

    expect(wrapper.find('[data-testid="maintenance-percent"]').text()).toBe('42%')
    expect(wrapper.text()).toContain('当前阶段：补齐父表索引')
    wrapper.unmount()
  })

  it('按 2s 轮询：未到 2s 不重复请求，到点后再次请求', async () => {
    getMaintenanceStatus.mockResolvedValue(makeSnapshot())
    const wrapper = mountApp()
    await flushPromises()
    expect(getMaintenanceStatus).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(1999)
    expect(getMaintenanceStatus).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(1)
    await flushPromises()
    expect(getMaintenanceStatus).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })

  it('轮询到 ready=true → 触发 location.reload() 回到正常入口', async () => {
    const reload = vi.fn()
    const originalLocation = window.location
    delete (window as unknown as { location?: Location }).location
    ;(window as unknown as Record<string, unknown>).location = { ...originalLocation, reload }

    getMaintenanceStatus
      .mockResolvedValueOnce(makeSnapshot({ ready: false }))
      .mockResolvedValueOnce(makeSnapshot({ ready: true }))

    const wrapper = mountApp()
    await flushPromises()
    expect(wrapper.find('[data-testid="maintenance-page"]').exists()).toBe(true)
    expect(reload).not.toHaveBeenCalled()

    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()
    expect(reload, '就绪后必须整页重载回正常入口').toHaveBeenCalledOnce()

    ;(window as unknown as Record<string, unknown>).location = originalLocation
    wrapper.unmount()
  })

  it('后端报告 failed=true → 维护页显示失败原因与「重新加载」入口', async () => {
    getMaintenanceStatus.mockResolvedValue(makeSnapshot({
      failed: true,
      percent: 12,
      steps: [
        { id: 'partition_unified_data', label: 'unified_data 分区迁移', weight: 25, state: 'failed', progress: 3, error: 'disk full' },
      ],
    }))
    const wrapper = mountApp()
    await flushPromises()

    expect(wrapper.text()).toContain('系统启动失败')
    expect(wrapper.text()).toContain('disk full')
    expect(wrapper.findAll('button').some((b) => b.text().includes('重新加载'))).toBe(true)
    wrapper.unmount()
  })
})

describe('App 启动门禁：请求失败（后端进程未监听）', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('请求失败时渲染维护页的「连接中」态，**不得**直接展示正常页面', async () => {
    getMaintenanceStatus.mockRejectedValue(new Error('Network Error'))
    const wrapper = mountApp()
    await flushPromises()

    expect(wrapper.find('[data-testid="maintenance-page"]').exists()).toBe(true)
    expect(wrapper.find('.router-view-stub').exists()).toBe(false)
    expect(wrapper.text()).toContain('无法连接服务器，正在重试')
    wrapper.unmount()
  })

  it('连续失败后仍继续轮询（不能因失败而停止），且间隔退避到 3s/5s 封顶', async () => {
    getMaintenanceStatus.mockRejectedValue(new Error('Network Error'))
    const wrapper = mountApp()
    await flushPromises()
    expect(getMaintenanceStatus).toHaveBeenCalledTimes(1)

    // 第 1 次失败 → 3s 后重试
    await vi.advanceTimersByTimeAsync(2999)
    expect(getMaintenanceStatus).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    await flushPromises()
    expect(getMaintenanceStatus).toHaveBeenCalledTimes(2)

    // 第 2 次失败起封顶 5s：4s 时还没发，5s 时才发
    await vi.advanceTimersByTimeAsync(4000)
    expect(getMaintenanceStatus).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(1000)
    await flushPromises()
    expect(getMaintenanceStatus).toHaveBeenCalledTimes(3)

    // 再走一轮，证明**永不停止**
    await vi.advanceTimersByTimeAsync(5000)
    await flushPromises()
    expect(getMaintenanceStatus).toHaveBeenCalledTimes(4)
    wrapper.unmount()
  })

  it('后端恢复后自动进入正常页面：失败若干次后 ready=true → reload（不停止轮询的自恢复）', async () => {
    const reload = vi.fn()
    const originalLocation = window.location
    delete (window as unknown as { location?: Location }).location
    ;(window as unknown as Record<string, unknown>).location = { ...originalLocation, reload }

    getMaintenanceStatus
      .mockRejectedValueOnce(new Error('Network Error'))
      .mockRejectedValueOnce(new Error('Network Error'))
      .mockResolvedValue(makeSnapshot({ ready: true }))

    const wrapper = mountApp()
    await flushPromises()
    expect(wrapper.text()).toContain('无法连接服务器')

    // 3s（第 1 次失败后）+ 5s（第 2 次失败后）→ 第 3 次拿到 ready
    await vi.advanceTimersByTimeAsync(3000)
    await flushPromises()
    await vi.advanceTimersByTimeAsync(5000)
    await flushPromises()
    expect(reload, '后端恢复后必须自动回到正常入口（用户无需手刷）').toHaveBeenCalledOnce()

    ;(window as unknown as Record<string, unknown>).location = originalLocation
    wrapper.unmount()
  })

  it('已有快照后连接中断 → 保留进度显示（真正原因不被「连接中」抹掉）', async () => {
    getMaintenanceStatus
      .mockResolvedValueOnce(makeSnapshot())
      .mockRejectedValue(new Error('Network Error'))
    const wrapper = mountApp()
    await flushPromises()
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()

    expect(wrapper.text()).toContain('无法连接服务器')
    expect(wrapper.find('[data-testid="maintenance-percent"]').text()).toBe('42%')
    wrapper.unmount()
  })
})

describe('App 启动门禁：向后兼容（fail-open）', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('后端不认识维护接口（旧版 / 纯静态托管）→ 直接进正常页面，不得锁死在维护页', async () => {
    getMaintenanceStatus.mockRejectedValue(new MaintenanceUnsupportedError('backend has no such route'))
    const wrapper = mountApp()
    await flushPromises()

    expect(wrapper.find('.router-view-stub').exists()).toBe(true)
    expect(wrapper.find('[data-testid="maintenance-page"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('维护接口返回 401/403 → 立即放行到正常页（后端已就绪，不是「启动中」）', async () => {
    // 推理链：维护期后端对业务请求统一回 503，而 /api/v1/system/maintenance 是免鉴权白名单
    // 的一员 —— 维护引擎不可能对它回 401/403。收到 401/403 说明请求已穿过维护引擎、
    // 打到了完整引擎的鉴权中间件 ⇒ 全量路由已注册完成 ⇒ 应该放行，不能把用户永久锁死。
    const unauthorized = () => Object.assign(new Error('Unauthorized'), { response: { status: 401 } })
    getMaintenanceStatus.mockRejectedValue(unauthorized())

    const wrapper = mountApp()
    await flushPromises()

    expect(wrapper.find('[data-testid="maintenance-page"]').exists()).toBe(false)
    expect(wrapper.find('.router-view-stub').exists()).toBe(true)
    wrapper.unmount()
  })

  it('放行后仍挂看门狗轮询：若后端之后重入维护态，要能重新显示维护页', async () => {
    getMaintenanceStatus
      .mockRejectedValueOnce(Object.assign(new Error('Forbidden'), { response: { status: 403 } }))
      .mockResolvedValue(makeSnapshot({ ready: false, percent: 7 }))

    const wrapper = mountApp()
    await flushPromises()
    expect(wrapper.find('.router-view-stub').exists()).toBe(true)

    // 看门狗用最慢档 5s
    await vi.advanceTimersByTimeAsync(5000)
    await flushPromises()
    expect(wrapper.find('[data-testid="maintenance-page"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="maintenance-percent"]').text()).toBe('7%')
    wrapper.unmount()
  })

  it('放行后看门狗期间的网络抖动不把用户拽回维护页（那是业务页错误横幅的职责）', async () => {
    getMaintenanceStatus
      .mockRejectedValueOnce(Object.assign(new Error('Forbidden'), { response: { status: 403 } }))
      .mockRejectedValue(new Error('Network Error'))

    const wrapper = mountApp()
    await flushPromises()
    expect(wrapper.find('.router-view-stub').exists()).toBe(true)

    await vi.advanceTimersByTimeAsync(5000)
    await flushPromises()
    expect(wrapper.find('.router-view-stub').exists()).toBe(true)
    expect(wrapper.find('[data-testid="maintenance-page"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('503 是维护期的正确信号：绝不能放行（放行会让用户撞满 503）', async () => {
    getMaintenanceStatus.mockRejectedValue(
      Object.assign(new Error('maintenance in progress'), { response: { status: 503 } }),
    )
    const wrapper = mountApp()
    await flushPromises()

    expect(wrapper.find('[data-testid="maintenance-page"]').exists()).toBe(true)
    expect(wrapper.find('.router-view-stub').exists()).toBe(false)
    wrapper.unmount()
  })
})
