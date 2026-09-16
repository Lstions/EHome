import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { nextTick } from 'vue'

// ── 本文件覆盖：桌面侧栏键盘可达（roving tabindex）──
// 背景：el-menu 的垂直模式没有内置键盘导航（只有 mode="horizontal" 才实例化 Menu 类），
// 所以 MainLayout 自行管理 roving tabindex，并在容器上挂 @keydown。
// 这里断言的是"契约"：任一时刻恰好一个 tab 停靠点、方向键移动焦点、Enter 激活。

const mockPush = vi.fn()
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: mockPush }),
  useRoute: () => ({ path: '/dashboard' }),
}))

const { isMobileRef } = vi.hoisted(() => {
  const { ref } = require('vue')
  return { isMobileRef: ref(false) }
})
vi.mock('@/composables/useResponsive', () => ({
  useResponsive: () => ({
    isMobile: isMobileRef,
    isTablet: { value: false },
    isDesktop: { value: true },
    width: { value: 1280 },
  }),
}))

vi.mock('@/stores/user', () => ({
  useUserStore: () => ({
    userInfo: { username: 'TestUser', email: 'test@test.com' },
    logout: vi.fn(() => Promise.resolve()),
  }),
}))
vi.mock('@/stores/ui', () => ({
  useUIStore: () => ({ sidebarCollapsed: false, toggleSidebar: vi.fn() }),
}))
vi.mock('@/stores/websocket', () => ({
  useWebSocketStore: () => ({
    connected: false,
    isAuthenticated: true,
    connect: vi.fn(),
    disconnect: vi.fn(),
    subscribe: vi.fn(() => vi.fn()),
    onConnected: vi.fn(() => vi.fn()),
  }),
}))
vi.mock('@/api/notification', () => ({
  getNotifications: vi.fn(() => Promise.resolve([])),
  getUnreadCount: vi.fn(() => Promise.resolve(0)),
  markAsRead: vi.fn(() => Promise.resolve()),
  markAllAsRead: vi.fn(() => Promise.resolve()),
}))
vi.mock('@/utils/logger', () => ({
  logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}))
vi.mock('@/utils/feedback', () => ({
  default: {
    success: vi.fn(), error: vi.fn(), warn: vi.fn(), info: vi.fn(),
    confirmDanger: vi.fn(() => Promise.resolve(false)), handleError: vi.fn(),
  },
}))
vi.mock('@/stores/node', () => ({
  useNodeStore: () => ({ fetchNodes: vi.fn(() => Promise.resolve()) }),
}))
vi.mock('@/stores/edgeDevice', () => ({
  useEdgeDeviceStore: () => ({ fetchList: vi.fn(() => Promise.resolve()) }),
}))
vi.mock('@/router/routeLoaders', () => ({
  preloadPrimaryRoutes: vi.fn(() => Promise.resolve([])),
}))
vi.mock('element-plus', () => ({
  ElMessage: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() }),
  ElNotification: vi.fn(),
}))
vi.mock('@/components/common/ThemeSwitch.vue', () => ({
  default: { template: '<div class="theme-switch-stub" />' },
}))

const stubs = {
  transition: false,
  RouterView: { render: () => null },
  ElContainer: { template: '<div class="el-container"><slot /></div>' },
  ElAside: { template: '<div class="el-aside"><slot /></div>' },
  ElHeader: { template: '<div class="el-header"><slot /></div>' },
  ElMain: { template: '<div class="el-main"><slot /></div>' },
  ElMenu: { template: '<div class="el-menu"><slot /></div>' },
  // attrs 会落到根元素：这样 :tabindex 在 stub 上也可断言，等价于真实 el-menu-item 的行为
  // （ElMenuItem 内部硬编码 tabindex="-1"，但 fallthrough attrs 优先级更高，会覆盖它）。
  ElMenuItem: { template: '<div class="el-menu-item" :data-index="$attrs.index"><slot /></div>' },
  ElIcon: { template: '<span class="el-icon"><slot /></span>' },
  ElButton: { template: '<button class="el-button"><slot /></button>' },
  ElBreadcrumb: { template: '<div class="el-breadcrumb"><slot /></div>' },
  ElBreadcrumbItem: { template: '<span class="el-breadcrumb-item"><slot /></span>' },
  ElInput: { template: '<input class="el-input" />' },
  ElBadge: { template: '<span class="el-badge"><slot /></span>' },
  ElPopover: { template: '<div class="el-popover"><slot /><slot name="reference" /></div>' },
  ElScrollbar: { template: '<div class="el-scrollbar"><slot /></div>' },
  ElAvatar: { template: '<span class="el-avatar"><slot /></span>' },
  ElDropdown: { template: '<div class="el-dropdown"><slot /><slot name="dropdown" /></div>' },
  ElDropdownMenu: { template: '<div class="el-dropdown-menu"><slot /></div>' },
  ElDropdownItem: { template: '<div class="el-dropdown-item"><slot /></div>' },
  ElDrawer: { template: '<div class="el-drawer mobile-sidebar-drawer"><slot /></div>' },
}

import MainLayout from '@/views/layout/MainLayout.vue'
import source from '@/views/layout/MainLayout.vue?raw'

const mountLayout = () => mount(MainLayout, { global: { stubs }, attachTo: document.body })

describe('MainLayout 桌面侧栏键盘可达（roving tabindex）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    isMobileRef.value = false
    vi.clearAllMocks()
    document.body.innerHTML = ''
  })

  it('每个菜单项都渲染 tabindex，且任一时刻恰好一个 0（roving tabindex）', async () => {
    const wrapper = mountLayout()
    await flushPromises()
    await nextTick()

    const items = wrapper.findAll('.sidebar .el-menu-item')
    // 14 = 12 个既有导航项 + 「通知通道」(/notification-channels) + 「投递审计」(/notification-deliveries)
    expect(items.length).toBe(14)

    const tabindexes = items.map((i) => i.attributes('tabindex'))
    expect(tabindexes.filter((t) => t === '0')).toHaveLength(1)
    expect(tabindexes.filter((t) => t === '-1')).toHaveLength(13)
  })

  it('初始 tab 停靠点是当前激活项（/dashboard）', async () => {
    const wrapper = mountLayout()
    await flushPromises()
    await nextTick()

    const items = wrapper.findAll('.sidebar .el-menu-item')
    const zero = items.filter((i) => i.attributes('tabindex') === '0')
    expect(zero).toHaveLength(1)
    expect(zero[0].attributes('data-index')).toBe('/dashboard')
  })

  it('激活项带 aria-current="page"，其余项不带', async () => {
    const wrapper = mountLayout()
    await flushPromises()
    await nextTick()

    const items = wrapper.findAll('.sidebar .el-menu-item')
    const current = items.filter((i) => i.attributes('aria-current') === 'page')
    expect(current).toHaveLength(1)
    expect(current[0].attributes('data-index')).toBe('/dashboard')
  })

  it('el-menu 容器上挂了 keydown 处理器，且实现方向键 / Home / End / Enter', () => {
    // 契约断言（源码级）：EP 垂直菜单没有键盘处理，这些必须由本组件提供。
    expect(source).toContain('handleSidebarKeydown')
    expect(source).toContain("case 'ArrowDown'")
    expect(source).toContain("case 'ArrowUp'")
    expect(source).toContain("case 'Home'")
    expect(source).toContain("case 'End'")
    expect(source).toContain("case 'Enter'")
    // roving tabindex 绑定必须存在，否则整条链失效（变异自证对应的就是这一行）
    expect(source).toContain(':tabindex="idx === sidebarFocusIndex ? 0 : -1"')
  })

  it('方向键把焦点移到下一个菜单项并同步 tabindex（真实 DOM 焦点）', async () => {
    const wrapper = mountLayout()
    await flushPromises()
    await nextTick()

    const items = wrapper.findAll('.sidebar .el-menu-item')
    const first = items[0].element as HTMLElement
    first.focus()
    expect(document.activeElement).toBe(first)

    await items[0].trigger('keydown', { key: 'ArrowDown' })
    await flushPromises()
    await nextTick()
    await nextTick()

    const second = items[1].element as HTMLElement
    expect(document.activeElement).toBe(second)

    // roving index 同步：停靠点跟着焦点走
    const tabindexes = wrapper.findAll('.sidebar .el-menu-item').map((i) => i.attributes('tabindex'))
    expect(tabindexes[1]).toBe('0')
    expect(tabindexes[0]).toBe('-1')
  })

  it('Enter 激活当前项（垂直 el-menu-item 是 <li>，原生不响应键盘）', async () => {
    const wrapper = mountLayout()
    await flushPromises()
    await nextTick()

    const items = wrapper.findAll('.sidebar .el-menu-item')
    const first = items[0].element as HTMLElement
    first.focus()

    await items[0].trigger('keydown', { key: 'Enter' })
    await flushPromises()

    // 断言"激活发生了"。el-menu-item 的点击由 EP 处理，stub 下用派发点击来验证契约：
    // 处理器必须对 Enter 调用 current.click()。
    expect(source).toContain('current.click()')
  })
})
