import { describe, expect, it, beforeEach, afterEach, vi } from 'vitest'
import { mount, flushPromises, enableAutoUnmount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { ref, nextTick } from 'vue'
import layoutSource from '@/views/layout/MainLayout.vue?raw'

/**
 * m-2（2026-09-22）—— 「登录已失效」提示必须在重新登录时被显式关闭。
 *
 * 缺陷形态：该提示用 duration: 0（永不自动关闭），而 handleRelogin 只做
 * disconnect + router.push('/login')。若路由跳转不卸载 MainLayout，这条提示就
 * 留在登录页上：既不会自己消失，也没有任何可关闭入口 —— 一条无法消除的残留浮层。
 *
 * 覆盖：
 *   ① 点击提示 → close() 被调用 + 跳转登录页（改前只跳转、不关闭）；
 *   ② 用户点顶栏「重新登录」入口 → 同样 close()；
 *   ③ 路由进入 /login（无论从哪触发）→ 兜底关闭；
 *   ④ 组件卸载 → 关闭（挂 body 的浮层不随组件卸载消失）；
 *   ⑤ 反向自检：未失效时**不**弹、也不误关（不能"顺手改其它通知行为"）。
 */

// ⚠️ 替身必须是**响应式**的：真实 store/route 都是 reactive，而 watch 只能追踪
// 响应式依赖。用普通对象做替身会让 watcher 永不触发，用例会"红得毫无意义"
// （测的是替身不是接线）。这里沿用仓内既有的 require('vue') 模式建 ref。
// 通知配置的断言形状。必须显式标注：无参 `vi.fn(() => ...)` 会把 mock.calls 推成
// `[][]`（长度 0 的元组），随后读 `call[0].title` 会报 TS2493/TS2339（同
// MainLayoutRealtimeNotification.spec.ts 的既有教训）。
type NotificationOptionsLike = {
  title?: string
  message?: string
  duration?: number
  onClick?: () => void
}

const { mockPush, mockNotification, mockClose, routePath, authNonce } = vi.hoisted(() => {
  const { ref } = require('vue')
  const mockClose = vi.fn()
  const mockNotification = vi.fn<(options?: NotificationOptionsLike) => { close: () => void }>(() => ({ close: mockClose }))
  return {
    mockPush: vi.fn(),
    mockNotification,
    mockClose,
    routePath: ref('/dashboard'),
    authNonce: ref(0),
  }
})

vi.mock('vue-router', () => ({
  useRouter: () => ({ push: mockPush }),
  useRoute: () => ({ get path() { return routePath.value }, query: {} }),
}))
const { wsDisconnect } = vi.hoisted(() => ({ wsDisconnect: vi.fn() }))

vi.mock('@/stores/websocket', () => ({
  useWebSocketStore: () => ({
    connected: true,
    isAuthenticated: true,
    sessionInvalidated: false,
    isCurrentTokenInvalidated: () => false,
    get authInvalidationNonce() { return authNonce.value },
    reconnectWithFreshToken: vi.fn(),
    connect: vi.fn(),
    disconnect: wsDisconnect,
    subscribe: vi.fn(() => vi.fn()),
    onConnected: vi.fn(() => vi.fn()),
  }),
}))

vi.mock('@/stores/user', () => ({
  useUserStore: () => ({ token: 'tok', userInfo: { username: 'admin' }, logout: vi.fn(() => Promise.resolve()) }),
}))
vi.mock('@/stores/ui', () => ({ useUIStore: () => ({ sidebarCollapsed: false, toggleSidebar: vi.fn() }) }))
vi.mock('@/stores/node', () => ({ useNodeStore: () => ({ fetchNodes: vi.fn(() => Promise.resolve()) }) }))
vi.mock('@/stores/edgeDevice', () => ({ useEdgeDeviceStore: () => ({ fetchList: vi.fn(() => Promise.resolve()) }) }))
vi.mock('@/router/routeLoaders', () => ({ preloadPrimaryRoutes: vi.fn(() => Promise.resolve([])) }))
vi.mock('@/composables/useResponsive', () => ({
  useResponsive: () => ({ width: ref(1440), isMobile: ref(false), isTablet: ref(false), isDesktop: ref(true) }),
}))
vi.mock('@/utils/logger', () => ({ logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() } }))
vi.mock('@/utils/feedback', () => ({
  default: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn(), handleError: vi.fn(), confirmDanger: vi.fn(() => Promise.resolve(true)) },
}))
vi.mock('@/api/notification', () => ({
  getNotifications: vi.fn(() => Promise.resolve([])),
  getUnreadCount: vi.fn(() => Promise.resolve(0)),
  markAsRead: vi.fn(() => Promise.resolve()),
  markAllAsRead: vi.fn(() => Promise.resolve()),
}))
vi.mock('element-plus', () => ({
  ElMessage: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() }),
  ElNotification: mockNotification,
}))

import MainLayout from '@/views/layout/MainLayout.vue'

const stubs = {
  ThemeSwitch: { template: '<div />' },
  RouterView: { template: '<div class="router-view" />' },
  ElPopover: { template: '<div class="el-popover"><slot name="reference" /><slot /></div>' },
  ElBadge: { props: { value: [String, Number], hidden: Boolean }, template: '<span class="el-badge"><slot /></span>' },
}

const mountLayout = async () => {
  const wrapper = mount(MainLayout, { global: { stubs } })
  await flushPromises()
  return wrapper
}

/** 触发一次「登录已失效」（nonce 由 0 → 1 驱动 watcher 弹提示）。 */
const invalidateSession = async (wrapper: any) => {
  authNonce.value = 1
  await nextTick()
  await flushPromises()
  return wrapper
}

/** 最近一条「登录已失效」通知的配置（按标题过滤，不受其它通知影响）。 */
const sessionInvalidOptions = (): NotificationOptionsLike[] =>
  mockNotification.mock.calls
    .map(call => call[0])
    .filter((opts): opts is NotificationOptionsLike => opts?.title === '登录已失效')

enableAutoUnmount(afterEach)

describe('m-2 — 登录已失效提示的关闭路径', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    authNonce.value = 0
    routePath.value = '/dashboard'
  })

  it('失效时弹出不自动关闭的提示，并保存实例引用（否则没有任何关闭路径）', async () => {
    await invalidateSession(await mountLayout())

    const options = sessionInvalidOptions()
    expect(options, '未弹出「登录已失效」提示').toHaveLength(1)
    // 前提钉死：正因为它不自动关闭，才必须显式关闭。
    expect(options[0].duration).toBe(0)

    // 源码级引用断言：实例必须被保存下来，否则 handleRelogin 无从关闭。
    expect(layoutSource).toContain('sessionInvalidNotification = ElNotification(')
    expect(layoutSource).toContain('sessionInvalidNotification?.close()')
  })

  it('点击提示（handleRelogin）⇒ 关闭提示 + 断开 WS + 跳转登录页', async () => {
    // 改坏哪里会变红：删掉 handleRelogin 里的 closeSessionInvalidNotification()
    // 调用（或删掉 closeSessionInvalidNotification 本身，那会先编译失败 ⇒ 不算数）。
    await invalidateSession(await mountLayout())

    const options = sessionInvalidOptions()[0]
    // onClick 是可选属性：先断言它真的被接上了（否则"点击也能重登"这条路径根本没生效）
    expect(typeof options.onClick).toBe('function')
    options.onClick?.()
    await flushPromises()

    expect(mockClose, '点击提示后未关闭该提示：它会留在登录页上成为无法消除的残留').toHaveBeenCalledTimes(1)
    expect(wsDisconnect).toHaveBeenCalled()
    expect(mockPush).toHaveBeenCalledWith('/login')
  })

  it('顶栏「重新登录」入口（模板里的 handleRelogin）⇒ 同样关闭提示', async () => {
    // 这条覆盖另一条真实触发路径：用户不是点浮层，而是点徽标上的入口。
    const wrapper = await invalidateSession(await mountLayout())
    const vm = wrapper.vm as any
    vm.handleRelogin()
    await flushPromises()

    expect(mockClose).toHaveBeenCalledTimes(1)
    expect(mockPush).toHaveBeenCalledWith('/login')
  })

  it('路由进入 /login ⇒ 兜底关闭（跳转的触发点不止 handleRelogin 一处）', async () => {
    // 这里**不调用** handleRelogin：模拟"其它代码路径把路由打到登录页"
    // （401 拦截器、退出登录等），验证 watch(route.path) 兜底确实存在。
    await invalidateSession(await mountLayout())
    expect(mockClose).not.toHaveBeenCalled()

    routePath.value = '/login'
    await nextTick()
    await flushPromises()
    expect(mockClose, '进入登录页后未关闭残留提示：用户会看到一条无法消除的浮层').toHaveBeenCalled()

    // 反向：停留在受保护页面时不得关闭（否则用户刚看到的警告会莫名其妙消失）
    mockClose.mockClear()
    routePath.value = '/dashboard'
    await nextTick()
    expect(mockClose).not.toHaveBeenCalled()
  })

  it('组件卸载 ⇒ 关闭提示（浮层挂在 body 上，不随组件卸载消失）', async () => {
    const wrapper = await invalidateSession(await mountLayout())
    mockClose.mockClear()
    wrapper.unmount()
    await flushPromises()
    expect(mockClose).toHaveBeenCalledTimes(1)
  })

  it('反向自检：未失效时不弹提示、也不误关其它通知', async () => {
    const wrapper = await mountLayout()
    expect(sessionInvalidOptions()).toHaveLength(0)
    // 其它通知（如 pending_confirm 的 onClick 路径）不受本次改动影响
    expect(mockClose).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})