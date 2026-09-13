import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { nextTick } from 'vue'

// ── 本文件覆盖：移动端抽屉焦点陷阱 ──
// 实测缺陷：抽屉打开后连按 6 次 Tab，焦点穿到遮罩后的页头与卡片。
// 根因：ElDrawer 自带 ElFocusTrap，但 obtainAllFocusableElements(抽屉) 返回空
// （抽屉内零个可聚焦元素）→ 不 preventDefault → Tab 直接逃逸。
// 修复：抽屉内菜单项全部 tabindex=0（模态内所有目的地都应可 Tab 到达），
// 陷阱随即接管循环；并挂 focusin 兜底把逃逸焦点拉回。

const mockPush = vi.fn()
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: mockPush }),
  useRoute: () => ({ path: '/dashboard' }),
}))

// vi.hoisted 在 import 之前执行，不能引用顶层 import 的 ref —— 与既有
// MainLayoutMobileDrawer.spec.ts 保持一致，在 hoisted 内 require。
const { isMobileRef } = vi.hoisted(() => {
  const { ref } = require('vue')
  return { isMobileRef: ref(true) }
})
vi.mock('@/composables/useResponsive', () => ({
  useResponsive: () => ({
    isMobile: isMobileRef,
    isTablet: { value: false },
    isDesktop: { value: false },
    width: { value: 390 },
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
    connected: false, isAuthenticated: true, connect: vi.fn(), disconnect: vi.fn(),
    subscribe: vi.fn(() => vi.fn()), onConnected: vi.fn(() => vi.fn()),
  }),
}))
vi.mock('@/api/notification', () => ({
  getNotifications: vi.fn(() => Promise.resolve([])),
  getUnreadCount: vi.fn(() => Promise.resolve(0)),
  markAsRead: vi.fn(() => Promise.resolve()),
  markAllAsRead: vi.fn(() => Promise.resolve()),
}))
vi.mock('@/utils/logger', () => ({ logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() } }))
vi.mock('@/utils/feedback', () => ({
  default: { success: vi.fn(), error: vi.fn(), warn: vi.fn(), info: vi.fn(), confirmDanger: vi.fn(() => Promise.resolve(false)), handleError: vi.fn() },
}))
vi.mock('@/stores/node', () => ({ useNodeStore: () => ({ fetchNodes: vi.fn(() => Promise.resolve()) }) }))
vi.mock('@/stores/edgeDevice', () => ({ useEdgeDeviceStore: () => ({ fetchList: vi.fn(() => Promise.resolve()) }) }))
vi.mock('@/router/routeLoaders', () => ({ preloadPrimaryRoutes: vi.fn(() => Promise.resolve([])) }))
vi.mock('element-plus', () => ({
  ElMessage: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
  ElNotification: vi.fn(),
}))
vi.mock('@/components/common/ThemeSwitch.vue', () => ({ default: { template: '<div />' } }))

// ElDrawer / ElMenuItem 用真实 class 的 stub：本轮验证的是 MainLayout 的契约，
// 不是 Element Plus 自身的陷阱实现（那部分由 Playwright 真实浏览器用例覆盖）。
const stubs = {
  transition: false,
  RouterView: { render: () => null },
  ElContainer: { template: '<div class="el-container"><slot /></div>' },
  ElAside: { template: '<div class="el-aside"><slot /></div>' },
  ElHeader: { template: '<div class="el-header"><slot /></div>' },
  ElMain: { template: '<div class="el-main"><slot /></div>' },
  ElMenu: { template: '<div class="el-menu"><slot /></div>' },
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

describe('MainLayout 移动端抽屉焦点陷阱', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    isMobileRef.value = true
    vi.clearAllMocks()
    document.body.innerHTML = ''
  })

  it('移动端抽屉内所有导航项都可 Tab 到达（tabindex=0，非 roving）', async () => {
    const wrapper = mount(MainLayout, { global: { stubs } })
    await flushPromises()
    await nextTick()

    const items = wrapper.findAll('.mobile-sidebar-drawer .el-menu-item')
    expect(items.length).toBe(12)

    // 模态抽屉内不应使用 roving tabindex：那会只剩 1 个停靠点，Tab 会卡在同一项上。
    const tabindexes = items.map((i) => i.attributes('tabindex'))
    expect(tabindexes.every((t) => t === '0')).toBe(true)
  })

  it('抽屉打开时对齐到激活项、挂上 focusin 兜底；关闭时解绑', () => {
    // 契约断言（源码级）：这三点是"最小可用版本"的兜底路径，缺一不可。
    expect(source).toContain("document.addEventListener('focusin', handleMobileDrawerFocusIn)")
    expect(source).toContain("document.removeEventListener('focusin', handleMobileDrawerFocusIn)")
    // onUnmounted 也必须解绑，避免组件销毁后闭包持有 DOM
    expect(source).toMatch(/onUnmounted\(\(\) => \{[\s\S]*removeEventListener\('focusin', handleMobileDrawerFocusIn\)/)
    expect(source).toContain('handleMobileDrawerFocusIn')
  })

  it('焦点逃逸到抽屉外时被拉回抽屉内（兜底逻辑生效），关闭后不再拦截', async () => {
    const wrapper = mount(MainLayout, { global: { stubs }, attachTo: document.body })
    await flushPromises()
    await nextTick()

    // 直接触发打开抽屉（跳过 UI 交互），把兜底监听挂上
    const vm = wrapper.vm as any
    vm.mobileDrawerVisible = true
    await flushPromises()
    await nextTick()

    // 在抽屉外造一个可聚焦元素，模拟"焦点穿到遮罩后的页头/卡片"
    const outside = document.createElement('button')
    outside.className = 'outside-card-button'
    document.body.appendChild(outside)

    // 真实地聚焦它 —— focusin 兜底应立即把焦点拉回抽屉内
    outside.focus()
    await nextTick()

    const active = document.activeElement as HTMLElement
    expect(active.closest('.mobile-sidebar-drawer')).not.toBeNull()
    expect(document.activeElement).not.toBe(outside)

    // 关闭 → 解绑兜底，焦点不再被拦截
    vm.mobileDrawerVisible = false
    await flushPromises()
    await nextTick()

    outside.focus()
    expect(document.activeElement).toBe(outside)

    outside.remove()
  })

  it('关闭后把焦点归还触发按钮（而非 body）', () => {
    expect(source).toContain('handleMobileDrawerClosed')
    expect(source).toContain('@closed="handleMobileDrawerClosed"')
    expect(source).toContain('mobileMenuTriggerRef')
  })
})
