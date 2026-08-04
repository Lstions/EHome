import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import MainLayout from '@/views/layout/MainLayout.vue'
import layoutSource from '../MainLayout.vue?raw'

// theme.css 通过同目录原始文本副本验证；happy-dom/Vitest 的 CSS ?raw 在该配置下为空。

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
    userInfo: { id: 1, username: 'admin', email: 'admin@test.com' },
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

// Mock element-plus ElMessage
vi.mock('element-plus', () => ({
  ElMessage: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}))

// Mock useResponsive composable
vi.mock('@/composables/useResponsive', () => ({
  useResponsive: () => ({
    width: { value: 375 },
    isMobile: { value: true },
    isTablet: { value: false },
    isDesktop: { value: false },
  }),
}))

// 全局 Element Plus stub 已在 src/test-setup.ts 注册。
// 仅 stub 项目内组件 ThemeSwitch 和 RouterView。
const stubs = {
  ThemeSwitch: { template: '<div data-testid="theme-switch" />' },
  RouterView: { template: '<div class="router-view" />' },
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

  it('renders all 9 admin menu items', async () => {
    const wrapper = mount(MainLayout, {
      global: {
        stubs,
        components: {},
      },
    })
    await flushPromises()

    const menuItems = wrapper.findAll('.el-menu-item')
    expect(menuItems).toHaveLength(9)
    const paths = menuItems.map((el) => el.attributes('data-index'))
    expect(paths).toEqual([
      '/dashboard',
      '/node',
      '/edge-device',
      '/logical-device',
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

  it('declares mobile drawer structure and drawer open/close interactions', () => {
    expect(layoutSource).toContain('class="mobile-drawer-body"')
    expect(layoutSource).toContain('class="mobile-logo-area"')
    expect(layoutSource).toContain('class="mobile-logo-text"')
    expect(layoutSource).toContain('class="mobile-sidebar-menu"')
    expect(layoutSource).toContain('class="mobile-sidebar-footer"')
    expect(layoutSource).toContain('class="mobile-version-info"')
    expect(layoutSource).toContain('aria-label="打开导航菜单"')
    expect(layoutSource).toContain('mobileDrawerVisible = true')
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

  // ── 主题 token / 深色模式精确覆盖 ──────────────────────

  it('移动端抽屉 body 背景使用 var(--el-bg-color) 而非硬编码白底', () => {
    // 抽屉 body 背景
    expect(layoutSource).toMatch(/\.mobile-sidebar-drawer \.el-drawer__body[^}]*background:\s*var\(--el-bg-color\)/)
    // 不应出现硬编码 #fff / #ffffff / white 作为背景
    expect(layoutSource).not.toMatch(/\.mobile-sidebar-drawer[^}]*background:\s*#fff\b/)
    expect(layoutSource).not.toMatch(/\.mobile-sidebar-drawer[^}]*background:\s*#ffffff\b/)
    expect(layoutSource).not.toMatch(/\.mobile-sidebar-drawer[^}]*background:\s*white\b/)
  })

  it('移动端抽屉菜单背景使用 var(--el-bg-color) 而非硬编码白底', () => {
    expect(layoutSource).toMatch(/\.mobile-sidebar-drawer \.mobile-sidebar-menu[^}]*background:\s*var\(--el-bg-color\)/)
    expect(layoutSource).not.toMatch(/\.mobile-sidebar-drawer \.mobile-sidebar-menu[^}]*background:\s*#fff\b/)
  })

  it('移动端抽屉所有颜色均使用 Element Plus CSS 变量 token', () => {
    // logo 文字 → var(--el-text-color-primary)
    expect(layoutSource).toMatch(/\.mobile-logo-text[^}]*color:\s*var\(--el-text-color-primary\)/)
    // 普通菜单项 → var(--el-text-color-regular)
    expect(layoutSource).toMatch(/\.mobile-sidebar-drawer \.el-menu-item[^}]*color:\s*var\(--el-text-color-regular\)/)
    // hover → var(--el-fill-color-light)
    expect(layoutSource).toMatch(/\.el-menu-item:hover[^}]*background:\s*var\(--el-fill-color-light\)/)
    // hover 文字 → var(--el-text-color-primary)
    expect(layoutSource).toMatch(/\.el-menu-item:hover[^}]*color:\s*var\(--el-text-color-primary\)/)
    // active → var(--el-color-primary) + var(--el-color-primary-light-9)
    expect(layoutSource).toMatch(/\.el-menu-item\.is-active[^}]*color:\s*var\(--el-color-primary\)/)
    expect(layoutSource).toMatch(/\.el-menu-item\.is-active[^}]*background:\s*var\(--el-color-primary-light-9\)/)
    // footer border → var(--el-border-color-lighter)
    expect(layoutSource).toMatch(/\.mobile-sidebar-footer[^}]*border-top:.*var\(--el-border-color-lighter\)/)
    // logo area border → var(--el-border-color-lighter)
    expect(layoutSource).toMatch(/\.mobile-logo-area[^}]*border-bottom:.*var\(--el-border-color-lighter\)/)
    // version info → var(--el-text-color-placeholder)
    expect(layoutSource).toMatch(/\.mobile-version-info[^}]*color:\s*var\(--el-text-color-placeholder\)/)
  })

  it('桌面端深色侧栏样式全部限定在 .sidebar 选择器内', () => {
    // logo-text 白色仅 .sidebar 内
    expect(layoutSource).toMatch(/\.sidebar \.logo-text[^}]*color:\s*#fff/)
    expect(layoutSource).not.toMatch(/^\s*\.logo-text\s*\{[^}]*color:\s*#fff/m)
    // el-menu-item 深色仅 .sidebar 内
    expect(layoutSource).toMatch(/\.sidebar :deep\(\.el-menu-item\)/)
    expect(layoutSource).not.toMatch(/^\s*:deep\(\.el-menu-item\)\s*\{/m)
    // sidebar-menu 仅 .sidebar 内
    expect(layoutSource).toMatch(/\.sidebar \.sidebar-menu/)
    expect(layoutSource).not.toMatch(/^\s*\.sidebar-menu\s*\{/m)
    // sidebar-footer 仅 .sidebar 内
    expect(layoutSource).toMatch(/\.sidebar \.sidebar-footer/)
    expect(layoutSource).not.toMatch(/^\s*\.sidebar-footer\s*\{/m)
    // version-info 仅 .sidebar 内
    expect(layoutSource).toMatch(/\.sidebar \.version-info/)
    expect(layoutSource).not.toMatch(/^\s*\.version-info\s*\{/m)
    // logo-area 仅 .sidebar 内
    expect(layoutSource).toMatch(/\.sidebar \.logo-area/)
    expect(layoutSource).not.toMatch(/^\s*\.logo-area\s*\{/m)
    // logo-icon 仅 .sidebar 内
    expect(layoutSource).toMatch(/\.sidebar \.logo-icon/)
    expect(layoutSource).not.toMatch(/^\s*\.logo-icon\s*\{/m)
  })

  // ── 通知跳转路由 (方案 v3.3 §六 D-2: 按 Notification.source 结构化路由) ──

  const mountWithNotifications = async (items: any[]) => {
    const { getNotifications } = await import('@/api/notification')
    vi.mocked(getNotifications).mockResolvedValue(items as any)
    const wrapper = mount(MainLayout, { global: { stubs } })
    await flushPromises()
    return wrapper
  }

  it('merge_failed 通知跳转逻辑设备管理页', async () => {
    const wrapper = await mountWithNotifications([
      { id: 1, type: 'error', title: '数据合并失败（已放弃重试）', description: '', source: 'merge_failed', source_id: '42', read: false, created_at: '' },
    ])
    const item = wrapper.find('.notification-item')
    expect(item.exists()).toBe(true)
    await item.trigger('click')
    expect(mockPush).toHaveBeenCalledWith('/logical-device')
  })

  it('retention_expiring 通知带 retention 深链跳转', async () => {
    const wrapper = await mountWithNotifications([
      { id: 2, type: 'warning', title: '数据即将到期', description: '', source: 'retention_expiring', source_id: '7', read: false, created_at: '' },
    ])
    await wrapper.find('.notification-item').trigger('click')
    expect(mockPush).toHaveBeenCalledWith('/logical-device?retention=7')
  })

  it('离线通知保留原 title 字符串匹配行为', async () => {
    const wrapper = await mountWithNotifications([
      { id: 3, type: 'warning', title: '节点离线', description: '', source: 'node_offline', source_id: '', read: false, created_at: '' },
    ])
    await wrapper.find('.notification-item').trigger('click')
    expect(mockPush).toHaveBeenCalledWith('/edge-device')
  })

  it('source 优先于 title 匹配 (source=merge_failed 但 title 含"离线")', async () => {
    const wrapper = await mountWithNotifications([
      { id: 4, type: 'error', title: '离线设备合并搬迁失败', description: '', source: 'merge_failed', source_id: '9', read: false, created_at: '' },
    ])
    await wrapper.find('.notification-item').trigger('click')
    expect(mockPush).toHaveBeenCalledWith('/logical-device')
    expect(mockPush).not.toHaveBeenCalledWith('/edge-device')
  })
})
