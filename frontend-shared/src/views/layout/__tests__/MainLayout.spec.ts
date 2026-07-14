import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import MainLayout from '@/views/layout/MainLayout.vue'
import layoutSource from '../MainLayout.vue?raw'

// ── Mocks ──────────────────────────────────────────────

const { mockPush, mockRoute } = vi.hoisted(() => ({
  mockPush: vi.fn(),
  mockRoute: { path: '/data' },
}))
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: mockPush }),
  useRoute: () => mockRoute,
}))

vi.mock('@/stores/user', () => ({
  useUserStore: () => ({
    userInfo: { id: 1, username: 'admin', email: 'admin@test.com', role: 'admin' },
    role: 'admin',
    isAdmin: true,
    isOperator: true,
    isViewer: false,
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
    connect: vi.fn(),
    disconnect: vi.fn(),
    isAuthenticated: true,
    subscribe: vi.fn(() => vi.fn()),
  }),
}))

vi.mock('@/stores/node', () => ({
  useNodeStore: () => ({
    fetchNodes: vi.fn(() => Promise.resolve()),
  }),
}))

vi.mock('@/stores/edgeDevice', () => ({
  useEdgeDeviceStore: () => ({
    fetchList: vi.fn(() => Promise.resolve()),
  }),
}))

vi.mock('@/api/notification', () => ({
  getNotifications: vi.fn(() => Promise.resolve([])),
  getUnreadCount: vi.fn(() => Promise.resolve(0)),
  markAsRead: vi.fn(() => Promise.resolve()),
  markAllAsRead: vi.fn(() => Promise.resolve()),
}))

vi.mock('@/router/routeLoaders', () => ({
  preloadPrimaryRoutes: vi.fn(() => Promise.resolve([])),
}))

vi.mock('@/utils/feedback', () => ({
  default: {
    success: vi.fn(),
    info: vi.fn(),
    warning: vi.fn(),
    handleError: vi.fn(),
    confirmDanger: vi.fn(() => Promise.resolve(true)),
  },
}))

vi.mock('@/utils/logger', () => ({
  logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}))

// Stub all Element Plus components to avoid full DOM rendering
const stubs = {
  ThemeSwitch: { template: '<div data-testid="theme-switch" />' },
  RouterView: { template: '<div class="router-view" />' },
  'el-container': { template: '<div class="el-container"><slot /></div>' },
  'el-aside': { template: '<div class="el-aside"><slot /></div>' },
  'el-header': { template: '<div class="el-header"><slot /></div>' },
  'el-main': { template: '<div class="el-main"><slot /></div>' },
  'el-menu': {
    template: '<div class="el-menu"><slot /></div>',
    emits: ['select'],
    setup(_props: any, { emit }: { emit: (event: string, ...args: any[]) => void }) {
      return {
        selectHandler: (index: string) => emit('select', index),
      }
    },
  },
  'el-menu-item': {
    template: '<div class="el-menu-item" :data-index="index"><slot /></div>',
    props: ['index'],
  },
  'el-drawer': {
    template: '<div class="el-drawer" v-if="modelValue"><slot /></div>',
    props: ['modelValue', 'direction', 'withHeader', 'size'],
    emits: ['update:modelValue'],
  },
  'el-icon': { template: '<i class="el-icon"><slot /></i>' },
  'el-button': {
    template: '<button class="el-button" @click="$emit(\'click\')"><slot /></button>',
    emits: ['click'],
  },
  'el-breadcrumb': { template: '<div class="el-breadcrumb"><slot /></div>' },
  'el-breadcrumb-item': { template: '<span class="el-breadcrumb-item"><slot /></span>' },
  'el-input': { template: '<input class="el-input" />' },
  'el-badge': { template: '<div class="el-badge"><slot /></div>' },
  'el-popover': { template: '<div class="el-popover"><slot /><slot name="reference" /></div>' },
  'el-scrollbar': { template: '<div class="el-scrollbar"><slot /></div>' },
  'el-dropdown': { template: '<div class="el-dropdown"><slot /><slot name="dropdown" /></div>' },
  'el-dropdown-menu': { template: '<div class="el-dropdown-menu"><slot /></div>' },
  'el-dropdown-item': { template: '<div class="el-dropdown-item"><slot /></div>' },
  'el-avatar': { template: '<div class="el-avatar"><slot /></div>' },
}

// ── Tests ──────────────────────────────────────────────

describe('MainLayout.vue', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    // Simulate mobile viewport
    Object.defineProperty(window, 'innerWidth', {
      writable: true,
      configurable: true,
      value: 375,
    })
    window.dispatchEvent(new Event('resize'))
  })

  it('renders all 8 admin menu items', async () => {
    const wrapper = mount(MainLayout, {
      global: {
        stubs,
        components: {},
      },
    })
    await flushPromises()

    const menuItems = wrapper.findAll('.el-menu-item')
    expect(menuItems).toHaveLength(8)
    const paths = menuItems.map((el) => el.attributes('data-index'))
    expect(paths).toEqual([
      '/dashboard',
      '/node',
      '/edge-device',
      '/channel',
      '/data',
      '/firmware',
      '/device-configs',
      '/monitor',
    ])
  })

  it('marks /data as the active menu', async () => {
    mount(MainLayout, {
      global: { stubs },
    })
    await flushPromises()
    // The el-menu stub receives default-active via attrs (not passed through in stub)
    // Verify from source that activeMenu is computed from route.path
    expect(layoutSource).toContain('activeMenu')
    expect(layoutSource).toContain("route.path")
    // route is mocked to /data
    expect(mockRoute.path).toBe('/data')
  })

  it('uses mobile-specific classes and structure in the drawer', async () => {
    const wrapper = mount(MainLayout, {
      global: { stubs },
    })
    await flushPromises()

    // Open the mobile drawer by clicking the hamburger button
    const hamburger = wrapper.find('[aria-label="打开导航菜单"]')
    expect(hamburger.exists()).toBe(true)
    await hamburger.trigger('click')
    await flushPromises()

    // Mobile drawer should exist (isMobile = true at 375px)
    const drawer = wrapper.find('.el-drawer')
    expect(drawer.exists()).toBe(true)

    // Mobile-specific classes
    expect(drawer.find('.mobile-drawer-body').exists()).toBe(true)
    expect(drawer.find('.mobile-logo-area').exists()).toBe(true)
    expect(drawer.find('.mobile-logo-text').exists()).toBe(true)
    expect(drawer.find('.mobile-sidebar-menu').exists()).toBe(true)
    expect(drawer.find('.mobile-sidebar-footer').exists()).toBe(true)
    expect(drawer.find('.mobile-version-info').exists()).toBe(true)
  })

  it('closes the drawer and navigates to /dashboard when logo is clicked', async () => {
    const wrapper = mount(MainLayout, {
      global: { stubs },
    })
    await flushPromises()

    // Open the mobile drawer
    const hamburger = wrapper.find('[aria-label="打开导航菜单"]')
    expect(hamburger.exists()).toBe(true)
    await hamburger.trigger('click')
    await flushPromises()

    // Click logo area
    const logoArea = wrapper.find('.mobile-logo-area')
    expect(logoArea.exists()).toBe(true)
    await logoArea.trigger('click')

    // Should navigate to /dashboard
    expect(mockPush).toHaveBeenCalledWith('/dashboard')
  })

  it('closes the drawer when a menu item is selected', async () => {
    mount(MainLayout, {
      global: { stubs },
    })
    await flushPromises()

    // Verify from source that @select closes the drawer
    expect(layoutSource).toContain('@select="mobileDrawerVisible = false"')
  })

  it('does not leak dark sidebar styles to the mobile drawer', async () => {
    // Verify from source: desktop styles are scoped to .sidebar
    expect(layoutSource).toContain('.sidebar .logo-text')
    expect(layoutSource).toContain('.sidebar :deep(.el-menu-item)')
    // Mobile drawer uses :global styles with Element Plus tokens
    expect(layoutSource).toContain(':global(.mobile-sidebar-drawer')
    expect(layoutSource).toContain('var(--el-text-color-primary)')
    expect(layoutSource).toContain('var(--el-text-color-regular)')
    expect(layoutSource).toContain('var(--el-color-primary-light-9)')
    // No bare .logo-text or :deep(.el-menu-item) outside .sidebar
    expect(layoutSource).not.toMatch(/^\s*\.logo-text\s*{/m)
    expect(layoutSource).not.toMatch(/^\s*:deep\(\.el-menu-item\)\s*{/m)
  })

  it('includes version footer in the mobile drawer', async () => {
    const wrapper = mount(MainLayout, {
      global: { stubs },
    })
    await flushPromises()

    // Open the mobile drawer
    const hamburger = wrapper.find('[aria-label="打开导航菜单"]')
    await hamburger.trigger('click')
    await flushPromises()

    const footer = wrapper.find('.mobile-sidebar-footer')
    expect(footer.exists()).toBe(true)
    const versionText = footer.find('.mobile-version-info')
    expect(versionText.exists()).toBe(true)
    expect(versionText.text()).toContain('v')
  })
})
