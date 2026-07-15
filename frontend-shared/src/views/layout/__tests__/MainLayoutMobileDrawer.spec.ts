import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { nextTick } from 'vue'

// Mock vue-router
const mockPush = vi.fn()
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: mockPush }),
  useRoute: () => ({ path: '/dashboard' }),
}))

// Mock useResponsive — default to desktop
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

// Mock stores
vi.mock('@/stores/user', () => ({
  useUserStore: () => ({
        userInfo: { username: 'TestUser', email: 'test@test.com' },
    logout: vi.fn(() => Promise.resolve()),
  }),
}))

vi.mock('@/stores/ui', () => ({
  useUIStore: () => ({
    sidebarCollapsed: false,
    toggleSidebar: vi.fn(),
  }),
}))

vi.mock('@/stores/websocket', () => ({
  useWebSocketStore: () => ({
    connected: false,
    isAuthenticated: true,
    connect: vi.fn(),
    disconnect: vi.fn(),
    subscribe: vi.fn(() => vi.fn()),
  }),
}))

// Mock API
vi.mock('@/api/notification', () => ({
  getNotifications: vi.fn(() => Promise.resolve([])),
  getUnreadCount: vi.fn(() => Promise.resolve(0)),
  markAsRead: vi.fn(() => Promise.resolve()),
  markAllAsRead: vi.fn(() => Promise.resolve()),
}))

// Mock logger
vi.mock('@/utils/logger', () => ({
  logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}))

// Mock feedback
vi.mock('@/utils/feedback', () => ({
  default: {
    success: vi.fn(),
    error: vi.fn(),
    warn: vi.fn(),
    info: vi.fn(),
    confirmDanger: vi.fn(() => Promise.resolve(false)),
    handleError: vi.fn(),
  },
}))

// Mock ThemeSwitch component
vi.mock('@/components/common/ThemeSwitch.vue', () => ({
  default: { template: '<div class="theme-switch-stub" />' },
}))

// Common stubs for all tests — router-view must render empty (no route matched in tests)
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

// Import after mocks are set up
import MainLayout from '@/views/layout/MainLayout.vue'

describe('MainLayout', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    isMobileRef.value = false
    vi.clearAllMocks()
  })

  it('桌面端渲染侧边栏 (.sidebar) 和菜单项', async () => {
    const wrapper = mount(MainLayout, { global: { stubs } })
    await flushPromises()

    // 桌面端应该有 .sidebar
    const sidebar = wrapper.find('.sidebar')
    expect(sidebar.exists()).toBe(true)

    // 桌面端不应该有 el-drawer
    expect(wrapper.find('.mobile-sidebar-drawer').exists()).toBe(false)

    // 菜单项存在
    const menuItems = wrapper.findAll('.sidebar .el-menu-item')
    expect(menuItems.length).toBeGreaterThan(0)
  })

  it('移动端渲染抽屉 (.mobile-sidebar-drawer) 而非侧边栏', async () => {
    isMobileRef.value = true

    const wrapper = mount(MainLayout, { global: { stubs } })
    await flushPromises()
    await nextTick()

    // 移动端不应该有 .sidebar
    expect(wrapper.find('.sidebar').exists()).toBe(false)

    // 移动端应该有 mobile-sidebar-drawer
    const drawer = wrapper.find('.mobile-sidebar-drawer')
    expect(drawer.exists()).toBe(true)

    // 抽屉内应该有菜单项
    const menuItems = drawer.findAll('.el-menu-item')
    expect(menuItems.length).toBeGreaterThan(0)
  })

  it('侧边栏菜单项包含预期路由', async () => {
    const wrapper = mount(MainLayout, { global: { stubs } })
    await flushPromises()

    const menuItems = wrapper.findAll('.sidebar .el-menu-item')
    const indexes = menuItems.map((el) => el.attributes('data-index'))
    expect(indexes).toContain('/dashboard')
    expect(indexes).toContain('/node')
    expect(indexes).toContain('/edge-device')
  })
})
